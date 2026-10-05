package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"novel-bot/internal/auth"
)

type KeyService interface {
	Authenticator
	Issue(context.Context, string, string, time.Duration) (auth.IssuedKey, error)
	List(context.Context, string) ([]auth.Key, error)
	Revoke(context.Context, string, string) error
}

type AccountManager interface {
	Register(context.Context, string, string, string) (auth.User, error)
	Login(context.Context, string, string) (auth.Login, error)
	AuthenticateSession(context.Context, string) (auth.AccountPrincipal, error)
	Logout(context.Context, auth.AccountPrincipal) error
}

func mountAccountRoutes(mux *http.ServeMux, keys KeyService, accounts AccountManager, logger *slog.Logger, timeout time.Duration) {
	h := accountHandlers{keys: keys, accounts: accounts, logger: logger}
	mux.Handle("/v1/auth/register", methodOnly("POST", operationTimeout(timeout, http.HandlerFunc(h.register))))
	mux.Handle("/v1/auth/login", methodOnly("POST", operationTimeout(timeout, http.HandlerFunc(h.login))))
	control := http.NewServeMux()
	control.Handle("/v1/auth/me", getOnly(h.me))
	control.Handle("/v1/auth/logout", methodOnly("POST", http.HandlerFunc(h.logout)))
	control.Handle("/v1/api-keys", http.HandlerFunc(h.keysCollection))
	control.Handle("/v1/api-keys/{id}", methodOnly("DELETE", http.HandlerFunc(h.revoke)))
	control.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, 404, "not_found", "Route not found") })
	protected := requireSession(accounts, logger, timeout)(operationTimeout(timeout, control))
	mux.Handle("/v1/auth/", protected)
	mux.Handle("/v1/api-keys", protected)
	mux.Handle("/v1/api-keys/", protected)
}

type accountHandlers struct {
	keys     KeyService
	accounts AccountManager
	logger   *slog.Logger
}

func (h accountHandlers) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := h.accounts.Register(r.Context(), body.Name, body.Email, body.Password)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user})
}

func (h accountHandlers) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	login, err := h.accounts.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, login)
}

func (h accountHandlers) me(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	writeJSON(w, http.StatusOK, map[string]any{"user": p.User})
}

func (h accountHandlers) logout(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	if err := h.accounts.Logout(r.Context(), p); err != nil {
		h.serviceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h accountHandlers) keysCollection(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		keys, err := h.keys.List(r.Context(), p.User.ClientID)
		if err != nil {
			h.serviceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	case http.MethodPost:
		var body struct {
			Name       string `json:"name"`
			TTLSeconds *int64 `json:"ttl_seconds"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		const maxTTLSeconds = int64(90 * 24 * 60 * 60)
		ttl := maxTTLSeconds
		if body.TTLSeconds != nil {
			ttl = *body.TTLSeconds
		}
		if ttl < 1 || ttl > maxTTLSeconds {
			writeError(w, r, 400, "invalid_input", "ttl_seconds must be between 1 and 7776000")
			return
		}
		issued, err := h.keys.Issue(r.Context(), p.User.ClientID, body.Name, time.Duration(ttl)*time.Second)
		if err != nil {
			h.serviceError(w, r, err)
			return
		}
		w.Header().Set("Location", "/v1/api-keys/"+issued.Key.ID)
		writeJSON(w, http.StatusCreated, issued)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeError(w, r, 405, "method_not_allowed", "Method not allowed")
	}
}

func (h accountHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	if err := h.keys.Revoke(r.Context(), p.User.ClientID, r.PathValue("id")); err != nil {
		h.serviceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h accountHandlers) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		writeError(w, r, 400, "invalid_input", err.Error())
	case errors.Is(err, auth.ErrConflict):
		writeError(w, r, 409, "email_registered", "Email already registered")
	case errors.Is(err, auth.ErrCredentials):
		writeError(w, r, 401, "invalid_credentials", "Invalid email or password")
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(w, r, 401, "unauthorized", "A valid login session is required")
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, 404, "not_found", "Resource not found")
	default:
		h.logger.ErrorContext(r.Context(), "account operation failed", "request_id", r.Context().Value(requestIDKey))
		writeError(w, r, 503, "service_unavailable", "Authentication service temporarily unavailable")
	}
}

func requireSession(accounts AccountManager, logger *slog.Logger, timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := credential(r.Header)
			// Account login tokens are accepted only through Authorization: Bearer.
			if !ok || len(r.Header.Values("Authorization")) != 1 {
				sessionUnauthorized(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			p, err := accounts.AuthenticateSession(ctx, token)
			cancel()
			if errors.Is(err, auth.ErrUnauthorized) {
				sessionUnauthorized(w, r)
				return
			}
			if err != nil {
				logger.ErrorContext(r.Context(), "session lookup failed", "request_id", r.Context().Value(requestIDKey))
				writeError(w, r, 503, "authentication_unavailable", "Authentication temporarily unavailable")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey, p)))
		})
	}
}

func sessionUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="account"`)
	writeError(w, r, 401, "unauthorized", "A valid login session is required")
}

func operationTimeout(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func methodOnly(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, r, 405, "method_not_allowed", "Method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, r, 415, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil {
		var extra any
		if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
			err = extraErr
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, 413, "body_too_large", "Request body exceeds 16 KiB")
		} else {
			writeError(w, r, 400, "invalid_json", "Provide one valid JSON object with supported fields")
		}
		return false
	}
	// null decodes into an empty struct and is rejected by domain validation.
	return true
}
