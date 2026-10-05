package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"novel-bot/internal/auth"
)

type AccountManager interface {
	Register(context.Context, string, string, string) (auth.User, error)
	Login(context.Context, string, string) (auth.Login, error)
	AuthenticateSession(context.Context, string) (auth.AccountPrincipal, error)
	Logout(context.Context, auth.AccountPrincipal) error
}

type accountHandlers struct {
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
		serviceError(h.logger, w, r, err)
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
		serviceError(h.logger, w, r, err)
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
		serviceError(h.logger, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
