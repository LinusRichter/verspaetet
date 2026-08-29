package asynqtasks

// Task type names.
const (
	TypeBoardFetch = "board:fetch"
	TypeDiscovery   = "discovery:fetch"
)

// Queue names.
const (
	QueueDefault   = "default"
	QueueDiscovery = "discovery"
)

// BoardFetchPayload is the payload for a board fetch + persist task.
type BoardFetchPayload struct {
	Slug      string `json:"slug"`
	Direction string `json:"direction"` // "departure" | "arrival"
}

// DiscoveryPayload is the payload for a discovery task (fetch board,
// persist, discover new stations, schedule monitors).
type DiscoveryPayload struct {
	Slug      string `json:"slug"`
	ParentEva string `json:"parent_eva"`
}