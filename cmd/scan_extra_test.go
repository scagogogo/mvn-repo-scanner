package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startMockMavenRepo404 启动一个目录树正常但 jar 下载返回 404 的仓库，
// 触发下载失败 → StatusFailed 路径。
func startMockMavenRepo404(t *testing.T) *httptest.Server {
	t.Helper()
	pages := map[string][]byte{
		"/":                       []byte(`<a href="com/">com/</a>`),
		"/com/":                   []byte(`<a href="example/">example/</a>`),
		"/com/example/":           []byte(`<a href="leaky/">leaky/</a>`),
		"/com/example/leaky/":     []byte(`<a href="1.0/">1.0/</a>`),
		"/com/example/leaky/1.0/": []byte(`<a href="leaky-1.0.jar">leaky-1.0.jar</a>`),
		// jar 路径故意不提供 → 404
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

// startSlowMockMavenRepo 启动一个每个响应都延迟 delay 的仓库，用于触发 timeout/中断。
func startSlowMockMavenRepo(t *testing.T, jarPath string, delay time.Duration) *httptest.Server {
	t.Helper()
	jarBytes, err := os.ReadFile(jarPath)
	require.NoError(t, err)
	pom := `<?xml version="1.0"?><project><groupId>com.example</groupId><artifactId>leaky</artifactId><version>1.0</version></project>`
	pages := map[string][]byte{
		"/":                                 []byte(`<a href="com/">com/</a>`),
		"/com/":                             []byte(`<a href="example/">example/</a>`),
		"/com/example/":                     []byte(`<a href="leaky/">leaky/</a>`),
		"/com/example/leaky/":               []byte(`<a href="1.0/">1.0/</a>`),
		"/com/example/leaky/1.0/":           []byte(`<a href="leaky-1.0.jar">leaky-1.0.jar</a><a href="leaky-1.0.pom">leaky-1.0.pom</a>`),
		"/com/example/leaky/1.0/leaky-1.0.jar": jarBytes,
		"/com/example/leaky/1.0/leaky-1.0.pom": []byte(pom),
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

// validScanCfg 构造一个指向 mock repo 的有效扫描配置。
func validScanCfg(t *testing.T, srvURL, stateFile string) *config.Config {
	return &config.Config{
		RepoURL:      srvURL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      10 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    stateFile,
		Output:       "console",
	}
}

// runScan 的 taskEnabled + TaskID 自动生成（line 95-97）+ StateFile 默认设置（100-103）。
func TestRunScan_TaskEnabled_AutoTaskIDAndStateFile(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// TaskID 空 + ScanInterval>0 → 自动生成 TaskID；StateFile 默认 → 设置到 workspace
	cfg = &config.Config{
		RepoURL:      srv.URL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      10 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		ScanInterval: 1 * time.Hour, // taskEnabled, TaskID 空
		Output:       "console",
	}
	_ = withStdoutCapture(t, func() {
		err := runScan(scanCmd, nil)
		assert.NoError(t, err)
	})
	// 自动生成的 TaskID 形如 task-YYYYMMDD-HHMMSS
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	tasks, err := store.ListTasks("")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Contains(t, tasks[0].TaskID, "task-")
	// StateFile 应在 workspace 的 states 目录下
	assert.Contains(t, tasks[0].StateFile, "states")
}

// runScan 的 existing task Status Completed → TaskActive（line 124-126）。
func TestRunScan_TaskExistingCompletedReactivates(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// 预存一个 Completed 状态的 task
	store, err := openTaskStore()
	require.NoError(t, err)
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:          "reactivate-task",
		RepoURL:         srv.URL,
		GroupFilter:     "com.example",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 3600,
		Status:          storage.TaskCompleted,
		StateFile:       filepath.Join(tmpDir, "state.json"),
		CreatedAt:       time.Now(),
	}))
	store.Close()

	cfg = &config.Config{
		RepoURL:      srv.URL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      10 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    filepath.Join(tmpDir, "state.json"),
		TaskID:       "reactivate-task",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// task 应被重新激活为 active
	store2, err := openTaskStore()
	require.NoError(t, err)
	defer store2.Close()
	task, err := store2.GetTask("reactivate-task")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskActive, task.Status)
}

// runScan 的 LoadRulesWithLevel err（line 139-141）：RulesFile 指向不存在的文件。
func TestRunScan_LoadRulesError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	cfg = &config.Config{
		RepoURL:    srv.URL,
		GroupFilter: "com.example",
		Concurrency: 1,
		Timeout:    10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel: "core",
		RulesFile:  filepath.Join(tmpDir, "nonexistent-rules.yaml"), // 不存在
		Output:     "console",
	}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load rules")
}

