package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"verspaetet/activities"
	asynqtasks "verspaetet/asynqtasks"
	"verspaetet/shared"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// worker handles board:fetch (one station, BOTH directions from one IRIS
// pass: 1 fchg + 1 cached plan) and station:resolve tasks.
//
// DRY_RUN=1 skips the actual IRIS fetches (pipeline testing).
func main() {
	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	dryRun := os.Getenv("DRY_RUN") == "1"

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	iris := &activities.Iris{}
	processor := &activities.Process{Pool: pool}

	mux := asynq.NewServeMux()
	mux.HandleFunc(asynqtasks.TypeBoardFetch, makeBoardFetchHandler(iris, processor, dryRun))
	mux.HandleFunc(asynqtasks.TypeStationResolve, makeStationResolveHandler(processor))

	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: redisAddr}, asynq.Config{
		Concurrency: envInt("ASYNQ_CONCURRENCY", 10),
		Queues: map[string]int{
			asynqtasks.QueueDiscovery: 10,
			asynqtasks.QueueDefault:  5,
		},
	})

	log.Printf("asynq worker starting (queues: discovery, default, dry_run=%v)", dryRun)
	if err := srv.Run(mux); err != nil {
		log.Fatalln("worker stopped:", err)
	}
}

// makeBoardFetchHandler fetches one station's complete board (both
// directions, single IRIS pass) and persists it as two direction batches.
// Unresolved route-path names become pending_stations rows (discovery).
func makeBoardFetchHandler(iris *activities.Iris, processor *activities.Process, dryRun bool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p asynqtasks.BoardFetchPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}
		if p.Eva == "" {
			return fmt.Errorf("empty eva: %w", asynq.SkipRetry)
		}

		if dryRun {
			log.Printf("DRY-RUN board:fetch %s (skipping fetch)", p.Eva)
			return nil
		}

		result, err := iris.FetchStationBoard(ctx, p.Eva)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", p.Eva, err)
		}

		// Split into per-direction batches (scrape_runs is direction-keyed).
		arrivals := make([]shared.StopEvent, 0, len(result.Events)/2)
		departures := make([]shared.StopEvent, 0, len(result.Events)/2)
		for _, ev := range result.Events {
			if ev.Direction == "arrival" {
				arrivals = append(arrivals, ev)
			} else {
				departures = append(departures, ev)
			}
		}

		var newNames []string
		for _, batch := range [][]shared.StopEvent{arrivals, departures} {
			if len(batch) == 0 {
				continue
			}
			pr, err := processor.PersistStopEvent(ctx, batch)
			if err != nil {
				return fmt.Errorf("persist %s/%s: %w", p.Eva, batch[0].Direction, err)
			}
			newNames = append(newNames, pr.NewStations...)
		}

		// Discovery: record unresolved names for the next StaDa import.
		if len(newNames) > 0 {
			if err := processor.RecordPendingStations(ctx, newNames, p.Eva); err != nil {
				log.Printf("WARN record pending from %s: %v", p.Eva, err)
			}
		}
		return nil
	}
}

// makeStationResolveHandler records a list of unresolved names for one
// source station (explicit/manual discovery backfill).
func makeStationResolveHandler(processor *activities.Process) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p asynqtasks.StationResolvePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}
		if err := processor.RecordPendingStations(ctx, p.Names, p.SeenFrom); err != nil {
			return fmt.Errorf("record pending: %w", err)
		}
		return nil
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

var _ = time.Now