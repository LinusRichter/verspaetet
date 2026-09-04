package shared

import (
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Frankfurt(Main)Hbf", "frankfurt-main-hbf"},
		{"Köln Hbf", "koeln-hbf"},
		{"München Ost", "muenchen-ost"},
		{"Berlin-Grunewald (Bahnhof)", "berlin-grunewald-bahnhof"},
		{"Bad Hersfeld", "bad-hersfeld"},
		{"Gießen", "giessen"},
	}
	for _, tt := range tests {
		if got := slugify(tt.in); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFetchOffset(t *testing.T) {
	// Deterministic: same key → same value.
	a := FetchOffset("frankfurt-main-hbf")
	b := FetchOffset("frankfurt-main-hbf")
	if a != b {
		t.Errorf("FetchOffset not deterministic: %d vs %d", a, b)
	}
	// In range [0, 30).
	if a < 0 || a >= 30 {
		t.Errorf("FetchOffset out of range: %d", a)
	}
	// Distinct keys spread (10 keys → expect at least 3 distinct slots).
	seen := map[int]struct{}{}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		seen[FetchOffset(k)] = struct{}{}
	}
	if len(seen) < 3 {
		t.Errorf("FetchOffset spread too weak: %d distinct slots for 10 keys", len(seen))
	}
}

func TestStopEventTypes(t *testing.T) {
	// Compile-time sanity: pointer fields exist and zero cleanly.
	ev := StopEvent{}
	if ev.TripDate != (*time.Time)(nil) {
		t.Fatal("TripDate zero value should be nil pointer")
	}
	if ev.ActualTime != (*time.Time)(nil) {
		t.Fatal("ActualTime zero value should be nil pointer")
	}
	_ = strings.TrimSpace("")
}