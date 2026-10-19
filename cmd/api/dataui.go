package main

// Data-UI endpoints (v2). READ-ONLY: kein Endpoint hier schreibt in die DB.
//
// Zeitmodell: alle gelieferten Zeiten sind MINUTEN SEIT TAGESANKER
// (Mitternacht Europe/Berlin des geladenen Tages). Fahrten über Mitternacht
// bekommen konsistent Werte > 1440 — sie liegen auf demselben Zeitstrahl
// wie der Wissensstand-Scrubber.
//
// Begriffe: Fahrt = stop_id-Präfix (dailyTripId-YYMMddHHmm), Halt =
// stop_id-Gruppe (Ankunft+Abfahrt an einer Station), Snapshot = eine
// stop_events-Row (scraped_at = Wissensstand).
//
// Der Journey-Zugriff ist eine Bereichssuche `stop_id >= run||'-' AND
// stop_id < run||'.'` auf dem vorhandenen (stop_id, scraped_at DESC)-Index
// (normale Range-Komparatoren funktionieren unter jeder Collation, anders
// als LIKE-Muster). Deshalb: KEINE Migration nötig.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Europe/Berlin auch in Containern ohne OS-tzdata

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Zeit-Helfer
// ---------------------------------------------------------------------------

var berlinLoc = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic("Europe/Berlin unavailable despite embedded tzdata: " + err.Error())
	}
	return loc
}()

// dayAnchor returns midnight Europe/Berlin of the given timestamp's local day.
func dayAnchor(ts time.Time) time.Time {
	local := ts.In(berlinLoc)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, berlinLoc)
}

// minutesOfDay: minutes since anchor; may be negative or > 1440.
func minutesOfDay(ts time.Time, anchor time.Time) int {
	return int(ts.Sub(anchor) / time.Minute)
}

// ---------------------------------------------------------------------------
// TTL-Cache (in-process; Tagesaggregate sind historisch stabil)
// ---------------------------------------------------------------------------

type ttlEntry[T any] struct {
	value   T
	expires time.Time
}

type ttlCache[T any] struct {
	mu  sync.Mutex
	m   map[string]ttlEntry[T]
	ttl time.Duration // 0 = unbegrenzt (historische Tage)
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{m: make(map[string]ttlEntry[T]), ttl: ttl}
}

func (c *ttlCache[T]) get(key string, now time.Time) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		var zero T
		return zero, false
	}
	if c.ttl > 0 && now.After(e.expires) {
		delete(c.m, key)
		var zero T
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[T]) put(key string, v T, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := ttlEntry[T]{value: v}
	if c.ttl > 0 {
		e.expires = now.Add(c.ttl)
	}
	c.m[key] = e
}

// ---------------------------------------------------------------------------
// Roh-Row-Modell: Handler scannen, reine Funktionen aggregieren.
// ---------------------------------------------------------------------------

type rawRow struct {
	StopID    string
	Direction string
	Eva       string
	Name      string
	Planned   time.Time
	Actual    *time.Time
	Cancelled bool
	Platform  string
	PlatFrom  string
	Scraped   time.Time
	Cat       string
	Label     string
	Via       []string
	Dest      string
}

var tripSuffixRe = regexp.MustCompile(`-[0-9]+$`)

func tripKey(stopID string) string { return tripSuffixRe.ReplaceAllString(stopID, "") }

func stopIdxOf(stopID string) int {
	i := strings.LastIndex(stopID, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(stopID[i+1:])
	if err != nil {
		return 0
	}
	return n
}

func lineKeyOf(cat, label string) string { return cat + "|" + label }
func lineLabelOf(cat, label string) string {
	return strings.TrimSpace(cat + " " + label)
}

// labelExpr: SQL-Ausdruck für das öffentliche Linien-Label
// (line_label, sonst Betriebsnummer als Fallback für alte Tage).
const labelExpr = `COALESCE(NULLIF(line_label, ''), NULLIF(train_number, ''), '')`

// ---------------------------------------------------------------------------
// JSON-Verträge (spiegeln web/src/types.ts)
// ---------------------------------------------------------------------------

type uiMeta struct {
	Day string `json:"day"`
	T0  int    `json:"t0"`
	T1  int    `json:"t1"`
	Now int    `json:"now"`
}

type uiLineInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	Ctx   string `json:"ctx,omitempty"`
}

type uiSearchResults struct {
	Stations []uiSearchStation `json:"stations"`
	Lines    []uiSearchLine    `json:"lines"`

	// Wenn der Query eine Uhrzeit enthielt („porz 14:30"):
	Time *int `json:"time,omitempty"` // Minuten des Tages

	// Wenn ein Bahnhof stark matchte: die Linien, die dort halten.
	StationName  string         `json:"stationName,omitempty"`
	StationLines []uiSearchLine `json:"stationLines,omitempty"`
}

type uiSearchStation struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type uiSearchLine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	Ctx   string `json:"ctx"`
	N     int    `json:"n"`
}

type uiBoardData struct {
	Rows  []uiBoardRow `json:"rows"`
	Lines []uiLineInfo `json:"lines"`
}

type uiBoardRow struct {
	RunID       string `json:"runId"`
	StopIdx     int    `json:"stopIdx"`
	LineKey     string `json:"lineKey"`
	LineLabel   string `json:"lineLabel"`
	Planned     int    `json:"planned"`
	Known       bool   `json:"known"`
	Delay       *int   `json:"delay"`
	Ist         bool   `json:"ist"`
	Cancelled   bool   `json:"cancelled"`
	Plat        string `json:"plat"`
	PlatFrom    string `json:"platFrom"`
	PlatChanged bool   `json:"platChanged"`
	Dest        string `json:"dest"`
	Via         string `json:"via"`
}

type uiAltLine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	RunID string `json:"runId"`
	Dep   int    `json:"dep"`
}

type uiSnap struct {
	At        int    `json:"at"`
	Prog      *int   `json:"prog"`
	Ist       bool   `json:"ist"`
	Cancelled bool   `json:"cancelled"`
	Plat      string `json:"plat"`

	// interne Zeitstempel für den Anchor-Durchlauf (nicht JSON):
	atTS time.Time `json:"-"`
}

