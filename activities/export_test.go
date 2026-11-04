package activities

import (
	"os"
	"path/filepath"
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
