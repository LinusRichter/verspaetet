package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"verspaetet/activities"
	asynqtasks "verspaetet/asynqtasks"
	"verspaetet/metrics"
	"verspaetet/shared"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// worker handles board:fetch (one station, BOTH directions from one IRIS
// pass: 1 fchg + 1 cached plan) and station:resolve tasks.
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

	// Metrics endpoint + asynq queue gauges (sampled every 15s).
	metrics.Listen(envOr("METRICS_ADDR", ":9091"))
	metrics.StartQueueCollector(redisAddr, 15*time.Second)

	mux := asynq.NewServeMux()
	mux.HandleFunc(asynqtasks.TypeBoardFetch, makeBoardFetchHandler(iris, processor, pool, dryRun))
	mux.HandleFunc(asynqtasks.TypeStationResolve, makeStationResolveHandler(processor))
	mux.HandleFunc(asynqtasks.TypeExportMonth, makeExportMonthHandler(pool))

	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: redisAddr}, asynq.Config{
		Concurrency: envInt("ASYNQ_CONCURRENCY", 10),
		Queues: map[string]int{
			asynqtasks.QueueDiscovery: 10,
			asynqtasks.QueueDefault:   5,
			asynqtasks.QueueExport:    1,
		},
	})

	log.Printf("asynq worker starting (queues: discovery, default, dry_run=%v)", dryRun)
	if err := srv.Run(mux); err != nil {
		log.Fatalln("worker stopped:", err)
	}
}

// makeBoardFetchHandler fetches one station's complete board (both
// directions, single IRIS pass) and persists it as two direction batches.
// Unresolved route-path names become pending_stations rows (discovery).
func makeBoardFetchHandler(iris *activities.Iris, processor *activities.Process, pool *pgxpool.Pool, dryRun bool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		result := "ok"
		defer func() { metrics.WorkerTasks.WithLabelValues("board:fetch", result).Inc() }()
		var p asynqtasks.BoardFetchPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			result = "bad_payload"
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}
		if p.Eva == "" {
			result = "bad_payload"
			return fmt.Errorf("empty eva: %w", asynq.SkipRetry)
		}

		if dryRun {
			result = "dry_run"
			log.Printf("DRY-RUN board:fetch %s (skipping fetch)", p.Eva)
			return nil
		}

		stationResult, err := iris.FetchStationBoard(ctx, p.Eva)
		if err != nil {
			result = "fetch_error"
			// IRIS answers HTTP 400 for EVAs without an IRIS Betriebsstelle.
			// Mark the station no_iris so the scheduler skips it from now on
			// (no more wasted requests), then stop retrying this task.
			if strings.Contains(err.Error(), "status 400") {
				result = "no_iris"
				if _, uerr := pool.Exec(ctx,
					"UPDATE stations SET no_iris = true WHERE eva = $1", p.Eva); uerr != nil {
					log.Printf("WARN mark no_iris %s: %v", p.Eva, uerr)
				}
				return fmt.Errorf("iris 400 (marked no_iris): %s: %w", p.Eva, asynq.SkipRetry)
			}
			return fmt.Errorf("fetch %s: %w", p.Eva, err)
		}

		// Empty board: IRIS knows the station but has no timetable data
		// (200 + <timetable/>, 13 bytes). If the station has NEVER yielded
			// events, it is permanently empty — mark it so the scheduler stops
			// wasting slots. A station with prior data gets a transient empty
			// board sometimes (froendenberg-froemern, day one); those must NOT
			// be marked — treat as a normal empty fetch.
		if len(stationResult.Events) == 0 {
			result = "empty_board"
			var prior int
			if qerr := pool.QueryRow(ctx,
				"SELECT count(*) FROM stop_events WHERE station_eva = $1", p.Eva).Scan(&prior); qerr == nil && prior == 0 {
				result = "no_iris"
				if _, uerr := pool.Exec(ctx,
					"UPDATE stations SET no_iris = true WHERE eva = $1", p.Eva); uerr != nil {
					log.Printf("WARN mark no_iris(empty) %s: %v", p.Eva, uerr)
				}
				log.Printf("empty board %s (marked no_iris)", p.Eva)
			} else {
				log.Printf("empty board %s (transient, prior events — not marked)", p.Eva)
			}
			// Nothing to persist; end the task successfully.
			return nil
		}

		// Split into per-direction batches (scrape_runs is direction-keyed).
		arrivals := make([]shared.StopEvent, 0, len(stationResult.Events)/2)
		departures := make([]shared.StopEvent, 0, len(stationResult.Events)/2)
		for _, ev := range stationResult.Events {
			if ev.Direction == "arrival" {
				arrivals = append(arrivals, ev)
			} else {
				departures = append(departures, ev)
			}
		}

		var newNames []string
		for _, batch := range [][]shared.StopEvent{arrivals, departures} {
			if len(batch) == 0 {
				continue
			}
			pr, err := processor.PersistStopEvent(ctx, batch)
			if err != nil {
				result = "persist_error"
				return fmt.Errorf("persist %s/%s: %w", p.Eva, batch[0].Direction, err)
			}
			newNames = append(newNames, pr.NewStations...)
		}

		// Discovery: record unresolved names for the next StaDa import.
		if len(newNames) > 0 {
			if err := processor.RecordPendingStations(ctx, newNames, p.Eva); err != nil {
				log.Printf("WARN record pending from %s: %v", p.Eva, err)
			}
		}
		return nil
	}
}

// makeExportMonthHandler exports one operating month (Parquet + manifest)
// into EXPORTS_DIR (default /exports). Schema version is configurable via
// DATASET_SCHEMA_VERSION (default v0.1-beta).
func makeExportMonthHandler(pool *pgxpool.Pool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		result := "ok"
		defer func() { metrics.WorkerTasks.WithLabelValues("export:month", result).Inc() }()
		var p asynqtasks.ExportMonthPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			result = "bad_payload"
			return fmt.Errorf("unmarshal payload: %v: %w", err, asynq.SkipRetry)
		}
		if p.Year < 2000 || p.Month < 1 || p.Month > 12 {
			return fmt.Errorf("invalid year/month %d/%d: %w", p.Year, p.Month, asynq.SkipRetry)
		}
		outDir := envOr("EXPORTS_DIR", "/exports")
		schema := envOr("DATASET_SCHEMA_VERSION", "v0.1-beta")
		chunkName := fmt.Sprintf("%04d-%02d", p.Year, p.Month)
		m, err := activities.ExportMonth(ctx, pool, filepath.Join(outDir, chunkName), p.Year, p.Month, schema)
		if err != nil {
			result = "error"
			return fmt.Errorf("export %s: %w", chunkName, err)
		}
		metrics.ExportLastSuccess.SetToCurrentTime()
		log.Printf("export %s done: %d stop_events, %d stations, manifest at %s",
			chunkName, m.StopEvents.Rows, m.Stations.Rows, outDir)

		// Upload to HuggingFace (HF_TOKEN + HF_REPO env; skip silently when
		// unset so local exports work without credentials).
		if os.Getenv("HF_TOKEN") != "" && os.Getenv("HF_REPO") != "" {
			if err := activities.UploadToHuggingFace(ctx, filepath.Join(outDir, chunkName)); err != nil {
				return fmt.Errorf("hf upload %s: %w", chunkName, err)
			}
		} else {
			log.Printf("HF_TOKEN/HF_REPO unset — chunk stays local in %s", outDir)
		}
		return nil
	}
}

// makeStationResolveHandler records a list of unresolved names for one
// source station (explicit/manual discovery backfill).
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