type uiStop struct {
	Name      string   `json:"name"`
	Eva       string   `json:"eva"`
	Idx       int      `json:"idx"`
	Planned   int      `json:"planned"`
	FinalD    int      `json:"finalD"`
	Actual    int      `json:"actual"`
	Cancelled bool     `json:"cancelled"`
	PlatFrom  string   `json:"platFrom"`
	PlatTo    string   `json:"platTo"`
	PlatKnown int      `json:"platKnown"` // großer Wert = kein Wechsel
	Snaps     []uiSnap `json:"snaps"`

	plannedTS time.Time `json:"-"`
}

type uiRun struct {
	ID        string   `json:"id"`
	LineKey   string   `json:"lineKey"`
	Dep       int      `json:"dep"`
	Cancelled bool     `json:"cancelled"`
	Stops     []uiStop `json:"stops"`
}

type uiJourneyData struct {
	Run       uiRun       `json:"run"`
	Line      uiLineInfo  `json:"line"`
	AltLines  []uiAltLine `json:"altLines"`
	AltScoped bool        `json:"altScoped"`
}

type uiLineRunStop struct {
	Name      string `json:"name"`
	FinalD    int    `json:"finalD"`
	Cancelled bool   `json:"cancelled"`
}

type uiLineRun struct {
	ID        string          `json:"id"`
	Dep       int             `json:"dep"`
	Cancelled bool            `json:"cancelled"`
	FinalD    int             `json:"finalD"`
	Stops     []uiLineRunStop `json:"stops"`
}

type uiLineStats struct {
	N    int `json:"n"`
	Cx   int `json:"cx"`
	P50  int `json:"p50"`
	P90  int `json:"p90"`
	Max  int `json:"max"`
	Punc int `json:"punc"`
	GC   int `json:"gc"`
}

type uiLineData struct {
	Line      uiLineInfo  `json:"line"`
	Ctx       string      `json:"ctx,omitempty"`
	Runs      []uiLineRun `json:"runs"`
	Stats     uiLineStats `json:"stats"`
	AltLines  []uiAltLine `json:"altLines"`
	AltScoped bool        `json:"altScoped"`
}

type uiDayKpis struct {
	Runs  int     `json:"runs"`
	Punc  int     `json:"punc"`
	Avg   float64 `json:"avg"`
	Cx    int     `json:"cx"`
	GC    int     `json:"gc"`
	Snaps int     `json:"snaps"`
}

type uiHourBar struct {
	H   int     `json:"h"`
	Avg float64 `json:"avg"`
	N   int     `json:"n"`
}

type uiLeaderRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	N     int    `json:"n"`
	P50   int    `json:"p50"`
	P90   int    `json:"p90"`
	Max   int    `json:"max"`
	Punc  int    `json:"punc"`
	Cx    int    `json:"cx"`
}

type uiTopRow struct {
	RunID  string `json:"runId"`
	Label  string `json:"label"`
	Dep    int    `json:"dep"`
	From   string `json:"from"`
	To     string `json:"to"`
	FinalD int    `json:"finalD"`
}

type dayData struct {
	Kpis        uiDayKpis     `json:"kpis"`
	Hours       []uiHourBar   `json:"hours"`
	Leaderboard []uiLeaderRow `json:"leaderboard"`
	Top         []uiTopRow    `json:"top"`

	TripsOfDay []tripFact `json:"-"`
}

type tripFact struct {
	Trip      string
	Cat       string
	Label     string
	DepMin    int
	DepFrom   string
	EndTo     string
	FinalD    int
	Cancelled bool
}

// ---------------------------------------------------------------------------
// dataUI
// ---------------------------------------------------------------------------

type dataUI struct {
	pool *pgxpool.Pool

	dayCache  *ttlCache[dayData]     // key: YYYY-MM-DD; heute TTL, historisch ∞
	stationLn *ttlCache[[]uiAltLine] // key: date|eva — Linien an einer Station

	stationsOnce sync.Once
	stations     []stationEntry // gesamt, im RAM (≈ 5.400 Zeilen)

	daySF daySingleflight // verhindert parallele computeDay-Scans pro Tag
}

func newDataUI(pool *pgxpool.Pool) *dataUI {
	d := &dataUI{
		pool:      pool,
		dayCache:  newTTLCache[dayData](10 * time.Minute),
		stationLn: newTTLCache[[]uiAltLine](10 * time.Minute),
	}
	// Tagesaggregate im Hintergrund vorwärmen, damit die erste Suche nie
	// den schweren Erstscan bezahlt (und niemand auf ihn wartet).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		anchor := dayAnchor(time.Now().UTC())
		d.dayFor(ctx, anchor, anchor.AddDate(0, 0, 1))
	}()
	return d
}

// daySingleflight: parallele Aufrufe für denselben Tag teilen EINE Berechnung
// (kein Thundering-Herd aus gestaffelten Suchanfragen).
type daySingleflight struct {
	mu    sync.Mutex
	calls map[string]*daySFCall
}

type daySFCall struct {
	done chan struct{}
	val  dayData
	ok   bool
}

func (g *daySingleflight) Do(key string, fn func() (dayData, bool)) (dayData, bool) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*daySFCall)
	}
	if c, exists := g.calls[key]; exists {
		g.mu.Unlock()
		<-c.done // auf die laufende Berechnung warten statt selbst zu scannen
		return c.val, c.ok
	}
	c := &daySFCall{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()
	c.val, c.ok = fn()
	close(c.done)
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	return c.val, c.ok
}

func registerDataUI(mux *http.ServeMux, pool *pgxpool.Pool) {
	d := newDataUI(pool)
	mux.HandleFunc("GET /api/meta", d.handleMeta)
	mux.HandleFunc("GET /api/search", d.handleSearch)
	mux.HandleFunc("GET /api/board", d.handleBoard)
	mux.HandleFunc("GET /api/journey", d.handleJourney)
	mux.HandleFunc("GET /api/line", d.handleLine)
	mux.HandleFunc("GET /api/day", d.handleDay)
}

