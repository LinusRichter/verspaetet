package activities

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"verspaetet/shared"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func parseFixture(t *testing.T, name, direction string) []shared.StopEvent {
	t.Helper()
	p := &Process{}
	events, err := p.ParseBoard(context.Background(), shared.ParseBoardInput{
		HTML:        loadFixture(t, name),
		Direction:   direction,
		StationSlug: "frankfurt-main-hbf",
		StationEva:  "8000105",
		ScrapedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ParseBoard: %v", err)
	}
	return events
}

func TestParseBoard_Normal(t *testing.T) {
	events := parseFixture(t, "abfahrt_normal.html", "departure")
	if len(events) < 30 {
		t.Fatalf("expected >= 30 rows, got %d", len(events))
	}
	for i, ev := range events {
		if ev.LineLabel == "" {
			t.Errorf("row %d: empty LineLabel", i)
		}
		if ev.Direction == "" {
			t.Errorf("row %d: empty Direction", i)
		}
		if ev.TripID == "" {
			t.Errorf("row %d: empty TripID", i)
		}
		if ev.TripUUID == "" {
			t.Errorf("row %d: empty TripUUID", i)
		}
		if ev.PlannedTime.IsZero() {
			t.Errorf("row %d: zero PlannedTime", i)
		}
		if ev.LineCategory == "" {
			t.Errorf("row %d: empty LineCategory (label=%q)", i, ev.LineLabel)
		}
	}
	// Duplicate-number bug check: no platform token may be a doubled
	// single token (e.g. "77" for platform 7, "101101" for platform 101).
	// Real platforms like "11" are also a doubled "1"+"1" but only when the
	// half is 1 char; we accept that ambiguity for "11" specifically (a
	// legitimate platform) and only flag multi-char halves that would imply
	// an implausible platform (e.g. half "10" → "1010" implies platform 10
	// rendered twice; half "101" → "101101" implies platform 101 doubled).
	for _, ev := range events {
		if isDoubledBugToken(ev.Platform) {
			t.Errorf("duplicate-number bug present: Platform=%q", ev.Platform)
		}
	}
	// Prove the cleaning worked: there must be at least one platform that
	// is a small single number (e.g. "5", "6", "7", "8", "9").
	smallSingleFound := false
	for _, ev := range events {
		if len(ev.Platform) == 1 && ev.Platform[0] >= '1' && ev.Platform[0] <= '9' {
			smallSingleFound = true
			break
		}
	}
	if !smallSingleFound {
		t.Errorf("no single-digit platform found — cleaning may be broken")
	}
}

func TestParseBoard_Arrivals(t *testing.T) {
	events := parseFixture(t, "ankunft_normal.html", "arrival")
	if len(events) < 30 {
		t.Fatalf("expected >= 30 rows, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Direction != "arrival" {
			t.Errorf("row %d: Direction=%q want arrival", i, ev.Direction)
		}
	}
}

func TestParseBoard_Ersatzbus(t *testing.T) {
	// The captured arrival fixture contains real Ersatzbus rows (label
	// like "S4E" with the "Ersatzbus" heading span and an empty track cell).
	events := parseFixture(t, "ankunft_normal.html", "arrival")
	trueErsatz := 0
	for _, ev := range events {
		// True Ersatzbus rows: label matches S<digit>E and category is ersatz.
		if ev.LineCategory == "ersatz" && isErsatzbusLabel(ev.LineLabel) {
			trueErsatz++
			if ev.Platform != "" {
				t.Errorf("true Ersatzbus row (label=%q) should have empty Platform, got %q", ev.LineLabel, ev.Platform)
			}
			if len(ev.ViaSlugs) != 0 {
				t.Errorf("true Ersatzbus row (label=%q) should have empty ViaSlugs, got %v", ev.LineLabel, ev.ViaSlugs)
			}
		}
	}
	if trueErsatz == 0 {
		t.Fatalf("no true Ersatzbus row found in fixture")
	}
	t.Logf("found %d true Ersatzbus rows", trueErsatz)
}

// isDoubledBugToken reports whether s looks like a duplicate-number bug
// artifact: a token whose second half equals its first half AND the half is
// at least 2 characters (so "11" is NOT flagged — it could be platform 11).
// "77" (half "7") is also NOT flagged by this rule; we rely on the
// small-single-platform check instead to prove cleaning worked. The bug
// manifests as e.g. "101101" (half "101") or "1414" (half "14") which are
// implausible as real platforms.
func isDoubledBugToken(s string) bool {
	if len(s) < 4 || len(s)%2 != 0 {
		return false
	}
	half := s[:len(s)/2]
	if len(half) < 2 {
		return false
	}
	return half == s[len(s)/2:]
}

// isErsatzbusLabel reports whether the label is the Ersatzbus compact form
// "S" + single digit + "E" (e.g. "S3E", "S4E").
func isErsatzbusLabel(s string) bool {
	if len(s) != 3 {
		return false
	}
	return s[0] == 'S' && s[1] >= '0' && s[1] <= '9' && s[2] == 'E'
}