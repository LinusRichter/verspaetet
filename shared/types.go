package shared

import "time"

// Task queues.
const (
	DiscoveryQueue = "discovery-queue"
	MonitorQueue   = "monitor-queue"
)

// Workflow names (registered on workers, used as the workflow type by clients).
const (
	StationDiscoveryWorkflowName = "StationDiscovery"
	StationMonitorWorkflowName   = "StationMonitor"
)

// StopEvent is one row of a station board as observed at a single scrape —
// the central persisted entity. See docs/domain/stop-event.md.
type StopEvent struct {
	ID              int64
	ScrapeRunID     int64
	StationSlug     string
	StationEva      string
	Direction       string
	LineLabel       string
	LineCategory    string
	DirectionName   string
	DirectionSlug   string
	PlannedTime     time.Time
	ActualTime      *time.Time
	Platform        string
	PlannedPlatform string
	ViaSlugs        []string
	ViaEvas         []string
	TripID          string
	TripDate        time.Time
	TripUUID        string
	Notes           string
	ScrapedAt       time.Time
	ParentEva       string
	StationName     string // resolved display name; used by PersistStopEvent when inserting a discovered station
	Cancelled       bool   // true when the train is cancelled (notes contain "fällt heute aus" etc.)
}

// FetchStationBoardInput is the input to the FetchStationBoard activity.
// See docs/architecture/activity-fetch-station-board.md.
type FetchStationBoardInput struct {
	Slug      string
	Direction string
}

// FetchStationBoardResult is the output of FetchStationBoard.
type FetchStationBoardResult struct {
	HTML         string
	ScrapedAt    time.Time
	URL          string
	ResolvedEva  string
	ResolvedName string // station display name parsed from the page <title>; empty if the title had no "– " separator
}

// ParseBoardInput is the input to the ParseBoard activity.
// See docs/architecture/activity-parse-board.md.
type ParseBoardInput struct {
	HTML         string
	Direction    string
	StationSlug  string
	StationEva   string
	ParentEva    string
	ScrapedAt    time.Time
	StationName  string // resolved display name from FetchStationBoardResult.ResolvedName; used by PersistStopEvent for discovered stations
}

// PersistResult is the output of PersistStopEvent.
// See docs/architecture/activity-persist-stopevent.md.
type PersistResult struct {
	ScrapeRunID int64
	NewStations []string
}

// GetStationCadenceInput is the input to the GetStationCadence activity.
// See docs/architecture/workflow-station-monitor.md.
type GetStationCadenceInput struct {
	StationSlug string
}

// GetStationCadenceResult is the output of GetStationCadence. Cadence is 0
// if stations.cadence_override is NULL (caller falls back to 5m per ADR-06).
type GetStationCadenceResult struct {
	Cadence time.Duration
}