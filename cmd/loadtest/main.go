package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"novel-bot/internal/config"
	"novel-bot/internal/loadtest"
)

func main() { os.Exit(run()) }
func run() int {
	var cfg loadtest.Config
	var levels, metrics, output string
	var memory uint64
	flag.StringVar(&cfg.APIURL, "url", "http://localhost:8080", "Public API origin (must match returned CDP URLs)")
	flag.StringVar(&levels, "concurrency", "1,2,4", "Increasing concurrent browser levels")
	flag.IntVar(&cfg.Rounds, "rounds", 3, "Complete batches per concurrency level")
	flag.DurationVar(&cfg.Hold, "hold", 15*time.Second, "Time every batch holds all browsers open")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "Deadline per creation/navigation operation")
	flag.DurationVar(&cfg.SampleInterval, "sample-interval", time.Second, "Worker metrics scrape interval")
	flag.StringVar(&metrics, "metrics-urls", "", "Comma-separated private worker origins; scrapes /metrics")
	flag.Uint64Var(&memory, "worker-memory-mib", 0, "Memory budget per worker in MiB; required for capacity qualification")
	flag.Float64Var(&cfg.Headroom, "headroom", .3, "Fraction of worker memory reserved for headroom, at least 0.1")
	flag.DurationVar(&cfg.MaxStartupP95, "max-startup-p95", 2*time.Second, "Maximum acceptable client-observed creation p95")
	flag.StringVar(&cfg.TargetURL, "target-url", loadtest.DefaultTarget(), "CDP navigation workload; default renders 5000 DOM rows")
	flag.StringVar(&output, "output", "loadtest-report.json", "JSON report output")
	flag.StringVar(&cfg.Workload, "workload", "", "Controlled workload: article, feed, dashboard, mixed; empty uses target-url")
	flag.StringVar(&cfg.FixtureURL, "fixture-url", "", "Fixture HTTP origin reachable from browser workers")
	flag.BoolVar(&cfg.RequireResources, "require-container-resources", false, "Qualify with enforced cgroup v2 CPU, memory, and task limits")
	flag.Float64Var(&cfg.WorkerCPUs, "worker-cpus", 4, "Enforced worker CPU budget for container qualification")
	flag.Float64Var(&cfg.MaxCPUUtilization, "max-cpu-utilization", .8, "Maximum steady CPU utilization p95 as fraction of CPU quota")
	flag.Float64Var(&cfg.MaxThrottleFraction, "max-throttle-fraction", .2, "Maximum steady throttled-period fraction p95")
	flag.DurationVar(&cfg.MaxWorkloadP95, "max-navigation-p95", 5*time.Second, "Maximum acceptable navigation/extraction p95")
	flag.DurationVar(&cfg.MaxActionP95, "max-action-p95", time.Second, "Maximum scraping extraction action p95")
	flag.Parse()
	cfg.APIKey = os.Getenv("LOADTEST_API_KEY")
	cfg.WorkerToken = os.Getenv("LOADTEST_WORKER_TOKEN")
	if memory > ^uint64(0)/(1024*1024) {
		fmt.Fprintln(os.Stderr, "Invalid worker memory budget")
		return 2
	}
	cfg.WorkerMemoryBytes = memory * 1024 * 1024
	for _, part := range strings.Split(levels, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			fmt.Fprintln(os.Stderr, "Invalid concurrency levels")
			return 2
		}
		cfg.Levels = append(cfg.Levels, n)
	}
	if metrics != "" {
		for _, raw := range strings.Split(metrics, ",") {
			cfg.MetricsURLs = append(cfg.MetricsURLs, strings.TrimSpace(raw))
		}
	}
	if len(cfg.MetricsURLs) > 0 && cfg.WorkerToken == "" {
		token, err := config.WorkerCredential(os.Getenv("WORKER_AUTH_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "Set LOADTEST_WORKER_TOKEN or source the local .env for worker metrics")
			return 2
		}
		cfg.WorkerToken = token
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "Running browser load test; credentials are read from LOADTEST_API_KEY and LOADTEST_WORKER_TOKEN.")
	cfg.OnStage = func(stage loadtest.Stage) {
		fmt.Printf("concurrency=%d created=%d/%d completed=%d startup_p95=%.3fs qualified=%t\n", stage.Concurrency, stage.Created, stage.Attempted, stage.Completed, stage.StartupP95, stage.Qualified)
	}
	report, err := loadtest.Run(ctx, cfg)
	data, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, "Could not encode report")
		return 1
	}
	if writeErr := os.WriteFile(output, append(data, '\n'), 0600); writeErr != nil {
		fmt.Fprintln(os.Stderr, "Could not write report")
		return 1
	}
	for _, recommendation := range report.Recommendations {
		fmt.Printf("worker=%s tested=%d suggested_capacity_upper_bound=%d\n", recommendation.WorkerID, recommendation.TestedSessions, recommendation.SuggestedCapacity)
	}
	fmt.Printf("Report saved to %s\n", output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, stage := range report.Stages {
		if len(stage.Failures) > 0 || stage.MetricsErrors > 0 || (cfg.WorkerMemoryBytes > 0 && !stage.Qualified) {
			return 1
		}
	}
	return 0
}
