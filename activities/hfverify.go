package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Verification of a published chunk on HuggingFace: the repo tree must list
// the month's parquet files with the exact LFS sha256 of the export, and the
// published manifest must match. Used by the worker right after an upload
// and by the prune before deleting rows.

// The hub's tree/resolve endpoints can lag slightly behind a fresh commit,
// so the whole check is repeated.
var (
	hfVerifyAttempts = 5
	hfVerifyPause    = 30 * time.Second
)

// VerifyOnHuggingFace checks that the month's published chunk matches the
// local manifest: LFS oids and sizes from the repo tree, then the published
// manifest's sha256 and row counts.
func VerifyOnHuggingFace(ctx context.Context, month string, want *Manifest) error {
	return verifyOnHuggingFace(ctx, month, want, true)
}

// VerifyRunOnHuggingFace is VerifyOnHuggingFace with the expected values
// taken from an export_runs row. Sizes are not stored, so only the LFS
// oids (and the manifest sha/rows) are compared.
func VerifyRunOnHuggingFace(ctx context.Context, run *ExportRun) error {
	if run == nil {
		return fmt.Errorf("verify: no export run")
	}
	var want Manifest
	if run.StopEventsSha256 != nil {
		want.StopEvents.Sha256 = *run.StopEventsSha256
	}
	if run.Rows != nil {
		want.StopEvents.Rows = *run.Rows
	}
	if run.ScrapeRunsSha256 != nil {
		want.ScrapeRuns.Sha256 = *run.ScrapeRunsSha256
	}
	if run.ScrapeRunsRows != nil {
		want.ScrapeRuns.Rows = *run.ScrapeRunsRows
	}
	return verifyOnHuggingFace(ctx, run.Month, &want, false)
}

func verifyOnHuggingFace(ctx context.Context, month string, want *Manifest, withSizes bool) error {
	repo := os.Getenv("HF_REPO")
	if repo == "" {
		return fmt.Errorf("HF_REPO not set")
	}
	if want == nil {
		return fmt.Errorf("verify %s: no expected manifest", month)
	}
	var lastErr error
	for attempt := 1; attempt <= hfVerifyAttempts; attempt++ {
		lastErr = verifyOnceOnHuggingFace(ctx, repo, month, want, withSizes)
		if lastErr == nil {
			return nil
		}
		if attempt < hfVerifyAttempts {
			select {
			case <-time.After(hfVerifyPause):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}

// hfTreeEntry is one entry of the hub's /tree endpoint.
type hfTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Lfs  *struct {
		Oid  string `json:"oid"`
		Size *int64 `json:"size"`
	} `json:"lfs"`
}

func verifyOnceOnHuggingFace(ctx context.Context, repo, month string, want *Manifest, withSizes bool) error {
	token := os.Getenv("HF_TOKEN")
	client := &http.Client{Timeout: 2 * time.Minute}

	// 1) repo tree: the parquet files must be there with the exact LFS oid.
	url := hfEndpoint + "/api/datasets/" + repo + "/tree/main/" + month
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("hf tree %s: %w", month, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hf tree %s: status %d: %s", month, resp.StatusCode, bytes.TrimSpace(body))
	}
	var tree []hfTreeEntry
	if err := json.Unmarshal(body, &tree); err != nil {
		return fmt.Errorf("hf tree %s decode: %w", month, err)
	}
	entries := map[string]hfTreeEntry{}
	for _, e := range tree {
		entries[e.Path] = e
	}
	if err := checkTreeFile(entries, month+"/stop_events.parquet", want.StopEvents.Sha256, want.StopEvents.SizeBytes, withSizes); err != nil {
		return err
	}
	if want.ScrapeRuns.Sha256 != "" {
		if err := checkTreeFile(entries, month+"/scrape_runs.parquet", want.ScrapeRuns.Sha256, want.ScrapeRuns.SizeBytes, withSizes); err != nil {
			return err
		}
	}

	// 2) published manifest: sha256 and row counts must match.
	pub, err := DownloadPublishedManifest(ctx, month)
	if err != nil {
		return err
	}
	if pub.StopEvents.Sha256 != want.StopEvents.Sha256 {
		return fmt.Errorf("verify %s: published manifest stop_events sha256 %q, want %q", month, pub.StopEvents.Sha256, want.StopEvents.Sha256)
	}
	if pub.StopEvents.Rows != want.StopEvents.Rows {
		return fmt.Errorf("verify %s: published manifest stop_events rows %d, want %d", month, pub.StopEvents.Rows, want.StopEvents.Rows)
	}
	if pub.ScrapeRuns.Sha256 != want.ScrapeRuns.Sha256 {
		return fmt.Errorf("verify %s: published manifest scrape_runs sha256 %q, want %q", month, pub.ScrapeRuns.Sha256, want.ScrapeRuns.Sha256)
	}
	if pub.ScrapeRuns.Rows != want.ScrapeRuns.Rows {
		return fmt.Errorf("verify %s: published manifest scrape_runs rows %d, want %d", month, pub.ScrapeRuns.Rows, want.ScrapeRuns.Rows)
	}
	return nil
}

func checkTreeFile(entries map[string]hfTreeEntry, path, wantSha string, wantSize int64, withSizes bool) error {
	e, ok := entries[path]
	if !ok {
		return fmt.Errorf("verify: %s missing from repo tree", path)
	}
	if e.Lfs == nil {
		return fmt.Errorf("verify %s: file is not LFS (no lfs oid)", path)
	}
	if e.Lfs.Oid != wantSha {
		return fmt.Errorf("verify %s: lfs oid %q, want %q", path, e.Lfs.Oid, wantSha)
	}
	if withSizes {
		size := e.Size
		if e.Lfs.Size != nil {
			size = *e.Lfs.Size
		}
		if size != wantSize {
			return fmt.Errorf("verify %s: lfs size %d, want %d", path, size, wantSize)
		}
	}
	return nil
}
