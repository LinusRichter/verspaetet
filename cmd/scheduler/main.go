package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

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

	metrics.Listen(envOr("METRICS_ADDR", ":9091"))

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Tick once immediately, then every minute. lastSlot tracks the last
	// processed slot so a stalled or sleeping process catches up missed
	// slots on the next tick instead of losing coverage permanently.
	lastSlot := int(time.Now().Unix()/60) % cadence
	tick(ctx, pool, client, cadence, dryRun, lastSlot)
	for {
		select {
		case <-ticker.C:
			cur := int(time.Now().Unix()/60) % cadence
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
			// Monthly dataset export: on the 4th of each month at 04:00 UTC
			// export the PREVIOUS operating month (+3 days finality lag).
			now := time.Now().UTC()
			if now.Day() == 4 && now.Hour() == 4 && now.Minute() == 0 {
				enqueueMonthExport(ctx, client, now.AddDate(0, -1, 0))
			}
		}
	}
}

// enqueueMonthExport enqueues the export task for the month of t.
// Guarded: the minute-tick fires exactly once per minute, and the day/hour/
// minute condition only matches once a month.
func enqueueMonthExport(ctx context.Context, client *asynq.Client, forMonth time.Time) {
	if dryRun() {
		return
	}
	y, m := forMonth.Year(), int(forMonth.Month())
	payload, err := json.Marshal(asynqtasks.ExportMonthPayload{Year: y, Month: m})
	if err != nil {
		log.Printf("WARN export marshal: %v", err)
		return
	}
	_, err = client.EnqueueContext(ctx,
		asynq.NewTask(asynqtasks.TypeExportMonth, payload),
		asynq.Queue(asynqtasks.QueueExport),
		asynq.MaxRetry(5),
		asynq.Timeout(30*time.Minute),
		asynq.TaskID(fmt.Sprintf("export:%04d%02d", y, m)), // dedup: once per month, ever
	)
	switch {
	case err == nil:
		log.Printf("enqueued export for %04d-%02d", y, m)
	case errors.Is(err, asynq.ErrDuplicateTask) || errors.Is(err, asynq.ErrTaskIDConflict):
		log.Printf("export for %04d-%02d already enqueued/done", y, m)
	default:
		log.Printf("WARN enqueue export %04d-%02d: %v", y, m, err)
	}
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
		payload, err := json.Marshal(asynqtasks.BoardFetchPayload{Eva: eva})
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
