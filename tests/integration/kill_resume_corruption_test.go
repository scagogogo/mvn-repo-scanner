//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForBak 轮询 stateFile 直到 .bak 出现（或超时）。
// .bak 在第二次 flush 时产生（第一次 flush 写主文件，第二次把上一次主文件备份成 .bak），
// 因此 kill 前需等 .bak 出现才能验证 .bak 回退恢复。
func waitForBak(t *testing.T, stateFile string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stateFile + ".bak"); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(".bak never appeared within %s", timeout)
}

// Scenario F: SIGKILL 后手动损坏主文件 → resume 从 .bak 恢复并完成。
// 验证 LoadScanState .bak 回退在子进程级端到端生效（非纯单测）。
func TestKillProcess_CorruptMain_ResumeFromBak(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "corruptmain.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight 后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	waitForBak(t, stateFile, 10*time.Second) // .bak 需第二次 flush 才产生
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// kill 后应存在主文件 + .bak（flush 时备份）
	require.FileExists(t, stateFile, "main state file should exist after kill")
	require.FileExists(t, stateFile+".bak", ".bak should exist after flush")

	// 损坏主文件，保留 .bak
	corruptStateFile(t, stateFile, "main")

	// resume 应从 .bak 恢复并完成
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume from .bak did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"], "resume should recover from .bak and complete")
}

// Scenario G: SIGKILL 后损坏主文件 + .bak → resume 应优雅降级（load 失败被当 warning
// 忽略，state 视为全新重扫），最终 completed 不 panic。
// 验证 LoadScanState 双文件都坏时 scan 不崩溃，而是从头重扫完成（兜底降级）。
func TestKillProcess_CorruptMainAndBak_ResumeFailsGracefully(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "doublebad.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑到 in-flight 后 SIGKILL
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	waitForBak(t, stateFile, 10*time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	require.FileExists(t, stateFile)
	require.FileExists(t, stateFile+".bak")

	// 同时损坏主文件 + .bak
	corruptStateFile(t, stateFile, "main")
	corruptStateFile(t, stateFile, "bak")

	// resume：双文件都坏 → LoadScanState 返回 err 但 runScan 只 warning 忽略 →
	// state 视为全新重扫，应退出 0 且最终 completed（优雅降级，不 panic）
	resume, buf := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume with both files corrupt did not finish in 30s")
	}

	// 输出应含 corrupt 相关警告（LoadScanState 的 warning log）
	out := buf.String()
	assert.True(t,
		strings.Contains(strings.ToLower(out), "corrupt") || strings.Contains(strings.ToLower(out), "parse") || strings.Contains(strings.ToLower(out), "warning"),
		"output should mention corruption/warning, got: %s", out)

	// 双文件都坏 → resume 视为全新重扫，应最终 completed（兜底降级，不丢扫描结果）
	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"],
		"resume should degrade to fresh scan and complete when both files corrupt")
}
