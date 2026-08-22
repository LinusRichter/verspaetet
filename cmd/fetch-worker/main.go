package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"verspaetet/activities"
	"verspaetet/shared"
	"verspaetet/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// fetch-worker polls discovery-queue and registers StationDiscovery + the
// fetch/discover activities. DB-free. See ticket T14.
func main() {
	c, err := client.Dial(client.Options{HostPort: getTemporalHost()})
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	defer c.Close()

	// Caps: FetchStationBoard concurrency is bounded so the single browserless
	// instance is not overloaded (per docs/architecture/crawler-policy.md — 5
	// per worker, ≤10 across both workers pointing at one browserless).
	// Workflow-task execution is also capped so the discovery fan-out (each
	// seed station can spawn hundreds of child StationDiscovery workflows)
	// cannot overrun the worker's capacity or browserless in a single burst.
	// Both caps are overridable via env (ACTIVITY_CAP / WORKFLOW_TASK_CAP) so a
	// low-power host (Raspberry Pi) can throttle without a code change.
	activityCap := envInt("ACTIVITY_CAP", 5)
	workflowTaskCap := envInt("WORKFLOW_TASK_CAP", 20)
	w := worker.New(c, shared.DiscoveryQueue, worker.Options{
		MaxConcurrentActivityExecutionSize:    activityCap,
		MaxConcurrentWorkflowTaskExecutionSize: workflowTaskCap,
	})
	w.RegisterWorkflow(workflows.StationDiscovery)
	w.RegisterActivity(&activities.Fetch{})
	w.RegisterActivity(&activities.Process{})

	log.Println("Fetch worker polling queue:", shared.DiscoveryQueue,
		fmt.Sprintf("(activity cap %d, workflow-task cap %d)", activityCap, workflowTaskCap))
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalln("Fetch worker stopped:", err)
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