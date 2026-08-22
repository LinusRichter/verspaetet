package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"verspaetet/activities"
	"verspaetet/shared"
	"verspaetet/workflows"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// process-worker polls monitor-queue and registers StationMonitor + all
// activities. It owns the pgx pool and passes it to activities.Process.
// See ticket T15 and docs/architecture/task-queues.md.
func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set; refusing to start process-worker with no DB")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres via POSTGRES_DSN:", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalln("Postgres ping failed:", err)
	}

	c, err := client.Dial(client.Options{HostPort: getTemporalHost()})
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	defer c.Close()

	// Caps: FetchStationBoard is registered here too (the monitor renders
	// boards on monitor-queue) so its concurrency must be bounded — 5 per
	// worker, ≤10 summed with fetch-worker against one browserless
	// (docs/architecture/crawler-policy.md). PersistStopEvent/ParseBoard are
	// cheap and DB/CPU-bound; the default cap is fine, but we set an explicit
	// activity cap so a monitor burst cannot starve the pgx pool. Both caps are
	// overridable via env (ACTIVITY_CAP / WORKFLOW_TASK_CAP) for low-power
	// hosts (Raspberry Pi).
	activityCap := envInt("ACTIVITY_CAP", 5)
	workflowTaskCap := envInt("WORKFLOW_TASK_CAP", 50)
	w := worker.New(c, shared.MonitorQueue, worker.Options{
		MaxConcurrentActivityExecutionSize:    activityCap,
		MaxConcurrentWorkflowTaskExecutionSize: workflowTaskCap,
	})
	w.RegisterWorkflow(workflows.StationMonitor)
	w.RegisterActivity(&activities.Fetch{})
	w.RegisterActivity(&activities.Process{Pool: pool})

	log.Println("Process worker polling queue:", shared.MonitorQueue,
		fmt.Sprintf("(activity cap %d, workflow-task cap %d)", activityCap, workflowTaskCap))
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalln("Process worker stopped:", err)
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func getTemporalHost() string {
	if h := os.Getenv("TEMPORAL_HOST"); h != "" {
		return h
	}
	return "localhost:7233"
}