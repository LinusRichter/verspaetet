package activities

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PruneMonth deletes a month's stop_events rows (and, when the fetch log is
// fully covered by published chunks, the scrape_runs rows below the oldest
// surviving stop_event) — the ONLY destructive code in the collector. It
// refuses to delete anything unless every pipeline check passes: the month
// is final, the chunk is exported/uploaded/verified/off-site, and the row
// counts match the exported manifest. No force flag, no option that skips a
// check.

type PruneOptions struct {
	DryRun    bool
	BatchSize int64         // default 200000
	Pause     time.Duration // default 200ms between batches
	Now       func() time.Time
	Verify    func(ctx context.Context, run *ExportRun) error // production: VerifyRunOnHuggingFace
}

type PruneResult struct {
	Month             string
	StopEventsDeleted int64
	ScrapeRunsDeleted int64
	LateRows          int64
	Boundary          time.Time
	Skipped           []string
}

// PruneMonth prunes one "YYYY-MM" operating month.
func PruneMonth(ctx context.Context, pool *pgxpool.Pool, month string, o PruneOptions) (*PruneResult, error) {
	if o.BatchSize <= 0 {
		o.BatchSize = 200000
	}
	if o.Pause <= 0 {
		o.Pause = 200 * time.Millisecond
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	res := &PruneResult{Month: month}

	start, err := ParseMonth(month)
	if err != nil {
		return res, err
	}
	end := start.AddDate(0, 1, 0)
	finalFrom := end.Add(3 * 24 * time.Hour)
	if o.Now().UTC().Before(finalFrom) {
		return res, fmt.Errorf("prune %s: month not final (finality from %s)", month, finalFrom.Format(time.RFC3339))
	}

	// One connection with no statement timeout: the big deletes must not
	// die half-way through because of a default per-statement limit.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return res, err
	}
	// One prune at a time (nightly job vs. a manual run). Session lock on
	// this pooled connection, so it must be released explicitly.
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", pruneLockKey).Scan(&locked); err != nil {
		return res, err
	}
	if !locked {
		return res, fmt.Errorf("prune %s: another prune is running", month)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", pruneLockKey); err != nil {
			log.Printf("WARN prune %s: advisory unlock: %v", month, err)
		}
	}()

	// A failed check deletes nothing and leaves the error on the row.
	fail := func(err error) (*PruneResult, error) {
		if o.DryRun {
			return res, err
		}
		if uerr := setPruneError(ctx, pool, month, err); uerr != nil {
			log.Printf("WARN prune %s: record last_error: %v", month, uerr)
		}
		return res, err
	}

	run, err := GetExportRun(ctx, pool, month)
	if err != nil {
		return res, err
	}
	if run == nil {
		return fail(fmt.Errorf("prune %s: no export_runs row", month))
	}
	if run.ExportedAt == nil || run.UploadedAt == nil || run.OffsiteAt == nil {
		return fail(fmt.Errorf("prune %s: chunk not fully published (exported_at=%s uploaded_at=%s offsite_at=%s)",
			month, fmtTS(run.ExportedAt), fmtTS(run.UploadedAt), fmtTS(run.OffsiteAt)))
	}
	if run.OffsiteAt.Before(*run.ExportedAt) {
		return fail(fmt.Errorf("prune %s: offsite_at %s is before exported_at %s", month, run.OffsiteAt, run.ExportedAt))
	}
	if run.Rows == nil || run.StopEventsSha256 == nil || run.MaxScrapedAt == nil {
		return fail(fmt.Errorf("prune %s: export_runs row incomplete (rows/stop_events_sha256/max_scraped_at)", month))
	}

	stopEventsPruned := run.PrunedAt != nil

	// ---- Part A: stop_events ---------------------------------------------

	if stopEventsPruned {
		res.Skipped = append(res.Skipped, "stop_events already pruned")
	} else {
		if o.Verify == nil {
			return fail(fmt.Errorf("prune %s: no Verify function provided", month))
		}
		if verr := o.Verify(ctx, run); verr != nil {
			return fail(verr)
		}

		// Predicate P: everything the exported chunk contains. The cut is
		// max_scraped_at + 1s (max_scraped_at is rounded to whole seconds).
		startD := start.Format("2006-01-02")
		endD := end.Format("2006-01-02")
		cut := run.MaxScrapedAt.Add(time.Second)

		var count int64
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM stop_events
			WHERE trip_date >= $1 AND trip_date < $2 AND scraped_at < $3`,
			startD, endD, cut).Scan(&count); err != nil {
			return res, fmt.Errorf("prune %s: count month: %w", month, err)
		}
		first := run.PruneStartedAt == nil
		if first && count != *run.Rows {
			return fail(fmt.Errorf("prune %s: count mismatch: %d rows in stop_events vs %d in the exported chunk", month, count, *run.Rows))
		}
		if !first {
			if count > *run.Rows {
				return fail(fmt.Errorf("prune %s: resuming: %d rows left, more than the %d exported", month, count, *run.Rows))
			}
			log.Printf("prune %s: resuming interrupted prune (%d rows left)", month, count)
		}

		var minID, maxID *int64
		if err := conn.QueryRow(ctx, "SELECT min(id), max(id) FROM stop_events").Scan(&minID, &maxID); err != nil {
			return res, fmt.Errorf("prune %s: id range: %w", month, err)
		}

		if o.DryRun {
			log.Printf("prune %s DRY-RUN: DELETE FROM stop_events WHERE trip_date >= %s AND trip_date < %s AND scraped_at < %s → %d rows (exported: %d)",
				month, startD, endD, cut.Format(time.RFC3339), count, *run.Rows)
			if minID != nil && maxID != nil {
				log.Printf("prune %s DRY-RUN: id range %d..%d", month, *minID, *maxID)
			}
		} else {
			if first {
				if _, err := conn.Exec(ctx, `
					UPDATE export_runs SET prune_started_at = now()
					WHERE month = $1 AND prune_started_at IS NULL`, month); err != nil {
					return res, fmt.Errorf("prune %s: mark prune_started: %w", month, err)
				}
			}
			if minID != nil && maxID != nil {
				deleted, derr := deleteBatches(ctx, conn, "stop_events", *minID, *maxID, o,
					"trip_date >= $3 AND trip_date < $4 AND scraped_at < $5", startD, endD, cut)
				if derr != nil {
					return res, fmt.Errorf("prune %s: delete stop_events: %w", month, derr)
				}
				res.StopEventsDeleted += deleted
			}

			// One final scan: nothing matching P may remain, and on a first
			// run the deleted total must equal the exported chunk.
			var remaining, late int64
			if err := conn.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE trip_date >= $1 AND trip_date < $2 AND scraped_at < $3),
				       count(*) FILTER (WHERE trip_date < $2)
				FROM stop_events`, startD, endD, cut).Scan(&remaining, &late); err != nil {
				return res, fmt.Errorf("prune %s: post-delete scan: %w", month, err)
			}
			if remaining != 0 {
				return fail(fmt.Errorf("prune %s: %d rows still match the predicate after delete", month, remaining))
			}
			if first && res.StopEventsDeleted != *run.Rows {
				return fail(fmt.Errorf("prune %s: deleted %d rows, expected %d", month, res.StopEventsDeleted, *run.Rows))
			}
			res.LateRows = late

			var note *string
			if late > 0 {
				s := fmt.Sprintf("%d late rows kept", late)
				note = &s
			}
			if _, err := conn.Exec(ctx, `
				UPDATE export_runs SET pruned_at = now(), pruned_rows = rows, prune_note = $2, last_error = NULL
				WHERE month = $1`, month, note); err != nil {
				return res, fmt.Errorf("prune %s: mark pruned: %w", month, err)
			}
			if _, err := conn.Exec(ctx, "VACUUM (ANALYZE) stop_events"); err != nil {
				// Rows are gone and the row is marked pruned — the vacuum
				// failing is only a statistics issue, not a data issue.
				log.Printf("WARN prune %s: vacuum stop_events: %v", month, err)
			}
			stopEventsPruned = true
			log.Printf("prune %s: deleted %d stop_events rows, %d late rows kept",
				month, res.StopEventsDeleted, late)
		}
	}

	// ---- Part B: scrape_runs (fetch log) -----------------------------------

	if run.ScrapeRunsPrunedAt != nil {
		res.Skipped = append(res.Skipped, "scrape_runs already pruned")
		return res, nil
	}
	if !o.DryRun && !stopEventsPruned {
		return fail(fmt.Errorf("prune %s: stop_events not pruned — refusing to prune scrape_runs", month))
	}
	if run.ScrapeRunsFrom == nil || run.ScrapeRunsTo == nil || run.ScrapeRunsSha256 == nil {
		return fail(fmt.Errorf("prune %s: export_runs row has no scrape_runs range/sha256", month))
	}
	// Part A verified the chunk in this run; when it was pruned earlier,
	// verify again before deleting its fetch log.
	if run.PrunedAt != nil {
		if o.Verify == nil {
			return fail(fmt.Errorf("prune %s: no Verify function provided", month))
		}
		if verr := o.Verify(ctx, run); verr != nil {
			return fail(verr)
		}
	}

	// Boundary: never delete the fetch log of rows that still exist.
	boundary := *run.ScrapeRunsTo
	var minSE *time.Time
	// scrape_runs.scraped_at is truncated to whole seconds, stop_events keeps
	// the fraction: the oldest referenced fetch-log row is at the truncated
	// value, so the boundary must be truncated too.
	if err := conn.QueryRow(ctx, "SELECT date_trunc('second', min(scraped_at)) FROM stop_events").Scan(&minSE); err != nil {
		return res, fmt.Errorf("prune %s: min stop_events scraped_at: %w", month, err)
	}
	if minSE != nil && minSE.Before(boundary) {
		boundary = *minSE
	}
	res.Boundary = boundary

	// Coverage guard: every fetch-log row below the boundary must be covered
	// by a published chunk, and the published months must form one
	// consecutive, gap-free chain.
	uploaded, err := ListExportRuns(ctx, pool)
	if err != nil {
		return res, fmt.Errorf("prune %s: list export_runs: %w", month, err)
	}
	var chain []ExportRun
	for _, r := range uploaded {
		if r.UploadedAt != nil && r.Month <= month {
			chain = append(chain, r)
		}
	}
	var oldest *time.Time
	if err := conn.QueryRow(ctx, `
		SELECT min(scraped_at) FROM scrape_runs WHERE scraped_at < $1`, boundary).Scan(&oldest); err != nil {
		return res, fmt.Errorf("prune %s: oldest scrape_run: %w", month, err)
	}
	if err := checkScrapeRunsCoverage(chain, oldest, boundary); err != nil {
		return fail(err)
	}

	var n int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM scrape_runs WHERE scraped_at < $1", boundary).Scan(&n); err != nil {
		return res, fmt.Errorf("prune %s: count scrape_runs: %w", month, err)
	}
	if o.DryRun {
		log.Printf("prune %s DRY-RUN: DELETE FROM scrape_runs WHERE scraped_at < %s → %d rows", month, boundary.Format(time.RFC3339), n)
		return res, nil
	}

	var minID, maxID *int64
	if err := conn.QueryRow(ctx, "SELECT min(id), max(id) FROM scrape_runs WHERE scraped_at < $1", boundary).Scan(&minID, &maxID); err != nil {
		return res, fmt.Errorf("prune %s: scrape_runs id range: %w", month, err)
	}
	if minID != nil && maxID != nil {
		deleted, derr := deleteBatches(ctx, conn, "scrape_runs", *minID, *maxID, o,
			"scraped_at < $3", boundary)
		if derr != nil {
			// stop_events.scrape_run_id is a plain FK (no cascade): a wrong
			// boundary makes the DELETE fail — exactly the safety net we want.
			return res, fmt.Errorf("prune %s: delete scrape_runs: %w", month, derr)
		}
		res.ScrapeRunsDeleted = deleted
	}
	if _, err := conn.Exec(ctx, `
		UPDATE export_runs SET scrape_runs_pruned_at = now(), scrape_runs_pruned_rows = $2
		WHERE month = $1`, month, res.ScrapeRunsDeleted); err != nil {
		return res, fmt.Errorf("prune %s: mark scrape_runs pruned: %w", month, err)
	}
	if _, err := conn.Exec(ctx, "VACUUM (ANALYZE) scrape_runs"); err != nil {
		log.Printf("WARN prune %s: vacuum scrape_runs: %v", month, err)
	}
	log.Printf("prune %s: deleted %d scrape_runs rows below %s", month, res.ScrapeRunsDeleted, boundary.Format(time.RFC3339))
	return res, nil
}

