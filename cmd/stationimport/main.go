package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"verspaetet/activities"
	"verspaetet/shared"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stationimport populates the stations table — the "universe" for the
// scheduler and the resolver for discovery.
//
// Sources (in order):
//   1. StaDa API (default): one call, all active stations (category 1-7),
//      incl. lat/lon + federal state. Terms: ~1 call/day — run periodically.
//   2. --csv=<file>: fallback import from a dumped stations table
//      (eva,name[,category]) — e.g. extracted from the old DB on the server.
//
// In both cases:
//   - slug = shared.Slugify(name), fetch_offset = shared.FetchOffset(slug)
//   - existing rows are updated (name/category/coords) but keep discovered_at
//   - pending_stations names are matched against imported stations and
//     resolved (deleted) when found — this is the discovery loop's exit.
//
// Usage:
//   stationimport                 # StaDa import
//   stationimport --csv=dump.csv  # CSV fallback
func main() {
	csvPath := flag.String("csv", "", "import from CSV (eva,name[,category]) instead of StaDa")
	flag.Parse()

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatalln("POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalln("Unable to connect to Postgres:", err)
	}
	defer pool.Close()

	if *csvPath != "" {
		if err := importFromCSV(ctx, pool, *csvPath); err != nil {
			log.Fatalln("CSV import:", err)
		}
	} else {
		if err := importFromStada(ctx, pool); err != nil {
			log.Fatalln("StaDa import:", err)
		}
	}

	if err := resolvePending(ctx, pool); err != nil {
		log.Printf("WARN resolve pending: %v", err)
	}
}

// stationRow is the normalized insert unit for both sources.
type stationRow struct {
	Eva     string
	Name    string
	Cat     *int32
	Lat     *float64
	Lon     *float64
	State   *string
}

func importFromStada(ctx context.Context, pool *pgxpool.Pool) error {
	stations, err := activities.FetchStadaStations(ctx)
	if err != nil {
		return fmt.Errorf("fetch stada: %w", err)
	}
	log.Printf("[stationimport] StaDa returned %d stations", len(stations))

	rows := make([]stationRow, 0, len(stations))
	for _, st := range stations {
		eva := st.MainEVA()
		if eva == "" || st.Name == "" {
			continue
		}
		cat := st.Category
		row := stationRow{Eva: eva, Name: st.Name, Cat: &cat, State: &st.FederalState}
		if lat, lon, ok := st.LatLon(); ok {
			row.Lat = &lat
			row.Lon = &lon
		}
		rows = append(rows, row)
	}
	return upsertStations(ctx, pool, rows)
}

// importFromCSV reads eva,name[,category] rows (header optional).
func importFromCSV(ctx context.Context, pool *pgxpool.Pool, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return err
	}
	rows := make([]stationRow, 0, len(records))
	for i, rec := range records {
		if len(rec) < 2 {
			continue
		}
		eva, name := strings.TrimSpace(rec[0]), strings.TrimSpace(rec[1])
		if eva == "" || name == "" {
			continue
		}
		// Skip header row.
		if i == 0 && !isDigits(eva) {
			continue
		}
		row := stationRow{Eva: eva, Name: name}
		if len(rec) >= 3 {
			if c, err := strconv.Atoi(strings.TrimSpace(rec[2])); err == nil {
				cat := int32(c)
				row.Cat = &cat
			}
		}
		rows = append(rows, row)
	}
	log.Printf("[stationimport] CSV contains %d stations", len(rows))
	return upsertStations(ctx, pool, rows)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func upsertStations(ctx context.Context, pool *pgxpool.Pool, rows []stationRow) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	upsert := `
INSERT INTO stations (eva, name, slug, category, lat, lon, federal_state, fetch_offset, discovered_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
ON CONFLICT (eva) DO UPDATE SET
  name = EXCLUDED.name,
  slug = EXCLUDED.slug,
  category = COALESCE(EXCLUDED.category, stations.category),
  lat = COALESCE(EXCLUDED.lat, stations.lat),
  lon = COALESCE(EXCLUDED.lon, stations.lon),
  federal_state = COALESCE(EXCLUDED.federal_state, stations.federal_state)`

	for _, r := range rows {
		slug := shared.Slugify(r.Name)
		_, err := tx.Exec(ctx, upsert,
			r.Eva, r.Name, slug, r.Cat, r.Lat, r.Lon, r.State,
			shared.FetchOffset(slug),
		)
		if err != nil {
			return fmt.Errorf("upsert %s (%s): %w", r.Eva, r.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	log.Printf("[stationimport] upserted %d stations", len(rows))
	return nil
}

// resolvePending resolves pending_stations rows whose name now exists in
// stations (discovery resolved by a fresh import): provenance (seen_from) is
// copied into stations.discovered_from when that field is still NULL, then
// the pending row is deleted. The remainder stays pending for the next import.
func resolvePending(ctx context.Context, pool *pgxpool.Pool) error {
	// Copy provenance into unresolved stations rows.
	_, err := pool.Exec(ctx,
		`UPDATE stations s
		 SET discovered_from = p.seen_from
		 FROM pending_stations p
		 WHERE s.name = p.name AND s.discovered_from IS NULL`)
	if err != nil {
		return fmt.Errorf("copy provenance: %w", err)
	}
	tag, err := pool.Exec(ctx,
		`DELETE FROM pending_stations p
		 USING stations s
		 WHERE s.name = p.name`)
	if err != nil {
		return err
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pending_stations`).Scan(&remaining); err != nil {
		return err
	}
	log.Printf("[stationimport] resolved %d pending names, %d still unresolved",
		tag.RowsAffected(), remaining)
	return nil
}