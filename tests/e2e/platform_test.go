//go:build e2e

package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"novel-bot/api"
	"novel-bot/internal/auth"
	"novel-bot/internal/sessions"
	redisstore "novel-bot/internal/storage/redis"
	"novel-bot/internal/testutil"
)

type platform struct {
	t          *testing.T
	infra      *testutil.Infrastructure
	credential string
	api        tc.Container
	workers    map[string]tc.Container
	origins    map[string]string
	directory  *redisstore.Directory
	key, owner string
	contract   routers.Router
}

func TestContainerPlatformLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s, err := testutil.Start(ctx, filepath.Join(os.Getenv("TEST_ARTIFACT_DIR"), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	p := &platform{t: t, infra: s, credential: testutil.Secret(), workers: map[string]tc.Container{}, origins: map[string]string{}}
	s.AddSecret(p.credential)
	t.Cleanup(func() {
		if t.Failed() {
			if err := s.SaveLogs(filepath.Join(os.Getenv("TEST_ARTIFACT_DIR"), "logs")); err != nil {
				t.Errorf("save redacted logs: %s", s.Redact(err.Error()))
			}
		}
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	document, err := openapi3.NewLoader().LoadFromData(api.Specification)
	if err != nil {
		t.Fatal(err)
	}
	// The public server is ephemeral. Validate the original document, then match
	// response operations by path instead of its documentation-relative server URL.
	if err := document.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	document.Servers = nil
	p.contract, err = legacy.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("TEST_REPO_ROOT")
	if root == "" {
		t.Fatal("run make test-e2e from the repository root")
	}
	profile, err := os.ReadFile(filepath.Join(root, "deploy", "chromium-seccomp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compact strings.Builder
	var profileJSON any
	if err := json.Unmarshal(profile, &profileJSON); err != nil {
		t.Fatal(err)
	}
	compactBytes, _ := json.Marshal(profileJSON)
	compact.Write(compactBytes)
	image := func(role string) string {
		if v := os.Getenv("TEST_" + strings.ToUpper(role) + "_IMAGE"); v != "" {
			return v
		}
		return "novelbot-test/" + role + ":local"
	}
	migration, err := s.Run(ctx, "migrate", tc.ContainerRequest{Image: image("migrate"), Env: map[string]string{"DATABASE_URL": s.InternalDatabaseURL, "DB_MAX_CONNS": "2"}, WaitingFor: wait.ForExit().WithExitTimeout(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	p.awaitExit(migration, 0)
	namespace := "e2e_" + testutil.Secret()[:16]
	for _, id := range []string{"worker-a", "worker-b"} {
		c, err := s.Run(ctx, id, tc.ContainerRequest{
			Image: image("worker"), ExposedPorts: []string{"8090/tcp"},
			Env:                map[string]string{"REDIS_URL": s.InternalRedisURL, "REDIS_NAMESPACE": namespace, "WORKER_AUTH_TOKEN": p.credential, "WORKER_ID": id, "WORKER_HTTP_ADDR": ":8090", "WORKER_URL": "http://" + id + ":8090", "WORKER_LEASE_TTL": "3s", "WORKER_DRAIN_TIMEOUT": "20s", "WORKER_STOP_GRACE_PERIOD": "50s", "BROWSER_MAX_SESSIONS": "1", "BROWSER_SESSION_TTL": "2m", "BROWSER_STARTUP_TIMEOUT": "20s", "BROWSER_HEADLESS": "true", "METRICS_SAMPLE_INTERVAL": "1s"},
			HostConfigModifier: testutil.WorkerHostConfig([]byte(compact.String())),
			WaitingFor:         wait.ForHTTP("/readyz").WithPort("8090/tcp").WithHeaders(map[string]string{"Authorization": "Bearer " + p.credential}).WithStartupTimeout(30 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		p.workers[id] = c
		p.origins[id], err = testutil.Address(ctx, c, "8090/tcp")
		if err != nil {
			t.Fatal(err)
		}
		inspect, err := c.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if inspect.Config.User == "" || inspect.Config.User == "root" || inspect.Config.User == "0" || inspect.HostConfig.Privileged || len(inspect.HostConfig.CapAdd) != 0 {
			t.Fatal("worker sandbox privileges drifted")
		}
		if inspect.HostConfig.SecurityOpt[0] != "no-new-privileges:true" || inspect.HostConfig.Resources.Memory != 4<<30 {
			t.Fatal("worker security/resource policy drifted")
		}
	}
	pepper := base64.StdEncoding.EncodeToString([]byte(testutil.Secret()))
	s.AddSecret(pepper)
	p.api, err = s.Run(ctx, "api", tc.ContainerRequest{Image: image("api"), ExposedPorts: []string{"8080/tcp"}, Env: map[string]string{"HTTP_ADDR": ":8080", "DATABASE_URL": s.InternalDatabaseURL, "DB_MAX_CONNS": "3", "REDIS_URL": s.InternalRedisURL, "REDIS_NAMESPACE": namespace, "WORKER_AUTH_TOKEN": p.credential, "API_KEY_PEPPER": pepper, "AUTH_TIMEOUT": "5s", "SESSION_STARTUP_TIMEOUT": "20s"}, WaitingFor: wait.ForHTTP("/readyz").WithPort("8080/tcp").WithStartupTimeout(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	p.origins["api"], err = testutil.Address(ctx, p.api, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	r, err := redisstore.Open(ctx, s.RedisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	p.directory, err = redisstore.NewDirectory(r, namespace)
	if err != nil {
		t.Fatal(err)
	}
	p.request("POST", "api", "/v1/auth/register", "", `{"name":"e2e","email":"e2e@example.test","password":"e2e correct horse battery staple"}`, 201, nil)
	var login auth.Login
	p.request("POST", "api", "/v1/auth/login", "", `{"email":"e2e@example.test","password":"e2e correct horse battery staple"}`, 200, &login)
	s.AddSecret(login.AccessToken)
	var issued auth.IssuedKey
	p.request("POST", "api", "/v1/api-keys", login.AccessToken, `{"name":"e2e"}`, 201, &issued)
	p.key, p.owner = issued.APIKey, login.User.ClientID
	s.AddSecret(p.key)
	p.request("GET", "api", "/v1/auth/me", login.AccessToken, "", 200, nil)
	p.request("GET", "api", "/v1/api-keys", login.AccessToken, "", 200, nil)
	p.request("GET", "api", "/v1/whoami", p.key, "", 200, nil)
	p.request("GET", "api", "/healthz", "", "", 200, nil)
	p.request("GET", "api", "/readyz", "", "", 200, nil)
	p.request("POST", "api", "/sessions", "", "{}", 401, nil)
	for id := range p.workers {
		p.request("GET", id, "/internal/status", "", "", 401, nil)
	}
	one, two := p.create(), p.create()
	r1, err := p.directory.Lookup(ctx, p.owner, one, "ready")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := p.directory.Lookup(ctx, p.owner, two, "ready")
	if err != nil {
		t.Fatal(err)
	}
	if r1.WorkerID == r2.WorkerID {
		t.Fatal("one-slot workers did not distribute sessions")
	}
	oneCDP := p.connect(one)
	p.sandbox(oneCDP)
	oneCDP.CloseNow()
	p.connect(two).CloseNow()
	// A public API process restart must not own or terminate browser processes.
	stop := 3 * time.Second
	if err := p.api.Stop(ctx, &stop); err != nil {
		t.Fatal(err)
	}
	if err := p.api.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p.origins["api"], err = testutil.Address(ctx, p.api, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p.awaitReady("api")
	live := p.connect(one)
	t.Log("API restart preserved session reconnect")
	for i := 0; i < 2; i++ {
		p.request("POST", r1.WorkerID, "/internal/drain", p.credential, "", 202, nil)
	}
	p.request("GET", r1.WorkerID, "/readyz", p.credential, "", 503, nil)
	p.request("GET", r1.WorkerID, "/healthz", "", "", 200, nil)
	p.request("DELETE", "api", "/sessions/"+two, p.key, "", 204, nil)
	three := p.create()
	r3, err := p.directory.Lookup(ctx, p.owner, three, "ready")
	if err != nil || r3.WorkerID != r2.WorkerID {
		t.Fatalf("new placement during drain: worker=%s error=%v", r3.WorkerID, err)
	}
	p.rpc(live, 7, "Browser.getVersion", nil, "")
	p.connect(one).CloseNow()
	p.request("DELETE", "api", "/sessions/"+one, p.key, "", 204, nil)
	p.awaitExit(p.workers[r1.WorkerID], 0)
	live.CloseNow()
	t.Log("drain preserved CDP/reconnect/delete; new placement used the other worker; exit was clean")
	// Restart the same filesystem to prove normal drain removed browser profiles.
	if err := p.workers[r1.WorkerID].Start(ctx); err != nil {
		t.Fatal(err)
	}
	p.origins[r1.WorkerID], err = testutil.Address(ctx, p.workers[r1.WorkerID], "8090/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p.awaitReady(r1.WorkerID)
	p.assertProfilesEmpty(p.workers[r1.WorkerID])
	// Retire the busy worker lease: its next heartbeat must fence and kill Chromium.
	if err := p.directory.Unregister(ctx, sessions.Worker{ID: r3.WorkerID, Token: r3.WorkerToken}); err != nil {
		t.Fatal(err)
	}
	p.awaitExit(p.workers[r3.WorkerID], 1)
	p.request("GET", "api", "/sessions/"+three, p.key, "", 404, nil)
	four := p.create() // Reconciles the lost incarnation and reuses its tenant quota.
	p.request("DELETE", "api", "/sessions/"+four, p.key, "", 204, nil)
	if err := p.workers[r3.WorkerID].Start(ctx); err != nil {
		t.Fatal(err)
	}
	p.origins[r3.WorkerID], err = testutil.Address(ctx, p.workers[r3.WorkerID], "8090/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p.awaitReady(r3.WorkerID)
	p.assertProfilesEmpty(p.workers[r3.WorkerID])
	t.Log("lease loss fenced the busy worker, removed profiles, and allowed quota reuse")
	p.request("DELETE", "api", "/v1/api-keys/"+issued.Key.ID, login.AccessToken, "", 204, nil)
	p.request("GET", "api", "/v1/whoami", p.key, "", 401, nil)
	p.request("POST", "api", "/v1/auth/logout", login.AccessToken, "", 204, nil)
}

func (p *platform) request(method, role, path, key, body string, want int, target any) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, p.origins[role]+path, strings.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(p.infra.Redact(err.Error()))
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		p.t.Fatal(err)
	}
	if res.StatusCode != want {
		p.t.Fatalf("%s %s %s: got %d want %d: %s", method, role, path, res.StatusCode, want, p.infra.Redact(string(data)))
	}
	if role == "api" {
		route, params, err := p.contract.FindRoute(req)
		if err != nil {
			p.t.Fatal(err)
		}
		input := &openapi3filter.ResponseValidationInput{RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route}, Status: res.StatusCode, Header: res.Header, Options: &openapi3filter.Options{IncludeResponseStatus: true}}
		if err := openapi3filter.ValidateResponse(ctx, input.SetBodyBytes(data)); err != nil {
			p.t.Fatalf("OpenAPI %s %s response: %s", method, path, p.infra.Redact(err.Error()))
		}
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			p.t.Fatal(err)
		}
	}
}

func (p *platform) create() string {
	p.t.Helper()
	var response struct{ ID string }
	p.request("POST", "api", "/sessions", p.key, "{}", 201, &response)
	if response.ID == "" {
		p.t.Fatal("missing session ID")
	}
	return response.ID
}

func (p *platform) connect(id string) *websocket.Conn {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.origins["api"], "http")+"/sessions/"+id, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + p.key}}})
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { c.CloseNow() })
	p.rpc(c, 1, "Browser.getVersion", nil, "")
	return c
}

func (p *platform) rpc(c *websocket.Conn, id int, method string, params any, sessionID string) json.RawMessage {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		command["sessionId"] = sessionID
	}
	data, _ := json.Marshal(command)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		p.t.Fatal(err)
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			p.t.Fatal(err)
		}
		var reply struct {
			ID     int
			Result json.RawMessage
			Error  json.RawMessage
		}
		if err := json.Unmarshal(data, &reply); err != nil {
			p.t.Fatal(err)
		}
		if reply.ID == id {
			if len(reply.Error) > 0 {
				p.t.Fatalf("CDP %s failed: %s", method, reply.Error)
			}
			return reply.Result
		}
	}
}

func (p *platform) sandbox(c *websocket.Conn) {
	p.t.Helper()
	var target struct{ TargetID string }
	json.Unmarshal(p.rpc(c, 2, "Target.createTarget", map[string]string{"url": "chrome://sandbox"}, ""), &target)
	var attached struct{ SessionID string }
	json.Unmarshal(p.rpc(c, 3, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, ""), &attached)
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var result struct{ Result struct{ Value string } }
		json.Unmarshal(p.rpc(c, 4, "Runtime.evaluate", map[string]any{"expression": "document.body && document.body.innerText", "returnByValue": true}, attached.SessionID), &result)
		last = result.Result.Value
		namespaces := regexp.MustCompile(`Namespace sandbox\s+Yes`).MatchString(last) || (regexp.MustCompile(`PID namespaces\s+Yes`).MatchString(last) && regexp.MustCompile(`Network namespaces\s+Yes`).MatchString(last))
		if namespaces && regexp.MustCompile(`Seccomp-BPF sandbox\s+Yes`).MatchString(last) {
			p.t.Log("Chromium renderer namespace and seccomp sandboxes enabled")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatalf("Chromium renderer namespace/seccomp sandbox verification failed: %s", last)
}

func (p *platform) awaitReady(role string) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", p.origins[role]+"/readyz", nil)
		if role != "api" {
			req.Header.Set("Authorization", "Bearer "+p.credential)
		}
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				cancel()
				return
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatalf("%s readiness timed out", role)
}

func (p *platform) awaitExit(c tc.Container, want int) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		state, err := c.State(ctx)
		if err != nil {
			p.t.Fatal(err)
		}
		if !state.Running {
			if state.ExitCode != want {
				p.t.Fatalf("worker exit: got %d want %d", state.ExitCode, want)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatal("worker did not exit before timeout")
}

func (p *platform) assertProfilesEmpty(c tc.Container) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, reader, err := c.Exec(ctx, []string{"sh", "-c", `test -z "$(find /tmp -maxdepth 1 -name 'novel-bot-browser-*' -print -quit)"`})
	if err != nil {
		p.t.Fatal(err)
	}
	io.Copy(io.Discard, reader)
	if code != 0 {
		p.t.Fatal(fmt.Sprintf("worker retained browser profiles (exit %d)", code))
	}
}
