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
	DirectionEva    string
	PlannedTime     time.Time
	ActualTime      *time.Time
	Platform        string
	PlannedPlatform string
	ViaSlugs        []string
	ViaEvas         []string
	TripID          string
	TripDate        *time.Time
	TripUUID        string
	Notes           string // raw notes text; resolved to notes_id by PersistStopEvent
	NotesID         int64  // FK to note_texts.id; set by PersistStopEvent
	ScrapedAt       time.Time
	ParentEva       string
	StationName     string // resolved display name; used by PersistStopEvent when inserting a discovered station
	Cancelled       bool   // true when the train is cancelled (notes contain "fällt heute aus" etc.)
}

// FetchStationBoardInput is the input to the FetchStationBoard activity.
type FetchStationBoardInput struct {
	Slug      string
	Direction string
}

// FetchStationBoardResult is the output of FetchStationBoard. It returns
// parsed StopEvents directly (no separate ParseBoard step needed) plus the
// resolved station EVA and display name.
type FetchStationBoardResult struct {
	Events       []StopEvent
	ScrapedAt    time.Time
	URL          string
	ResolvedEva  string
	ResolvedName string
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