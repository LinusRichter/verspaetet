package activities

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseMonth(t *testing.T) {
	for _, s := range []string{"2026-01", "2026-12"} {
		m, err := ParseMonth(s)
		if err != nil {
			t.Errorf("ParseMonth(%q): %v", s, err)
			continue
		}
		if got := MonthString(m); got != s {
			t.Errorf("ParseMonth(%q) = %v, MonthString = %q", s, m, got)
		}
		if m.Day() != 1 || m.Hour() != 0 || m.Location() != time.UTC {
			t.Errorf("ParseMonth(%q) = %v, want first day 00:00 UTC", s, m)
		}
	}
	for _, s := range []string{"", "2026-1", "2026-13", "2026-00", "26-01", "2026/01", "2026-01-02", "garbage"} {
		if _, err := ParseMonth(s); err == nil {
			t.Errorf("ParseMonth(%q) = no error, want error", s)
		}
	}
}

func TestDueExportMonths(t *testing.T) {
	cases := []struct {
		name string
		now  string
		want []string
	}{
		{"oct due, nov not", "2026-12-03T23:59:59Z", []string{"2026-10"}},
		{"one minute early", "2026-12-04T03:59:59Z", []string{"2026-10"}},
		{"exactly at deadline", "2026-12-04T04:00:00Z", []string{"2026-10", "2026-11"}},
		{"late in the month", "2026-12-31T12:00:00Z", []string{"2026-10", "2026-11"}},
		{"first month just due", "2026-11-04T04:00:00Z", []string{"2026-09", "2026-10"}},
		{"day before", "2026-11-04T03:59:59Z", []string{"2026-09"}},
		{"early in month", "2026-11-01T00:00:00Z", []string{"2026-09"}},
		{"non-UTC input", "2026-12-04T05:00:00+01:00", []string{"2026-10", "2026-11"}},
	}
	for _, c := range cases {
		now, err := time.Parse(time.RFC3339, c.now)
		if err != nil {
			t.Fatal(err)
		}
		got := DueExportMonths(now)
		if len(got) != len(c.want) {
			t.Errorf("%s: DueExportMonths(%s) = %v, want %v", c.name, c.now, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: DueExportMonths(%s) = %v, want %v", c.name, c.now, got, c.want)
				break
			}
		}
	}
}

// The export_runs upserts, end to end against a fresh schema.
func TestExportRunUpserts(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()

	month := "2026-10"
	if run, err := GetExportRun(ctx, pool, month); err != nil || run != nil {
		t.Fatalf("GetExportRun on empty table = (%v, %v), want (nil, nil)", run, err)
	}

	if err := MarkExportAttempt(ctx, pool, month); err != nil {
		t.Fatal(err)
	}
	if err := MarkExportAttempt(ctx, pool, month); err != nil {
		t.Fatal(err)
	}
	run, err := GetExportRun(ctx, pool, month)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Attempts != 2 || run.LastAttemptAt == nil {
		t.Fatalf("after 2 attempts: %+v", run)
	}

	// A failed attempt records the error.
	failErr := fmt.Errorf("boom")
	if err := MarkExportFailed(ctx, pool, month, failErr); err != nil {
		t.Fatal(err)
	}

	// A completed export clears last_error and fills the manifest fields.
	m := &Manifest{Year: 2026, Month: 10}
	m.StopEvents.Rows = 42
	m.StopEvents.Sha256 = "abc"
	m.StopEvents.MaxScraped = "2026-10-31T09:00:00Z"
	m.ScrapeRuns.Rows = 7
	m.ScrapeRuns.Sha256 = "def"
	m.ScrapeRuns.From = "2026-09-30T18:00:00Z"
	m.ScrapeRuns.To = "2026-11-01T09:00:00Z"
	if err := MarkExported(ctx, pool, month, m); err != nil {
		t.Fatal(err)
	}
	run, err = GetExportRun(ctx, pool, month)
	if err != nil {
		t.Fatal(err)
	}
	if run.LastError != nil {
		t.Errorf("last_error = %q after MarkExported, want NULL", *run.LastError)
	}
	if run.Rows == nil || *run.Rows != 42 || run.StopEventsSha256 == nil || *run.StopEventsSha256 != "abc" {
		t.Errorf("stop_events fields: %+v", run)
	}
	if run.MaxScrapedAt == nil || !run.MaxScrapedAt.Equal(time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("max_scraped_at: %+v", run.MaxScrapedAt)
	}
	if run.ScrapeRunsFrom == nil || run.ScrapeRunsTo == nil || run.ScrapeRunsRows == nil || *run.ScrapeRunsRows != 7 {
		t.Errorf("scrape_runs fields: %+v", run)
	}
	if run.ExportedAt == nil {
		t.Error("exported_at = NULL after MarkExported")
	}

	// Upload + offsite gates.
	if err := MarkUploaded(ctx, pool, month); err != nil {
		t.Fatal(err)
	}
	if err := MarkOffsite(ctx, pool, month); err != nil {
		t.Fatal(err)
	}
	run, err = GetExportRun(ctx, pool, month)
	if err != nil {
		t.Fatal(err)
	}
	if run.UploadedAt == nil || run.OffsiteAt == nil {
		t.Errorf("uploaded_at/offsite_at = %+v", run)
	}

	// The gates fail without a row (or without an exported row).
	if err := MarkUploaded(ctx, pool, "2026-11"); err == nil {
		t.Error("MarkUploaded on missing row: no error")
	}
	if err := MarkOffsite(ctx, pool, "2026-11"); err == nil {
		t.Error("MarkOffsite on missing row: no error")
	}
	if err := MarkExportAttempt(ctx, pool, "2026-11"); err != nil {
		t.Fatal(err)
	}
	if err := MarkOffsite(ctx, pool, "2026-11"); err == nil || !strings.Contains(err.Error(), "no exported export_runs row") {
		t.Errorf("MarkOffsite without exported_at: err = %v", err)
	}

	// Listing is ordered by month and MonthsNeedingExport honours state.
	if err := MarkExportAttempt(ctx, pool, "2026-12"); err != nil {
		t.Fatal(err)
	}
	runs, err := ListExportRuns(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].Month != "2026-10" || runs[1].Month != "2026-11" || runs[2].Month != "2026-12" {
		t.Errorf("ListExportRuns order: %+v", months(runs))
	}

	now := time.Date(2026, 12, 4, 4, 0, 0, 0, time.UTC) // 2026-10 and 2026-11 due
	need, err := MonthsNeedingExport(ctx, pool, now, true)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10 is uploaded; 2026-11 has only an attempt row → hf mode wants 11.
	if len(need) != 1 || need[0] != "2026-11" {
		t.Errorf("MonthsNeedingExport(hf) = %v, want [2026-11]", need)
	}
	// In local-only mode 2026-10 is done (exported), 2026-11 is not.
	need, err = MonthsNeedingExport(ctx, pool, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(need) != 1 || need[0] != "2026-11" {
		t.Errorf("MonthsNeedingExport(local) = %v, want [2026-11]", need)
	}
}

func months(runs []ExportRun) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.Month)
	}
	return out
}
