package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"novel-bot/internal/auth"
	"novel-bot/internal/sessions"
)

type proxyBrowser struct {
	endpoint string
	done     chan struct{}
	once     sync.Once
}

func (b *proxyBrowser) Endpoint() string           { return b.endpoint }
func (b *proxyBrowser) Done() <-chan struct{}      { return b.done }
func (b *proxyBrowser) Stop(context.Context) error { b.once.Do(func() { close(b.done) }); return nil }

type proxyLauncher struct{ endpoint string }

func (l proxyLauncher) Launch(context.Context) (sessions.Browser, error) {
	return &proxyBrowser{endpoint: l.endpoint, done: make(chan struct{})}, nil
}

func sessionTestServer(t *testing.T, launcher sessions.Launcher, restTimeout ...time.Duration) *httptest.Server {
	t.Helper()
	manager, err := sessions.NewManager(launcher, sessions.Options{MaxSessions: 1, TTL: time.Minute, StartupTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	keys := authenticateFunc(func(ctx context.Context, token string) (auth.Principal, error) {
		switch token {
		case "owner", "other":
			return auth.Principal{ClientID: token}, nil
		default:
			return auth.Principal{}, auth.ErrUnauthorized
		}
	})
	router := NewRouter(keys, &accountStub{}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, RouterOptions{Sessions: manager})
	server := httptest.NewUnstartedServer(router)
	deadline := 100 * time.Millisecond
	if len(restTimeout) > 0 {
		deadline = restTimeout[0]
	}
	server.Config.ReadTimeout = deadline
	server.Config.WriteTimeout = deadline
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func sessionRequest(t *testing.T, server *httptest.Server, method, path, body, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func createBrowserSession(t *testing.T, server *httptest.Server) (string, string) {
	t.Helper()
	res := sessionRequest(t, server, "POST", "/sessions", "{}", "owner")
	var body struct {
		ID     string `json:"id"`
		CDPURL string `json:"cdp_url"`
	}
	if res.StatusCode != 201 {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("create: %d %s", res.StatusCode, data)
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID == "" || body.CDPURL != "ws"+strings.TrimPrefix(server.URL, "http")+"/sessions/"+body.ID {
		t.Fatalf("wrong proxy response: %+v", body)
	}
	return body.ID, body.CDPURL
}

func TestSessionRoutesAuthenticationOwnershipAndCapacity(t *testing.T) {
	server := sessionTestServer(t, proxyLauncher{"ws://127.0.0.1:9222/devtools/browser/test"})
	for _, token := range []string{"", "nb_session_login-token"} {
		if res := sessionRequest(t, server, "POST", "/sessions", "{}", token); res.StatusCode != 401 {
			t.Fatal("unauthenticated session creation")
		}
	}
	if res := sessionRequest(t, server, "POST", "/sessions", `{"args":["--no-sandbox"]}`, "owner"); res.StatusCode != 400 {
		t.Fatal("unrecognized launch options accepted")
	}
	id, _ := createBrowserSession(t, server)
	if res := sessionRequest(t, server, "POST", "/sessions", "{}", "owner"); res.StatusCode != 503 || res.Header.Get("Retry-After") == "" {
		t.Fatal("capacity not enforced")
	}
	for _, method := range []string{"GET", "DELETE"} {
		if res := sessionRequest(t, server, method, "/sessions/"+id, "", "other"); res.StatusCode != 404 {
			t.Fatal("cross-tenant browser access")
		}
	}
	if res := sessionRequest(t, server, "GET", "/sessions/"+id, "", "owner"); res.StatusCode != 400 {
		t.Fatal("non-WebSocket CDP request accepted")
	}
	if res := sessionRequest(t, server, "DELETE", "/sessions/"+id, "", "owner"); res.StatusCode != 204 {
		t.Fatal("delete failed")
	}
	if res := sessionRequest(t, server, "GET", "/sessions/"+id, "", "owner"); res.StatusCode != 404 {
		t.Fatal("deleted session still accessible")
	}
	createBrowserSession(t, server)
}

func TestCDPProxyRoundTripAndDeletionClosesConnection(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devtools/browser/test" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			t.Error("client credentials/path forwarded to browser")
			http.Error(w, "unsafe proxy", 400)
			return
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
	defer backend.Close()
	server := sessionTestServer(t, proxyLauncher{"ws" + strings.TrimPrefix(backend.URL, "http") + "/devtools/browser/test"})
	id, endpoint := createBrowserSession(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, res, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer other"}}})
	if err == nil || res == nil || res.StatusCode != 404 {
		t.Fatal("cross-tenant WebSocket accepted")
	}
	conn, _, err := websocket.Dial(ctx, endpoint+"?ignored=true", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer owner"}, "Origin": {"https://app.example.com"}, "Cookie": {"private=secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// Wait beyond REST read/write deadlines: upgraded connections stay usable.
	time.Sleep(150 * time.Millisecond)
	message := []byte(`{"id":1,"method":"Browser.getVersion"}`)
	if err := conn.Write(ctx, websocket.MessageText, message); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.Read(ctx)
	if err != nil || string(reply) != string(message) {
		t.Fatalf("CDP round trip: %s %v", reply, err)
	}
	if res := sessionRequest(t, server, "DELETE", "/sessions/"+id, "", "owner"); res.StatusCode != 204 {
		t.Fatal("delete failed")
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("deleted browser connection still open")
	}
}