// pruneLockKey is the pg advisory lock id held while a prune runs.
const pruneLockKey int64 = 7302112601

// deleteBatches deletes rows in [minID, maxID] (table-scoped id batches,
// autocommit, one statement per batch, Pause between). extra is appended to
// the WHERE clause, bound to $3... via extraArgs. Progress is logged every
// 50 batches.
func deleteBatches(ctx context.Context, conn *pgxpool.Conn, table string, minID, maxID int64, o PruneOptions, extra string, extraArgs ...any) (int64, error) {
	var deleted int64
	for i, b := range idBatches(minID, maxID, o.BatchSize) {
		args := append([]any{b[0], b[1]}, extraArgs...)
		ct, err := conn.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE id >= $1 AND id < $2 AND %s", table, extra), args...)
		if err != nil {
			return deleted, err
		}
		deleted += ct.RowsAffected()
		if (i+1)%50 == 0 {
			log.Printf("prune: %s batch %d: %d rows deleted so far", table, i+1, deleted)
		}
		select {
		case <-time.After(o.Pause):
		case <-ctx.Done():
			return deleted, ctx.Err()
		}
	}
	return deleted, nil
}

// idBatches splits [minID, maxID] into half-open [a, b) id ranges of at most
// step ids; the last batch ends at maxID+1 so maxID is included.
func idBatches(min, max, step int64) [][2]int64 {
	if step <= 0 || min > max {
		return nil
	}
	var out [][2]int64
	for a := min; a <= max; a += step {
		b := a + step
		if b > max+1 {
			b = max + 1
		}
		out = append(out, [2]int64{a, b})
	}
	return out
}

