package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"novel-bot/internal/auth"
	"novel-bot/internal/browser"
	"novel-bot/internal/limits"
	"novel-bot/internal/sessions"
	redisstore "novel-bot/internal/storage/redis"
	"novel-bot/internal/worker"
)

func clusterDirectory(t *testing.T) *redisstore.Directory {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("set TEST_REDIS_URL to test Redis-backed worker routing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := redisstore.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	namespace := "http_test_" + id
	d, err := redisstore.NewDirectory(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, namespace+":*", 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		_ = client.Close()
	})
	return d
}

const testWorkerCredential = "isolated-test-worker-credential-32-chars"

func clusterWorker(t *testing.T, d sessions.Directory, id string, launcher sessions.Launcher) (*worker.Agent, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := worker.NewAgent(context.Background(), d, launcher, sessions.Worker{ID: id, URL: origin}, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: 5 * time.Second}, 5*time.Second, logger)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = NewWorkerRouter(a, testWorkerCredential, logger)
	server.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
		server.Close()
	})
	return a, server
}
func clusterGateway(t *testing.T, d sessions.Directory, providers ...limits.Provider) *httptest.Server {
	t.Helper()
	client := worker.NewClient(testWorkerCredential)
	t.Cleanup(client.Close)
	var provider limits.Provider = limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) { return limits.DefaultPolicy(), nil })
	if len(providers) > 0 {
		provider = providers[0]
	}
	cluster := sessions.NewCluster(d, client, 5*time.Second, provider)
	keys := authenticateFunc(func(_ context.Context, token string) (auth.Principal, error) {
		if token != "owner" && token != "other" {
			return auth.Principal{}, auth.ErrUnauthorized
		}
		return auth.Principal{ClientID: token}, nil
	})
	server := httptest.NewServer(NewRouter(keys, &accountStub{}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, RouterOptions{Sessions: cluster, WorkerAuthToken: testWorkerCredential}))
	t.Cleanup(server.Close)
	return server
}
func echoCDPBackend(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "X-API-Key", worker.ClientHeader, worker.WorkerHeader} {
			if r.Header.Get(name) != "" {
				t.Error("worker credentials reached Chromium")
				http.Error(w, "unsafe headers", 400)
				return
			}
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			kind, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func dialCluster(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer owner"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func TestRedisRoutesSessionsAcrossReplicasAndProtectsWorkerAPI(t *testing.T) {
	d := clusterDirectory(t)
	backend := echoCDPBackend(t)
	launcher := proxyLauncher{"ws" + strings.TrimPrefix(backend.URL, "http") + "/devtools/browser/test"}
	a, private := clusterWorker(t, d, "a", launcher)
	clusterWorker(t, d, "b", launcher)
	first, second := clusterGateway(t, d), clusterGateway(t, d)
	id, _ := createBrowserSession(t, first)
	id2, _ := createBrowserSession(t, second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := d.Lookup(ctx, "owner", id, "ready")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := d.Lookup(ctx, "owner", id2, "ready")
	if err != nil || r.WorkerID == r2.WorkerID {
		t.Fatal("sessions did not spread across available workers", err)
	}
	if res := sessionRequest(t, first, "POST", "/sessions", "{}", "owner"); res.StatusCode != 503 {
		t.Fatal("cluster capacity exceeded")
	}
	if res := sessionRequest(t, second, "GET", "/sessions/"+id, "", "other"); res.StatusCode != 404 {
		t.Fatal("cross-tenant remote lookup succeeded")
	}
	if res := sessionRequest(t, private, "GET", "/internal/sessions/"+id, "", "owner"); res.StatusCode != 401 {
		t.Fatal("public API key accepted by private worker API")
	}
	req, _ := http.NewRequest("GET", private.URL+"/internal/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testWorkerCredential)
	req.Header.Set(worker.ClientHeader, "owner")
	req.Header.Set(worker.WorkerHeader, "old-incarnation")
	res, err := private.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("stale incarnation reached a live worker")
	}
	conn := dialCluster(t, "ws"+strings.TrimPrefix(second.URL, "http")+"/sessions/"+id)
	payload := []byte(`{"id":1,"method":"Browser.getVersion"}`)
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.Read(ctx)
	if err != nil || string(reply) != string(payload) {
		t.Fatalf("cross-worker proxy: %s %v", reply, err)
	}
	if res := sessionRequest(t, second, "DELETE", "/sessions/"+id, "", "owner"); res.StatusCode != 204 {
		t.Fatal("remote deletion failed")
	}
	if _, err := d.Lookup(ctx, "owner", id, "ready"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("deleted session stayed in Redis")
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("remote deletion left the CDP connection open")
	}
	id3, _ := createBrowserSession(t, first)
	ownedByA := id2
	if r2.WorkerID != "a" {
		ownedByA = id3
	}
	live := dialCluster(t, "ws"+strings.TrimPrefix(second.URL, "http")+"/sessions/"+ownedByA)
	// Forcibly revoke a live worker lease. Its browsers and connections must stop.
	if err := d.Unregister(ctx, a.Worker()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not fence itself after lease loss")
	}
	if _, _, err := live.Read(ctx); err == nil {
		t.Fatal("lease loss left a CDP connection open")
	}
	if _, err := d.Lookup(ctx, "owner", ownedByA, "ready"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("lease-lost worker still routable")
	}
}

func TestRedisChromiumSessionAcrossReplicas(t *testing.T) {
	path := os.Getenv("TEST_CHROMIUM_PATH")
	if path == "" {
		t.Skip("set TEST_CHROMIUM_PATH for real Chromium with Redis and worker routing")
	}
	d := clusterDirectory(t)
	profiles := t.TempDir()
	launcher, err := browser.NewChromium(path, profiles, true)
	if err != nil {
		t.Fatal(err)
	}
	clusterWorker(t, d, "real-chromium", launcher)
	first, second := clusterGateway(t, d), clusterGateway(t, d)
	id, _ := createBrowserSession(t, first)
	conn := dialCluster(t, "ws"+strings.TrimPrefix(second.URL, "http")+"/sessions/"+id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"Browser.getVersion"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		ID     int `json:"id"`
		Result struct {
			Product string `json:"product"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.ID != 1 || reply.Result.Product == "" {
		t.Fatalf("invalid real CDP response: %s %v", data, err)
	}
	if res := sessionRequest(t, second, "DELETE", "/sessions/"+id, "", "owner"); res.StatusCode != 204 {
		t.Fatal("real remote browser deletion failed")
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("deleted remote browser connection survived")
	}
	files, err := os.ReadDir(profiles)
	if err != nil || len(files) != 0 {
		t.Fatalf("remote profile leaked: %v %v", files, err)
	}
}

func TestTenantAdmissionAcrossGatewaysAndBrowserExpiry(t *testing.T) {
	d := clusterDirectory(t)
	backend := echoCDPBackend(t)
	launcher := proxyLauncher{"ws" + strings.TrimPrefix(backend.URL, "http") + "/devtools/browser/test"}
	clusterWorker(t, d, "a", launcher)
	clusterWorker(t, d, "b", launcher)
	provider := limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) {
		return limits.Policy{MaxConcurrentSessions: 1, MaxSessionTTL: 500 * time.Millisecond, SessionRequestsPerMinute: 4}, nil
	})
	first, second := clusterGateway(t, d, provider), clusterGateway(t, d, provider)
	id, _ := createBrowserSession(t, first)
	metadata, err := d.Lookup(context.Background(), "owner", id, "ready")
	if err != nil {
		t.Fatal(err)
	}
	if ttl := metadata.ExpiresMS - metadata.CreatedMS; ttl != 500 {
		t.Fatalf("worker ignored tenant TTL: %dms", ttl)
	}
	res := sessionRequest(t, second, "POST", "/sessions", "{}", "owner")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "tenant_session_limit_reached") {
		t.Fatalf("quota response %d: %s", res.StatusCode, body)
	}
	// Another tenant retains its own budget, despite the first tenant's full quota.
	res = sessionRequest(t, second, "POST", "/sessions", "{}", "other")
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatal("tenant budgets are not isolated", res.StatusCode)
	}
	conn := dialCluster(t, "ws"+strings.TrimPrefix(second.URL, "http")+"/sessions/"+id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("tenant TTL did not close CDP connection")
	}
	id, _ = createBrowserSession(t, second)
	res = sessionRequest(t, first, "DELETE", "/sessions/"+id, "", "owner")
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("remote quota cleanup failed")
	}
	createBrowserSession(t, first)
	res = sessionRequest(t, second, "POST", "/sessions", "{}", "owner")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 429 || !strings.Contains(string(body), "session_rate_limit_exceeded") {
		t.Fatalf("tenant rate response %d: %s", res.StatusCode, body)
	}
}

type failingLauncher struct{}

func (failingLauncher) Launch(context.Context) (sessions.Browser, error) {
	return nil, errors.New("test startup failure")
}
func TestFailedStartupReleasesTenantQuota(t *testing.T) {
	d := clusterDirectory(t)
	clusterWorker(t, d, "a", failingLauncher{})
	provider := limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) {
		return limits.Policy{MaxConcurrentSessions: 1, MaxSessionTTL: time.Minute, SessionRequestsPerMinute: 10}, nil
	})
	gateway := clusterGateway(t, d, provider)
	for i := 0; i < 3; i++ {
		res := sessionRequest(t, gateway, "POST", "/sessions", "{}", "owner")
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 503 || strings.Contains(string(data), "tenant_session_limit_reached") {
			t.Fatalf("failed launch leaked quota: %d %s", res.StatusCode, data)
		}
	}
}

func TestAuthenticationRateBudgetSharedAcrossRouters(t *testing.T) {
	d := clusterDirectory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := func() http.Handler {
		return NewRouter(&keySpy{}, &accountStub{}, func(context.Context) error { return nil }, logger, time.Second, RouterOptions{RateLimiter: d, AuthRequestsPerMinute: 2})
	}
	first, second := router(), router()
	if res := apiRequest(first, "POST", "/v1/auth/login", `{"email":"owner@example.com","password":"password"}`, ""); res.Code != 200 {
		t.Fatal(res.Code)
	}
	if res := apiRequest(second, "GET", "/v1/auth/me", "", ""); res.Code != 401 {
		t.Fatal(res.Code)
	}
	if res := apiRequest(first, "POST", "/v1/auth/register", `{}`, ""); res.Code != 429 || res.Header().Get("Retry-After") == "" {
		t.Fatal("replicas used separate auth budgets", res.Code)
	}
	req := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"email":"other@example.com","password":"password"}`))
	req.RemoteAddr = "198.51.100.5:123"
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	second.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal("unrelated IP rejected", response.Code)
	}
}
