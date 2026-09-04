package activities

import (
	"context"
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verspaetet/shared"
)

func loadXMLFixture(t *testing.T, name string) *Timetable {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var tt Timetable
	if err := xml.Unmarshal(b, &tt); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return &tt
}

func TestParseIrisTime(t *testing.T) {
	// 2026-09-04 10:03 Berlin (CEST, UTC+2) → 08:03 UTC.
	got, err := parseIrisTime("2609041003")
	if err != nil {
		t.Fatalf("parseIrisTime: %v", err)
	}
	want := time.Date(2026, 9, 4, 8, 3, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseIrisTime = %v, want %v", got, want)
	}
	// Winter time (CET, UTC+1): 2026-01-04 10:03 → 09:03 UTC.
	got, err = parseIrisTime("2601041003")
	if err != nil {
		t.Fatalf("parseIrisTime: %v", err)
	}
	want = time.Date(2026, 1, 4, 9, 3, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("winter parseIrisTime = %v, want %v", got, want)
	}
}

func TestIrisXMLDecode(t *testing.T) {
	tt := loadXMLFixture(t, "iris_fchg.xml")
	if tt.Eva != "8000105" {
		t.Errorf("Eva = %q", tt.Eva)
	}
	if tt.Station != "Frankfurt(Main)Hbf" {
		t.Errorf("Station = %q", tt.Station)
	}
	if len(tt.Stops) != 3 {
		t.Fatalf("expected 3 stops, got %d", len(tt.Stops))
	}

	// Stop 1: ICE 577, delayed departure (ct), platform 7 (cp), message present.
	s1 := tt.Stops[0]
	if s1.TL.C != "ICE" || s1.TL.N != "577" || s1.TL.O != "DB" || s1.TL.T != "p" {
		t.Errorf("trip label = %+v", s1.TL)
	}
	if s1.DP == nil {
		t.Fatal("stop 1 has no dp")
	}
	if s1.DP.CT != "2609041003" || s1.DP.CP != "7" || s1.DP.CS != "p" {
		t.Errorf("dp changed attrs = %+v", s1.DP)
	}
	if s1.DP.PT != "" {
		t.Errorf("fchg payload should not carry pt, got %q", s1.DP.PT)
	}
	if s1.AR != nil {
		t.Error("stop 1 should not have ar (departure only)")
	}

	// Stop 2: S8 with both ar and dp.
	s2 := tt.Stops[1]
	if s2.AR == nil || s2.DP == nil {
		t.Fatal("stop 2 should have both ar and dp")
	}
	if s2.TL.T != "s" {
		t.Errorf("trip kind = %q, want s", s2.TL.T)
	}

	// Stop 3: cancelled departure (cs="c", clt set).
	s3 := tt.Stops[2]
	if s3.DP == nil || s3.DP.CS != "c" {
		t.Fatalf("stop 3 should be cancelled, dp=%+v", s3.DP)
	}
	if s3.DP.CLT != "2609040955" {
		t.Errorf("clt = %q", s3.DP.CLT)
	}
}

