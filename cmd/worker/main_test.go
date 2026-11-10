package main

import (
	"errors"
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	cadence := 40 * time.Minute
	cases := []struct {
		name       string
		enqueuedAt int64
		recheck    bool
		want       bool
	}{
		{"fresh", now.Add(-5 * time.Minute).Unix(), false, false},
		{"exactly at cadence", now.Add(-cadence).Unix(), false, false},
		{"older than cadence", now.Add(-cadence - time.Second).Unix(), false, true},
		{"zero enqueued_at", 0, false, false},
		{"negative enqueued_at", -1, false, false},
		{"recheck never stale", now.Add(-24 * time.Hour).Unix(), true, false},
	}
	for _, c := range cases {
		if got := isStale(c.enqueuedAt, c.recheck, now, cadence); got != c.want {
			t.Errorf("%s: isStale(%d, %v) = %v, want %v", c.name, c.enqueuedAt, c.recheck, got, c.want)
		}
	}
}

func TestShouldMarkNoIris(t *testing.T) {
	lookupErr := errors.New("no station row")
	if shouldMarkNoIris(false, nil) != true {
		t.Error("never had data, clean lookup: want mark")
	}
	if shouldMarkNoIris(true, nil) != false {
		t.Error("prior data: want keep")
	}
	if shouldMarkNoIris(false, lookupErr) != false {
		t.Error("lookup error: want keep")
	}
	if shouldMarkNoIris(true, lookupErr) != false {
		t.Error("prior data + lookup error: want keep")
	}
}
