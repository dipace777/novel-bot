package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"novel-bot/internal/auth"
	"novel-bot/internal/httpapi"
	"novel-bot/internal/storage/postgres"
	"novel-bot/migrations"
)

// This test creates and removes an isolated schema, never public tables.
func TestPostgresAuthenticationIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "auth_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("repeat migrations: %v", err)
	}
	repository := postgres.NewRepository(pool)
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("schema readiness: %v", err)
	}
	service, err := auth.NewService(repository, []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.CreateClient(ctx, "integration app")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(ctx, client.ID, "integration key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var digest []byte
	if err := pool.QueryRow(ctx, `SELECT digest FROM api_keys WHERE id = $1`, issued.Key.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if len(digest) != 32 {
		t.Fatal("incorrect persisted digest")
	}
	if _, err := service.Issue(ctx, strings.Repeat("a", 32), "unknown", time.Hour); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("foreign key error: %v", err)
	}
	keys, err := service.List(ctx, client.ID)
	if err != nil || len(keys) != 1 || keys[0].Digest != nil {
		t.Fatalf("metadata list: %+v, %v", keys, err)
	}
	router := httpapi.NewRouter(service, nil, pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	request := func(token string, want int) {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/whoami", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("got %d, want %d: %s", response.Code, want, response.Body.String())
		}
	}
	request(issued.APIKey, 200)
	if err := service.Revoke(ctx, client.ID, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, client.ID, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	request(issued.APIKey, 401)
	expired, err := service.Issue(ctx, client.ID, "expired", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE api_keys SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = $1`, expired.Key.ID); err != nil {
		t.Fatal(err)
	}
	request(expired.APIKey, 401)
	if err := service.Revoke(ctx, client.ID, strings.Repeat("f", 32)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	testHTTPAccountFlow(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = 'changed'`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err == nil {
		t.Fatal("migration checksum drift was not rejected")
	}
}

func testHTTPAccountFlow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	repo := postgres.NewRepository(pool)
	pepper := []byte(strings.Repeat("p", 32))
	keys, err := auth.NewService(repo, pepper)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.NewAccountService(repo, pepper, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(keys, accounts, pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second)
	request := func(method, path, token string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, response.Code, want, response.Body.String())
		}
		return response
	}
	decode := func(response *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	const password = "correct horse battery staple"
	registration := map[string]string{"name": "Owner", "email": "Owner@Example.com", "password": password}
	request("POST", "/v1/auth/register", "", registration, 201)
	var clientCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM clients`).Scan(&clientCount); err != nil {
		t.Fatal(err)
	}
	request("POST", "/v1/auth/register", "", registration, 409)
	var afterDuplicate int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM clients`).Scan(&afterDuplicate); err != nil {
		t.Fatal(err)
	}
	if clientCount != afterDuplicate {
		t.Fatal("duplicate signup left an orphan client")
	}
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE email = 'owner@example.com'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		t.Fatal("persisted password hash invalid:", err)
	}
	request("POST", "/v1/auth/login", "", map[string]string{"email": "owner@example.com", "password": "wrong password"}, 401)
	request("POST", "/v1/auth/login", "", map[string]string{"email": "missing@example.com", "password": password}, 401)
	var login auth.Login
	decode(request("POST", "/v1/auth/login", "", map[string]string{"email": "OWNER@example.com", "password": password}, 200), &login)
	request("GET", "/v1/auth/me", login.AccessToken, nil, 200)
	request("POST", "/v1/api-keys", "", map[string]string{"name": "key"}, 401)
	var issued auth.IssuedKey
	decode(request("POST", "/v1/api-keys", login.AccessToken, map[string]any{"name": "production", "ttl_seconds": 3600}, 201), &issued)
	if issued.Key.ClientID != login.User.ClientID {
		t.Fatal("key belongs to wrong client")
	}
	var list struct {
		Keys []auth.Key `json:"keys"`
	}
	decode(request("GET", "/v1/api-keys", login.AccessToken, nil, 200), &list)
	if len(list.Keys) != 1 || list.Keys[0].ID != issued.Key.ID {
		t.Fatal("incorrect key list")
	}
	request("GET", "/v1/whoami", issued.APIKey, nil, 200)
	request("POST", "/v1/api-keys", issued.APIKey, map[string]string{"name": "escalation"}, 401)
	request("GET", "/v1/whoami", login.AccessToken, nil, 401)
	request("POST", "/v1/auth/register", "", map[string]string{"name": "Other", "email": "other@example.com", "password": password}, 201)
	var other auth.Login
	decode(request("POST", "/v1/auth/login", "", map[string]string{"email": "other@example.com", "password": password}, 200), &other)
	decode(request("GET", "/v1/api-keys", other.AccessToken, nil, 200), &list)
	if len(list.Keys) != 0 {
		t.Fatal("other tenant could list owner's key")
	}
	request("DELETE", "/v1/api-keys/"+issued.Key.ID, other.AccessToken, nil, 404)
	request("GET", "/v1/whoami", issued.APIKey, nil, 200)
	request("DELETE", "/v1/api-keys/"+issued.Key.ID, login.AccessToken, nil, 204)
	request("DELETE", "/v1/api-keys/"+issued.Key.ID, login.AccessToken, nil, 204)
	request("GET", "/v1/whoami", issued.APIKey, nil, 401)
	request("POST", "/v1/auth/logout", login.AccessToken, nil, 204)
	request("GET", "/v1/auth/me", login.AccessToken, nil, 401)
	request("POST", "/v1/api-keys", login.AccessToken, map[string]string{"name": "after logout"}, 401)
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET created_at=now()-interval '2 hours', expires_at=now()-interval '1 hour' WHERE user_id=$1`, other.User.ID); err != nil {
		t.Fatal(err)
	}
	request("GET", "/v1/api-keys", other.AccessToken, nil, 401)
}
