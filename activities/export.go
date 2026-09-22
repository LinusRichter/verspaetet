package activities

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	Coverage struct {
		// Days in the month with at least one collected event. Days NOT in
		// this list (within the collected range) are known gaps.
		DaysWithEvents []string `json:"days_with_events"`
		MissingDays    []string `json:"missing_days"`
		Downtimes      []string `json:"downtimes"`
	} `json:"coverage"`
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

	// ---- coverage: days with events, missing days, consumed downtimes ----
	daysWith, missingDays, err := coverageDays(ctx, pool, monthStart, monthEnd)
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}
	downtimes := consumeDowntimeNotes()
	for _, d := range downtimes {
		log.Printf("[export] consumed downtime note: %s", d)
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
	m.Coverage.DaysWithEvents = daysWith
	m.Coverage.MissingDays = missingDays
	m.Coverage.Downtimes = downtimes

	if err := writeJSONAtomic(manifestFile, m); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	if err := WriteDatasetCard(outDir, m, int(stationsRows)); err != nil {
		log.Printf("WARN dataset card: %v", err)
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

// coverageDays lists the days of the month that have at least one collected
// event, and the days within the collected range that have none (known gaps).
func coverageDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time) (daysWith, missingDays []string, err error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT trip_date FROM stop_events
		WHERE trip_date >= $1 AND trip_date < $2
		ORDER BY trip_date`, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	has := map[string]bool{}
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, nil, err
		}
		has[d.Format("2006-01-02")] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// Only flag gaps up to today — future days of the current month are not
	// gaps, they just haven't happened yet.
	today := time.Now().UTC().Format("2006-01-02")
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		if has[day] {
			daysWith = append(daysWith, day)
		} else if day <= today {
			missingDays = append(missingDays, day)
		}
	}
	return daysWith, missingDays, nil
}

// consumeDowntimeNotes reads and deletes all downtime notes from the
// DOWNTIMES_DIR (default /downtimes). Each file is one note: filename is
// free-form (e.g. `2026-10-03_server-down.txt`), content is the reason text.
// Notes are consumed (deleted) after being read so each appears in exactly
// one manifest. Directory must exist; missing dir = no notes.
func consumeDowntimeNotes() []string {
	dir := os.Getenv("DOWNTIMES_DIR")
	if dir == "" {
		dir = "/downtimes"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no dir = no notes
	}
	var notes []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		reason := strings.TrimSpace(string(b))
		if reason == "" {
			reason = e.Name()
		} else {
			reason = e.Name() + ": " + reason
		}
		notes = append(notes, reason)
		os.Remove(filepath.Join(dir, e.Name()))
	}
	if notes == nil {
		notes = []string{}
	}
	return notes
}

// WriteDatasetCard generates a README.md for the chunk directory
// (HuggingFace picks it up as the dataset card).
func WriteDatasetCard(outDir string, m *Manifest, stationCount int) error {
	var b strings.Builder
	b.WriteString("# verspaetet — DB delay snapshots " + fmt.Sprintf("%04d-%02d\n\n", m.Year, m.Month))
	b.WriteString("Monthly chunk of delay-evolution snapshots collected from Deutsche Bahn\n")
	b.WriteString("station boards via the official DB Timetables API (IRIS).\n\n")
	b.WriteString("Each row is one observation of a train's arrival/departure at one station\n")
	b.WriteString("at one point in time. Multiple rows per stop_id show how the delay evolved\n")
	b.WriteString("as the train approached. See `manifest.json` for checksums and coverage.\n\n")
	b.WriteString("## Coverage\n\n")
	b.WriteString(fmt.Sprintf("- Operating days with data: %d\n", len(m.Coverage.DaysWithEvents)))
	if len(m.Coverage.MissingDays) > 0 {
		b.WriteString(fmt.Sprintf("- Known gaps (no data): %s\n", strings.Join(m.Coverage.MissingDays, ", ")))
	}
	if len(m.Coverage.Downtimes) > 0 {
		b.WriteString("- Documented downtimes:\n")
		for _, d := range m.Coverage.Downtimes {
			b.WriteString(fmt.Sprintf("  - %s\n", d))
		}
	}
	b.WriteString(fmt.Sprintf("- Stations in registry: %d\n\n", stationCount))
	b.WriteString("## Files\n\n")
	b.WriteString(fmt.Sprintf("- `%s` — %d rows, %d bytes\n", m.StopEvents.File, m.StopEvents.Rows, m.StopEvents.SizeBytes))
	b.WriteString(fmt.Sprintf("- `%s` — %d stations\n", m.Stations.File, m.Stations.Rows))
	b.WriteString("- `manifest.json` — checksums (SHA256), coverage, schema version\n\n")
	b.WriteString("## License & attribution\n\n")
	b.WriteString(m.Attribution + "\n")
	card := filepath.Join(outDir, "README.md")
	tmp := card + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, card)
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

// UploadToHuggingFace pushes the exported chunk files to the HF dataset repo
// via the git protocol. HF_TOKEN and HF_REPO must be set
// (e.g. HF_TOKEN=hf_..., HF_REPO=LinusRichter404/verspaetet).
// The repo is cloned fresh into a temp dir each run; chunk files are copied
// in, committed and pushed. Idempotent per chunk: re-uploading overwrites
// the same paths.
func UploadToHuggingFace(ctx context.Context, chunkDir string) error {
	token := os.Getenv("HF_TOKEN")
	repo := os.Getenv("HF_REPO")
	if token == "" || repo == "" {
		return fmt.Errorf("HF_TOKEN or HF_REPO not set")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git not found in PATH: %w", err)
	}

	tmp, err := os.MkdirTemp("", "hf-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cloneURL := fmt.Sprintf("https://oauth2:%s@huggingface.co/datasets/%s", token, repo)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("clone", "--depth", "1", cloneURL, "."); err != nil {
		return fmt.Errorf("clone HF repo: %w", err)
	}

	// Copy the chunk files into the repo root (flat layout).
	entries, err := os.ReadDir(chunkDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(chunkDir, e.Name())
		dst := filepath.Join(tmp, e.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", e.Name(), err)
		}
	}

	if err := run("add", "-A"); err != nil {
		return err
	}
	// Nothing new? git commit fails with "nothing to commit" — treat as done.
	commitErr := run("-c", "user.name=verspaetet", "-c", "user.email=verspaetet@localhost",
		"commit", "-m", fmt.Sprintf("dataset chunk %s", filepath.Base(chunkDir)))
	if commitErr != nil && !strings.Contains(commitErr.Error(), "nothing to commit") {
		return fmt.Errorf("commit: %w", commitErr)
	}
	if err := run("push"); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	log.Printf("[export] uploaded chunk %s to huggingface.co/datasets/%s", filepath.Base(chunkDir), repo)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}