package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"verspaetet/activities"
	asynqtasks "verspaetet/asynqtasks"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Usage:
//
//	seeder migrate up                 apply all pending up-migrations
//	seeder migrate down [N]           rollback the last N migrations
//	seeder --eva=<eva>                enqueue one board fetch (both directions)
//	seeder                            enqueue a board:fetch for all stations
//	seeder --export YYYY-MM           enqueue a monthly export task
//	seeder --backfill-scrape-runs YYYY-MM [--dry-run]
//	                                  add scrape_runs to an already-published
//	                                  month (synchronous, no asynq; needs
//	                                  POSTGRES_DSN, HF_TOKEN/HF_REPO, EXPORTS_DIR)
//
// Periodic monitoring is handled by cmd/scheduler — the seeder is only for
// manual one-offs and initial dispatch.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrateSubcommand(os.Args[2:])
		return
	}

	var eva string
	var export string
	var backfillScrapeRuns string
	var dryRun bool
	flag.StringVar(&eva, "eva", "", "enqueue one board fetch (both directions) for this EVA")
	flag.StringVar(&export, "export", "", "enqueue a monthly export task (YYYY-MM) — also clears its archived dedup twin")
	flag.StringVar(&backfillScrapeRuns, "backfill-scrape-runs", "", "backfill scrape_runs.parquet for an already-published month (YYYY-MM); synchronous, no asynq")
	flag.BoolVar(&dryRun, "dry-run", false, "with --backfill-scrape-runs: prepare and log only, do not upload")
	flag.Parse()

	if backfillScrapeRuns != "" {
		runBackfillScrapeRuns(backfillScrapeRuns, dryRun)
		return
	}

	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	if export != "" {
		enqueueExport(client, export)
		return
	}
	if eva != "" {
		enqueueBoardFetch(client, eva)
		return
	}
	runFullSeed(client)
}

// enqueueExport enqueues a monthly export task. A failed export sits in the
// asynq archive under the same dedup TaskID — before re-enqueueing we try to
// delete it (Inspector) so the new run passes the TaskID-uniqueness check.
func enqueueExport(client *asynq.Client, ym string) {
	y, m, ok := parseYearMonth(ym)
	if !ok {
		log.Fatalf("[seeder] --export expects YYYY-MM, got %q", ym)
	}
	taskID := fmt.Sprintf("export:%04d%02d", y, m)
	ins := asynq.NewInspector(asynq.RedisClientOpt{Addr: envOr("REDIS_ADDR", "redis:6379")})
	if err := ins.DeleteTask(asynqtasks.QueueExport, taskID); err != nil {
		// Not fatal: the task simply may not exist in the archive.
		log.Printf("[seeder] delete archived %s: %v (ignoring)", taskID, err)
	}
	payload, err := json.Marshal(asynqtasks.ExportMonthPayload{Year: y, Month: m})
	if err != nil {
		log.Fatalln("marshal:", err)
	}
	info, err := client.Enqueue(
		asynq.NewTask(asynqtasks.TypeExportMonth, payload),
		asynq.Queue(asynqtasks.QueueExport),
		asynq.MaxRetry(5),
		asynq.Timeout(asynqtasks.ExportTimeout),
		asynq.TaskID(taskID),
	)
	if err != nil {
		log.Fatalf("[seeder] Unable to enqueue export %s: %v\n", ym, err)
	}
	log.Printf("[seeder] Enqueued export:month %s (id %s)\n", ym, info.ID)
}

// parseYearMonth parses "YYYY-MM" (also tolerates "YYYY-M").
func parseYearMonth(s string) (int, int, bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 || len(parts[0]) != 4 {
		return 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 || y < 2000 {
		return 0, 0, false
	}
	return y, m, true
}

// runBackfillScrapeRuns adds the scrape_runs observation log to an already
// published month (September/October 2026 are schema v0.2 without it).
// Synchronous, no asynq: needs POSTGRES_DSN, HF_TOKEN/HF_REPO and
// EXPORTS_DIR (default /exports). --dry-run prepares and logs only.
func runBackfillScrapeRuns(ym string, dryRun bool) {
	y, m, ok := parseYearMonth(ym)
	if !ok {
		log.Fatalf("[seeder] --backfill-scrape-runs expects YYYY-MM, got %q", ym)
	}
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

	exportsDir := envOr("EXPORTS_DIR", "/exports")
	man, err := activities.BackfillScrapeRuns(ctx, pool, exportsDir, y, m, dryRun)
	if err != nil {
		log.Fatalf("[seeder] backfill %s failed: %v\n", ym, err)
	}
	log.Printf("[seeder] backfill %s ok: scrape_runs %d rows [%s .. %s], sha %s, schema %s\n",
		ym, man.ScrapeRuns.Rows, man.ScrapeRuns.From, man.ScrapeRuns.To, man.ScrapeRuns.Sha256, man.SchemaVersion)
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
