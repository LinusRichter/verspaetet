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

// FetchStationBoardInput is the input to a board fetch.
type FetchStationBoardInput struct {
	Eva      string
	Direction string // "departure" | "arrival"
}

// FetchStationBoardResult is the output of a board fetch.
type FetchStationBoardResult struct {
	Events    []StopEvent
	ScrapedAt time.Time
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