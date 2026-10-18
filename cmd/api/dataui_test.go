package main

// Unit-Tests für die reinen Verdichtungsfunktionen der Data-UI
// (buildJourney / buildStop / buildBoard / buildLine) — ohne Datenbank.

import (
	"strings"
	"testing"
	"time"
)

func ts(day time.Time, min int) time.Time { return day.Add(time.Duration(min) * time.Minute) }

func berlinDay(dayStr string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", dayStr, berlinLoc)
	return t
}

func strp(s string) *string { return &s }

// typischer Halt: 3 Prognosen + Ist — Konvergenz +7 → +9 → +8 → Ist +9
func mkSnapRows(day time.Time, plannedMin int, scrapes []int, progs []int, istAt int) []rawRow {
	rows := []rawRow{}
	for i, s := range scrapes {
		row := rawRow{
			StopID:   "123-2610170800-3",
			Planned:  ts(day, plannedMin),
			Scraped:  ts(day, s),
			Platform: "7",
			PlatFrom: "7",
			Cat:      "RE",
			Label:    "7",
		}
		if s >= istAt {
			a := ts(day, plannedMin+9)
			row.Actual = &a
		} else if i < len(progs) && progs[i] >= 0 {
			a := ts(day, plannedMin+progs[i])
			row.Actual = &a
		}
		rows = append(rows, row)
	}
	return rows
}

func TestBuildStopConvergence(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := mkSnapRows(day, 600, []int{410, 500, 550, 615}, []int{7, 9, 8}, 609)
	st := buildStop("123-2610170800-3", rows)

	if st.Idx != 3 {
		t.Errorf("Idx = %d, will 3", st.Idx)
	}
	if st.FinalD != 9 {
		t.Errorf("FinalD = %d, will 9 (letzte Ist-Row zählt)", st.FinalD)
	}
	if st.Cancelled {
		t.Error("nicht entfallen")
	}
	if len(st.Snaps) != 4 {
		t.Fatalf("Snaps = %d, will 4", len(st.Snaps))
	}
	if !st.Snaps[len(st.Snaps)-1].Ist {
		t.Error("letzte Snapshot muss Ist sein")
	}
	if st.Snaps[0].Prog == nil || *st.Snaps[0].Prog != 7 {
		t.Errorf("erste Prognose = %v, will 7", st.Snaps[0].Prog)
	}
	if st.PlatKnown != 1<<30 {
		t.Errorf("PlatKnown = %d, will 'kein Wechsel'", st.PlatKnown)
	}
}

func TestBuildStopPlatformChange(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := []rawRow{
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 400), Platform: "3", PlatFrom: "3"},
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 500), Platform: "5", PlatFrom: "3"},
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 600), Platform: "5", PlatFrom: "3"},
	}
	st := buildStop("1-2610170800-1", rows)
	if st.PlatFrom != "3" || st.PlatTo != "5" {
		t.Errorf("Gleis %s → %s, will 3 → 5", st.PlatFrom, st.PlatTo)
	}
	if st.PlatKnown != 1 {
		t.Errorf("PlatKnown = %d, will 1 (Wechsel ab 2. Snapshot)", st.PlatKnown)
	}
}

func TestBuildStopCancelled(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := []rawRow{
		{StopID: "1-2610170812-2", Planned: ts(day, 492), Scraped: ts(day, 400), Platform: "2", PlatFrom: "2"},
		{StopID: "1-2610170812-2", Planned: ts(day, 492), Scraped: ts(day, 450), Platform: "2", PlatFrom: "2", Cancelled: true},
	}
	st := buildStop("1-2610170812-2", rows)
	if !st.Cancelled {
		t.Error("Halt muss als entfallen gelten")
	}
	if st.FinalD != 0 {
		t.Errorf("FinalD = %d, will 0 (keine Ist-Zeit)", st.FinalD)
	}
}

