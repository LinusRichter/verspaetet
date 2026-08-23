package activities

import (
	"context"
	"testing"
	"time"

	"verspaetet/shared"
)

func TestCleanLineName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"S\u202f8", "S8"},
		{"S\u00a03", "S3"},
		{"ICE", "ICE"},
		{"RE 50", "RE50"},
		{"RB 3", "RB3"},
	}
	for _, tt := range tests {
		got := cleanLineName(tt.input)
		if got != tt.want {
			t.Errorf("cleanLineName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCategorizeByType(t *testing.T) {
	tests := []struct {
		transportType string
		kind          string
		replacement   string
		want          string
	}{
		{"HIGH_SPEED_TRAIN", "default", "", "fern"},
		{"INTERCITY_TRAIN", "default", "", "fern"},
		{"INTER_REGIONAL_TRAIN", "default", "", "fern"},
		{"REGIONAL_TRAIN", "default", "", "regio"},
		{"CITY_TRAIN", "default", "", "s_bahn"},
		{"CITY_TRAIN", "replacement-service", "BUS", "ersatz"},
		{"REGIONAL_TRAIN", "replacement-service", "BUS", "ersatz"},
		{"UNKNOWN_TYPE", "default", "", "unknown"},
	}
	for _, tt := range tests {
		got := categorizeByType(tt.transportType, tt.kind, tt.replacement)
		if got != tt.want {
			t.Errorf("categorizeByType(%q, %q, %q) = %q, want %q",
				tt.transportType, tt.kind, tt.replacement, got, tt.want)
		}
	}
}

func TestParseRSCResponse(t *testing.T) {
	// Simulate a minimal RSC stream
	rsc := []byte("0:{\"a\":\"$@1\"}\n1:{\"globalMessages\":[],\"entries\":[[{\"lineName\":\"ICE\",\"timeSchedule\":\"2026-08-23T12:00:00+02:00\",\"timeDelayed\":\"2026-08-23T12:05:00+02:00\",\"delayed\":true,\"platform\":\"3\",\"platformSchedule\":\"3\",\"canceled\":false,\"id\":\"8000105_D_1\",\"journeyID\":\"20260823-62f5a1c3-6d5f-3a04-aa1b-d668f6fa5e81\",\"type\":\"HIGH_SPEED_TRAIN\",\"kind\":\"default\",\"replacementServiceType\":\"\",\"destination\":{\"evaNumber\":\"8000156\",\"name\":\"M\\u00fcnchen Hbf\",\"slug\":\"muenchen-hbf\"},\"viaStops\":[{\"evaNumber\":\"8000255\",\"name\":\"Augsburg Hbf\",\"slug\":\"augsburg-hbf\"}],\"messages\":{\"common\":[],\"delay\":[]}}]]}")
	entries, err := parseRSCResponse(rsc)
	if err != nil {
		t.Fatalf("parseRSCResponse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].LineName != "ICE" {
		t.Errorf("LineName = %q, want ICE", entries[0].LineName)
	}
}

func TestMapEntryToStopEvent(t *testing.T) {
	entry := boardEntry{
		LineName:      "S\u202f8",
		TimeSchedule:  "2026-08-23T12:44:00+02:00",
		TimeDelayed:   "2026-08-23T13:08:00+02:00",
		Delayed:       true,
		Platform:      "102",
		PlatformSched: "102",
		Canceled:      false,
		ID:            "8098105_D_1",
		JourneyID:     "20260823-62f5a1c3-6d5f-3a04-aa1b-d668f6fa5e81",
		Type:          "CITY_TRAIN",
		Kind:          "default",
		Destination: stationRef{
			EvaNumber: "8004645",
			Name:      "Offenbach(Main)Ost",
			Slug:      "offenbach-main-ost",
		},
		ViaStops: []stationRef{
			{EvaNumber: "8006691", Name: "Frankfurt(M)Taunusanlage", Slug: "frankfurt-main-taunusanlage"},
		},
	}

	scrapedAt := time.Now().UTC()
	ev := mapEntryToStopEvent(entry, "frankfurt-main-hbf", "8000105", "Frankfurt (Main) Hbf", "departure", scrapedAt)

	if ev.LineLabel != "S8" {
		t.Errorf("LineLabel = %q, want S8", ev.LineLabel)
	}
	if ev.LineCategory != "s_bahn" {
		t.Errorf("LineCategory = %q, want s_bahn", ev.LineCategory)
	}
	if ev.DirectionName != "Offenbach(Main)Ost" {
		t.Errorf("DirectionName = %q, want Offenbach(Main)Ost", ev.DirectionName)
	}
	if ev.DirectionSlug != "offenbach-main-ost" {
		t.Errorf("DirectionSlug = %q, want offenbach-main-ost", ev.DirectionSlug)
	}
	if ev.Platform != "102" {
		t.Errorf("Platform = %q, want 102", ev.Platform)
	}
	if ev.PlannedPlatform != "102" {
		t.Errorf("PlannedPlatform = %q, want 102", ev.PlannedPlatform)
	}
	if ev.TripID != "8098105_D_1" {
		t.Errorf("TripID = %q, want 8098105_D_1", ev.TripID)
	}
	if ev.TripUUID != "62f5a1c3-6d5f-3a04-aa1b-d668f6fa5e81" {
		t.Errorf("TripUUID = %q, want 62f5a1c3-6d5f-3a04-aa1b-d668f6fa5e81", ev.TripUUID)
	}
	if !ev.PlannedTime.Equal(time.Date(2026, 8, 23, 10, 44, 0, 0, time.UTC)) {
		t.Errorf("PlannedTime = %v, want 2026-08-23T10:44:00Z", ev.PlannedTime)
	}
	if ev.ActualTime == nil {
		t.Fatal("ActualTime is nil")
	}
	if !ev.ActualTime.Equal(time.Date(2026, 8, 23, 11, 8, 0, 0, time.UTC)) {
		t.Errorf("ActualTime = %v, want 2026-08-23T11:08:00Z", *ev.ActualTime)
	}
	if len(ev.ViaSlugs) != 1 || ev.ViaSlugs[0] != "frankfurt-main-taunusanlage" {
		t.Errorf("ViaSlugs = %v, want [frankfurt-main-taunusanlage]", ev.ViaSlugs)
	}
	if len(ev.ViaEvas) != 1 || ev.ViaEvas[0] != "8006691" {
		t.Errorf("ViaEvas = %v, want [8006691]", ev.ViaEvas)
	}
	if ev.Cancelled {
		t.Error("Cancelled = true, want false")
	}
	if ev.StationSlug != "frankfurt-main-hbf" {
		t.Errorf("StationSlug = %q, want frankfurt-main-hbf", ev.StationSlug)
	}
	if ev.StationEva != "8000105" {
		t.Errorf("StationEva = %q, want 8000105", ev.StationEva)
	}
	if ev.StationName != "Frankfurt (Main) Hbf" {
		t.Errorf("StationName = %q, want Frankfurt (Main) Hbf", ev.StationName)
	}
}

func TestMapEntryToStopEvent_Arrival(t *testing.T) {
	entry := boardEntry{
		LineName:     "ICE",
		TimeSchedule: "2026-08-23T14:00:00+02:00",
		Canceled:     false,
		Type:         "HIGH_SPEED_TRAIN",
		Kind:         "default",
		Origin: &stationRef{
			EvaNumber: "8000255",
			Name:      "Munich Hbf",
			Slug:      "muenchen-hbf",
		},
		Destination: stationRef{
			EvaNumber: "8000105",
			Name:      "Frankfurt Hbf",
			Slug:      "frankfurt-main-hbf",
		},
	}

	ev := mapEntryToStopEvent(entry, "frankfurt-main-hbf", "8000105", "Frankfurt Hbf", "arrival", time.Now().UTC())

	if ev.DirectionName != "Munich Hbf" {
		t.Errorf("DirectionName = %q, want Munich Hbf (origin for arrivals)", ev.DirectionName)
	}
	if ev.DirectionSlug != "muenchen-hbf" {
		t.Errorf("DirectionSlug = %q, want muenchen-hbf", ev.DirectionSlug)
	}
}

func TestMapEntryToStopEvent_Ersatzbus(t *testing.T) {
	entry := boardEntry{
		LineName:       "RE50",
		Type:           "REGIONAL_TRAIN",
		Kind:           "replacement-service",
		ReplacementSvc: "BUS",
		Platform:       "",
		Destination: stationRef{
			EvaNumber: "8000300",
			Name:      "Passau Hbf",
			Slug:      "passau-hbf",
		},
	}

	ev := mapEntryToStopEvent(entry, "plattling", "8000301", "Plattling", "departure", time.Now().UTC())

	if ev.LineCategory != "ersatz" {
		t.Errorf("LineCategory = %q, want ersatz", ev.LineCategory)
	}
	if ev.Platform != "" {
		t.Errorf("Platform = %q, want empty for Ersatzbus", ev.Platform)
	}
}

func TestMapEntryToStopEvent_Messages(t *testing.T) {
	entry := boardEntry{
		LineName: "ICE 577",
		Type:     "HIGH_SPEED_TRAIN",
		Kind:     "default",
		Destination: stationRef{
			EvaNumber: "8000291",
			Name:      "Stuttgart Hbf",
			Slug:      "stuttgart-hbf",
		},
		Messages: struct {
			Common []struct {
				Text string `json:"text"`
			} `json:"common"`
			Delay []struct {
				Text string `json:"text"`
			} `json:"delay"`
		}{
			Common: []struct {
				Text string `json:"text"`
			}{
				{Text: "Fahrradmitnahme reservierungspflichtig"},
			},
			Delay: []struct {
				Text string `json:"text"`
			}{
				{Text: "Verspätung aus vorheriger Fahrt"},
			},
		},
	}

	ev := mapEntryToStopEvent(entry, "frankfurt-main-hbf", "8000105", "Frankfurt Hbf", "departure", time.Now().UTC())

	if ev.Notes == "" {
		t.Fatal("Notes is empty")
	}
	if !contains(ev.Notes, "Fahrradmitnahme reservierungspflichtig") {
		t.Errorf("Notes missing common message: %q", ev.Notes)
	}
	if !contains(ev.Notes, "Verspätung aus vorheriger Fahrt") {
		t.Errorf("Notes missing delay message: %q", ev.Notes)
	}
}

func TestMapReason(t *testing.T) {
	tests := []struct {
		notes string
		want  string
	}{
		{"Notarzteinsatz auf der Strecke", "MEDICAL_EMERGENCY"},
		{"Polizeieinsatz", "POLICE_ACTIVITY"},
		{"Streik der GDL", "STRIKE"},
		{"Technische Störung am Zug", "TECHNICAL_PROBLEM_VEHICLE"},
		{"Technische Störung", "TECHNICAL_PROBLEM_OTHER"},
		{"Hitzebedingte Einschränkungen", "WEATHER_HEAT"},
		{"Unwetterwarnung", "WEATHER_STORM"},
		{"Winterwitterung", "WEATHER_WINTER"},
		{"Unbekannte Meldung", ""},
	}
	for _, tt := range tests {
		got := mapReason(tt.notes)
		if got != tt.want {
			t.Errorf("mapReason(%q) = %q, want %q", tt.notes, got, tt.want)
		}
	}
}

func TestIsCancelled(t *testing.T) {
	tests := []struct {
		notes string
		want  bool
	}{
		{"Zug fällt heute aus", true},
		{"Zug entfällt", true},
		{"Zugausfall zwischen München und Augsburg", true},
		{"Verspätung aus vorheriger Fahrt", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isCancelled(tt.notes)
		if got != tt.want {
			t.Errorf("isCancelled(%q) = %v, want %v", tt.notes, got, tt.want)
		}
	}
}

// Verify that an empty board (entries: []) is handled gracefully.
func TestParseRSCResponse_Empty(t *testing.T) {
	rsc := []byte("0:{\"a\":\"$@1\"}\n1:{\"globalMessages\":[],\"entries\":[]}")
	entries, err := parseRSCResponse(rsc)
	if err != nil {
		t.Fatalf("parseRSCResponse: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// Verify that a missing "1:" row returns an error.
func TestParseRSCResponse_NoDataRow(t *testing.T) {
	rsc := []byte("0:{\"a\":\"$@1\"}")
	_, err := parseRSCResponse(rsc)
	if err == nil {
		t.Fatal("expected error for missing data row")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Ensure the Fetch struct compiles (no methods needed for these tests).
var _ shared.FetchStationBoardInput = shared.FetchStationBoardInput{}
var _ shared.FetchStationBoardResult = shared.FetchStationBoardResult{}
var _ shared.StopEvent = shared.StopEvent{}
var _ context.Context = context.Background()