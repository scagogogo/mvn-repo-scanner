package scanner

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSummary(t *testing.T) {
	s := NewSummary()
	assert.Equal(t, 0, s.TotalFindings)
	assert.NotNil(t, s.BySeverity)
	assert.NotNil(t, s.ByRule)
}

func TestSummary_AddFinding(t *testing.T) {
	s := NewSummary()
	s.AddFinding(detector.Finding{RuleID: "hardcoded-password", Severity: detector.SeverityCritical})
	s.AddFinding(detector.Finding{RuleID: "aws-secret-key", Severity: detector.SeverityHigh})
	s.AddFinding(detector.Finding{RuleID: "hardcoded-password", Severity: detector.SeverityCritical})

	assert.Equal(t, 3, s.TotalFindings)
	assert.Equal(t, 2, s.BySeverity[detector.SeverityCritical])
	assert.Equal(t, 1, s.BySeverity[detector.SeverityHigh])
	assert.Equal(t, 2, s.ByRule["hardcoded-password"])
	assert.Equal(t, 1, s.ByRule["aws-secret-key"])
}

func TestSummary_String(t *testing.T) {
	s := NewSummary()
	s.TotalDiscovered = 100
	s.TotalScanned = 95
	s.TotalFailed = 5
	s.AddFinding(detector.Finding{RuleID: "test", Severity: detector.SeverityCritical})

	output := s.String()
	assert.Contains(t, output, "Discovered: 100")
	assert.Contains(t, output, "Scanned:    95")
	assert.Contains(t, output, "CRITICAL: 1")
}

func TestIsScannableFile(t *testing.T) {
	assert.True(t, isScannableFile("application.properties"))
	assert.True(t, isScannableFile("config.yml"))
	assert.True(t, isScannableFile("settings.xml"))
	assert.False(t, isScannableFile("MyClass.class"))
	assert.False(t, isScannableFile("image.png"))
}

func TestIsScannableFile_ExtendedExtensions(t *testing.T) {
	// 规则目标扩展名
	assert.True(t, isScannableFile("build.gradle"))
	assert.True(t, isScannableFile(".npmrc"))
	assert.True(t, isScannableFile("server.pem"))
	assert.True(t, isScannableFile("id_rsa.key"))
	assert.True(t, isScannableFile("keystore.jks"))
	assert.True(t, isScannableFile("deploy.sh"))
	assert.True(t, isScannableFile("README.md"))
	assert.True(t, isScannableFile("Main.java"))
	assert.True(t, isScannableFile("config.policy"))
	assert.True(t, isScannableFile("settings.xml.bak"))
	// 仍然排除二进制
	assert.False(t, isScannableFile("App.class"))
	assert.False(t, isScannableFile("image.png"))
}

func TestScanStatus_IsValid(t *testing.T) {
	assert.True(t, StatusPending.IsValid())
	assert.True(t, StatusScanning.IsValid())
	assert.True(t, StatusComplete.IsValid())
	assert.True(t, StatusFailed.IsValid())
	assert.False(t, ScanStatus("unknown").IsValid())
}

func TestArtifactResult_String(t *testing.T) {
	r := ArtifactResult{
		Artifact: repo.Artifact{GroupID: "com.example", ArtifactID: "mylib", Version: "1.0"},
		Status:   StatusComplete,
		Findings: []detector.Finding{{RuleID: "test"}},
	}
	assert.Equal(t, "com.example:mylib:1.0 complete (1 findings)", r.String())
}

// mockDownloader tracks cleanup calls for testing panic-safe cleanup
type mockDownloader struct {
	mu        sync.Mutex
	downloaded []string
	cleaned    []string
}

func (m *mockDownloader) Download(_ context.Context, artifact repo.Artifact) (*repo.DownloadResult, error) {
	tmpDir, _ := os.MkdirTemp("", "scanner-test-*")
	localPath := filepath.Join(tmpDir, artifact.FileName)
	// For archive extensions, write a valid (empty) zip so scanArchive succeeds.
	if strings.HasSuffix(artifact.FileName, ".jar") ||
		strings.HasSuffix(artifact.FileName, ".war") ||
		strings.HasSuffix(artifact.FileName, ".ear") {
		writeEmptyZipFile(localPath)
	} else {
		os.WriteFile(localPath, []byte("test"), 0644)
	}
	m.mu.Lock()
	m.downloaded = append(m.downloaded, localPath)
	m.mu.Unlock()
	return &repo.DownloadResult{Artifact: artifact, LocalPath: localPath}, nil
}

func (m *mockDownloader) Cleanup(localPath string) {
	os.Remove(localPath)
	m.mu.Lock()
	m.cleaned = append(m.cleaned, localPath)
	m.mu.Unlock()
}

// writeEmptyZipFile writes a valid (empty) zip archive to path so that
// scanArchive's zip.OpenReader succeeds in tests.
func writeEmptyZipFile(path string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	w := zip.NewWriter(f)
	w.Close()
}

