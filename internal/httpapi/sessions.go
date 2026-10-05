package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"novel-bot/internal/sessions"
)

// SessionManager can later be implemented by a service that routes to workers.
type SessionManager interface {
	Create(context.Context, string) (sessions.Session, error)
	Get(context.Context, string, string) (sessions.Session, error)
	Delete(context.Context, string, string) error
}

// RouterOptions keeps browser execution optional for auth-only tests/consumers.
type RouterOptions struct {
	Sessions     SessionManager
	PublicAPIURL string
}

type sessionHandlers struct {
	manager   SessionManager
	publicURL string
	logger    *slog.Logger
}

func mountSessionRoutes(mux *http.ServeMux, manager SessionManager, publicURL string, logger *slog.Logger, keys Authenticator, timeout time.Duration) {
	h := sessionHandlers{manager: manager, publicURL: publicURL, logger: logger}
	protected := http.NewServeMux()
	protected.Handle("/sessions", methodOnly(http.MethodPost, http.HandlerFunc(h.create)))
	protected.HandleFunc("/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.connect(w, r)
		case http.MethodDelete:
			h.delete(w, r)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		}
	})
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "Route not found")
	})
	authenticated := RequireAPIKey(keys, logger, timeout)(protected)
	mux.Handle("/sessions", authenticated)
	mux.Handle("/sessions/", authenticated)
}

func (h sessionHandlers) create(w http.ResponseWriter, r *http.Request) {
	// No launch flags or arbitrary browser URLs are accepted from callers.
	var body *struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "Provide one JSON object")
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	s, err := h.manager.Create(r.Context(), principal.ClientID)
	if err != nil {
		h.sessionError(w, r, err)
		return
	}
	base := h.publicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = (&url.URL{Scheme: scheme, Host: r.Host}).String()
	}
	endpoint, _ := url.Parse(base) // PUBLIC_API_URL is validated at startup.
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = "/sessions/" + s.ID
	endpoint.RawQuery, endpoint.Fragment = "", ""
	w.Header().Set("Location", endpoint.Path)
	writeJSON(w, http.StatusCreated, struct {
		ID        string    `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
		CDPURL    string    `json:"cdp_url"`
	}{s.ID, s.CreatedAt, s.ExpiresAt, endpoint.String()})
}

func (h sessionHandlers) delete(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.manager.Delete(ctx, principal.ClientID, r.PathValue("id")); err != nil {
		h.sessionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h sessionHandlers) sessionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sessions.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "session_not_found", "Browser session not found")
	case errors.Is(err, sessions.ErrCapacity):
		w.Header().Set("Retry-After", "5")
		writeError(w, r, http.StatusServiceUnavailable, "session_capacity_reached", "Browser session capacity reached")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, r, http.StatusGatewayTimeout, "browser_timeout", "Browser operation timed out")
	default:
		h.logger.ErrorContext(r.Context(), "browser session operation failed", "request_id", r.Context().Value(requestIDKey))
		writeError(w, r, http.StatusServiceUnavailable, "browser_unavailable", "Browser service temporarily unavailable")
	}
}
