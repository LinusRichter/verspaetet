package activities

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"verspaetet/shared"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
)

// Process holds the processing activities: PersistStopEvent, GetStationCadence.
// The Pool field (a *pgxpool.Pool) is set by the process-worker; nil on the
// fetch-worker where these activities are not called.
type Process struct {
	Pool *pgxpool.Pool
}

// ErrUnresolvedStation is a non-retryable error returned by PersistStopEvent
// when the batch's StationEva is empty and no stations row with the batch's
// StationSlug exists. Register it in the workflow's RetryPolicy
// NonRetryableErrorTypes.
type ErrUnresolvedStation struct {
	Slug string
}

func (e *ErrUnresolvedStation) Error() string {
	return fmt.Sprintf("ErrUnresolvedStation: no eva and no stations row for slug %q", e.Slug)
}

var _ error = (*ErrUnresolvedStation)(nil)

// PersistStopEvent is the only activity that writes to Postgres. One
// transaction per batch.
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
	stationSlug := first.StationSlug
	stationEva := first.StationEva
	parentEva := first.ParentEva

	// Step 1: resolve the canonical EVA from the stations table (authoritative).
	var dbEva string
	lookupErr := tx.QueryRow(ctx, "SELECT eva FROM stations WHERE slug = $1", stationSlug).Scan(&dbEva)
	switch {
	case lookupErr == nil:
		stationEva = dbEva
	case errors.Is(lookupErr, pgx.ErrNoRows):
		if stationEva == "" {
			return shared.PersistResult{}, &ErrUnresolvedStation{Slug: stationSlug}
		}
	default:
		return shared.PersistResult{}, fmt.Errorf("resolve eva: %w", lookupErr)
	}
	for i := range events {
		events[i].StationEva = stationEva
	}

	// Step 2: upsert station if not yet in the DB.
	var stationExists bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stations WHERE eva = $1)", stationEva).Scan(&stationExists)
	if err != nil {
		return shared.PersistResult{}, fmt.Errorf("station exists check: %w", err)
	}
	if !stationExists {
		name := first.StationName
		if name == "" {
			name = stationSlug
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO stations (eva, slug, name, category, discovered_at, discovered_from)
		     SELECT $1, $2, $3, NULL, NOW(),
		            CASE WHEN EXISTS (SELECT 1 FROM stations WHERE eva = $4) THEN $4 ELSE NULL END
		     ON CONFLICT (slug) DO NOTHING`,
			stationEva, stationSlug, name, parentEva,
		)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("station upsert (slug): %w", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO stations (eva, slug, name, category, discovered_at, discovered_from)
		     SELECT $1, $2, $3, NULL, NOW(),
		            CASE WHEN EXISTS (SELECT 1 FROM stations WHERE eva = $4) THEN $4 ELSE NULL END
		     WHERE NOT EXISTS (SELECT 1 FROM stations WHERE slug = $2 AND eva <> $1)
		     ON CONFLICT (eva) DO NOTHING`,
			stationEva, stationSlug, name, parentEva,
		)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("station upsert (eva): %w", err)
		}
	}

	// Step 2b: upsert the lines referenced by this batch.
	{
		seen := map[[2]string]struct{}{}
		for _, ev := range events {
			key := [2]string{ev.LineLabel, ev.LineCategory}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if ev.LineLabel == "" {
				continue
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO lines (line_label, line_category) VALUES ($1, $2)
		         ON CONFLICT (line_label, line_category) DO NOTHING`,
				ev.LineLabel, ev.LineCategory,
			)
			if err != nil {
				return shared.PersistResult{}, fmt.Errorf("line upsert (%s/%s): %w", ev.LineLabel, ev.LineCategory, err)
			}
		}
	}

	// Step 2c: resolve notes text → notes_id via the lookup table.
	for i := range events {
		if events[i].Notes == "" {
			continue
		}
		var notesID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO note_texts (text) VALUES ($1)
		     ON CONFLICT (text) DO UPDATE SET text = EXCLUDED.text
		     RETURNING id`,
			events[i].Notes,
		).Scan(&notesID)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("notes upsert: %w", err)
		}
		events[i].NotesID = notesID
	}

	// Step 3: create the scrape_runs row (idempotent).
	var scrapeRunID int64
	direction := first.Direction
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

	// Step 4: stamp scrape_run_id on every event.
	for i := range events {
		events[i].ScrapeRunID = scrapeRunID
	}

	// Step 5: per-event upsert with the dedup rule.
	upsertSQL := `
WITH latest AS (
  SELECT id, actual_time, platform, planned_platform, notes_id, direction_name
  FROM stop_events
  WHERE station_eva = $1
    AND direction   = $2
    AND trip_id      = $3
    AND trip_date    = $4
  ORDER BY scraped_at DESC
  LIMIT 1
)
INSERT INTO stop_events (
  scrape_run_id, station_eva, direction, line_label, line_category,
  direction_name, direction_eva, planned_time, actual_time, platform,
  planned_platform, via_evas, via_slugs, trip_id, trip_date, trip_uuid,
  notes_id, scraped_at, direction_slug, reason_code, cancelled
)
  SELECT $5, $1, $2, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $3, $4, $16, $17, $18, $19, $20, $21
  WHERE NOT EXISTS (
    SELECT 1 FROM latest
    WHERE latest.actual_time      IS NOT DISTINCT FROM $11
      AND latest.platform         IS NOT DISTINCT FROM $12
      AND latest.planned_platform IS NOT DISTINCT FROM $13
      AND latest.notes_id         IS NOT DISTINCT FROM $17
      AND latest.direction_name   IS NOT DISTINCT FROM $8
  )`
	for _, ev := range events {
		if ev.LineLabel == "" {
			activity.GetLogger(ctx).Warn("PersistStopEvent: skipping event with empty LineLabel",
				"direction", ev.Direction,
				"direction_name", ev.DirectionName,
				"planned_time", ev.PlannedTime,
				"trip_id", ev.TripID)
			continue
		}
		var actualTime interface{}
		if ev.ActualTime != nil {
			actualTime = *ev.ActualTime
		}
		var tripDate interface{}
		if ev.TripDate != nil {
			tripDate = *ev.TripDate
		}
		viaEvas := ev.ViaEvas
		if viaEvas == nil {
			viaEvas = []string{}
		}
		viaSlugs := ev.ViaSlugs
		if viaSlugs == nil {
			viaSlugs = []string{}
		}
		var platform, plannedPlatform interface{}
		if ev.Platform != "" {
			platform = ev.Platform
		}
		if ev.PlannedPlatform != "" {
			plannedPlatform = ev.PlannedPlatform
		}
		var directionSlug interface{}
		if ev.DirectionSlug != "" {
			directionSlug = ev.DirectionSlug
		}
		var directionEva interface{}
		if ev.DirectionEva != "" {
			directionEva = ev.DirectionEva
		}
		var notesID interface{}
		if ev.NotesID != 0 {
			notesID = ev.NotesID
		}
		_, err := tx.Exec(ctx, upsertSQL,
			stationEva, ev.Direction, ev.TripID, tripDate,
			scrapeRunID, ev.LineLabel, ev.LineCategory,
			ev.DirectionName, directionEva, ev.PlannedTime, actualTime, platform,
			plannedPlatform, viaEvas, viaSlugs, ev.TripUUID, notesID, ev.ScrapedAt,
			directionSlug, mapReason(ev.Notes), ev.Cancelled,
		)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("upsert stop_event (trip_id=%s): %w", ev.TripID, err)
		}
	}

	// Collect NewStations: via/direction slugs not yet in stations.slug.
	slugSet := map[string]struct{}{}
	for _, ev := range events {
		if ev.DirectionSlug != "" {
			slugSet[ev.DirectionSlug] = struct{}{}
		}
		for _, s := range ev.ViaSlugs {
			slugSet[s] = struct{}{}
		}
	}
	candidateSlugs := make([]string, 0, len(slugSet))
	for s := range slugSet {
		candidateSlugs = append(candidateSlugs, s)
	}
	newStations := make([]string, 0)
	if len(candidateSlugs) > 0 {
		knownRows, err := tx.Query(ctx,
			`SELECT slug FROM stations WHERE slug = ANY($1)`, candidateSlugs)
		if err != nil {
			return shared.PersistResult{}, fmt.Errorf("query known slugs: %w", err)
		}
		known := map[string]struct{}{}
		for knownRows.Next() {
			var s string
			if err := knownRows.Scan(&s); err == nil {
				known[s] = struct{}{}
			}
		}
		knownRows.Close()
		for _, s := range candidateSlugs {
			if _, ok := known[s]; !ok {
				newStations = append(newStations, s)
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

// GetStationCadence is a tiny read-only DB activity. Returns Cadence=0 when
// the station row is missing or cadence_override is NULL.
func (a *Process) GetStationCadence(ctx context.Context, in shared.GetStationCadenceInput) (shared.GetStationCadenceResult, error) {
	if a.Pool == nil {
		return shared.GetStationCadenceResult{}, errors.New("GetStationCadence: Process.Pool is nil")
	}
	var cadence *time.Duration
	err := a.Pool.QueryRow(ctx,
		"SELECT cadence_override FROM stations WHERE slug = $1", in.StationSlug,
	).Scan(&cadence)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return shared.GetStationCadenceResult{Cadence: 0}, nil
		}
		return shared.GetStationCadenceResult{}, fmt.Errorf("get cadence: %w", err)
	}
	if cadence == nil {
		return shared.GetStationCadenceResult{Cadence: 0}, nil
	}
	return shared.GetStationCadenceResult{Cadence: *cadence}, nil
}

// mapReason maps free-text notes to the DB's structured delay-reason taxonomy.
// Returns "" when no known reason matches.
func mapReason(notes string) string {
	n := strings.ToLower(notes)
	switch {
	case strings.Contains(n, "notarzteinsatz"), strings.Contains(n, "personenunfall"):
		return "MEDICAL_EMERGENCY"
	case strings.Contains(n, "polizeieinsatz"):
		return "POLICE_ACTIVITY"
	case strings.Contains(n, "streik"):
		return "STRIKE"
	case strings.Contains(n, "bauarbeiten"), strings.Contains(n, "baustelle"):
		return "CONSTRUCTION"
	case strings.Contains(n, "signalstörung"):
		return "TECHNICAL_PROBLEM_SIGNAL"
	case strings.Contains(n, "weichenstörung"):
		return "TECHNICAL_PROBLEM_SWITCH"
	case strings.Contains(n, "oberleitungsstörung"), strings.Contains(n, "oberleitung"):
		return "TECHNICAL_PROBLEM_OVERHEAD"
	case strings.Contains(n, "streckenstörung"), strings.Contains(n, "strecke") && strings.Contains(n, "beeinträchtigt"):
		return "TECHNICAL_PROBLEM_RAILWAY_SECTION"
	case strings.Contains(n, "technische störung am zug"), strings.Contains(n, "fahrzeugstörung"):
		return "TECHNICAL_PROBLEM_VEHICLE"
	case strings.Contains(n, "technische störung"), strings.Contains(n, "betriebsstörung"):
		return "TECHNICAL_PROBLEM_OTHER"
	case strings.Contains(n, "verspätung eines vorausfahrenden"),
		strings.Contains(n, "verspätung eines vorherigen"),
		strings.Contains(n, "verspätung aus vorheriger"):
		return "PREVIOUS_TRAIN_DELAY"
	case strings.Contains(n, "verzögerung im betriebsablauf"), strings.Contains(n, "betriebsablauf"):
		return "OPERATIONAL_DELAY"
	case strings.Contains(n, "tier auf der strecke"), strings.Contains(n, "tierauf"):
		return "ANIMAL_ON_TRACK"
	case strings.Contains(n, "hitze"):
		return "WEATHER_HEAT"
	case strings.Contains(n, "unwetter"):
		return "WEATHER_STORM"
	case strings.Contains(n, "winterwitterung"):
		return "WEATHER_WINTER"
	default:
		return ""
	}
}