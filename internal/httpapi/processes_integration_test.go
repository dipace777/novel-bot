package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"novel-bot/internal/auth"
	"novel-bot/internal/sessions"
	"novel-bot/internal/storage/postgres"
	redisstore "novel-bot/internal/storage/redis"
	"novel-bot/migrations"
)

type childProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startRole(t *testing.T, binary string, env map[string]string) *childProcess {
	t.Helper()
	cmd := exec.Command(binary)
	// Strip role settings from the parent so the test's configuration is deterministic.
	settings := []string{"DATABASE_URL", "DB_MAX_CONNS", "API_KEY_PEPPER", "HTTP_ADDR", "AUTH_TIMEOUT", "AUTH_REQUESTS_PER_MINUTE", "TRUSTED_PROXY_CIDRS", "SESSION_TTL", "SESSION_STARTUP_TIMEOUT", "PUBLIC_API_URL", "REDIS_URL", "REDIS_NAMESPACE", "WORKER_AUTH_TOKEN", "WORKER_ID", "WORKER_HTTP_ADDR", "WORKER_URL", "WORKER_LEASE_TTL", "WORKER_DRAIN_TIMEOUT", "WORKER_STOP_GRACE_PERIOD", "CHROMIUM_PATH", "BROWSER_PROFILE_DIR", "BROWSER_HEADLESS", "BROWSER_MAX_SESSIONS", "BROWSER_SESSION_TTL", "BROWSER_STARTUP_TIMEOUT", "METRICS_SAMPLE_INTERVAL"}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		skip := false
		for _, name := range settings {
			if key == name {
				skip = true
				break
			}
		}
		if !skip {
			cmd.Env = append(cmd.Env, v)
		}
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &childProcess{cmd: cmd, done: make(chan struct{})}
	go func() { child.err = cmd.Wait(); close(child.done) }()
	t.Cleanup(func() { stopRole(t, child) })
	return child
}
func stopRole(t *testing.T, p *childProcess) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
		if p.err != nil {
			t.Errorf("role did not stop gracefully: %v", p.err)
		}
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Error("role shutdown timed out")
	}
}
func freeOrigin(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	listener.Close()
	return origin
}
func processRequest(t *testing.T, method, endpoint, key, body string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var data bytes.Buffer
	if _, err := data.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data.Bytes()
}
func awaitRole(t *testing.T, child *childProcess, origin, key string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-child.done:
			t.Fatalf("role exited before readiness: %v", child.err)
		default:
		}
		req, _ := http.NewRequest("GET", origin+"/readyz", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if res, err := client.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("role readiness timed out")
}
func processCDP(t *testing.T, origin, id, key string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(origin, "http")+"/sessions/"+id, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"Browser.getVersion"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, reply, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var message struct {
			ID     int
			Result struct{ Product string }
		}
		if err := json.Unmarshal(reply, &message); err != nil {
			t.Fatal(err)
		}
		if message.ID == 1 {
			if message.Result.Product == "" {
				t.Fatal("missing CDP version")
			}
			break
		}
	}
	return conn
}

