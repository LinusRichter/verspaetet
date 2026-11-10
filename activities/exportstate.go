package activities

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// export_runs tracks the pipeline state of every operating month:
// export -> upload -> offsite backup -> prune. It is the single source of
// truth for "is this month done", so failed exports can be retried
// unattended and no month is ever pruned before it is safely published.

var monthRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)

// ParseMonth parses a strict "YYYY-MM" string and returns the first day of
// that month, 00:00 UTC. Anything else is an error.
func ParseMonth(s string) (time.Time, error) {
	if !monthRE.MatchString(s) {
		return time.Time{}, fmt.Errorf("invalid month %q (want YYYY-MM)", s)
	}
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid month %q: %w", s, err)
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), nil
}

// MonthString formats a time as "YYYY-MM" (UTC).
func MonthString(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// ExportRun mirrors one export_runs row. Nullable columns are pointers.
type ExportRun struct {
	Month                string
	Attempts             int
	LastAttemptAt        *time.Time
	LastError            *string
	Rows                 *int64
	StopEventsSha256     *string
	MaxScrapedAt         *time.Time
	ScrapeRunsRows       *int64
	ScrapeRunsSha256     *string
	ScrapeRunsFrom       *time.Time
	ScrapeRunsTo         *time.Time
	ExportedAt           *time.Time
	UploadedAt           *time.Time
	OffsiteAt            *time.Time
	PruneStartedAt       *time.Time
	PrunedAt             *time.Time
	PrunedRows           *int64
	PruneNote            *string
	ScrapeRunsPrunedAt   *time.Time
	ScrapeRunsPrunedRows *int64
}

const exportRunCols = `month, attempts, last_attempt_at, last_error, rows, stop_events_sha256,
	max_scraped_at, scrape_runs_rows, scrape_runs_sha256, scrape_runs_from, scrape_runs_to,
	exported_at, uploaded_at, offsite_at, prune_started_at, pruned_at, pruned_rows, prune_note,
	scrape_runs_pruned_at, scrape_runs_pruned_rows`

func scanExportRun(row interface{ Scan(...any) error }) (*ExportRun, error) {
	var r ExportRun
	if err := row.Scan(&r.Month, &r.Attempts, &r.LastAttemptAt, &r.LastError,
		&r.Rows, &r.StopEventsSha256, &r.MaxScrapedAt,
		&r.ScrapeRunsRows, &r.ScrapeRunsSha256, &r.ScrapeRunsFrom, &r.ScrapeRunsTo,
		&r.ExportedAt, &r.UploadedAt, &r.OffsiteAt,
		&r.PruneStartedAt, &r.PrunedAt, &r.PrunedRows, &r.PruneNote,
		&r.ScrapeRunsPrunedAt, &r.ScrapeRunsPrunedRows); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetExportRun returns the export_runs row for a "YYYY-MM" month, or
// (nil, nil) when no row exists.
func GetExportRun(ctx context.Context, pool *pgxpool.Pool, month string) (*ExportRun, error) {
	row := pool.QueryRow(ctx, "SELECT "+exportRunCols+" FROM export_runs WHERE month = $1", month)
	r, err := scanExportRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// ListExportRuns returns all export_runs rows ordered by month.
func ListExportRuns(ctx context.Context, pool *pgxpool.Pool) ([]ExportRun, error) {
	rows, err := pool.Query(ctx, "SELECT "+exportRunCols+" FROM export_runs ORDER BY month")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRun
	for rows.Next() {
		r, err := scanExportRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// MarkExportAttempt records one export attempt (upsert): attempts+1,
// last_attempt_at=now().
func MarkExportAttempt(ctx context.Context, pool *pgxpool.Pool, month string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO export_runs (month, attempts, last_attempt_at)
		VALUES ($1, 1, now())
		ON CONFLICT (month) DO UPDATE
		SET attempts = export_runs.attempts + 1, last_attempt_at = now()`, month)
	return err
}

// MarkExportFailed records the error of a failed attempt (upsert).
func MarkExportFailed(ctx context.Context, pool *pgxpool.Pool, month string, err error) error {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	_, uerr := pool.Exec(ctx, `
		INSERT INTO export_runs (month, last_error)
		VALUES ($1, $2)
		ON CONFLICT (month) DO UPDATE SET last_error = $2`, month, msg)
	return uerr
}

// MarkExported records a locally completed export (upsert) from the manifest.
func MarkExported(ctx context.Context, pool *pgxpool.Pool, month string, m *Manifest) error {
	if m == nil {
		return fmt.Errorf("MarkExported %s: no manifest", month)
	}
	var maxScraped, srFrom, srTo *time.Time
	if m.StopEvents.MaxScraped != "" {
		t, err := time.Parse(time.RFC3339, m.StopEvents.MaxScraped)
		if err != nil {
			return fmt.Errorf("MarkExported %s: parse max_scraped_at %q: %w", month, m.StopEvents.MaxScraped, err)
		}
		maxScraped = &t
	}
	if m.ScrapeRuns.From != "" {
		t, err := time.Parse(time.RFC3339, m.ScrapeRuns.From)
		if err != nil {
			return fmt.Errorf("MarkExported %s: parse scrape_runs.from %q: %w", month, m.ScrapeRuns.From, err)
		}
		srFrom = &t
	}
	if m.ScrapeRuns.To != "" {
		t, err := time.Parse(time.RFC3339, m.ScrapeRuns.To)
		if err != nil {
			return fmt.Errorf("MarkExported %s: parse scrape_runs.to %q: %w", month, m.ScrapeRuns.To, err)
		}
		srTo = &t
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO export_runs (month, rows, stop_events_sha256, max_scraped_at,
			scrape_runs_rows, scrape_runs_sha256, scrape_runs_from, scrape_runs_to,
			exported_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), NULL)
		ON CONFLICT (month) DO UPDATE SET
			rows = $2, stop_events_sha256 = $3, max_scraped_at = $4,
			scrape_runs_rows = $5, scrape_runs_sha256 = $6,
			scrape_runs_from = $7, scrape_runs_to = $8,
			exported_at = now(), last_error = NULL`,
		month, m.StopEvents.Rows, m.StopEvents.Sha256, maxScraped,
		m.ScrapeRuns.Rows, m.ScrapeRuns.Sha256, srFrom, srTo)
	return err
}

// MarkUploaded marks the month as uploaded AND verified on HuggingFace.
// Errors when no export_runs row was updated (nothing exported yet).
func MarkUploaded(ctx context.Context, pool *pgxpool.Pool, month string) error {
	ct, err := pool.Exec(ctx, `
		UPDATE export_runs SET uploaded_at = now(), last_error = NULL
		WHERE month = $1`, month)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("MarkUploaded %s: no export_runs row updated", month)
	}
	return nil
}

// MarkOffsite records the operator's off-site backup of the month's files.
// Requires an exported row; errors otherwise.
func MarkOffsite(ctx context.Context, pool *pgxpool.Pool, month string) error {
	ct, err := pool.Exec(ctx, `
		UPDATE export_runs SET offsite_at = now()
		WHERE month = $1 AND exported_at IS NOT NULL`, month)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("MarkOffsite %s: no exported export_runs row", month)
	}
	return nil
}

// DueExportMonths returns, of the previous two calendar months (UTC), those
// whose export deadline (first day of the following month + 3 days finality
// lag + 4h) has passed. Oldest first.
func DueExportMonths(now time.Time) []string {
	now = now.UTC()
	var due []string
	for i := 2; i >= 1; i-- {
		m := time.Date(now.Year(), now.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		deadline := m.AddDate(0, 1, 0).Add(3*24*time.Hour + 4*time.Hour)
		if !now.Before(deadline) {
			due = append(due, MonthString(m))
		}
	}
	return due
}

// MonthsNeedingExport filters DueExportMonths down to the months that still
// need work: no export_runs row at all, or the row has no uploaded_at
// (hfEnabled) resp. no exported_at (local-only mode).
func MonthsNeedingExport(ctx context.Context, pool *pgxpool.Pool, now time.Time, hfEnabled bool) ([]string, error) {
	var need []string
	for _, ym := range DueExportMonths(now) {
		run, err := GetExportRun(ctx, pool, ym)
		if err != nil {
			return nil, err
		}
		if run == nil {
			need = append(need, ym)
			continue
		}
		if hfEnabled && run.UploadedAt == nil {
			need = append(need, ym)
		}
		if !hfEnabled && run.ExportedAt == nil {
			need = append(need, ym)
		}
	}
	return need, nil
}
