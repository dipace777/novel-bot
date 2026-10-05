// Package httpapi owns REST routes and transport concerns.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// NewRouter separates account sessions for key management from application API keys.
func NewRouter(service KeyService, accounts AccountManager, ready func(context.Context) error, logger *slog.Logger, authTimeout time.Duration, options ...RouterOptions) http.Handler {
	api := http.NewServeMux()
	api.Handle("/v1/whoami", getOnly(whoami))
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "Route not found")
	})
	mux := http.NewServeMux()
	mountDocumentation(mux)
	if len(options) > 0 && options[0].Sessions != nil {
		mountSessionRoutes(mux, options[0].Sessions, options[0].PublicAPIURL, logger, service, authTimeout, options[0].WorkerAuthToken)
	}
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

func mountAccountRoutes(mux *http.ServeMux, keys KeyService, accounts AccountManager, logger *slog.Logger, timeout time.Duration) {
	h := accountHandlers{accounts: accounts, logger: logger}
	k := keyHandlers{keys: keys, logger: logger}
	mux.Handle("/v1/auth/register", methodOnly("POST", operationTimeout(timeout, http.HandlerFunc(h.register))))
	mux.Handle("/v1/auth/login", methodOnly("POST", operationTimeout(timeout, http.HandlerFunc(h.login))))
	control := http.NewServeMux()
	control.Handle("/v1/auth/me", getOnly(h.me))
	control.Handle("/v1/auth/logout", methodOnly("POST", http.HandlerFunc(h.logout)))
	control.Handle("/v1/api-keys", http.HandlerFunc(k.keysCollection))
	control.Handle("/v1/api-keys/{id}", methodOnly("DELETE", http.HandlerFunc(k.revoke)))
	control.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, 404, "not_found", "Route not found") })
	protected := requireSession(accounts, logger, timeout)(operationTimeout(timeout, control))
	mux.Handle("/v1/auth/", protected)
	mux.Handle("/v1/api-keys", protected)
	mux.Handle("/v1/api-keys/", protected)
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
