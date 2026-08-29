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
//   seeder migrate down [N]           rollback the last N migrations (default 1)
//   seeder [--station=<slug>] [--once]
//   seeder                            full seed (enqueue discovery for all stations)
//
// The periodic monitors are handled by cmd/scheduler — the seeder only
// enqueues the initial discovery tasks (one per station).
func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrateSubcommand(os.Args[2:])
		return
	}

	var (
		stationSlug string
		once        bool
	)
	flag.StringVar(&stationSlug, "station", "", "constrain to one station (by bahnhof.de slug)")
	flag.BoolVar(&once, "once", false, "run one discovery, then exit")
	flag.Parse()

	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	if stationSlug != "" {
		runOneStation(client, stationSlug, once)
		return
	}
	runFullSeed(client)
}

// runOneStation enqueues a discovery task for a single station.
func runOneStation(client *asynq.Client, slug string, once bool) {
	payload, err := json.Marshal(asynqtasks.DiscoveryPayload{Slug: slug})
	if err != nil {
		log.Fatalln("marshal:", err)
	}
	taskOpts := []asynq.Option{
		asynq.Queue(asynqtasks.QueueDiscovery),
		asynq.MaxRetry(3),
		asynq.TaskID("discovery:" + slug),
	}
	if once {
		taskOpts = append(taskOpts, asynq.Unique(time.Hour))
	}
	info, err := client.Enqueue(asynq.NewTask(asynqtasks.TypeDiscovery, payload), taskOpts...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			log.Printf("[seeder] Discovery for %q already queued/running.", slug)
			return
		}
		log.Fatalf("[seeder] Unable to enqueue discovery for %q: %v\n", slug, err)
	}
	log.Printf("[seeder] Enqueued discovery: %s (id %s, queue %s)\n", slug, info.ID, info.Queue)
}

// runFullSeed enqueues discovery tasks for all stations in Postgres.
func runFullSeed(client *asynq.Client) {
	ctx := context.Background()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set; cannot read station list")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, "SELECT slug FROM stations ORDER BY name")
	if err != nil {
		log.Fatalln("Unable to query stations:", err)
	}
	defer rows.Close()

	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			log.Fatalln("Scanning station slug:", err)
		}
		slugs = append(slugs, s)
	}
	if err := rows.Err(); err != nil {
		log.Fatalln("Iterating station rows:", err)
	}
	if len(slugs) == 0 {
		log.Fatalln("No stations found (run `seeder migrate up` first)")
	}
	log.Printf("[seeder] Full seed: enqueuing discovery for %d stations.\n", len(slugs))

	enqueued, skipped := 0, 0
	for _, slug := range slugs {
		payload, err := json.Marshal(asynqtasks.DiscoveryPayload{Slug: slug})
		if err != nil {
			continue
		}
		_, err = client.Enqueue(
			asynq.NewTask(asynqtasks.TypeDiscovery, payload),
			asynq.Queue(asynqtasks.QueueDiscovery),
			asynq.MaxRetry(3),
			asynq.TaskID("discovery:"+slug),
			asynq.Unique(time.Hour),
		)
		if err != nil {
			if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
				skipped++
				continue
			}
			log.Printf("[seeder] WARN: discovery enqueue failed for %q: %v\n", slug, err)
			continue
		}
		enqueued++
	}
	log.Printf("[seeder] Seed dispatched: %d enqueued, %d already pending. Exiting.\n", enqueued, skipped)
}

// --- migrate subcommand (unchanged from the Temporal version) ---

func runMigrateSubcommand(args []string) {
	if len(args) == 0 {
		log.Fatalln("migrate subcommand requires an action: up | down [N]")
	}
	action := args[0]

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set; cannot run migrations")
	}

	migrationsPath := "db/migrations"
	m, err := migrate.New("file://"+migrationsPath, dsn)
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