// Package httpapi owns REST routes and transport concerns.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"novel-bot/internal/auth"
)

type Authenticator interface {
	Authenticate(context.Context, string) (auth.Principal, error)
}

type contextKey int

const (
	principalKey contextKey = iota
	accountKey
	requestIDKey
)

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error     errorDetail `json:"error"`
	RequestID string      `json:"request_id,omitempty"`
}

func PrincipalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p, ok
}

// NewRouter separates account sessions for key management from application API keys.
func NewRouter(service KeyService, accounts AccountManager, ready func(context.Context) error, logger *slog.Logger, authTimeout time.Duration) http.Handler {
	api := http.NewServeMux()
	api.Handle("/v1/whoami", getOnly(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Authentication context missing")
			return
		}
		writeJSON(w, http.StatusOK, principal)
	}))
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "Route not found")
	})
	mux := http.NewServeMux()
	mountDocumentation(mux)
	if accounts != nil {
		mountAccountRoutes(mux, service, accounts, logger, authTimeout)
	}
	mux.Handle("/healthz", getOnly(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.Handle("/readyz", getOnly(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_ready", "Database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}))
	mux.Handle("/v1/", RequireAPIKey(service, logger, authTimeout)(api))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "Route not found")
	})
	return requestMetadata(recoverPanic(mux, logger))
}

func RequireAPIKey(service Authenticator, logger *slog.Logger, timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := credential(r.Header)
			if !ok {
				unauthorized(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			principal, err := service.Authenticate(ctx, token)
			cancel()
			if errors.Is(err, auth.ErrUnauthorized) {
				unauthorized(w, r)
				return
			}
			if err != nil {
				// Never log request headers or credentials.
				logger.ErrorContext(r.Context(), "authentication lookup failed", "request_id", r.Context().Value(requestIDKey))
				writeError(w, r, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication temporarily unavailable")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
		})
	}
}

// Reject duplicate or conflicting credential headers rather than picking one.
func credential(headers http.Header) (string, bool) {
	authorization, key := headers.Values("Authorization"), headers.Values("X-API-Key")
	if len(authorization)+len(key) != 1 {
		return "", false
	}
	if len(key) == 1 {
		value := key[0]
		return value, value != "" && !strings.ContainsAny(value, " ,\t\r\n")
	}
	parts := strings.Fields(authorization[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], !strings.Contains(parts[1], ",")
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	writeError(w, r, http.StatusUnauthorized, "unauthorized", "A valid API key is required")
}

func requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Unable to process request")
			return
		}
		id := hex.EncodeToString(bytes[:])
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func recoverPanic(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				if value == http.ErrAbortHandler {
					panic(value)
				}
				logger.ErrorContext(r.Context(), "request panic", "request_id", r.Context().Value(requestIDKey))
				writeError(w, r, http.StatusInternalServerError, "internal_error", "Unable to process request")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	id, _ := r.Context().Value(requestIDKey).(string)
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}, RequestID: id})
}

func getOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
