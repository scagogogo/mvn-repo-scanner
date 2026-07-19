//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario J: 高频 flush 下 SIGKILL 模拟 mid-flush 掉电 → 主文件或 .bak 至少一个完好
// → resume 可续并完成。验证掉电模拟端到端：主文件要么完整要么有 .bak 兜底，resume 总能恢复。
// checkpoint-interval=1 让每次 MarkCompleted/MarkInFlight 都 flush，最大化命中 flush 时窗。
func TestKillProcess_FlushMidWrite_PowerLossSimulation(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "powerloss.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan：checkpoint-interval=1 高频 flush，跑到 in-flight 后 SIGKILL
	// startScanSubprocess 已设 --checkpoint-interval 1
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	waitForInflight(t, stateFile, 10*time.Second)
	waitForBak(t, stateFile, 10*time.Second) // .bak 需第二次 flush 才产生
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// kill 后检查：主文件或 .bak 至少一个可解析（掉电不应同时损坏两者）
	mainOK := fileIsParsable(t, stateFile)
	bakOK := fileIsParsable(t, stateFile+".bak")
	require.True(t, mainOK || bakOK,
		"after SIGKILL mid-flush, at least main or .bak must be parseable (got main=%v bak=%v)",
		mainOK, bakOK)

	// resume 应能从完好的文件恢复并完成
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after mid-flush power-loss did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"],
		"resume should recover after mid-flush power-loss and complete")
	inflight, _ := st["in_flight_artifacts"].([]interface{})
	assert.Empty(t, inflight, "in-flight must be cleared after power-loss resume")
}

// fileIsParsable 检查文件是否存在且 JSON 可解析。
func fileIsParsable(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]interface{}
	return json.Unmarshal(data, &m) == nil
}
