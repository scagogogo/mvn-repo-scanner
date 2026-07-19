//go:build integration

package integration

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario I: 大 jar（1MB）触发 DiskWatcher Acquire → SIGKILL（预留未 Release）
// → resume 新进程预算从 0 起重新算，不卡死，完成扫描。
// 验证 DiskWatcher 预算在崩溃后 resume 不会因泄漏的预留而阻塞（新进程预算重新初始化）。
func TestKillProcess_DiskBudget_AcquireThenKillResumeNotBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "diskbudget.json")
	srv := startLargeJarMockRepo(t, largeJar(t))

	// 第一次 scan 跑到 in-flight（大 jar 触发 Acquire）后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// in-flight 应残留（kill 时 Acquire 已发生但 Release 未执行）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "disk-budget kill should leave in-flight artifacts")

	// resume + retry-failed：新进程 DiskWatcher 预算从 0 起重新算，
	// 不应因上一进程泄漏的预留而阻塞，应能完成扫描
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after disk-budget kill did not finish in 30s (budget may be stuck)")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"],
		"resume should complete despite leaked disk budget reservation")
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after disk-budget resume")
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed, "should complete artifacts after disk-budget resume")
}