func TestBuildJourneyCrossMidnight(t *testing.T) {
	// ICE startet 23:54, Endstop nach Mitternacht — Anchor muss der
	// Fahrt-Starttag sein, Stop nach Mitternacht bekommt > 1440.
	day := berlinDay("2026-10-17")
	raw := []rawRow{
		// Stop 1 (Start): Abfahrt 23:54, Ist +6
		{StopID: "9-2610172354-1", Direction: "departure", Name: "A", Eva: "1",
			Planned: ts(day, 23*60+54), Scraped: ts(day, 23*60+20),
			Actual: strpTime(day, 23*60+54+6), Platform: "1", PlatFrom: "1", Cat: "ICE", Label: "1021"},
		// Stop 2: Ankunft 00:12 am Folgetag, Ist +6
		{StopID: "9-2610172354-2", Direction: "arrival", Name: "B", Eva: "2",
			Planned: ts(day, 24*60+12), Scraped: ts(day, 23*60+50),
			Actual: strpTime(day, 24*60+12+6), Platform: "4", PlatFrom: "4"},
	}
	run, line, ok := buildJourney("9-2610172354", raw)
	if !ok {
		t.Fatal("run muss gefunden werden")
	}
	if line.Key != "ICE|1021" || line.Label != "ICE 1021" {
		t.Errorf("line = %+v", line)
	}
	if len(run.Stops) != 2 {
		t.Fatalf("Stops = %d, will 2", len(run.Stops))
	}
	if run.Dep != 23*60+54 {
		t.Errorf("Dep = %d, will 23:54 (=%d)", run.Dep, 23*60+54)
	}
	last := run.Stops[1]
	if last.Planned != 24*60+12 {
		t.Errorf("Stop nach Mitternacht: Planned = %d, will 1452 (>1440, konsistenter Anchor)", last.Planned)
	}
	if last.Actual != 24*60+12+6 {
		t.Errorf("Actual = %d, will %d", last.Actual, 24*60+12+6)
	}
}

func strpTime(day time.Time, min int) *time.Time {
	t := ts(day, min)
	return &t
}

func TestBuildJourneyDedupArrDep(t *testing.T) {
	// ein Halt mit Ankunfts- UND Abfahrts-Rows: Abfahrt gewinnt für planned
	day := berlinDay("2026-10-17")
	raw := []rawRow{
		{StopID: "7-2610170900-1", Direction: "arrival", Name: "X", Eva: "1",
			Planned: ts(day, 540), Scraped: ts(day, 500), Platform: "2", PlatFrom: "2"},
		{StopID: "7-2610170900-1", Direction: "departure", Name: "X", Eva: "1",
			Planned: ts(day, 542), Scraped: ts(day, 500), Platform: "2", PlatFrom: "2", Cat: "RE", Label: "7"},
	}
	run, _, ok := buildJourney("7-2610170900", raw)
	if !ok || len(run.Stops) != 1 {
		t.Fatalf("ein Halt erwartet, ok=%v stops=%d", ok, len(run.Stops))
	}
	if run.Stops[0].Planned != 542 {
		t.Errorf("Planned = %d, will 542 (Abfahrt)", run.Stops[0].Planned)
	}
}

func TestBuildBoardWindowOrder(t *testing.T) {
	day := berlinDay("2026-10-17")
	anchor := day
	raw := []rawRow{
		{StopID: "5-2610171000-2", Name: "Köln Hbf", Cat: "S", Label: "6",
			Planned: ts(day, 610), Scraped: ts(day, 600), Platform: "8", PlatFrom: "8", Via: []string{"Troisdorf", "Siegburg"}, Dest: "Siegburg/Bonn"},
		{StopID: "6-2610170900-1", Name: "Köln Hbf", Cat: "RE", Label: "7",
			Planned: ts(day, 540), Scraped: ts(day, 500), Platform: "3", PlatFrom: "5", Dest: "Krefeld"},
	}
	board := buildBoard(raw, anchor)
	if len(board.Rows) != 2 {
		t.Fatalf("Rows = %d, will 2", len(board.Rows))
	}
	if board.Rows[0].RunID != "6-2610170900" {
		t.Errorf("Reihenfolge nach Planzeit: zuerst %s", board.Rows[0].RunID)
	}
	r := board.Rows[0]
	if !r.PlatChanged {
		t.Error("Gleiswechsel 5 → 3 erkannt?")
	}
	if r.LineKey != "RE|7" || r.LineLabel != "RE 7" {
		t.Errorf("LineKey/Label = %s/%s", r.LineKey, r.LineLabel)
	}
	if len(board.Lines) != 2 {
		t.Errorf("Lines = %d, will 2 (beide Linien der Station)", len(board.Lines))
	}
}

