package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── StaDa API (station master data) ───────────────────────────────────────

// StadaStation is one station from the StaDa /stations response.
// The live API wraps each item in {"Station": {...}}.
type StadaStation struct {
	Number       int64       `json:"number"`
	Name         string      `json:"name"`
	Category     int32       `json:"category"`
	FederalState string      `json:"federalState"`
	EVANumbers   []StadaEVA  `json:"evaNumbers"`
}

// StadaEVA is one EVA number of a station (isMain marks the canonical one).
type StadaEVA struct {
	Number int64            `json:"number"`
	IsMain bool             `json:"isMain"`
	Coords *StadaGeoPoint   `json:"geographicCoordinates"`
}

// StadaGeoPoint is GeoJSON: coordinates are [lon, lat] — ORDER MATTERS.
type StadaGeoPoint struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

// stadaWrapper handles the {"Station": {...}} envelope of the live API.
type stadaWrapper struct {
	Station StadaStation `json:"Station"`
}

// stadaResponse is the /stations envelope.
type stadaResponse struct {
	Total  int64           `json:"total"`
	Result []stadaWrapper  `json:"result"`
}

// stadaBaseURL returns the StaDa base (overridable for tests).
func stadaBaseURL() string {
	if v := os.Getenv("STADA_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://apis.deutschebahn.com/db-api-marketplace/apis/station-data/v2"
}

var stadaHTTP = &http.Client{Timeout: 60 * time.Second}

// FetchStadaStations queries all active stations (category 1-7) in ONE call.
// Auth uses the StaDa key pair (STADA_CLIENT_ID/STADA_API_KEY), falling back
// to the IRIS pair when the StaDa-specific envs are unset (single-subscription setups).
func FetchStadaStations(ctx context.Context) ([]StadaStation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		stadaBaseURL()+"/stations?limit=10000&category=1-7", nil)
	if err != nil {
		return nil, err
	}
	clientID := os.Getenv("STADA_CLIENT_ID")
	if clientID == "" {
		clientID = os.Getenv("IRIS_CLIENT_ID")
	}
	apiKey := os.Getenv("STADA_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("IRIS_API_KEY")
	}
	if clientID != "" {
		req.Header.Set("DB-Client-ID", clientID)
	}
	if apiKey != "" {
		req.Header.Set("DB-Api-Key", apiKey)
	}
	resp, err := stadaHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stada GET stations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("stada GET stations: status %d: %s", resp.StatusCode, string(body))
	}
	var r stadaResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("stada decode: %w", err)
	}
	out := make([]StadaStation, 0, len(r.Result))
	for _, w := range r.Result {
		out = append(out, w.Station)
	}
	return out, nil
}

// MainEVA returns the canonical (isMain) EVA of a station as a string, or "".
func (s StadaStation) MainEVA() string {
	for _, e := range s.EVANumbers {
		if e.IsMain {
			return fmt.Sprintf("%d", e.Number)
		}
	}
	// Fall back to the first EVA if none is flagged main.
	if len(s.EVANumbers) > 0 {
		return fmt.Sprintf("%d", s.EVANumbers[0].Number)
	}
	return ""
}

// LatLon returns (lat, lon) of the main EVA's coordinates (GeoJSON order
// [lon, lat] is swapped here). Returns (0,0,false) when missing.
func (s StadaStation) LatLon() (float64, float64, bool) {
	for _, e := range s.EVANumbers {
		if e.IsMain && e.Coords != nil && len(e.Coords.Coordinates) >= 2 {
			lon := e.Coords.Coordinates[0]
			lat := e.Coords.Coordinates[1]
			return lat, lon, true
		}
	}
	return 0, 0, false
}