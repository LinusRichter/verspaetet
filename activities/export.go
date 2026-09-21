package activities

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// MonthlyParquetDataset exports one operating month of stop_events
// (partitioned by trip_date) plus the stations registry as Parquet files,
// with a JSON manifest describing the chunk.

// StopEventParquet is the flat Parquet row schema for stop_events.
// Field names in the file are snake_case (parquet-go uses the struct field
// name; we set explicit tags via struct order — parquet-go names columns
// after the Go fields, so we keep them exported and CamelCase-free by using
// parquet tags).
type StopEventParquet struct {
	StationEva      string  `parquet:"station_eva"`
	Direction       string  `parquet:"direction"`
	StopID          string  `parquet:"stop_id"`
	TripDate        string  `parquet:"trip_date"` // YYYY-MM-DD (empty = unknown)
	LineCategory    string  `parquet:"line_category"`
	TrainNumber     string  `parquet:"train_number,optional"`
	Owner           string  `parquet:"owner,optional"`
	TripKind        string  `parquet:"trip_kind,optional"`
	DirectionName   string  `parquet:"direction_name,optional"`
	ViaPath         string  `parquet:"via_path,optional"` // pipe-separated
	PlannedTime     int64   `parquet:"planned_time"`      // unix seconds UTC
	ActualTime      *int64  `parquet:"actual_time,optional"`
	PlannedPlatform *string `parquet:"planned_platform,optional"`
	Platform        *string `parquet:"platform,optional"`
	Cancelled       bool    `parquet:"cancelled"`
	ScrapedAt       int64   `parquet:"scraped_at"` // unix seconds UTC
}

// StationParquet is the flat Parquet row schema for stations.
type StationParquet struct {
	Eva          string  `parquet:"eva"`
	Slug         string  `parquet:"slug"`
	Name         string  `parquet:"name"`
	Category     *int32  `parquet:"category,optional"`
	Lat          *float64 `parquet:"lat,optional"`
	Lon          *float64 `parquet:"lon,optional"`
	FederalState *string `parquet:"federal_state,optional"`
}

// Manifest describes one exported monthly chunk.
type Manifest struct {
	Year          int       `json:"year"`
	Month         int       `json:"month"`
	GeneratedAt   time.Time `json:"generated_at"`
	SchemaVersion string    `json:"schema_version"`
	StopEvents    struct {
		Rows       int64  `json:"rows"`
		File       string `json:"file"`
		SizeBytes  int64  `json:"size_bytes"`
		Sha256     string `json:"sha256"`
		MinScraped string `json:"min_scraped_at"`
		MaxScraped string `json:"max_scraped_at"`
	} `json:"stop_events"`
	Stations struct {
		Rows      int64  `json:"rows"`
		File      string `json:"file"`
		SizeBytes int64  `json:"size_bytes"`
		Sha256    string `json:"sha256"`
	} `json:"stations"`
	Attribution string `json:"attribution"`
}

// ExportMonth exports the given operating month to outDir.
// All files are written atomically (tmp -> rename). Returns the manifest.
func ExportMonth(ctx context.Context, pool *pgxpool.Pool, outDir string, year, month int, schemaVersion string) (*Manifest, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	monthPrefix := monthStart.Format("2006-01")

	eventsFile := filepath.Join(outDir, fmt.Sprintf("stop_events_%s.parquet", monthPrefix))
	stationsFile := filepath.Join(outDir, "stations.parquet")
	manifestFile := filepath.Join(outDir, "manifest.json")

	// ---- stop_events: stream rows into Parquet ----
	rowsWritten, minScraped, maxScraped, err := exportStopEvents(ctx, pool, eventsFile, monthStart, monthEnd)
	if err != nil {
		return nil, fmt.Errorf("export stop_events: %w", err)
	}

	// ---- stations: full registry (small, refreshed each chunk) ----
	stationsRows, err := exportStations(ctx, pool, stationsFile)
	if err != nil {
		return nil, fmt.Errorf("export stations: %w", err)
	}

	// ---- manifest ----
	evSize, _ := fileSize(eventsFile)
	stSize, _ := fileSize(stationsFile)
	evSha, _ := fileSHA256(eventsFile)
	stSha, _ := fileSHA256(stationsFile)
	m := &Manifest{
		Year:          year,
		Month:         month,
		GeneratedAt:   time.Now().UTC(),
		SchemaVersion: schemaVersion,
		Attribution:   "Data: Deutsche Bahn (Timetables API & StaDa API), license CC BY 4.0 — verspaetet",
	}
	m.StopEvents.Rows = rowsWritten
	m.StopEvents.File = filepath.Base(eventsFile)
	m.StopEvents.SizeBytes = evSize
	m.StopEvents.Sha256 = evSha
	m.StopEvents.MinScraped = minScraped
	m.StopEvents.MaxScraped = maxScraped
	m.Stations.Rows = stationsRows
	m.Stations.File = filepath.Base(stationsFile)
	m.Stations.SizeBytes = stSize
	m.Stations.Sha256 = stSha

	if err := writeJSONAtomic(manifestFile, m); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	log.Printf("[export] %d-%02d: %d stop_events, %d stations -> %s", year, month, rowsWritten, stationsRows, outDir)
	return m, nil
}

