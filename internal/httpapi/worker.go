package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"novel-bot/internal/auth"
	"novel-bot/internal/sessions"
	"novel-bot/internal/worker"
)

type WorkerService interface {
	Worker() sessions.Worker
	Ready() error
	CreateReserved(context.Context, string, string) (sessions.Session, error)
	Get(context.Context, string, string) (sessions.Session, error)
	Delete(context.Context, string, string) error
}

// NewWorkerRouter is served on a separate private listener, never the public API.
func NewWorkerRouter(agent WorkerService, token string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	h := sessionHandlers{manager: workerLocal{agent}, logger: logger}
	mux.Handle("/internal/sessions", methodOnly(http.MethodPost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if len(body.ID) != 32 {
			writeError(w, r, 400, "invalid_input", "Invalid session ID")
			return
		}
		principal, _ := PrincipalFromContext(r.Context())
		s, err := agent.CreateReserved(r.Context(), principal.ClientID, body.ID)
		if err != nil {
			h.sessionError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, sessions.Record{ID: s.ID, ClientID: s.ClientID, WorkerID: agent.Worker().ID, WorkerToken: s.WorkerToken, WorkerURL: s.WorkerURL, State: "ready", CreatedMS: s.CreatedAt.UnixMilli(), ExpiresMS: s.ExpiresAt.UnixMilli()})
	})))
	mux.HandleFunc("/internal/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.connect(w, r)
		case http.MethodDelete:
			h.delete(w, r)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeError(w, r, 405, "method_not_allowed", "Method not allowed")
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, 404, "not_found", "Route not found") })
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(token) < 32 || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			writeError(w, r, 401, "unauthorized", "Worker authentication required")
			return
		}
		if len(r.Header.Values(worker.WorkerHeader)) != 1 || r.Header.Get(worker.WorkerHeader) != agent.Worker().Token {
			writeError(w, r, 404, "session_not_found", "Worker incarnation unavailable")
			return
		}
		clientID := r.Header.Get(worker.ClientHeader)
		if len(r.Header.Values(worker.ClientHeader)) != 1 || clientID == "" || len(clientID) > 128 {
			writeError(w, r, 400, "invalid_input", "Client identity required")
			return
		}
		if r.Method != http.MethodDelete {
			if err := agent.Ready(); err != nil {
				h.sessionError(w, r, err)
				return
			}
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, auth.Principal{ClientID: clientID})))
	})
	return requestMetadata(recoverPanic(protected, logger))
}

// Creation goes through reservations; the local adapter supports only lookup/deletion.
type workerLocal struct{ WorkerService }

func (workerLocal) Create(context.Context, string) (sessions.Session, error) {
	return sessions.Session{}, sessions.ErrClosed
}