func TestIrisEventToStopEvent(t *testing.T) {
	tt := loadXMLFixture(t, "iris_fchg.xml")
	scrapedAt := time.Now().UTC()

	// Delayed ICE departure (ct≠pt via fchg payload).
	se, ok := irisEventToStopEvent(&tt.Stops[0], tt.Stops[0].DP, "8000105", "departure", scrapedAt)
	if !ok {
		t.Fatal("event mapping failed")
	}
	if se.LineCategory != "ICE" || se.TrainNumber != "577" || se.Owner != "DB" {
		t.Errorf("trip fields: %+v", se)
	}
	if len(se.ViaPath) != 3 || se.ViaPath[2] != "Köln Hbf" {
		t.Errorf("ViaPath = %v", se.ViaPath)
	}
	if se.DirectionName != "Köln Hbf" {
		t.Errorf("DirectionName = %q", se.DirectionName)
	}
	if se.Cancelled {
		t.Error("ICE 577 should not be cancelled")
	}
	if se.ActualTime == nil {
		t.Fatal("ActualTime should be set (ct present)")
	}
	// fchg-only stop: no pt in payload → planned falls back to ct (10:03
	// Berlin). The plan/fchg merge in FetchStationBoard backfills the real
	// planned time; here we exercise the fchg-alone path.
	if !se.PlannedTime.Equal(mustParseUTC(t, "2609041003")) {
		t.Errorf("PlannedTime = %v, want fallback to ct 10:03 Berlin", se.PlannedTime)
	}
	// Trip date from stop id part 2.
	if se.TripDate == nil {
		t.Fatal("TripDate should be derived from stop id")
	}

	// Cancelled RE departure.
	se3, ok := irisEventToStopEvent(&tt.Stops[2], tt.Stops[2].DP, "8000105", "departure", scrapedAt)
	if !ok {
		t.Fatal("event mapping failed for cancelled stop")
	}
	if !se3.Cancelled {
		t.Error("RE 30 should be cancelled (cs=c)")
	}
	if se3.PlannedPlatform != "" {
		// fchg payload has no pp for this stop — planned platform empty.
		t.Logf("note: planned platform = %q", se3.PlannedPlatform)
	}
	if se3.Platform != "5" {
		t.Errorf("Platform = %q, want changed platform 5", se3.Platform)
	}
}

func mustParseUTC(t *testing.T, iris string) time.Time {
	t.Helper()
	tm, err := parseIrisTime(iris)
	if err != nil {
		t.Fatalf("mustParseUTC: %v", err)
	}
	return tm
}

func TestIrisMergePlanAndFchg(t *testing.T) {
	// The merge logic lives in FetchStationBoard; here we exercise it via a
	// local fixture HTTP server.
	t.Setenv("IRIS_BASE_URL", "http://127.0.0.1:18099")
	dir, err := filepath.Abs(filepath.Join("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fchg/8000105", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(filepath.Join(dir, "iris_fchg.xml"))
		w.Header().Set("Content-Type", "application/xml")
		w.Write(b)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// /plan/... — any hour.
		b, _ := os.ReadFile(filepath.Join(dir, "iris_plan.xml"))
		w.Header().Set("Content-Type", "application/xml")
		w.Write(b)
	})
	srv := &http.Server{Addr: "127.0.0.1:18099", Handler: mux}
	go srv.ListenAndServe()
	defer srv.Close()
	time.Sleep(100 * time.Millisecond)

	iris := &Iris{}
	res, err := iris.FetchStationBoard(context.Background(), shared.FetchStationBoardInput{
		Eva: "8000105", Direction: "departure",
	})
	if err != nil {
		t.Fatalf("FetchStationBoard: %v", err)
	}

	// 3 unique departures: ICE 577 (fchg), S8 dp (fchg), RE 30 (fchg),
	// ICE 990 (plan only, unchanged). ICE 577 exists in both → merged once.
	byNumber := map[string]int{}
	for _, ev := range res.Events {
		byNumber[ev.TrainNumber]++
	}
	if byNumber["577"] != 1 {
		t.Errorf("ICE 577 appears %d times (plan+fchg must merge to 1)", byNumber["577"])
	}
	if byNumber["990"] != 1 {
		t.Errorf("ICE 990 (plan-only, unchanged) appears %d times, want 1", byNumber["990"])
	}
	if byNumber["30"] != 1 {
		t.Errorf("RE 30 appears %d times, want 1", byNumber["30"])
	}
	if byNumber["8"] != 1 {
		t.Errorf("S8 departure appears %d times, want 1", byNumber["8"])
	}

	// Arrival direction: S8 ar + ICE 577 has no ar → only S8.
	resArr, err := iris.FetchStationBoard(context.Background(), shared.FetchStationBoardInput{
		Eva: "8000105", Direction: "arrival",
	})
	if err != nil {
		t.Fatalf("FetchStationBoard arrival: %v", err)
	}
	if len(resArr.Events) != 1 || resArr.Events[0].TrainNumber != "8" {
		t.Errorf("arrival events = %+v, want only S8", resArr.Events)
	}
	_ = strings.TrimSpace
}