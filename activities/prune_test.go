package activities

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIDBatches(t *testing.T) {
	cases := []struct {
		name     string
		min, max int64
		step     int64
		want     [][2]int64
	}{
		{"empty (min > max)", 5, 1, 10, nil},
		{"one row", 7, 7, 10, [][2]int64{{7, 8}}},
		{"max on step boundary", 0, 9, 10, [][2]int64{{0, 10}}},
		{"max off step boundary", 1, 11, 5, [][2]int64{{1, 6}, {6, 11}, {11, 12}}},
		{"exact multiple", 1, 10, 5, [][2]int64{{1, 6}, {6, 11}}},
		{"zero step", 1, 10, 0, nil},
	}
	for _, c := range cases {
		got := idBatches(c.min, c.max, c.step)
		if len(got) != len(c.want) {
			t.Errorf("%s: idBatches(%d, %d, %d) = %v, want %v", c.name, c.min, c.max, c.step, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: idBatches(%d, %d, %d) = %v, want %v", c.name, c.min, c.max, c.step, got, c.want)
				break
			}
		}
	}
}

func TestCheckScrapeRunsCoverage(t *testing.T) {
	run := func(month, from, to string) ExportRun {
		f, _ := time.Parse(time.RFC3339, from)
		tt, _ := time.Parse(time.RFC3339, to)
		return ExportRun{Month: month, ScrapeRunsFrom: &f, ScrapeRunsTo: &tt}
	}
	chain := []ExportRun{
		run("2026-09", "2026-09-20T18:00:00Z", "2026-10-01T09:00:00Z"),
		run("2026-10", "2026-09-30T18:00:00Z", "2026-11-01T09:00:00Z"),
		run("2026-11", "2026-10-31T18:00:00Z", "2026-12-01T09:00:00Z"),
	}
	oldest := time.Date(2026, 9, 20, 17, 59, 59, 0, time.UTC) // exactly U[0].from - 1s: allowed
	boundary := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	if err := checkScrapeRunsCoverage(chain, &oldest, boundary); err != nil {
		t.Errorf("ok case: %v", err)
	}
	if err := checkScrapeRunsCoverage(chain, nil, boundary); err != nil {
		t.Errorf("no rows below boundary: %v", err)
	}

	// Hole between months.
	hole := []ExportRun{chain[0], chain[2]}
	if err := checkScrapeRunsCoverage(hole, nil, boundary); err == nil || !strings.Contains(err.Error(), "hole") {
		t.Errorf("hole: err = %v, want hole", err)
	}

	// Non-overlapping consecutive ranges (gap in the fetch log).
	gap := []ExportRun{chain[0], run("2026-10", "2026-10-02T18:00:00Z", "2026-11-01T09:00:00Z")}
	if err := checkScrapeRunsCoverage(gap, nil, boundary); err == nil || !strings.Contains(err.Error(), "starts") {
		t.Errorf("gap: err = %v, want range gap", err)
	}

	// A fetch-log row older than the first exported range.
	tooOld := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	if err := checkScrapeRunsCoverage(chain, &tooOld, boundary); err == nil || !strings.Contains(err.Error(), "predates") {
		t.Errorf("old row: err = %v, want predates", err)
	}

	// A row without its scrape_runs range.
	noRange := []ExportRun{chain[0], {Month: "2026-10"}, chain[2]}
	if err := checkScrapeRunsCoverage(noRange, nil, boundary); err == nil || !strings.Contains(err.Error(), "no scrape_runs range") {
		t.Errorf("missing range: err = %v, want no-range error", err)
	}
}

// --- integration (needs TEST_POSTGRES_DSN) ---

const (
	ptEvaA = "8000001"
	ptEvaB = "8000002"
)

func ptOptions() PruneOptions {
	return PruneOptions{
		BatchSize: 10, // exercise multiple batches
		Pause:     time.Millisecond,
		Now:       func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
		Verify:    func(ctx context.Context, run *ExportRun) error { return nil },
	}
}

