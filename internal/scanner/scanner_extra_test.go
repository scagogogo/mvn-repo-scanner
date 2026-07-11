package scanner

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run 的 DownloadConcurrency > 0 / ScanConcurrency > 0 配置分支。
func TestScanner_Run_DownloadAndScanConcurrency(t *testing.T) {
	cfg := &config.Config{
		RepoURL:             "https://repo.example.com",
		Concurrency:         1,
		DownloadConcurrency: 2,
		ScanConcurrency:     3,
		Timeout:             10 * time.Second,
		MaxFileSize:         "50MB",
		RulesLevel:          "core",
	}
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "test",
		Version: "1.0", FileName: "test.txt",
		DownloadURL: "https://repo.example.com/test.txt",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalScanned)
}

// mockDownloaderError 总返回下载失败，覆盖 Run 的 download err 分支
// （diskWatcher.Release + resultCh StatusFailed）。
type mockDownloaderError struct{ mockDownloader }

func (m *mockDownloaderError) Download(_ context.Context, artifact repo.Artifact) (*repo.DownloadResult, error) {
	return nil, fmt.Errorf("simulated download failure")
}

func TestScanner_Run_DownloadError(t *testing.T) {
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
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloaderError{}
	det := &mockDetector{}
	dw := NewDiskWatcher(10 * 1024 * 1024)
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det), WithDiskWatcher(dw))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalFailed, "download failure should count as failed")
}

// Run 的 cacheCleaner.EnforceCacheLimit 分支：清理超过 50 个结果时触发。
func TestScanner_Run_CacheCleanerInvoked(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     30 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}
	// 51 个 artifact 触发 cleanedCount%50==0
	var arts []repo.Artifact
	for i := 0; i < 51; i++ {
		arts = append(arts, repo.Artifact{
			GroupID: "com.example", ArtifactID: "test",
			Version: fmt.Sprintf("1.%d", i), FileName: fmt.Sprintf("test-%d.txt", i),
			DownloadURL: fmt.Sprintf("https://repo.example.com/test-%d.txt", i),
		})
	}
	browser := &mockBrowser{artifacts: arts}
	dl := &mockDownloader{}
	det := &mockDetector{}
	cc := &mockCacheCleaner{}
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det), WithCacheCleaner(cc))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 51, summary.TotalScanned)
	assert.True(t, cc.called, "EnforceCacheLimit should be called at cleanedCount=50")
}

// discoverStreaming 的 resume-from-persisted-cursor 分支：cursorSaver 有 cursor 且非 rediscover。
func TestScanner_DiscoverStreaming_ResumeFromCursor(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-resume", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	// 预设一个 discovery cursor（模拟中断恢复）
	scanState.SetDiscoveryCursor([]state.CursorFrameJSON{{DirPath: "com/example", NextIdx: 0}})

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// 从 com/example 恢复，应扫到该子树下的 artifact
	assert.GreaterOrEqual(t, summary.TotalDiscovered, 1)
}

// discoverStreaming 的 GroupFilter startPath 分支：GroupFilter 非空时从 group 路径开始。
func TestScanner_DiscoverStreaming_GroupFilterStartPath(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		GroupFilter:        "com.example",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-group", cfg.RepoURL, cfg.GroupFilter, stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, GroupFilter: cfg.GroupFilter, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
}

// discoverStreaming 的 rediscover + ClearDiscoveryCursor 分支。
func TestScanner_DiscoverStreaming_RediscoverClearsCursor(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-red", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	// 预设 cursor，rediscover 应清除它
	scanState.SetDiscoveryCursor([]state.CursorFrameJSON{{DirPath: "com/example", NextIdx: 0}})
	require.True(t, scanState.HasDiscoveryCursor())

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
		WithRediscover(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
}

// discoverStreaming 的 Verbose 日志分支。
func TestScanner_DiscoverStreaming_Verbose(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
		Verbose:            true,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-v", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
}

// Run 在 ctx 取消时覆盖 downloadCh 的 ctx.Done Cleanup 分支 +
// discoverBatched 的 ctx.Done return 分支。用 >buffer 的 artifact + 立即 cancel，
// 让 producer 在 artifactCh 阻塞时走 ctx.Done，download worker 在 downloadCh 阻塞时走 ctx.Done Cleanup。
func TestScanner_Run_CtxCancelDuringDownload(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}
	// 5 个 artifact > artifactCh buffer(2)，立即 cancel 让 producer 走 ctx.Done
	var arts []repo.Artifact
	for i := 0; i < 5; i++ {
		arts = append(arts, repo.Artifact{
			GroupID: "com.example", ArtifactID: "test",
			Version: fmt.Sprintf("1.%d", i), FileName: fmt.Sprintf("test-%d.txt", i),
			DownloadURL: fmt.Sprintf("https://repo.example.com/test-%d.txt", i),
		})
	}
	browser := &mockBrowser{artifacts: arts}
	dl := &mockDownloader{}
	det := &mockDetector{}
	dw := NewDiskWatcher(10 * 1024 * 1024)
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det), WithDiskWatcher(dw))
	// 立即取消
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		scan.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run should not hang on canceled ctx")
	}
}

