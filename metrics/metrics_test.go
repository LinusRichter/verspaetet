package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerExposesMetrics(t *testing.T) {
	// Touch collectors so at least one series exists for each.
	IRISFetches.WithLabelValues("fchg", "ok").Inc()
	IRISFetches.WithLabelValues("plan", "http_429").Inc()
	IRISFetchDuration.WithLabelValues("fchg").Observe(0.3)
	IRISTokens.Set(42)
	IRISTokensMax.Set(60)
	StopEventsPersisted.Add(7)
	WorkerTasks.WithLabelValues("board:fetch", "ok").Inc()
	SchedulerEnqueued.Add(3)
	ExportLastSuccess.Set(1_700_000_000)
	QueueTasks.WithLabelValues("default", "pending").Set(12)

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	buf := make([]byte, 0)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	body := string(buf)

	for _, want := range []string{
		"verspaetet_iris_fetch_total",
		"verspaetet_iris_fetch_duration_seconds",
		"verspaetet_iris_ratelimit_tokens",
		"verspaetet_iris_ratelimit_max",
		"verspaetet_stop_events_persisted_total",
		"verspaetet_worker_tasks_total",
		"verspaetet_scheduler_tasks_enqueued_total",
		"verspaetet_export_last_success_timestamp",
		"verspaetet_asynq_queue_tasks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}

	// Label values survive the round trip.
	if !strings.Contains(body, `endpoint="fchg"`) || !strings.Contains(body, `result="http_429"`) {
		t.Error("expected labelled samples missing")
	}
	if !strings.Contains(body, fmt.Sprintf("verspaetet_stop_events_persisted_total 7")) {
		t.Error("counter value not exposed correctly")
	}
}