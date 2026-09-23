package shared

import (
	"fmt"
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

func TestFetchOffsetHash(t *testing.T) {
	// Deterministic: same key → same hash.
	a := FetchOffsetHash("frankfurt-main-hbf")
	b := FetchOffsetHash("frankfurt-main-hbf")
	if a != b {
		t.Errorf("FetchOffsetHash not deterministic: %d vs %d", a, b)
	}
	// Modulo at call sites must produce distinct slots across a cadence
	// window — including cadences > 30 (regression: the mod-30 bug left
	// slots 30-39 permanently empty at cadence 40).
	for _, cadence := range []uint64{30, 40, 60} {
		seen := map[uint64]struct{}{}
		for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
			seen[FetchOffsetHash(k)%cadence] = struct{}{}
		}
		if len(seen) < 3 {
			t.Errorf("FetchOffsetHash spread too weak at cadence %d: %d distinct slots", cadence, len(seen))
		}
	}
	// Slots must be able to reach the upper half of the cadence window
	// (would fail if a modulo were baked into the hash function).
	big := map[uint64]struct{}{}
	for i := 0; i < 200; i++ {
		big[FetchOffsetHash(fmt.Sprintf("station-%d", i))%40] = struct{}{}
	}
	for s := uint64(30); s < 40; s++ {
		if _, ok := big[s]; !ok {
			t.Errorf("cadence-40 slot %d never hit by 200 keys — distribution broken", s)
		}
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
