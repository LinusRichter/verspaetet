package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type stationRow struct {
	Eva          string  `json:"eva"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Category     *int    `json:"category"`
	Lat          *float64 `json:"lat"`
	Lon          *float64 `json:"lon"`
	FederalState *string `json:"federal_state"`
	StopEvents   int     `json:"stop_events"`
}

type lineRow struct {
	LineCategory string `json:"line_category"`
	TrainNumber  string `json:"train_number"`
	StopEvents   int    `json:"stop_events"`
	AvgDelayS    int    `json:"avg_delay_s"`
}

type eventRow struct {
	ID            int64      `json:"id"`
	Direction     string     `json:"direction"`
	LineCategory  string     `json:"line_category"`
	TrainNumber   string     `json:"train_number"`
	Owner         *string    `json:"owner"`
	StopID        string     `json:"stop_id"`
	DirectionName *string    `json:"direction_name"`
	ViaPath       []string   `json:"via_path"`
	PlannedTime   time.Time  `json:"planned_time"`
	ActualTime    *time.Time `json:"actual_time"`
	DelayS        *int       `json:"delay_s"`
	Platform      *string    `json:"platform"`
	PlannedPlatform *string  `json:"planned_platform"`
	Cancelled     bool       `json:"cancelled"`
	ScrapedAt     time.Time  `json:"scraped_at"`
	StationName   *string    `json:"station_name"`
}

type topDelayRow struct {
	StationName   string     `json:"station_name"`
	LineCategory  string     `json:"line_category"`
	TrainNumber   *string    `json:"train_number"`
	DirectionName *string    `json:"direction_name"`
	PlannedTime   time.Time  `json:"planned_time"`
	ActualTime    *time.Time `json:"actual_time"`
	DelayS        int        `json:"delay_s"`
	ScrapedAt     time.Time  `json:"scraped_at"`
}

type routeRow struct {
	LineCategory  string   `json:"line_category"`
	TrainNumber   string   `json:"train_number"`
	DirectionName *string  `json:"direction_name"`
	ViaPath       []string `json:"via_path"`
	StopEvents    int      `json:"stop_events"`
	AvgDelayS     int      `json:"avg_delay_s"`
}

type tripStopRow struct {
	StationEva      string     `json:"station_eva"`
	StationName     string     `json:"station_name"`
	LineCategory    string     `json:"line_category"`
	TrainNumber     string     `json:"train_number"`
	PlannedTime     time.Time  `json:"planned_time"`
	ActualTime      *time.Time `json:"actual_time"`
	DelayS          *int       `json:"delay_s"`
	Platform        *string    `json:"platform"`
	PlannedPlatform *string    `json:"planned_platform"`
	ScrapedAt       time.Time  `json:"scraped_at"`
}

type statsRow struct {
	Stations   int `json:"stations"`
	StopEvents int `json:"stop_events"`
	Delayed    int `json:"delayed"`
	AvgDelayS  int `json:"avg_delay_s"`
	MaxDelayS  int `json:"max_delay_s"`
}

type healthRow struct {
	RecentRuns    int     `json:"recent_runs"`
	RecentEvents  int     `json:"recent_events"`
	LastScrapeAgo int     `json:"last_scrape_ago_s"`
	ExpectedRuns  int     `json:"expected_runs"`
	FetchRate     float64 `json:"fetch_rate"`
}

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stations", func(w http.ResponseWriter, r *http.Request) {
		handleStations(w, r, pool)
	})
	mux.HandleFunc("GET /api/stations/{slug}/lines", func(w http.ResponseWriter, r *http.Request) {
		handleLines(w, r, pool, r.PathValue("slug"))
	})
	mux.HandleFunc("GET /api/stations/{slug}/lines/{lineLabel}/events", func(w http.ResponseWriter, r *http.Request) {
		handleEvents(w, r, pool, r.PathValue("slug"), r.PathValue("lineLabel"))
	})
	mux.HandleFunc("GET /api/stations/{slug}/routes", func(w http.ResponseWriter, r *http.Request) {
		handleRoutes(w, r, pool, r.PathValue("slug"))
	})
	mux.HandleFunc("GET /api/trips/{stopId}/stops", func(w http.ResponseWriter, r *http.Request) {
		handleTripStops(w, r, pool, r.PathValue("stopId"))
	})
	mux.HandleFunc("GET /api/delays/top", func(w http.ResponseWriter, r *http.Request) {
		handleTopDelays(w, r, pool)
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		handleStats(w, r, pool)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handleHealth(w, r, pool)
	})

	// Serve React static files if web/dist exists.
	if _, err := os.Stat("web/dist"); err == nil {
		mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	}

	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("API server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalln("Server error:", err)
	}
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, `{"error":"encode failed"}`, http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handleStations(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	rows, err := pool.Query(r.Context(), `
		SELECT s.eva, s.slug, s.name, s.category, s.lat, s.lon, s.federal_state,
		       count(se.id) AS stop_events
		FROM stations s
		LEFT JOIN stop_events se ON se.station_eva = s.eva
		GROUP BY s.eva, s.slug, s.name, s.category, s.lat, s.lon, s.federal_state
		ORDER BY s.name`)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query stations: %v", err))
		return
	}
	defer rows.Close()
	var out []stationRow
	for rows.Next() {
		var s stationRow
		if err := rows.Scan(&s.Eva, &s.Slug, &s.Name, &s.Category, &s.Lat, &s.Lon, &s.FederalState, &s.StopEvents); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, s)
	}
	if out == nil {
		out = []stationRow{}
	}
	writeJSON(w, out)
}

func handleLines(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, slug string) {
	rows, err := pool.Query(r.Context(), `
		SELECT se.line_category, COALESCE(se.train_number, ''), count(*) AS stop_events,
		       COALESCE(avg(EXTRACT(EPOCH FROM (se.actual_time - se.planned_time)))::int, 0) AS avg_delay_s
		FROM stop_events se
		WHERE se.station_eva = (SELECT eva FROM stations WHERE slug = $1)
		GROUP BY se.line_category, se.train_number
		ORDER BY stop_events DESC`, slug)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query lines: %v", err))
		return
	}
	defer rows.Close()
	var out []lineRow
	for rows.Next() {
		var l lineRow
		if err := rows.Scan(&l.LineCategory, &l.TrainNumber, &l.StopEvents, &l.AvgDelayS); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, l)
	}
	if out == nil {
		out = []lineRow{}
	}
	writeJSON(w, out)
}

func handleEvents(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, slug, lineLabel string) {
	rows, err := pool.Query(r.Context(), `
		SELECT se.id, se.direction, se.line_category, se.train_number, se.owner,
		       se.stop_id, se.direction_name, se.via_path,
		       se.planned_time, se.actual_time,
		       EXTRACT(EPOCH FROM (se.actual_time - se.planned_time))::int AS delay_s,
		       se.platform, se.planned_platform, se.cancelled, se.scraped_at,
		       st.name AS station_name
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.station_eva = (SELECT eva FROM stations WHERE slug = $1)
		  AND se.line_category = $2
		ORDER BY se.scraped_at DESC
		LIMIT 200`, slug, lineLabel)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query events: %v", err))
		return
	}
	defer rows.Close()
	var out []eventRow
	for rows.Next() {
		var e eventRow
		if err := rows.Scan(&e.ID, &e.Direction, &e.LineCategory, &e.TrainNumber, &e.Owner,
			&e.StopID, &e.DirectionName, &e.ViaPath,
			&e.PlannedTime, &e.ActualTime, &e.DelayS,
			&e.Platform, &e.PlannedPlatform, &e.Cancelled, &e.ScrapedAt, &e.StationName); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		if e.ViaPath == nil {
			e.ViaPath = []string{}
		}
		out = append(out, e)
	}
	if out == nil {
		out = []eventRow{}
	}
	writeJSON(w, out)
}

func handleRoutes(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, slug string) {
	rows, err := pool.Query(r.Context(), `
		SELECT line_category, COALESCE(train_number,''), direction_name, via_path,
		       count(*) AS stop_events,
		       COALESCE(avg(EXTRACT(EPOCH FROM (actual_time - planned_time)))::int, 0) AS avg_delay_s
		FROM stop_events
		WHERE station_eva = (SELECT eva FROM stations WHERE slug = $1)
		GROUP BY line_category, train_number, direction_name, via_path
		ORDER BY stop_events DESC
		LIMIT 100`, slug)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query routes: %v", err))
		return
	}
	defer rows.Close()
	var out []routeRow
	for rows.Next() {
		var r2 routeRow
		if err := rows.Scan(&r2.LineCategory, &r2.TrainNumber, &r2.DirectionName, &r2.ViaPath, &r2.StopEvents, &r2.AvgDelayS); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		if r2.ViaPath == nil {
			r2.ViaPath = []string{}
		}
		out = append(out, r2)
	}
	if out == nil {
		out = []routeRow{}
	}
	writeJSON(w, out)
}

// handleTripStops reconstructs one trip's stop sequence from its stop id
// prefix (the dailyTripId part of {dailyTripId}-{YYMMddHHmm}-{index}).
func handleTripStops(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, stopIdPrefix string) {
	rows, err := pool.Query(r.Context(), `
		SELECT DISTINCT ON (se.station_eva)
		       se.station_eva, st.name, se.line_category, se.train_number,
		       se.planned_time, se.actual_time,
		       EXTRACT(EPOCH FROM (se.actual_time - se.planned_time))::int AS delay_s,
		       se.platform, se.planned_platform, se.scraped_at
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE split_part(se.stop_id, '-', 2) = split_part($1, '-', 2)
		  AND se.stop_id LIKE '%' || split_part($1, '-', 2) || '%'
		ORDER BY se.station_eva, se.scraped_at DESC`, stopIdPrefix)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query trip stops: %v", err))
		return
	}
	defer rows.Close()

	var out []tripStopRow
	for rows.Next() {
		var s tripStopRow
		if err := rows.Scan(&s.StationEva, &s.StationName, &s.LineCategory, &s.TrainNumber,
			&s.PlannedTime, &s.ActualTime, &s.DelayS,
			&s.Platform, &s.PlannedPlatform, &s.ScrapedAt); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PlannedTime.Before(out[j].PlannedTime)
	})
	if out == nil {
		out = []tripStopRow{}
	}
	writeJSON(w, out)
}

func handleTopDelays(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := pool.Query(r.Context(), `
		SELECT s.name, se.line_category, se.train_number, se.direction_name,
		       se.planned_time, se.actual_time,
		       COALESCE(EXTRACT(EPOCH FROM (se.actual_time - se.planned_time)), 0)::int AS delay_s,
		       se.scraped_at
		FROM stop_events se
		JOIN stations s ON s.eva = se.station_eva
		WHERE se.actual_time IS NOT NULL
		ORDER BY delay_s DESC
		LIMIT $1`, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query top delays: %v", err))
		return
	}
	defer rows.Close()
	var out []topDelayRow
	for rows.Next() {
		var t topDelayRow
		if err := rows.Scan(&t.StationName, &t.LineCategory, &t.TrainNumber, &t.DirectionName,
			&t.PlannedTime, &t.ActualTime, &t.DelayS, &t.ScrapedAt); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, t)
	}
	if out == nil {
		out = []topDelayRow{}
	}
	writeJSON(w, out)
}

func handleStats(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	var s statsRow
	err := pool.QueryRow(r.Context(), `
		SELECT
		  (SELECT count(*) FROM stations),
		  (SELECT count(*) FROM stop_events),
		  (SELECT count(*) FROM stop_events WHERE actual_time IS NOT NULL),
		  COALESCE((SELECT avg(EXTRACT(EPOCH FROM (actual_time - planned_time)))::int FROM stop_events WHERE actual_time IS NOT NULL), 0),
		  COALESCE((SELECT max(EXTRACT(EPOCH FROM (actual_time - planned_time)))::int FROM stop_events WHERE actual_time IS NOT NULL), 0)`).Scan(
		&s.Stations, &s.StopEvents, &s.Delayed, &s.AvgDelayS, &s.MaxDelayS)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query stats: %v", err))
		return
	}
	writeJSON(w, s)
}

func handleHealth(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	var h healthRow
	err := pool.QueryRow(r.Context(), `
		SELECT
		  (SELECT count(*) FROM scrape_runs WHERE scraped_at > NOW() - INTERVAL '10 minutes'),
		  (SELECT count(*) FROM stop_events WHERE scraped_at > NOW() - INTERVAL '10 minutes'),
		  COALESCE(EXTRACT(EPOCH FROM (NOW() - (SELECT max(scraped_at) FROM stop_events)))::int, -1),
		  (SELECT count(*) FROM stations),
		  0`).Scan(
		&h.RecentRuns, &h.RecentEvents, &h.LastScrapeAgo, &h.ExpectedRuns, &h.FetchRate)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query health: %v", err))
		return
	}
	// Expected scrape_runs in 10 min = stations / 3 (30-min cadence = 1/3 per 10 min)
	expected := h.ExpectedRuns / 3
	if expected > 0 {
		h.FetchRate = float64(h.RecentRuns) / float64(expected)
		if h.FetchRate > 1.0 {
			h.FetchRate = 1.0
		}
	}
	writeJSON(w, h)
}