package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"strconv"
	"time"

	asynqtasks "verspaetet/asynqtasks"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Usage:
//   seeder migrate up                 apply all pending up-migrations
//   seeder migrate down [N]           rollback the last N migrations
//   seeder --eva=<eva>                enqueue one board fetch (both directions)
//   seeder                            enqueue a board:fetch for all stations
//
// Periodic monitoring is handled by cmd/scheduler — the seeder is only for
// manual one-offs and initial dispatch.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrateSubcommand(os.Args[2:])
		return
	}

	var eva string
	flag.StringVar(&eva, "eva", "", "enqueue one board fetch (both directions) for this EVA")
	flag.Parse()

	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	if eva != "" {
		enqueueBoardFetch(client, eva)
		return
	}
	runFullSeed(client)
}

// enqueueBoardFetch enqueues a single board:fetch task (both directions).
func enqueueBoardFetch(client *asynq.Client, eva string) {
	payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva})
	if err != nil {
		log.Fatalln("marshal:", err)
	}
	info, err := client.Enqueue(
		asynq.NewTask(asynqtasks.TypeBoardFetch, payload),
		asynq.Queue(asynqtasks.QueueDefault),
		asynq.MaxRetry(3),
		asynq.Timeout(2*time.Minute),
	)
	if err != nil {
		log.Fatalf("[seeder] Unable to enqueue %s: %v\n", eva, err)
	}
	log.Printf("[seeder] Enqueued board:fetch %s (id %s)\n", eva, info.ID)
}

// runFullSeed enqueues board:fetch (both directions) for every station in
// Postgres — one initial scrape of the whole universe.
func runFullSeed(client *asynq.Client) {
	ctx := context.Background()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, "SELECT eva FROM stations ORDER BY name")
	if err != nil {
		log.Fatalln("Unable to query stations:", err)
	}
	defer rows.Close()

	var evas []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err == nil {
			evas = append(evas, e)
		}
	}
	if len(evas) == 0 {
		log.Fatalln("No stations found (run stationimport first)")
	}
	log.Printf("[seeder] Full seed: enqueuing board:fetch for %d stations.\n", len(evas))

	for _, eva := range evas {
		payload, _ := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva})
		_, err := client.Enqueue(
			asynq.NewTask(asynqtasks.TypeBoardFetch, payload),
			asynq.Queue(asynqtasks.QueueDefault),
			asynq.MaxRetry(3),
			asynq.Timeout(2*time.Minute),
		)
		if err != nil {
			log.Printf("[seeder] WARN: enqueue %s: %v\n", eva, err)
		}
	}
	log.Println("[seeder] Full seed dispatched; exiting.")
}

// --- migrate subcommand ---

func runMigrateSubcommand(args []string) {
	if len(args) == 0 {
		log.Fatalln("migrate subcommand requires an action: up | down [N]")
	}
	action := args[0]

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set; cannot run migrations")
	}

	m, err := migrate.New("file://db/migrations", dsn)
	if err != nil {
		log.Fatalln("Unable to construct migrate instance:", err)
	}
	defer m.Close()

	switch action {
	case "up":
		if err := m.Up(); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				log.Println("No pending migrations; schema is up to date.")
				return
			}
			log.Fatalln("migrate up failed:", err)
		}
		log.Println("Migrations applied successfully.")
	case "down":
		steps := 1
		if len(args) >= 2 {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				log.Fatalf("migrate down: %q is not an integer\n", args[1])
			}
			steps = n
		}
		if err := m.Steps(-steps); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				log.Println("No migrations to roll back.")
				return
			}
			log.Fatalln("migrate down failed:", err)
		}
		log.Printf("Rolled back %d migration(s).\n", steps)
	default:
		log.Fatalf("Unknown migrate action %q (expected up|down)\n", action)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}