// Run 的 diskWatcher.Acquire err 分支（line 306-311）：budget=1MB，reservation=1MB，
// 5 个 artifact + DownloadConcurrency=5：第一个 worker 占满 budget，其余 4 个 Acquire 阻塞
// → cancel ctx → Acquire 返回 err → select。ScanConcurrency=1 让 resultCh buffer=1，
// 第一个 err 走 308（发送成功），后续 err 走 309（resultCh 满+ctx 取消）。
// blockingDownloader 让占 budget 的 worker 不释放，保证其余 worker 必然阻塞在 Acquire。
func TestScanner_Run_DiskBudgetAcquireError(t *testing.T) {
	cfg := &config.Config{
		RepoURL:             "https://repo.example.com",
		Concurrency:         2,
		DownloadConcurrency: 5, // 5 download workers 抢 1MB budget
		ScanConcurrency:     1, // resultCh buffer=1，让多个 err 触发 309
		Timeout:             10 * time.Second,
		MaxFileSize:         "50MB",
		RulesLevel:          "core",
	}
	// 5 个 artifact，确保 5 个 download worker 都有事做
	var arts []repo.Artifact
	for i := 0; i < 5; i++ {
		arts = append(arts, repo.Artifact{
			GroupID: "com.example", ArtifactID: "test",
			Version: fmt.Sprintf("1.%d", i), FileName: fmt.Sprintf("test-%d.txt", i),
			DownloadURL: fmt.Sprintf("https://repo.example.com/test-%d.txt", i),
		})
	}
	browser := &mockBrowser{artifacts: arts}
	dl := &blockingDownloader{}
	det := &mockDetector{}
	dw := NewDiskWatcher(1024 * 1024) // 1MB，reservation=1MB，第一个占满，其余阻塞
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det), WithDiskWatcher(dw))

	var failed int
	scan.OnResult(func(r ArtifactResult) {
		if r.Status == StatusFailed {
			failed++
		}
	})
	runCtx, runCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond) // 让 worker 启动、多数 Acquire 阻塞
		runCancel()
	}()
	done := make(chan struct{})
	go func() {
		scan.Run(runCtx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		runCancel()
		t.Fatal("Run should not hang")
	}
	// 多个 worker 因 Acquire 取消而 failed。竞态分支，只断言不 hang/panic。
}

// blockingDownloader 下载时长时间阻塞，用于卡住 worker。
type blockingDownloader struct{ mockDownloader }

func (m *blockingDownloader) Download(ctx context.Context, artifact repo.Artifact) (*repo.DownloadResult, error) {
	// 阻塞直到 ctx 取消
	<-ctx.Done()
	return nil, ctx.Err()
}

// downloadCh 发送 ctx.Done Cleanup Release 分支（line 345-347）：
// ScanConcurrency=1 让 downloadCh buffer=2，slowDetector 让 scan worker 卡住 →
// downloadCh 填满 → download worker 的 `downloadCh <- job` 阻塞 → cancel ctx →
// select 走 ctx.Done → Cleanup + diskWatcher.Release(fileSize)。
type slowDetector struct{ mockDetector }

func (m *slowDetector) ScanFile(_ string) ([]detector.Finding, error) {
	time.Sleep(500 * time.Millisecond)
	return nil, nil
}

func (m *slowDetector) ScanContent(_ io.Reader, _ string) ([]detector.Finding, error) {
	time.Sleep(500 * time.Millisecond)
	return nil, nil
}

func TestScanner_Run_DownloadChCtxDoneCleanup(t *testing.T) {
	cfg := &config.Config{
		RepoURL:             "https://repo.example.com",
		Concurrency:         1,
		DownloadConcurrency: 2, // 2 download workers
		ScanConcurrency:     1, // downloadCh buffer = 2
		Timeout:             10 * time.Second,
		MaxFileSize:         "50MB",
		RulesLevel:          "core",
	}
	var arts []repo.Artifact
	for i := 0; i < 6; i++ {
		arts = append(arts, repo.Artifact{
			GroupID: "com.example", ArtifactID: "test",
			Version: fmt.Sprintf("1.%d", i), FileName: fmt.Sprintf("test-%d.txt", i),
			DownloadURL: fmt.Sprintf("https://repo.example.com/test-%d.txt", i),
		})
	}
	browser := &mockBrowser{artifacts: arts}
	dl := &mockDownloader{}
	det := &slowDetector{}
	dw := NewDiskWatcher(10 * 1024 * 1024)
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det), WithDiskWatcher(dw))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond) // 等 downloadCh 填满、download worker 阻塞
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		scan.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run should not hang when ctx canceled during downloadCh send")
	}
}

