package activities

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"verspaetet/shared"
)

// berlin is the timezone all IRIS times are expressed in (no offsets in data).
var berlin = mustBerlin()

func mustBerlin() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic("cannot load Europe/Berlin: " + err.Error())
	}
	return loc
}

// irisTimeLayout is the IRIS timestamp format: YYMMddHHmm (2-digit year).
const irisTimeLayout = "0601021504"

// parseIrisTime parses an IRIS local (Berlin) time string to UTC.
func parseIrisTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	t, err := time.ParseInLocation(irisTimeLayout, s, berlin)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %q: %w", s, err)
	}
	return t.UTC(), nil
}

// ── XML schema (per the Timetables OpenAPI contract) ─────────────────────

// Timetable is the root <timetable> element of plan/fchg/rchg responses.
type Timetable struct {
	XMLName  xml.Name  `xml:"timetable"`
	Eva      string    `xml:"eva,attr"`
	Station  string    `xml:"station,attr"`
	Stops    []IrisStop `xml:"s"`
}

// IrisStop is one <s> timetable-stop element. ar/dp are optional (pointer).
type IrisStop struct {
	Eva string    `xml:"eva,attr"`
	ID  string    `xml:"id,attr"`
	TL  TripLabel `xml:"tl"`
	AR  *IrisEvent `xml:"ar"`
	DP  *IrisEvent `xml:"dp"`
}

// TripLabel is the <tl> child of <s>.
type TripLabel struct {
	C string `xml:"c,attr"` // category (ICE, RE, S...)
	N string `xml:"n,attr"` // train number
	O string `xml:"o,attr"` // owner/EVU
	T string `xml:"t,attr"` // trip type: p passenger, e replacement, z additional, s suburban, h auxiliary, n night
	F string `xml:"f,attr"`
}

// IrisEvent is an <ar>/<dp> element. Planned fields (pt/pp/ppth/ps/pde) come
// from /plan; changed fields (ct/cp/cpth/cs/cde/clt) from /fchg. A change
// payload for an added stop also carries the planned fields.
type IrisEvent struct {
	PT   string `xml:"pt,attr"`   // planned time YYMMddHHmm
	PP   string `xml:"pp,attr"`   // planned platform
	PPTH string `xml:"ppth,attr"` // planned path, pipe-separated names
	PS   string `xml:"ps,attr"`   // planned status: p/a/c
	PDE  string `xml:"pde,attr"` // planned distant endpoint
	CT   string `xml:"ct,attr"`   // changed time
	CP   string `xml:"cp,attr"`   // changed platform
	CPTH string `xml:"cpth,attr"` // changed path
	CS   string `xml:"cs,attr"`   // changed status: p/a/c ("c" = cancelled)
	CDE  string `xml:"cde,attr"`  // changed distant endpoint
	CLT  string `xml:"clt,attr"`  // cancellation time (when the cancellation was recorded)
	L    string `xml:"l,attr"`    // line indicator (e.g. S-Bahn line)
}

