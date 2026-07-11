package scanner

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeScenarioHarness 封装多 phase resume 测试的公共构造。
// 不持有 downloader —— 不同 phase 需要阻塞不同 artifact，downloader 由调用方传入。
type resumeScenarioHarness struct {
	cfg     *config.Config
	fetcher *mockPageFetcher
	state   *state.ScanState
	det     *mockDetector
}

func newResumeScenarioHarness(t *testing.T, scanID string) *resumeScenarioHarness {
	t.Helper()
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        1,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1, // 每 yield 1 个就 checkpoint cursor，保证 cancel 前 cursor 已落盘
	}
	fetcher := newStreamingMockRepo() // 4 artifacts, DFS 顺序: lib/1.0, lib/2.0, commons/1.0, commons/2.0
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, scanID+".json")
	st := state.NewScanStateWithConfig(scanID, cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	return &resumeScenarioHarness{cfg: cfg, fetcher: fetcher, state: st, det: &mockDetector{}}
}

// runPhaseBlocking 跑一次 scan：用 selectiveBlockDownloader 阻塞 blockPath 的下载，
// 在第 n 个 artifact 完成后 cancel。返回本 phase 扫到的路径。
//
// 为什么用 selectiveBlockDownloader 而非 mockDownloader：
// mockDownloader 即时返回，cancel 生效前 worker 可能已多扫（CPU 紧张时竞态放大），
// 这是既有 TestScanner_StreamingDiscoveryResumable 的 flaky 根因。阻塞第 n+1 个
// artifact 让 cancel 时它确定性处于 in-flight，cancel 前 worker 确定性只扫 n 个。
func (h *resumeScenarioHarness) runPhaseBlocking(t *testing.T, n int, blockPath string) []string {
	t.Helper()
	dl := &selectiveBlockDownloader{
		mockDownloader: mockDownloader{},
		blockPath:      blockPath,
		blocked:        make(chan struct{}),
	}
	scan := NewScanner(h.cfg,
		WithDownloader(dl), WithDetector(h.det),
		WithState(h.state), WithDiscoveryCacher(h.state),
		WithFailedRetryer(h.state), WithCursorSaver(h.state),
		WithPageFetcher(h.fetcher), WithStreamingDiscovery(),
	)
	var scanned []string
	var mu sync.Mutex
	var done = make(chan struct{})
	scan.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			_ = h.state.MarkCompleted(r.Artifact.Path())
			mu.Lock()
			scanned = append(scanned, r.Artifact.Path())
			count := len(scanned)
			mu.Unlock()
			if count >= n {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-done; cancel() }()
	go func() { time.Sleep(3 * time.Second); cancel() }() // 兜底防 hang
	scan.Run(ctx)
	require.NoError(t, h.state.Flush())
	return scanned
}

// runPhaseToCompletion 跑一次 scan 到完成（非阻塞 downloader）。返回本 phase 扫到的路径。
func (h *resumeScenarioHarness) runPhaseToCompletion(t *testing.T) []string {
	t.Helper()
	scan := NewScanner(h.cfg,
		WithDownloader(&mockDownloader{}), WithDetector(h.det),
		WithState(h.state), WithDiscoveryCacher(h.state),
		WithFailedRetryer(h.state), WithCursorSaver(h.state),
		WithPageFetcher(h.fetcher), WithStreamingDiscovery(),
	)
	var scanned []string
	var mu sync.Mutex
	scan.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			_ = h.state.MarkCompleted(r.Artifact.Path())
			mu.Lock()
			scanned = append(scanned, r.Artifact.Path())
			mu.Unlock()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := scan.Run(ctx)
	require.NoError(t, err)
	require.NoError(t, h.state.Flush())
	return scanned
}

// 场景 1：3 phase 反复 resume，每 phase 扫 1 个，最终 4 个全覆盖且不重复。
// DFS 顺序：lib/1.0(1) → lib/2.0(2) → commons/1.0(3) → commons/2.0(4)。
//
//	phase1: 扫 lib/1.0，阻塞 lib/2.0 → cancel，扫 1 个
//	phase2: resume，lib/1.0 已 completed 跳过，扫 lib/2.0，阻塞 commons/1.0 → cancel，扫 1 个
//	phase3: resume 跑完，扫 commons/1.0 + commons/2.0，2 个
func TestResumeScenario_MultiPhaseProgressive(t *testing.T) {
	h := newResumeScenarioHarness(t, "multi-phase")

	p1 := h.runPhaseBlocking(t, 1, "com/example/lib/2.0")
	p2 := h.runPhaseBlocking(t, 1, "org/apache/commons/1.0")
	p3 := h.runPhaseToCompletion(t)

	all := append(append(p1, p2...), p3...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "3 phase 应覆盖全部 4 artifact: %v", all)

	// 每个 artifact 恰好扫一次
	seen := map[string]int{}
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s 扫了 %d 次", p, c)
	}
	require.GreaterOrEqual(t, len(p1), 1, "phase1 应至少扫 1 个")
	require.GreaterOrEqual(t, len(p2), 1, "phase2 应至少扫 1 个")
}

