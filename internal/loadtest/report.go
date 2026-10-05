// Package loadtest exercises authenticated browser lifecycles and measures worker capacity.
package loadtest

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"time"
)

type Config struct {
	APIURL, APIKey                               string
	MetricsURLs                                  []string
	WorkerToken                                  string
	Levels                                       []int
	Rounds                                       int
	Hold, Timeout, SampleInterval, MaxStartupP95 time.Duration
	WorkerMemoryBytes                            uint64
	Headroom                                     float64
	TargetURL                                    string
}

func origin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("URLs must be HTTP(S) origins without credentials, paths, query, or fragment")
	}
	return nil
}
func (c Config) Validate() error {
	if err := origin(c.APIURL); err != nil {
		return err
	}
	if c.APIKey == "" || len(c.Levels) == 0 || c.Rounds < 1 || c.Rounds > 1000 || c.Hold <= 0 || c.Hold > time.Hour || c.Timeout <= 0 || c.Timeout > 5*time.Minute || c.SampleInterval < 100*time.Millisecond || c.SampleInterval > time.Minute || c.MaxStartupP95 <= 0 || math.IsNaN(c.Headroom) || c.Headroom < .1 || c.Headroom >= 1 {
		return fmt.Errorf("invalid load test configuration")
	}
	last := 0
	for _, n := range c.Levels {
		if n <= last || n > 10000 {
			return fmt.Errorf("concurrency levels must increase, between 1 and 10000")
		}
		last = n
	}
	seen := map[string]bool{}
	for _, raw := range c.MetricsURLs {
		if err := origin(raw); err != nil {
			return err
		}
		if seen[raw] {
			return fmt.Errorf("duplicate metrics origin")
		}
		seen[raw] = true
	}
	if len(c.MetricsURLs) > 0 && (len(c.WorkerToken) < 32 || c.Hold < 2*c.SampleInterval) {
		return fmt.Errorf("worker metrics require a worker credential and hold time of at least two scrape intervals")
	}
	u, err := url.Parse(c.TargetURL)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "data") {
		return fmt.Errorf("workload URL must use HTTP(S) or data without credentials")
	}
	return nil
}

type WorkerEvidence struct {
	WorkerID             string `json:"worker_id"`
	PeakActive           int    `json:"peak_active_sessions"`
	PeakReserved         int    `json:"peak_reserved_sessions"`
	PeakBrowserRSS       uint64 `json:"peak_browser_rss_bytes"`
	PeakServiceRSS       uint64 `json:"peak_service_rss_bytes"`
	PeakTotalRSS         uint64 `json:"peak_total_rss_bytes"`
	PeakBrowserProcesses int    `json:"peak_browser_processes"`
	Samples              int    `json:"fresh_memory_samples"`
	SteadySamples        int    `json:"fresh_samples_at_target_concurrency"`
	Unhealthy            bool   `json:"unhealthy_or_missing_samples"`
	lastStamp            float64
}
type Stage struct {
	Concurrency   int                        `json:"concurrency"`
	Attempted     int                        `json:"attempted"`
	Created       int                        `json:"created"`
	Completed     int                        `json:"completed"`
	Failures      map[string]int             `json:"failures"`
	StartupP50    float64                    `json:"startup_p50_seconds"`
	StartupP95    float64                    `json:"startup_p95_seconds"`
	StartupP99    float64                    `json:"startup_p99_seconds"`
	WorkloadP95   float64                    `json:"navigation_p95_seconds"`
	Workers       map[string]*WorkerEvidence `json:"workers"`
	MetricsErrors int                        `json:"metrics_errors"`
	Qualified     bool                       `json:"qualified"`
	Reasons       []string                   `json:"qualification_failures,omitempty"`
}
type Recommendation struct {
	WorkerID          string `json:"worker_id"`
	TestedSessions    int    `json:"highest_qualified_tested_sessions"`
	MemoryEstimate    int    `json:"estimated_sessions_with_memory_headroom"`
	SuggestedCapacity int    `json:"suggested_capacity_upper_bound"`
}
type Report struct {
	StartedAt         time.Time        `json:"started_at"`
	DurationSeconds   float64          `json:"duration_seconds"`
	Rounds            int              `json:"rounds"`
	HoldSeconds       float64          `json:"hold_seconds"`
	WorkerMemoryBytes uint64           `json:"worker_memory_budget_bytes"`
	Headroom          float64          `json:"memory_headroom_fraction"`
	MaxStartupP95     float64          `json:"startup_p95_threshold_seconds"`
	Stages            []Stage          `json:"stages"`
	Recommendations   []Recommendation `json:"recommendations"`
	Notes             []string         `json:"notes"`
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sort.Float64s(samples)
	index := int(math.Ceil(p*float64(len(samples)))) - 1
	return samples[max(0, index)]
}
func qualify(s *Stage, c Config) {
	s.Reasons = nil
	if s.Completed != s.Attempted || len(s.Failures) > 0 {
		s.Reasons = append(s.Reasons, "not all browser lifecycles completed successfully")
	}
	if s.StartupP95 > c.MaxStartupP95.Seconds() {
		s.Reasons = append(s.Reasons, "startup p95 exceeded threshold")
	}
	if len(c.MetricsURLs) == 0 || len(s.Workers) != len(c.MetricsURLs) || s.MetricsErrors > 0 {
		s.Reasons = append(s.Reasons, "worker metrics missing or failed")
	}
	if c.WorkerMemoryBytes == 0 {
		s.Reasons = append(s.Reasons, "worker memory budget not supplied")
	}
	for id, w := range s.Workers {
		if w.Unhealthy || w.SteadySamples < 2 || w.PeakActive == 0 || w.PeakBrowserRSS == 0 {
			s.Reasons = append(s.Reasons, id+": insufficient healthy memory samples at target concurrency")
		}
		if c.WorkerMemoryBytes > 0 && float64(w.PeakTotalRSS) > float64(c.WorkerMemoryBytes)*(1-c.Headroom) {
			s.Reasons = append(s.Reasons, id+": memory budget with headroom exceeded")
		}
	}
	s.Qualified = len(s.Reasons) == 0
}
func recommendations(stages []Stage, c Config) []Recommendation {
	best := map[string]Recommendation{}
	for _, stage := range stages {
		if !stage.Qualified {
			continue
		}
		for id, w := range stage.Workers {
			available := float64(c.WorkerMemoryBytes)*(1-c.Headroom) - float64(w.PeakServiceRSS)
			perBrowser := float64(w.PeakBrowserRSS) / float64(w.PeakActive)
			estimate := max(0, int(math.Floor(available/perBrowser)))
			suggestion := min(estimate, w.PeakActive, stage.Concurrency)
			candidate := Recommendation{id, w.PeakActive, estimate, suggestion}
			// Retain the strongest directly tested bound; never recommend above observed concurrency.
			if suggestion > best[id].SuggestedCapacity || (suggestion == best[id].SuggestedCapacity && w.PeakActive > best[id].TestedSessions) {
				best[id] = candidate
			}
		}
	}
	ids := make([]string, 0, len(best))
	for id := range best {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Recommendation, 0, len(ids))
	for _, id := range ids {
		result = append(result, best[id])
	}
	return result
}