// splitLineLabel splits an IRIS line indicator (the ar/dp `l` attribute,
// e.g. "RB34", "ICE577", "S8", "RE60") into (category, number). The
// category is the leading alphabetic prefix; the number is the trailing
// alphanumeric part. Pure digits (rare) yield ("", label).
func splitLineLabel(l string) (string, string) {
	i := 0
	for i < len(l) && !isDigit(l[i]) {
		i++
	}
	if i == 0 || i == len(l) {
		return "", l
	}
	return l[:i], l[i:]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// ── IRIS client ───────────────────────────────────────────────────────────

// Iris holds the IRIS Timetables API client. Base URL and credentials are
// configurable via env so tests can point at a fixture server.
type Iris struct{}

// irisBaseURL returns the Timetables API base (overridable for tests).
func irisBaseURL() string {
	if v := os.Getenv("IRIS_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://apis.deutschebahn.com/db-api-marketplace/apis/timetables/v1"
}

// irisHTTP is the shared client (connection reuse, sane timeout).
var irisHTTP = &http.Client{Timeout: 30 * time.Second}

// irisGet fetches and decodes a <timetable> XML document from one endpoint.
func irisGet(ctx context.Context, path string) (*Timetable, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, irisBaseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	if id := os.Getenv("IRIS_CLIENT_ID"); id != "" {
		req.Header.Set("DB-Client-ID", id)
	}
	if key := os.Getenv("IRIS_API_KEY"); key != "" {
		req.Header.Set("DB-Api-Key", key)
	}
	resp, err := irisHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iris GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("iris GET %s: status %d: %s", path, resp.StatusCode, string(body))
	}
	var tt Timetable
	if err := xml.NewDecoder(resp.Body).Decode(&tt); err != nil {
		return nil, fmt.Errorf("iris GET %s: decode: %w", path, err)
	}
	return &tt, nil
}

// planCache caches /plan slices per (eva, day, hour). Plan data is static
// (generated hours in advance, never contains changes), so re-fetching the
// same slice within its hour is pure waste. Bounded: entries expire when the
// hour passes; a janitor drops stale keys opportunistically.
var planCache sync.Map // map[string]*Timetable

func planCacheKey(eva, dayHour string) string { return eva + ":" + dayHour }

// janitorPlanCache removes entries whose hour has passed. Called
// opportunistically (not on every fetch) to keep the map small.
func janitorPlanCache(nowDayHour string) {
	planCache.Range(func(k, _ interface{}) bool {
		if s, ok := k.(string); ok {
			if p := strings.LastIndex(s, ":"); p >= 0 && s[p+1:] != nowDayHour[:len(nowDayHour)] {
				// cheap prefix check: hour string differs -> stale
				planCache.Delete(k)
			}
		}
		return true
	})
}

// fetchPlanHour returns the cached /plan slice for one (eva, hour) or
// fetches it. Errors fall back to an empty timetable (hour without traffic
// is not fatal).
func fetchPlanHour(ctx context.Context, eva string, h time.Time) (*Timetable, error) {
	dayHour := h.Format("06010215")
	key := planCacheKey(eva, dayHour)
	if v, ok := planCache.Load(key); ok {
		return v.(*Timetable), nil
	}
	tt, err := irisGet(ctx, fmt.Sprintf("/plan/%s/%s/%s", eva, h.Format("060102"), h.Format("15")))
	if err != nil {
		return &Timetable{}, err
	}
	planCache.Store(key, tt)
	if h.Minute()%15 == 0 { // occasional cleanup
		go janitorPlanCache(dayHour)
	}
	return tt, nil
}

// FetchStationBoard fetches one station's COMPLETE board (both directions)
// in a single pass: 1× /fchg (live changes) + 1× /plan for the current hour
// (static baseline, cached per hour). Arrivals and departures are split
// client-side — IRIS returns both in the same documents, so one task per
// station loses NOTHING versus per-direction tasks while halving the requests.
//
// Request budget per station: 1 fchg per cycle + 1 plan per HOUR (cache hit
// on repeated cycles within the same hour).
func (a *Iris) FetchStationBoard(ctx context.Context, eva string) (shared.FetchStationBoardResult, error) {
	if eva == "" {
		return shared.FetchStationBoardResult{}, fmt.Errorf("ErrInvalidInput: Eva is empty")
	}

	scrapedAt := time.Now().UTC()

	// Live + future changes: /fchg is the complete deviation picture.
	fchg, err := irisGet(ctx, "/fchg/"+eva)
	if err != nil {
		return shared.FetchStationBoardResult{}, err
	}

	// Static baseline for the current hour (cached; next-hour trips are
	// captured by the next cycle's plan — plan data never changes).
	now := time.Now().In(berlin)
	plan, _ := fetchPlanHour(ctx, eva, now)

	// Merge: plan provides the baseline (tl/pt/pp/ppth/pde), fchg overlays
	// the changed fields (ct/cp/cpth/cs/cde).
	merged := map[string]*IrisStop{}
	for i := range plan.Stops {
		merged[plan.Stops[i].ID] = &plan.Stops[i]
	}
	for i := range fchg.Stops {
		f := &fchg.Stops[i]
		if p, ok := merged[f.ID]; ok {
			f = mergeStop(p, f)
		}
		merged[f.ID] = f
	}

	// Emit one StopEvent per ar AND per dp (both directions).
	events := make([]shared.StopEvent, 0, len(merged)*2)
	for _, stop := range merged {
		for _, dir := range []struct {
			name  string
			event *IrisEvent
		}{
			{"arrival", stop.AR},
			{"departure", stop.DP},
		} {
			if dir.event == nil {
				continue
			}
			se, ok := irisEventToStopEvent(stop, dir.event, eva, dir.name, scrapedAt)
			if !ok {
				continue // unparsable time — skip malformed entries
			}
			events = append(events, se)
		}
	}
	return shared.FetchStationBoardResult{Events: events, ScrapedAt: scrapedAt}, nil
}

// mergeStop overlays an fchg stop onto its plan twin: changed fields from
// fchg, missing planned fields backfilled from plan. Returns a NEW value
// (neither input is mutated).
func mergeStop(plan, fchg *IrisStop) *IrisStop {
	out := *fchg // changed attrs win
	if out.TL == (TripLabel{}) {
		out.TL = plan.TL
	}
	out.AR = mergeEvent(plan.AR, fchg.AR)
	out.DP = mergeEvent(plan.DP, fchg.DP)
	return &out
}

// mergeEvent overlays a changed event onto its planned twin. nil handling:
//   - both nil → nil; one nil → that one (added events only exist in fchg,
//     hidden events only in plan)
func mergeEvent(plan, fchg *IrisEvent) *IrisEvent {
	if plan == nil {
		return fchg
	}
	if fchg == nil {
		return plan
	}
	out := *fchg // changed attrs win
	if out.PT == "" {
		out.PT = plan.PT
	}
	if out.PP == "" {
		out.PP = plan.PP
	}
	if out.PPTH == "" {
		out.PPTH = plan.PPTH
	}
	if out.PS == "" {
		out.PS = plan.PS
	}
	if out.PDE == "" {
		out.PDE = plan.PDE
	}
	if out.L == "" {
		out.L = plan.L
	}
	return &out
}

// irisEventToStopEvent maps one <ar>/<dp> + its parent <s> to a StopEvent.
func irisEventToStopEvent(stop *IrisStop, ev *IrisEvent, eva, direction string, scrapedAt time.Time) (shared.StopEvent, bool) {
	// Changed time wins (ct); fall back to planned (pt). An event with
	// neither is malformed — caller skips.
	timeStr := ev.CT
	if timeStr == "" {
		timeStr = ev.PT
	}
	plannedStr := ev.PT
	if plannedStr == "" {
		plannedStr = timeStr
	}
	if plannedStr == "" {
		return shared.StopEvent{}, false
	}
	planned, err := parseIrisTime(plannedStr)
	if err != nil {
		return shared.StopEvent{}, false
	}

	se := shared.StopEvent{
		StationEva:   eva,
		Direction:    direction,
		StopID:       stop.ID,
		LineCategory: stop.TL.C,
		TrainNumber:  stop.TL.N,
		Owner:        stop.TL.O,
		TripKind:     stop.TL.T,
		ScrapedAt:    scrapedAt,
	}

	// Fallback for fchg-only stops without <tl>: the per-event line
	// indicator (l, e.g. "RB34", "ICE577", "S8") carries the same info.
	// Parse it as category-prefix + trailing number/digits.
	if se.LineCategory == "" && ev.L != "" {
		se.LineCategory, se.TrainNumber = splitLineLabel(ev.L)
	}

	// Trip date = the YYMMddHHmm part of the stop id
	// ({dailyTripId}-{YYMMddHHmm}-{stopIndex}). Negative dailyTripIds add a
	// leading empty split part — trim it before counting.
	parts := strings.Split(stop.ID, "-")
	if parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) == 3 && len(parts[1]) >= 6 {
		if d, err := time.ParseInLocation("060102", parts[1][:6], berlin); err == nil {
			tripDate := d.UTC()
			se.TripDate = &tripDate
		}
	}

	// Distant endpoint: changed beats planned.
	se.DirectionName = ev.CDE
	if se.DirectionName == "" {
		se.DirectionName = ev.PDE
	}

	// Route path: changed beats planned; pipe-separated names.
	path := ev.CPTH
	if path == "" {
		path = ev.PPTH
	}
	if path != "" {
		se.ViaPath = strings.Split(path, "|")
	}

	// Platforms: planned always; changed (cp) overrides actual.
	se.PlannedPlatform = ev.PP
	se.Platform = ev.PP
	if ev.CP != "" {
		se.Platform = ev.CP
	}

	// Times: planned = pt; actual = ct when present (pointer).
	se.PlannedTime = planned
	if ev.CT != "" {
		if actual, err := parseIrisTime(ev.CT); err == nil {
			se.ActualTime = &actual
		}
	}

	// Cancellation: cs='c' (changed status) or ps='c' (planned status).
	se.Cancelled = ev.CS == "c" || ev.PS == "c"

	return se, true
}

// tripKindHuman is kept for potential UI display mapping.
func tripKindHuman(t string) string {
	switch t {
	case "p":
		return "passenger"
	case "e":
		return "replacement"
	case "z":
		return "additional"
	case "s":
		return "suburban"
	case "h":
		return "auxiliary"
	case "n":
		return "night"
	}
	return ""
}

// _ suppresses unused warnings for helpers reserved for later use.
var _ = tripKindHuman