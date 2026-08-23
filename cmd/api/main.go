package main

import (
	"context"
	"database/sql"
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
	Eva         string `json:"eva"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Category    *int   `json:"category"`
	StopEvents  int    `json:"stop_events"`
}

type lineRow struct {
	LineLabel    string `json:"line_label"`
	LineCategory string `json:"line_category"`
	StopEvents   int    `json:"stop_events"`
	AvgDelayS    int    `json:"avg_delay_s"`
}

type eventRow struct {
	ID              int64      `json:"id"`
	Direction       string     `json:"direction"`
	LineLabel      string     `json:"line_label"`
	LineCategory   string     `json:"line_category"`
	DirectionName   *string    `json:"direction_name"`
	DirectionSlug   *string    `json:"direction_slug"`
	ViaSlugs        []string   `json:"via_slugs"`
	TripUUID        string     `json:"trip_uuid"`
	TripID          string     `json:"trip_id"`
	TripDate        string     `json:"trip_date"`
	StationName     *string    `json:"station_name"`
	PlannedTime     time.Time  `json:"planned_time"`
	ActualTime      *time.Time `json:"actual_time"`
	DelayS          int        `json:"delay_s"`
	Platform        *string    `json:"platform"`
	PlannedPlatform *string    `json:"planned_platform"`
	Notes           *string    `json:"notes"`
	ScrapedAt       time.Time  `json:"scraped_at"`
}

type topDelayRow struct {
	StationName   string     `json:"station_name"`
	LineLabel     string     `json:"line_label"`
	DirectionName *string    `json:"direction_name"`
	PlannedTime   time.Time  `json:"planned_time"`
	ActualTime    *time.Time `json:"actual_time"`
	DelayS        int        `json:"delay_s"`
	ScrapedAt     time.Time  `json:"scraped_at"`
}

type routeRow struct {
	LineLabel     string   `json:"line_label"`
	LineCategory  string   `json:"line_category"`
	DirectionName *string  `json:"direction_name"`
	DirectionSlug *string  `json:"direction_slug"`
	ViaSlugs      []string `json:"via_slugs"`
	StopEvents    int      `json:"stop_events"`
	AvgDelayS     int      `json:"avg_delay_s"`
}

type statsRow struct {
	Stations   int `json:"stations"`
	StopEvents int `json:"stop_events"`
	Delayed    int `json:"delayed"`
	AvgDelayS  int `json:"avg_delay_s"`
	MaxDelayS  int `json:"max_delay_s"`
}

type healthRow struct {
	RecentRuns    int `json:"recent_runs"`
	RecentEvents  int `json:"recent_events"`
	LastScrapeAgo int `json:"last_scrape_ago_s"`
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
	mux.HandleFunc("GET /api/trips/{uuid}/stops", func(w http.ResponseWriter, r *http.Request) {
		handleTripStops(w, r, pool, r.PathValue("uuid"))
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
		SELECT s.eva, s.slug, s.name, s.category, count(se.id) AS stop_events
		FROM stations s
		LEFT JOIN stop_events se ON se.station_eva = s.eva
		GROUP BY s.eva, s.slug, s.name, s.category
		ORDER BY s.name`)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query stations: %v", err))
		return
	}
	defer rows.Close()
	var out []stationRow
	for rows.Next() {
		var s stationRow
		var cat sql.NullInt64
		if err := rows.Scan(&s.Eva, &s.Slug, &s.Name, &cat, &s.StopEvents); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		if cat.Valid {
			c := int(cat.Int64)
			s.Category = &c
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
		SELECT se.line_label, se.line_category, count(*) AS stop_events,
		       avg(COALESCE(EXTRACT(EPOCH FROM (se.actual_time - se.planned_time)), 0))::int AS avg_delay_s
		FROM stop_events se
		WHERE se.station_eva = (SELECT eva FROM stations WHERE slug = $1)
		GROUP BY se.line_label, se.line_category
		ORDER BY stop_events DESC`, slug)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query lines: %v", err))
		return
	}
	defer rows.Close()
	var out []lineRow
	for rows.Next() {
		var l lineRow
		if err := rows.Scan(&l.LineLabel, &l.LineCategory, &l.StopEvents, &l.AvgDelayS); err != nil {
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
		SELECT se.id, se.direction, se.line_label, se.line_category, se.direction_name, se.direction_slug,
		       se.via_slugs, se.trip_uuid, se.trip_id, to_char(se.trip_date, 'YYYY-MM-DD') AS trip_date,
		       st.name AS station_name, se.planned_time, se.actual_time,
		       COALESCE(EXTRACT(EPOCH FROM (se.actual_time - se.planned_time)), 0)::int AS delay_s,
		       se.platform, se.planned_platform, se.notes, se.scraped_at
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.station_eva = (SELECT eva FROM stations WHERE slug = $1)
		  AND se.line_label = $2
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
		if err := rows.Scan(&e.ID, &e.Direction, &e.LineLabel, &e.LineCategory, &e.DirectionName, &e.DirectionSlug,
			&e.ViaSlugs, &e.TripUUID, &e.TripID, &e.TripDate, &e.StationName,
			&e.PlannedTime, &e.ActualTime,
			&e.DelayS, &e.Platform, &e.PlannedPlatform, &e.Notes, &e.ScrapedAt); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		if e.ViaSlugs == nil {
			e.ViaSlugs = []string{}
		}
		out = append(out, e)
	}
	if out == nil {
		out = []eventRow{}
	}
	writeJSON(w, out)
}

func handleTripStops(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, uuid string) {
	date := r.URL.Query().Get("date")
	// DISTINCT ON (station_eva) keeps the LATEST observation per station (the
	// most recent scraped_at), so each stop appears once with its freshest
	// planned/actual times. Rows are then ordered by planned_time so the stop
	// sequence reads naturally.
	query := `
		SELECT DISTINCT ON (se.station_eva)
		       se.station_eva, st.slug AS station_slug, st.name AS station_name,
		       se.direction, se.line_label, se.line_category,
		       se.planned_time, se.actual_time,
		       COALESCE(EXTRACT(EPOCH FROM (se.actual_time - se.planned_time)), 0)::int AS delay_s,
		       se.platform, se.planned_platform, se.scraped_at
		FROM stop_events se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.trip_uuid = $1`
	args := []interface{}{uuid}
	if date != "" {
		query += ` AND se.trip_date = $2`
		args = append(args, date)
	}
	query += ` ORDER BY se.station_eva, se.scraped_at DESC`
	rows, err := pool.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query trip stops: %v", err))
		return
	}
	defer rows.Close()

	type stopRow struct {
		StationEva      string     `json:"station_eva"`
		StationSlug     string     `json:"station_slug"`
		StationName     string     `json:"station_name"`
		Direction       string     `json:"direction"`
		LineLabel       string     `json:"line_label"`
		LineCategory    string     `json:"line_category"`
		PlannedTime     time.Time  `json:"planned_time"`
		ActualTime      *time.Time `json:"actual_time"`
		DelayS          int        `json:"delay_s"`
		Platform        *string    `json:"platform"`
		PlannedPlatform *string    `json:"planned_platform"`
		ScrapedAt       time.Time  `json:"scraped_at"`
	}
	var out []stopRow
	for rows.Next() {
		var s stopRow
		if err := rows.Scan(&s.StationEva, &s.StationSlug, &s.StationName, &s.Direction,
			&s.LineLabel, &s.LineCategory, &s.PlannedTime, &s.ActualTime, &s.DelayS,
			&s.Platform, &s.PlannedPlatform, &s.ScrapedAt); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, s)
	}
	// Sort by planned_time so the stops read in journey order.
	sort.Slice(out, func(i, j int) bool {
		return out[i].PlannedTime.Before(out[j].PlannedTime)
	})
	if out == nil {
		out = []stopRow{}
	}
	writeJSON(w, out)
}

func handleRoutes(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, slug string) {
	rows, err := pool.Query(r.Context(), `
		SELECT line_label, line_category, direction_name, direction_slug,
		       via_slugs, count(*) AS stop_events,
		       avg(COALESCE(EXTRACT(EPOCH FROM (actual_time - planned_time)), 0))::int AS avg_delay_s
		FROM stop_events
		WHERE station_eva = (SELECT eva FROM stations WHERE slug = $1)
		GROUP BY line_label, line_category, direction_name, direction_slug, via_slugs
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
		if err := rows.Scan(&r2.LineLabel, &r2.LineCategory, &r2.DirectionName, &r2.DirectionSlug,
			&r2.ViaSlugs, &r2.StopEvents, &r2.AvgDelayS); err != nil {
			writeError(w, 500, fmt.Sprintf("scan: %v", err))
			return
		}
		if r2.ViaSlugs == nil {
			r2.ViaSlugs = []string{}
		}
		out = append(out, r2)
	}
	if out == nil {
		out = []routeRow{}
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
		SELECT s.name, se.line_label, se.direction_name, se.planned_time, se.actual_time,
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
		if err := rows.Scan(&t.StationName, &t.LineLabel, &t.DirectionName, &t.PlannedTime,
			&t.ActualTime, &t.DelayS, &t.ScrapedAt); err != nil {
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
		  COALESCE(EXTRACT(EPOCH FROM (NOW() - (SELECT max(scraped_at) FROM stop_events)))::int, -1)`).Scan(
		&h.RecentRuns, &h.RecentEvents, &h.LastScrapeAgo)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query health: %v", err))
		return
	}
	writeJSON(w, h)
}