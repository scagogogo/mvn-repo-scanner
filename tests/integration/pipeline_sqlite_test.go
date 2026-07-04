//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipeline_WithSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	tmpDir := t.TempDir()
	ws, err := storage.NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	store, err := storage.OpenStore(ws.DBPath)
	require.NoError(t, err)
	defer store.Close()

	cfg := &config.Config{
		RepoURL:     "https://repo.maven.apache.org/maven2",
		GroupFilter: "org.apache.commons.commons-collections4",
		Concurrency: 5,
		QPS:         10,
		Timeout:     120 * time.Second,
		Retries:     2,
		Verbose:     true,
		MaxFileSize: "50MB",
	}

	rules, err := detector.LoadRulesWithLevel("", "all", false)
	require.NoError(t, err)

	det, err := detector.NewDetector(rules)
	require.NoError(t, err)

	browser := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, ws.CacheDir, 0)
	scan := scanner.New(cfg, browser, dl, det, nil)

	scan.OnResult(func(result scanner.ArtifactResult) {
		status := storage.DBStatusComplete
		errMsg := ""
		if result.Status == scanner.StatusFailed {
			status = storage.DBStatusFailed
			errMsg = result.Error.Error()
		}
		rec := &storage.GAVRecord{
			GroupID:    result.Artifact.GroupID,
			ArtifactID: result.Artifact.ArtifactID,
			Version:    result.Artifact.Version,
			RepoURL:    cfg.RepoURL,
			Status:     status,
			Findings:   len(result.Findings),
			ScanTime:   time.Now(),
		}
		if errMsg != "" {
			rec.Error = errMsg
		}
		store.UpsertRecord(rec)

		if got, _ := store.GetRecord(rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL); got != nil {
			for _, f := range result.Findings {
				store.InsertFinding(&storage.FindingRecord{
					RecordID:    got.ID,
					RuleID:      f.RuleID,
					RuleName:    f.RuleName,
					Severity:    string(f.Severity),
					FilePath:    f.FilePath,
					LineNumber:  f.LineNumber,
					LineContent: f.LineContent,
					Match:       f.Match,
				})
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Greater(t, summary.TotalScanned, 0)

	stats, err := store.GetStats()
	require.NoError(t, err)
	assert.Greater(t, stats.TotalRecords, 0)
	assert.Equal(t, stats.TotalRecords, stats.Completed+stats.Failed)

	t.Logf("Pipeline+SQLite: discovered=%d scanned=%d failed=%d db_records=%d db_findings=%d",
		summary.TotalDiscovered, summary.TotalScanned, summary.TotalFailed,
		stats.TotalRecords, stats.TotalFindings)
}

func TestSQLite_Deduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	tmpDir := t.TempDir()
	ws, err := storage.NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	store, err := storage.OpenStore(ws.DBPath)
	require.NoError(t, err)
	defer store.Close()

	cfg := &config.Config{
		RepoURL:     "https://repo.maven.apache.org/maven2",
		GroupFilter: "org.apache.commons.commons-collections4",
		Concurrency: 5,
		QPS:         10,
		Timeout:     120 * time.Second,
		Retries:     2,
		Verbose:     false,
		MaxFileSize: "50MB",
	}

	rules, err := detector.LoadRulesWithLevel("", "core", false)
	require.NoError(t, err)

	det, err := detector.NewDetector(rules)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	browser := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, ws.CacheDir, 0)
	scan1 := scanner.New(cfg, browser, dl, det, nil)

	scan1.OnResult(func(result scanner.ArtifactResult) {
		status := storage.DBStatusComplete
		if result.Status == scanner.StatusFailed {
			status = storage.DBStatusFailed
		}
		store.UpsertRecord(&storage.GAVRecord{
			GroupID:    result.Artifact.GroupID,
			ArtifactID: result.Artifact.ArtifactID,
			Version:    result.Artifact.Version,
			RepoURL:    cfg.RepoURL,
			Status:     status,
			Findings:   len(result.Findings),
			ScanTime:   time.Now(),
		})
	})

	summary1, err := scan1.Run(ctx)
	require.NoError(t, err)
	require.Greater(t, summary1.TotalScanned, 0)

	// Check SQLite deduplication — verify a known GAV exists
	scanned, err := store.IsScanned("org.apache.commons", "commons-collections4", "4.4", cfg.RepoURL)
	require.NoError(t, err)
	assert.True(t, scanned, "Should find scanned GAV in database")

	t.Logf("Dedup test: scanned=%d, isScanned(4.4)=%v", summary1.TotalScanned, scanned)
}