func seedStation(t *testing.T, pool *pgxpool.Pool, eva string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO stations (eva, name, slug, fetch_offset) VALUES ($1, $1, $1, 0)", eva); err != nil {
		t.Fatalf("seed station %s: %v", eva, err)
	}
}

// seedMonth inserts n scrape_runs (1 minute apart from start) and one
// stop_event per run (same scraped_at, trip_date on the month's first day).
func seedMonth(t *testing.T, pool *pgxpool.Pool, eva string, start time.Time, n int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO scrape_runs (station_eva, direction, scraped_at)
		SELECT $1, 'departure', $2::timestamptz + make_interval(mins => g)
		FROM generate_series(0, $3::int - 1) g`, eva, start, n); err != nil {
		t.Fatalf("seed scrape_runs %s: %v", eva, err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO stop_events (scrape_run_id, station_eva, direction, stop_id,
			trip_date, line_category, planned_time, scraped_at)
		SELECT r.id, r.station_eva, r.direction, 'stop-' || r.id, $4::date, 'RE', r.scraped_at, r.scraped_at + interval '700 milliseconds'
		FROM scrape_runs r
		WHERE r.station_eva = $1 AND r.scraped_at >= $2 AND r.scraped_at < $3`,
		eva, start, start.AddDate(0, 0, 1), start.Format("2006-01-02")); err != nil {
		t.Fatalf("seed stop_events %s: %v", eva, err)
	}
}

// seedExportRun inserts a fully published export_runs row for one month
// (max_scraped_at = srTo; offsite=false leaves the off-site gate closed).
func seedExportRun(t *testing.T, pool *pgxpool.Pool, month string, rows int64, srFrom, srTo time.Time, offsite bool) {
	t.Helper()
	offsiteExpr := "now()"
	if !offsite {
		offsiteExpr = "NULL"
	}
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`
		INSERT INTO export_runs (month, rows, stop_events_sha256, max_scraped_at,
			scrape_runs_rows, scrape_runs_sha256, scrape_runs_from, scrape_runs_to,
			exported_at, uploaded_at, offsite_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now(), %s)`, offsiteExpr),
		month, rows, "sha-"+month, srTo, rows, "sr-sha-"+month, srFrom, srTo); err != nil {
		t.Fatalf("seed export_runs %s: %v", month, err)
	}
}

