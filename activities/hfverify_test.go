package activities

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hfFake serves the two endpoints the verification hits: the repo tree and
// the published manifest. Both are swapped per test case.
type hfFake struct {
	// tree returns the repo tree entries and status for the nth call
	// (1-based); nil means a 500.
	tree     func(call int) ([]hfTreeEntry, int)
	manifest *Manifest
	calls    int
}

func newHFFake(t *testing.T) *hfFake {
	t.Helper()
	oldEndpoint, oldPause := hfEndpoint, hfVerifyPause
	hfVerifyPause = 0
	t.Setenv("HF_REPO", "test/repo")
	t.Setenv("HF_TOKEN", "tok")
	t.Cleanup(func() { hfEndpoint, hfVerifyPause = oldEndpoint, oldPause })

	f := &hfFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/datasets/test/repo/tree/main/", func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		entries, status := f.tree(f.calls)
		w.WriteHeader(status)
		if status == http.StatusOK {
			json.NewEncoder(w).Encode(entries)
		}
	})
	mux.HandleFunc("/datasets/test/repo/resolve/main/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/manifest.json") {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(f.manifest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hfEndpoint = srv.URL
	return f
}

// verifyWant is the expected chunk: one stop_events and one scrape_runs file.
func verifyWant() *Manifest {
	m := &Manifest{Year: 2026, Month: 10}
	m.StopEvents.Rows = 100
	m.StopEvents.Sha256 = "se-sha"
	m.StopEvents.SizeBytes = 12345
	m.ScrapeRuns.Rows = 50
	m.ScrapeRuns.Sha256 = "sr-sha"
	m.ScrapeRuns.SizeBytes = 678
	return m
}

// entries builds a repo tree for want with swappable oids; srOid "" omits
// the scrape_runs file.
func entries(want *Manifest, stopOid, srOid string) []hfTreeEntry {
	out := []hfTreeEntry{{
		Type: "file", Path: "2026-10/stop_events.parquet", Size: want.StopEvents.SizeBytes,
		Lfs: &struct {
			Oid  string `json:"oid"`
			Size *int64 `json:"size"`
		}{Oid: stopOid},
	}}
	if srOid != "" {
		size := want.ScrapeRuns.SizeBytes
		out = append(out, hfTreeEntry{
			Type: "file", Path: "2026-10/scrape_runs.parquet", Size: size,
			Lfs: &struct {
				Oid  string `json:"oid"`
				Size *int64 `json:"size"`
			}{Oid: srOid, Size: &size},
		})
	}
	return out
}

func TestVerifyOnHuggingFace(t *testing.T) {
	want := verifyWant()
	cases := []struct {
		name      string
		treeOids  func() []hfTreeEntry
		manifest  *Manifest
		wantErr   string
		wantCalls int
	}{
		{
			name:      "all match",
			treeOids:  func() []hfTreeEntry { return entries(want, "se-sha", "sr-sha") },
			manifest:  want,
			wantCalls: 1,
		},
		{
			name:      "wrong oid",
			treeOids:  func() []hfTreeEntry { return entries(want, "wrong-oid", "sr-sha") },
			manifest:  want,
			wantErr:   "lfs oid",
			wantCalls: hfVerifyAttempts,
		},
		{
			name:      "missing scrape_runs file",
			treeOids:  func() []hfTreeEntry { return entries(want, "se-sha", "") },
			manifest:  want,
			wantErr:   "missing from repo tree",
			wantCalls: hfVerifyAttempts,
		},
		{
			name:      "manifest rows differ",
			treeOids:  func() []hfTreeEntry { return entries(want, "se-sha", "sr-sha") },
			manifest:  func() *Manifest { m := verifyWant(); m.StopEvents.Rows = 101; return m }(),
			wantErr:   "rows",
			wantCalls: hfVerifyAttempts,
		},
		{
			name: "first tree call 500, then ok",
			treeOids: func() []hfTreeEntry {
				return entries(want, "se-sha", "sr-sha") // status via calls below
			},
			manifest:  want,
			wantCalls: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newHFFake(t)
			switch c.name {
			case "first tree call 500, then ok":
				f.tree = func(call int) ([]hfTreeEntry, int) {
					if call == 1 {
						return nil, 500
					}
					return c.treeOids(), 200
				}
			default:
				f.tree = func(int) ([]hfTreeEntry, int) { return c.treeOids(), 200 }
			}
			f.manifest = c.manifest
			err := VerifyOnHuggingFace(t.Context(), "2026-10", want)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
			if f.calls != c.wantCalls {
				t.Errorf("tree served %d times, want %d", f.calls, c.wantCalls)
			}
		})
	}
}

// VerifyRunOnHuggingFace compares only oids (sizes are not stored).
func TestVerifyRunOnHuggingFace(t *testing.T) {
	want := verifyWant()
	rows, srRows := int64(100), int64(50)
	run := &ExportRun{
		Month:            "2026-10",
		Rows:             &rows,
		StopEventsSha256: ptrString("se-sha"),
		ScrapeRunsRows:   &srRows,
		ScrapeRunsSha256: ptrString("sr-sha"),
	}
	f := newHFFake(t)
	f.tree = func(int) ([]hfTreeEntry, int) { return entries(want, "se-sha", "sr-sha"), 200 }
	f.manifest = want
	if err := VerifyRunOnHuggingFace(t.Context(), run); err != nil {
		t.Fatalf("want nil, got %v", err)
	}

	// A stale sha (row from a different chunk) must fail.
	run2 := &ExportRun{Month: "2026-10", Rows: &rows, StopEventsSha256: ptrString("other-sha")}
	if err := VerifyRunOnHuggingFace(t.Context(), run2); err == nil || !strings.Contains(err.Error(), "lfs oid") {
		t.Fatalf("err = %v, want lfs oid mismatch", err)
	}
}

func ptrString(s string) *string { return &s }