// mockBrowser returns a fixed set of artifacts
type mockBrowser struct {
	artifacts []repo.Artifact
}

func (m *mockBrowser) Discover(_ context.Context, _ string) ([]repo.Artifact, error) {
	return m.artifacts, nil
}

// mockBrowserError always fails discovery
type mockBrowserError struct{}

func (m *mockBrowserError) Discover(_ context.Context, _ string) ([]repo.Artifact, error) {
	return nil, fmt.Errorf("simulated discovery failure")
}

// mockDetector scans content
type mockDetector struct {
	panicOnFile string // if set, panics when scanning this file
	findings    []detector.Finding
}

func (m *mockDetector) ScanFile(filePath string) ([]detector.Finding, error) {
	if m.panicOnFile != "" && filepath.Base(filePath) == m.panicOnFile {
		panic("intentional test panic")
	}
	return m.findings, nil
}

func (m *mockDetector) ScanContent(_ io.Reader, _ string) ([]detector.Finding, error) {
	return m.findings, nil
}

func TestScanner_PanicSafeCleanup(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}

	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "test",
		Version: "1.0", FileName: "panic-trigger.txt",
		DownloadURL: "https://repo.example.com/test.txt",
	}

	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{panicOnFile: "panic-trigger.txt"}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
	)

	// The scan should not hang — processJob recovers from panic via defer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run in a goroutine since it may panic in a sub-goroutine
	done := make(chan *Summary, 1)
	errCh := make(chan error, 1)
	go func() {
		summary, err := scan.Run(ctx)
		if err != nil {
			errCh <- err
		} else {
			done <- summary
		}
	}()

	select {
	case summary := <-done:
		// Cleanup should have been called even on panic
		assert.Equal(t, 1, len(dl.cleaned), "Cleanup should be called even on panic")
		_ = summary
	case err := <-errCh:
		t.Fatalf("Scan should not return error for panicking scan: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Scan should not hang on panic — processJob should recover")
	}
}

func TestScanner_DiskWatcherIntegration(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 2,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}

	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "test",
		Version: "1.0", FileName: "test.txt",
		DownloadURL: "https://repo.example.com/test.txt",
	}

	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}

	// Small disk budget — should still complete
	dw := NewDiskWatcher(10 * 1024 * 1024) // 10MB

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithDiskWatcher(dw),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalScanned)
	assert.Equal(t, int64(0), dw.Current(), "DiskWatcher should be back to 0 after all releases")
}

func TestScanner_ResumeSkipsCompleted(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 2,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}

	artifacts := []repo.Artifact{
		{GroupID: "com.example", ArtifactID: "lib-a", Version: "1.0", FileName: "lib-a-1.0.txt", DownloadURL: "https://repo.example.com/a.txt"},
		{GroupID: "com.example", ArtifactID: "lib-b", Version: "2.0", FileName: "lib-b-2.0.txt", DownloadURL: "https://repo.example.com/b.txt"},
		{GroupID: "com.example", ArtifactID: "lib-c", Version: "3.0", FileName: "lib-c-3.0.txt", DownloadURL: "https://repo.example.com/c.txt"},
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithCheckpoint("scan-resume", "https://repo.example.com", "", stateFile, 0)

	// Mark first artifact as already completed
	scanState.MarkCompleted("com/example/lib-a/1.0")

	browser := &mockBrowser{artifacts: artifacts}
	dl := &mockDownloader{}
	det := &mockDetector{}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)

	// Only 2 should be scanned (lib-a was already completed)
	assert.Equal(t, 3, summary.TotalDiscovered, "should discover all 3 artifacts")
	assert.Equal(t, 2, summary.TotalScanned, "should only scan 2 (skipping completed)")
}

func TestScanner_DiscoveryCache(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
		Verbose:     true,
	}

	artifacts := []repo.Artifact{
		{GroupID: "com.example", ArtifactID: "lib-a", Version: "1.0", FileName: "lib-a-1.0.txt", DownloadURL: "https://repo.example.com/a.txt"},
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithCheckpoint("scan-cache", "https://repo.example.com", "", stateFile, 0)

	// Pre-populate discovery cache
	scanState.SetDiscoveredArtifacts([]string{"com/example/lib-a/1.0"})
	require.True(t, scanState.HasDiscoveryCache())

	browser := &mockBrowser{artifacts: artifacts}
	dl := &mockDownloader{}
	det := &mockDetector{}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalDiscovered)
}

func TestScanner_RediscoverForcesFreshDiscovery(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}

	artifacts := []repo.Artifact{
		{GroupID: "com.example", ArtifactID: "lib-a", Version: "1.0", FileName: "lib-a-1.0.txt", DownloadURL: "https://repo.example.com/a.txt"},
		{GroupID: "com.example", ArtifactID: "lib-b", Version: "2.0", FileName: "lib-b-2.0.txt", DownloadURL: "https://repo.example.com/b.txt"},
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithCheckpoint("scan-rediscover", "https://repo.example.com", "", stateFile, 0)

	// Pre-populate with stale cache (only 1 artifact)
	scanState.SetDiscoveredArtifacts([]string{"com/example/lib-a/1.0"})
	require.True(t, scanState.HasDiscoveryCache())

	browser := &mockBrowser{artifacts: artifacts} // browser returns 2 artifacts
	dl := &mockDownloader{}
	det := &mockDetector{}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithRediscover(), // Force re-discovery
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, summary.TotalDiscovered, "rediscover should use fresh discovery, not cache")
}