// dayWindow: [start, end) UTC + Berlin-Mitternachts-Anker des angefragten
// Tages (Default: heute).
func (d *dataUI) dayWindow(r *http.Request) (time.Time, time.Time, time.Time) {
	if dayStr := r.URL.Query().Get("day"); dayStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", dayStr, berlinLoc); err == nil {
			return t, t.AddDate(0, 0, 1), t
		}
	}
	anchor := dayAnchor(time.Now().UTC())
	return anchor, anchor.AddDate(0, 0, 1), anchor
}

func (d *dataUI) handleMeta(w http.ResponseWriter, r *http.Request) {
	start, _, anchor := d.dayWindow(r)
	now := 1440 // historischer Tag: alles ist Vergangenheit
	if anchor.Equal(dayAnchor(time.Now().UTC())) {
		now = minutesOfDay(time.Now().UTC(), anchor)
	}
	writeJSON(w, uiMeta{
		Day: start.In(berlinLoc).Format("Mon 02.01.2006"),
		T0:  300,
		T1:  1440,
		Now: now,
	})
}

// ---------------------------------------------------------------------------
// Suche
// ---------------------------------------------------------------------------

// stationEntry: Station für die RAM-Suche (normalisierte Formen vorgebacken).
type stationEntry struct {
	Eva  string
	Name string
	Norm string // lowercase, diakritikfrei, nur [a-z0-9]
}

var umlautRepl = strings.NewReplacer(
	"ä", "a", "ö", "o", "ü", "u", "ß", "ss",
	"Ä", "a", "Ö", "o", "Ü", "u")

// normName: robuste Normalisierung für Suche — "Köln Hbf"→"kolnhbf",
// "S 6"→"s6", "RE 7"→"re7".
func normName(s string) string {
	s = strings.ToLower(umlautRepl.Replace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normInput: Normalisierungs-Varianten der EINGABE — deckt sowohl "koln" als
// auch "koeln"/"muenster" ab (zweite Variante nur, wenn sie abweicht).
func normInput(q string) []string {
	base := normName(q)
	alt := normName(strings.NewReplacer("oe", "o", "ue", "u", "ae", "a", "OE", "o", "UE", "u", "AE", "a").Replace(q))
	if alt == base {
		return []string{base}
	}
	return []string{base, alt}
}

// Zeitmuster im Query — bewusst drei getrennte Muster statt Lookahead:
//  1. „14:30“, „14.30", "14 uhr 30", "14h30"  (expliziter Trenner + Minuten)
//  2. „1430“ (vierstellig am Stück)
//  3. „14 uhr" / "7 uhr" (Stunde nur mit uhr-Suffix — „RE 7" bleibt Liniensuche)
var timeWithSep = regexp.MustCompile(`(?:^|\s)([01]?\d|2[0-3])\s*(?::|\.|h|uhr)\s*([0-5]\d)(?:$|\s|:|\.)`)
var timeAttached = regexp.MustCompile(`(?:^|\s)((?:[01]\d|2[0-3])[0-5]\d)(?:$|\s)`)
var timeHourOnly = regexp.MustCompile(`(?:^|\s)([01]?\d|2[0-3])\s*uhr(?:$|\s)`)

// parseTimeHint extrahiert die ERSTE Uhrzeit als Tagesminuten und gibt den
// Rest-Query zurück (leer, wenn nichts übrig bleibt).
func parseTimeHint(q string) (int, string, bool) {
	cut := func(m []int) (int, string) {
		num := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, q[m[0]:m[1]])
		n := 0
		for _, d := range num {
			n = n*10 + int(d-'0')
		}
		var min int
		if len(num) <= 2 { // nur Stunde
			min = n * 60
		} else { // hhmm vierstellig
			min = (n/100)*60 + n%100
		}
		return min, strings.TrimSpace(q[:m[0]] + " " + q[m[1]:])
	}
	if m := timeWithSep.FindStringIndex(q); m != nil {
		min, rest := cut(m)
		return min, rest, true
	}
	if m := timeAttached.FindStringIndex(q); m != nil {
		min, rest := cut(m)
		return min, rest, true
	}
	if m := timeHourOnly.FindStringIndex(q); m != nil {
		min, rest := cut(m)
		return min, rest, true
	}
	return 0, q, false
}

// matchScore: 0 = exakt, 1 = Präfix, 2 = enthält, -1 = kein Treffer.
func matchScore(norm string, variants []string) int {
	best := -1
	for _, v := range variants {
		if v == "" {
			continue
		}
		if norm == v {
			return 0
		}
		if strings.HasPrefix(norm, v) {
			if best == -1 || 1 < best {
				best = 1
			}
		} else if strings.Contains(norm, v) && (best == -1 || 2 < best) {
			best = 2
		}
	}
	return best
}

func (d *dataUI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	// Uhrzeit im Query? („porz 14:30")
	out := uiSearchResults{Stations: []uiSearchStation{}, Lines: []uiSearchLine{}}
	var stationQ string
	if t, rest, ok := parseTimeHint(q); ok {
		tt := t
		out.Time = &tt
		q = rest
	}
	stationQ = strings.TrimSpace(q)
	if stationQ == "" {
		writeJSON(w, out)
		return
	}

	// Stationen aus dem RAM-Index (fuzzy über normalisierte Formen).
	d.ensureStations(r)
	variants := normInput(stationQ)
	type hit struct {
		e     stationEntry
		score int
	}
	hits := []hit{}
	for _, e := range d.stations {
		if s := matchScore(e.Norm, variants); s >= 0 {
			hits = append(hits, hit{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return len(hits[i].e.Name) < len(hits[j].e.Name) // kürzer = konkreter
	})
	if len(hits) > 7 {
		hits = hits[:7]
	}
	for _, h := range hits {
		out.Stations = append(out.Stations, uiSearchStation{Name: h.e.Name, N: 0})
	}

	// Linien: aus den Tagesfakten — NUR aus dem Cache, die Suche stößt nie
	// eine Tagesberechnung an (Warm-up beim Start füllt den Cache).
	if day, ok := d.cachedDay(r); ok {
		type entry struct {
			cat, label, from, to string
			n, score             int
		}
		byKey := map[string]*entry{}
		for _, tf := range day.TripsOfDay {
			key := lineKeyOf(tf.Cat, tf.Label)
			e := byKey[key]
			if e == nil {
				e = &entry{cat: tf.Cat, label: tf.Label, from: tf.DepFrom, to: tf.EndTo}
				byKey[key] = e
			}
			e.n++
		}
		for _, e := range byKey {
			label := lineLabelOf(e.cat, e.label)
			normCat := normName(e.cat) + normName(e.label) // "s"+"6" → "s6"
			score := matchScore(normCat, variants)
			if normName(e.label) != "" { // auch "6" allein matchen („6" zeigt S 6, RE 6 …)
				if s2 := matchScore(normName(e.label), variants); s2 >= 0 && (score < 0 || s2+1 < score) {
					score = s2 + 1
				}
			}
			if score < 0 {
				continue
			}
			out.Lines = append(out.Lines, uiSearchLine{
				Key: lineKeyOf(e.cat, e.label), Label: label, Cat: e.cat,
				Ctx: e.from + " ⇢ " + e.to, N: e.n,
			})
		}
		sort.SliceStable(out.Lines, func(i, j int) bool { return out.Lines[i].Label < out.Lines[j].Label })
		if len(out.Lines) > 6 {
			out.Lines = out.Lines[:6]
		}
	}

	// Starker Bahnhofs-Treffer? → Linien, die dort halten (Sektion 3).
	if len(out.Stations) > 0 {
		out.StationName = out.Stations[0].Name
		if alt, _ := d.altLinesFor(r, uiLineInfo{}, out.StationName); len(alt) > 0 {
			out.StationLines = []uiSearchLine{}
			for _, l := range alt {
				if len(out.StationLines) >= 6 {
					break
				}
				out.StationLines = append(out.StationLines, uiSearchLine{
					Key: l.Key, Label: l.Label, Cat: l.Cat, N: 0,
					Ctx: "hält hier",
				})
			}
		}
	}

	writeJSON(w, out)
}

// ensureStations lädt den Stations-Index einmalig in den RAM.
func (d *dataUI) ensureStations(r *http.Request) {
	d.stationsOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := d.pool.Query(ctx, `SELECT eva, name FROM stations ORDER BY name`)
		if err != nil {
			log.Printf("[dataui] stations load: %v", err)
			return
		}
		defer rows.Close()
		list := []stationEntry{}
		for rows.Next() {
			var e stationEntry
			if err := rows.Scan(&e.Eva, &e.Name); err == nil {
				e.Norm = normName(e.Name)
				list = append(list, e)
			}
		}
		d.stations = list
		log.Printf("[dataui] stations index: %d entries", len(list))
	})
}

// ---------------------------------------------------------------------------
// Bahnhofstafel
// ---------------------------------------------------------------------------

func (d *dataUI) handleBoard(w http.ResponseWriter, r *http.Request) {
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	atMin := 877
	if v := r.URL.Query().Get("at"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			atMin = n
		}
	}
	if station == "" {
		writeError(w, 400, "station required")
		return
	}

	_, _, anchor := d.dayWindow(r)
	at := anchor.Add(time.Duration(atMin) * time.Minute)
	lo := at.Add(-150 * time.Minute)
	hi := at.Add(240 * time.Minute)

	// Abfahrten im Planfenster; pro Halt der letzte Stand ≤ Wissensstand.
	// Nutzt idx (station_eva, direction, planned_time) + Dedup-Sort über die
	// wenigen Fensterevents.
	rows, err := d.pool.Query(r.Context(), `
		SELECT DISTINCT ON (se.stop_id)
		       se.stop_id, st.name, se.line_category, `+labelExpr+`,
		       COALESCE(se.direction_name, ''), se.via_path,
		       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
		       COALESCE(se.platform, ''), COALESCE(se.planned_platform, '')
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.station_eva = (SELECT eva FROM stations WHERE name = $1 LIMIT 1)
		  AND se.direction = 'departure'
		  AND se.planned_time BETWEEN $2 AND $3
		  AND se.scraped_at <= $4
		ORDER BY se.stop_id, se.scraped_at DESC`, station, lo, hi, at)
	if err != nil {
		writeError(w, 500, "board query: "+err.Error())
		return
	}
	defer rows.Close()

	raw := []rawRow{}
	for rows.Next() {
		row, err := scanRaw(rows)
		if err != nil {
			writeError(w, 500, "board scan: "+err.Error())
			return
		}
		raw = append(raw, row)
	}

	writeJSON(w, buildBoard(raw, anchor))
}

