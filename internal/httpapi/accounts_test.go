package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novel-bot/internal/auth"
)

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

func TestAccountRouteAuthenticationAndKeyOwnership(t *testing.T) {
	keys, accounts := &keySpy{}, &accountStub{}
	router := accountRouter(keys, accounts)
	for _, path := range []string{"/v1/auth/me", "/v1/auth/logout", "/v1/api-keys", "/v1/api-keys/" + testKeyID} {
		for _, token := range []string{"", "application-api-key"} {
			response := apiRequest(router, "GET", path, "", token)
			if response.Code != 401 {
				t.Fatalf("unprotected %s: %d", path, response.Code)
			}
		}
	}
	response := apiRequest(router, "POST", "/v1/api-keys", `{"name":"production","ttl_seconds":3600}`, "session")
	if response.Code != 201 || keys.clientID != testClientID || keys.name != "production" || keys.ttl != time.Hour {
		t.Fatalf("issue: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "issued-secret") || response.Header().Get("Location") != "/v1/api-keys/"+testKeyID {
		t.Fatal("issued key response missing secret or location")
	}
	response = apiRequest(router, "GET", "/v1/api-keys", "", "session")
	if response.Code != 200 || keys.clientID != testClientID || strings.Contains(response.Body.String(), "secret") {
		t.Fatal("unsafe key list")
	}
	response = apiRequest(router, "DELETE", "/v1/api-keys/"+testKeyID, "", "session")
	if response.Code != 204 || keys.clientID != testClientID || keys.id != testKeyID {
		t.Fatalf("revoke: %d", response.Code)
	}
	response = apiRequest(router, "POST", "/v1/api-keys", `{"name":"evil","client_id":"another-client"}`, "session")
	if response.Code != 400 || keys.calls != 3 {
		t.Fatal("caller-supplied tenant was accepted")
	}
	req := httptest.NewRequest("GET", "/v1/api-keys", nil)
	req.Header.Set("X-API-Key", "session")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatal("account token accepted via X-API-Key")
	}
	if response := apiRequest(router, "POST", "/v1/auth/logout", "", "session"); response.Code != 204 || !accounts.logoutCalled {
		t.Fatal("logout not called")
	}
}

func TestAccountRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"name":"key","ttl_seconds":0}`, 400}, {`{"name":"key","ttl_seconds":-1}`, 400},
		{`{"name":"key","ttl_seconds":7776001}`, 400}, {`{"name":"key","ttl_seconds":1.5}`, 400},
		{`{"name":"key","ttl_seconds":9223372036854775807}`, 400}, {`{"name":"key"} {}`, 400},
		{`{"name":"key","unexpected":true}`, 400}, {`{bad json`, 400},
		{`{"name":"` + strings.Repeat("x", 17000) + `"}`, 413},
	} {
		keys := &keySpy{}
		response := apiRequest(accountRouter(keys, &accountStub{}), "POST", "/v1/api-keys", tc.body, "session")
		if response.Code != tc.status || keys.calls != 0 {
			t.Fatalf("validation: got %d want %d: %s", response.Code, tc.status, response.Body.String())
		}
	}
	response := apiRequest(accountRouter(&keySpy{}, &accountStub{}), "POST", "/v1/api-keys", `{"name":"key"}`, "session")
	if response.Code != 201 {
		t.Fatalf("default TTL issuance: %d", response.Code)
	}
	req := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"email":"a@example.com","password":"password"}`))
	response = httptest.NewRecorder()
	accountRouter(&keySpy{}, &accountStub{}).ServeHTTP(response, req)
	if response.Code != 415 {
		t.Fatal("missing Content-Type accepted")
	}
}

func TestPublicAccountRoutesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		err        error
		status     int
	}{
		{"/v1/auth/register", `{"name":"App","email":"a@example.com","password":"long enough password"}`, nil, 201},
		{"/v1/auth/register", `{"name":"App","email":"a@example.com","password":"long enough password"}`, auth.ErrConflict, 409},
		{"/v1/auth/register", `{}`, auth.ErrInvalidInput, 400},
		{"/v1/auth/login", `{"email":"a@example.com","password":"long enough password"}`, nil, 200},
		{"/v1/auth/login", `{}`, auth.ErrCredentials, 401},
		{"/v1/auth/login", `{}`, errors.New("private database details"), 503},
	} {
		response := apiRequest(accountRouter(&keySpy{}, &accountStub{err: tc.err}), "POST", tc.path, tc.body, "")
		if response.Code != tc.status || strings.Contains(response.Body.String(), "private") {
			t.Fatalf("account response: %d %s", response.Code, response.Body.String())
		}
	}
	for _, tc := range []struct{ path, method string }{
		{"/v1/auth/register", "GET"}, {"/v1/auth/login", "GET"}, {"/v1/auth/me", "POST"},
		{"/v1/auth/logout", "GET"}, {"/v1/api-keys", "PATCH"}, {"/v1/api-keys/" + testKeyID, "POST"},
	} {
		response := apiRequest(accountRouter(&keySpy{}, &accountStub{}), tc.method, tc.path, "", "session")
		if response.Code != 405 {
			t.Fatalf("unsupported method: %s %s: %d", tc.method, tc.path, response.Code)
		}
	}
}

func TestSessionLookupTimeout(t *testing.T) {
	accounts := &accountStub{authFn: func(ctx context.Context, _ string) (auth.AccountPrincipal, error) {
		<-ctx.Done()
		return auth.AccountPrincipal{}, ctx.Err()
	}}
	router := NewRouter(&keySpy{}, accounts, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	response := apiRequest(router, "GET", "/v1/auth/me", "", "session")
	if response.Code != 503 {
		t.Fatalf("timeout status: %d", response.Code)
	}
}
