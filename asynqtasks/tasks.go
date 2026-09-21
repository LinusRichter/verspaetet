package asynqtasks

// Task type names.
const (
	TypeBoardFetch     = "board:fetch"
	TypeStationResolve = "station:resolve"
	TypeExportMonth    = "export:month"
)

// Queue names.
const (
	QueueDefault   = "default"
	QueueDiscovery = "discovery"
	QueueExport    = "export"
)

// BoardFetchPayload is the payload for a board fetch + persist task.
// One task per STATION — fchg/plan return both directions in one document;
// the worker splits arrival/departure batches client-side (zero extra requests).
type BoardFetchPayload struct {
	Eva string `json:"eva"`
}

// StationResolvePayload records unresolved route-path names.
type StationResolvePayload struct {
	Names    []string `json:"names"`
	SeenFrom string    `json:"seen_from"`
}

// ExportMonthPayload exports one operating month to Parquet + manifest.
type ExportMonthPayload struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}