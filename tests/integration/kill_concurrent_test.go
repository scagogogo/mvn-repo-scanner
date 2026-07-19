//go:build integration

package integration

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario H: 多 disjoint subtree（lib + lib2）并发 in-flight → SIGKILL → resume。
// 验证 revisitPendingDirs 重访多个 in-flight 目录，不丢不重（4 个 version 全完成无重复）。
func TestKillProcess_MultiSubtree_ConcurrentInflightResume(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "multisubtree.json")
	srv := startMultiSubtreeMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight（多 worker 并发下载 lib/lib2 的 jar）后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// in-flight 应残留多个 artifact（lib + lib2 的 version）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "multi-subtree kill should leave in-flight artifacts")

	// resume + retry-failed 续跑
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("multi-subtree resume did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after multi-subtree resume")

	// 不丢：4 个 version 全完成
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.GreaterOrEqual(t, len(completed), 4,
		"all 4 versions across 2 subtrees should complete, got %d", len(completed))

	// 不重：每个 path 只出现一次
	pathCount := map[string]int{}
	for _, c := range completed {
		if s, ok := c.(string); ok {
			pathCount[s]++
		}
	}
	for p, c := range pathCount {
		assert.Equal(t, 1, c, "duplicate path %s scanned %d times after multi-subtree resume", p, c)
	}
}