// Built binaries are essential here: this verifies isolation and process lifecycle,
// not just routing between in-process HTTP test servers.
func TestIndependentAPIAndWorkerProcesses(t *testing.T) {
	apiBinary, workerBinary := os.Getenv("TEST_API_BINARY"), os.Getenv("TEST_WORKER_BINARY")
	dbURL, redisURL, chrome := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL"), os.Getenv("TEST_CHROMIUM_PATH")
	if apiBinary == "" || workerBinary == "" || dbURL == "" || redisURL == "" || chrome == "" {
		t.Skip("set TEST_API_BINARY, TEST_WORKER_BINARY, TEST_DATABASE_URL, TEST_REDIS_URL, TEST_CHROMIUM_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	id, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "process_test_" + id
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	apiDBURL := dbURL + " search_path=" + schema
	if strings.HasPrefix(dbURL, "postgres://") || strings.HasPrefix(dbURL, "postgresql://") {
		u, err := url.Parse(dbURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		apiDBURL = u.String()
	}
	pepper := []byte(strings.Repeat("p", 32))
	service, err := auth.NewService(postgres.NewRepository(pool), pepper)
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := service.CreateClient(ctx, "process integration")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(ctx, tenant.ID, "process key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	redis, err := redisstore.Open(ctx, redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		var cursor uint64
		for {
			keys, next, err := redis.Scan(cleanup, cursor, schema+":*", 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := redis.Del(cleanup, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		redis.Close()
	})
	directory, err := redisstore.NewDirectory(redis, schema)
	if err != nil {
		t.Fatal(err)
	}
	public := freeOrigin(t)
	apiEnv := map[string]string{"HTTP_ADDR": strings.TrimPrefix(public, "http://"), "DATABASE_URL": apiDBURL, "API_KEY_PEPPER": base64.StdEncoding.EncodeToString(pepper), "WORKER_AUTH_TOKEN": testWorkerCredential, "REDIS_URL": redisURL, "REDIS_NAMESPACE": schema, "SESSION_STARTUP_TIMEOUT": "10s", "CHROMIUM_PATH": "/nonexistent/chromium", "BROWSER_MAX_SESSIONS": "invalid", "WORKER_URL": "invalid"}
	api := startRole(t, apiBinary, apiEnv)
	awaitRole(t, api, public, "")
	if status, _ := processRequest(t, "POST", public+"/sessions", issued.APIKey, "{}"); status != 503 {
		t.Fatal("API without workers must reject capacity", status)
	}
	if status, _ := processRequest(t, "GET", public+"/readyz", "", ""); status != 200 {
		t.Fatal("empty cluster must not unready API")
	}
	profiles := []string{t.TempDir(), t.TempDir()}
	workers := make([]*childProcess, 2)
	privateOrigins := map[string]string{}
	startWorker := func(i int) *childProcess {
		name := []string{"a", "b"}[i]
		private := freeOrigin(t)
		child := startRole(t, workerBinary, map[string]string{"DATABASE_URL": "", "API_KEY_PEPPER": "invalid", "HTTP_ADDR": "invalid", "WORKER_ID": name, "WORKER_HTTP_ADDR": strings.TrimPrefix(private, "http://"), "WORKER_URL": private, "WORKER_AUTH_TOKEN": testWorkerCredential, "REDIS_URL": redisURL, "REDIS_NAMESPACE": schema, "CHROMIUM_PATH": chrome, "BROWSER_HEADLESS": "true", "BROWSER_PROFILE_DIR": profiles[i], "BROWSER_MAX_SESSIONS": "1", "BROWSER_SESSION_TTL": "1m", "BROWSER_STARTUP_TIMEOUT": "10s", "WORKER_LEASE_TTL": "3s", "WORKER_DRAIN_TIMEOUT": "5s", "METRICS_SAMPLE_INTERVAL": "250ms"})
		awaitRole(t, child, private, testWorkerCredential)
		if status, _ := processRequest(t, "GET", private+"/readyz", "", ""); status != 401 {
			t.Fatal("worker readiness exposed", status)
		}
		privateOrigins[name] = private
		return child
	}
	for i := range workers {
		workers[i] = startWorker(i)
	}
	create := func() sessions.Record {
		t.Helper()
		status, data := processRequest(t, "POST", public+"/sessions", issued.APIKey, "{}")
		if status != 201 {
			t.Fatalf("create status %d", status)
		}
		var result struct{ ID string }
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		r, err := directory.Lookup(ctx, tenant.ID, result.ID, "ready")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, second := create(), create()
	if first.WorkerID == second.WorkerID {
		t.Fatal("sessions failed to spread across workers")
	}
	connection := processCDP(t, public, first.ID, issued.APIKey)
	stopRole(t, api)
	disconnected, stop := context.WithTimeout(ctx, 3*time.Second)
	if _, _, err := connection.Read(disconnected); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Error("API shutdown did not close proxy")
	}
	stop()
	api = startRole(t, apiBinary, apiEnv)
	awaitRole(t, api, public, "")
	processCDP(t, public, first.ID, issued.APIKey)
	processCDP(t, public, second.ID, issued.APIKey)
	// Force lease loss; only the owning worker and its browsers should exit.
	if err := directory.Unregister(ctx, sessions.Worker{ID: first.WorkerID, Token: first.WorkerToken}); err != nil {
		t.Fatal(err)
	}
	lost := 0
	if first.WorkerID == "b" {
		lost = 1
	}
	select {
	case <-workers[lost].done:
		if workers[lost].err == nil {
			t.Fatal("lease loss did not fail worker process")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("lease-lost worker remained running")
	}
	if status, _ := processRequest(t, "GET", public+"/readyz", "", ""); status != 200 {
		t.Fatal("worker failure stopped API", status)
	}
	if _, err := directory.Lookup(ctx, tenant.ID, first.ID, "ready"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("lost session stayed routable", err)
	}
	processCDP(t, public, second.ID, issued.APIKey)
	if status, _ := processRequest(t, "DELETE", public+"/sessions/"+second.ID, issued.APIKey, ""); status != 204 {
		t.Fatal("surviving worker deletion failed", status)
	}
	replacement := create()
	if replacement.WorkerID != second.WorkerID {
		t.Fatal("new session routed to failed worker")
	}
	if status, _ := processRequest(t, "DELETE", public+"/sessions/"+replacement.ID, issued.APIKey, ""); status != 204 {
		t.Fatal("replacement deletion failed", status)
	}
	// Restart the fenced worker, then SIGTERM a busy worker. Existing CDP must
	// survive while new placement moves to the other worker.
	workers[lost] = startWorker(lost)
	draining, occupied := create(), create()
	target := 0
	if draining.WorkerID == "b" {
		target = 1
	}
	activeConnection := processCDP(t, public, draining.ID, issued.APIKey)
	if err := workers[target].cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(2 * time.Second)
	for {
		status, data := processRequest(t, "GET", privateOrigins[draining.WorkerID]+"/internal/status", testWorkerCredential, "")
		var state sessions.WorkerStatus
		if status == 200 && json.Unmarshal(data, &state) == nil && state.State == sessions.WorkerDraining {
			break
		}
		if time.Now().After(until) {
			t.Fatal("SIGTERM did not start drain")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		if status, _ := processRequest(t, "POST", privateOrigins[draining.WorkerID]+"/internal/drain", testWorkerCredential, ""); status != 202 {
			t.Fatal("duplicate drain failed", status)
		}
	}
	if status, _ := processRequest(t, "GET", privateOrigins[draining.WorkerID]+"/readyz", testWorkerCredential, ""); status != 503 {
		t.Fatal("draining worker remained ready")
	}
	if status, _ := processRequest(t, "DELETE", public+"/sessions/"+occupied.ID, issued.APIKey, ""); status != 204 {
		t.Fatal("other worker delete failed")
	}
	duringDrain := create()
	if duringDrain.WorkerID == draining.WorkerID {
		t.Fatal("placement selected draining worker")
	}
	assertCDPVersion(t, activeConnection)
	processCDP(t, public, draining.ID, issued.APIKey)
	if status, _ := processRequest(t, "DELETE", public+"/sessions/"+draining.ID, issued.APIKey, ""); status != 204 {
		t.Fatal("draining worker delete failed")
	}
	select {
	case <-workers[target].done:
		if workers[target].err != nil {
			t.Fatal("planned drain failed", workers[target].err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("drained process did not exit")
	}
	if status, _ := processRequest(t, "DELETE", public+"/sessions/"+duringDrain.ID, issued.APIKey, ""); status != 204 {
		t.Fatal("other worker cleanup failed")
	}
	stopRole(t, workers[1-target])
	for _, dir := range profiles {
		entries, err := os.ReadDir(filepath.Clean(dir))
		if err != nil || len(entries) != 0 {
			t.Fatal("worker leaked browser profiles", err)
		}
	}
}
