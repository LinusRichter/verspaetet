package activities

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed export is retried: the notes must survive until the chunk is
// complete, or the published manifest loses them (October 2026 export).
func TestDowntimeNotesSurviveUntilConsumed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOWNTIMES_DIR", dir)
	for name, content := range map[string]string{
		"a.txt":     "reason a",
		"empty.txt": "",
		"skip.md":   "not a note",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for attempt := 1; attempt <= 2; attempt++ {
		notes, files := readDowntimeNotes()
		if len(notes) != 2 || len(files) != 2 {
			t.Fatalf("attempt %d: got notes %q files %q", attempt, notes, files)
		}
		if notes[0] != "a.txt: reason a" || notes[1] != "empty.txt" {
			t.Fatalf("attempt %d: notes = %q", attempt, notes)
		}
	}

	_, files := readDowntimeNotes()
	if err := os.WriteFile(filepath.Join(dir, "late.txt"), []byte("filed during export"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{downtimeFiles: files}
	m.ConsumeDowntimeNotes()

	notes, _ := readDowntimeNotes()
	if len(notes) != 1 || notes[0] != "late.txt: filed during export" {
		t.Fatalf("after consume: notes = %q", notes)
	}
	if _, err := os.Stat(filepath.Join(dir, "skip.md")); err != nil {
		t.Fatalf("non-note file removed: %v", err)
	}
}

func TestDowntimeNotesMissingDir(t *testing.T) {
	t.Setenv("DOWNTIMES_DIR", filepath.Join(t.TempDir(), "nope"))
	if notes, files := readDowntimeNotes(); notes != nil || files != nil {
		t.Fatalf("got notes %q files %q", notes, files)
	}
}

func TestHfRepoPath(t *testing.T) {
	cases := []struct{ month, name, want string }{
		{"2026-11", "stations.parquet", "stations.parquet"},
		{"2026-11", "stop_events_2026-11.parquet", "2026-11/stop_events.parquet"},
		{"2026-11", "scrape_runs_2026-11.parquet", "2026-11/scrape_runs.parquet"},
		{"2026-11", "manifest.json", "2026-11/manifest.json"},
		{"2026-11", "README.md", "2026-11/README.md"},
	}
	for _, c := range cases {
		if got := hfRepoPath(c.month, c.name); got != c.want {
			t.Errorf("hfRepoPath(%q, %q) = %q, want %q", c.month, c.name, got, c.want)
		}
	}
}

func TestIsOldFlatRepoPath(t *testing.T) {
	// Leftovers of the old flat layout (and duplicate month-suffixed
	// parquets inside month folders) get removed by the regular upload.
	for _, p := range []string{
		"stop_events_2026-09.parquet",
		"manifest.json",
		"2026-10/stop_events_2026-10.parquet",
	} {
		if !isOldFlatRepoPath(p) {
			t.Errorf("isOldFlatRepoPath(%q) = false, want true", p)
		}
	}
	// The current layout must never be deleted — including the new
	// scrape_runs file.
	for _, p := range []string{
		"2026-10/scrape_runs.parquet",
		"2026-10/stop_events.parquet",
		"stations.parquet",
		"2026-10/manifest.json",
		"2026-10/README.md",
		"README.md",
	} {
		if isOldFlatRepoPath(p) {
			t.Errorf("isOldFlatRepoPath(%q) = true, want false", p)
		}
	}
}

func TestWriteDatasetCardScrapeRuns(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Year: 2026, Month: 11, SchemaVersion: "v0.3"}
	m.StopEvents.Rows = 25266564
	m.Stations.Rows = 5461
	m.ScrapeRuns.Rows = 11000000
	m.ScrapeRuns.File = "scrape_runs.parquet"
	if err := WriteDatasetCard(dir, m, 5461); err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(card)
	if !strings.Contains(s, "stop_events is change-only: a new row is written only when actual time, platform, destination, route or cancellation changed.") {
		t.Error("card must contain the exact change-only sentence")
	}
	if !strings.Contains(s, "scrape_runs") {
		t.Error("card must mention scrape_runs for a v0.3 manifest")
	}
	if !strings.Contains(s, "scrape_runs.parquet`: 11000000 rows") {
		t.Error("Files section must list the scrape_runs file")
	}
}

func TestWriteDatasetCardOldManifest(t *testing.T) {
	// A v0.2 manifest (no scrape_runs) must not list the file.
	dir := t.TempDir()
	m := &Manifest{Year: 2026, Month: 9, SchemaVersion: "v0.2"}
	m.StopEvents.Rows = 25266564
	m.Stations.Rows = 5461
	if err := WriteDatasetCard(dir, m, 5461); err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(card)
	if strings.Contains(s, "/scrape_runs.parquet`") {
		t.Error("old manifest card must not list a scrape_runs file")
	}
	if !strings.Contains(s, "has no fetch log") {
		t.Error("old manifest card must say that unchanged and not observed cannot be told apart")
	}
}

// An empty re-export (month already pruned locally) must leave the local
// copy of the published chunk untouched.
func TestExportMonthEmptyKeepsExistingFile(t *testing.T) {
	pool := testDB(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "stop_events_2026-10.parquet")
	if err := os.WriteFile(existing, []byte("published chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExportMonth(context.Background(), pool, dir, 2026, 10, "v0.3")
	if !errors.Is(err, ErrEmptyMonth) {
		t.Fatalf("err = %v, want ErrEmptyMonth", err)
	}
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != "published chunk" {
		t.Fatalf("existing file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(existing + ".new"); !os.IsNotExist(err) {
		t.Fatalf("side file left behind: %v", err)
	}
}
