package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"verspaetet/shared"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fetch holds the fetch activities. The only one is FetchStationBoard.
type Fetch struct{}

// errInvalidInput is returned for empty Slug or bad Direction and is
// non-retryable (Temporal RetryPolicy.NonRetryableErrorTypes includes
// "ErrInvalidInput").
type errInvalidInput struct{ msg string }

func (e *errInvalidInput) Error() string { return "ErrInvalidInput: " + e.msg }

// nextActionHash is the Next.js Server Action ID for the board data fetch.
// It's a build-time hash that may change when bahnhof.de deploys a new build.
// If this breaks, the activity returns an error (retryable) and the workflow
// retries. Update this hash by inspecting the Network tab on a live board page.
const nextActionHash = "7f224b883c4a036854b93606d3611b94aebb8ae93b"

// boardRequest is the JSON body sent to the bahnhof.de Server Action.
type boardRequest struct {
	Duration          int      `json:"duration"`
	Type              string   `json:"type"`
	Locale            string   `json:"locale"`
	EvaNumbers        []string `json:"evaNumbers"`
	AdditionalEva     []string `json:"additionalEvaNumbers"`
	ExcludeEva        []string `json:"excludeEvaNumbers"`
	StationCategory   int      `json:"stationCategory"`
	FilterTransports  []string `json:"filterTransports"`
	SortBy            string   `json:"sortBy"`
}

// boardResponse is the parsed row "1:" from the RSC stream.
type boardResponse struct {
	GlobalMessages json.RawMessage `json:"globalMessages"`
	Entries        [][]boardEntry  `json:"entries"`
}

// boardEntry is one train on the board.
type boardEntry struct {
	ID             string      `json:"id"`
	JourneyID      string      `json:"journeyID"`
	LineName       string      `json:"lineName"`
	Direction      string      `json:"direction"`
	TimeSchedule   string      `json:"timeSchedule"`
	TimeDelayed    string      `json:"timeDelayed"`
	Delayed        bool        `json:"delayed"`
	Platform       string      `json:"platform"`
	PlatformSched  string      `json:"platformSchedule"`
	Canceled       bool        `json:"canceled"`
	Type           string      `json:"type"`
	Kind           string      `json:"kind"`
	ReplacementSvc string      `json:"replacementServiceType"`
	StopPlace      stationRef  `json:"stopPlace"`
	Destination    stationRef  `json:"destination"`
	Origin         *stationRef `json:"origin"`
	ViaStops       []stationRef `json:"viaStops"`
	Messages       struct {
		Common []struct {
			Text string `json:"text"`
		} `json:"common"`
		Delay []struct {
			Text string `json:"text"`
		} `json:"delay"`
	} `json:"messages"`
}

