package asynqtasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hibiken/asynq"
)

// ExportTimeout bounds one export:month attempt: Parquet build (~30 min for
// 85 M rows) plus the HF upload, on the same box as the nightly pg_dump.
// EXPORT_TIMEOUT overrides the default (12h); invalid or <=0 falls back.
func ExportTimeout() time.Duration {
	d, err := time.ParseDuration(os.Getenv("EXPORT_TIMEOUT"))
	if err != nil || d <= 0 {
		return 12 * time.Hour
	}
	return d
}

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
	// EnqueuedAt (unix seconds) lets the worker drop stale backlog tasks
	// after an outage instead of replaying a whole cadence cycle late.
	EnqueuedAt int64 `json:"enqueued_at,omitempty"`
	// Recheck marks the monthly no_iris re-enable probe: never stale, no
	// per-station logging.
	Recheck bool `json:"recheck,omitempty"`
}

// StationResolvePayload records unresolved route-path names.
type StationResolvePayload struct {
	Names    []string `json:"names"`
	SeenFrom string   `json:"seen_from"`
}

// ExportMonthPayload exports one operating month to Parquet + manifest.
type ExportMonthPayload struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

// EnqueueMonthExport enqueues the export task for one month with the
// stable dedup id export:<YYYYMM>. Returns "enqueued" on success. When the
// id already exists: if that task is archived and replaceArchived is set,
// the archived twin is deleted and the task re-enqueued ("re-enqueued");
// otherwise the existing task's state is returned (e.g. "active",
// "pending", "retry", "archived") with a nil error.
func EnqueueMonthExport(ctx context.Context, client *asynq.Client, ins *asynq.Inspector, year, month int, replaceArchived bool) (string, error) {
	payload, err := json.Marshal(ExportMonthPayload{Year: year, Month: month})
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("export:%04d%02d", year, month)
	task := asynq.NewTask(TypeExportMonth, payload)
	opts := []asynq.Option{
		asynq.Queue(QueueExport),
		asynq.MaxRetry(5),
		asynq.Timeout(ExportTimeout()),
		asynq.TaskID(id), // dedup: once per month, ever
	}
	if _, err := client.EnqueueContext(ctx, task, opts...); err == nil {
		return "enqueued", nil
	} else if !errors.Is(err, asynq.ErrDuplicateTask) && !errors.Is(err, asynq.ErrTaskIDConflict) {
		return "", err
	}
	info, ierr := ins.GetTaskInfo(QueueExport, id)
	if ierr != nil {
		return "", fmt.Errorf("get task info %s: %w", id, ierr)
	}
	if info.State != asynq.TaskStateArchived || !replaceArchived {
		return info.State.String(), nil
	}
	if derr := ins.DeleteTask(QueueExport, id); derr != nil {
		return "", fmt.Errorf("delete archived %s: %w", id, derr)
	}
	if _, err := client.EnqueueContext(ctx, task, opts...); err != nil {
		return "", err
	}
	return "re-enqueued", nil
}