// exportStopEvents streams all events with trip_date in [start, end) to a
// Parquet file. Returns rows written and the scraped_at range.
func exportStopEvents(ctx context.Context, pool *pgxpool.Pool, path string, start, end time.Time) (int64, string, string, error) {
	rows, err := pool.Query(ctx, `
		SELECT station_eva, direction, stop_id,
		       to_char(trip_date, 'YYYY-MM-DD'),
		       line_category, COALESCE(train_number,''), COALESCE(owner,''),
		       COALESCE(trip_kind,''), COALESCE(direction_name,''),
		       array_to_string(via_path, '|'),
		       EXTRACT(EPOCH FROM planned_time)::bigint,
		       EXTRACT(EPOCH FROM actual_time)::bigint,
		       planned_platform, platform, cancelled, EXTRACT(EPOCH FROM scraped_at)::bigint
		FROM stop_events
		WHERE trip_date >= $1 AND trip_date < $2
		ORDER BY stop_id, scraped_at`, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return 0, "", "", err
	}
	defer rows.Close()

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()

	w := parquet.NewGenericWriter[StopEventParquet](f,
		parquet.Compression(&zstd.Codec{}),
	)
	var written int64
	var minScraped, maxScraped time.Time
	buf := make([]StopEventParquet, 0, 1000)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
		buf = buf[:0]
		return nil
	}

	for rows.Next() {
		var eva, dir, stopID, tripDate, lineCat string
		var trainNumber, owner, tripKind, directionName, viaPath string
		var planned int64
		var actual *int64
		var plannedPlatform, platform *string
		var cancelled bool
		var scraped int64
		if err := rows.Scan(&eva, &dir, &stopID, &tripDate, &lineCat,
			&trainNumber, &owner, &tripKind, &directionName, &viaPath,
			&planned, &actual, &plannedPlatform, &platform, &cancelled, &scraped); err != nil {
			return 0, "", "", err
		}
		buf = append(buf, StopEventParquet{
			StationEva: eva, Direction: dir, StopID: stopID,
			TripDate: tripDate, LineCategory: lineCat,
			TrainNumber: trainNumber, Owner: owner, TripKind: tripKind,
			DirectionName: directionName, ViaPath: viaPath,
			PlannedTime: planned, ActualTime: actual,
			PlannedPlatform: plannedPlatform, Platform: platform,
			Cancelled: cancelled, ScrapedAt: scraped,
		})
		written++
		st := time.Unix(scraped, 0)
		if minScraped.IsZero() || st.Before(minScraped) {
			minScraped = st
		}
		if st.After(maxScraped) {
			maxScraped = st
		}
		if len(buf) >= 1000 {
			if err := flush(); err != nil {
				return 0, "", "", err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", "", err
	}
	if err := flush(); err != nil {
		return 0, "", "", err
	}
	if err := w.Close(); err != nil {
		return 0, "", "", err
	}
	if err := f.Sync(); err != nil {
		return 0, "", "", err
	}
	if err := f.Close(); err != nil {
		return 0, "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, "", "", err
	}
	minS, maxS := "", ""
	if !minScraped.IsZero() {
		minS = minScraped.UTC().Format(time.RFC3339)
	}
	if !maxScraped.IsZero() {
		maxS = maxScraped.UTC().Format(time.RFC3339)
	}
	return written, minS, maxS, nil
}

// exportStations writes the full station registry to Parquet.
func exportStations(ctx context.Context, pool *pgxpool.Pool, path string) (int64, error) {
	rows, err := pool.Query(ctx, `
		SELECT eva, slug, name, category, lat, lon, federal_state
		FROM stations ORDER BY eva`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := parquet.NewGenericWriter[StationParquet](f, parquet.Compression(&zstd.Codec{}))

	var written int64
	buf := make([]StationParquet, 0, 1000)
	for rows.Next() {
		var s StationParquet
		if err := rows.Scan(&s.Eva, &s.Slug, &s.Name, &s.Category, &s.Lat, &s.Lon, &s.FederalState); err != nil {
			return 0, err
		}
		buf = append(buf, s)
		written++
		if len(buf) >= 1000 {
			if _, err := w.Write(buf); err != nil {
				return 0, err
			}
			buf = buf[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(buf) > 0 {
		if _, err := w.Write(buf); err != nil {
			return 0, err
		}
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return written, nil
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func writeJSONAtomic(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var _ = log.Printf