// checkScrapeRunsCoverage guards the scrape_runs delete boundary: every
// uploaded month up to this one must carry its scrape_runs range, the
// months must be consecutive (no hole), consecutive ranges must touch or
// overlap, and no fetch-log row below the boundary may predate the oldest
// range. Pure — called with the export_runs chain and the oldest
// scrape_runs row below the boundary (nil = none).
func checkScrapeRunsCoverage(runs []ExportRun, oldest *time.Time, boundary time.Time) error {
	for i := range runs {
		if runs[i].ScrapeRunsFrom == nil || runs[i].ScrapeRunsTo == nil {
			return fmt.Errorf("scrape_runs coverage: %s has no scrape_runs range", runs[i].Month)
		}
	}
	for i := 0; i+1 < len(runs); i++ {
		a, b := runs[i], runs[i+1]
		if nextMonth(a.Month) != b.Month {
			return fmt.Errorf("scrape_runs coverage: hole between %s and %s", a.Month, b.Month)
		}
		if b.ScrapeRunsFrom.After(a.ScrapeRunsTo.Add(time.Second)) {
			return fmt.Errorf("scrape_runs coverage: %s starts %s, after %s ends %s",
				b.Month, b.ScrapeRunsFrom, a.Month, a.ScrapeRunsTo)
		}
	}
	if oldest != nil && len(runs) > 0 && oldest.Before(runs[0].ScrapeRunsFrom.Add(-time.Second)) {
		return fmt.Errorf("scrape_runs coverage: fetch-log row at %s below boundary %s predates the oldest exported range (%s)",
			oldest, boundary.Format(time.RFC3339), runs[0].ScrapeRunsFrom)
	}
	return nil
}

// nextMonth returns the calendar month after "YYYY-MM".
func nextMonth(ym string) string {
	t, err := ParseMonth(ym)
	if err != nil {
		return ""
	}
	return MonthString(t.AddDate(0, 1, 0))
}

// setPruneError records a failed prune on the export_runs row (best effort,
// no upsert — a row must exist).
func setPruneError(ctx context.Context, pool *pgxpool.Pool, month string, err error) error {
	msg := "prune: " + err.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	_, uerr := pool.Exec(ctx, "UPDATE export_runs SET last_error = $2 WHERE month = $1", month, msg)
	return uerr
}

// fmtTS renders a nullable timestamp for error messages.
func fmtTS(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
