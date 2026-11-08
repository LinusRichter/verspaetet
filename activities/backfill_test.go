package activities

import (
	"strings"
	"testing"
)

// prepareBackfillManifest is the pure validation+merge of the backfill: the
// refusal cases guard against clobbering a published chunk, and the success
// case must keep every published field untouched except the schema version.
func TestPrepareBackfillManifest(t *testing.T) {
	valid := func() *Manifest {
		m := &Manifest{Year: 2026, Month: 9, SchemaVersion: "v0.2"}
		m.StopEvents.Rows = 25266564
		m.StopEvents.MinScraped = "2026-09-21T18:00:00Z"
		m.StopEvents.MaxScraped = "2026-10-01T09:09:10Z"
		m.Stations.Rows = 5461
		m.Coverage.Downtimes = []string{"2026-10-19_note.txt: reason"}
		m.Attribution = datasetAttribution
		return m
	}

	cases := []struct {
		name    string
		m       *Manifest
		year    int
		month   int
		wantErr string
	}{
		{"wrong month", valid(), 2026, 10, "requested 2026-10"},
		{"no stop_events", func() *Manifest {
			m := valid()
			m.StopEvents.Rows = 0
			return m
		}(), 2026, 9, "no stop_events rows"},
		{"no scraped range", func() *Manifest {
			m := valid()
			m.StopEvents.MinScraped = ""
			return m
		}(), 2026, 9, "no scraped_at range"},
		{"already backfilled", func() *Manifest {
			m := valid()
			m.ScrapeRuns.Rows = 42
			return m
		}(), 2026, 9, "already backfilled"},
	}
	for _, c := range cases {
		err := prepareBackfillManifest(c.m, c.year, c.month)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
		}
	}

	m := valid()
	if err := prepareBackfillManifest(m, 2026, 9); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != "v0.3" {
		t.Errorf("schema version = %q, want v0.3", m.SchemaVersion)
	}
	// Everything from the published manifest must survive the merge.
	if m.StopEvents.Rows != 25266564 || m.StopEvents.MinScraped != "2026-09-21T18:00:00Z" {
		t.Errorf("stop_events fields lost: %+v", m.StopEvents)
	}
	if m.Stations.Rows != 5461 {
		t.Errorf("stations rows lost: %d", m.Stations.Rows)
	}
	if len(m.Coverage.Downtimes) != 1 || m.Coverage.Downtimes[0] != "2026-10-19_note.txt: reason" {
		t.Errorf("downtimes lost: %q", m.Coverage.Downtimes)
	}
	if m.Attribution != datasetAttribution {
		t.Error("attribution lost")
	}
}
