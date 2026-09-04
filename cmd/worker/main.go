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

// worker handles board:fetch (one station, one direction) and
// station:resolve (record unresolved route-path names) tasks. It reuses the
// IRIS client + persist logic directly — no workflow engine involved.
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

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	mux := asynq.NewServeMux()
	mux.HandleFunc(asynqtasks.TypeBoardFetch, makeBoardFetchHandler(iris, processor, client, dryRun))
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

// makeBoardFetchHandler fetches one station board (IRIS) and persists it.
// Unresolved route-path names become pending_stations rows (discovery).
func makeBoardFetchHandler(iris *activities.Iris, processor *activities.Process, client *asynq.Client, dryRun bool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p asynqtasks.BoardFetchPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}
		if p.Eva == "" {
			return fmt.Errorf("empty eva: %w", asynq.SkipRetry)
		}

		if dryRun {
			log.Printf("DRY-RUN board:fetch %s/%s (skipping fetch)", p.Eva, p.Direction)
			return nil
		}

		result, err := iris.FetchStationBoard(ctx, shared.FetchStationBoardInput{
			Eva:       p.Eva,
			Direction: p.Direction,
		})
		if err != nil {
			return fmt.Errorf("fetch %s/%s: %w", p.Eva, p.Direction, err)
		}

		if len(result.Events) == 0 {
			return nil
		}

		pr, err := processor.PersistStopEvent(ctx, result.Events)
		if err != nil {
			return fmt.Errorf("persist %s/%s: %w", p.Eva, p.Direction, err)
		}

		// Discovery: record unresolved names (Fire&Forget-ish — errors are
		// logged, not retried; the next scrape re-derives the same names).
		if len(pr.NewStations) > 0 {
			if err := processor.RecordPendingStations(ctx, pr.NewStations, p.Eva); err != nil {
				log.Printf("WARN record pending from %s: %v", p.Eva, err)
			}
		}
		return nil
	}
}

// makeStationResolveHandler records a list of unresolved names for one
// source station (used when discovery is triggered explicitly).
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