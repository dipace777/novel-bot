// Package observability exports worker metrics without tenant or session labels.
package observability

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"novel-bot/internal/sessions"
)

type Worker interface {
	Stats() sessions.Stats
	Ready() error
	PendingCleanup() int
}
type Groups interface{ ProcessGroups() []int }

type Metrics struct {
	registry  *prometheus.Registry
	register  prometheus.Registerer
	startup   *prometheus.HistogramVec
	events    *prometheus.CounterVec
	memory    *memoryCollector
	resources *resourceCollector
}

func New(workerID string) *Metrics {
	registry := prometheus.NewRegistry()
	register := prometheus.WrapRegistererWith(prometheus.Labels{"worker_id": workerID}, registry)
	m := &Metrics{registry: registry, register: register}
	m.startup = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "novelbot_browser_startup_seconds", Help: "Local Chromium launch to CDP-ready latency, including failed launches.", Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 20, 30, 60}}, []string{"outcome"})
	m.events = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "novelbot_worker_events_total", Help: "Browser lifecycle, lease, and directory cleanup events."}, []string{"event"})
	register.MustRegister(m.startup, m.events, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m.memory = newMemoryCollector()
	m.resources = newResourceCollector()
	register.MustRegister(m.memory, m.resources)
	return m
}
func (m *Metrics) ObserveLaunch(elapsed time.Duration, err error) {
	outcome := "success"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		outcome = "timeout"
	case errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "error"
	}
	m.startup.WithLabelValues(outcome).Observe(elapsed.Seconds())
}
func (m *Metrics) Event(name string) { m.events.WithLabelValues(name).Inc() }
func (m *Metrics) Bind(w Worker) {
	for _, setting := range []struct {
		name, help string
		value      func() float64
	}{
		{"novelbot_worker_capacity", "Configured browser slots per worker.", func() float64 { return float64(w.Stats().Capacity) }},
		{"novelbot_worker_reserved_sessions", "Slots including pending launches and stopping browsers.", func() float64 { return float64(w.Stats().Reserved) }},
		{"novelbot_worker_starting_sessions", "Browser launches currently in progress.", func() float64 { return float64(w.Stats().Starting) }},
		{"novelbot_worker_active_sessions", "Ready or stopping local browser sessions.", func() float64 { return float64(w.Stats().Active) }},
		{"novelbot_worker_ready", "1 while the local worker lease is valid; 0 after fencing.", func() float64 {
			if w.Ready() == nil {
				return 1
			}
			return 0
		}},
		{"novelbot_worker_cleanup_pending", "Stopped browser directory releases awaiting retry.", func() float64 { return float64(w.PendingCleanup()) }},
	} {
		m.register.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: setting.name, Help: setting.help}, setting.value))
	}
}
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 3 * time.Second})
}

// Sampling happens outside scrapes, with one OS snapshot for the entire worker.
func (m *Metrics) Sample(ctx context.Context, groups Groups, interval time.Duration) {
	m.memory.mu.Lock()
	m.memory.sample.Interval = interval.Seconds()
	m.memory.mu.Unlock()
	sample := func() {
		m.resources.update()
		ids := groups.ProcessGroups()
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		rows, err := processMemory(bounded)
		cancel()
		if err != nil {
			m.memory.failed()
			m.Event("memory_sample_failure")
			return
		}
		totals := make(map[int]uint64, len(ids))
		for _, id := range ids {
			totals[id] = 0
		}
		var count int
		var service uint64
		for _, row := range rows {
			if row.PID == os.Getpid() {
				service = row.RSS
			}
			if _, ok := totals[row.Group]; ok {
				totals[row.Group] += row.RSS
				count++
			}
		}
		var total, largest uint64
		complete := service > 0
		for _, rss := range totals {
			total += rss
			if rss > largest {
				largest = rss
			}
			if rss == 0 {
				complete = false
			}
		}
		// A browser can stop during sampling. Mark partial snapshots explicitly.
		if !complete {
			m.memory.failed()
			return
		}
		m.memory.mu.Lock()
		m.memory.sample = memorySample{RSS: total, MaxRSS: largest, Processes: count, ServiceRSS: service, OK: true, Stamp: float64(time.Now().UnixNano()) / 1e9, Interval: interval.Seconds()}
		m.memory.mu.Unlock()
	}
	sample()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample()
		}
	}
}
