package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackfillScrapeRuns adds scrape_runs.parquet to an already-published month
// chunk. September and October 2026 went out as schema v0.2 without the
// observation log; their stop_events rows are gone from Postgres, but the
// scrape_runs rows still exist. The published manifest is downloaded, the
// log is exported for its scraped_at range, manifest + README are rewritten
// as schema v0.3 and ONLY those files are uploaded — no stop_events, no
// stations, no root README, no repo deletions. With dryRun everything is
// prepared and logged, nothing uploaded. Returns the merged manifest.
func BackfillScrapeRuns(ctx context.Context, pool *pgxpool.Pool, exportsDir string, year, month int, dryRun bool) (*Manifest, error) {
	ym := fmt.Sprintf("%04d-%02d", year, month)

	// 1) Published manifest from the HF repo.
	old, err := downloadPublishedManifest(ctx, ym)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: %w", ym, err)
	}
	if err := prepareBackfillManifest(old, year, month); err != nil {
		return nil, fmt.Errorf("backfill %s: %w", ym, err)
	}

	// 2) Fresh staging dir. The upload derives the repo folder from
	// filepath.Base(chunkDir), so the leaf must be the month.
	stage := filepath.Join(exportsDir, "_backfill", ym)
	if err := os.RemoveAll(stage); err != nil {
		return nil, fmt.Errorf("backfill %s: staging: %w", ym, err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, fmt.Errorf("backfill %s: staging: %w", ym, err)
	}

	// 3) Observation log for the published chunk's scraped_at range.
	from, err := time.Parse(time.RFC3339, old.StopEvents.MinScraped)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: parse min_scraped_at %q: %w", ym, old.StopEvents.MinScraped, err)
	}
	to, err := time.Parse(time.RFC3339, old.StopEvents.MaxScraped)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: parse max_scraped_at %q: %w", ym, old.StopEvents.MaxScraped, err)
	}
	srPath := filepath.Join(stage, fmt.Sprintf("scrape_runs_%s.parquet", ym))
	srRows, err := exportScrapeRuns(ctx, pool, srPath, from, to)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: export scrape_runs: %w", ym, err)
	}
	if srRows == 0 {
		return nil, fmt.Errorf("backfill %s: scrape_runs for [%s .. %s] is empty — refusing to publish an empty log", ym, old.StopEvents.MinScraped, old.StopEvents.MaxScraped)
	}
	srSize, err := fileSize(srPath)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: %w", ym, err)
	}
	srSha, err := fileSHA256(srPath)
	if err != nil {
		return nil, fmt.Errorf("backfill %s: %w", ym, err)
	}

	// 4) Merged manifest: everything from the published one (stop_events,
	// stations, coverage incl. downtimes, attribution), schema v0.3 + log.
	old.ScrapeRuns.Rows = srRows
	old.ScrapeRuns.File = "scrape_runs.parquet"
	old.ScrapeRuns.SizeBytes = srSize
	old.ScrapeRuns.Sha256 = srSha
	old.ScrapeRuns.From = old.StopEvents.MinScraped
	old.ScrapeRuns.To = old.StopEvents.MaxScraped
	if err := writeJSONAtomic(filepath.Join(stage, "manifest.json"), old); err != nil {
		return nil, fmt.Errorf("backfill %s: write manifest: %w", ym, err)
	}
	if err := WriteDatasetCard(stage, old, int(old.Stations.Rows)); err != nil {
		return nil, fmt.Errorf("backfill %s: dataset card: %w", ym, err)
	}

	// 5) Dry run: prepare and log only.
	if dryRun {
		log.Printf("[backfill] %s dry run: would upload scrape_runs_%s.parquet (%d rows, %d bytes, sha %s), manifest.json (schema %s), README.md",
			ym, ym, srRows, srSize, srSha, old.SchemaVersion)
		return old, nil
	}

	// 6) Upload ONLY the three staged files — no root README, no deletions.
	if err := uploadToHuggingFace(ctx, stage, hfUploadOpts{includeOverview: false, cleanupOldFlat: false}); err != nil {
		return nil, fmt.Errorf("backfill %s: upload: %w", ym, err)
	}

	// 7) Local mirror (WriteOverviewCard reads the manifests from here) and
	// a refreshed overview card — NOT uploaded here; the next regular export
	// publishes it.
	dst := filepath.Join(exportsDir, ym)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, fmt.Errorf("backfill %s: mirror: %w", ym, err)
	}
	for _, name := range []string{fmt.Sprintf("scrape_runs_%s.parquet", ym), "manifest.json", "README.md"} {
		if err := copyFile(filepath.Join(stage, name), filepath.Join(dst, name)); err != nil {
			return nil, fmt.Errorf("backfill %s: mirror %s: %w", ym, name, err)
		}
	}
	if err := WriteOverviewCard(exportsDir); err != nil {
		log.Printf("WARN overview card: %v", err)
	}
	log.Printf("[backfill] %s: scrape_runs %d rows [%s .. %s], sha %s, schema %s",
		ym, srRows, old.ScrapeRuns.From, old.ScrapeRuns.To, srSha, old.SchemaVersion)
	return old, nil
}

// downloadPublishedManifest fetches the manifest of a published chunk from
// the HF dataset repo (resolve endpoint, Bearer token if HF_TOKEN is set).
func downloadPublishedManifest(ctx context.Context, ym string) (*Manifest, error) {
	repo := os.Getenv("HF_REPO")
	if repo == "" {
		return nil, fmt.Errorf("HF_REPO not set")
	}
	url := hfEndpoint + "/datasets/" + repo + "/resolve/main/" + ym + "/manifest.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("HF_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest %s: status %d: %s", ym, resp.StatusCode, bytes.TrimSpace(body))
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest %s decode: %w: %s", ym, err, bytes.TrimSpace(body))
	}
	return &m, nil
}

// prepareBackfillManifest validates a published manifest for a scrape_runs
// backfill and stamps it as schema v0.3. Pure: mutates only SchemaVersion,
// so the merge (everything else survives untouched) is trivially testable.
func prepareBackfillManifest(m *Manifest, year, month int) error {
	if m == nil {
		return fmt.Errorf("no manifest")
	}
	if m.Year != year || m.Month != month {
		return fmt.Errorf("manifest is for %04d-%02d, requested %04d-%02d", m.Year, m.Month, year, month)
	}
	if m.StopEvents.Rows == 0 {
		return fmt.Errorf("published chunk has no stop_events rows")
	}
	if m.StopEvents.MinScraped == "" || m.StopEvents.MaxScraped == "" {
		return fmt.Errorf("published manifest has no scraped_at range")
	}
	if m.ScrapeRuns.Rows > 0 {
		return fmt.Errorf("chunk already contains scrape_runs (%d rows) — already backfilled", m.ScrapeRuns.Rows)
	}
	m.SchemaVersion = "v0.3"
	return nil
}