func TestScanner_InFlightMarking(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}

	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "test",
		Version: "1.0", FileName: "test.txt",
		DownloadURL: "https://repo.example.com/test.txt",
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithCheckpoint("scan-inflight", "https://repo.example.com", "", stateFile, 0)

	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
	)

	// Register OnResult callback that marks completed/failed (like cmd/scan.go does)
	scan.OnResult(func(result ArtifactResult) {
		artifactPath := result.Artifact.Path()
		if result.Status == StatusComplete {
			scanState.MarkCompleted(artifactPath)
		} else {
			scanState.MarkFailed(artifactPath, result.Error.Error(), 0)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalScanned)

	// After completion, artifact should be completed and NOT in-flight
	require.NoError(t, scanState.Flush())
	assert.True(t, scanState.IsCompleted("com/example/test/1.0"))
	assert.False(t, scanState.IsInFlight("com/example/test/1.0"))
	assert.Equal(t, 0, scanState.InFlightCount())
}

func TestScanner_FailedRetryOnResume(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
		Verbose:     true,
	}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithCheckpoint("scan-retry", "https://repo.example.com", "", stateFile, 0)

	// Simulate a previously failed artifact
	scanState.MarkFailed("com/example/lib-a/1.0", "HTTP 503", 1)

	// Discovery returns the same artifact
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "lib-a", Version: "1.0",
		FileName: "lib-a-1.0.txt", DownloadURL: "https://repo.example.com/a.txt",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}

	scan := NewScanner(cfg,
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithRetryFailed(3), // Retry failed artifacts with max 3 retries
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// The artifact should have been retried (it's in the discovery list anyway)
	assert.Equal(t, 1, summary.TotalScanned)
}

// mockPageFetcher serves a small Maven tree for streaming discovery tests.
type mockPageFetcher struct {
	pages map[string]string

	mu         sync.Mutex
	fetchCount map[string]int // url -> number of FetchPage calls (lazily allocated)
	// failBeforeSuccess maps a url to the number of times FetchPage should
	// return an error BEFORE serving the page. Each call decrements; once it
	// reaches 0 the page is served normally. Simulates transient fetch
	// failures for testing resume re-visit of failed directories.
	failBeforeSuccess map[string]int
}

func (m *mockPageFetcher) FetchPage(_ context.Context, url string) (string, error) {
	m.mu.Lock()
	if m.fetchCount == nil {
		m.fetchCount = make(map[string]int)
	}
	m.fetchCount[url]++
	if remains, ok := m.failBeforeSuccess[url]; ok && remains > 0 {
		m.failBeforeSuccess[url] = remains - 1
		m.mu.Unlock()
		return "", fmt.Errorf("HTTP 503 (simulated transient): %s", url)
	}
	m.mu.Unlock()

	html, ok := m.pages[url]
	if !ok {
		return "", fmt.Errorf("HTTP 404: %s", url)
	}
	return html, nil
}

// fetches returns the number of times a URL has been fetched.
func (m *mockPageFetcher) fetches(url string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fetchCount[url]
}

// newStreamingMockRepo builds a tree with one file per version so each GAV
// maps to exactly one artifact path (avoids same-GAV multi-file dedup noise
// in resume tests). Tree:
//
//	com/example/lib/{1.0,2.0}            -> lib-1.0.jar, lib-2.0.jar
//	org/apache/commons/1.0               -> commons-1.0.jar
//	org/apache/commons/2.0               -> commons-2.0.jar
//
// 4 distinct GAVs total.
func newStreamingMockRepo() *mockPageFetcher {
	base := "https://repo.example.com/maven2"
	return &mockPageFetcher{pages: map[string]string{
		base + "/":                          `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
		base + "/com/":                      `<a href="../">../</a><a href="example/">example/</a>`,
		base + "/com/example/":              `<a href="../">../</a><a href="lib/">lib/</a>`,
		base + "/com/example/lib/":          `<a href="../">../</a><a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`,
		base + "/com/example/lib/1.0/":      `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
		base + "/com/example/lib/2.0/":      `<a href="../">../</a><a href="lib-2.0.jar">lib-2.0.jar</a>`,
		base + "/org/":                      `<a href="../">../</a><a href="apache/">apache/</a>`,
		base + "/org/apache/":               `<a href="../">../</a><a href="commons/">commons/</a>`,
		base + "/org/apache/commons/":       `<a href="../">../</a><a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`,
		base + "/org/apache/commons/1.0/":   `<a href="../">../</a><a href="commons-1.0.jar">commons-1.0.jar</a>`,
		base + "/org/apache/commons/2.0/":   `<a href="../">../</a><a href="commons-2.0.jar">commons-2.0.jar</a>`,
	}}
}

