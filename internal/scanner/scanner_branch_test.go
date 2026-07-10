package scanner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanner_branch_test.go 覆盖 scanner.go 中 streaming/batched 发现入口的
// 三层复合条件短路组合，把语句覆盖推进到分支/条件覆盖。

// scanner.go:276 `if s.useStreamingDiscovery && s.pageFetcher != nil`
// 覆盖短路侧：useStreamingDiscovery=true 但 pageFetcher=nil → 条件为 false → 降级到 discoverBatched。
// 通过 WithStreamingDiscovery() 设 useStreamingDiscovery=true，但故意不传 WithPageFetcher。
func TestScanner_DiscoverStreaming_NoPageFetcher_FallsBackToBatched(t *testing.T) {
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		FileName: "lib-1.0.jar", DownloadURL: "https://repo.example.com/lib-1.0.jar",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}
	cfg := &config.Config{
		RepoURL: "https://repo.example.com", Concurrency: 1,
		Timeout: 5 * time.Second, MaxFileSize: "50MB", RulesLevel: "core",
	}
	// WithStreamingDiscovery 设 true，但故意不传 WithPageFetcher → pageFetcher=nil → 276 短路
	scan := NewScanner(cfg,
		WithBrowser(browser), WithDownloader(dl), WithDetector(det),
		WithStreamingDiscovery(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// 走 batched 分支（discoverBatched 用 browser.Discover）→ 扫到 1 个
	assert.Equal(t, 1, summary.TotalScanned, "pageFetcher=nil 时 streaming 应降级到 batched")
}

// scanner.go:442 `cursorSaver != nil && !s.rediscover && s.cursorSaver.HasDiscoveryCursor()`
// + scanner.go:457 `s.cursorSaver != nil && s.rediscover` → ClearDiscoveryCursor
// + scanner.go:677 `s.discoveryCacher != nil && !s.rediscover && s.discoveryCacher.HasDiscoveryCache()`
// + scanner.go:686 `s.discoveryCacher != nil && s.rediscover` → ClearDiscoveryCache
//
// 覆盖：rediscover=true 时，即使预填了 cursor 和 cache，也走短路（442/677 的 !rediscover 为 false）
// 并触发 Clear 分支（457/686）。用真实 artifact 验证不读旧 cursor/cache。
func TestScanner_Rediscover_SkipsCursorAndCacheShortCircuit(t *testing.T) {
	artifact := repo.Artifact{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		FileName: "lib-1.0.jar", DownloadURL: "https://repo.example.com/lib-1.0.jar",
	}
	browser := &mockBrowser{artifacts: []repo.Artifact{artifact}}
	dl := &mockDownloader{}
	det := &mockDetector{}
	cfg := &config.Config{
		RepoURL: "https://repo.example.com", Concurrency: 1,
		Timeout: 5 * time.Second, MaxFileSize: "50MB", RulesLevel: "core",
	}
	tmpDir := t.TempDir()
	scanState := state.NewScanStateWithConfig("scan-rc", cfg.RepoURL, "", filepath.Join(tmpDir, "s.json"), 0,
		state.ConfigSnapshot{RepoURL: cfg.RepoURL, RulesLevel: cfg.RulesLevel, MaxFileSize: cfg.MaxFileSize})
	// 预填一个 discovery cache（模拟旧 cache），rediscover 应忽略它并 Clear
	scanState.SetDiscoveredArtifacts([]string{"com/old/1.0"})
	require.True(t, scanState.HasDiscoveryCache(), "预填 cache 后应 HasDiscoveryCache")
	// 预填一个 discovery cursor（模拟旧 cursor），rediscover 应忽略它并 Clear
	scanState.SetDiscoveryCursor([]state.CursorFrameJSON{
		{DirPath: "com/old", NextIdx: 1},
	})
	require.True(t, scanState.HasDiscoveryCursor(), "预填 cursor 后应 HasDiscoveryCursor")

	scan := NewScanner(cfg,
		WithBrowser(browser), WithDownloader(dl), WithDetector(det),
		WithState(scanState), WithDiscoveryCacher(scanState),
		WithCursorSaver(scanState),
		WithRediscover(), // rediscover=true → 442/677 短路（!rediscover=false），457/686 Clear
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := scan.Run(ctx)
	require.NoError(t, err)
	// 不读旧 cache/cursor，用 browser 的真实 artifact → 扫到 lib/1.0 而非 old
	assert.Equal(t, 1, summary.TotalScanned, "rediscover 应忽略旧 cache/cursor 用真实发现")
}
