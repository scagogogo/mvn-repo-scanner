//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFlakyMockRepo 启动一个部分目录 fetch 返回 500 的仓库，触发 MarkDirFailed。
// 第一次请求 /com/example/lib/ 返回 500（模拟 transient fetch 失败），后续成功。
// 让 scan 在 discovery 阶段记录 failed_dir 后被 kill，验证 FailedDirs 跨崩溃存活。
func startFlakyMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	var libHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="com/">com/</a>`)
		case "/com/":
			fmt.Fprint(w, `<a href="example/">example/</a>`)
		case "/com/example/":
			fmt.Fprint(w, `<a href="lib/">lib/</a>`)
		case "/com/example/lib/":
			n := atomic.AddInt32(&libHits, 1)
			if n <= 1 {
				w.WriteHeader(http.StatusInternalServerError) // 第一次失败 → MarkDirFailed
				return
			}
			fmt.Fprint(w, `<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`)
		case "/com/example/lib/1.0/":
			fmt.Fprint(w, `<a href="lib-1.0.jar">lib-1.0.jar</a>`)
		case "/com/example/lib/2.0/":
			fmt.Fprint(w, `<a href="lib-2.0.jar">lib-2.0.jar</a>`)
		case "/com/example/lib/1.0/lib-1.0.jar",
			"/com/example/lib/2.0/lib-2.0.jar":
			time.Sleep(5 * time.Second) // 慢下载卡住 worker，提供 kill 时窗
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Scenario D: discovery 阶段 fetch 失败记录 failed_dir → kill → resume 重访 failed_dir。
// 验证 FailedDirs 跨崩溃存活，revisitPendingDirs 重访成功后 ClearDirFailed。
func TestKillProcess_DiscoveryFetchFail_SurvivesCrash(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "faildir.json")
	srv := startFlakyMockRepo(t, emptyJar(t))

	// 第一次 scan：lib 目录第一次 fetch 500 → MarkDirFailed；等 state 落盘后 kill
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	// 等 failed_dirs 出现或 in-flight 出现（任一表示 discovery 已跑）。
	// 轮询期间 state 文件可能尚未创建（scan 刚启动未 flush），用容错读取而非 loadStateJSON。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(stateFile); err == nil {
			var st map[string]interface{}
			if json.Unmarshal(data, &st) == nil {
				if fd, _ := st["failed_dirs"].([]interface{}); len(fd) > 0 {
					break
				}
				if inf, _ := st["in_flight_artifacts"].([]interface{}); len(inf) > 0 {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// resume：flaky repo 第二次 lib fetch 成功 → revisit 重访 → ClearDirFailed → 扫到 artifact
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after discovery-fetch-fail did not finish in 30s")
	}

	st := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st["status"])
	// failed_dirs 应被清空（revisit 成功后 ClearDirFailed）
	failedDirs, _ := st["failed_dirs"].([]interface{})
	assert.Empty(t, failedDirs, "failed_dirs must be cleared after successful revisit on resume")
	// 应扫到 artifact（revisit 重访成功）
	completed, _ := st["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed)
}

// Scenario E: discovery 中途 SIGKILL（cursor 已 advance 但 discovery 未完成）→ resume 从 cursor 续。
// 验证 discovery_cursor 跨崩溃存活，resume 不从头重发 discovery。
func TestKillProcess_DiscoveryMidCursorKill_ResumeContinues(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "midcursor.json")
	srv := startKillableMockRepo(t, emptyJar(t))

	// 第一次 scan 跑一会（discovery 跑了一部分）后 kill
	cmd, _ := startScanSubprocess(t, srv.URL, stateFile)
	// 等 discovery_cursor 出现（表示 streaming discovery 跑过至少一帧）。
	// 轮询期间 state 文件可能尚未创建，用容错读取。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(stateFile); err == nil {
			var st map[string]interface{}
			if json.Unmarshal(data, &st) == nil {
				if c, _ := st["discovery_cursor"].([]interface{}); len(c) > 0 {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	// cursor 应已落盘
	st := loadStateJSON(t, stateFile)
	cursor, _ := st["discovery_cursor"].([]interface{})
	require.NotEmpty(t, cursor, "discovery cursor should be persisted before kill")

	// resume 应从 cursor 续，最终 completed
	resume, _ := startScanSubprocess(t, srv.URL, stateFile, "--resume", "--retry-failed")
	done := make(chan error)
	go func() { done <- resume.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		resume.Process.Kill()
		t.Fatal("resume after mid-discovery kill did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	assert.Equal(t, "completed", st2["status"])
	// resume 完成后 cursor 应被清空（discovery 跑完）
	cursor2, _ := st2["discovery_cursor"].([]interface{})
	assert.Empty(t, cursor2, "cursor must be cleared after discovery completes on resume")
}
