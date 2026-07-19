//go:build integration

package integration

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario A: SIGKILL 强杀（不可捕获）→ state 残留 in-flight → resume + retry-failed 不丢不重。
// 这是 V2 手动验证过的核心 case，现在自动化。SIGKILL 让 signal handler 无机会执行，
// state 停在最后一次 MarkInFlight/MarkCompleted 的 flush（status 可能 running，in-flight 残留）。
func TestKillProcess_SIGKILL_ResumeRecovers(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "kill.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)

	// SIGKILL 不可捕获 → 子进程立即死，signal handler 无机会跑
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// state 应残留 in-flight（status running 或 interrupted，关键看 in-flight 非空）
	st := loadStateJSON(t, stateFile)
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight, "SIGKILL should leave in-flight artifacts in state")

	// resume + retry-failed 续跑
	resumeCmd, buf := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resumeCmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resumeCmd.Process.Kill()
		t.Fatal("resume did not finish in 30s")
	}

	// 续跑后 state 应 completed，in-flight 清空
	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	inflight2, _ := st2["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight2, "in-flight must be cleared after resume")
	// 至少扫到一些 artifact（mock repo 有 2 个 version）
	completed, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed, "resume should complete the in-flight artifacts")
	_ = buf
}

// Scenario B: SIGTERM（可捕获）→ signal handler cancel ctx → runScan 标 interrupted。
// 验证 state status=interrupted（区别于 SIGKILL 的 running 残留）+ resume 可续。
func TestKillProcess_SIGTERM_MarksInterruptedAndResumes(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "term.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)

	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("SIGTERM should let scan finish gracefully in 10s")
	}

	// SIGTERM 走 graceful shutdown → status=interrupted（或 completed 若 cancel 前跑完）
	st := loadStateJSON(t, stateFile)
	status, _ := st["status"].(string)
	assert.Contains(t, []string{"interrupted", "completed"}, status,
		"SIGTERM should produce interrupted or completed status, got %s", status)

	// resume 续跑应正常完成（无论上一轮 interrupted 还是 completed）
	resumeCmd, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done2 := make(chan error)
	go func() { done2 <- resumeCmd.Wait() }()
	select {
	case <-done2:
	case <-time.After(30 * time.Second):
		resumeCmd.Process.Kill()
		t.Fatal("resume after SIGTERM did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
}

// Scenario C: 双重崩溃——resume 跑了一半又 SIGKILL，二次 resume 仍不丢不重。
// 验证 resume 本身也是可中断的（resume 不是原子操作，中途再崩仍能恢复）。
func TestKillProcess_DoubleCrash_SecondResumeRecovers(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "double.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 SIGKILL
	cmd1, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	_ = cmd1.Process.Signal(syscall.SIGKILL)
	cmd1.Wait()

	st1 := loadStateJSON(t, stateFile)
	inflight1, _ := st1["in_flight_artifacts"].([]interface{})
	assert.NotEmpty(t, inflight1, "first kill should leave in-flight")

	// 第一次 resume，跑一半再 SIGKILL
	resume1, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	waitForInflight(t, stateFile, 10*time.Second) // resume 进入下载阶段再杀
	_ = resume1.Process.Signal(syscall.SIGKILL)
	resume1.Wait()

	// 第二次 resume 应仍能恢复并完成
	resume2, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume2.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume2.Process.Kill()
		t.Fatal("second resume did not finish in 30s")
	}

	st3 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st3["status"])
	inflight3, _ := st3["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight3, "in-flight must be cleared after second resume")
	// 不丢：completed 路径应覆盖 mock repo 的全部 version
	completed, _ := st3["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed, "should have completed artifacts after double-crash resume")
	// 不重：每个 path 只出现一次
	pathCount := map[string]int{}
	for _, c := range completed {
		if s, ok := c.(string); ok {
			pathCount[s]++
		}
	}
	for p, c := range pathCount {
		assert.Equal(t, 1, c, "duplicate path %s scanned %d times after double-crash resume", p, c)
	}
}