// 场景 2：scan-count 限制主动停下（cancel 无 signal）后 resume 能继续，不丢不重。
// 模拟：phase1 扫 2 个后 ctx cancel（非 signal，模拟 --max-scan 到达），phase2 resume 跑完。
//
//	phase1: 扫 lib/1.0 + lib/2.0，阻塞 commons/1.0 → cancel，扫 2 个
func TestResumeScenario_ArtificialStopAndResume(t *testing.T) {
	h := newResumeScenarioHarness(t, "stop-resume")

	p1 := h.runPhaseBlocking(t, 2, "org/apache/commons/1.0")
	require.GreaterOrEqual(t, len(p1), 1)
	// 主动停下：标 finished（区别于 interrupted）
	h.state.MarkFinishedStatus()
	require.NoError(t, h.state.Flush())

	// phase2 resume：从 finished 状态继续
	p2 := h.runPhaseToCompletion(t)

	all := append(p1, p2...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "finished 后 resume 应覆盖全部: %v", all)

	seen := map[string]int{}
	for _, p := range all {
		seen[p]++
	}
	for p, c := range seen {
		assert.Equal(t, 1, c, "artifact %s 扫了 %d 次", p, c)
	}
}

// 场景 3：并发中断点——多个 worker 同时在飞时 cancel，in-flight 不丢。
// 用 Concurrency=2 让 2 个 download worker 并发；用 selectiveBlockDownloader 阻塞
// commons/1.0 让其在 cancel 时确定性 in-flight，避免「cancel 前 worker 多扫」竞态。
func TestResumeScenario_ConcurrentCancelPoint(t *testing.T) {
	cfg := &config.Config{
		RepoURL:            "https://repo.example.com/maven2",
		Concurrency:        2, // 2 并发 worker
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		CheckpointInterval: 1,
	}
	fetcher := newStreamingMockRepo()
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "conc.json")
	st := state.NewScanStateWithConfig("conc", cfg.RepoURL, "", stateFile, 1,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})

	// phase1：扫 1 个后 cancel；selectiveBlockDownloader 阻塞 commons/1.0 确保它在飞。
	dl1 := &selectiveBlockDownloader{
		mockDownloader: mockDownloader{},
		blockPath:      "org/apache/commons/1.0",
		blocked:        make(chan struct{}),
	}
	scan1 := NewScanner(cfg,
		WithDownloader(dl1), WithDetector(&mockDetector{}),
		WithState(st), WithDiscoveryCacher(st),
		WithFailedRetryer(st), WithCursorSaver(st),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var p1 []string
	var mu sync.Mutex
	var done = make(chan struct{})
	scan1.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			mu.Lock()
			p1 = append(p1, r.Artifact.Path())
			mu.Unlock()
			_ = st.MarkCompleted(r.Artifact.Path())
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { <-done; cancel1() }()
	go func() { time.Sleep(3 * time.Second); cancel1() }()
	scan1.Run(ctx1)
	require.NoError(t, st.Flush())

	// phase2：resume 到完成
	scan2 := NewScanner(cfg,
		WithDownloader(&mockDownloader{}), WithDetector(&mockDetector{}),
		WithState(st), WithDiscoveryCacher(st),
		WithFailedRetryer(st), WithCursorSaver(st),
		WithPageFetcher(fetcher), WithStreamingDiscovery(),
	)
	var p2 []string
	var mu2 sync.Mutex
	scan2.OnResult(func(r ArtifactResult) {
		if r.Status == StatusComplete {
			mu2.Lock()
			p2 = append(p2, r.Artifact.Path())
			mu2.Unlock()
			_ = st.MarkCompleted(r.Artifact.Path())
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_, err := scan2.Run(ctx2)
	require.NoError(t, err)

	all := append(p1, p2...)
	unique := map[string]bool{}
	for _, p := range all {
		unique[p] = true
	}
	assert.Equal(t, 4, len(unique), "并发 cancel 后 resume 应覆盖全部: %v", all)
}

// 场景 4：resume 时 in-flight 集合残留（模拟崩溃后未清理）能被 revisit 重扫。
// 直接预置一个 in-flight 路径到 state，验证 phase2 revisit 它。
func TestResumeScenario_StaleInFlightRevisited(t *testing.T) {
	h := newResumeScenarioHarness(t, "stale-inflight")
	// 预置一个「上次崩溃遗留」的 in-flight 路径
	// MarkInFlight 不返回 error（无返回值），直接调用。
	h.state.MarkInFlight("com/example/lib/1.0")
	require.NoError(t, h.state.Flush())

	// phase2 resume：revisit 应重扫 com/example/lib/1.0（in-flight → completed）
	p := h.runPhaseToCompletion(t)
	assert.Contains(t, p, "com/example/lib/1.0", "遗留 in-flight 应被 revisit 重扫")

	unique := map[string]bool{}
	for _, x := range p {
		unique[x] = true
	}
	assert.Equal(t, 4, len(unique), "应覆盖全部 4 artifact: %v", p)
}