// runScan 的 state load err（line 149-151）：StateFile 是损坏 JSON。
func TestRunScan_StateLoadError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")
	require.NoError(t, os.WriteFile(stateFile, []byte("{ broken json"), 0644))

	cfg = validScanCfg(t, srv.URL, stateFile)
	// runScan 会 log Warning 但不返回 err（state load err 只 warning）
	_ = withStdoutCapture(t, func() {
		err := runScan(scanCmd, nil)
		assert.NoError(t, err)
	})
}

// runScan 的 existing state no resume（line 152-155）：先跑一次产生 state，再跑不 --resume。
func TestRunScan_ExistingStateNoResume(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")
	cfg = validScanCfg(t, srv.URL, stateFile)
	_ = withStdoutCapture(t, func() { _ = runScan(scanCmd, nil) })

	// 第二次跑：state 已存在，不 --resume → log "Found existing state"
	cfg = validScanCfg(t, srv.URL, stateFile)
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// 不 panic 即可（分支只 log）
}

// runScan 的 ValidateConfig err on resume（line 167-169）：state 存在 + --resume + 不同 RepoURL。
func TestRunScan_ResumeConfigMismatch(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")
	cfg = validScanCfg(t, srv.URL, stateFile)
	_ = withStdoutCapture(t, func() { _ = runScan(scanCmd, nil) })

	// resume 用不同 RepoURL → config mismatch
	cfg = validScanCfg(t, "https://different.example.com", stateFile)
	cfg.Resume = true
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config mismatch on resume")
}

// runScan 的 RetryFailed + Rediscover scanner opts（line 219-221, 222-224）+ resume。
func TestRunScan_ResumeWithRetryAndRediscover(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")
	cfg = validScanCfg(t, srv.URL, stateFile)
	_ = withStdoutCapture(t, func() { _ = runScan(scanCmd, nil) })

	// resume 带 RetryFailed + Rediscover
	cfg = validScanCfg(t, srv.URL, stateFile)
	cfg.Resume = true
	cfg.RetryFailed = true
	cfg.Rediscover = true
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// 不 panic 即可，覆盖 scannerOpts append 分支
}

// runScan 的 Verbose callback FAIL/FOUND log（line 261-267）：Verbose=true + leaky jar。
func TestRunScan_VerboseCallback(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	cfg = validScanCfg(t, srv.URL, filepath.Join(tmpDir, "state.json"))
	cfg.Verbose = true
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// 覆盖 Verbose 分支的 FOUND log
}

// runScan 的 StatusFailed 路径（line 291-293, 299-302, 312-314）：下载失败 → failed result。
func TestRunScan_DownloadFailure(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	// mock repo 返回 404 for jar → 下载失败
	srv := startMockMavenRepo404(t)
	defer srv.Close()

	cfg = validScanCfg(t, srv.URL, filepath.Join(tmpDir, "state.json"))
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// 下载失败 → StatusFailed → MarkFailed + dbStatus=Failed + rec.Error
}

// runScan 的 MarkInterrupted（line 350-352）+ runStatus interrupted（line 365-367）：
// runScan 的 ctx 只被 signal handler 取消，测试无法安全发 signal。这条路径用 cfg.Timeout
// 无法触发（Timeout 只作用于 HTTP 请求，不取消 scan ctx）。改为覆盖 err != nil 路径
// （line 350-352 MarkInterrupted）：让 scan.Run 返回 err。
// scan.Run 在 discover 完全失败时仍返回 nil err（容错），难触发。此处用不可达的 RepoURL
// 让 HTTP 全部失败，summary 0 findings，runStatus="scanned=0"。
func TestRunScan_TaskRunStatusError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	// 不可达的 RepoURL → 所有 HTTP 请求失败
	cfg = &config.Config{
		RepoURL:      "http://127.0.0.1:1/", // 不可达端口
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      100 * time.Millisecond,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    filepath.Join(tmpDir, "state.json"),
		TaskID:       "err-task",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// task 应有 run 记录（runStatus 可能是 scanned=0 或 error）
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	task, err := store.GetTask("err-task")
	require.NoError(t, err)
	assert.NotEmpty(t, task.LastRunStatus)
}

