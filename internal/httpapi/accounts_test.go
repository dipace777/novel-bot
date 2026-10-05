package httpapi

import (
	"errors"
	"strings"
	"testing"

	"novel-bot/internal/auth"
)

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
