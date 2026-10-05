package httpapi

import (
	"context"
	"log/slog"
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

type keyHandlers struct {
	keys   KeyService
	logger *slog.Logger
}

func whoami(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Authentication context missing")
		return
	}
	writeJSON(w, http.StatusOK, principal)
}

func (h keyHandlers) keysCollection(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		keys, err := h.keys.List(r.Context(), p.User.ClientID)
		if err != nil {
			serviceError(h.logger, w, r, err)
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
			serviceError(h.logger, w, r, err)
			return
		}
		w.Header().Set("Location", "/v1/api-keys/"+issued.Key.ID)
		writeJSON(w, http.StatusCreated, issued)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeError(w, r, 405, "method_not_allowed", "Method not allowed")
	}
}

func (h keyHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(accountKey).(auth.AccountPrincipal)
	if err := h.keys.Revoke(r.Context(), p.User.ClientID, r.PathValue("id")); err != nil {
		serviceError(h.logger, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
