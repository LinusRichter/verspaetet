package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"verspaetet/shared"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
)

// Usage:
//   bahnhof seeder migrate up                 apply all pending up-migrations
//   bahnhof seeder migrate down [N]            rollback the last N migrations (default 1)
//   bahnhof seeder [--station=<slug>] [--once] [--direction=both|departure|arrival]
//   bahnhof seeder                            full seed (start discovery+monitor for all seed stations)
//
// See docs/decisions/adr-09-cli-flag-test-mode.md, docs/data/migrations.md,
// docs/runbook/one-station-mode.md, and ticket T16.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrateSubcommand(os.Args[2:])
		return
	}

	var (
		stationSlug string
		once        bool
		direction   string
	)
	flag.StringVar(&stationSlug, "station", "", "constrain to one station (by bahnhof.de slug)")
	flag.BoolVar(&once, "once", false, "run one discovery + one monitor cycle, then exit (requires --station)")
	flag.StringVar(&direction, "direction", "both", "limit the monitor cycle to one board: both|departure|arrival (reserved for v1; StationMonitor always scrapes both)")
	flag.Parse()

	// --direction is reserved for v1: the StationMonitor(ctx, slug) workflow
	// signature does not take a direction filter, so the flag is accepted but
	// not forwarded. See ticket T16 step 5 and ADR-09.
	_ = direction

	if stationSlug != "" {
		runOneStation(stationSlug, once)
		return
	}
	runFullSeed()
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

// --- crawl/seed path ---

func runOneStation(slug string, once bool) {
	ctx := context.Background()
	c := dialTemporal(ctx)
	defer c.Close()

	// 1. Start StationDiscovery and wait for completion.
	discOpts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("station-discovery-slug-%s", slug),
		TaskQueue: shared.DiscoveryQueue,
	}
	discWe, err := c.ExecuteWorkflow(ctx, discOpts, shared.StationDiscoveryWorkflowName, slug, "", 0)
	if err != nil {
		log.Fatalf("Unable to start StationDiscovery for %q: %v\n", slug, err)
	}
	log.Printf("[seeder] Started StationDiscovery: %s (run %s)\n", discWe.GetID(), discWe.GetRunID())
	if err := discWe.Get(ctx, nil); err != nil {
		log.Fatalf("StationDiscovery %q failed: %v\n", slug, err)
	}
	log.Printf("[seeder] StationDiscovery %q completed.\n", slug)

	if !once {
		// Long-running monitor: start and exit.
		monOpts := client.StartWorkflowOptions{
			ID:        fmt.Sprintf("station-monitor-slug-%s", slug),
			TaskQueue: shared.MonitorQueue,
		}
		monWe, err := c.ExecuteWorkflow(ctx, monOpts, shared.StationMonitorWorkflowName, slug, true)
		if err != nil {
			log.Fatalf("Unable to start StationMonitor for %q: %v\n", slug, err)
		}
		log.Printf("[seeder] Started StationMonitor: %s (run %s); exiting.\n", monWe.GetID(), monWe.GetRunID())
		return
	}

	// --once: start one monitor cycle with a short WorkflowRunTimeout so the
	// first cycle completes and Temporal kills the ContinueAsNew before the
	// second cycle starts. See ticket T16 (implementation note).
	monOpts := client.StartWorkflowOptions{
		ID:                  fmt.Sprintf("station-monitor-slug-%s-test-%d", slug, time.Now().Unix()),
		TaskQueue:           shared.MonitorQueue,
		WorkflowRunTimeout:  30 * time.Second,
		WorkflowTaskTimeout: 10 * time.Second,
	}
	monWe, err := c.ExecuteWorkflow(ctx, monOpts, shared.StationMonitorWorkflowName, slug, true)
	if err != nil {
		log.Fatalf("Unable to start StationMonitor (once) for %q: %v\n", slug, err)
	}
	log.Printf("[seeder] Started StationMonitor (once): %s (run %s); waiting for first cycle.\n", monWe.GetID(), monWe.GetRunID())
	if err := monWe.Get(ctx, nil); err != nil {
		// A timeout on the ContinueAsNew'd run is expected once the first
		// cycle finishes; surface other errors but don't treat the expected
		// timeout as fatal. Log and exit 0 to match the runbook.
		log.Printf("[seeder] StationMonitor (once) returned: %v\n", err)
	}
	log.Printf("[seeder] One-station mode complete for %q.\n", slug)
}

func runFullSeed() {
	ctx := context.Background()
	c := dialTemporal(ctx)
	defer c.Close()

	slugs := loadAllSlugs(ctx)
	log.Printf("[seeder] Full seed: starting workflows for %d stations.\n", len(slugs))

	for _, slug := range slugs {
		discOpts := client.StartWorkflowOptions{
			ID:        fmt.Sprintf("station-discovery-slug-%s", slug),
			TaskQueue: shared.DiscoveryQueue,
		}
		discWe, err := c.ExecuteWorkflow(ctx, discOpts, shared.StationDiscoveryWorkflowName, slug, "", 0)
		if err != nil {
			log.Printf("[seeder] WARN: StationDiscovery start failed for %q: %v\n", slug, err)
		} else {
			log.Printf("[seeder] Started StationDiscovery: %s (run %s)\n", discWe.GetID(), discWe.GetRunID())
		}

		monOpts := client.StartWorkflowOptions{
			ID:        fmt.Sprintf("station-monitor-slug-%s", slug),
			TaskQueue: shared.MonitorQueue,
		}
		monWe, err := c.ExecuteWorkflow(ctx, monOpts, shared.StationMonitorWorkflowName, slug, true)
		if err != nil {
			log.Printf("[seeder] WARN: StationMonitor start failed for %q: %v\n", slug, err)
		} else {
			log.Printf("[seeder] Started StationMonitor: %s (run %s)\n", monWe.GetID(), monWe.GetRunID())
		}
	}
	log.Println("[seeder] Full seed dispatched; exiting.")
}

// loadAllSlugs reads all station slugs from Postgres (seeds + discovered).
func loadAllSlugs(ctx context.Context) []string {
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
		log.Fatalln("Unable to query seed stations:", err)
	}
	defer rows.Close()

	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			log.Fatalln("Scanning seed station slug:", err)
		}
		slugs = append(slugs, s)
	}
	if err := rows.Err(); err != nil {
		log.Fatalln("Iterating seed station rows:", err)
	}
	if len(slugs) == 0 {
		log.Fatalln("No stations found (run `seeder migrate up` first)")
	}
	return slugs
}

func dialTemporal(ctx context.Context) client.Client {
	host := "localhost:7233"
	if h := os.Getenv("TEMPORAL_HOST"); h != "" {
		host = h
	}
	c, err := client.Dial(client.Options{HostPort: host})
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	return c
}