// buildBoard: reine Verdichtung (getestet).
func buildBoard(raw []rawRow, anchor time.Time) uiBoardData {
	sort.Slice(raw, func(i, j int) bool { return raw[i].Planned.Before(raw[j].Planned) })
	out := uiBoardData{Rows: []uiBoardRow{}, Lines: []uiLineInfo{}}
	seenLines := map[string]bool{}
	for _, s := range raw {
		row := uiBoardRow{
			RunID:     tripKey(s.StopID),
			StopIdx:   stopIdxOf(s.StopID),
			LineKey:   lineKeyOf(s.Cat, s.Label),
			LineLabel: lineLabelOf(s.Cat, s.Label),
			Planned:   minutesOfDay(s.Planned, anchor),
			Plat:      s.Platform,
			PlatFrom:  s.PlatFrom,
			Dest:      s.Dest,
			Via:       strings.Join(firstN(s.Via, 2), " – "),
			Cancelled: s.Cancelled,
		}
		row.Known = s.Actual != nil || s.Cancelled
		if s.Actual != nil {
			delay := minutesOfDay(*s.Actual, anchor) - row.Planned
			row.Delay = &delay
			row.Ist = !s.Scraped.Before(*s.Actual)
		}
		row.PlatChanged = row.Plat != "" && row.PlatFrom != "" && row.Plat != row.PlatFrom
		out.Rows = append(out.Rows, row)

		if !seenLines[row.LineKey] {
			seenLines[row.LineKey] = true
			out.Lines = append(out.Lines, uiLineInfo{Key: row.LineKey, Label: row.LineLabel, Cat: s.Cat})
		}
	}
	return out
}