// stationRef is a reference to a station in the board data.
type stationRef struct {
	EvaNumber string `json:"evaNumber"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
}

// FetchStationBoard fetches a station's departure or arrival board via the
// bahnhof.de Next.js Server Action (direct HTTP POST, no browser needed).
// It returns parsed StopEvents ready for persistence.
func (a *Fetch) FetchStationBoard(ctx context.Context, in shared.FetchStationBoardInput) (shared.FetchStationBoardResult, error) {
	if in.Slug == "" {
		return shared.FetchStationBoardResult{}, &errInvalidInput{"Slug is empty"}
	}
	if in.Direction != "departure" && in.Direction != "arrival" {
		return shared.FetchStationBoardResult{}, &errInvalidInput{fmt.Sprintf("Direction %q must be departure or arrival", in.Direction)}
	}

	dirPath := "abfahrt"
	boardType := "departures"
	if in.Direction == "arrival" {
		dirPath = "ankunft"
		boardType = "arrivals"
	}
	url := fmt.Sprintf("https://www.bahnhof.de/%s/%s", in.Slug, dirPath)
	scrapedAt := time.Now().UTC()

	// We need the station's EVA to query the API. Try the DB first (via env),
	// then fall back to fetching the hub page's meta tag.
	eva, name, err := resolveStationEva(ctx, in.Slug)
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("resolving EVA for slug %q: %w", in.Slug, err)
	}
	if name == "" {
		name = in.Slug
	}

	// POST to the Server Action.
	body, err := json.Marshal([]boardRequest{{
		Duration:         60,
		Type:             boardType,
		Locale:           "de",
		EvaNumbers:       []string{eva},
		AdditionalEva:    []string{},
		ExcludeEva:       []string{},
		StationCategory:  1,
		FilterTransports: []string{"HIGH_SPEED_TRAIN", "INTERCITY_TRAIN", "INTER_REGIONAL_TRAIN", "REGIONAL_TRAIN", "CITY_TRAIN"},
		SortBy:           "TIME_SCHEDULE",
	}})
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Next-Action", nextActionHash)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("calling bahnhof.de: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return shared.FetchStationBoardResult{}, fmt.Errorf("bahnhof.de returned %d: %s", resp.StatusCode, string(respBody)[:min(200, len(respBody))])
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("reading response: %w", err)
	}

	// Parse the RSC stream: find the line starting with "1:" and parse the JSON.
	entries, err := parseRSCResponse(respBody)
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("parsing RSC response: %w", err)
	}

	// Map entries to StopEvents.
	events := make([]shared.StopEvent, 0, len(entries))
	for _, entry := range entries {
		ev := mapEntryToStopEvent(entry, in.Slug, eva, name, in.Direction, scrapedAt)
		if ev.LineLabel == "" {
			continue
		}
		events = append(events, ev)
	}

	return shared.FetchStationBoardResult{
		Events:       events,
		ScrapedAt:    scrapedAt,
		URL:          url,
		ResolvedEva:  eva,
		ResolvedName: name,
	}, nil
}

// resolveStationEva resolves a station slug to its EVA number and display name.
// It first tries the DB (if POSTGRES_DSN is set), then falls back to fetching
// the station's hub page and parsing the <meta name="bf:evaNumbers"> tag.
func resolveStationEva(ctx context.Context, slug string) (eva, name string, err error) {
	// Try the DB first.
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn != "" {
		eva, name, err = lookupStationInDB(ctx, slug, dsn)
		if err == nil {
			return eva, name, nil
		}
	}

	// Fall back to fetching the hub page meta tags.
	return lookupStationViaHubPage(ctx, slug)
}

// lookupStationInDB queries the stations table for the EVA and name.
func lookupStationInDB(ctx context.Context, slug, dsn string) (eva, name string, err error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return "", "", err
	}
	defer pool.Close()

	err = pool.QueryRow(ctx,
		"SELECT eva, name FROM stations WHERE slug = $1", slug,
	).Scan(&eva, &name)
	return eva, name, err
}

// lookupStationViaHubPage fetches the station's hub page (GET, no browser)
// and extracts the EVA from <meta name="bf:evaNumbers"> and the name from
// <meta name="bf:..."> or the page title.
func lookupStationViaHubPage(ctx context.Context, slug string) (eva, name string, err error) {
	url := fmt.Sprintf("https://www.bahnhof.de/%s", slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("hub page returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	html := string(body)

	// Extract EVA from <meta name="bf:evaNumbers" content="...">.
	if idx := strings.Index(html, `name="bf:evaNumbers"`); idx >= 0 {
		contentStart := strings.Index(html[idx:], `content="`)
		if contentStart >= 0 {
			contentStart += len(`content="`)
			contentEnd := strings.Index(html[idx+contentStart:], `"`)
			if contentEnd >= 0 {
				eva = html[idx+contentStart : idx+contentStart+contentEnd]
				eva = strings.Split(eva, ",")[0] // take first if comma-separated
			}
		}
	}

	// Extract name from <title> tag.
	if titleStart := strings.Index(html, "<title>"); titleStart >= 0 {
		titleStart += len("<title>")
		titleEnd := strings.Index(html[titleStart:], "</title>")
		if titleEnd >= 0 {
			title := html[titleStart : titleStart+titleEnd]
			if idx := strings.LastIndex(title, "–"); idx >= 0 {
				name = strings.TrimSpace(title[idx+len("–"):])
			} else {
				name = strings.TrimSpace(title)
			}
		}
	}

	if eva == "" {
		return "", "", fmt.Errorf("no EVA found on hub page for slug %q", slug)
	}
	return eva, name, nil
}

