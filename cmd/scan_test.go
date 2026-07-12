package cmd

import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLeakyJar 构造一个含硬编码密码的 jar，用于 runScan 端到端测试。
func makeLeakyJar(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	w := zip.NewWriter(f)
	fw, err := w.Create("application.properties")
	require.NoError(t, err)
	_, err = fw.Write([]byte("db.password = HardcodedSecret123\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

// startMockMavenRepo 启动一个本地 HTTP 仓库，serve 一个含泄露的 jar。
func startMockMavenRepo(t *testing.T, jarPath string) *httptest.Server {
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
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

func TestRunScan_FullPipeline_FindsLeak(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()

	// 准备含泄露的 jar
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// 设置全局 cfg（runScan 读包级 cfg）
	stateFile := filepath.Join(tmpDir, "state.json")
	cfg = &config.Config{
		RepoURL:            srv.URL,
		GroupFilter:        "com.example",
		Concurrency:        2,
		Timeout:            10 * time.Second,
		MaxFileSize:        "50MB",
		RulesLevel:         "core",
		StateFile:          stateFile,
		Output:             "json",
		OutputFile:         filepath.Join(tmpDir, "report.json"),
	}

	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})

	// 验证报告文件已写出且包含泄露
	reportBytes, readErr := os.ReadFile(filepath.Join(tmpDir, "report.json"))
	require.NoError(t, readErr)
	reportStr := string(reportBytes)
	assert.Contains(t, reportStr, "com.example:leaky:1.0")
	assert.Contains(t, reportStr, "hardcoded-password")
}

func TestRunScan_InvalidConfig(t *testing.T) {
	redirectHome(t)
	cfg = &config.Config{RepoURL: "", Concurrency: 1, Timeout: 1 * time.Second, MaxFileSize: "50MB", RulesLevel: "core"}
	err := runScan(scanCmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid configuration")
}

func TestRunScan_TaskEnabled_PersistsTask(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	cfg = &config.Config{
		RepoURL:      srv.URL,
		GroupFilter:  "com.example",
		Concurrency:  1,
		Timeout:      10 * time.Second,
		MaxFileSize:  "50MB",
		RulesLevel:   "core",
		StateFile:    filepath.Join(tmpDir, "state.json"),
		TaskID:       "scan-task-1",
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}

	_ = withStdoutCapture(t, func() {
		_ = runScan(scanCmd, nil)
	})

	// 任务应已持久化
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	task, err := store.GetTask("scan-task-1")
	require.NoError(t, err)
	assert.Equal(t, "scan-task-1", task.TaskID)
	assert.Equal(t, srv.URL, task.RepoURL)
	assert.Equal(t, int64(3600), task.ScanIntervalSec)
}

func TestTaskRun_WithDueTask(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// 预存一个 due task（next_run_at 为过去时间或 NULL）
	store, err := openTaskStore()
	require.NoError(t, err)
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:        "due-task-1",
		RepoURL:       srv.URL,
		GroupFilter:   "com.example",
		Config:        []byte(`{}`),
		ScanIntervalSec: 3600,
		Status:        storage.TaskActive,
		StateFile:     filepath.Join(tmpDir, "state.json"),
		CreatedAt:     time.Now(),
	}))
	store.Close()

	_ = withStdoutCapture(t, func() {
		_ = runTaskRun(nil, nil)
	})

	// due task 应被运行，state file 应已创建（runScan 持久化）
	_, statErr := os.Stat(filepath.Join(tmpDir, "state.json"))
	assert.NoError(t, statErr, "state file should be created by task run")
}

func TestRunScan_Resume(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	stateFile := filepath.Join(tmpDir, "state.json")

	// 第一次扫描
	cfg = &config.Config{
		RepoURL: srv.URL, GroupFilter: "com.example",
		Concurrency: 1, Timeout: 10 * time.Second, MaxFileSize: "50MB",
		RulesLevel: "core", StateFile: stateFile,
	}
	_ = withStdoutCapture(t, func() { _ = runScan(scanCmd, nil) })

	// state 文件应已生成
	_, err := os.Stat(stateFile)
	require.NoError(t, err)

	// 第二次扫描带 --resume（cfg.Resume=true）
	cfg = &config.Config{
		RepoURL: srv.URL, GroupFilter: "com.example",
		Concurrency: 1, Timeout: 10 * time.Second, MaxFileSize: "50MB",
		RulesLevel: "core", StateFile: stateFile, Resume: true,
	}
	_ = withStdoutCapture(t, func() { _ = runScan(scanCmd, nil) })
	// resume 应正常完成（已完成的 artifact 被跳过），不报配置不匹配
}
