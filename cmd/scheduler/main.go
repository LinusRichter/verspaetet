package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	asynqtasks "verspaetet/asynqtasks"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduler enqueues board:fetch tasks for all stations on a 30-min cadence,
// staggered via fetch_offset (hash of slug % 30). Instead of registering
// thousands of cron entries, it runs a tick loop: every minute it selects the
// stations whose fetch_offset matches the current minute slot. This means
// newly imported stations are picked up automatically — no restart needed.
func main() {
	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	if os.Getenv("DRY_RUN") == "1" {
		log.Println("scheduler starting in DRY_RUN mode (no enqueues)")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Tick once immediately, then every minute.
	tick(ctx, pool, client)
	for range ticker.C {
		tick(ctx, pool, client)
	}
}

// tick enqueues board:fetch (both directions) for every station due in this
// minute slot. ~5400 stations / 30 slots ≈ 180 stations per tick.
func tick(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client) {
	if os.Getenv("DRY_RUN") == "1" {
		return
	}
	slot := time.Now().UTC().Minute() % 30
	rows, err := pool.Query(ctx, "SELECT eva FROM stations WHERE fetch_offset = $1", slot)
	if err != nil {
		log.Printf("WARN tick query: %v", err)
		return
	}
	var evas []string
	for rows.Next() {
		var eva string
		if err := rows.Scan(&eva); err == nil {
			evas = append(evas, eva)
		}
	}
	rows.Close()

	enqueued := 0
	for _, eva := range evas {
		for _, direction := range []string{"departure", "arrival"} {
			payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva, Direction: direction})
			if err != nil {
				continue
			}
			_, err = client.EnqueueContext(ctx,
				asynq.NewTask(asynqtasks.TypeBoardFetch, payload),
				asynq.Queue(asynqtasks.QueueDefault),
				asynq.MaxRetry(3),
				asynq.Timeout(2*time.Minute),
			)
			if err != nil {
				log.Printf("WARN enqueue %s/%s: %v", eva, direction, err)
				continue
			}
			enqueued++
		}
	}
	if enqueued > 0 {
		log.Printf("tick slot=%d: enqueued %d tasks for %d stations", slot, enqueued, len(evas))
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}