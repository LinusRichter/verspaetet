package activities

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"verspaetet/shared"

	"github.com/PuerkitoBio/goquery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
)

// Process holds the processing activities: ParseBoard, PersistStopEvent,
// GetStationCadence. (DiscoverStations is folded into the workflows — see
// ticket T08 — and is NOT a method on this struct.) The Pool field
// (a *pgxpool.Pool) is set by the process-worker (T15); nil on the
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

// tripInfoRe parses the Fahrtinformationen link href. Captures:
//   1: fahrtverlauf|wagenreihung
//   2: YYYYMMDD trip_date
//   3: UUID
//   4: trip_id (e.g. 8000105_D_1)
var tripInfoRe = regexp.MustCompile(`/(fahrtverlauf|wagenreihung)/(\d{8})-([0-9a-f-]{36})/(.+)$`)

// platformRe extracts a platform token from a cleaned "Gleis" cell text.
// Allows a trailing letter (e.g. 14a) and slash-split platforms (e.g. 5/6).
var platformRe = regexp.MustCompile(`Gleis\s+([0-9]+[a-zA-Z]?(?:/[0-9]+[a-zA-Z]?)?)`)

// plannedPlatformRe extracts "geplant war Gleis X" from cleaned text.
var plannedPlatformRe = regexp.MustCompile(`geplant war Gleis\s+([0-9]+[a-zA-Z]?)`)