// processJob 的 resultCh ctx.Done 分支（line 826）：scanConcurrency=1 让 resultCh
// buffer=1，slowOnResult 让 collector 停滞 → resultCh 满 → processJob 的
// `resultCh <- result` 阻塞 → cancel ctx → select 走 ctx.Done（826）。
func TestScanner_Run_ProcessJobResultChCtxDone(t *testing.T) {
	cfg := &config.Config{
		RepoURL:         "https://repo.example.com",
		Concurrency:     1,
		ScanConcurrency: 1, // resultCh buffer = 1
		Timeout:         10 * time.Second,
		MaxFileSize:     "50MB",
		RulesLevel:      "core",
	}
	var arts []repo.Artifact
	for i := 0; i < 5; i++ {
		arts = append(arts, repo.Artifact{
			GroupID: "com.example", ArtifactID: "test",
			Version: fmt.Sprintf("1.%d", i), FileName: fmt.Sprintf("test-%d.txt", i),
			DownloadURL: fmt.Sprintf("https://repo.example.com/test-%d.txt", i),
		})
	}
	browser := &mockBrowser{artifacts: arts}
	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det))

	// onResult 阻塞 200ms 让 collector 停滞、resultCh 填满
	scan.OnResult(func(r ArtifactResult) {
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond) // 等 resultCh 填满、processJob 阻塞
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		scan.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run should not hang when ctx canceled during processJob send")
	}
}

// discoverStreaming 的 yield ctx.Done return 分支（line 498-499）+ ctx.Err() break
// 分支（line 552-553）：streaming discovery + blockingDownloader 让 download worker
// 停滞 → artifactCh 填满 → mainYield 的 `artifactCh <- a` 阻塞 → cancel ctx → select
// 走 ctx.Done return（498）→ Walk 继续，shouldStop 检测 ctx.Err() 返回 true → Walk
// return nil err，cur 非空 → 552 break。
func TestScanner_DiscoverStreaming_YieldCtxDone(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1, // artifactCh buffer = 2
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50, // 不让 shouldStop 因 batch 抢先返回
	}
	fetcher := newStreamingMockRepo() // 4 artifacts
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-yieldctx", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &blockingDownloader{} // 下载阻塞，让 artifactCh 填满
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond) // 等 artifactCh 填满、第 3 个 yield 阻塞
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		scan.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run should not hang when ctx canceled during streaming yield")
	}
}

// discoverStreaming 的 walkErr 分支 + ctx.Err() break 分支（line 544-546, 552-553）：
// CursorWalker.Walk 唯一返回非 nil err 的路径是循环顶部 select <-ctx.Done()。
// 用慢 fetcher：root 目录立即返回，但子目录 fetch 阻塞。这样 Walk 进入子目录帧后
// ctx 取消 → 子目录 FetchPage 返回 err → Walk pop 子目录帧（root 仍在）→ 下一轮
// 循环顶部 select 命中 ctx.Done → return ctx.Err() → walkErr 分支。
type slowPageFetcher struct{ mockPageFetcher }

func (m *slowPageFetcher) FetchPage(ctx context.Context, url string) (string, error) {
	// root URL 立即返回，让 Walk 进入子目录；子目录阻塞让 ctx 有时间取消。
	if url == "https://repo.example.com/maven2/" {
		return m.mockPageFetcher.FetchPage(ctx, url)
	}
	select {
	case <-time.After(200 * time.Millisecond):
		return m.mockPageFetcher.FetchPage(ctx, url)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestScanner_DiscoverStreaming_WalkError(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}
	fetcher := &slowPageFetcher{mockPageFetcher: *newStreamingMockRepo()}
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-walkerr", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	// 慢 fetcher 阻塞 200ms；50ms 后取消 ctx，让 Walk 在 getNode 中感知取消。
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
}

// discoverStreaming 的 ctx.Err() break 分支（line 552-553）：Walk 返回 nil err
// （shouldStop 因 batchYielded>=checkpointEvery 返回 true）+ len(cur)>0 + ctx 取消。
// 用慢 cursorSaver 让 saveCursorFrom 阻塞，期间 cancel ctx：Walk 已 return nil err，
// saveCursor 阻塞后 ctx 取消，552 检测到 ctx.Err() 而 break（而非继续 resume）。
type slowCursorSaver struct {
	*state.ScanState
	delay time.Duration
}

func (s *slowCursorSaver) SetDiscoveryCursor(c []state.CursorFrameJSON) {
	time.Sleep(s.delay)
	s.ScanState.SetDiscoveryCursor(c)
}

func TestScanner_DiscoverStreaming_CtxErrBreak(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1, // 每yield 1个artifact就checkpoint，让Walk return nil err
	}
	fetcher := newStreamingMockRepo() // 4 artifacts
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-ctxbreak", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	slowSaver := &slowCursorSaver{ScanState: scanState, delay: 300 * time.Millisecond}

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(slowSaver),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	// Walk yield 1 个 artifact 后 shouldStop return nil err，saveCursor 阻塞 300ms；
	// 100ms 后 cancel ctx，saveCursor 仍在阻塞 → 552 命中。
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		scan.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run should not hang when ctx canceled during saveCursor")
	}
}