// runScan 的 write report err（line 387-389）：Output=json + OutputFile 不可写。
func TestRunScan_WriteReportError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	cfg = validScanCfg(t, srv.URL, filepath.Join(tmpDir, "state.json"))
	cfg.Output = "json"
	// OutputFile 指向一个已存在的目录 → Write 失败
	cfg.OutputFile = tmpDir
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write report")
}

// runScan 的 NewWorkspace err（line 77-79）：HOME 坏。
func TestRunScan_WorkspaceError(t *testing.T) {
	brokenHomeEnv(t)
	cfg = &config.Config{RepoURL: "https://x", Concurrency: 1, Timeout: time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runScan 的 MarkInterrupted（line 346-348）+ runStatus interrupted（line 365-367）：
// 慢 mock repo 让 scan 长时间运行，发 SIGTERM → runScan 的 signal handler cancel ctx
// → scan.Run 返回（ctx 取消）→ MarkInterrupted + runStatus="interrupted"。
func TestRunScan_SignalInterrupts(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	// 慢 repo：每个响应延迟 500ms，让 scan 有时间接收 signal
	srv := startSlowMockMavenRepo(t, jarPath, 500*time.Millisecond)
	defer srv.Close()

	cfg = &config.Config{
		RepoURL:      srv.URL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      30 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    filepath.Join(tmpDir, "state.json"),
		TaskID:       "sig-task",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}

	done := make(chan struct{})
	go func() {
		_ = withStdoutCapture(t, func() {
			_ = runScan(scanCmd, nil)
		})
		close(done)
	}()
	// 等 scan 启动，发 SIGTERM
	time.Sleep(100 * time.Millisecond)
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runScan should finish after SIGTERM")
	}

	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	task, err := store.GetTask("sig-task")
	require.NoError(t, err)
	assert.Equal(t, "interrupted", task.LastRunStatus)
}

// runScan 的 NewDetector err（line 143-145）：自定义 RulesFile 含坏 file_patterns
// → LoadRulesWithLevel 成功（YAML 不编译）→ NewDetector 的 compileFilePatterns err。
func TestRunScan_NewDetectorError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// 自定义 rules：enabled rule 含坏 file_pattern "[" → compileFilePatterns err
	rulesFile := filepath.Join(tmpDir, "bad-rules.yaml")
	require.NoError(t, os.WriteFile(rulesFile, []byte(`
rules:
  - id: bad-rule
    name: Bad Rule
    severity: HIGH
    enabled: true
    engine: regex
    patterns: ["secret"]
    file_patterns: ["["]
`), 0644))

	cfg = &config.Config{
		RepoURL:    srv.URL,
		GroupFilter: "com.example",
		Concurrency: 1,
		Timeout:    10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel: "core",
		RulesFile:  rulesFile, // merge=false → custom 替换内置
		Output:     "console",
	}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init detector")
}

// runScan 的 Verbose callback FAIL log（line 263-265）：Verbose=true + 下载失败。
func TestRunScan_VerboseFailLog(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	srv := startMockMavenRepo404(t)
	defer srv.Close()

	cfg = validScanCfg(t, srv.URL, filepath.Join(tmpDir, "state.json"))
	cfg.Verbose = true
	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})
	// 下载失败 + Verbose → FAIL log 分支
}

// runScan 的 OpenStore err（line 83-85）：注入 openStoreFn 失败。
func TestRunScan_OpenStoreError(t *testing.T) {
	redirectHome(t)
	saved := openStoreFn
	openStoreFn = func(path string) (*storage.Store, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { openStoreFn = saved })
	cfg = &config.Config{RepoURL: "https://x", GroupFilter: "g", Concurrency: 1, Timeout: time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open database")
}

// runScan 的 SaveTask err（line 130-132）：taskEnabled + closed store → SaveTask 失败。
func TestRunScan_SaveTaskError(t *testing.T) {
	redirectHome(t)
	closedStoreFnForRun(t)
	cfg = &config.Config{
		RepoURL:      "https://x",
		GroupFilter:  "g",
		Concurrency:  1,
		Timeout:      time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		TaskID:       "save-task",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save task")
}