// ParseBoard is the pure-HTML-parse activity. It implements the parse rule in
// docs/sources/page-abfahrt-ankunft.md verbatim. No I/O, no DB.
func (a *Process) ParseBoard(ctx context.Context, in shared.ParseBoardInput) ([]shared.StopEvent, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(in.HTML))
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	events := make([]shared.StopEvent, 0)
	doc.Find("main h2").Each(func(_ int, h2 *goquery.Selection) {
		row := h2.Parent()
		if row.Length() == 0 {
			return
		}
		// Skip wing-train sub-rows: their <h2> is a screen-reader summary
		// heading for a wing section (data-is-wing-train="true" on the row
		// container), not a real board row. The docs do not mention wing
		// trains; flagging this for the human.
		if isWingTrainRow(row) {
			return
		}
		ev := shared.StopEvent{
			StationSlug:  in.StationSlug,
			StationEva:   in.StationEva,
			StationName:  in.StationName,
			Direction:    in.Direction,
			ScrapedAt:    in.ScrapedAt,
			ParentEva:    in.ParentEva,
			ViaSlugs:     []string{},
			ViaEvas:      []string{},
		}

		// line_label: first span[aria-hidden="true"] inside <h2>, whitespace-removed.
		h2.Find("span[aria-hidden=\"true\"]").First().Each(func(_ int, s *goquery.Selection) {
			ev.LineLabel = strings.Join(strings.Fields(s.Text()), "")
		})

		// line_category: Ersatzbus detection OR prefix mapping.
		ev.LineCategory = categorizeLine(h2, row)

		// direction cell: direct child whose text starts with "nach"/"aus" (case-insensitive).
		directionCell, directionLink := findDirectionCell(row)
		if directionCell != nil {
			if directionLink != nil && directionLink.Length() > 0 {
				// direction_name: aria-hidden chip span inside the link (SHORT form).
				directionLink.Find("span[aria-hidden=\"true\"]").First().Each(func(_ int, s *goquery.Selection) {
					name := strings.Join(strings.Fields(s.Text()), " ")
					name = strings.TrimSuffix(name, ".")
					ev.DirectionName = name
				})
				if href, ok := directionLink.Attr("href"); ok {
					ev.DirectionSlug = strings.TrimPrefix(href, "/")
				}
			} else {
				// No link (observed on rows to foreign stations like Bruxelles
				// Midi — the destination is rendered as a <span>, not an <a>).
				// Fall back to the station-name span text; slug stays empty
				// (the docs say direction_slug is NOT NULL, but the live DOM
				// contradicts this for a handful of rows — flagging for human).
				if nameSpan := directionCell.Find("[data-testid=\"station-name\"]").First(); nameSpan.Length() > 0 {
					ev.DirectionName = strings.TrimSuffix(strings.Join(strings.Fields(nameSpan.Text()), " "), ".")
				}
			}
		}

		// times: <time> elements; prefer datetime attr.
		timeSel := row.Find("time")
		if t0 := timeSel.First(); t0.Length() > 0 {
			if dt, ok := t0.Attr("datetime"); ok && dt != "" {
				if parsed, perr := time.Parse(time.RFC3339, dt); perr == nil {
					ev.PlannedTime = parsed.UTC()
				}
			}
		}
		if timeSel.Length() >= 2 {
			if t1 := timeSel.Eq(1); t1.Length() > 0 {
				if dt, ok := t1.Attr("datetime"); ok && dt != "" {
					if parsed, perr := time.Parse(time.RFC3339, dt); perr == nil {
						p := parsed.UTC()
						ev.ActualTime = &p
					}
				}
			}
		}

		// platform cell: direct child whose text contains "Gleis".
		platformCell := findCellContaining(row, "Gleis")
		if platformCell != nil {
			clean := cleanCellText(platformCell)
			beforeChange := clean
			if idx := strings.Index(clean, "Gleiswechsel"); idx >= 0 {
				beforeChange = clean[:idx]
			}
			if m := platformRe.FindStringSubmatch(beforeChange); m != nil {
				ev.Platform = m[1]
			}
			if m := plannedPlatformRe.FindStringSubmatch(clean); m != nil {
				ev.PlannedPlatform = m[1]
			}
		}
		// planned_platform fallback: notes <p> texts (cleaned).
		if ev.PlannedPlatform == "" {
			noteTexts := notesTexts(row)
			joined := strings.Join(noteTexts, " ")
			if m := plannedPlatformRe.FindStringSubmatch(joined); m != nil {
				ev.PlannedPlatform = m[1]
			}
		}

		// via cell: direct child whose text starts with "über" (case-insensitive).
		viaCell := findCellPrefix(row, "über")
		if viaCell != nil {
			viaCell.Find("a").Each(func(_ int, a *goquery.Selection) {
				if href, ok := a.Attr("href"); ok {
					ev.ViaSlugs = append(ev.ViaSlugs, strings.TrimPrefix(href, "/"))
				}
			})
		}

		// trip info: exactly one Fahrtinformationen link per row.
		href := ""
		row.Find("a[href*=\"/fahrtverlauf/\"]").Each(func(_ int, a *goquery.Selection) {
			if h, ok := a.Attr("href"); ok && href == "" {
				href = h
			}
		})
		if href == "" {
			row.Find("a[href*=\"/wagenreihung/\"]").Each(func(_ int, a *goquery.Selection) {
				if h, ok := a.Attr("href"); ok && href == "" {
					href = h
				}
			})
		}
		if m := tripInfoRe.FindStringSubmatch(href); m != nil {
			ev.TripID = m[4]
			if td, perr := time.Parse("20060102", m[2]); perr == nil {
				ev.TripDate = td
			}
			ev.TripUUID = m[3]
		}

		// notes: ul[aria-label contains "Hinweise zur Fahrt"] > li > p only.
		ev.Notes = buildNotes(row)
		ev.Cancelled = isCancelled(ev.Notes)

		events = append(events, ev)
	})

	return events, nil
}

