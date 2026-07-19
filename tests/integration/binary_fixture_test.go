//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testBinaryPath 是 TestMain 一次性构建的 mvn-repo-scanner 二进制路径。
// 所有 kill-process 测试复用，避免每次 go build。
var testBinaryPath string

// TestMain 构建一次二进制 fixture 供 kill-process 测试启动子进程。
// 构建失败时相关测试 t.Skip 而非 fail，避免在无 go toolchain 的环境阻断套件。
func TestMain(m *testing.M) {
	cleanup, built := buildTestBinary()
	if built {
		defer cleanup()
	}
	code := m.Run()
	if built {
		os.Remove(testBinaryPath)
	}
	os.Exit(code)
}

// buildTestBinary 用 go build 输出二进制到临时文件，返回清理函数。
// 若 go 不可用或构建失败，返回 built=false（相关测试会 t.Skip）。
// 构建后显式 chmod 0755：某些沙箱环境的 umask 会剥掉 go build 输出的执行位，
// 导致 fork/exec "permission denied"。
func buildTestBinary() (cleanup func(), built bool) {
	binFile, err := os.CreateTemp("", "mvn-repo-scanner-test-*")
	if err != nil {
		return func() {}, false
	}
	binFile.Close()
	testBinaryPath = binFile.Name()
	os.Remove(testBinaryPath) // go build 输出到不存在的路径更稳定

	// ./../../cmd/mvn-repo-scanner 是 tests/integration 相对仓库根的 main 包路径。
	// 注意：必须是 main 包（cmd/mvn-repo-scanner/），不是 ./../../cmd（那是 package cmd，
	// go build 会输出 ar archive 而非可执行文件）。
	cmd := exec.Command("go", "build", "-o", testBinaryPath, "./../../cmd/mvn-repo-scanner")
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(testBinaryPath)
		_ = out
		return func() {}, false
	}
	_ = out
	// 显式赋执行位（沙箱 umask 可能剥掉 go build 默认的 0755）
	_ = os.Chmod(testBinaryPath, 0755)
	return func() { os.Remove(testBinaryPath) }, true
}

// startKillableMockRepo 启动一个 mock Maven 仓库，确保 scan 子进程可在中途被 kill。
// 目录列表即时返回（discovery 快速找到 artifact），但 jar 下载延迟 5 秒——
// 让 download worker 卡住，in-flight 持续存在，提供稳定的 kill 时窗。
// jar 用空 zip 让扫描（若完成）快速结束、无 finding。
//
// HTML 链接必须是相对于当前目录的直接子项（Maven Central 风格），否则
// discovery 的相对路径解析会错误拼接（如 com/example + com/ → com/example/com）。
func startKillableMockRepo(t *testing.T, jarBytes []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="com/">com/</a>`)
		case "/com/":
			fmt.Fprint(w, `<a href="example/">example/</a>`)
		case "/com/example/":
			fmt.Fprint(w, `<a href="lib/">lib/</a>`)
		case "/com/example/lib/":
			fmt.Fprint(w, `<a href="1.0/">1.0/</a><a href="2.0/">2.0/</a>`)
		case "/com/example/lib/1.0/":
			fmt.Fprint(w, `<a href="lib-1.0.jar">lib-1.0.jar</a>`)
		case "/com/example/lib/2.0/":
			fmt.Fprint(w, `<a href="lib-2.0.jar">lib-2.0.jar</a>`)
		case "/com/example/lib/1.0/lib-1.0.jar",
			"/com/example/lib/2.0/lib-2.0.jar":
			// 慢下载：卡住 download worker，确保 kill 时 in-flight 持续存在
			time.Sleep(5 * time.Second)
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jarBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startScanSubprocess 启动一个 scan 子进程对接 mockRepo，返回 cmd 与 stateFile。
// 子进程跑真实二进制，可被外部 SIGKILL/SIGTERM。
func startScanSubprocess(t *testing.T, mockRepo, stateFile string, extraArgs ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	if testBinaryPath == "" {
		t.Skip("test binary not built")
	}
	args := []string{"scan",
		"--repo", mockRepo,
		"--group", "com.example",
		"--rules-level", "core",
		"--checkpoint-interval", "1",
		"--state-file", stateFile,
		"--timeout", "10s",
	}
	args = append(args, extraArgs...)
	cmd := exec.Command(testBinaryPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd, &buf
}

// waitForInflight 轮询 stateFile 直到出现 in_flight_artifacts（或超时），
// 确保 scan 已进入下载阶段（kill 时窗已到）。
func waitForInflight(t *testing.T, stateFile string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(stateFile); err == nil {
			var st struct {
				InFlight []string `json:"in_flight_artifacts"`
			}
			if json.Unmarshal(data, &st) == nil && len(st.InFlight) > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scan never reached in-flight state within %s", timeout)
}

// loadStateJSON 读取 state 文件解析为 map（测试断言用）。
func loadStateJSON(t *testing.T, stateFile string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// 用空 zip 作为 jar 内容（扫描器解压后无 finding，快速完成）。
var emptyJarOnce sync.Once
var emptyJarBytes []byte

func emptyJar(t *testing.T) []byte {
	t.Helper()
	emptyJarOnce.Do(func() {
		buf := new(bytes.Buffer)
		w := zip.NewWriter(buf)
		w.Close()
		emptyJarBytes = buf.Bytes()
	})
	return emptyJarBytes
}