func TestScanner_StreamingDiscoveryResumable(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1, // checkpoint cursor every artifact (so a cursor is saved early)
	}

	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-stream", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	// Phase 1: scan ONE artifact, then cancel. We mark it completed via callback
	// and rely on the cursor being checkpointed so discovery resumes mid-tree.
	// 用 selectiveBlockDownloader 阻塞第 2 个 artifact（com/example/lib/2.0）的下载，
	// 确保 cancel 时第 2 个处于 in-flight（已 yield 未完成），消除「cancel 前 worker 多扫」
	// 的时序竞态——该竞态在全包高并发 race 下偶发导致 phase1 扫 1~2 个波动。
	var scanned1 []string
	var phase1Done = make(chan struct{})
	dl1 := &selectiveBlockDownloader{
		mockDownloader: mockDownloader{},
		blockPath:      "com/example/lib/2.0",
		blocked:        make(chan struct{}),
	}
	scan1 := NewScanner(cfg,
		WithDownloader(dl1),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithCursorSaver(scanState),
		WithPageFetcher(fetcher),
		WithStreamingDiscovery(),
	)
	scan1.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned1 = append(scanned1, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
			// Cancel after the first completed artifact.
			select {
			case phase1Done <- struct{}{}:
			default:
			}
		}
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		<-phase1Done
		cancel1()
	}()
	scan1.Run(ctx1)
	require.GreaterOrEqual(t, len(scanned1), 1, "phase 1 should scan at least 1")
	t.Logf("Phase 1: scanned=%v", scanned1)

	require.NoError(t, scanState.Flush())

	// Phase 2: resume. The completed artifact from phase 1 should be skipped;
	// the remaining artifacts should be scanned exactly once.
	scan2 := NewScanner(cfg,
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithCursorSaver(scanState),
		WithPageFetcher(fetcher),
		WithStreamingDiscovery(),
	)
	var scanned2 []string
	scan2.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned2 = append(scanned2, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	summary2, err := scan2.Run(ctx2)
	require.NoError(t, err)
	t.Logf("Phase 2 (resume): scanned=%v, discovered=%d", scanned2, summary2.TotalDiscovered)

	// Combined unique should be exactly 4 (all artifacts, no duplicates).
	all := append(append([]string{}, scanned1...), scanned2...)
	unique := make(map[string]bool)
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "resume should cover all 4 artifacts exactly once: %v", all)

	// No artifact scanned in both phases.
	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s scanned %d times (should be 1)", p, c)
	}
}

func TestScanner_StreamingDiscoveryFullRun(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        2,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}

	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-full", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	var scanned []string
	scan := NewScanner(cfg,
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithCursorSaver(scanState),
		WithPageFetcher(fetcher),
		WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned = append(scanned, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, summary.TotalScanned, "should scan all 4 artifacts")
	assert.Equal(t, 4, len(scanned))
	// After full run, cursor should be cleared (discovery complete)
	require.NoError(t, scanState.Flush())
	assert.False(t, scanState.HasDiscoveryCursor(), "cursor should be cleared after discovery completes")
}

// TestScanner_StreamingDiscoveryInFlightNotLost verifies that an artifact
// which has been yielded into the channel buffer but not yet scanned when a
// cancellation arrives is NOT lost on resume. This is the core guarantee of
// the cursor-rollback logic: in-flight artifacts are rediscovered by rolling
// the cursor back to their directory (or an ancestor) so resume re-walks it.
//
// We force the race-yielding scenario by running with Concurrency=1 and a
// CheckpointInterval that lets discovery race ahead of scanning, then cancel
// after the first completion. Across many runs (run with -count) this covers
// both "in-flight dir still on stack" and "in-flight dir already popped" cases.
func TestScanner_StreamingDiscoveryInFlightNotLost(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}

	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-inflight", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	// Phase 1: cancel as soon as the first artifact completes.
	var scanned1 []string
	var phase1Done = make(chan struct{})
	scan1 := NewScanner(cfg,
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithCursorSaver(scanState),
		WithPageFetcher(fetcher),
		WithStreamingDiscovery(),
	)
	scan1.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned1 = append(scanned1, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
			select {
			case phase1Done <- struct{}{}:
			default:
			}
		}
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		<-phase1Done
		cancel1()
	}()
	scan1.Run(ctx1)
	require.GreaterOrEqual(t, len(scanned1), 1)
	require.NoError(t, scanState.Flush())

	// Phase 2: resume and run to completion. Every artifact must be covered
	// exactly once across both phases — no in-flight artifact lost.
	scan2 := NewScanner(cfg,
		WithDownloader(dl),
		WithDetector(det),
		WithState(scanState),
		WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState),
		WithCursorSaver(scanState),
		WithPageFetcher(fetcher),
		WithStreamingDiscovery(),
	)
	var scanned2 []string
	scan2.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned2 = append(scanned2, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	scan2.Run(ctx2)

	all := append(append([]string{}, scanned1...), scanned2...)
	unique := make(map[string]bool)
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "all 4 artifacts must be covered across phases (phase1=%v, phase2=%v)", scanned1, scanned2)

	// No artifact scanned twice.
	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s scanned %d times", p, c)
	}

	// After completing, in-flight set must be empty (everything was processed).
	assert.Equal(t, 0, scanState.InFlightCount(), "in-flight set should be drained after full resume")
}

