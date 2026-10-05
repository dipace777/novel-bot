package loadtest

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runner struct {
	cfg    Config
	client *http.Client
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxIdleConns: 100, MaxIdleConnsPerHost: 100, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func Run(ctx context.Context, c Config) (Report, error) {
	if err := c.Validate(); err != nil {
		return Report{}, err
	}
	started := time.Now()
	report := Report{Stages: []Stage{}, MaxActionP95: c.MaxActionP95.Seconds(), Workload: c.Workload, FixtureVersion: "", RequireResources: c.RequireResources, WorkerCPUs: c.WorkerCPUs, MaxCPUUtilization: c.MaxCPUUtilization, MaxThrottleFraction: c.MaxThrottleFraction, MaxWorkloadP95: c.MaxWorkloadP95.Seconds(), StartedAt: started.UTC(), Rounds: c.Rounds, HoldSeconds: c.Hold.Seconds(), WorkerMemoryBytes: c.WorkerMemoryBytes, Headroom: c.Headroom, MaxStartupP95: c.MaxStartupP95.Seconds(), Recommendations: []Recommendation{}, Notes: []string{
		"Startup latency is client-observed POST /sessions latency for successful launches; worker histograms measure local CDP readiness.",
		"RSS includes Chromium children in owned process groups; shared pages can be double-counted and short peaks between samples can be missed.",
		"Unacknowledged launches after transport failure rely on worker cleanup or the server session TTL; known sessions are explicitly deleted.",
		"Suggested capacity is capped at directly tested concurrency and depends on the workload, hardware, isolation, and supplied memory budget.",
		"Use isolated workers and a dedicated test tenant; quotas remain enabled. Replay customer workloads and measure sustained behavior on target production hardware before production sizing.",
	}}
	if c.Workload != "" {
		report.FixtureVersion = FixtureVersion
	}
	r := runner{c, newHTTPClient(c.Timeout)}
	defer r.client.CloseIdleConnections()
	for _, level := range c.Levels {
		stage := Stage{Concurrency: level, Failures: map[string]int{}, Workers: map[string]*WorkerEvidence{}, Workloads: map[string]int{}}
		// Do not mix unrelated browser traffic into a capacity recommendation.
		if len(c.MetricsURLs) > 0 {
			samples, err := scrapeAll(ctx, r.client, c)
			if err != nil {
				return report, err
			}
			for _, sample := range samples {
				if sample.Active != 0 || sample.Reserved != 0 {
					return report, fmt.Errorf("load testing requires idle, isolated workers")
				}
				stage.Workers[sample.ID] = &WorkerEvidence{WorkerID: sample.ID, lastStamp: sample.Stamp, resourceEvidence: resourceEvidence{previous: sample.Resource}}
			}
		}
		var startups, navigations []float64
		for round := 0; round < c.Rounds; round++ {
			r.round(ctx, &stage, &startups, &navigations, round)
			if len(stage.Failures) > 0 || ctx.Err() != nil {
				break
			}
			if len(c.MetricsURLs) > 0 {
				// Wait for process cleanup and a fresh idle memory sample before the next batch.
				idle, cancel := context.WithTimeout(ctx, c.Timeout)
				err := r.waitIdle(idle)
				cancel()
				if err != nil {
					stage.Failures["worker_not_idle_after_cleanup"]++
					break
				}
			}
		}
		stage.StartupP50 = percentile(startups, .5)
		stage.StartupP95 = percentile(startups, .95)
		stage.StartupP99 = percentile(startups, .99)
		stage.WorkloadP95 = percentile(navigations, .95)
		qualify(&stage, c)
		report.Stages = append(report.Stages, stage)
		if c.OnStage != nil {
			c.OnStage(stage)
		}
		// Stop increasing pressure after failed lifecycles or memory/latency thresholds.
		if len(stage.Failures) > 0 || stage.StartupP95 > c.MaxStartupP95.Seconds() {
			break
		}
		exceeded := false
		for _, w := range stage.Workers {
			if !c.RequireResources && c.WorkerMemoryBytes > 0 && float64(w.PeakTotalRSS) > float64(c.WorkerMemoryBytes)*(1-c.Headroom) {
				exceeded = true
			}
		}
		if c.RequireResources && !stage.Qualified {
			exceeded = true
		}
		if c.MaxWorkloadP95 > 0 && stage.WorkloadP95 > c.MaxWorkloadP95.Seconds() {
			exceeded = true
		}
		if c.MaxActionP95 > 0 && stage.ActionP95 > c.MaxActionP95.Seconds() {
			exceeded = true
		}
		if exceeded || ctx.Err() != nil {
			break
		}
	}
	report.DurationSeconds = time.Since(started).Seconds()
	report.Recommendations = recommendations(report.Stages, c)
	return report, ctx.Err()
}
func (r runner) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.cfg.APIURL, "/")+path, body)
	if err != nil {
		return nil, fmt.Errorf("invalid API request")
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed")
	}
	return response, nil
}
func (r runner) create(ctx context.Context) (string, string, string) {
	response, err := r.request(ctx, "POST", "/sessions", bytes.NewBufferString("{}"))
	if err != nil {
		return "", "", "create_transport"
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		code := fmt.Sprintf("create_http_%d", response.StatusCode)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) == nil {
			switch body.Error.Code {
			case "tenant_session_limit_reached", "session_rate_limit_exceeded", "session_capacity_reached", "browser_timeout", "browser_unavailable", "unauthorized":
				code += "_" + body.Error.Code
			}
		}
		return "", "", code
	}
	var body struct {
		ID     string `json:"id"`
		CDPURL string `json:"cdp_url"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&body) != nil || len(body.ID) != 32 {
		return "", "", "create_invalid_response"
	}
	if _, err := hex.DecodeString(body.ID); err != nil {
		return "", "", "create_invalid_response"
	}
	return body.ID, body.CDPURL, ""
}
func (r runner) remove(id string) bool {
	for attempt := 0; attempt < 3; attempt++ {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		response, err := r.request(cleanup, "DELETE", "/sessions/"+id, nil)
		if err == nil {
			_ = response.Body.Close()
			cancel()
			if response.StatusCode == 204 || response.StatusCode == 404 {
				return true
			}
		} else {
			cancel()
		}
	}
	return false
}
func (r runner) round(ctx context.Context, stage *Stage, startups, navigations *[]float64, round int) {
	var mu sync.Mutex
	var waiting, finished sync.WaitGroup
	waiting.Add(stage.Concurrency)
	finished.Add(stage.Concurrency)
	begin, done := make(chan struct{}), make(chan struct{})
	var holding atomic.Bool
	var actions []float64
	previousResourceSteady := map[string]int{}
	previousSteady := map[string]int{}
	for id, w := range stage.Workers {
		w.previousSteady = false // Startup/cleanup between rounds must not count as steady CPU.
		previousSteady[id] = w.SteadySamples
		previousResourceSteady[id] = w.ResourceSteadySamples
	}
	telemetry, cancelTelemetry := context.WithCancel(ctx)
	telemetryDone := make(chan struct{})
	go func() {
		defer close(telemetryDone)
		if len(r.cfg.MetricsURLs) == 0 {
			return
		}
		ticker := time.NewTicker(r.cfg.SampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-telemetry.Done():
				return
			case <-ticker.C:
				samples, err := scrapeAll(telemetry, r.client, r.cfg)
				mu.Lock()
				if err != nil {
					if telemetry.Err() == nil {
						stage.MetricsErrors++
					}
				} else {
					observeSamples(stage, samples, holding.Load())
				}
				mu.Unlock()
			}
		}
	}()
	fail := func(code string) { mu.Lock(); stage.Failures[code]++; mu.Unlock() }
	stage.Attempted += stage.Concurrency
	for i := 0; i < stage.Concurrency; i++ {
		go func(index int) {
			defer finished.Done()
			signaled := false
			signal := func() {
				if !signaled {
					waiting.Done()
					signaled = true
				}
			}
			defer signal()
			creation, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
			started := time.Now()
			id, endpoint, code := r.create(creation)
			elapsed := time.Since(started).Seconds()
			cancel()
			if code != "" {
				fail(code)
				return
			}
			mu.Lock()
			stage.Created++
			*startups = append(*startups, elapsed)
			mu.Unlock()
			defer func() {
				if !r.remove(id) {
					fail("delete_failed")
				}
			}()
			workload, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
			navigationStart := time.Now()
			scenarioCfg := r.cfg
			name, target := r.cfg.scenario(index + round)
			scenarioCfg.TargetURL = target
			browser, session, err := attach(workload, scenarioCfg, endpoint, r.client)
			if err == nil && r.cfg.Workload != "" {
				err = browser.extract(workload, session)
			}
			cancel()
			if err != nil {
				if browser != nil {
					browser.conn.CloseNow()
				}
				fail("cdp_navigation_failed")
				return
			}
			defer browser.conn.CloseNow()
			mu.Lock()
			*navigations = append(*navigations, time.Since(navigationStart).Seconds())
			stage.Workloads[name]++
			mu.Unlock()
			signal()
			select {
			case <-ctx.Done():
				fail("canceled")
				return
			case <-begin:
			}
			var action func(context.Context) error
			if r.cfg.Workload != "" {
				action = func(ctx context.Context) error {
					started := time.Now()
					err := browser.extract(ctx, session)
					if err == nil {
						mu.Lock()
						stage.Actions++
						actions = append(actions, time.Since(started).Seconds())
						mu.Unlock()
					}
					return err
				}
			}
			if err := browser.hold(ctx, done, r.cfg.Timeout, session, action); err != nil {
				fail("cdp_hold_failed")
				return
			}
			mu.Lock()
			stage.Completed++
			mu.Unlock()
		}(i)
	}
	waiting.Wait()
	holding.Store(true)
	close(begin)
	timer := time.NewTimer(r.cfg.Hold)
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	timer.Stop()
	holding.Store(false)
	close(done)
	finished.Wait()
	cancelTelemetry()
	<-telemetryDone
	if len(r.cfg.MetricsURLs) > 0 {
		for id, w := range stage.Workers {
			if r.cfg.RequireResources && w.ResourceSteadySamples-previousResourceSteady[id] < 2 {
				w.ResourceUnhealthy = true
			}
			if w.SteadySamples-previousSteady[id] < 2 {
				w.Unhealthy = true
			}
		}
	}
	stage.ActionP95 = max(stage.ActionP95, percentile(actions, .95))
}
func (r runner) waitIdle(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.SampleInterval)
	defer ticker.Stop()
	for {
		samples, err := scrapeAll(ctx, r.client, r.cfg)
		if err != nil {
			return err
		}
		idle := true
		for _, sample := range samples {
			idle = idle && sample.Active == 0 && sample.Reserved == 0
		}
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