// parseRSCResponse parses the Next.js RSC stream. It finds the line starting
// with "1:" and unmarshals the JSON after the prefix.
func parseRSCResponse(body []byte) ([]boardEntry, error) {
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "1:") {
			var resp boardResponse
			if err := json.Unmarshal([]byte(line[2:]), &resp); err != nil {
				return nil, fmt.Errorf("unmarshaling row 1: %w", err)
			}
			// entries is [][]boardEntry — flatten the outer slice.
			var all []boardEntry
			for _, group := range resp.Entries {
				all = append(all, group...)
			}
			return all, nil
		}
	}
	return nil, fmt.Errorf("no row starting with '1:' in RSC response")
}

// mapEntryToStopEvent maps a JSON board entry to a StopEvent.
func mapEntryToStopEvent(e boardEntry, stationSlug, stationEva, stationName, direction string, scrapedAt time.Time) shared.StopEvent {
	ev := shared.StopEvent{
		StationSlug:  stationSlug,
		StationEva:   stationEva,
		StationName:  stationName,
		Direction:    direction,
		ScrapedAt:    scrapedAt,
		ViaSlugs:     []string{},
		ViaEvas:      []string{},
		LineLabel:     cleanLineName(e.LineName),
		LineCategory: categorizeByType(e.Type, e.Kind, e.ReplacementSvc),
		Cancelled:    e.Canceled,
	}

	// Times.
	if t, err := time.Parse(time.RFC3339, e.TimeSchedule); err == nil {
		ev.PlannedTime = t.UTC()
	}
	if e.TimeDelayed != "" {
		if t, err := time.Parse(time.RFC3339, e.TimeDelayed); err == nil {
			utc := t.UTC()
			ev.ActualTime = &utc
		}
	}

	// Platform.
	ev.Platform = e.Platform
	ev.PlannedPlatform = e.PlatformSched

	// Direction (destination for departures, origin for arrivals).
	if direction == "departure" {
		ev.DirectionName = e.Destination.Name
		ev.DirectionSlug = e.Destination.Slug
	} else {
		if e.Origin != nil {
			ev.DirectionName = e.Origin.Name
			ev.DirectionSlug = e.Origin.Slug
		}
	}

	// Via stops.
	for _, vs := range e.ViaStops {
		if vs.Slug != "" {
			ev.ViaSlugs = append(ev.ViaSlugs, vs.Slug)
		}
		if vs.EvaNumber != "" {
			ev.ViaEvas = append(ev.ViaEvas, vs.EvaNumber)
		}
	}

	// Trip ID and UUID.
	ev.TripID = e.ID
	// journeyID is "YYYYMMDD-<uuid>" — split into date and UUID.
	if len(e.JourneyID) >= 36 {
		ev.TripUUID = e.JourneyID[9:] // skip "YYYYMMDD-"
		if td, err := time.Parse("20060102", e.JourneyID[:8]); err == nil {
			ev.TripDate = td
		}
	}

	// Notes — join all message texts.
	var notes []string
	for _, m := range e.Messages.Common {
		if m.Text != "" {
			notes = append(notes, m.Text)
		}
	}
	for _, m := range e.Messages.Delay {
		if m.Text != "" {
			notes = append(notes, m.Text)
		}
	}
	if len(notes) > 0 {
		ev.Notes = strings.Join(notes, " \u23ce ")
		if len(ev.Notes) > 2000 {
			ev.Notes = ev.Notes[:2000]
		}
	}

	return ev
}

// cleanLineName removes the narrow-space character (U+202F) that bahnhof.de
// inserts between letter and number in line names (e.g. "S\u202f8" → "S8").
func cleanLineName(s string) string {
	s = strings.ReplaceAll(s, "\u202f", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	return strings.Join(strings.Fields(s), "")
}

// categorizeByType maps the JSON transport type to our line category.
func categorizeByType(transportType, kind, replacementSvc string) string {
	if kind == "replacement-service" || replacementSvc == "BUS" {
		return "ersatz"
	}
	switch transportType {
	case "HIGH_SPEED_TRAIN":
		return "fern"
	case "INTERCITY_TRAIN":
		return "fern"
	case "INTER_REGIONAL_TRAIN":
		return "fern"
	case "REGIONAL_TRAIN":
		return "regio"
	case "CITY_TRAIN":
		return "s_bahn"
	default:
		return "unknown"
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}