// TestScanner_FailedDirRetriedOnResume verifies the fix for the silent-skip
// defect: when a directory listing fetch fails transiently during discovery,
// the directory is recorded as failed (via the walker's onDirFailed callback)
// and re-visited on resume. Without this, the cursor advances past the
// failed directory and every artifact under it is permanently lost.
func TestScanner_FailedDirRetriedOnResume(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}

	base := "https://repo.example.com/maven2"
	// org/apache/commons/1.0/ fails once on the first run, then succeeds.
	fetcher := &mockPageFetcher{pages: map[string]string{
		base + "/":                          `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
		base + "/com/":                      `<a href="../">../</a><a href="example/">example/</a>`,
		base + "/com/example/":              `<a href="../">../</a><a href="lib/">lib/</a>`,
		base + "/com/example/lib/":          `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		base + "/com/example/lib/1.0/":      `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
		base + "/org/":                      `<a href="../">../</a><a href="apache/">apache/</a>`,
		base + "/org/apache/":               `<a href="../">../</a><a href="commons/">commons/</a>`,
		base + "/org/apache/commons/":       `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		base + "/org/apache/commons/1.0/":   `<a href="../">../</a><a href="commons-1.0.jar">commons-1.0.jar</a>`,
	}}
	fetcher.failBeforeSuccess = map[string]int{base + "/org/apache/commons/1.0/": 1}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-faildir", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	// Phase 1: run to completion. The transient 503 on commons/1.0/ means that
	// directory is skipped and recorded as failed during discovery. Its one
	// artifact is NOT scanned in this phase.
	var scanned1 []string
	scan1 := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan1.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned1 = append(scanned1, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	scan1.Run(ctx1)
	require.NoError(t, scanState.Flush())

	// The failed directory must be persisted.
	failedDirs := scanState.GetFailedDirs()
	require.Len(t, failedDirs, 1, "commons/1.0 should be recorded as failed: %v", failedDirs)
	assert.Contains(t, failedDirs[0], "org/apache/commons/1.0")
	// And its artifact was NOT scanned in phase 1.
	for _, p := range scanned1 {
		assert.NotContains(t, p, "org/apache/commons/1.0",
			"failed-dir artifact must not be scanned in the failed run")
	}

	// Phase 2: resume. The fetch now succeeds, so the failed directory is
	// re-visited and its artifact scanned. The failed marker is cleared.
	scan2 := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var scanned2 []string
	scan2.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned2 = append(scanned2, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	scan2.Run(ctx2)
	require.NoError(t, scanState.Flush())

	assert.Empty(t, scanState.GetFailedDirs(), "failed dir should be cleared after successful re-visit")

	// The previously-lost artifact must now be scanned.
	all := append(append([]string{}, scanned1...), scanned2...)
	var foundLost bool
	for _, p := range all {
		if strings.Contains(p, "org/apache/commons/1.0") {
			foundLost = true
		}
	}
	assert.True(t, foundLost, "failed-dir artifact must be scanned on resume: %v", all)

	// No artifact scanned twice across phases.
	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s scanned %d times", p, c)
	}
}

// TestScanner_InFlightNotLostAcrossDisjointSubtrees verifies the fix for the
// rollback-to-root defect: when in-flight artifacts span disjoint subtrees
// (one under com/, one under org/), resume must re-yield BOTH without rolling
// the main cursor back to the repository root. We assert that the root page is
// NOT re-fetched a large number of times (which would indicate a full re-walk
// from the root).
func TestScanner_InFlightNotLostAcrossDisjointSubtrees(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}

	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-disjoint", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	// Phase 1: scan the first artifact (com/example/lib/1.0) then cancel.
	// At cancellation, other artifacts (including org/... ones) may be
	// in-flight in the channel buffer — spanning disjoint subtrees.
	var scanned1 []string
	var phase1Done = make(chan struct{})
	scan1 := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan1.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned1 = append(scanned1, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
			select {
			case phase1Done <- struct{}{}:
			default:
			}
		}
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		<-phase1Done
		cancel1()
	}()
	scan1.Run(ctx1)
	require.GreaterOrEqual(t, len(scanned1), 1)
	require.NoError(t, scanState.Flush())

	rootFetchesAfterPhase1 := fetcher.fetches(cfg.RepoURL + "/")

	// Phase 2: resume to completion. Both disjoint subtrees must be covered.
	scan2 := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var scanned2 []string
	scan2.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanned2 = append(scanned2, result.Artifact.Path())
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	scan2.Run(ctx2)

	all := append(append([]string{}, scanned1...), scanned2...)
	unique := make(map[string]bool)
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "all 4 artifacts must be covered across phases: %v", all)

	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s scanned %d times", p, c)
	}

	// The fix's key guarantee: resume does NOT re-walk from the root. The root
	// page should be fetched at most a small constant number of times across
	// both phases (re-visit pass + main cursor resume), not O(scanned dirs).
	rootFetchesTotal := fetcher.fetches(cfg.RepoURL + "/")
	assert.LessOrEqual(t, rootFetchesTotal, rootFetchesAfterPhase1+2,
		"root page should not be re-fetched many times on resume (rollback-to-root defect): got %d",
		rootFetchesTotal-rootFetchesAfterPhase1)
}

