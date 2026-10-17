package main

// API server: serves the data-UI endpoints (dataui.go), the ops health
// endpoint used by monitoring, Prometheus metrics, and the React static
// build. The v1 endpoints (stations/lines/events/routes/trips/delays/stats)
// were removed together with the v1 frontend.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"verspaetet/metrics"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

	// Data-UI v2 (read-only): /api/meta /api/search /api/board /api/journey
	// /api/line /api/day
	registerDataUI(mux, pool)

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handleHealth(w, r, pool)
	})
	mux.Handle("GET /metrics", metrics.Handler())

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

func handleHealth(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	var h healthRow
	err := pool.QueryRow(r.Context(), `
		SELECT
		  (SELECT count(*) FROM scrape_runs WHERE scraped_at > NOW() - INTERVAL '10 minutes'),
		  (SELECT count(*) FROM stop_events WHERE scraped_at > NOW() - INTERVAL '10 minutes'),
		  COALESCE(EXTRACT(EPOCH FROM (NOW() - (SELECT max(scraped_at) FROM stop_events)))::int, -1),
		  (SELECT count(*) FROM stations WHERE NOT no_iris),
		  0`).Scan(
		&h.RecentRuns, &h.RecentEvents, &h.LastScrapeAgo, &h.ExpectedRuns, &h.FetchRate)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("query health: %v", err))
		return
	}
	// Expected scrape_runs in 10 min = stations / (CADENCE_MINUTES / 10).
	// CADENCE_MINUTES must match the scheduler + stationimport value (the
	// fetch_offset hash uses the same modulo). Default 30 (=1/3 per 10 min).
	cadenceMin := 30
	if v := os.Getenv("CADENCE_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cadenceMin = n
		}
	}
	expected := h.ExpectedRuns * 10 / cadenceMin
	if expected > 0 {
		h.FetchRate = float64(h.RecentRuns) / float64(expected)
		if h.FetchRate > 1.0 {
			h.FetchRate = 1.0
		}
	}
	writeJSON(w, h)
}