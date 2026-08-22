package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"verspaetet/shared"

	"github.com/PuerkitoBio/goquery"
)

// Fetch holds the fetch activities. The only one is FetchStationBoard.
// See docs/architecture/activity-fetch-station-board.md.
type Fetch struct{}

// errInvalidInput is returned for empty Slug or bad Direction and is
// non-retryable (Temporal RetryPolicy.NonRetryableErrorTypes includes
// "ErrInvalidInput").
type errInvalidInput struct{ msg string }

func (e *errInvalidInput) Error() string { return "ErrInvalidInput: " + e.msg }

// tripIDPrefixRe matches the leading numeric EVA prefix of a trip_id, e.g.
// "8000105_D_1" → "8000105". Used to resolve the station's EVA from the
// rendered board without a full DOM parse.
var tripIDPrefixRe = regexp.MustCompile(`/(?:fahrtverlauf|wagenreihung)/\d{8}-[0-9a-f-]{36}/(\d+)_`)

// FetchStationBoard renders one station board via browserless and resolves the
// station's EVA from the rendered HTML's trip_id prefix.
// See docs/architecture/activity-fetch-station-board.md.
func (a *Fetch) FetchStationBoard(ctx context.Context, in shared.FetchStationBoardInput) (shared.FetchStationBoardResult, error) {
	if in.Slug == "" {
		return shared.FetchStationBoardResult{}, &errInvalidInput{"Slug is empty"}
	}
	if in.Direction != "departure" && in.Direction != "arrival" {
		return shared.FetchStationBoardResult{}, &errInvalidInput{fmt.Sprintf("Direction %q must be departure or arrival", in.Direction)}
	}

	dirPath := "abfahrt"
	if in.Direction == "arrival" {
		dirPath = "ankunft"
	}
	url := fmt.Sprintf("https://www.bahnhof.de/%s/%s", in.Slug, dirPath)

	html, err := renderViaBrowserless(ctx, url)
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("rendering %s: %w", url, err)
	}

	// Acceptance check: at least one <h2> inside <main>. The pre-hydration shell
	// has zero <h2> in <main>; "Gleis"/"verspaetet" strings are present in the shell
	// too, so they are NOT sufficient. See docs/sources/render-contract.md.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return shared.FetchStationBoardResult{}, fmt.Errorf("parsing rendered HTML: %w", err)
	}
	h2Count := doc.Find("main h2").Length()
	if h2Count == 0 {
		return shared.FetchStationBoardResult{}, fmt.Errorf("acceptance check failed: no <h2> in <main> (got %d bytes of HTML that is likely the pre-hydration shell)", len(html))
	}

	// Resolve the EVA from the first Fahrtinformationen link's trip_id prefix.
	resolvedEva := ""
	if m := tripIDPrefixRe.FindStringSubmatch(html); m != nil {
		resolvedEva = m[1]
	}

	// Resolve the station display name from the page <title>. The board pages
	// use the form "Abfahrt – Essen Hbf" / "Ankunft – Essen Hbf" (the separator
	// is an en dash, U+2013); the hub pages use just "Essen Hbf". Take the part
	// after the last "– " (falling back to the whole title) and trim it.
	resolvedName := ""
	if title := strings.TrimSpace(doc.Find("title").Text()); title != "" {
		if idx := strings.LastIndex(title, "–"); idx >= 0 {
			resolvedName = strings.TrimSpace(title[idx+len("–"):])
		}
		if resolvedName == "" {
			resolvedName = title
		}
	}

	return shared.FetchStationBoardResult{
		HTML:         html,
		ScrapedAt:    time.Now().UTC(),
		URL:          url,
		ResolvedEva:  resolvedEva,
		ResolvedName: resolvedName,
	}, nil
}

// renderViaBrowserless calls the browserless /content endpoint with a
// waitForSelector so the SPA is fully hydrated before HTML is returned.
// See docs/sources/render-contract.md.
func renderViaBrowserless(ctx context.Context, url string) (string, error) {
	host := os.Getenv("BROWSERLESS_HOST")
	if host == "" {
		host = "browserless:3000"
	}

	payload := map[string]interface{}{
		"url":         url,
		"gotoOptions": map[string]interface{}{"waitUntil": "domcontentloaded"},
		"waitFor":     map[string]interface{}{"selector": "main h2", "timeout": 25000},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling browserless payload: %w", err)
	}

	apiURL := fmt.Sprintf("http://%s/content", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating browserless request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling browserless: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("browserless returned %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading browserless response: %w", err)
	}
	return string(respBody), nil
}