// TestScanner_FailedDirPermanent404DoesNotBlock verifies that a directory
// which is permanently missing (404 on every run) does not block the scan:
// it is re-recorded as failed on each resume but every OTHER directory is
// still fully covered.
func TestScanner_FailedDirPermanent404DoesNotBlock(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}

	base := "https://repo.example.com/maven2"
	// org/apache/commons/1.0/ is permanently 404 (no entry in pages, no
	// failBeforeSuccess). com/example/lib/1.0 is valid.
	fetcher := &mockPageFetcher{pages: map[string]string{
		base + "/":                          `<a href="../">../</a><a href="com/">com/</a><a href="org/">org/</a>`,
		base + "/com/":                      `<a href="../">../</a><a href="example/">example/</a>`,
		base + "/com/example/":              `<a href="../">../</a><a href="lib/">lib/</a>`,
		base + "/com/example/lib/":          `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		base + "/com/example/lib/1.0/":      `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
		base + "/org/":                      `<a href="../">../</a><a href="apache/">apache/</a>`,
		base + "/org/apache/":               `<a href="../">../</a><a href="commons/">commons/</a>`,
		base + "/org/apache/commons/":       `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		// org/apache/commons/1.0/ intentionally absent -> permanent 404
	}}

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-404", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}

	runOnce := func() []string {
		var scanned []string
		scan := NewScanner(cfg,
			WithDownloader(dl), WithDetector(det),
			WithState(scanState), WithDiscoveryCacher(scanState),
			WithFailedRetryer(scanState), WithCursorSaver(scanState),
			WithPageFetcher(fetcher), WithStreamingDiscovery(),
		)
		scan.OnResult(func(result ArtifactResult) {
			if result.Status == StatusComplete {
				scanned = append(scanned, result.Artifact.Path())
				scanState.MarkCompleted(result.Artifact.Path())
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		scan.Run(ctx)
		require.NoError(t, scanState.Flush())
		return scanned
	}

	// Run twice (simulating two resumes). The permanent 404 dir must not block
	// and must remain recorded as failed (re-recorded on each re-visit attempt).
	runOnce()
	runOnce()

	// The valid artifact must be scanned exactly once (it is completed after
	// the first run and skipped thereafter).
	assert.True(t, scanState.IsCompleted("com/example/lib/1.0"),
		"valid artifact must be scanned despite a permanent 404 elsewhere")
	// The permanent 404 directory stays recorded as failed — it is retried on
	// every resume but never clears, which is correct (we never want to lose
	// the record of an unscannable subtree).
	failed := scanState.GetFailedDirs()
	assert.NotEmpty(t, failed, "permanent 404 dir should remain recorded as failed")
}

// ---- New / WithProgressCallback / WithCacheCleaner ----

func TestNew_DeprecatedConstructor(t *testing.T) {
	cfg := &config.Config{RepoURL: "https://x.example.com", Concurrency: 1, Timeout: 1 * time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	browser := repo.NewBrowser(0, "")
	dl := repo.NewDownloader(0, 0, 0, t.TempDir(), 0)
	det, err := detector.NewDetector(detector.DefaultRules())
	require.NoError(t, err)
	scanState := state.NewScanStateWithCheckpoint("t", "https://x.example.com", "", filepath.Join(t.TempDir(), "s.json"), 0)

	s := New(cfg, browser, dl, det, scanState)
	require.NotNil(t, s)
	assert.NotNil(t, s.browser)
	assert.NotNil(t, s.downloader)
	assert.NotNil(t, s.detector)
	assert.NotNil(t, s.state)
	assert.True(t, s.useStreamingDiscovery) // browser != nil → 启用 streaming
}

func TestNew_NilBrowserNoStreaming(t *testing.T) {
	cfg := &config.Config{RepoURL: "https://x.example.com", Concurrency: 1, Timeout: 1 * time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	dl := repo.NewDownloader(0, 0, 0, t.TempDir(), 0)
	det, err := detector.NewDetector(detector.DefaultRules())
	require.NoError(t, err)

	s := New(cfg, nil, dl, det, nil)
	require.NotNil(t, s)
	assert.False(t, s.useStreamingDiscovery) // browser=nil → 不启用
}

func TestWithProgressCallback(t *testing.T) {
	cfg := &config.Config{RepoURL: "https://x", Concurrency: 1, Timeout: 1 * time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	called := false
	cb := func(r ArtifactResult) { called = true }
	s := NewScanner(cfg, WithProgressCallback(cb))
	require.NotNil(t, s)
	assert.NotNil(t, s.onResult)
	s.onResult(ArtifactResult{})
	assert.True(t, called)
}

// mockCacheCleaner records EnforceCacheLimit calls.
type mockCacheCleaner struct{ called bool }

func (m *mockCacheCleaner) EnforceCacheLimit() error { m.called = true; return nil }

func TestWithCacheCleaner(t *testing.T) {
	cfg := &config.Config{RepoURL: "https://x", Concurrency: 1, Timeout: 1 * time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	cc := &mockCacheCleaner{}
	s := NewScanner(cfg, WithCacheCleaner(cc))
	require.NotNil(t, s)
	assert.NotNil(t, s.cacheCleaner)
	require.NoError(t, s.cacheCleaner.EnforceCacheLimit())
	assert.True(t, cc.called)
}

// ---- addRetryableFailed 分支 ----

func TestAddRetryableFailed_NoFailures(t *testing.T) {
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	s := &Scanner{cfg: &config.Config{}, failedRetryer: scanState, maxRetries: 3}
	in := []repo.Artifact{{GroupID: "a", ArtifactID: "b", Version: "1"}}
	out := s.addRetryableFailed(in)
	assert.Equal(t, len(in), len(out))
}

func TestAddRetryableFailed_AddsAndDedups(t *testing.T) {
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	// 记录一个失败 artifact：path = com/example/lib/1.0
	scanState.MarkFailed("com/example/lib/1.0", "HTTP 503", 1)

	s := &Scanner{cfg: &config.Config{}, failedRetryer: scanState, maxRetries: 3}
	// 已存在的 artifact（同 path）应被 dedup
	existing := []repo.Artifact{{GroupID: "com.example", ArtifactID: "lib", Version: "1.0"}}
	out := s.addRetryableFailed(existing)
	assert.Equal(t, 1, len(out), "duplicate path should not be added again")

	// 不存在的 artifact → 应被加入
	scanState.MarkFailed("org/other/pkg/2.0", "HTTP 500", 1)
	out2 := s.addRetryableFailed(existing)
	assert.Equal(t, 2, len(out2))
}

func TestAddRetryableFailed_BadPathSkipped(t *testing.T) {
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	// path 段数 < 3 → 应被跳过
	scanState.MarkFailed("only-two-parts", "err", 1)

	s := &Scanner{cfg: &config.Config{}, failedRetryer: scanState, maxRetries: 3}
	out := s.addRetryableFailed(nil)
	assert.Equal(t, 0, len(out), "path with <3 segments should be skipped")
}

func TestAddRetryableFailed_VerboseLog(t *testing.T) {
	// cfg.Verbose=true && added > 0 → 覆盖 line 765-767 的 log 分支
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	scanState.MarkFailed("com/example/lib/1.0", "HTTP 503", 1)

	s := &Scanner{cfg: &config.Config{Verbose: true}, failedRetryer: scanState, maxRetries: 3}
	out := s.addRetryableFailed(nil)
	assert.Equal(t, 1, len(out))
}

func TestPathsToArtifacts(t *testing.T) {
	s := &Scanner{cfg: &config.Config{}}
	// 正常 path
	out := s.pathsToArtifacts([]string{"com/example/lib/1.0"})
	require.Equal(t, 1, len(out))
	assert.Equal(t, "com.example", out[0].GroupID)
	assert.Equal(t, "lib", out[0].ArtifactID)
	assert.Equal(t, "1.0", out[0].Version)
	// 段数 < 3 → 跳过（line 778-780）
	out2 := s.pathsToArtifacts([]string{"only-two"})
	assert.Equal(t, 0, len(out2))
	// 空
	assert.Equal(t, 0, len(s.pathsToArtifacts(nil)))
}

func TestScanDownloadedArtifact_ArchiveScanFails(t *testing.T) {
	// 损坏的 .jar（非 zip）→ scanArchiveFile 报错 → StatusFailed
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.jar")
	require.NoError(t, os.WriteFile(path, []byte("not a zip"), 0644))

	det, err := detector.NewDetector(detector.DefaultRules())
	require.NoError(t, err)
	s := &Scanner{detector: det, cfg: &config.Config{}}

	res := s.scanDownloadedArtifact(context.Background(), downloadJob{
		artifact:  repo.Artifact{GroupID: "g", ArtifactID: "a", Version: "1", FileName: "broken.jar", DownloadURL: "u"},
		localPath: path,
	})
	assert.Equal(t, StatusFailed, res.Status)
	assert.Error(t, res.Error)
}

func TestScanDownloadedArtifact_PlainFileOK(t *testing.T) {
	// 非 archive → detector.ScanFile 路径
	dir := t.TempDir()
	path := filepath.Join(dir, "app.properties")
	require.NoError(t, os.WriteFile(path, []byte("db.password = ok-secret\n"), 0644))

	det, err := detector.NewDetector(detector.DefaultRules())
	require.NoError(t, err)
	s := &Scanner{detector: det, cfg: &config.Config{}}

	res := s.scanDownloadedArtifact(context.Background(), downloadJob{
		artifact:  repo.Artifact{GroupID: "g", ArtifactID: "a", Version: "1", FileName: "app.properties", DownloadURL: "u"},
		localPath: path,
	})
	assert.Equal(t, StatusComplete, res.Status)
	assert.NoError(t, res.Error)
	assert.NotEmpty(t, res.Findings)
}

// TestIsArchiveFile 已在 archive_test.go 中定义，此处不重复。

func TestSaveCursorFrom_EmptyClears(t *testing.T) {
	// 空 cursor → ClearDiscoveryCursor（line 645-649）
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	// 先设一个 cursor
	scanState.SetDiscoveryCursor([]state.CursorFrameJSON{{DirPath: "com", NextIdx: 0}})
	require.True(t, scanState.HasDiscoveryCursor())

	s := &Scanner{cfg: &config.Config{}, cursorSaver: scanState}
	s.saveCursorFrom(nil) // 空 cursor → 清除
	assert.False(t, scanState.HasDiscoveryCursor())
}

func TestSaveCursorFrom_NilCursorSaver(t *testing.T) {
	// cursorSaver=nil → 直接 return（line 641-643）
	s := &Scanner{cfg: &config.Config{}, cursorSaver: nil}
	s.saveCursorFrom(repo.RootCursor()) // 不 panic
}

func TestSaveCursorFrom_Persists(t *testing.T) {
	// 非空 cursor → SetDiscoveryCursor（line 650-654）
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	s := &Scanner{cfg: &config.Config{}, cursorSaver: scanState}
	s.saveCursorFrom(repo.Cursor{{DirPath: "com/example", NextIdx: 2}})
	assert.True(t, scanState.HasDiscoveryCursor())
}

func TestDiscoverArtifacts_BrowserFails(t *testing.T) {
	// browser.Discover 失败 → discoverArtifacts 返回 err（line 692-694）
	s := &Scanner{
		cfg:     &config.Config{RepoURL: "https://x"},
		browser: &mockBrowserError{},
	}
	_, err := s.discoverArtifacts(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated discovery failure")
}

func TestDiscoverArtifacts_CacheHit(t *testing.T) {
	// discoveryCacher 有缓存 && !rediscover → 走缓存分支（line 677-683）
	scanState := state.NewScanStateWithCheckpoint("t", "https://x", "", filepath.Join(t.TempDir(), "s.json"), 0)
	scanState.SetDiscoveredArtifacts([]string{"com/example/lib/1.0"})
	s := &Scanner{
		cfg:              &config.Config{RepoURL: "https://x", Verbose: true},
		browser:          &mockBrowserError{}, // 缓存命中不应调用 browser
		discoveryCacher:  scanState,
	}
	out, err := s.discoverArtifacts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, len(out))
	assert.Equal(t, "com.example", out[0].GroupID)
}

func TestDiscoverBatched_DiscoveryFails(t *testing.T) {
	// discoverArtifacts 失败 → discoverBatched log + return（line 405-408）
	s := &Scanner{
		cfg:     &config.Config{RepoURL: "https://x"},
		browser: &mockBrowserError{},
	}
	summary := NewSummary()
	ch := make(chan repo.Artifact, 1)
	s.discoverBatched(context.Background(), ch, summary)
	close(ch)
	// 失败后应不发送 artifact
	assert.Equal(t, 0, len(ch))
}

func TestRevisitPendingDirs_VerboseAndCanceled(t *testing.T) {
	// state 含 failed dir → pending 非空 → Verbose log（line 601-603）
	// ctx 已取消 → 循环内 ctx.Done return（line 607-609）
	scanState := state.NewScanStateWithCheckpoint("t", "https://repo.example.com/maven2", "", filepath.Join(t.TempDir(), "s.json"), 0)
	scanState.MarkDirFailed("com/example/lib/1.0") // pending dir

	fetcher := newStreamingMockRepo()
	s := &Scanner{
		cfg:    &config.Config{RepoURL: "https://repo.example.com/maven2", Verbose: true},
		state:  scanState,
	}
	walker := repo.NewCursorWalker(fetcher, s.cfg.RepoURL)
	yield := func(repo.Artifact) {}
	shouldStop := func() bool { return false }

	// ctx 已取消 → revisitPendingDirs 在第一个 pending dir 的 Walk 中返回
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.revisitPendingDirs(ctx, walker, yield, shouldStop)
	// 不 panic 即可；failed dir 应被处理
}
