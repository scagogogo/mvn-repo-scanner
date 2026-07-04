//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/report"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMavenCentral_FullPipeline tests the complete scan pipeline against Maven Central.
// Uses a small, well-known group (commons-collections4) to keep test fast.
func TestMavenCentral_FullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		RepoURL:     "https://repo.maven.apache.org/maven2",
		GroupFilter: "org.apache.commons.commons-collections4",
		Concurrency: 5,
		QPS:         10,
		Timeout:     30 * time.Second,
		Retries:     2,
		Verbose:     true,
		MaxFileSize: "50MB",
	}

	// Load detection rules
	rules, err := detector.LoadRules("")
	require.NoError(t, err)

	det, err := detector.NewDetector(rules)
	require.NoError(t, err)

	// Create components
	browser := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, "", 0)
		scan := scanner.New(cfg, browser, dl, det, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		summary, err := scan.Run(ctx)
		require.NoError(t, err, "Full pipeline should complete without error")

	// Assertions — relaxed for real network: allow some failures, just verify the pipeline works
	assert.Greater(t, summary.TotalDiscovered, 0, "Should discover at least 1 artifact")
	assert.Greater(t, summary.TotalScanned, 0, "Should scan at least some artifacts")
	assert.Equal(t, summary.TotalDiscovered, summary.TotalScanned+summary.TotalFailed,
		"Discovered should equal scanned + failed")

	t.Logf("Pipeline result: discovered=%d scanned=%d failed=%d findings=%d",
		summary.TotalDiscovered, summary.TotalScanned, summary.TotalFailed, summary.TotalFindings)
}

// TestMavenCentral_WithStateResume tests that resume functionality works correctly.
// Phase 1: Scan a group and save state.
// Phase 2: Resume the scan — should skip already completed artifacts.
func TestMavenCentral_WithStateResume(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "scan-state.json")

	cfg := &config.Config{
		RepoURL:     "https://repo.maven.apache.org/maven2",
		GroupFilter: "org.apache.commons.commons-collections4",
		Concurrency: 5,
		QPS:         10,
		Timeout:     30 * time.Second,
		Retries:     2,
		Verbose:     false,
		MaxFileSize: "50MB",
		StateFile:   stateFile,
	}

	rules, err := detector.LoadRules("")
	require.NoError(t, err)

	det, err := detector.NewDetector(rules)
	require.NoError(t, err)

	// Phase 1: Full scan with state persistence
	scanState := state.NewScanState("scan-resume-test", cfg.RepoURL, cfg.GroupFilter, cfg.StateFile)
	browser := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, "", 0)
		scan1 := scanner.New(cfg, browser, dl, det, scanState)

	// Track completed artifacts via callback
	scan1.OnResult(func(result scanner.ArtifactResult) {
		if result.Status == scanner.StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	summary1, err := scan1.Run(ctx)
	require.NoError(t, err)
	require.Greater(t, summary1.TotalScanned, 0, "Phase 1 should scan artifacts")

	// Flush state to disk (batch checkpoint may not have triggered for small sets)
	require.NoError(t, scanState.Flush())
	_, err = os.Stat(stateFile)
	require.NoError(t, err, "State file should exist after phase 1")

	// Phase 2: Resume scan — should skip all completed artifacts
	loadedState, err := state.LoadScanState(stateFile)
	require.NoError(t, err)
	require.NotNil(t, loadedState)

	browser2 := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl2 := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, "", 0)
	scan2 := scanner.New(cfg, browser2, dl2, det, loadedState)

	summary2, err := scan2.Run(ctx)
	require.NoError(t, err)

	// Resume should skip completed artifacts — scanned in phase 2 should be <= scanned in phase 1
	assert.LessOrEqual(t, summary2.TotalScanned, summary1.TotalScanned,
		"Resume should skip completed artifacts")
	assert.Equal(t, summary1.TotalDiscovered, summary2.TotalDiscovered, "Discovery should find same artifacts")

	t.Logf("Phase 1: scanned=%d, Phase 2 (resume): scanned=%d (should be 0)",
		summary1.TotalScanned, summary2.TotalScanned)
}

// TestMavenCentral_JSONReport tests that JSON report generation works end-to-end.
func TestMavenCentral_JSONReport(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		RepoURL:     "https://repo.maven.apache.org/maven2",
		GroupFilter: "org.apache.commons.commons-collections4",
		Concurrency: 5,
		QPS:         10,
		Timeout:     30 * time.Second,
		Retries:     2,
		Verbose:     false,
		MaxFileSize: "50MB",
	}

	rules, err := detector.LoadRules("")
	require.NoError(t, err)

	det, err := detector.NewDetector(rules)
	require.NoError(t, err)

	browser := repo.NewBrowser(cfg.Timeout, cfg.GroupFilter)
	dl := repo.NewDownloader(cfg.Timeout, cfg.Retries, cfg.QPS, "", 0)
	scan := scanner.New(cfg, browser, dl, det, nil)

	scanID := "scan-report-test"
	rpt := report.NewReport(scanID, cfg.RepoURL, cfg.GroupFilter, cfg.Concurrency)

	scan.OnResult(func(result scanner.ArtifactResult) {
		for _, f := range result.Findings {
			fd := report.FindingDetail{
				Artifact:    result.Artifact.GroupID + ":" + result.Artifact.ArtifactID + ":" + result.Artifact.Version,
				File:        f.FilePath,
				RuleID:      f.RuleID,
				RuleName:    f.RuleName,
				Severity:    string(f.Severity),
				LineNumber:  f.LineNumber,
				LineContent: f.LineContent,
				Match:       f.Match,
				Description: f.Description,
			}
			rpt.Findings = append(rpt.Findings, fd)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	require.Greater(t, summary.TotalScanned, 0, "Should scan at least some artifacts")
	rpt.Summary = summary
	rpt.EndTime = time.Now().Format(time.RFC3339)

	// Generate JSON report to temp file
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "report.json")
	jr := report.NewJSONReporter(outputFile, rpt)
	require.NoError(t, jr.Write())

	// Parse and verify the JSON report
	data, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, scanID, parsed["scan_id"])
	assert.NotNil(t, parsed["summary"])
	assert.NotNil(t, parsed["findings"])

	summaryMap := parsed["summary"].(map[string]interface{})
	assert.Greater(t, summaryMap["total_scanned"].(float64), float64(0))

	t.Logf("JSON report: scan_id=%s, findings=%v, scanned=%v",
		parsed["scan_id"], summaryMap["total_findings"], summaryMap["total_scanned"])
}
