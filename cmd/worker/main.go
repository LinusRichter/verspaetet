package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	cadence := time.Duration(envInt("CADENCE_MINUTES", 40)) * time.Minute

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	iris := &activities.Iris{}
	processor := &activities.Process{Pool: pool}

	// Metrics endpoint + asynq queue gauges (sampled every 15s) + continuous
	// IRIS rate-limit bucket sampling (every 5s — the post-fetch snapshot
	// flattered to zero under burst load).
	metrics.Listen(envOr("METRICS_ADDR", ":9091"))
	metrics.StartQueueCollector(redisAddr, 15*time.Second)
	metrics.StartTokenSampler(5*time.Second, activities.IRISTokenSnapshot)

	mux := asynq.NewServeMux()
	mux.HandleFunc(asynqtasks.TypeBoardFetch, makeBoardFetchHandler(iris, processor, pool, dryRun, cadence))
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

// isStale reports whether a board:fetch task was enqueued more than one
// cadence cycle ago (and is not a recheck probe): after an outage the queue
// holds a backlog of stale fetches — executing them late would skew the
// snapshot cadence, so the worker drops them and waits for fresh ones.
func isStale(enqueuedAt int64, recheck bool, now time.Time, cadence time.Duration) bool {
	if enqueuedAt <= 0 || recheck {
		return false
	}
	return now.Sub(time.Unix(enqueuedAt, 0)) > cadence
}

// shouldMarkNoIris decides the HTTP-400 branch: a station that has never
// delivered data gets permanently marked no_iris; one with prior data gets
// a transient 400 (IRIS glitches) and is kept. Only with a clean lookup.
func shouldMarkNoIris(hadData bool, lookupErr error) bool {
	return lookupErr == nil && !hadData
}

