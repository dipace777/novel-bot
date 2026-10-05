package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestAPIKeyRequestValidation(t *testing.T) {
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