// discoverStreaming 的 CheckpointInterval == 0 fallback 分支。
func TestScanner_DiscoverStreaming_CheckpointIntervalZero(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 0, // 触发 fallback 50
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-cp0", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	dl := &mockDownloader{}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	scan.OnResult(func(result ArtifactResult) {
		if result.Status == StatusComplete {
			scanState.MarkCompleted(result.Artifact.Path())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, summary.TotalScanned)
}

// Run 的 collector AddFinding 循环分支（line 379-381）：detector 返回非空
// findings 时，summary.AddFinding 应对每个 finding 调用。用一个普通 .txt
// artifact（走 ScanFile 路径）+ 注入 findings 的 detector 覆盖。
func TestScanner_Run_CollectorAddFindings(t *testing.T) {
	cfg := &config.Config{
		RepoURL:     "https://repo.example.com",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
	}
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "test",
		Version: "1.0", FileName: "app.txt",
		DownloadURL: "https://repo.example.com/app.txt",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	// 注入两个 findings，覆盖 summary.AddFinding 循环
	det := &mockDetector{findings: []detector.Finding{
		{RuleID: "r1", Severity: detector.SeverityHigh, FilePath: "app.txt", LineNumber: 1},
		{RuleID: "r2", Severity: detector.SeverityCritical, FilePath: "app.txt", LineNumber: 2},
	}}
	scan := NewScanner(cfg, WithBrowser(browser), WithDownloader(dl), WithDetector(det))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalScanned)
	assert.Equal(t, 2, summary.TotalFindings)
	assert.Equal(t, 1, summary.BySeverity[detector.SeverityHigh])
	assert.Equal(t, 1, summary.BySeverity[detector.SeverityCritical])
}


// selectiveBlockDownloader 仅阻塞指定 artifact 的下载（直到 ctx 取消），其他即时。
// 用于让 revisit pass yield 的 artifact 保持 in-flight，使 main walk 走到同一
// artifact 时 IsInFlight=true → skip（line 490-493），稳定覆盖该竞态分支。
type selectiveBlockDownloader struct {
	mockDownloader
	blockPath string
	blocked   chan struct{}
}

func (b *selectiveBlockDownloader) Download(ctx context.Context, artifact repo.Artifact) (*repo.DownloadResult, error) {
	if artifact.Path() == b.blockPath {
		close(b.blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.mockDownloader.Download(ctx, artifact)
}

// discoverStreaming 的 main walk skipInFlight 分支（line 490-493）：revisit pass
// re-yield 了仍 in-flight 的 artifact（下载卡住），main walk 走到同一 artifact 时
// IsInFlight=true → skip。用 selectiveBlockDownloader 让 lib-1.0.jar 保持 in-flight。
func TestScanner_DiscoverStreaming_MainWalkSkipsInFlight(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 50,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	scanState := state.NewScanStateWithConfig("scan-skip", cfg.RepoURL, "", stateFile, 50,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	// 预 MarkInFlight com/example/lib/1.0 → revisit pass 会 re-walk 该 GAV 目录
	scanState.MarkInFlight("com/example/lib/1.0")

	dl := &selectiveBlockDownloader{
		blockPath: "com/example/lib/1.0",
		blocked:   make(chan struct{}),
	}
	det := &mockDetector{}
	scan := NewScanner(cfg,
		WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithFailedRetryer(scanState), WithCursorSaver(scanState),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 等 selectiveBlockDownloader 开始卡 lib-1.0.jar（revisit pass 已 yield 它且仍 in-flight），
	// 给 main walk 时间走到同一 artifact 触发 skip，然后让 ctx 超时结束。
	go func() {
		<-dl.blocked
		time.Sleep(150 * time.Millisecond) // 让 main walk 走到 lib-1.0.jar 并 skip
	}()
	_, _ = scan.Run(ctx)
}