// makeBoardFetchHandler fetches one station's complete board (both
// directions, single IRIS pass) and persists it as two direction batches.
// Unresolved route-path names become pending_stations rows (discovery).
func makeBoardFetchHandler(iris *activities.Iris, processor *activities.Process, pool *pgxpool.Pool, dryRun bool, cadence time.Duration) func(context.Context, *asynq.Task) error {
	// In-memory has_data cache: skips the per-fetch UPDATE for stations
	// already known to deliver data.
	var hasDataMu sync.Mutex
	hasData := map[string]bool{}

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

		// Stale backlog (see isStale): drop without fetching.
		if isStale(p.EnqueuedAt, p.Recheck, time.Now(), cadence) {
			result = "stale"
			return nil
		}

		if dryRun {
			result = "dry_run"
			log.Printf("DRY-RUN board:fetch %s (skipping fetch)", p.Eva)
			return nil
		}

		stationResult, err := iris.FetchStationBoard(ctx, p.Eva)
		if err != nil {
			result = "fetch_error"
			// Surface every failed attempt: network flaps (recurring on this
			// homeserver) are otherwise invisible until a task exhausted its
			// retries — asynq only logs exhaustion, not the individual attempt.
			log.Printf("WARN fetch %s failed: %v", p.Eva, err)
			// IRIS answers HTTP 400 for EVAs without an IRIS Betriebsstelle.
			// Mark the station no_iris so the scheduler skips it from now on
			// (no more wasted requests) — but ONLY if it never delivered
			// data; a station with prior data gets a transient 400 sometimes.
			if strings.Contains(err.Error(), "status 400") {
				var hadData bool
				lookupErr := pool.QueryRow(ctx,
					"SELECT has_data FROM stations WHERE eva = $1", p.Eva).Scan(&hadData)
				if shouldMarkNoIris(hadData, lookupErr) {
					result = "no_iris"
					if _, uerr := pool.Exec(ctx,
						"UPDATE stations SET no_iris = true WHERE eva = $1", p.Eva); uerr != nil {
						log.Printf("WARN mark no_iris %s: %v", p.Eva, uerr)
					}
					return fmt.Errorf("iris 400 (marked no_iris): %s: %w", p.Eva, asynq.SkipRetry)
				}
				result = "http_400_kept"
				if !p.Recheck {
					log.Printf("WARN iris 400 for %s with prior data — not marked", p.Eva)
				}
				return fmt.Errorf("iris 400 for %s with prior data — not marked: %w", p.Eva, asynq.SkipRetry)
			}
			return fmt.Errorf("fetch %s: %w", p.Eva, err)
		}

		// Empty board: IRIS knows the station but has no timetable data
		// (200 + <timetable/>, 13 bytes). If the station has NEVER yielded
		// events, it is permanently empty — mark it so the scheduler stops
		// wasting slots. A station with prior data gets a transient empty
		// board sometimes (froendenberg-froemern, day one; line closures for
		// days); those must NOT be marked — treat as a normal empty fetch.
		//
		// "Ever yielded events" is the stations.has_data flag, not a
		// stop_events/scrape_runs lookup: exported months are pruned, so a
		// station silent for a while must not look like it never had data.
		if len(stationResult.Events) == 0 {
			result = "empty_board"
			var hadData bool
			if qerr := pool.QueryRow(ctx,
				"SELECT has_data FROM stations WHERE eva = $1", p.Eva).Scan(&hadData); qerr == nil && !hadData {
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

		// The station delivered data: flip has_data (first time only) and,
		// for a recheck probe, re-enable the station.
		hasDataMu.Lock()
		known := hasData[p.Eva]
		hasDataMu.Unlock()
		if !known {
			if _, uerr := pool.Exec(ctx,
				"UPDATE stations SET has_data = true WHERE eva = $1 AND NOT has_data", p.Eva); uerr != nil {
				log.Printf("WARN mark has_data %s: %v", p.Eva, uerr)
			} else {
				hasDataMu.Lock()
				hasData[p.Eva] = true
				hasDataMu.Unlock()
			}
		}
		if p.Recheck {
			if ct, uerr := pool.Exec(ctx,
				"UPDATE stations SET no_iris = false WHERE eva = $1 AND no_iris", p.Eva); uerr != nil {
				log.Printf("WARN re-enable %s: %v", p.Eva, uerr)
			} else if ct.RowsAffected() > 0 {
				log.Printf("station %s delivers data again — re-enabled", p.Eva)
			}
		}

		// A freshly fetched plan hour pins the true scheduled times onto
		// earlier snapshots of its stops (fchg omits pt for ~90% of events;
		// until the plan hour arrives, those rows carry the communicated
		// time as planned_time and their delay reads as 0).
		if stationResult.PlanFresh && len(stationResult.PlanBaseline) > 0 {
			if n, err := processor.BackfillPlanned(ctx, p.Eva, stationResult.PlanBaseline); err != nil {
				log.Printf("WARN backfill planned %s: %v", p.Eva, err)
			} else if n > 0 {
				log.Printf("backfill planned %s: %d rows pinned", p.Eva, n)
			}
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
// into EXPORTS_DIR (default /exports), uploads + verifies it on HuggingFace
// when HF_TOKEN/HF_REPO are set, and records every step in export_runs.
// Schema version is configurable via DATASET_SCHEMA_VERSION (default v0.3).
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
			result = "bad_payload"
			return fmt.Errorf("invalid year/month %d/%d: %w", p.Year, p.Month, asynq.SkipRetry)
		}
		hfEnabled := os.Getenv("HF_TOKEN") != "" && os.Getenv("HF_REPO") != ""
		chunkName := fmt.Sprintf("%04d-%02d", p.Year, p.Month)
		exportsDir := envOr("EXPORTS_DIR", "/exports")
		chunkDir := filepath.Join(exportsDir, chunkName)
		schema := envOr("DATASET_SCHEMA_VERSION", "v0.3")

		// Every error path records the failure on the export_runs row so the
		// scheduler keeps retrying unattended and --status shows why.
		fail := func(err error) error {
			result = "error"
			if merr := activities.MarkExportFailed(ctx, pool, chunkName, err); merr != nil {
				log.Printf("WARN mark export failed %s: %v", chunkName, merr)
			}
			return err
		}

		// Already done (retry after success / dedup race): the state row is
		// the single source of truth.
		run, err := activities.GetExportRun(ctx, pool, chunkName)
		if err != nil {
			return fail(fmt.Errorf("export %s: load export_runs: %w", chunkName, err))
		}
		if run != nil {
			if (hfEnabled && run.UploadedAt != nil) || (!hfEnabled && run.ExportedAt != nil) {
				log.Printf("export %s already done (export_runs) — skipping", chunkName)
				return nil
			}
		}
		// A month that is already published but has no state row was exported
		// before export_runs existed: re-exporting it would rebuild the local
		// files from whatever rows are left. It has to be adopted instead.
		if hfEnabled && (run == nil || run.ExportedAt == nil) {
			if _, perr := activities.DownloadPublishedManifest(ctx, chunkName); perr == nil {
				result = "skipped_published"
				log.Printf("WARN export %s: already published on HuggingFace but not in export_runs — not exporting; run seeder --adopt-month %s", chunkName, chunkName)
				return fmt.Errorf("export %s: published but not adopted: %w", chunkName, asynq.SkipRetry)
			} else if !strings.Contains(perr.Error(), "status 404") {
				return fail(fmt.Errorf("export %s: check published manifest: %w", chunkName, perr))
			}
		}
		if err := activities.MarkExportAttempt(ctx, pool, chunkName); err != nil {
			log.Printf("WARN mark export attempt %s: %v", chunkName, err)
		}

		// Disk pre-flight: the Parquet build of a full month needs room.
		if free, ok := diskFreeBytes(exportsDir); ok {
			need := int64(envInt("EXPORT_MIN_FREE_GB", 25)) << 30
			if free < need {
				return fail(fmt.Errorf("export %s: only %d GB free, need %d", chunkName, free>>30, need>>30))
			}
		}

		m, err := activities.ExportMonth(ctx, pool, chunkDir, p.Year, p.Month, schema)
		if errors.Is(err, activities.ErrEmptyMonth) {
			// Nothing to publish (e.g. a month whose rows were already
			// exported and deleted). Retrying cannot help.
			result = "empty"
			if merr := activities.MarkExportFailed(ctx, pool, chunkName, err); merr != nil {
				log.Printf("WARN mark export failed %s: %v", chunkName, merr)
			}
			return fmt.Errorf("export %s: %v: %w", chunkName, err, asynq.SkipRetry)
		}
		if err != nil {
			return fail(fmt.Errorf("export %s: %w", chunkName, err))
		}
		log.Printf("export %s done: %d stop_events, %d stations, manifest at %s",
			chunkName, m.StopEvents.Rows, m.Stations.Rows, exportsDir)

		// Fatal on purpose: without the state row the scheduler would enqueue
		// this month again every hour.
		if err := activities.MarkExported(ctx, pool, chunkName, m); err != nil {
			return fail(fmt.Errorf("mark exported %s: %w", chunkName, err))
		}

		// Upload to HuggingFace (HF_TOKEN + HF_REPO env; skip silently when
		// unset so local exports work without credentials).
		if hfEnabled {
			// Never replace a published chunk with a smaller one (e.g. a month
			// that was already pruned locally but has no export_runs row).
			if pub, perr := activities.DownloadPublishedManifest(ctx, chunkName); perr == nil {
				if pub.StopEvents.Rows > m.StopEvents.Rows {
					return fail(fmt.Errorf("export %s: published chunk has %d rows, new export only %d — not uploading (adopt the month instead: seeder --adopt-month)",
						chunkName, pub.StopEvents.Rows, m.StopEvents.Rows))
				}
			} else if !strings.Contains(perr.Error(), "status 404") {
				return fail(fmt.Errorf("export %s: check published manifest: %w", chunkName, perr))
			}
			if err := activities.UploadToHuggingFace(ctx, chunkDir); err != nil {
				return fail(fmt.Errorf("hf upload %s: %w", chunkName, err))
			}
			if err := activities.VerifyOnHuggingFace(ctx, chunkName, m); err != nil {
				return fail(fmt.Errorf("hf verify %s: %w", chunkName, err))
			}
			if err := activities.MarkUploaded(ctx, pool, chunkName); err != nil {
				return fail(fmt.Errorf("mark uploaded %s: %w", chunkName, err))
			}
		} else {
			log.Printf("HF_TOKEN/HF_REPO unset — chunk stays local in %s", exportsDir)
		}

		m.ConsumeDowntimeNotes()
		metrics.ExportLastSuccess.SetToCurrentTime()
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