// scanRaw liest eine Row im Standardfeld-Layout der Data-UI-Queries.
func scanRaw(rows interface {
	Scan(dest ...any) error
}) (rawRow, error) {
	var s rawRow
	var via []string
	var dest string
	err := rows.Scan(&s.StopID, &s.Name, &s.Cat, &s.Label, &dest, &via,
		&s.Planned, &s.Actual, &s.Cancelled, &s.Scraped, &s.Platform, &s.PlatFrom)
	s.Dest = dest
	s.Via = via
	return s, err
}

// ---------------------------------------------------------------------------
// Zuglauf
// ---------------------------------------------------------------------------

func (d *dataUI) handleJourney(w http.ResponseWriter, r *http.Request) {
	run := strings.TrimSpace(r.URL.Query().Get("run"))
	if run == "" {
		writeError(w, 400, "run required")
		return
	}
	station := strings.TrimSpace(r.URL.Query().Get("station"))

	// Bereichssuche: [run||'-', run||'.') deckt exakt run+'-'+Ziffern ab
	// (Byte-Reihenfolge '-' < '.' < '0'..'9').
	rows, err := d.pool.Query(r.Context(), `
		SELECT se.stop_id, se.direction, se.station_eva, st.name,
		       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
		       COALESCE(se.platform, ''), COALESCE(se.planned_platform, ''),
		       se.line_category, `+labelExpr+`,
		       se.via_path, COALESCE(se.direction_name, '')
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.stop_id >= $1 || '-'
		  AND se.stop_id <  $1 || '.'
		ORDER BY se.stop_id, se.scraped_at`, run)
	if err != nil {
		writeError(w, 500, "journey query: "+err.Error())
		return
	}
	defer rows.Close()

	raw := []rawRow{}
	for rows.Next() {
		row, err := scanJourney(rows)
		if err != nil {
			writeError(w, 500, "journey scan: "+err.Error())
			return
		}
		raw = append(raw, row)
	}

	runOut, line, ok := buildJourney(run, raw)
	if !ok {
		writeError(w, 404, "run not found")
		return
	}

	alt, scoped := d.altLinesFor(r, line, station)
	writeJSON(w, uiJourneyData{Run: runOut, Line: line, AltLines: alt, AltScoped: scoped})
}

// scanJourney: Layout der Journey-Query (Richtung vor Name).
func scanJourney(rows interface {
	Scan(dest ...any) error
}) (rawRow, error) {
	var s rawRow
	var via []string
	err := rows.Scan(&s.StopID, &s.Direction, &s.Eva, &s.Name,
		&s.Planned, &s.Actual, &s.Cancelled, &s.Scraped,
		&s.Platform, &s.PlatFrom, &s.Cat, &s.Label, &via, &s.Dest)
	s.Via = via
	return s, err
}

// buildJourney: reine Verdichtung aller Rows einer Fahrt (getestet).
func buildJourney(runID string, raw []rawRow) (uiRun, uiLineInfo, bool) {
	if len(raw) == 0 {
		return uiRun{}, uiLineInfo{}, false
	}

	// Halte gruppieren (stop_id), Abfahrt vor Ankunft.
	deps := map[string][]rawRow{}
	arrs := map[string][]rawRow{}
	for _, s := range raw {
		if s.Direction == "departure" {
			deps[s.StopID] = append(deps[s.StopID], s)
		} else {
			arrs[s.StopID] = append(arrs[s.StopID], s)
		}
	}

	stops := make([]uiStop, 0, len(deps)+len(arrs))
	seen := map[string]bool{}
	for _, s := range raw {
		if seen[s.StopID] {
			continue
		}
		seen[s.StopID] = true
		list := deps[s.StopID]
		if len(list) == 0 {
			list = arrs[s.StopID]
		}
		if len(list) == 0 {
			continue
		}
		stops = append(stops, buildStop(s.StopID, list))
	}
	sort.Slice(stops, func(i, j int) bool { return stops[i].Idx < stops[j].Idx })

	// Anchor = Berlin-Mitternacht des Tages, an dem die Fahrt STARTET
	// (konsistent über Mitternacht hinweg).
	anchor := dayAnchor(stops[0].plannedTS)
	for i := range stops {
		s := &stops[i]
		s.Planned = minutesOfDay(s.plannedTS, anchor)
		s.Actual = s.Planned + s.FinalD
		for j := range s.Snaps {
			s.Snaps[j].At = minutesOfDay(s.Snaps[j].atTS, anchor)
		}
	}

	// Linien-Metadaten aus der ersten Row mit Kategorie.
	var line uiLineInfo
	for _, s := range raw {
		if s.Cat != "" {
			ctx := strings.Join(firstN(s.Via, 2), " – ")
			if s.Dest != "" {
				ctx = strings.TrimSpace(strings.TrimSuffix(ctx, " – "+firstOf(s.Via, s.Dest)))
			}
			line = uiLineInfo{
				Key:   lineKeyOf(s.Cat, s.Label),
				Label: lineLabelOf(s.Cat, s.Label),
				Cat:   s.Cat,
				Ctx:   ctx,
			}
			break
		}
	}

	out := uiRun{
		ID:        runID,
		LineKey:   line.Key,
		Dep:       stops[0].Planned,
		Cancelled: allCancelled(stops),
		Stops:     stops,
	}
	return out, line, true
}

func firstOf(v []string, fallback string) string {
	if len(v) > 0 {
		return v[0]
	}
	return fallback
}

func allCancelled(stops []uiStop) bool {
	for _, s := range stops {
		if !s.Cancelled {
			return false
		}
	}
	return len(stops) > 0
}

