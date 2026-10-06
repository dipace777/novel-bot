package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"novel-bot/internal/browser"
	"novel-bot/internal/worker"
)

func assertCDPVersion(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":42,"method":"Browser.getVersion"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			ID     int
			Result struct{ Product string }
		}
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID == 42 {
			if reply.Result.Product == "" {
				t.Fatal("CDP version missing")
			}
			return
		}
	}
}

func TestRedisChromiumDrainAcrossWorkers(t *testing.T) {
	path := os.Getenv("TEST_CHROMIUM_PATH")
	if path == "" {
		t.Skip("set TEST_REDIS_URL and TEST_CHROMIUM_PATH for real-browser drain test")
	}
	d := clusterDirectory(t)
	agents := map[string]*worker.Agent{}
	origins := map[string]string{}
	for _, name := range []string{"a", "b"} {
		launcher, err := browser.NewChromium(path, t.TempDir(), true)
		if err != nil {
			t.Fatal(err)
		}
		a, s := clusterWorker(t, d, name, launcher)
		agents[name] = a
		origins[name] = s.URL
	}
	gateway := clusterGateway(t, d)
	id, _ := createBrowserSession(t, gateway)
	r, err := d.Lookup(context.Background(), "owner", id, "ready")
	if err != nil {
		t.Fatal(err)
	}
	conn := dialCluster(t, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/sessions/"+id)
	for i := 0; i < 2; i++ {
		status, _ := processRequest(t, "POST", origins[r.WorkerID]+"/internal/drain", testWorkerCredential, "")
		if status != 202 {
			t.Fatal("drain failed", status)
		}
	}
	if status, _ := processRequest(t, "GET", origins[r.WorkerID]+"/readyz", testWorkerCredential, ""); status != 503 {
		t.Fatal("drain readiness", status)
	}
	if status, _ := processRequest(t, "GET", origins[r.WorkerID]+"/healthz", "", ""); status != 200 {
		t.Fatal("drain liveness", status)
	}
	newID, _ := createBrowserSession(t, gateway)
	placed, err := d.Lookup(context.Background(), "owner", newID, "ready")
	if err != nil || placed.WorkerID == r.WorkerID {
		t.Fatal("draining worker selected", placed, err)
	}
	// Existing transport and a new connection both remain usable during drain.
	assertCDPVersion(t, conn)
	reconnected := dialCluster(t, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/sessions/"+id)
	assertCDPVersion(t, reconnected)
	res := sessionRequest(t, gateway, "DELETE", "/sessions/"+id, "", "owner")
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("drain deletion failed", res.StatusCode)
	}
	select {
	case <-agents[r.WorkerID].Done():
	case <-time.After(3 * time.Second):
		t.Fatal("drained agent did not exit")
	}
	if agents[r.WorkerID].Err() != nil || agents[r.WorkerID].Status().Forced {
		t.Fatal("drain did not finish cleanly")
	}
	if _, err := d.Lookup(context.Background(), "owner", id, "ready"); err == nil {
		t.Fatal("deleted session retained routing")
	}
	res = sessionRequest(t, gateway, "DELETE", "/sessions/"+newID, "", "owner")
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("surviving worker cleanup failed")
	}
	if agents[placed.WorkerID].Ready() != nil {
		t.Fatal("other worker stopped")
	}
	if got := agents[r.WorkerID].Stats().Reserved; got != 0 {
		t.Fatal("local capacity leaked", got)
	}
}
