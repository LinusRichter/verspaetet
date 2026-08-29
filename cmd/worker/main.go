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

// worker replaces the two Temporal workers (fetch-worker + process-worker).
// It handles board:fetch and discovery:fetch tasks, reusing the existing
// activity code unchanged. The pgx pool is shared with the activities.
//
// DRY_RUN=1 skips the actual board fetch (returns success immediately) —
// used to test the asynq pipeline without hitting bahnhof.de.
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

	fetcher := &activities.Fetch{}
	processor := &activities.Process{Pool: pool}

	mux := asynq.NewServeMux()
	mux.HandleFunc(asynqtasks.TypeBoardFetch, makeBoardFetchHandler(fetcher, processor, dryRun))
	mux.HandleFunc(asynqtasks.TypeDiscovery, makeDiscoveryHandler(fetcher, processor, dryRun))

	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: redisAddr}, asynq.Config{
		Concurrency: envInt("ASYNQ_CONCURRENCY", 10),
		Queues: map[string]int{
			asynqtasks.QueueDiscovery: 10,
			asynqtasks.QueueDefault:   5,
		},
	})

	log.Printf("asynq worker starting (queues: discovery, default, dry_run=%v)", dryRun)
	if err := srv.Run(mux); err != nil {
		log.Fatalln("worker stopped:", err)
	}
}

// makeBoardFetchHandler fetches one station board and persists the events.
// It reuses the existing activity logic (Fetch.FetchStationBoard +
// Process.PersistStopEvent) — no Temporal required.
func makeBoardFetchHandler(fetcher *activities.Fetch, processor *activities.Process, dryRun bool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p asynqtasks.BoardFetchPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}

		if dryRun {
			log.Printf("DRY-RUN board:fetch %s/%s (skipping fetch)", p.Slug, p.Direction)
			return nil
		}

		result, err := fetcher.FetchStationBoard(ctx, shared.FetchStationBoardInput{
			Slug:      p.Slug,
			Direction: p.Direction,
		})
		if err != nil {
			return fmt.Errorf("fetch %s/%s: %w", p.Slug, p.Direction, err)
		}

		if len(result.Events) == 0 {
			return nil
		}

		pr, err := processor.PersistStopEvent(ctx, result.Events)
		if err != nil {
			return fmt.Errorf("persist %s/%s: %w", p.Slug, p.Direction, err)
		}
		_ = pr
		return nil
	}
}

// makeDiscoveryHandler fetches a station board, persists it, and enqueues
// discovery tasks for any new stations found. It also schedules the periodic
// monitor for the station (the new-station monitor is scheduled by the
// seeder for existing stations, by the discovery handler for new ones).
func makeDiscoveryHandler(fetcher *activities.Fetch, processor *activities.Process, dryRun bool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p asynqtasks.DiscoveryPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}

		if dryRun {
			log.Printf("DRY-RUN discovery:fetch %s (skipping fetch)", p.Slug)
			return nil
		}

		redisAddr := envOr("REDIS_ADDR", "redis:6379")
		client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
		defer client.Close()

		// Fetch + persist both directions, collect new stations.
		var allNewStations []string
		for _, direction := range []string{"departure", "arrival"} {
			result, err := fetcher.FetchStationBoard(ctx, shared.FetchStationBoardInput{
				Slug:      p.Slug,
				Direction: direction,
			})
			if err != nil {
				return fmt.Errorf("fetch %s/%s: %w", p.Slug, direction, err)
			}
			if len(result.Events) == 0 {
				continue
			}
			for i := range result.Events {
				result.Events[i].ParentEva = p.ParentEva
			}
			pr, err := processor.PersistStopEvent(ctx, result.Events)
			if err != nil {
				return fmt.Errorf("persist %s/%s: %w", p.Slug, direction, err)
			}
			allNewStations = append(allNewStations, pr.NewStations...)
		}

		// Enqueue discovery tasks for new stations.
		for _, slug := range allNewStations {
			if slug == "" || slug == p.Slug {
				continue
			}
			payload, err := json.Marshal(asynqtasks.DiscoveryPayload{Slug: slug, ParentEva: ""})
			if err != nil {
				continue
			}
			_, err = client.Enqueue(
				asynq.NewTask(asynqtasks.TypeDiscovery, payload),
				asynq.MaxRetry(5),
				asynq.Queue(asynqtasks.QueueDiscovery),
				asynq.Unique(time.Hour),
			)
			if err != nil {
				log.Printf("WARN: enqueue discovery for %q: %v", slug, err)
			}
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