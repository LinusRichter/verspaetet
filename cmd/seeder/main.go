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
//	seeder --export YYYY-MM           enqueue a monthly export task (replaces
//	                                  its archived dedup twin)
//	seeder --backfill-scrape-runs YYYY-MM [--dry-run]
//	                                  add scrape_runs to an already-published
//	                                  month (synchronous, no asynq; needs
//	                                  POSTGRES_DSN, HF_TOKEN/HF_REPO, EXPORTS_DIR)
//	seeder --prune-month YYYY-MM [--dry-run]
//	                                  delete a fully published month's rows
//	                                  from Postgres (needs POSTGRES_DSN,
//	                                  HF_TOKEN/HF_REPO)
//	seeder --mark-offsite YYYY-MM     record the operator's off-site backup
//	seeder --adopt-month YYYY-MM      adopt a pre-export_runs month from its
//	                                  published manifest
//	seeder --status                   print all export_runs rows
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
	var pruneMonth string
	var markOffsite string
	var adoptMonth string
	var status bool
	var dryRun bool
	flag.StringVar(&eva, "eva", "", "enqueue one board fetch (both directions) for this EVA")
	flag.StringVar(&export, "export", "", "enqueue a monthly export task (YYYY-MM)")
	flag.StringVar(&backfillScrapeRuns, "backfill-scrape-runs", "", "backfill scrape_runs.parquet for an already-published month (YYYY-MM); synchronous, no asynq")
	flag.StringVar(&pruneMonth, "prune-month", "", "prune a fully published month (YYYY-MM) from Postgres; synchronous; --dry-run logs only")
	flag.StringVar(&markOffsite, "mark-offsite", "", "record the off-site backup of a month (YYYY-MM) in export_runs")
	flag.StringVar(&adoptMonth, "adopt-month", "", "adopt a month exported before export_runs existed (YYYY-MM), from its published manifest")
	flag.BoolVar(&status, "status", false, "print all export_runs rows")
	flag.BoolVar(&dryRun, "dry-run", false, "with --backfill-scrape-runs/--prune-month: prepare and log only")
	flag.Parse()

	switch {
	case backfillScrapeRuns != "":
		runBackfillScrapeRuns(backfillScrapeRuns, dryRun)
	case pruneMonth != "":
		runPruneMonth(pruneMonth, dryRun)
	case markOffsite != "":
		runMarkOffsite(markOffsite)
	case adoptMonth != "":
		runAdoptMonth(adoptMonth)
	case status:
		runStatus()
	}

	if backfillScrapeRuns != "" || pruneMonth != "" || markOffsite != "" || adoptMonth != "" || status {
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
// asynq archive under the same dedup TaskID — EnqueueMonthExport(replace-
// Archived=true) deletes the archived twin and re-enqueues.
func enqueueExport(client *asynq.Client, ym string) {
	y, m, ok := parseYearMonth(ym)
	if !ok {
		log.Fatalf("[seeder] --export expects YYYY-MM, got %q", ym)
	}
	ins := asynq.NewInspector(asynq.RedisClientOpt{Addr: envOr("REDIS_ADDR", "redis:6379")})
	defer ins.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := asynqtasks.EnqueueMonthExport(ctx, client, ins, y, m, true)
	if err != nil {
		log.Fatalf("[seeder] Unable to enqueue export %s: %v\n", ym, err)
	}
	log.Printf("[seeder] export:month %s: %s\n", ym, state)
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
	ctx := context.Background()
	pool := mustPool(ctx)
	defer pool.Close()

	exportsDir := envOr("EXPORTS_DIR", "/exports")
	man, err := activities.BackfillScrapeRuns(ctx, pool, exportsDir, y, m, dryRun)
	if err != nil {
		log.Fatalf("[seeder] backfill %s failed: %v\n", ym, err)
	}
	log.Printf("[seeder] backfill %s ok: scrape_runs %d rows [%s .. %s], sha %s, schema %s\n",
		ym, man.ScrapeRuns.Rows, man.ScrapeRuns.From, man.ScrapeRuns.To, man.ScrapeRuns.Sha256, man.SchemaVersion)
}

// runPruneMonth deletes a fully published month's rows from Postgres
// (stop_events, and the fetch log below the oldest surviving row). All
// pipeline checks must pass; --dry-run logs what would be deleted.
func runPruneMonth(ym string, dryRun bool) {
	if _, _, ok := parseYearMonth(ym); !ok {
		log.Fatalf("[seeder] --prune-month expects YYYY-MM, got %q", ym)
	}
	if os.Getenv("HF_REPO") == "" {
		log.Fatalln("[seeder] --prune-month needs HF_REPO (prune verifies the published chunk first)")
	}
	ctx := context.Background()
	pool := mustPool(ctx)
	defer pool.Close()

	res, err := activities.PruneMonth(ctx, pool, ym, activities.PruneOptions{
		DryRun: dryRun,
		Verify: activities.VerifyRunOnHuggingFace,
	})
	if err != nil {
		log.Fatalf("[seeder] prune %s failed: %v\n", ym, err)
	}
	if res == nil {
		return
	}
	log.Printf("[seeder] prune %s ok: %d stop_events deleted, %d scrape_runs deleted, %d late rows kept, boundary %s\n",
		ym, res.StopEventsDeleted, res.ScrapeRunsDeleted, res.LateRows, res.Boundary.UTC().Format(time.RFC3339))
	if len(res.Skipped) > 0 {
		log.Printf("[seeder] prune %s skipped: %s\n", ym, strings.Join(res.Skipped, "; "))
	}
}

// runMarkOffsite records the operator's off-site backup of a month (its
// files and a DB dump verified off-site) — the last gate before prune.
func runMarkOffsite(ym string) {
	if _, _, ok := parseYearMonth(ym); !ok {
		log.Fatalf("[seeder] --mark-offsite expects YYYY-MM, got %q", ym)
	}
	ctx := context.Background()
	pool := mustPool(ctx)
	defer pool.Close()

	if err := activities.MarkOffsite(ctx, pool, ym); err != nil {
		log.Fatalf("[seeder] mark offsite %s failed: %v\n", ym, err)
	}
	log.Printf("[seeder] %s marked off-site\n", ym)
	printExportRun(ctx, pool, ym)
}

// runAdoptMonth creates the export_runs row for a month that was exported
// before export_runs existed: the published manifest is downloaded,
// verified against the repo, and the row is filled from it (exported +
// uploaded). If Postgres no longer has the month's stop_events rows, the
// row is marked pruned as well. Never touches offsite_at or the fetch-log
// prune columns.
func runAdoptMonth(ym string) {
	if _, _, ok := parseYearMonth(ym); !ok {
		log.Fatalf("[seeder] --adopt-month expects YYYY-MM, got %q", ym)
	}
	ctx := context.Background()
	pool := mustPool(ctx)
	defer pool.Close()

	man, err := activities.DownloadPublishedManifest(ctx, ym)
	if err != nil {
		log.Fatalf("[seeder] adopt %s: download manifest: %v\n", ym, err)
	}
	if err := activities.VerifyOnHuggingFace(ctx, ym, man); err != nil {
		log.Fatalf("[seeder] adopt %s: verify published chunk: %v\n", ym, err)
	}
	if err := activities.MarkExported(ctx, pool, ym, man); err != nil {
		log.Fatalf("[seeder] adopt %s: record export: %v\n", ym, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE export_runs
		SET exported_at = COALESCE(exported_at, now()),
		    uploaded_at  = COALESCE(uploaded_at, now())
		WHERE month = $1`, ym); err != nil {
		log.Fatalf("[seeder] adopt %s: stamp exported/uploaded: %v\n", ym, err)
	}
	start, err := activities.ParseMonth(ym)
	if err != nil {
		log.Fatalf("[seeder] adopt %s: %v\n", ym, err)
	}
	var rows int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM stop_events
		WHERE trip_date >= $1 AND trip_date < $2`,
		start.Format("2006-01-02"), start.AddDate(0, 1, 0).Format("2006-01-02")).Scan(&rows); err != nil {
		log.Fatalf("[seeder] adopt %s: count stop_events: %v\n", ym, err)
	}
	if rows == 0 {
		if _, err := pool.Exec(ctx, `
			UPDATE export_runs
			SET pruned_at = now(), pruned_rows = 0, prune_note = 'adopted: no local rows'
			WHERE month = $1 AND pruned_at IS NULL`, ym); err != nil {
			log.Fatalf("[seeder] adopt %s: mark pruned: %v\n", ym, err)
		}
	}
	printExportRun(ctx, pool, ym)
}

// runStatus prints every export_runs row (one line each).
func runStatus() {
	ctx := context.Background()
	pool := mustPool(ctx)
	defer pool.Close()

	runs, err := activities.ListExportRuns(ctx, pool)
	if err != nil {
		log.Fatalln("[seeder] list export_runs:", err)
	}
	if len(runs) == 0 {
		fmt.Println("[seeder] no export_runs rows")
		return
	}
	for _, r := range runs {
		printRun(&r)
	}
}

func printExportRun(ctx context.Context, pool *pgxpool.Pool, ym string) {
	r, err := activities.GetExportRun(ctx, pool, ym)
	if err != nil {
		log.Printf("[seeder] WARN load %s: %v\n", ym, err)
		return
	}
	if r == nil {
		log.Printf("[seeder] %s: no export_runs row\n", ym)
		return
	}
	printRun(r)
}

func ts(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func i64Str(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

func strPtr(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func mustPool(ctx context.Context) *pgxpool.Pool {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	return pool
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

func printRun(r *activities.ExportRun) {
	fmt.Printf("[seeder] %s rows=%s exported=%s uploaded=%s offsite=%s pruned=%s (%s rows) fetchlog_pruned=%s (%s rows) note=%s last_error=%s\n",
		r.Month, i64Str(r.Rows), ts(r.ExportedAt), ts(r.UploadedAt), ts(r.OffsiteAt),
		ts(r.PrunedAt), i64Str(r.PrunedRows), ts(r.ScrapeRunsPrunedAt), i64Str(r.ScrapeRunsPrunedRows),
		strPtr(r.PruneNote), strPtr(r.LastError))
}
