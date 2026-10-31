package activities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	LineLabel       string  `parquet:"line_label,optional"` // public line number (IRIS l), empty pre-2026-10-16
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
	Eva          string   `parquet:"eva"`
	Slug         string   `parquet:"slug"`
	Name         string   `parquet:"name"`
	Category     *int32   `parquet:"category,optional"`
	Lat          *float64 `parquet:"lat,optional"`
	Lon          *float64 `parquet:"lon,optional"`
	FederalState *string  `parquet:"federal_state,optional"`
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
		Attribution:   datasetAttribution,
	}
	m.StopEvents.Rows = rowsWritten
	// Published name: the upload stores the local stop_events_<YYYY-MM>.parquet
	// as <YYYY-MM>/stop_events.parquet (see repoPath in the HF upload).
	m.StopEvents.File = "stop_events.parquet"
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
	// Root-Übersicht (Repo-Card im HF-Repo): Index ALLER Monats-Chunks.
	if err := WriteOverviewCard(filepath.Dir(outDir)); err != nil {
		log.Printf("WARN overview card: %v", err)
	}
	log.Printf("[export] %d-%02d: %d stop_events, %d stations -> %s", year, month, rowsWritten, stationsRows, outDir)
	return m, nil
}

// WriteOverviewCard regenerates the ROOT README.md in the exports dir: an
// index of all month chunks (row counts from each chunk's manifest.json).
// Uploaded as the HuggingFace repo card on every export.
func WriteOverviewCard(exportsDir string) error {
	dirs, err := filepath.Glob(filepath.Join(exportsDir, "20??-??"))
	if err != nil {
		return err
	}
	sort.Strings(dirs)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("language:\n  - de\n")
	b.WriteString("license: cc-by-4.0\n")
	b.WriteString("pretty_name: \"verspaetet: Deutsche Bahn delay snapshots\"\n")
	b.WriteString("size_categories:\n  - 10M<n<100M\n")
	b.WriteString("tags:\n  - deutsche-bahn\n  - timetable\n  - delay\n  - transportation\n  - railway\n")
	// configs: lets the Hub's dataset viewer and datasets.load_dataset find the
	// files in the month-folder layout.
	b.WriteString("configs:\n")
	b.WriteString("  - config_name: stop_events\n    data_files: \"20*/stop_events.parquet\"\n    default: true\n")
	b.WriteString("  - config_name: stations\n    data_files: stations.parquet\n")
	b.WriteString("---\n\n")
	b.WriteString("# verspaetet: Deutsche Bahn delay snapshots\n\n")
	b.WriteString("Arrival and departure data for all ~5,400 German IRIS stations, collected\n")
	b.WriteString("from the official DB Timetables API (IRIS) at the same cadence for every\n")
	b.WriteString("station. Each train stop is recorded several times, so the data shows how\n")
	b.WriteString("the communicated delay evolved, not only the final delay.\n\n")
	b.WriteString("## Layout\n\n")
	b.WriteString("- `<YYYY-MM>/stop_events.parquet`: all stop-event snapshots of one operating month (zstd)\n")
	b.WriteString("- `<YYYY-MM>/manifest.json`: SHA256 checksums, coverage, schema version\n")
	b.WriteString("- `<YYYY-MM>/README.md`: coverage and known gaps of that month\n")
	b.WriteString("- `stations.parquet`: station registry, refreshed on every export\n\n")
	b.WriteString("One chunk per operating month, published on the 4th of the following month\n")
	b.WriteString("(3-day finality lag). This README is regenerated on every export.\n\n")
	b.WriteString("## Months\n\n")
	for _, dir := range dirs {
		month := filepath.Base(dir)
		rows := "?"
		if man, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
			var mj struct {
				StopEvents struct {
					Rows int64 `json:"rows"`
				} `json:"stop_events"`
			}
			if json.Unmarshal(man, &mj) == nil {
				rows = fmt.Sprintf("%d", mj.StopEvents.Rows)
			}
		}
		b.WriteString(fmt.Sprintf("- [%s](%s/README.md): %s stop-event snapshots\n", month, month, rows))
	}
	b.WriteString("\n")
	b.WriteString(schemaSection)
	b.WriteString(cardFooter)
	card := filepath.Join(exportsDir, "README.md")
	tmp := card + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, card)
}

