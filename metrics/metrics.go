// Package metrics centralises all verspaetet Prometheus instrumentation.
// App code only touches the exported counters/gauges here; the /metrics
// endpoint and any listener wiring live in this file too, so monitoring
// concerns never leak into business logic.
//
// Metric naming: verspaetet_<subsystem>_<name>_<unit>.
package metrics

import (
	"log"
	"net"
	"net/http"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ── IRIS fetching (activities/iris.go) ────────────────────────────────────

// IRISFetches counts every outbound IRIS request. endpoint is "fchg" or
// "plan" (the only two documents we fetch); result is "ok" or a failure
// class ("http_429", "http_400", "network_error", "decode_error", "limiter").
var IRISFetches = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "verspaetet_iris_fetch_total",
	Help: "IRIS API requests by endpoint and result.",
}, []string{"endpoint", "result"})

// IRISFetchDuration observes request latency per endpoint.
var IRISFetchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "verspaetet_iris_fetch_duration_seconds",
	Help:    "IRIS request latency by endpoint.",
	Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 30},
}, []string{"endpoint"})

// IRISTokens / IRISTokensMax snapshot the shared token bucket after each
// fetch (see ratelimit.Limiter.Tokens). Snapshot-per-fetch is accurate
// enough to show how close the fleet is to the request budget.
var (
	IRISTokens = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "verspaetet_iris_ratelimit_tokens",
		Help: "Tokens currently available in the IRIS rate-limit bucket.",
	})
	IRISTokensMax = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "verspaetet_iris_ratelimit_max",
		Help: "IRIS rate-limit bucket capacity (requests/minute).",
	})
)

// ── Persistence (activities/process.go) ───────────────────────────────────

// StopEventsPersisted counts rows actually inserted into stop_events
// (post-dedup — unchanged snapshots are NOT counted).
var StopEventsPersisted = promauto.NewCounter(prometheus.CounterOpts{
	Name: "verspaetet_stop_events_persisted_total",
	Help: "Stop event snapshots inserted into Postgres (dedup applied).",
})

// ── Worker tasks ──────────────────────────────────────────────────────────

// WorkerTasks counts task handler completions by type and result
// ("ok", "error", "skip_retry", "no_iris" ...).
var WorkerTasks = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "verspaetet_worker_tasks_total",
	Help: "Task handler completions by type and result.",
}, []string{"type", "result"})

// ── Scheduler ─────────────────────────────────────────────────────────────

// SchedulerEnqueued counts board:fetch tasks enqueued by the scheduler tick.
var SchedulerEnqueued = promauto.NewCounter(prometheus.CounterOpts{
	Name: "verspaetet_scheduler_tasks_enqueued_total",
	Help: "Tasks enqueued by the scheduler.",
})

// ── Exports ───────────────────────────────────────────────────────────────

// ExportLastSuccess is a Unix timestamp of the last completed monthly
// export; alert on time() - this.
var ExportLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "verspaetet_export_last_success_timestamp",
	Help: "Unix time of the last successful monthly export (0 = never).",
})

// ── asynq queue states (sampled by StartQueueCollector) ───────────────────

// QueueTasks gauges the per-queue task counts by state
// (pending/active/scheduled/retry/archived).
var QueueTasks = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "verspaetet_asynq_queue_tasks",
	Help: "Tasks per asynq queue by state (sampled).",
}, []string{"queue", "state"})

// ── HTTP plumbing ─────────────────────────────────────────────────────────

// Handler returns the Prometheus scrape handler for the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Listen serves /metrics on addr until the process exits. Used by the
// worker and scheduler, which otherwise have no HTTP server. A failure to
// bind is fatal (monitoring silently missing is worse than a loud crash).
func Listen(addr string) {
	if addr == "" {
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("metrics listener: %v", err)
	}
	log.Printf("metrics endpoint listening on %s", ln.Addr())
	go func() {
		if err := newServer().Serve(ln); err != nil {
			log.Fatalf("metrics server: %v", err)
		}
	}()
}

// newServer builds the standalone metrics HTTP server (own mux — the
// worker/scheduler have no other HTTP surface).
func newServer() *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", Handler())
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

// StartQueueCollector samples asynq queue statistics every interval and
// updates the QueueTasks gauges. Errors (e.g. Redis briefly unavailable)
// are logged once per tick and skipped — the gauges simply keep their last
// value until scraping resumes.
func StartQueueCollector(redisAddr string, interval time.Duration) {
	if redisAddr == "" || interval <= 0 {
		return
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisAddr})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			collectQueues(inspector)
		}
	}()
}

func collectQueues(inspector *asynq.Inspector) {
	names, err := inspector.Queues()
	if err != nil {
		log.Printf("metrics queue collector: %v", err)
		return
	}
	for _, q := range names {
		s, err := inspector.GetQueueInfo(q)
		if err != nil {
			continue
		}
		QueueTasks.WithLabelValues(q, "pending").Set(float64(s.Pending))
		QueueTasks.WithLabelValues(q, "active").Set(float64(s.Active))
		QueueTasks.WithLabelValues(q, "scheduled").Set(float64(s.Scheduled))
		QueueTasks.WithLabelValues(q, "retry").Set(float64(s.Retry))
		QueueTasks.WithLabelValues(q, "archived").Set(float64(s.Archived))
	}
}