// buildStop: reine Verdichtung einer Snapshot-Reihe (getestet).
func buildStop(stopID string, list []rawRow) uiStop {
	sort.Slice(list, func(i, j int) bool { return list[i].Scraped.Before(list[j].Scraped) })
	st := uiStop{
		Idx:       stopIdxOf(stopID),
		Name:      list[0].Name,
		Eva:       list[0].Eva,
		PlatFrom:  list[0].PlatFrom,
		PlatKnown: 1 << 30,
		Snaps:     make([]uiSnap, 0, len(list)),
	}
	if st.PlatFrom == "" {
		st.PlatFrom = list[0].Platform
	}
	lastPlat := st.PlatFrom
	for i, s := range list {
		snap := uiSnap{
			Ist:       s.Actual != nil && !s.Scraped.Before(*s.Actual),
			Cancelled: s.Cancelled,
			Plat:      s.Platform,
			atTS:      s.Scraped,
		}
		if snap.Plat == "" {
			snap.Plat = s.PlatFrom
		}
		if s.Actual != nil {
			d := int(s.Actual.Sub(s.Planned) / time.Minute)
			snap.Prog = &d
			st.FinalD = d
			st.Cancelled = false
		}
		if !s.Cancelled {
			st.Cancelled = false
		}
		if snap.Plat != lastPlat {
			if st.PlatKnown == 1<<30 {
				st.PlatKnown = i
			}
			lastPlat = snap.Plat
		}
		st.Snaps = append(st.Snaps, snap)
	}
	// Entfall = Zustand des LETZTEN Snapshots (Ummeldungen zurück sind möglich),
	// aber eine Ist-Zeit übersteuert immer.
	st.Cancelled = st.Snaps[len(st.Snaps)-1].Cancelled
	st.PlatTo = lastPlat
	st.plannedTS = list[0].Planned
	return st
}

// ---------------------------------------------------------------------------
// Linie
// ---------------------------------------------------------------------------

func (d *dataUI) handleLine(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("line"))
	cat, label := splitLineKey(key)
	if cat == "" {
		writeError(w, 400, "line required (CAT|LABEL)")
		return
	}
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	start, end, anchor := d.dayWindow(r)

	// Stop-Ebene der Linie: letzte Snapshot je Halt (Index: line_category +
	// scraped_at; Label-Filter danach in Go, da label ggf. Fallback ist).
	rows, err := d.pool.Query(r.Context(), `
		SELECT DISTINCT ON (se.stop_id)
		       se.stop_id, se.direction, se.station_eva, st.name,
		       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
		       '' AS platform, '' AS planned_platform,
		       se.line_category, `+labelExpr+`,
		       se.via_path, COALESCE(se.direction_name, '')
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.scraped_at >= $1 AND se.scraped_at < $2
		  AND se.line_category = $3
		ORDER BY se.stop_id, se.scraped_at DESC`, start, end, cat)
	if err != nil {
		writeError(w, 500, "line query: "+err.Error())
		return
	}
	defer rows.Close()

	raw := []rawRow{}
	for rows.Next() {
		row, err := scanJourney(rows)
		if err != nil {
			writeError(w, 500, "line scan: "+err.Error())
			return
		}
		if row.Label == label {
			raw = append(raw, row)
		}
	}

	// Station-Scope: nur Fahrten, die diese Station berühren.
	if station != "" {
		raw = filterByStation(raw, station)
	}

	out := buildLine(raw, anchor, key, cat, label)
	if len(out.Runs) == 0 {
		writeError(w, 404, "line not found on this day")
		return
	}

	out.AltLines, out.AltScoped = d.altLinesFor(r, out.Line, station)
	writeJSON(w, out)
}

// filterByStation behält nur Rows von Fahrten, die an der Station halten.
func filterByStation(raw []rawRow, station string) []rawRow {
	trips := map[string]bool{}
	for _, s := range raw {
		if s.Name == station {
			trips[tripKey(s.StopID)] = true
		}
	}
	out := []rawRow{}
	for _, s := range raw {
		if trips[tripKey(s.StopID)] {
			out = append(out, s)
		}
	}
	return out
}

// buildLine: reine Verdichtung (getestet).
func buildLine(raw []rawRow, anchor time.Time, key, cat, label string) uiLineData {
	// pro Fahrt: Halte sortieren, Endverspätung vom letzten Stop.
	type trip struct {
		id      string
		stops   []uiLineRunStop
		idxs    []int
		cxLast  bool
		firstPt time.Time
	}
	byTrip := map[string]*trip{}
	var order []string
	for _, s := range raw {
		tk := tripKey(s.StopID)
		t := byTrip[tk]
		if t == nil {
			t = &trip{id: tk}
			byTrip[tk] = t
			order = append(order, tk)
		}
		finalD := 0
		if s.Actual != nil {
			finalD = int(s.Actual.Sub(s.Planned) / time.Minute)
		}
		t.stops = append(t.stops, uiLineRunStop{Name: s.Name, FinalD: finalD, Cancelled: s.Cancelled})
		t.idxs = append(t.idxs, stopIdxOf(s.StopID))
		if t.firstPt.IsZero() {
			t.firstPt = s.Planned
		}
	}

	runs := []uiLineRun{}
	for _, tk := range order {
		t := byTrip[tk]
		// nach stop_idx sortieren (Index-Komponente der stop_id)
		idx := make([]int, len(t.stops))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return t.idxs[idx[a]] < t.idxs[idx[b]] })
		sorted := make([]uiLineRunStop, len(idx))
		for i, ix := range idx {
			sorted[i] = t.stops[ix]
		}
		// Endverspätung: letzter Stop mit Ist-Wert.
		finalD := 0
		for i := len(sorted) - 1; i >= 0; i-- {
			if sorted[i].FinalD > 0 {
				finalD = sorted[i].FinalD
				break
			}
		}
		dep := 0
		if len(sorted) > 0 {
			dep = minutesOfDay(t.firstPt, anchor)
		}
		cancelled := len(sorted) > 0 && sorted[len(sorted)-1].Cancelled
		runs = append(runs, uiLineRun{ID: tk, Dep: dep, Cancelled: cancelled, FinalD: finalD, Stops: sorted})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Dep < runs[j].Dep })

	fin := []int{}
	cx := 0
	for _, run := range runs {
		if run.Cancelled {
			cx++
		} else {
			fin = append(fin, run.FinalD)
		}
	}

	out := uiLineData{
		Line: uiLineInfo{Key: key, Label: lineLabelOf(cat, label), Cat: cat},
		Runs: runs,
		Stats: uiLineStats{
			N:    len(runs),
			Cx:   cx,
			P50:  percentile(fin, 50),
			P90:  percentile(fin, 90),
			Max:  maxInt(fin),
			Punc: puncOf(fin),
		},
		AltLines: []uiAltLine{},
	}

	// Kontext (Disambiguierung) aus der ersten Fahrt.
	if len(runs) > 0 && len(runs[0].Stops) > 1 {
		s := runs[0].Stops
		mid := s[len(s)/2]
		last := s[len(s)-1]
		out.Ctx = mid.Name + " ⇢ " + last.Name
		out.Line.Ctx = out.Ctx
	}
	return out
}

