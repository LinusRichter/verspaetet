package shared

import "time"

// StopEvent is one row of a station board as observed at a single scrape —
// the central persisted entity. One IRIS <s> element yields up to two
// StopEvents: arrival (<ar>) and departure (<dp>).
type StopEvent struct {
	ID              int64
	ScrapeRunID     int64
	StationEva      string
	Direction       string // "departure" | "arrival"
	StopID          string // IRIS s.id — persistent stop key
	TripDate        *time.Time
	LineCategory    string // tl.c (ICE, RE, S, ...)
	TrainNumber     string // tl.n
	Owner           string // tl.o
	TripKind        string // tl.t (p/e/z/s/h/n)
	DirectionName   string // pde/cde
	ViaPath         []string
	PlannedTime     time.Time
	ActualTime      *time.Time
	PlannedPlatform string
	Platform        string
	Cancelled       bool
	ScrapedAt       time.Time
	ParentEva       string // station that discovered this one (path names), "" for direct scrapes
}

// FetchStationBoardResult is the output of a board fetch (both directions).
type FetchStationBoardResult struct {
	Events    []StopEvent
	ScrapedAt time.Time

	// PlanBaseline carries the scheduled times (pt) from the freshly fetched
	// /plan hour, per stop and direction. The fchg document omits pt for ~90%
	// of its events; rows persisted while a trip was still outside the current
	// plan hour therefore carry the communicated time in planned_time (their
	// delay is masked to 0). When a plan hour arrives (PlanFresh), the worker
	// backfills already-persisted rows of these stops with the true schedule.
	PlanBaseline []PlanBaselineRow
	PlanFresh    bool
}

// PlanBaselineRow is one stop's scheduled arrival/departure from /plan.
type PlanBaselineRow struct {
	StopID      string
	ArrivalPT   *time.Time
	DeparturePT *time.Time
}

// PersistResult is the output of PersistStopEvent.
type PersistResult struct {
	ScrapeRunID int64
	NewStations []string // route-path station names not yet in stations
}

// Slugify converts a station name to a lowercase URL-safe slug
// (used for StaDa-derived stations; the old bahnhof.de slugs are gone).
func Slugify(name string) string {
	return slugify(name)
}
