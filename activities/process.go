package activities

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"verspaetet/shared"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Process holds the processing activities: PersistStopEvent.
// The Pool field is set by the worker; nil is invalid.
type Process struct {
	Pool *pgxpool.Pool
}

// ErrUnresolvedStation is a non-retryable error returned by PersistStopEvent
// when the batch's StationEva has no stations row (station not imported).
type ErrUnresolvedStation struct {
	Eva string
}

func (e *ErrUnresolvedStation) Error() string {
	return fmt.Sprintf("ErrUnresolvedStation: no stations row for eva %q", e.Eva)
}

var _ error = (*ErrUnresolvedStation)(nil)

// PersistStopEvent is the only activity that writes to Postgres. One
// transaction per batch. All events in a batch share station+direction.
//
// Dedup: a new row is inserted only when something observable changed since
// the latest snapshot of the same (eva, direction, stop_id) — actual_time,
// platform, direction_name, via_path, or cancelled. The UNIQUE constraint
// (station_eva, direction, stop_id, scraped_at) is the DB-level race safety
// net (ON CONFLICT DO NOTHING).
func (a *Process) PersistStopEvent(ctx context.Context, events []shared.StopEvent) (shared.PersistResult, error) {
	if a.Pool == nil {
		return shared.PersistResult{}, errors.New("PersistStopEvent: Process.Pool is nil")
	}
	if len(events) == 0 {
		return shared.PersistResult{}, nil
	}

	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return shared.PersistResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	first := events[0]
	stationEva := first.StationEva
	direction := first.Direction

	// Verify the station exists (imported via StaDa) — non-retryable if not.
	var eva string
	err = tx.QueryRow(ctx, "SELECT eva FROM stations WHERE eva = $1", stationEva).Scan(&eva)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return shared.PersistResult{}, &ErrUnresolvedStation{Eva: stationEva}
		}
		return shared.PersistResult{}, fmt.Errorf("resolve station: %w", err)
	}

	// scrape_runs row (idempotent upsert).
	var scrapeRunID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO scrape_runs (station_eva, direction, scraped_at)
		 VALUES ($1, $2, date_trunc('second', $3::timestamptz))
		 ON CONFLICT (station_eva, direction, scraped_at) DO UPDATE SET scraped_at = EXCLUDED.scraped_at
		 RETURNING id`,
		stationEva, direction, first.ScrapedAt,
	).Scan(&scrapeRunID)
	if err != nil {
		return shared.PersistResult{}, fmt.Errorf("upsert scrape_runs: %w", err)
	}

	upsertSQL := `
WITH latest AS (
  SELECT id, actual_time, platform, direction_name, via_path, cancelled
  FROM stop_events
  WHERE station_eva = $1
    AND direction    = $2
    AND stop_id      = $3
  ORDER BY scraped_at DESC
  LIMIT 1
)
INSERT INTO stop_events (
  scrape_run_id, station_eva, direction, stop_id, trip_date,
  line_category, train_number, owner, trip_kind,
  direction_name, via_path, planned_time, actual_time,
  planned_platform, platform, cancelled, scraped_at
)
  SELECT $4, $1, $2, $3, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
  WHERE NOT EXISTS (
    SELECT 1 FROM latest
    WHERE latest.actual_time    IS NOT DISTINCT FROM $13
      AND latest.platform       IS NOT DISTINCT FROM $15
      AND latest.direction_name IS NOT DISTINCT FROM $10
      AND latest.via_path       IS NOT DISTINCT FROM $11
      AND latest.cancelled      IS NOT DISTINCT FROM $16
  )
  ON CONFLICT (station_eva, direction, stop_id, scraped_at) DO NOTHING`

	for _, ev := range events {
		var tripDate interface{}
		if ev.TripDate != nil {
			tripDate = *ev.TripDate
		}
		var actualTime interface{}
		if ev.ActualTime != nil {
			actualTime = *ev.ActualTime
		}
		viaPath := ev.ViaPath
		if viaPath == nil {
			viaPath = []string{}
		}
		var plannedPlatform interface{}
		if ev.PlannedPlatform != "" {
			plannedPlatform = ev.PlannedPlatform
		}
		var platform interface{}
		if ev.Platform != "" {
			platform = ev.Platform
		}
		var trainNumber interface{}
		if ev.TrainNumber != "" {
			trainNumber = ev.TrainNumber
		}
		var owner interface{}
		if ev.Owner != "" {
			owner = ev.Owner
		}
		_, err := tx.Exec(ctx, upsertSQL,
			stationEva, ev.Direction, ev.StopID,
			scrapeRunID, tripDate,
			ev.LineCategory, trainNumber, owner, ev.TripKind,
			ev.DirectionName, viaPath, ev.PlannedTime, actualTime,
			plannedPlatform, platform, ev.Cancelled, ev.ScrapedAt,
		)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("upsert stop_event (stop_id=%s): %w", ev.StopID, err)
		}
	}

	// Collect unresolved route-path names for discovery.
	nameSet := map[string]struct{}{}
	for _, ev := range events {
		if ev.DirectionName != "" {
			nameSet[ev.DirectionName] = struct{}{}
		}
		for _, n := range ev.ViaPath {
			if n != "" {
				nameSet[n] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	newStations := make([]string, 0)
	if len(names) > 0 {
		// Which names already exist as stations?
		knownRows, err := tx.Query(ctx,
			`SELECT name FROM stations WHERE name = ANY($1)`, names)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("query known names: %w", err)
		}
		known := map[string]struct{}{}
		for knownRows.Next() {
			var n string
			if err := knownRows.Scan(&n); err == nil {
				known[n] = struct{}{}
			}
		}
		knownRows.Close()
		// Which names are already pending (seen before)?
		pendingRows, err := tx.Query(ctx,
			`SELECT name FROM pending_stations WHERE name = ANY($1)`, names)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("query pending names: %w", err)
		}
		pending := map[string]struct{}{}
		for pendingRows.Next() {
			var n string
			if err := pendingRows.Scan(&n); err == nil {
				pending[n] = struct{}{}
			}
		}
		pendingRows.Close()
		for _, n := range names {
			_, isKnown := known[n]
			_, isPending := pending[n]
			if !isKnown && !isPending {
				newStations = append(newStations, n)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return shared.PersistResult{}, fmt.Errorf("commit: %w", err)
	}

	return shared.PersistResult{
		ScrapeRunID: scrapeRunID,
		NewStations: newStations,
	}, nil
}

// RecordPendingStations inserts unseen route-path names into pending_stations
// so the next StaDa refresh can try to resolve them.
func (a *Process) RecordPendingStations(ctx context.Context, names []string, seenFrom string) error {
	if a.Pool == nil {
		return errors.New("RecordPendingStations: Process.Pool is nil")
	}
	if len(names) == 0 {
		return nil
	}
	_, err := a.Pool.Exec(ctx,
		`INSERT INTO pending_stations (name, seen_from)
		 SELECT n, $2 FROM unnest($1::text[]) AS n
		 ON CONFLICT (name) DO NOTHING`,
		names, seenFrom,
	)
	if err != nil {
		return fmt.Errorf("record pending stations: %w", err)
	}
	return nil
}

var _ = log.Printf
var _ = time.Now