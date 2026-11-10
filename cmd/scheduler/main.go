package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	"verspaetet/activities"
	asynqtasks "verspaetet/asynqtasks"
	"verspaetet/metrics"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduler enqueues ONE board:fetch task (both directions) per station per
// cadence cycle. It ticks every minute and selects stations whose
// fetch_offset matches the current slot in the cycle. Newly imported
// stations are picked up automatically — no restart needed.
//
// It also drives the monthly export pipeline: every hour it checks which of
// the previous two months still needs an export (export_runs state), so a
// failed export is retried instead of rotting in the asynq archive. And on
// day 2 of each month at 03:00 UTC it re-probes all no_iris stations.
//
// CADENCE_MINUTES (default 40) MUST match the value stationimport used to
// compute fetch_offset. Changing the cadence = re-run stationimport with the
// new value + restart the scheduler with the same env.
func main() {
	redisAddr := envOr("REDIS_ADDR", "redis:6379")
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	cadence := envInt("CADENCE_MINUTES", 40)
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
	ins := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisAddr})
	defer ins.Close()

	metrics.Listen(envOr("METRICS_ADDR", ":9091"))

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Tick once immediately, then every minute. lastSlot tracks the last
	// processed slot so a stalled or sleeping process catches up missed
	// slots on the next tick instead of losing coverage permanently.
	lastSlot := int(time.Now().Unix()/60) % cadence
	tick(ctx, pool, client, cadence, dryRun, lastSlot)
	var lastExportCheck time.Time // zero: first tick checks immediately
	var lastRecheckMonth string
	for {
		select {
		case <-ticker.C:
			now := time.Now().UTC()
			// Monthly export check — BEFORE the missed==0 continue below, so
			// it runs on every loop iteration, hourly.
			if time.Since(lastExportCheck) >= time.Hour {
				lastExportCheck = now
				checkExports(ctx, pool, client, ins, now)
			}
			// Monthly no_iris re-probe (day 2, 03:00 UTC, once per month).
			recheckDisabledStations(ctx, pool, client, now, &lastRecheckMonth)

			cur := int(now.Unix()/60) % cadence
			missed := (cur - lastSlot + cadence) % cadence
			if missed == 0 {
				// Ticker fired within the same slot (tick ran < 60s ago):
				// nothing to do — that slot was just enqueued.
				continue
			}
			// Enqueue every slot from lastSlot+1 through cur (usually just
			// cur; a real stall catches up everything missed).
			var slots []int
			for i := missed - 1; i >= 0; i-- {
				slots = append(slots, (cur-i+cadence)%cadence)
			}
			if missed > 1 {
				log.Printf("catching up %d missed slot(s): last=%d cur=%d", missed, lastSlot, cur)
			}
			tick(ctx, pool, client, cadence, dryRun, slots...)
			lastSlot = cur
		}
	}
}

// checkExports enqueues export tasks for every due month that is not
// finished yet (export_runs state). Hourly; errors are logged, never fatal.
// Archived exports are replaced once per day (04:00 UTC hour) — the retry
// path for a month whose last attempts all failed.
func checkExports(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client, ins *asynq.Inspector, now time.Time) {
	if dryRun() {
		return
	}
	// Same rule as the worker: only with both set does an export end in an upload.
	hfEnabled := os.Getenv("HF_TOKEN") != "" && os.Getenv("HF_REPO") != ""
	months, err := activities.MonthsNeedingExport(ctx, pool, now, hfEnabled)
	if err != nil {
		log.Printf("WARN export check: %v", err)
		return
	}
	for _, ym := range months {
		start, err := activities.ParseMonth(ym)
		if err != nil {
			log.Printf("WARN export check %s: %v", ym, err)
			continue
		}
		state, err := asynqtasks.EnqueueMonthExport(ctx, client, ins, start.Year(), int(start.Month()), now.Hour() == 4)
		if err != nil {
			log.Printf("WARN enqueue export %s: %v", ym, err)
			continue
		}
		log.Printf("export %s: %s", ym, state)
	}
}

// recheckDisabledStations enqueues a recheck board:fetch for every no_iris
// station on day 2 of the month, hour 3 UTC — once per process per month.
// A station that delivers data again is re-enabled by the worker.
func recheckDisabledStations(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client, now time.Time, lastMonth *string) {
	if now.Day() != 2 || now.Hour() != 3 {
		return
	}
	ym := now.Format("2006-01")
	if *lastMonth == ym {
		return
	}
	*lastMonth = ym
	if dryRun() {
		log.Printf("DRY-RUN recheck of disabled stations skipped")
		return
	}
	rows, err := pool.Query(ctx, "SELECT eva FROM stations WHERE no_iris")
	if err != nil {
		log.Printf("WARN recheck query: %v", err)
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
		payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva, EnqueuedAt: now.Unix(), Recheck: true})
		if err != nil {
			continue
		}
		ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = client.EnqueueContext(ectx,
			asynq.NewTask(asynqtasks.TypeBoardFetch, payload),
			asynq.Queue(asynqtasks.QueueDefault),
			asynq.MaxRetry(0),
			asynq.Timeout(2*time.Minute),
		)
		cancel()
		if err != nil {
			log.Printf("WARN enqueue recheck %s: %v", eva, err)
			continue
		}
		enqueued++
	}
	log.Printf("recheck: enqueued board:fetch for %d disabled stations", enqueued)
}

func dryRun() bool { return os.Getenv("DRY_RUN") == "1" }

// tick enqueues one board:fetch per station due in each minute slot given by
// slots — a single slot on the happy path, or the catch-up range after a
// stall. Each enqueue is bounded by a per-call timeout so one slow Redis
// roundtrip can never freeze the tick loop (seen 2026-09-22: 11-min stall,
// lost slots).
func tick(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client, cadence int, dryRun bool, slots ...int) {
	if dryRun {
		return
	}
	if len(slots) == 0 {
		slots = []int{int(time.Now().Unix()/60) % cadence}
	}
	for _, slot := range slots {
		runTick(ctx, pool, client, slot)
	}
}

func runTick(ctx context.Context, pool *pgxpool.Pool, client *asynq.Client, slot int) {
	rows, err := pool.Query(ctx, "SELECT eva FROM stations WHERE fetch_offset = $1 AND NOT no_iris", slot)
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
		payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva, EnqueuedAt: time.Now().Unix()})
		if err != nil {
			continue
		}
		// Bounded enqueue: each call gets its own 5s window. On failure we
		// log and move on — asynq has no record of the task, so the next
		// catch-up run must cover it (handled by the caller's catch-up logic).
		ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = client.EnqueueContext(ectx,
			asynq.NewTask(asynqtasks.TypeBoardFetch, payload),
			asynq.Queue(asynqtasks.QueueDefault),
			asynq.MaxRetry(3),
			asynq.Timeout(2*time.Minute),
		)
		cancel()
		if err != nil {
			log.Printf("WARN enqueue %s: %v", eva, err)
			continue
		}
		enqueued++
	}
	if enqueued > 0 {
		metrics.SchedulerEnqueued.Add(float64(enqueued))
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
