package observability

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novel-bot/internal/sessions"
)

type workerStub struct{ draining bool }

func (workerStub) Stats() sessions.Stats {
	return sessions.Stats{Capacity: 10, Reserved: 3, Starting: 1, Active: 2}
}
func (workerStub) Ready() error        { return nil }
func (workerStub) PendingCleanup() int { return 1 }
func (w workerStub) Status() sessions.WorkerStatus {
	s := sessions.WorkerStatus{State: sessions.WorkerReady, Accepting: true, LeaseValid: true}
	if w.draining {
		s.State = sessions.WorkerDraining
		s.Accepting = false
	}
	return s
}

func TestDrainMetricsKeepLeaseHealthSeparateFromAdmission(t *testing.T) {
	m := New("draining-worker")
	m.Bind(workerStub{draining: true})
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	for _, metric := range []string{
		`novelbot_worker_ready{worker_id="draining-worker"} 1`,
		`novelbot_worker_draining{worker_id="draining-worker"} 1`,
		`novelbot_worker_accepting_sessions{worker_id="draining-worker"} 0`,
	} {
		if !strings.Contains(response.Body.String(), metric) {
			t.Errorf("missing %s", metric)
		}
	}
}
func TestExporterReportsLifecycleAndWorkerState(t *testing.T) {
	m := New("worker-a")
	m.Bind(workerStub{})
	m.ObserveLaunch(250*time.Millisecond, nil)
	m.ObserveLaunch(time.Second, context.DeadlineExceeded)
	m.Event("cleanup_failure")
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	body := response.Body.String()
	for _, metric := range []string{`novelbot_worker_capacity{worker_id="worker-a"} 10`, `novelbot_worker_reserved_sessions{worker_id="worker-a"} 3`, `novelbot_worker_starting_sessions{worker_id="worker-a"} 1`, `novelbot_worker_active_sessions{worker_id="worker-a"} 2`, `novelbot_worker_ready{worker_id="worker-a"} 1`, `novelbot_worker_cleanup_pending{worker_id="worker-a"} 1`, `novelbot_browser_startup_seconds_count{outcome="success",worker_id="worker-a"} 1`, `novelbot_browser_startup_seconds_count{outcome="timeout",worker_id="worker-a"} 1`, `novelbot_worker_events_total{event="cleanup_failure",worker_id="worker-a"} 1`} {
		if !strings.Contains(body, metric) {
			t.Errorf("missing %s", metric)
		}
	}
	for _, secret := range []string{"tenant_id=", "client_id=", "session_id=", "api_key="} {
		if strings.Contains(body, secret) {
			t.Error("sensitive/high-cardinality label exposed")
		}
	}
	// Each API instance has its own registry, so tests/replicas cannot double-register.
	_ = New("worker-b")
}
func TestProcessRSSParserIncludesChildrenAndConvertsKiB(t *testing.T) {
	rows, err := parsePS(" 10 10 100\n11 10 200\n12 12 500\n")
	if err != nil || len(rows) != 3 || rows[1].RSS != 200*1024 || rows[1].Group != 10 {
		t.Fatal(rows, err)
	}
	for _, invalid := range []string{"10 10", "10 bad 1", "10 10 -1", fmt.Sprintf("10 10 %d", ^uint64(0))} {
		if _, err := parsePS(invalid); err == nil {
			t.Error("invalid memory data accepted")
		}
	}
}

func TestLinuxStatParserUsesProcessGroupAndRSSPageFields(t *testing.T) {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[2] = "123"
	fields[21] = "42"
	input := "77 (a process with ) and spaces) " + strings.Join(fields, " ")
	row, err := parseProcStat(77, input, 4096)
	if err != nil || row.PID != 77 || row.Group != 123 || row.RSS != 42*4096 {
		t.Fatal("incorrect /proc stat accounting", row, err)
	}
	fields[21] = "-1"
	if _, err := parseProcStat(77, "77 (name) "+strings.Join(fields, " "), 4096); err == nil {
		t.Fatal("negative RSS accepted")
	}
}