// ---------------------------------------------------------------------------
// Tages-Übersicht
// ---------------------------------------------------------------------------

func (d *dataUI) handleDay(w http.ResponseWriter, r *http.Request) {
	day, ok := d.loadDay(r, true)
	if !ok {
		writeError(w, 500, "day aggregation failed")
		return
	}
	writeJSON(w, day)
}

// loadDay berechnet (und cacht) die Tagesaggregate — mit Singleflight.
func (d *dataUI) loadDay(r *http.Request, required bool) (dayData, bool) {
	start, end, _ := d.dayWindow(r)
	key := start.Format("2006-01-02")
	now := time.Now().UTC()

	if !start.Equal(dayAnchor(now)) {
		if v, ok := d.dayCache.get(key, now); ok {
			return v, true
		}
	}

	v, ok := d.daySF.Do(key, func() (dayData, bool) {
		day, err := d.computeDay(r.Context(), start, end)
		if err != nil {
			log.Printf("[dataui] computeDay %s: %v", key, err)
			return dayData{}, false
		}
		d.dayCache.put(key, day, now)
		return day, true
	})
	if !ok && required {
		log.Printf("[dataui] day %s unavailable", key)
	}
	return v, ok
}

// cachedDay: nur Cache-Lookup, NIE eine Berechnung anstoßen (für die Suche).
func (d *dataUI) cachedDay(r *http.Request) (dayData, bool) {
	start, _, _ := d.dayWindow(r)
	return d.dayCache.get(start.Format("2006-01-02"), time.Now().UTC())
}

// dayFor: Berechnung mit eigenem Kontext (Warm-up beim Start).
func (d *dataUI) dayFor(ctx context.Context, start, end time.Time) (dayData, bool) {
	key := start.Format("2006-01-02")
	now := time.Now().UTC()
	if v, ok := d.dayCache.get(key, now); ok {
		return v, true
	}
	v, ok := d.daySF.Do(key, func() (dayData, bool) {
		day, err := d.computeDay(ctx, start, end)
		if err != nil {
			log.Printf("[dataui] warmup computeDay %s: %v", key, err)
			return dayData{}, false
		}
		d.dayCache.put(key, day, now)
		return day, true
	})
	return v, ok
}

// computeDay: letzte Snapshots je Halt → pro Fahrt Start und Ende.
func (d *dataUI) computeDay(ctx context.Context, start, end time.Time) (dayData, error) {
	anchor := start

	rows, err := d.pool.Query(ctx, `
		WITH day_last AS (
			SELECT DISTINCT ON (se.stop_id)
			       se.stop_id, se.direction,
			       se.planned_time, se.actual_time, se.cancelled,
			       se.line_category,
			       `+labelExpr+` AS line_label,
			       st.name AS station_name
			FROM stop_events se
			JOIN stations st ON st.eva = se.station_eva
			WHERE se.scraped_at >= $1 AND se.scraped_at < $2
			  AND se.line_category <> '' -- fchg-Delta-Rows ohne tl ausschließen
			ORDER BY se.stop_id, se.scraped_at DESC
		), numbered AS (
			SELECT *,
			       regexp_replace(stop_id, '-[0-9]+$', '') AS trip,
			       split_part(stop_id, '-', 3)::int AS stop_idx
			FROM day_last
		), first AS (
			SELECT DISTINCT ON (trip) trip, cat, label, dep_min, from_name, cancelled
			FROM (
				SELECT trip, line_category AS cat, line_label AS label,
				       EXTRACT(EPOCH FROM (planned_time - $1))::int / 60 AS dep_min,
				       station_name AS from_name, cancelled, stop_idx
				FROM numbered WHERE direction = 'departure'
			) f
			ORDER BY trip, stop_idx
		), last AS (
			SELECT DISTINCT ON (trip) trip, to_name, delay, cancelled
			FROM (
				SELECT trip, station_name AS to_name,
				       CASE WHEN actual_time IS NULL THEN NULL
				            ELSE EXTRACT(EPOCH FROM (actual_time - planned_time))::int / 60 END AS delay,
				       cancelled, stop_idx
				FROM numbered
			) l
			ORDER BY trip, stop_idx DESC
		)
		SELECT f.trip, f.cat, f.label, f.dep_min, f.from_name,
		       l.to_name, COALESCE(l.delay, 0)::int, (f.cancelled AND l.cancelled)
		FROM first f JOIN last l USING (trip)`, start)
	if err != nil {
		return dayData{}, err
	}
	defer rows.Close()

	day := dayData{
		TripsOfDay:  []tripFact{},
		Hours:       []uiHourBar{},
		Leaderboard: []uiLeaderRow{},
		Top:         []uiTopRow{},
	}
	hours := map[int][]int{}
	perLine := map[string][]int{}
	lineMeta := map[string]*uiLeaderRow{}

	for rows.Next() {
		var tf tripFact
		var depMin int
		var bothCx bool
		if err := rows.Scan(&tf.Trip, &tf.Cat, &tf.Label, &depMin, &tf.DepFrom, &tf.EndTo, &tf.FinalD, &bothCx); err != nil {
			return dayData{}, err
		}
		tf.Cancelled = bothCx
		tf.DepMin = depMin
		day.TripsOfDay = append(day.TripsOfDay, tf)
		if tf.Cancelled {
			continue
		}
		h := ((depMin % 1440) + 1440) % 1440 / 60
		hours[h] = append(hours[h], tf.FinalD)

		key := lineKeyOf(tf.Cat, tf.Label)
		perLine[key] = append(perLine[key], tf.FinalD)
		if lineMeta[key] == nil {
			lineMeta[key] = &uiLeaderRow{Key: key, Label: lineLabelOf(tf.Cat, tf.Label)}
		}
	}
	for _, tf := range day.TripsOfDay {
		key := lineKeyOf(tf.Cat, tf.Label)
		if lineMeta[key] == nil {
			lineMeta[key] = &uiLeaderRow{Key: key, Label: lineLabelOf(tf.Cat, tf.Label)}
		}
		lineMeta[key].N++
		if tf.Cancelled {
			lineMeta[key].Cx++
		}
	}

	all := []int{}
	for _, v := range hours {
		all = append(all, v...)
	}
	day.Kpis = uiDayKpis{
		Runs:  len(day.TripsOfDay),
		Punc:  puncOf(all),
		Avg:   avgOf(all),
		Cx:    countCx(day.TripsOfDay),
		Snaps: d.countSnaps(ctx, start, end),
	}

	hs := make([]int, 0, len(hours))
	for h := range hours {
		hs = append(hs, h)
	}
	sort.Ints(hs)
	for _, h := range hs {
		day.Hours = append(day.Hours, uiHourBar{H: h, Avg: avgOf(hours[h]), N: len(hours[h])})
	}

	for key, fin := range perLine {
		e := lineMeta[key]
		e.P50, e.P90, e.Max = percentile(fin, 50), percentile(fin, 90), maxInt(fin)
		e.Punc = puncOf(fin)
		day.Leaderboard = append(day.Leaderboard, *e)
	}
	sort.Slice(day.Leaderboard, func(i, j int) bool { return day.Leaderboard[i].P90 > day.Leaderboard[j].P90 })

	top := make([]tripFact, 0)
	for _, tf := range day.TripsOfDay {
		if !tf.Cancelled {
			top = append(top, tf)
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].FinalD > top[j].FinalD })
	for i, tf := range top {
		if i >= 8 {
			break
		}
		day.Top = append(day.Top, uiTopRow{
			RunID: tf.Trip, Label: lineLabelOf(tf.Cat, tf.Label),
			Dep: tf.DepMin, From: tf.DepFrom, To: tf.EndTo, FinalD: tf.FinalD,
		})
	}

	_ = anchor
	return day, nil
}

