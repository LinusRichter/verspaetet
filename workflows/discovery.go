package workflows

import (
	"time"

	"verspaetet/activities"
	"verspaetet/shared"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// maxDiscoveryDepth caps how deep the discovery graph may grow. Seeds are
// depth 0, their via-stations are depth 1, and so on. When a workflow's depth
// reaches maxDiscoveryDepth it still scrapes and persists its own boards but
// does NOT spawn child StationDiscovery workflows. This bounds the fan-out
// that otherwise grew 23 seeds to 1245+ stations and saturated browserless
// (429 Too Many Requests). See docs/open-questions.md (Discovery fan-out
// control) and docs/architecture/workflow-station-discovery.md.
//
// 3 was too shallow: a real chain observed on the live site
// (dortmund-hbf → eschweiler-hbf → siegen-hbf → giessen) put Gießen on the
// boundary, so its via-stations (kirchhain-bz-kassel, stadtallendorf, treysa,
// marburg-lahn) were never discovered even though they were visible on
// Gießen's departure board. Germany has ~5400 stations and the graph is at
// most ~15 hops deep; the worker concurrency caps
// (MaxConcurrentActivityExecutionSize: 5) already bound the fan-out, so the
// depth cap is a safety net, not the primary throttle. 8 gives good coverage
// while still bounding runaway growth.
const maxDiscoveryDepth = 8

// StationDiscovery is the v1 discovery workflow. It fetches one station's
// departure and arrival boards, persists the observed StopEvents, and starts
// child StationDiscovery workflows for any via/direction slugs not yet in the
// stations table.
//
// parentEva is the EVA of the station that discovered this slug (empty for
// seeds); depth is this workflow's depth in the discovery graph (seeds are
// 0, children are parentDepth+1). Children are only spawned when
// depth < maxDiscoveryDepth.
//
// Runs on discovery-queue. PersistStopEvent is scheduled onto monitor-queue
// (cross-queue activity call) because the fetch-worker has no Postgres pool.
func StationDiscovery(ctx workflow.Context, stationSlug string, parentEva string, depth int) error {
	ctx = workflow.WithWorkflowRunTimeout(ctx, 10*time.Minute)

	fetchOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        5 * time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:       2 * time.Minute,
			MaximumAttempts:        4,
			NonRetryableErrorTypes: []string{"ErrInvalidInput"},
		},
	}
	persistOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Second,
		TaskQueue:           shared.MonitorQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        200 * time.Millisecond,
			BackoffCoefficient:     2.0,
			MaximumAttempts:         5,
			NonRetryableErrorTypes: []string{"ErrUnresolvedStation"},
		},
	}

	var (
		fetch    activities.Fetch
		process  activities.Process
		depErr   error
		arrErr   error
		resolved string
	)

	resolved, depErr = runDiscoveryDirection(ctx, stationSlug, "departure", fetchOpts, persistOpts, &fetch, &process, "", parentEva, depth)
	_, arrErr = runDiscoveryDirection(ctx, stationSlug, "arrival", fetchOpts, persistOpts, &fetch, &process, resolved, parentEva, depth)

	if depErr != nil && arrErr != nil {
		workflow.GetLogger(ctx).Error("StationDiscovery: both directions failed",
			"slug", stationSlug, "departure_err", depErr, "arrival_err", arrErr)
		return depErr
	}
	if depErr != nil {
		workflow.GetLogger(ctx).Warn("StationDiscovery: departure failed, arrival persisted",
			"slug", stationSlug, "err", depErr)
	}
	if arrErr != nil {
		workflow.GetLogger(ctx).Warn("StationDiscovery: arrival failed, departure persisted",
			"slug", stationSlug, "err", arrErr)
	}
	return nil
}

// runDiscoveryDirection runs the fetch→parse→persist→discover cycle for one
// direction. resolvedEva, when non-empty, is reused from the other direction's
// fetch; when empty the fetch's own ResolvedEva is used. parentEva is this
// workflow's parent (empty for seeds) and is stamped onto every StopEvent so
// PersistStopEvent can populate stations.discovered_from. depth is this
// workflow's depth; children are spawned at depth+1 with this station's
// resolvedEva as their parentEva, and only when depth < maxDiscoveryDepth.
// Returns the resolved EVA (for reuse by the other direction) and any error
// from the cycle.
func runDiscoveryDirection(
	ctx workflow.Context,
	stationSlug, direction string,
	fetchOpts, persistOpts workflow.ActivityOptions,
	fetch *activities.Fetch,
	process *activities.Process,
	resolvedEva string,
	parentEva string,
	depth int,
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

	// Stamp ParentEva on events (the fetch activity doesn't know it).
	for i := range fr.Events {
		fr.Events[i].ParentEva = parentEva
	}

	persistCtx := workflow.WithActivityOptions(ctx, persistOpts)
	var pr shared.PersistResult
	if err := workflow.ExecuteActivity(persistCtx, process.PersistStopEvent, fr.Events).
		Get(persistCtx, &pr); err != nil {
		return resolvedEva, err
	}

	startDiscoveryChildren(ctx, stationSlug, pr.NewStations, resolvedEva, depth)
	return resolvedEva, nil
}

// startDiscoveryChildren starts a child StationDiscovery workflow (id
// station-discovery-slug-<slug>) for each slug not yet discovered. Dedup is
// by workflow id; WorkflowExecutionAlreadyStarted is treated as success.
// Children are spawned at depth+1 only when depth < maxDiscoveryDepth, which
// bounds the discovery graph fan-out. parentEva is the discovering station's
// own resolved EVA (empty for seeds with an unresolved EVA) and is passed to
// each child so it can stamp it on its StopEvents and populate
// stations.discovered_from for the slugs it discovers in turn.
// See docs/architecture/activity-discover-stations.md.
func startDiscoveryChildren(ctx workflow.Context, fromSlug string, slugs []string, parentEva string, depth int) {
	logger := workflow.GetLogger(ctx)
	if depth >= maxDiscoveryDepth {
		if len(slugs) > 0 {
			logger.Warn("StationDiscovery: depth cap reached, not spawning children",
				"parent_slug", fromSlug, "depth", depth,
				"max_depth", maxDiscoveryDepth, "child_count", len(slugs))
		}
		return
	}
	childDepth := depth + 1
	for _, slug := range slugs {
		if slug == "" || slug == fromSlug {
			continue
		}
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID:       "station-discovery-slug-" + slug,
			ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
			TaskQueue:         shared.DiscoveryQueue,
		})
		childFuture := workflow.ExecuteChildWorkflow(childCtx, StationDiscovery, slug, parentEva, childDepth)
		var exec workflow.Execution
		if err := childFuture.GetChildWorkflowExecution().Get(ctx, &exec); err != nil {
			if isAlreadyStartedErr(err) {
				continue
			}
			logger.Warn("StationDiscovery: failed to start child",
				"parent_slug", fromSlug, "child_slug", slug, "err", err)
			continue
		}

		// Also start a StationMonitor for this station (recurring 30-min loop).
		// Dedup by workflow id: if already running, the start is a no-op.
		monitorCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID:       "station-monitor-slug-" + slug,
			ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
			TaskQueue:         shared.MonitorQueue,
		})
		workflow.ExecuteChildWorkflow(monitorCtx, StationMonitor, slug, true)
	}
}

// isAlreadyStartedErr reports whether err is (or wraps) a
// ChildWorkflowExecutionAlreadyStartedError, which the SDK raises when the
// child workflow id is already running or completed. Treated as success.
func isAlreadyStartedErr(err error) bool {
	return temporal.IsWorkflowExecutionAlreadyStartedError(err)
}