// PersistStopEvent is the only activity that writes to Postgres. One
// transaction per batch. See docs/architecture/activity-persist-stopevent.md.
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

	// Step 1: resolve the canonical EVA. The `stations` table is AUTHORITATIVE:
	// FetchStationBoard's ResolvedEva (parsed from a trip_id prefix) can be the
	// EVA of a controlling station for a through-train, not the scraped
	// station's own EVA — so if the slug already exists in stations, use that
	// EVA unconditionally. ResolvedEva is only a fallback for genuinely new
	// stations.
	var dbEva string
	lookupErr := tx.QueryRow(ctx, "SELECT eva FROM stations WHERE slug = $1", stationSlug).Scan(&dbEva)
	switch {
	case lookupErr == nil:
		stationEva = dbEva
	case errors.Is(lookupErr, pgx.ErrNoRows):
		// Station unknown → keep ResolvedEva (stationEva from input); if empty,
		// we cannot persist. The station row is created in step 2.
		if stationEva == "" {
			return shared.PersistResult{}, &ErrUnresolvedStation{Slug: stationSlug}
		}
	default:
		return shared.PersistResult{}, fmt.Errorf("resolve eva: %w", lookupErr)
	}
	for i := range events {
		events[i].StationEva = stationEva
	}

	// Step 2: upsert station. The two-statement form in docs/data/schema.md
	// fires the second INSERT only when the slug was unknown; for seed stations
	// (slug already in the DB) the first statement is a no-op and the second
	// would raise stations_slug_key (a row with the same slug/EVA already
	// exists). So guard the second statement: only run it when the EVA is not
	// yet in stations (i.e. a genuinely discovered station).
	var stationExists bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stations WHERE eva = $1)", stationEva).Scan(&stationExists)
	if err != nil {
		return shared.PersistResult{}, fmt.Errorf("station exists check: %w", err)
	}
	if !stationExists {
		// Prefer the resolved display name (parsed by FetchStationBoard from
		// the board page <title>, e.g. "Abfahrt – Essen Hbf" → "Essen Hbf").
		// Fall back to the slug when no name was resolved (e.g. empty night
		// board or a page whose <title> had no "– " separator).
		name := first.StationName
		if name == "" {
			name = stationSlug
		}
		// discovered_from references stations(eva) via a FK. Under concurrent
		// discovery the parent station may not be persisted yet, so only set
		// discovered_from when the parent EVA already exists; otherwise NULL
		// (safe — the FK can never fire).
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
		// Second statement: insert-by-EVA (back-fill path). Use a single
		// statement that is safe for BOTH conflict axes:
		//   - WHERE NOT EXISTS(... slug taken by a DIFFERENT eva ...) skips the
		//     insert when the slug already belongs to another station row, so
		//     the unique constraint on slug can never fire.
		//   - ON CONFLICT (eva) DO NOTHING handles the eva-already-exists case.
		// discovered_from is guarded the same way as above.
		// (Postgres allows only ONE ON CONFLICT clause per INSERT, so the slug
		// guard must live in the WHERE.)
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

	// Step 2b: upsert the lines referenced by this batch (the stop_events
	// table has a composite FK to lines; lines must exist before the events).
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
  SELECT id, actual_time, platform, planned_platform, notes, direction_name
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
  notes, scraped_at, direction_slug, reason_code, cancelled
)
  SELECT $5, $1, $2, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $3, $4, $16, $17, $18, $19, $20, $21
  WHERE NOT EXISTS (
    SELECT 1 FROM latest
    WHERE latest.actual_time      IS NOT DISTINCT FROM $11
      AND latest.platform         IS NOT DISTINCT FROM $12
      AND latest.planned_platform IS NOT DISTINCT FROM $13
      AND latest.notes            IS NOT DISTINCT FROM $17
      AND latest.direction_name   IS NOT DISTINCT FROM $8
  )`
	for _, ev := range events {
		// Skip events with an empty line_label — these are parse failures
		// (the <h2> chip span was not found), not unknown categories. Log
		// them so the parser bug is visible. Events with a non-empty
		// LineLabel are always persisted: unclassifiable labels are mapped
		// to the "unknown" category by categoryByPrefix (migration 0004).
		if ev.LineLabel == "" {
			activity.GetLogger(ctx).Warn("PersistStopEvent: skipping event with empty LineLabel (parse failure — chip span not found)",
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
		viaEvas := ev.ViaEvas
		if viaEvas == nil {
			viaEvas = []string{}
		}
		viaSlugs := ev.ViaSlugs
		if viaSlugs == nil {
			viaSlugs = []string{}
		}
		// Map empty platform/planned_platform to NULL so the
		// platform_changes view's IS NOT NULL filter works (T19).
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
		_, err := tx.Exec(ctx, upsertSQL,
			stationEva, ev.Direction, ev.TripID, ev.TripDate,
			scrapeRunID, ev.LineLabel, ev.LineCategory,
			ev.DirectionName, nil, ev.PlannedTime, actualTime, platform,
			plannedPlatform, viaEvas, viaSlugs, ev.TripUUID, ev.Notes, ev.ScrapedAt,
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

// --- ParseBoard helpers ---

// isWingTrainRow reports whether the row container is a wing-train sub-row
// (data-is-wing-train="true"). Wing-train rows have a screen-reader <h2>
// that is NOT a real board row; the parser skips them.
func isWingTrainRow(row *goquery.Selection) bool {
	if v, ok := row.Attr("data-is-wing-train"); ok {
		return v == "true"
	}
	return false
}

// categorizeLine classifies the row's line. Ersatzbus detection first, then
// prefix mapping. See docs/sources/page-abfahrt-ankunft.md — line_category.
func categorizeLine(h2, row *goquery.Selection) string {
	// (1) Ersatzbus: h2's first direct-child span text === "Ersatzbus".
	// This is the only reliable signal — the notes token "Ersatzverkehr"
	// also appears in regular S/RE construction notices and was
	// misclassifying real S-bahn rows as ersatz (T19 retrospective).
	firstChild := h2.Children().First()
	if firstChild.Length() > 0 && strings.TrimSpace(firstChild.Text()) == "Ersatzbus" {
		return "ersatz"
	}

	label := strings.ToUpper(strings.Join(strings.Fields(h2.Find("span[aria-hidden=\"true\"]").First().Text()), ""))
	return categoryByPrefix(label)
}

// categoryByPrefix maps a line label to a category by prefix.
func categoryByPrefix(label string) string {
	switch {
	case strings.HasPrefix(label, "ICE"), strings.HasPrefix(label, "TGV"),
		strings.HasPrefix(label, "RJ"), strings.HasPrefix(label, "ECE"):
		return "fern"
	case strings.HasPrefix(label, "RE"), strings.HasPrefix(label, "RB"),
		strings.HasPrefix(label, "ME"), strings.HasPrefix(label, "HEX"),
		strings.HasPrefix(label, "ALX"), strings.HasPrefix(label, "ERB"):
		return "regio"
	case isSingleDigitPrefix(label, "S"):
		return "s_bahn"
	case isSingleDigitPrefix(label, "U"):
		return "u_bahn"
	case strings.HasPrefix(label, "TRAM"):
		return "strassenbahn"
	case strings.HasPrefix(label, "BUS"), strings.HasPrefix(label, "B"):
		return "bus"
	case isAllDigits(label):
		return "strassenbahn"
	default:
		return "unknown"
	}
}

// isSingleDigitPrefix returns true if label is prefix + a single ASCII digit
// (e.g. "S5", "U6"). Ersatzbus labels like "S3E" fail (good — they are caught
// earlier by the Ersatzbus rule).
func isSingleDigitPrefix(label, prefix string) bool {
	if !strings.HasPrefix(label, prefix) {
		return false
	}
	rest := label[len(prefix):]
	if len(rest) != 1 {
		return false
	}
	return rest[0] >= '0' && rest[0] <= '9'
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// findDirectionCell returns the row's direction cell (label "nach"/"aus")
// and the link inside it.
func findDirectionCell(row *goquery.Selection) (*goquery.Selection, *goquery.Selection) {
	var dirCell, dirLink *goquery.Selection
	row.Children().Each(func(_ int, c *goquery.Selection) {
		if dirCell != nil {
			return
		}
		t := strings.ToLower(strings.Join(strings.Fields(c.Text()), " "))
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "nach") || strings.HasPrefix(t, "aus") {
			dirCell = c
			dirLink = c.Find("a").First()
		}
	})
	return dirCell, dirLink
}

// findCellContaining returns the first direct child whose text contains substr.
func findCellContaining(row *goquery.Selection, substr string) *goquery.Selection {
	var found *goquery.Selection
	row.Children().Each(func(_ int, c *goquery.Selection) {
		if found != nil {
			return
		}
		if strings.Contains(c.Text(), substr) {
			found = c
		}
	})
	return found
}

// findCellPrefix returns the first direct child whose trimmed lowercased text
// starts with the given prefix.
func findCellPrefix(row *goquery.Selection, prefix string) *goquery.Selection {
	var found *goquery.Selection
	pLower := strings.ToLower(prefix)
	row.Children().Each(func(_ int, c *goquery.Selection) {
		if found != nil {
			return
		}
		t := strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(c.Text()), " ")))
		if strings.HasPrefix(t, pLower) {
			found = c
		}
	})
	return found
}

// cleanCellText clones a selection's node, removes [aria-hidden="true"]
// descendants, and returns the whitespace-collapsed textContent. Used for the
// duplicate-number bug on platform/planned_platform and notes <p>.
func cleanCellText(sel *goquery.Selection) string {
	clone := sel.Clone()
	clone.Find("[aria-hidden=\"true\"]").Remove()
	return strings.Join(strings.Fields(clone.Text()), " ")
}

// notesTexts returns the cleaned per-<p> text of all <li><p> notes inside the
// row's "Hinweise zur Fahrt" list (skipping <li> that contain only a <button>).
func notesTexts(row *goquery.Selection) []string {
	out := []string{}
	row.Find("ul[aria-label]").Each(func(_ int, ul *goquery.Selection) {
		label, _ := ul.Attr("aria-label")
		if !strings.Contains(strings.ToLower(strings.TrimSpace(label)), "hinweise zur fahrt") {
			return
		}
		ul.Find("li").Each(func(_ int, li *goquery.Selection) {
			p := li.Find("p").First()
			if p.Length() == 0 {
				return
			}
			out = append(out, cleanCellText(p))
		})
	})
	return out
}

// buildNotes joins the cleaned notes <p> texts with " \u23ce " and truncates
// to 2000 chars. Empty string when no notes.
func buildNotes(row *goquery.Selection) string {
	texts := notesTexts(row)
	if len(texts) == 0 {
		return ""
	}
	joined := strings.Join(texts, " \u23ce ")
	if len(joined) > 2000 {
		joined = joined[:2000]
	}
	return joined
}

// mapReason maps free-text notes to the DB's structured delay-reason taxonomy
// (verified live on the fahrtverlauf page RSC payload, 2026-08-07). Returns ""
// when no known reason matches. The taxonomy may grow; unknown reasons stay "".
// See docs/architecture/delay-forensics.md.
func mapReason(notes string) string {
	n := strings.ToLower(notes)
	switch {
	case strings.Contains(n, "notarzteinsatz"):
		return "MEDICAL_EMERGENCY"
	case strings.Contains(n, "polizeieinsatz"):
		return "POLICE_ACTIVITY"
	case strings.Contains(n, "streik"):
		return "STRIKE"
	case strings.Contains(n, "streckenstörung"), strings.Contains(n, "strecke") && strings.Contains(n, "beeinträchtigt"):
		return "TECHNICAL_PROBLEM_RAILWAY_SECTION"
	case strings.Contains(n, "technische störung am zug"):
		return "TECHNICAL_PROBLEM_VEHICLE"
	case strings.Contains(n, "technische störung"):
		return "TECHNICAL_PROBLEM_OTHER"
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

// isCancelled reports whether the notes indicate a cancelled train.
func isCancelled(notes string) bool {
	n := strings.ToLower(notes)
	return strings.Contains(n, "fällt heute aus") ||
		strings.Contains(n, "entfällt") ||
		strings.Contains(n, "ausfall")
}