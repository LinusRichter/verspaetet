package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	asynqtasks "verspaetet/asynqtasks"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduler enqueues ONE board:fetch task (both directions) per station per
// cadence cycle. It ticks every minute and selects stations whose
// fetch_offset matches the current slot in the cycle. Newly imported
// stations are picked up automatically — no restart needed.
//
// CADENCE_MINUTES (default 30) MUST match the value stationimport used to
// compute fetch_offset. Changing the cadence = re-run stationimport with the
// new value + restart the scheduler with the same env.
func main() {
	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	cadence := envInt("CADENCE_MINUTES", 30)
	dryRun := os.Getenv("DRY_RUN") == "1"
	if dryRun {
		log.Printf("scheduler starting in DRY_RUN mode (no enqueues), cadence=%dm", cadence)
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
	tick(ctx, pool, client, cadence, dryRun)
	for range ticker.C {
		tick(ctx, pool, client, cadence, dryRun)
	}
}

// tick enqueues one board:fetch per station due in this minute slot.
// ~5400 stations / 30 slots ≈ 180 stations per tick (at 30-min cadence).
func tick(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client, cadence int, dryRun bool) {
	if dryRun {
		return
	}
	slot := int(time.Now().Unix() / 60) % cadence
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
		payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva})
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
			log.Printf("WARN enqueue %s: %v", eva, err)
			continue
		}
		enqueued++
	}
	if enqueued > 0 {
		log.Printf("tick slot=%d: enqueued %d station tasks", slot, enqueued)
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
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}