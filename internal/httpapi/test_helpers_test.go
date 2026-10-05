package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"novel-bot/internal/auth"
)

type authenticateFunc func(context.Context, string) (auth.Principal, error)

func (fn authenticateFunc) Authenticate(ctx context.Context, token string) (auth.Principal, error) {
	return fn(ctx, token)
}

func (fn authenticateFunc) Issue(context.Context, string, string, time.Duration) (auth.IssuedKey, error) {
	return auth.IssuedKey{}, errors.New("not configured")
}
func (fn authenticateFunc) List(context.Context, string) ([]auth.Key, error) {
	return nil, errors.New("not configured")
}
func (fn authenticateFunc) Revoke(context.Context, string, string) error {
	return errors.New("not configured")
}

func testRouter(fn authenticateFunc, ready func(context.Context) error) http.Handler {
	return NewRouter(fn, nil, ready, slog.New(slog.NewTextHandler(io.Discard, nil)), 20*time.Millisecond)
}

const testClientID = "11111111111111111111111111111111"
const testKeyID = "22222222222222222222222222222222"

type accountStub struct {
	err          error
	authFn       func(context.Context, string) (auth.AccountPrincipal, error)
	logoutCalled bool
}

func (s *accountStub) Register(context.Context, string, string, string) (auth.User, error) {
	return auth.User{ID: "user", ClientID: testClientID}, s.err
}
func (s *accountStub) Login(context.Context, string, string) (auth.Login, error) {
	return auth.Login{AccessToken: "session", TokenType: "Bearer"}, s.err
}
func (s *accountStub) AuthenticateSession(ctx context.Context, token string) (auth.AccountPrincipal, error) {
	if s.authFn != nil {
		return s.authFn(ctx, token)
	}
	if token != "session" {
		return auth.AccountPrincipal{}, auth.ErrUnauthorized
	}
	return auth.AccountPrincipal{User: auth.User{ID: "user", ClientID: testClientID}, SessionID: "session-id"}, s.err
}
func (s *accountStub) Logout(context.Context, auth.AccountPrincipal) error {
	s.logoutCalled = true
	return s.err
}

type keySpy struct {
	clientID, name, id string
	ttl                time.Duration
	calls              int
}

func (s *keySpy) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.ErrUnauthorized
}
func (s *keySpy) Issue(ctx context.Context, clientID, name string, ttl time.Duration) (auth.IssuedKey, error) {
	if err := ctx.Err(); err != nil {
		return auth.IssuedKey{}, err
	}
	s.clientID, s.name, s.ttl = clientID, name, ttl
	s.calls++
	return auth.IssuedKey{Key: auth.Key{ID: testKeyID, ClientID: clientID, Name: name}, APIKey: "issued-secret"}, nil
}
func (s *keySpy) List(ctx context.Context, clientID string) ([]auth.Key, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.clientID = clientID
	s.calls++
	return []auth.Key{{ID: testKeyID, ClientID: clientID}}, nil
}
func (s *keySpy) Revoke(ctx context.Context, clientID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.clientID, s.id = clientID, id
	s.calls++
	return nil
}

func accountRouter(keys KeyService, accounts AccountManager) http.Handler {
	return NewRouter(keys, accounts, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
}

func apiRequest(router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}
