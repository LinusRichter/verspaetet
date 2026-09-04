package asynqtasks

// Task type names.
const (
	TypeBoardFetch     = "board:fetch"
	TypeStationResolve = "station:resolve"
)

// Queue names.
const (
	QueueDefault   = "default"
	QueueDiscovery = "discovery"
)

// BoardFetchPayload is the payload for a board fetch + persist task.
type BoardFetchPayload struct {
	Eva       string `json:"eva"`
	Direction string `json:"direction"` // "departure" | "arrival"
}

// StationResolvePayload records unresolved route-path names.
type StationResolvePayload struct {
	Names    []string `json:"names"`
	SeenFrom string    `json:"seen_from"`
}