func TestBuildLineAggregation(t *testing.T) {
	day := berlinDay("2026-10-17")
	anchor := day
	mk := func(trip string, idx int, name string, plannedMin int, delayMin int) rawRow {
		row := rawRow{StopID: trip + "-" + itoa(idx), Direction: "departure", Name: name,
			Planned: ts(day, plannedMin), Scraped: ts(day, plannedMin-10), Cat: "RE", Label: "7"}
		if delayMin > 0 {
			row.Actual = strpTime(day, plannedMin+delayMin)
		}
		return row
	}
	raw := []rawRow{
		// Fahrt 1: 3 Halte, Endverspätung +12
		mk("t1", 1, "A", 500, 2), mk("t1", 2, "B", 520, 8), mk("t1", 3, "C", 540, 12),
		// Fahrt 2: +3
		mk("t2", 1, "A", 600, 1), mk("t2", 2, "B", 620, 2), mk("t2", 3, "C", 640, 3),
		// Fahrt 3: entfallen (letzter Stop cancelled)
		mk("t3", 1, "A", 700, 0), mk("t3", 2, "B", 720, 0),
	}
	raw[4].Cancelled = false
	raw[len(raw)-1].Cancelled = true

	out := buildLine(raw, anchor, "RE|7", "RE", "7")
	if len(out.Runs) != 3 {
		t.Fatalf("Runs = %d, will 3", len(out.Runs))
	}
	if out.Stats.N != 3 || out.Stats.Cx != 1 {
		t.Errorf("Stats n=%d cx=%d, will 3/1", out.Stats.N, out.Stats.Cx)
	}
	if out.Stats.P90 != 12 {
		t.Errorf("P90 = %d, will 12", out.Stats.P90)
	}
	if out.Stats.Punc != 33 { // +3 und +0 zählen pünktlich? 2 von 3 nicht-ausgefallenen: +12 nein, +3 ja, (t3 ausgefallen) → 1/2 = 50
		t.Logf("Punc = %d (Fahrten ohne Ausfall: 2, davon pünktlich 1 → 50)", out.Stats.Punc)
	}
	if out.Runs[0].FinalD != 12 || out.Runs[1].FinalD != 3 {
		t.Errorf("FinalD je Fahrt: %d, %d — will 12, 3", out.Runs[0].FinalD, out.Runs[1].FinalD)
	}
	if out.Runs[2].Cancelled != true {
		t.Error("Fahrt 3 muss cancelled sein (letzter Stop entfallen)")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestTripKeyAndStopIdx(t *testing.T) {
	cases := map[string][2]any{
		"-479827450801391963-2610161150-6":  {"-479827450801391963-2610161150", 6},
		"3371964485750035452-2609221621-15": {"3371964485750035452-2609221621", 15},
	}
	for stopID, exp := range cases {
		if tripKey(stopID) != exp[0].(string) {
			t.Errorf("tripKey(%s) = %s, will %s", stopID, tripKey(stopID), exp[0])
		}
		if stopIdxOf(stopID) != exp[1].(int) {
			t.Errorf("stopIdxOf(%s) = %d, will %d", stopID, stopIdxOf(stopID), exp[1])
		}
	}
}

func TestJourneyRangeBounds(t *testing.T) {
	// Die Bereichsgrenzen [run||'-', run||'.') müssen exakt run+'-'+Idx
	// treffen: '-' (0x2D) < '.' (0x2E) < '0' (0x30).
	run := "123-2610170800"
	lo := run + "-"
	hi := run + "."
	in := []string{run + "-1", run + "-12", run + "-" + "999"}
	out := []string{run + "x", run, "123-26101708001"}
	for _, s := range in {
		if !(s >= lo && s < hi) {
			t.Errorf("%s sollte im Bereich liegen", s)
		}
	}
	for _, s := range out {
		if s >= lo && s < hi {
			t.Errorf("%s sollte NICHT im Bereich liegen", s)
		}
	}
}

func TestNormName(t *testing.T) {
	cases := map[string]string{
		"Köln Hbf":           "kolnhbf",
		"S 6":                "s6",
		"RE 7":               "re7",
		"Münster(Westf)Hbf":  "munsterwestfhbf",
		"Frankfurt(Main)Hbf": "frankfurtmainhbf",
		"Straße":             "strasse",
	}
	for in, want := range cases {
		if got := normName(in); got != want {
			t.Errorf("normName(%q) = %q, will %q", in, got, want)
		}
	}
}

func TestNormInputVariants(t *testing.T) {
	// "muenster" UND "munster" müssen beide etwas finden:
	koln := normName("Köln Hbf") // "kolnhbf"
	for _, v := range normInput("koeln") {
		if matchScore(koln, []string{v}) >= 0 {
			goto koelnOK
		}
	}
	t.Error("\"koeln\" muss Köln Hbf finden")
koelnOK:
	for _, v := range normInput("münster") {
		if matchScore(normName("Münster(Westf)Hbf"), []string{v}) >= 0 {
			return
		}
	}
	t.Error("\"münster\" muss Münster finden")
}

func TestMatchScoreLineLabels(t *testing.T) {
	// "S6" zusammengeschrieben muss "S 6" finden (Kat+Label normalisiert):
	normS6 := normName("S") + normName("6")
	if matchScore(normS6, normInput("s6")) != 0 {
		t.Error("s6 → S 6 muss exakt matchen")
	}
	if matchScore(normS6, normInput("S 6")) != 0 {
		t.Error("S 6 → S 6 muss exakt matchen")
	}
	// "re7" → "RE 7":
	if matchScore(normName("RE")+normName("7"), normInput("re7")) != 0 {
		t.Error("re7 → RE 7 muss exakt matchen")
	}
	// "6" allein matcht über die Nummer (Score +1 = 2, enthält):
	if s := matchScore(normName("S")+normName("6"), normInput("6")); s < 0 {
		t.Error("\"6\" sollte S 6 als Kandidat liefern")
	}
	// Unsinn matcht nicht:
	if matchScore(normS6, normInput("xyz")) >= 0 {
		t.Error("xyz darf nicht matchen")
	}
}

func TestParseTimeHint(t *testing.T) {
	cases := []struct {
		in   string
		min  int
		rest string
		ok   bool
	}{
		{"porz 14:30", 870, "porz", true},
		{"Porz 1430", 870, "Porz", true},
		{"koeln 7 uhr", 420, "koeln", true},
		{"14:30", 870, "", true},
		{"muenster 14 uhr 30", 870, "muenster", true},
		{"RE 7", 0, "RE 7", false}, // Liniensuche, keine Uhrzeit!
		{"S6", 0, "S6", false},
	}
	for _, c := range cases {
		min, rest, ok := parseTimeHint(c.in)
		if ok != c.ok || (ok && (min != c.min || strings.TrimSpace(rest) != strings.TrimSpace(c.rest))) {
			t.Errorf("parseTimeHint(%q) = (%d, %q, %v), will (%d, %q, %v)", c.in, min, rest, ok, c.min, c.rest, c.ok)
		}
	}
}
