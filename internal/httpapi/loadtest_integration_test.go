package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"novel-bot/internal/browser"
	"novel-bot/internal/limits"
	"novel-bot/internal/loadtest"
	"novel-bot/internal/observability"
	"novel-bot/internal/sessions"
	"novel-bot/internal/worker"
)

// Runs a deliberately small benchmark on isolated Redis metadata and profiles.
func TestChromiumLoadTestCapacityReport(t *testing.T) {
	executable := os.Getenv("TEST_CHROMIUM_PATH")
	if executable == "" {
		t.Skip("set TEST_CHROMIUM_PATH and TEST_REDIS_URL for a real browser capacity smoke test")
	}
	d := clusterDirectory(t)
	profiles := t.TempDir()
	launcher, err := browser.NewChromium(executable, profiles)
	if err != nil {
		t.Fatal(err)
	}
	private := httptest.NewUnstartedServer(nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.New("capacity-smoke")
	agent, err := worker.NewAgent(context.Background(), d, launcher, sessions.Worker{ID: "capacity-smoke", URL: "http://" + private.Listener.Addr().String()}, sessions.Options{MaxSessions: 3, TTL: time.Minute, StartupTimeout: 15 * time.Second, Observer: metrics}, 5*time.Second, logger)
	if err != nil {
		private.Close()
		t.Fatal(err)
	}
	metrics.Bind(agent)
	private.Config.Handler = NewWorkerRouter(agent, testWorkerCredential, logger, metrics.Handler())
	private.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			t.Error(err)
		}
		private.Close()
	})
	sampleCtx, stop := context.WithCancel(context.Background())
	sampleDone := make(chan struct{})
	go func() { defer close(sampleDone); metrics.Sample(sampleCtx, launcher, 250*time.Millisecond) }()
	t.Cleanup(func() { stop(); <-sampleDone })
	provider := limits.ProviderFunc(func(context.Context, string) (limits.Policy, error) { return limits.DefaultPolicy(), nil })
	public := clusterGateway(t, d, provider)
	reportPath := os.Getenv("TEST_LOADTEST_REPORT")
	if reportPath == "" {
		reportPath = filepath.Join(t.TempDir(), "report.json")
	}
	var report loadtest.Report
	binary := os.Getenv("TEST_LOADTEST_BINARY")
	if binary != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "--url", public.URL, "--metrics-urls", private.URL, "--concurrency", "1,2,3", "--rounds", "1", "--hold", "2s", "--sample-interval", "250ms", "--worker-memory-mib", "4096", "--max-startup-p95", "15s", "--output", reportPath)
		command.Env = append(os.Environ(), "LOADTEST_API_KEY=owner", "LOADTEST_WORKER_TOKEN="+testWorkerCredential)
		data, err := command.CombinedOutput()
		if err != nil {
			var exit *exec.ExitError
			// A threshold crossing is an expected outcome of a capacity sweep, and
			// must still produce a report containing the passing lower levels.
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("loadtest CLI failed: %v\n%s", err, data)
			}
		}
		t.Log(string(data))
		data, err = os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(data, &report) != nil {
			t.Fatal("invalid CLI report")
		}
	} else {
		cfg := loadtest.Config{APIURL: public.URL, APIKey: "owner", MetricsURLs: []string{private.URL}, WorkerToken: testWorkerCredential, Levels: []int{1, 2, 3}, Rounds: 1, Hold: 2 * time.Second, Timeout: 20 * time.Second, SampleInterval: 250 * time.Millisecond, MaxStartupP95: 15 * time.Second, WorkerMemoryBytes: 4 * 1024 * 1024 * 1024, Headroom: .3, TargetURL: loadtest.DefaultTarget()}
		report, err = loadtest.Run(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(reportPath, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(report.Stages) == 0 || len(report.Stages) > 3 || len(report.Recommendations) != 1 || report.Recommendations[0].SuggestedCapacity < 1 || report.Recommendations[0].SuggestedCapacity > 3 {
		t.Fatalf("benchmark did not qualify tested levels: %+v", report)
	}
	for _, stage := range report.Stages {
		if len(stage.Failures) > 0 || stage.MetricsErrors > 0 || stage.Completed != stage.Concurrency {
			t.Fatalf("browser load stage failed: %+v", stage)
		}
		for _, evidence := range stage.Workers {
			if evidence.SteadySamples < 2 || evidence.PeakBrowserRSS == 0 || evidence.Unhealthy {
				t.Fatalf("browser memory measurement incomplete: %+v", evidence)
			}
			t.Logf("concurrency=%d startup_p95=%.3fs peak_browser_rss_mib=%.1f service_rss_mib=%.1f sampled_browser_processes=%s", stage.Concurrency, stage.StartupP95, float64(evidence.PeakBrowserRSS)/(1024*1024), float64(evidence.PeakServiceRSS)/(1024*1024), strconv.Itoa(evidence.PeakBrowserProcesses))
		}
	}
	if agent.Stats().Reserved != 0 || len(launcher.ProcessGroups()) != 0 {
		t.Fatal("benchmark left browser processes running")
	}
	files, err := os.ReadDir(profiles)
	if err != nil || len(files) != 0 {
		t.Fatal("benchmark leaked profiles")
	}
}
