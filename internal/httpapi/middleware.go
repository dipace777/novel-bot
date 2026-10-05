package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

func PrincipalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p, ok
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

func operationTimeout(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