// exportStopEvents streams all events with trip_date in [start, end) to a
// Parquet file. Returns rows written and the scraped_at range.
func exportStopEvents(ctx context.Context, pool *pgxpool.Pool, path string, start, end time.Time) (int64, string, string, error) {
	rows, err := pool.Query(ctx, `
		SELECT station_eva, direction, stop_id,
		       to_char(trip_date, 'YYYY-MM-DD'),
		       line_category, COALESCE(line_label,''), COALESCE(train_number,''), COALESCE(owner,''),
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
		var lineLabel, trainNumber, owner, tripKind, directionName, viaPath string
		var planned int64
		var actual *int64
		var plannedPlatform, platform *string
		var cancelled bool
		var scraped int64
		if err := rows.Scan(&eva, &dir, &stopID, &tripDate, &lineCat, &lineLabel,
			&trainNumber, &owner, &tripKind, &directionName, &viaPath,
			&planned, &actual, &plannedPlatform, &platform, &cancelled, &scraped); err != nil {
			return 0, "", "", err
		}
		buf = append(buf, StopEventParquet{
			StationEva: eva, Direction: dir, StopID: stopID,
			TripDate: tripDate, LineCategory: lineCat,
			LineLabel: lineLabel, TrainNumber: trainNumber, Owner: owner, TripKind: tripKind,
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

// datasetAttribution is the CC BY 4.0 attribution line (manifest + cards).
const datasetAttribution = "Data: Deutsche Bahn (Timetables API & StaDa API), license CC BY 4.0. Dataset: verspaetet, https://huggingface.co/datasets/LinusRichter404/verspaetet"

// schemaSection documents the Parquet columns (StopEventParquet, StationParquet).
const schemaSection = "## Schema\n\n" +
	"`stop_events.parquet`, one row per snapshot of one train stop:\n\n" +
	"| Column | Type | Description |\n" +
	"|---|---|---|\n" +
	"| `station_eva` | string | EVA number of the station |\n" +
	"| `direction` | string | `arrival` or `departure` |\n" +
	"| `stop_id` | string | IRIS stop id (`{dailyTripId}-{YYMMddHHmm}-{stopIndex}`), stable across snapshots |\n" +
	"| `trip_date` | string | operating day of the trip, `YYYY-MM-DD` |\n" +
	"| `line_category` | string | train category, e.g. `ICE`, `RE`, `S` |\n" +
	"| `line_label` | string, optional | public line label (IRIS `l`), e.g. `12` for S12; empty before 2026-10-16 |\n" +
	"| `train_number` | string, optional | operational train number (5-digit trip number for S-Bahn) |\n" +
	"| `owner` | string, optional | operator code (IRIS `tl o`) |\n" +
	"| `trip_kind` | string, optional | IRIS trip type: `p` passenger, `e` replacement, `z` additional, `s` suburban, ... |\n" +
	"| `direction_name` | string, optional | far endpoint: destination for departures, origin for arrivals |\n" +
	"| `via_path` | string, optional | route stations, pipe-separated |\n" +
	"| `planned_time` | int64 | planned time, unix seconds UTC |\n" +
	"| `actual_time` | int64, optional | communicated changed time, unix seconds UTC; null if no change was announced |\n" +
	"| `planned_platform` | string, optional | planned platform |\n" +
	"| `platform` | string, optional | current platform (changed platform if announced) |\n" +
	"| `cancelled` | bool | stop cancelled |\n" +
	"| `scraped_at` | int64 | time of the snapshot, unix seconds UTC |\n\n" +
	"Delay in seconds = `actual_time - planned_time`. Group by (`station_eva`, `direction`, `stop_id`)\n" +
	"and sort by `scraped_at` to follow how the delay evolved.\n\n" +
	"`stations.parquet`: `eva`, `slug`, `name`, `category` (station category, optional),\n" +
	"`lat`, `lon` (optional), `federal_state` (optional).\n\n"

// cardFooter closes both cards: attribution and links back to code and write-up.
const cardFooter = "## License & attribution\n\n" +
	"CC BY 4.0, derived from Deutsche Bahn open data. When reusing the data, include:\n\n" +
	"> " + datasetAttribution + "\n\n" +
	"## Links\n\n" +
	"- Code: [github.com/LinusRichter/verspaetet](https://github.com/LinusRichter/verspaetet)\n" +
	"- Project write-up: [linusrichter.de/projects/verspaetet](https://linusrichter.de/projects/verspaetet/)\n"

// WriteDatasetCard generates a README.md for the chunk directory
// (HuggingFace picks it up as the dataset card).
func WriteDatasetCard(outDir string, m *Manifest, stationCount int) error {
	var b strings.Builder
	// YAML front matter — HuggingFace liest das als Repo-Card-Metadaten;
	// ohne den Block warnt die Hub-UI ("empty or missing yaml metadata").
	b.WriteString("---\n")
	b.WriteString("language:\n")
	b.WriteString("- de\n")
	b.WriteString("license: cc-by-4.0\n")
	b.WriteString(fmt.Sprintf("pretty_name: \"verspaetet: Deutsche Bahn delay snapshots %04d-%02d\"\n", m.Year, m.Month))
	b.WriteString("size_categories:\n")
	b.WriteString("- 10M<n<100M\n")
	b.WriteString("tags:\n")
	for _, t := range []string{"deutsche-bahn", "timetable", "delay", "transportation", "railway"} {
		b.WriteString("- " + t + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# verspaetet: Deutsche Bahn delay snapshots %04d-%02d\n\n", m.Year, m.Month))
	b.WriteString("Monthly chunk of delay-evolution snapshots collected from Deutsche Bahn\n")
	b.WriteString("station boards via the official DB Timetables API (IRIS).\n\n")
	b.WriteString("Each row is one observation of a train's arrival or departure at one station\n")
	b.WriteString("at one point in time. Multiple rows per stop_id show how the delay evolved\n")
	b.WriteString("as the train approached. See `manifest.json` for checksums and coverage.\n\n")
	b.WriteString("## Coverage\n\n")
	b.WriteString(fmt.Sprintf("- Operating days with data: %d\n", len(m.Coverage.DaysWithEvents)))
	// Days before the first collected day are not gaps: collection had not
	// started yet (the first month is partial).
	missing := m.Coverage.MissingDays
	if days := append([]string(nil), m.Coverage.DaysWithEvents...); len(days) > 0 {
		sort.Strings(days)
		first := days[0]
		if first != fmt.Sprintf("%04d-%02d-01", m.Year, m.Month) {
			b.WriteString(fmt.Sprintf("- Collection started: %s\n", first))
		}
		var gaps []string
		for _, d := range missing {
			if d > first {
				gaps = append(gaps, d)
			}
		}
		missing = gaps
	}
	if len(missing) > 0 {
		b.WriteString(fmt.Sprintf("- Known gaps (no data): %s\n", strings.Join(missing, ", ")))
	}
	if len(m.Coverage.Downtimes) > 0 {
		b.WriteString("- Documented downtimes:\n")
		for _, d := range m.Coverage.Downtimes {
			b.WriteString(fmt.Sprintf("  - %s\n", d))
		}
	}
	b.WriteString(fmt.Sprintf("- Stations in registry: %d\n\n", stationCount))
	b.WriteString("## Files\n\n")
	b.WriteString(fmt.Sprintf("- `%04d-%02d/stop_events.parquet`: %d rows, %d bytes\n", m.Year, m.Month, m.StopEvents.Rows, m.StopEvents.SizeBytes))
	b.WriteString(fmt.Sprintf("- `%04d-%02d/manifest.json`: checksums (SHA256), coverage, schema version\n", m.Year, m.Month))
	b.WriteString(fmt.Sprintf("- `stations.parquet` (repo root): %d stations\n\n", m.Stations.Rows))
	b.WriteString(schemaSection)
	b.WriteString(cardFooter)
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

// UploadToHuggingFace uploads the exported chunk files to the HF dataset repo
// via the Hub REST API — NO git. Files larger than lfsThreshold go through the
// LFS protocol (batch → presigned PUT → verify), everything else is inlined
// base64 in an NDJSON commit. Idempotent: the LFS batch skips objects that the
// Hub already has (no "actions" in the response), and the commit overwrites
// the same paths. HF_TOKEN and HF_REPO must be set
// (e.g. HF_TOKEN=hf_..., HF_REPO=LinusRichter404/verspaetet).
func UploadToHuggingFace(ctx context.Context, chunkDir string) error {
	token := os.Getenv("HF_TOKEN")
	repo := os.Getenv("HF_REPO")
	if token == "" || repo == "" {
		return fmt.Errorf("HF_TOKEN or HF_REPO not set")
	}

	type fileMeta struct {
		name string
		size int64
		sha  string // hex sha256
		lfs  bool
	}
	entries, err := os.ReadDir(chunkDir)
	if err != nil {
		return err
	}
	var files []fileMeta
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(chunkDir, e.Name())
		size, err := fileSize(p)
		if err != nil {
			return err
		}
		sha, err := fileSHA256(p)
		if err != nil {
			return err
		}
		isText := strings.HasSuffix(e.Name(), ".md") || strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".txt")
		files = append(files, fileMeta{name: e.Name(), size: size, sha: sha, lfs: !isText && size > 1024})
	}
	if len(files) == 0 {
		return fmt.Errorf("no files in %s", chunkDir)
	}

	const hfEndpoint = "https://huggingface.co"
	// Long timeout: the 308 MB parquet PUT must fit a slow line.
	client := &http.Client{Timeout: 30 * time.Minute}

	// 1) LFS batch for large files: get presigned upload URLs.
	type lfsAction struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}
	type lfsObject struct {
		OID     string               `json:"oid"`
		Size    int64                `json:"size"`
		Actions map[string]lfsAction `json:"actions"`
	}
	batchObjects := []map[string]interface{}{}
	var lfsFiles []fileMeta
	for _, f := range files {
		if f.lfs {
			lfsFiles = append(lfsFiles, f)
			batchObjects = append(batchObjects, map[string]interface{}{"oid": f.sha, "size": f.size})
		}
	}
	uploaded := map[string]bool{} // sha → already on the hub
	if len(lfsFiles) > 0 {
		payload, err := json.Marshal(map[string]interface{}{
			"operation": "upload",
			"transfers": []string{"basic"},
			"objects":   batchObjects,
			"hash_algo": "sha256",
			"ref":       map[string]interface{}{"name": "main"},
		})
		if err != nil {
			return err
		}
		url := hfEndpoint + "/datasets/" + repo + ".git/info/lfs/objects/batch"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("lfs batch: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("lfs batch: status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
		}
		var batch struct {
			Objects []lfsObject `json:"objects"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return fmt.Errorf("lfs batch decode: %w: %s", err, bytes.TrimSpace(body))
		}
		for _, obj := range batch.Objects {
			up, ok := obj.Actions["upload"]
			if !ok {
				// No actions = the Hub already has this content — nothing to upload.
				uploaded[obj.OID] = true
				log.Printf("[export] lfs: object %s already on the hub — skip", obj.OID[:8])
				continue
			}
			// Find the local file for this oid.
			var src fileMeta
			for _, f := range lfsFiles {
				if f.sha == obj.OID {
					src = f
					break
				}
			}
			if src.name == "" {
				return fmt.Errorf("lfs: no local file for oid %s", obj.OID[:8])
			}
			f, err := os.Open(filepath.Join(chunkDir, src.name))
			if err != nil {
				return err
			}
			put, err := http.NewRequestWithContext(ctx, http.MethodPut, up.Href, f)
			if err != nil {
				f.Close()
				return err
			}
			put.ContentLength = src.size
			for k, v := range up.Header {
				put.Header.Set(k, v)
			}
			put.Header.Set("Content-Type", "application/octet-stream")
			pr, err := client.Do(put)
			if err != nil {
				f.Close()
				return fmt.Errorf("lfs upload %s: %w", src.name, err)
			}
			prBody, _ := io.ReadAll(io.LimitReader(pr.Body, 4096))
			pr.Body.Close()
			f.Close()
			if pr.StatusCode < 200 || pr.StatusCode >= 300 {
				return fmt.Errorf("lfs upload %s: status %d: %s", src.name, pr.StatusCode, bytes.TrimSpace(prBody))
			}
			uploaded[obj.OID] = true
			log.Printf("[export] lfs: uploaded %s (%d bytes, oid %s, status %d)", src.name, src.size, obj.OID[:8], pr.StatusCode)
			// Optional verify step.
			if verify, ok := obj.Actions["verify"]; ok {
				vjson, _ := json.Marshal(map[string]interface{}{"oid": obj.OID, "size": obj.Size})
				vr, err := http.NewRequestWithContext(ctx, http.MethodPost, verify.Href, bytes.NewReader(vjson))
				if err == nil {
					vr.Header.Set("Authorization", "Bearer "+token)
					vr.Header.Set("Content-Type", "application/vnd.git-lfs+json")
					vresp, err := client.Do(vr)
					if err == nil {
						log.Printf("[export] lfs: verify %s -> status %d", src.name, vresp.StatusCode)
						vresp.Body.Close()
					} else {
						log.Printf("[export] WARN lfs verify %s: %v", src.name, err)
					}
				}
			}
			// Settle-Wait: auf Xet-Backed-Repos ist ein eben gePUTetes Objekt
			// Server-Seitig noch nicht sichtbar — ein direkt danach eingereichter
			// Commit mit diesem OID hing sonst (beobachtet 12.10.: hängt bis
			// Client-Timeout). 20s Puffer vor dem Commit.
			log.Printf("[export] lfs: settle 20s nach Upload (Xet-Sichtbarkeit)")
			select {
			case <-time.After(20 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// 2) NDJSON commit — Monatsordner-Layout:
	//    <month>/stop_events.parquet, <month>/manifest.json, <month>/README.md;
	//    stations.parquet (Registry, immer aktuell) + README.md (Übersicht) flat.
	month := filepath.Base(chunkDir)
	repoPath := func(name string) string {
		if name == "stations.parquet" {
			return name
		}
		if parts := strings.SplitN(name, ".parquet", 2); strings.HasPrefix(parts[0], "stop_events") && len(parts) > 1 {
			// stop_events_2026-09.parquet → <month>/stop_events.parquet
			// (der Ordner trägt bereits den Monat; im Ordner ein einheitlicher Name)
			return month + "/" + "stop_events.parquet"
		}
		return month + "/" + name
	}
	commitMsg := fmt.Sprintf("dataset chunk %s", month)
	var commitBody bytes.Buffer
	enc := json.NewEncoder(&commitBody)
	enc.Encode(map[string]interface{}{"key": "header", "value": map[string]interface{}{
		"summary": commitMsg, "description": "verspaetet monthly export",
	}})
	for _, f := range files {
		path := repoPath(f.name)
		if f.lfs {
			enc.Encode(map[string]interface{}{"key": "lfsFile", "value": map[string]interface{}{
				"path": path, "algo": "sha256", "oid": f.sha, "size": f.size,
			}})
		} else {
			b, err := os.ReadFile(filepath.Join(chunkDir, f.name))
			if err != nil {
				return err
			}
			enc.Encode(map[string]interface{}{"key": "file", "value": map[string]interface{}{
				"path": path, "content": base64.StdEncoding.EncodeToString(b), "encoding": "base64",
			}})
		}
	}
	// Root-Übersichts-README aus dem exports-Dir (wird von WriteOverviewCard
	// bei jedem Export neu generiert) → Repo-Card.
	if overview, err := os.ReadFile(filepath.Join(filepath.Dir(chunkDir), "README.md")); err == nil {
		enc.Encode(map[string]interface{}{"key": "file", "value": map[string]interface{}{
			"path": "README.md", "content": base64.StdEncoding.EncodeToString(overview), "encoding": "base64",
		}})
	}
	// Alt-Layout aufräumen: flat liegende stop_events-Parquets und manifest.json
	// (vor der Ordner-Umstellung) aus dem Repo-Tree entfernen — IDEMPOTENT:
	// im Tree fehlen sie in künftigen Läufen → die Liste ist leer.
	treeURL := hfEndpoint + "/api/datasets/" + repo + "/tree/main?recursive=true"
	treeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return err
	}
	treeReq.Header.Set("Authorization", "Bearer "+token)
	treeResp, err := client.Do(treeReq)
	if err != nil {
		return fmt.Errorf("hf tree: %w", err)
	}
	treeBody, _ := io.ReadAll(io.LimitReader(treeResp.Body, 1<<20))
	treeResp.Body.Close()
	if treeResp.StatusCode == http.StatusOK {
		var tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		if json.Unmarshal(treeBody, &tree) == nil {
			for _, f := range tree {
				if f.Type != "file" || f.Path == ".gitattributes" || f.Path == "README.md" || f.Path == "stations.parquet" {
					continue
				}
				oldFlat := (strings.HasPrefix(f.Path, "stop_events_") && strings.HasSuffix(f.Path, ".parquet") && !strings.Contains(f.Path, "/")) || f.Path == "manifest.json" || (strings.Contains(f.Path, "/") && strings.HasPrefix(f.Path, "20") && strings.HasPrefix(filepath.Base(f.Path), "stop_events_") && filepath.Base(f.Path) != "stop_events.parquet" && strings.HasSuffix(f.Path, ".parquet"))
				if oldFlat {
					enc.Encode(map[string]interface{}{"key": "deletedFile", "value": map[string]interface{}{"path": f.Path}})
					log.Printf("[export] hf: removing old flat file %s from repo tree", f.Path)
				}
			}
		}
	} else {
		log.Printf("[export] WARN hf tree list: status %d (skipping cleanup)", treeResp.StatusCode)
	}
	curl_ := hfEndpoint + "/api/datasets/" + repo + "/commit/main"
	cr, err := http.NewRequestWithContext(ctx, http.MethodPost, curl_, &commitBody)
	if err != nil {
		return err
	}
	cr.Header.Set("Authorization", "Bearer "+token)
	cr.Header.Set("Content-Type", "application/x-ndjson")
	// Commit mit Retry: bei Fehlern die Response sichtbar loggen (der
	// Xet-Store braucht nach einem frischen PUT manchmal einen Versuch länger).
	cbody := []byte(nil)
	var cresp *http.Response
	for attempt := 1; attempt <= 3; attempt++ {
		cr, err := http.NewRequestWithContext(ctx, http.MethodPost, curl_, bytes.NewReader(commitBody.Bytes()))
		if err != nil {
			return err
		}
		cr.Header.Set("Authorization", "Bearer "+token)
		cr.Header.Set("Content-Type", "application/x-ndjson")
		cresp, err = client.Do(cr)
		if err != nil {
			return fmt.Errorf("hf commit: %w", err)
		}
		cbody, _ = io.ReadAll(io.LimitReader(cresp.Body, 1<<20))
		cresp.Body.Close()
		if cresp.StatusCode >= 200 && cresp.StatusCode < 300 {
			break
		}
		log.Printf("[export] WARN hf commit (attempt %d): status %d: %s", attempt, cresp.StatusCode, bytes.TrimSpace(cbody))
		if attempt < 3 {
			select {
			case <-time.After(15 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if cresp == nil || cresp.StatusCode < 200 || cresp.StatusCode >= 300 {
		return fmt.Errorf("hf commit: status %d (3 attempts): %s", cresp.StatusCode, bytes.TrimSpace(cbody))
	}
	log.Printf("[export] hf commit response: %s", bytes.TrimSpace(cbody))
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
