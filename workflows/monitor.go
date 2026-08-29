package workflows

import (
	"hash/fnv"
	"time"

	"verspaetet/activities"
	"verspaetet/shared"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// StationMonitor is the monitor workflow: a long-running loop that fetches
// a station's departure and arrival boards, persists the observed StopEvents,
// and ContinueAsNews forever.
//
// firstCycle is true only on the very first run of a workflow execution; the
// ContinueAsNew call passes false so the jitter fires exactly once per
// station, not on every cycle (Attempt resets to 1 on ContinueAsNew).
//
// The jitter is derived deterministically from a hash of the slug — no
// math/rand (which would break Temporal replay determinism).
func StationMonitor(ctx workflow.Context, stationSlug string, firstCycle bool) error {
	logger := workflow.GetLogger(ctx)

	cadenceOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    100 * time.Millisecond,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
	fetchOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        5 * time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:        2 * time.Minute,
			MaximumAttempts:         4,
			NonRetryableErrorTypes: []string{"ErrInvalidInput"},
		},
	}
	persistOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        200 * time.Millisecond,
			BackoffCoefficient:     2.0,
			MaximumAttempts:        5,
			NonRetryableErrorTypes: []string{"ErrUnresolvedStation"},
		},
	}

	var (
		fetch   activities.Fetch
		process activities.Process
	)

	// Cadence: read via activity (the workflow MUST NOT touch the DB).
	cadenceCtx := workflow.WithActivityOptions(ctx, cadenceOpts)
	var cadence shared.GetStationCadenceResult
	if err := workflow.ExecuteActivity(cadenceCtx, process.GetStationCadence,
		shared.GetStationCadenceInput{StationSlug: stationSlug}).Get(cadenceCtx, &cadence); err != nil {
		logger.Warn("StationMonitor: GetStationCadence failed, using 30m default",
			"slug", stationSlug, "err", err)
		cadence.Cadence = 0
	}
	sleepFor := cadence.Cadence
	if sleepFor == 0 {
		sleepFor = 30 * time.Minute
	}

	// Stagger: on the first cycle only, sleep a deterministic pseudo-random
	// fraction of the cadence (hash of the slug) so stations don't all fire
	// at once. Deterministic = safe for Temporal replay.
	if firstCycle {
		jitter := time.Duration(hashToJitter(stationSlug) % int64(sleepFor))
		if jitter > 0 {
			if err := workflow.Sleep(ctx, jitter); err != nil {
				logger.Warn("StationMonitor: jitter sleep interrupted", "slug", stationSlug, "err", err)
			}
		}
	}

	cycleStart := workflow.Now(ctx)
	resolvedEva := ""

	for _, direction := range []string{"departure", "arrival"} {
		eva, err := runMonitorDirection(ctx, stationSlug, direction, fetchOpts, persistOpts, &fetch, &process, resolvedEva)
		if err != nil {
			logger.Warn("StationMonitor: direction cycle failed, skipping",
				"slug", stationSlug, "direction", direction, "err", err)
			continue
		}
		if resolvedEva == "" {
			resolvedEva = eva
		}
	}

	// Sleep the remainder of the cadence interval, clamped to >= 0.
	elapsed := workflow.Now(ctx).Sub(cycleStart)
	remaining := sleepFor - elapsed
	if remaining < 0 {
		remaining = 0
	}
	if err := workflow.Sleep(ctx, remaining); err != nil {
		logger.Warn("StationMonitor: sleep interrupted, continuing",
			"slug", stationSlug, "err", err)
	}

	return workflow.NewContinueAsNewError(ctx, StationMonitor, stationSlug, false)
}

// hashToJitter deterministically maps a slug to a pseudo-random int64.
// FNV-1a is fast and stable across runs/platforms — same slug always
// produces the same jitter, which keeps Temporal replays deterministic.
func hashToJitter(slug string) int64 {
	h := fnv.New64a()
	h.Write([]byte(slug))
	return int64(h.Sum64())
}

// runMonitorDirection runs fetch→parse→persist→discover for one direction.
// Errors are returned to the caller, which logs and skips the direction.
// resolvedEva is this station's EVA (reused across directions within a cycle);
// it is passed to startDiscoveryChildren as the children's parentEva so
// newly-discovered stations record this station as their discoverer.
func runMonitorDirection(
	ctx workflow.Context,
	stationSlug, direction string,
	fetchOpts, persistOpts workflow.ActivityOptions,
	fetch *activities.Fetch,
	process *activities.Process,
	resolvedEva string,
) (string, error) {
	fetchCtx := workflow.WithActivityOptions(ctx, fetchOpts)
	var fr shared.FetchStationBoardResult
	if err := workflow.ExecuteActivity(fetchCtx, fetch.FetchStationBoard,
		shared.FetchStationBoardInput{Slug: stationSlug, Direction: direction}).Get(fetchCtx, &fr); err != nil {
		return "", err
	}
	if resolvedEva == "" {
		resolvedEva = fr.ResolvedEva
	}

	persistCtx := workflow.WithActivityOptions(ctx, persistOpts)
	var pr shared.PersistResult
	if err := workflow.ExecuteActivity(persistCtx, process.PersistStopEvent, fr.Events).
		Get(persistCtx, &pr); err != nil {
		return resolvedEva, err
	}

	startDiscoveryChildren(ctx, stationSlug, pr.NewStations, resolvedEva, 0)
	return resolvedEva, nil
}