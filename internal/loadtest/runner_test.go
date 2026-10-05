package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testConfig() Config {
	return Config{APIURL: "http://localhost:8080", APIKey: "test-api-credential", WorkerToken: strings.Repeat("w", 32), Levels: []int{1, 2}, Rounds: 1, Hold: 450 * time.Millisecond, Timeout: 2 * time.Second, SampleInterval: 100 * time.Millisecond, MaxStartupP95: time.Second, Headroom: .3, WorkerMemoryBytes: 512 * 1024 * 1024, TargetURL: DefaultTarget()}
}
func benchmarkServer(t *testing.T, createStatus int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var active, deletions atomic.Int32
	var ids atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-api-credential" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if createStatus != 201 {
			w.WriteHeader(createStatus)
			return
		}
		id := fmt.Sprintf("%032x", ids.Add(1))
		active.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "cdp_url": "ws" + strings.TrimPrefix(server.URL, "http") + "/sessions/" + id})
	})
	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) { active.Add(-1); deletions.Add(1); w.WriteHeader(204) })
	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var message struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(data, &message) != nil {
				return
			}
			result := map[string]any{}
			switch message.Method {
			case "Target.createTarget":
				result["targetId"] = "page"
			case "Target.attachToTarget":
				result["sessionId"] = "page-session"
			case "Page.navigate":
				result["loaderId"] = "navigation-loader"
				event, _ := json.Marshal(map[string]any{"method": "Page.lifecycleEvent", "params": map[string]string{"name": "load", "loaderId": "navigation-loader"}})
				if conn.Write(r.Context(), websocket.MessageText, event) != nil {
					return
				}
			case "Runtime.evaluate":
				result["result"] = map[string]string{"value": "complete"}
			}
			reply, _ := json.Marshal(map[string]any{"id": message.ID, "result": result})
			if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("w", 32) {
			http.Error(w, "unauthorized", 401)
			return
		}
		n := active.Load()
		values := map[string]float64{"novelbot_worker_active_sessions": float64(n), "novelbot_worker_reserved_sessions": float64(n), "novelbot_worker_starting_sessions": 0, "novelbot_worker_ready": 1, "novelbot_browser_rss_bytes": float64(n) * 64 * 1024 * 1024, "novelbot_worker_service_rss_bytes": 16 * 1024 * 1024, "novelbot_browser_processes": float64(n) * 4, "novelbot_browser_memory_sample_success": 1, "novelbot_browser_memory_sample_timestamp_seconds": float64(time.Now().UnixNano()) / 1e9, "novelbot_browser_memory_sample_interval_seconds": .1}
		for name, value := range values {
			fmt.Fprintf(w, "# TYPE %s gauge\n%s{worker_id=\"worker-a\"} %g\n", name, name, value)
		}
	})
	return server, &active, &deletions
}
func TestLoadRunnerCreatesNavigatesHoldsCleansAndReportsCapacity(t *testing.T) {
	server, active, deletions := benchmarkServer(t, 201)
	cfg := testConfig()
	cfg.APIURL = server.URL
	cfg.MetricsURLs = []string{server.URL}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 || deletions.Load() != 3 {
		t.Fatal("browser cleanup failed", active.Load(), deletions.Load())
	}
	if len(report.Stages) != 2 || !report.Stages[0].Qualified || !report.Stages[1].Qualified {
		t.Fatalf("stages did not qualify: %+v", report.Stages)
	}
	if len(report.Recommendations) != 1 || report.Recommendations[0].SuggestedCapacity != 2 {
		t.Fatalf("capacity extrapolated beyond tested concurrency: %+v", report.Recommendations)
	}
	data, _ := json.Marshal(report)
	for _, secret := range []string{cfg.APIKey, cfg.WorkerToken, cfg.TargetURL} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credential/workload leaked into report")
		}
	}
}
func TestAdmissionRejectionStopsIncreasingLoad(t *testing.T) {
	server, active, _ := benchmarkServer(t, 429)
	cfg := testConfig()
	cfg.APIURL = server.URL
	cfg.MetricsURLs = nil
	cfg.Hold = time.Millisecond
	cfg.WorkerMemoryBytes = 0
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 || len(report.Stages) != 1 || report.Stages[0].Failures["create_http_429"] != 1 || len(report.Recommendations) != 0 {
		t.Fatalf("rejection ignored: %+v", report)
	}
}
func TestMemoryThresholdNeverSuggestsUntestedOrOverBudgetCapacity(t *testing.T) {
	cfg := testConfig()
	cfg.MetricsURLs = []string{"http://worker"}
	stage := Stage{Concurrency: 4, Attempted: 4, Completed: 4, Failures: map[string]int{}, Workers: map[string]*WorkerEvidence{"worker": {WorkerID: "worker", PeakActive: 4, PeakBrowserRSS: 400 * 1024 * 1024, PeakServiceRSS: 32 * 1024 * 1024, PeakTotalRSS: 432 * 1024 * 1024, SteadySamples: 2}}}
	qualify(&stage, cfg)
	if stage.Qualified || len(recommendations([]Stage{stage}, cfg)) != 0 {
		t.Fatal("over-budget stage generated capacity recommendation")
	}
	cfg.WorkerMemoryBytes = 4 * 1024 * 1024 * 1024
	qualify(&stage, cfg)
	rec := recommendations([]Stage{stage}, cfg)
	if !stage.Qualified || len(rec) != 1 || rec[0].SuggestedCapacity != 4 {
		t.Fatal("memory extrapolation was not capped by direct observation")
	}
}
func TestInvalidConfigAndMetricsCannotGenerateCapacity(t *testing.T) {
	cfg := testConfig()
	cfg.APIURL = "http://user:secret@localhost"
	if cfg.Validate() == nil {
		t.Fatal("credentials in origins accepted")
	}
	cfg = testConfig()
	cfg.Levels = []int{2, 1}
	if cfg.Validate() == nil {
		t.Fatal("decreasing concurrency accepted")
	}
	cfg = testConfig()
	stage := Stage{Attempted: 1, Completed: 1, Failures: map[string]int{}, Workers: map[string]*WorkerEvidence{}}
	qualify(&stage, cfg)
	if stage.Qualified {
		t.Fatal("missing metrics generated recommendation")
	}
}

func TestCancellationCleansKnownBrowserSessions(t *testing.T) {
	server, active, deletions := benchmarkServer(t, 201)
	cfg := testConfig()
	cfg.APIURL = server.URL
	cfg.Levels = []int{1}
	cfg.MetricsURLs = nil
	cfg.WorkerMemoryBytes = 0
	cfg.Hold = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for active.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	report, err := Run(ctx, cfg)
	if err == nil || active.Load() != 0 || deletions.Load() != 1 || len(report.Recommendations) != 0 {
		t.Fatalf("cancellation did not clean browsers: %v %+v", err, report)
	}
}