func (d *dataUI) countSnaps(ctx context.Context, start, end time.Time) int {
	var n int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM stop_events WHERE scraped_at >= $1 AND scraped_at < $2`,
		start, end).Scan(&n); err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// Schnellauswahl: Linien an einer Station (gecacht pro Tag+Station)
// ---------------------------------------------------------------------------

func (d *dataUI) altLinesFor(r *http.Request, line uiLineInfo, station string) ([]uiAltLine, bool) {
	if station == "" {
		return []uiAltLine{}, false
	}
	start, end, anchor := d.dayWindow(r)
	now := time.Now().UTC()
	key := start.Format("2006-01-02") + "|" + station

	var lines []uiAltLine
	if v, ok := d.stationLn.get(key, now); ok {
		lines = v
	} else {
		rows, err := d.pool.Query(r.Context(), `
			SELECT DISTINCT ON (se.line_category, `+labelExpr+`)
			       se.line_category, `+labelExpr+`,
			       regexp_replace(se.stop_id, '-[0-9]+$', '') AS trip,
			       EXTRACT(EPOCH FROM (se.planned_time - $2))::int / 60 AS dep_min
			FROM stop_events se
			WHERE se.station_eva = (SELECT eva FROM stations WHERE name = $1 LIMIT 1)
			  AND se.scraped_at >= $3 AND se.scraped_at < $4
			  AND se.direction = 'departure'
			  AND se.line_category <> '' -- fchg-Delta-Rows ohne tl ausschließen
			ORDER BY se.line_category, `+labelExpr+`, ABS(EXTRACT(EPOCH FROM (se.planned_time - $2))::int)`, station, anchor, start, end)
		if err != nil {
			log.Printf("[dataui] altLines: %v", err)
			return []uiAltLine{}, false
		}
		defer rows.Close()
		lines = []uiAltLine{}
		for rows.Next() {
			var cat, label, trip string
			var depMin int
			if rows.Scan(&cat, &label, &trip, &depMin) == nil {
				lines = append(lines, uiAltLine{
					Key: lineKeyOf(cat, label), Label: lineLabelOf(cat, label),
					Cat: cat, RunID: trip, Dep: depMin,
				})
			}
		}
		d.stationLn.put(key, lines, now)
	}

	scoped := false
	for _, l := range lines {
		if l.Key == line.Key {
			scoped = true
			break
		}
	}
	return lines, scoped
}

// ---------------------------------------------------------------------------
// kleine Helfer
// ---------------------------------------------------------------------------

func firstN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

func splitLineKey(key string) (cat, label string) {
	i := strings.Index(key, "|")
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}

func percentile(arr []int, p int) int {
	if len(arr) == 0 {
		return 0
	}
	s := append([]int{}, arr...)
	sort.Ints(s)
	return s[min(len(s)-1, (p*len(s))/100)]
}

func maxInt(arr []int) int {
	m := 0
	for _, v := range arr {
		if v > m {
			m = v
		}
	}
	return m
}

func puncOf(arr []int) int {
	if len(arr) == 0 {
		return 0
	}
	n := 0
	for _, v := range arr {
		if v <= 5 {
			n++
		}
	}
	return 100 * n / len(arr)
}

func avgOf(arr []int) float64 {
	if len(arr) == 0 {
		return 0
	}
	s := 0
	for _, v := range arr {
		s += v
	}
	return float64(s) / float64(len(arr))
}

func countCx(t []tripFact) int {
	n := 0
	for _, tf := range t {
		if tf.Cancelled {
			n++
		}
	}
	return n
}

// JSON-Codierung von dayData ohne interne Felder:
func (d dayData) MarshalJSON() ([]byte, error) {
	type alias struct {
		Kpis        uiDayKpis     `json:"kpis"`
		Hours       []uiHourBar   `json:"hours"`
		Leaderboard []uiLeaderRow `json:"leaderboard"`
		Top         []uiTopRow    `json:"top"`
	}
	return json.Marshal(alias{Kpis: d.Kpis, Hours: d.Hours, Leaderboard: d.Leaderboard, Top: d.Top})
}
