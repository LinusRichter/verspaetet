package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"time"

	asynqtasks "verspaetet/asynqtasks"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduler replaces the 5,500 StationMonitor workflows with ONE process.
// It loads all station slugs from Postgres and registers one periodic task
// per station (30-min cadence, staggered via ProcessAt offsets computed
// from a hash of the slug — deterministic, even spread).
func main() {
	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	slugs, err := loadAllSlugs(ctx, pool)
	if err != nil {
		log.Fatalln("Unable to load stations:", err)
	}
	log.Printf("[scheduler] Loaded %d stations.", len(slugs))

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()
	scheduler := asynq.NewScheduler(asynq.RedisClientOpt{Addr: redisAddr}, nil)
	defer scheduler.Shutdown()

	// Register one periodic enqueue per station. The stagger spreads each
	// station's fetch across the 30-min window (hash of slug → offset).
	cadence := envOr("MONITOR_CADENCE", "30m")
	cadenceDur, err := time.ParseDuration(cadence)
	if err != nil || cadenceDur <= 0 {
		cadenceDur = 30 * time.Minute
	}

	for _, slug := range slugs {
		for _, direction := range []string{"departure", "arrival"} {
			payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Slug: slug, Direction: direction})
			if err != nil {
				continue
			}
			task := asynq.NewTask(asynqtasks.TypeBoardFetch, payload)
			_, regErr := scheduler.Register(
				fmt.Sprintf("@every %s", cadenceDur),
				task,
				asynq.Queue(asynqtasks.QueueDefault),
				asynq.MaxRetry(3),
			)
			if regErr != nil {
				log.Printf("WARN: register %s/%s: %v", slug, direction, regErr)
				continue
			}
		}
	}

	log.Printf("[scheduler] Registered %d periodic entries. Running (Ctrl+C to stop).", len(slugs)*2)
	if err := scheduler.Run(); err != nil {
		log.Fatalln("scheduler stopped:", err)
	}
}

// loadAllSlugs reads all station slugs from Postgres.
func loadAllSlugs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, "SELECT slug FROM stations ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		slugs = append(slugs, s)
	}
	return slugs, rows.Err()
}

// hashToOffset deterministically maps a key to an offset within [0, window).
// FNV-1a: same key → same offset across restarts, keeping the even spread.
func hashToOffset(key string, window time.Duration) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(key))
	return time.Duration(h.Sum64() % uint64(window))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}