func countOf(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func TestPruneMonthIntegration(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	t.Run("month not final", func(t *testing.T) {
		pool := testDB(t)
		o := ptOptions()
		o.Now = func() time.Time { return time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC) }
		_, err := PruneMonth(context.Background(), pool, "2026-01", o)
		if err == nil || !strings.Contains(err.Error(), "not final") {
			t.Fatalf("err = %v, want not final", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedStation(t, pool, ptEvaB)
		seedMonth(t, pool, ptEvaA, jan, 100)
		seedMonth(t, pool, ptEvaB, feb, 100)
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)

		res, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.StopEventsDeleted != 100 {
			t.Errorf("stop_events deleted = %d, want 100", res.StopEventsDeleted)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE trip_date >= $1 AND trip_date < $2", jan, feb); n != 0 {
			t.Errorf("month A stop_events left: %d", n)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE trip_date >= $1 AND trip_date < $2", feb, mar); n != 100 {
			t.Errorf("month B stop_events: %d, want 100", n)
		}
		// Fetch log below the boundary is gone; month B's runs are kept.
		if res.ScrapeRunsDeleted != 99 {
			t.Errorf("scrape_runs deleted = %d, want 99 (boundary is this chunk's scrape_runs_to)", res.ScrapeRunsDeleted)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM scrape_runs WHERE station_eva = $1", ptEvaB); n != 100 {
			t.Errorf("month B scrape_runs: %d, want 100", n)
		}
		run, err := GetExportRun(ctx, pool, "2026-01")
		if err != nil || run == nil {
			t.Fatal(err)
		}
		if run.PrunedAt == nil || run.ScrapeRunsPrunedAt == nil || run.LastError != nil {
			t.Errorf("run state after prune: %+v", run)
		}
		if run.PrunedRows == nil || *run.PrunedRows != 100 {
			t.Errorf("pruned_rows = %v, want 100", run.PrunedRows)
		}
		if run.PruneNote != nil {
			t.Errorf("prune_note = %q, want NULL (no late rows)", *run.PruneNote)
		}
	})

	t.Run("count mismatch deletes nothing", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedMonth(t, pool, ptEvaA, jan, 100)
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)
		// One extra row inside the predicate → count(101) != rows(100).
		if _, err := pool.Exec(ctx, `
			INSERT INTO stop_events (scrape_run_id, station_eva, direction, stop_id,
				trip_date, line_category, planned_time, scraped_at)
			SELECT id, station_eva, direction, 'extra-1', $2::date, 'RE', scraped_at, scraped_at
			FROM scrape_runs WHERE station_eva = $1 LIMIT 1`, ptEvaA, jan.Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}

		_, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err == nil || !strings.Contains(err.Error(), "count mismatch") {
			t.Fatalf("err = %v, want count mismatch", err)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE trip_date >= $1 AND trip_date < $2", jan, feb); n != 101 {
			t.Errorf("rows deleted despite mismatch: %d left, want 101", n)
		}
		run, err := GetExportRun(ctx, pool, "2026-01")
		if err != nil || run == nil {
			t.Fatal(err)
		}
		if run.PrunedAt != nil {
			t.Error("pruned_at set despite mismatch")
		}
		if run.LastError == nil || !strings.HasPrefix(*run.LastError, "prune: ") {
			t.Errorf("last_error = %v, want prune:-prefixed", run.LastError)
		}
	})

	t.Run("late row kept and counted", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedMonth(t, pool, ptEvaA, jan, 100)
		cut := jan.Add(99 * time.Minute).Add(time.Second)
		// A row scraped after the chunk's cut — part of the NEXT chunk.
		if _, err := pool.Exec(ctx, `
			INSERT INTO scrape_runs (station_eva, direction, scraped_at)
			VALUES ($1, 'departure', $2)`, ptEvaA, cut.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO stop_events (scrape_run_id, station_eva, direction, stop_id,
				trip_date, line_category, planned_time, scraped_at)
			SELECT id, station_eva, direction, 'late-1', $2::date, 'RE', scraped_at, scraped_at
			FROM scrape_runs WHERE station_eva = $1 AND scraped_at = $3`, ptEvaA, jan.Format("2006-01-02"), cut.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)

		res, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.StopEventsDeleted != 100 || res.LateRows != 1 {
			t.Errorf("deleted = %d, late = %d, want 100/1", res.StopEventsDeleted, res.LateRows)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE station_eva = $1 AND stop_id = 'late-1'", ptEvaA); n != 1 {
			t.Errorf("late row deleted: %d left", n)
		}
		run, err := GetExportRun(ctx, pool, "2026-01")
		if err != nil || run == nil {
			t.Fatal(err)
		}
		if run.PruneNote == nil || !strings.Contains(*run.PruneNote, "1 late rows kept") {
			t.Errorf("prune_note = %v, want late-rows note", run.PruneNote)
		}
	})

	t.Run("missing offsite_at refuses", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedMonth(t, pool, ptEvaA, jan, 100)
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), false)

		_, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err == nil || !strings.Contains(err.Error(), "offsite_at") {
			t.Fatalf("err = %v, want offsite gate", err)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE trip_date >= $1 AND trip_date < $2", jan, feb); n != 100 {
			t.Errorf("rows deleted: %d left, want 100", n)
		}
	})

	t.Run("resume after interrupted prune", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedMonth(t, pool, ptEvaA, jan, 100)
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)
		// Half the rows are already gone and prune_started_at is set.
		if _, err := pool.Exec(ctx, `
			DELETE FROM stop_events WHERE id IN (
				SELECT id FROM stop_events WHERE trip_date >= $1 AND trip_date < $2
				ORDER BY id LIMIT 50)`, jan, feb); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE export_runs SET prune_started_at = now() WHERE month = '2026-01'`); err != nil {
			t.Fatal(err)
		}

		res, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.StopEventsDeleted != 50 {
			t.Errorf("stop_events deleted = %d, want 50 (the remaining half)", res.StopEventsDeleted)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE trip_date >= $1 AND trip_date < $2", jan, feb); n != 0 {
			t.Errorf("rows left: %d", n)
		}
		run, err := GetExportRun(ctx, pool, "2026-01")
		if err != nil || run == nil {
			t.Fatal(err)
		}
		if run.PrunedAt == nil {
			t.Error("pruned_at not set after resume")
		}
	})

	t.Run("next month overlaps: boundary truncated to whole seconds", func(t *testing.T) {
		// Realistic shape: February trips are already scraped on Jan 31,
		// before January's fetch-log range ends. stop_events keeps sub-second
		// scraped_at, scrape_runs is truncated to seconds.
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedMonth(t, pool, ptEvaA, jan, 100)
		overlap := jan.Add(50 * time.Minute)
		if _, err := pool.Exec(ctx, `
			INSERT INTO scrape_runs (station_eva, direction, scraped_at) VALUES ($1, 'arrival', $2)`, ptEvaA, overlap); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO stop_events (scrape_run_id, station_eva, direction, stop_id, trip_date, line_category, planned_time, scraped_at)
			SELECT id, station_eva, direction, 'feb-1', $2::date, 'RE', scraped_at, scraped_at + interval '700 milliseconds'
			FROM scrape_runs WHERE direction = 'arrival' AND scraped_at = $1`, overlap, feb.Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)

		res, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Boundary.Equal(overlap) {
			t.Errorf("boundary = %s, want %s", res.Boundary, overlap)
		}
		if res.ScrapeRunsDeleted != 50 {
			t.Errorf("scrape_runs deleted = %d, want 50", res.ScrapeRunsDeleted)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM stop_events WHERE stop_id = 'feb-1'"); n != 1 {
			t.Errorf("february row lost: %d", n)
		}
	})

	t.Run("coverage hole prunes stop_events but not the fetch log", func(t *testing.T) {
		pool := testDB(t)
		ctx := context.Background()
		seedStation(t, pool, ptEvaA)
		seedStation(t, pool, ptEvaB)
		seedMonth(t, pool, ptEvaA, jan, 100)
		seedMonth(t, pool, ptEvaB, feb, 100)
		// Two uploaded months with a hole (2025-12 missing) → the fetch-log
		// delete would lose coverage of December.
		novStart := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
		seedExportRun(t, pool, "2025-11", 100, novStart, novStart.Add(99*time.Minute), true)
		seedExportRun(t, pool, "2026-01", 100, jan, jan.Add(99*time.Minute), true)

		res, err := PruneMonth(ctx, pool, "2026-01", ptOptions())
		if err == nil || !strings.Contains(err.Error(), "hole") {
			t.Fatalf("err = %v, want coverage hole", err)
		}
		if res.StopEventsDeleted != 100 {
			t.Errorf("stop_events deleted = %d, want 100 (part A ran first)", res.StopEventsDeleted)
		}
		if n := countOf(t, pool, "SELECT count(*) FROM scrape_runs"); n != 200 {
			t.Errorf("scrape_runs deleted despite coverage hole: %d left, want 200", n)
		}
		run, err := GetExportRun(ctx, pool, "2026-01")
		if err != nil || run == nil {
			t.Fatal(err)
		}
		if run.PrunedAt == nil {
			t.Error("stop_events prune should be recorded")
		}
		if run.ScrapeRunsPrunedAt != nil {
			t.Error("scrape_runs prune must not be recorded")
		}
		if run.LastError == nil || !strings.Contains(*run.LastError, "hole") {
			t.Errorf("last_error = %v, want the coverage error", run.LastError)
		}
	})
}
