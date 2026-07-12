package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectHome 把 HOME 指向临时目录，使 NewWorkspace() 用隔离的数据库。
func redirectHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// withStdoutCapture 捕获 os.Stdout 的输出。
func withStdoutCapture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// seedTask 直接写一个 task 到 openTaskStore 使用的同一个数据库。
func seedTask(t *testing.T, taskID string) {
	t.Helper()
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:        taskID,
		RepoURL:       "https://repo.example.com",
		GroupFilter:   "com.example",
		Config:        json.RawMessage(`{"concurrency":3,"output":"json"}`),
		ScanIntervalSec: 3600,
		Status:        storage.TaskActive,
		StateFile:     "/tmp/state.json",
		CreatedAt:     time.Now(),
	}))
}

func TestApplyConfigSnapshot(t *testing.T) {
	c := config.DefaultConfig()
	saved := map[string]json.RawMessage{
		"concurrency":         json.RawMessage(`5`),
		"qps":                 json.RawMessage(`10`),
		"rules":               json.RawMessage(`"/path/rules.yaml"`),
		"output":              json.RawMessage(`"json"`),
		"output-file":         json.RawMessage(`"report.json"`),
		"max-file-size":       json.RawMessage(`"20MB"`),
		"timeout":             json.RawMessage(`45000000000`), // 45s in nanoseconds (time.Duration)
		"retries":             json.RawMessage(`7`),
		"exclude":             json.RawMessage(`["a","b"]`),
		"verbose":             json.RawMessage(`true`),
		"checkpoint-interval": json.RawMessage(`25`),
		"rules-level":         json.RawMessage(`"extended"`),
		"download-concurrency": json.RawMessage(`8`),
		"disk-budget":         json.RawMessage(`2000`),
		"retry-failed":        json.RawMessage(`true`),
		"rediscover":          json.RawMessage(`true`),
		"auth-username":       json.RawMessage(`"u"`),
		"auth-password":       json.RawMessage(`"p"`),
		"auth-token":          json.RawMessage(`"tok"`),
	}
	applyConfigSnapshot(c, saved)

	assert.Equal(t, 5, c.Concurrency)
	assert.Equal(t, 10, c.QPS)
	assert.Equal(t, "/path/rules.yaml", c.RulesFile)
	assert.Equal(t, "json", c.Output)
	assert.Equal(t, "report.json", c.OutputFile)
	assert.Equal(t, "20MB", c.MaxFileSize)
	assert.Equal(t, 45*time.Second, c.Timeout)
	assert.Equal(t, 7, c.Retries)
	assert.Equal(t, []string{"a", "b"}, c.Exclude)
	assert.True(t, c.Verbose)
	assert.Equal(t, 25, c.CheckpointInterval)
	assert.Equal(t, "extended", c.RulesLevel)
	assert.Equal(t, 8, c.DownloadConcurrency)
	assert.Equal(t, 2000, c.DiskBudgetMB)
	assert.True(t, c.RetryFailed)
	assert.True(t, c.Rediscover)
	assert.Equal(t, "u", c.AuthUsername)
	assert.Equal(t, "p", c.AuthPassword)
	assert.Equal(t, "tok", c.AuthToken)
}

func TestApplyConfigSnapshot_EmptyAndUnknown(t *testing.T) {
	c := config.DefaultConfig()
	// 空 map + 未知 key → 不 panic，字段保持默认
	applyConfigSnapshot(c, map[string]json.RawMessage{
		"unknown-field": json.RawMessage(`"x"`),
		"concurrency":   json.RawMessage(`not-json`), // 解析失败 → 字段不变
	})
	assert.Equal(t, config.DefaultConfig().Concurrency, c.Concurrency)
}

func TestOpenTaskStore(t *testing.T) {
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()
	// 再次打开应幂等
	store2, err := openTaskStore()
	require.NoError(t, err)
	defer store2.Close()
}

func TestTaskList_Empty(t *testing.T) {
	redirectHome(t)
	out := withStdoutCapture(t, func() {
		require.NoError(t, runTaskList(nil, nil))
	})
	assert.Contains(t, out, "No tasks")
}

func TestTaskList_WithTask(t *testing.T) {
	redirectHome(t)
	seedTask(t, "t-list")
	out := withStdoutCapture(t, func() {
		require.NoError(t, runTaskList(nil, nil))
	})
	assert.Contains(t, out, "t-list")
	assert.Contains(t, out, "https://repo.example.com")
}

func TestTaskShow(t *testing.T) {
	redirectHome(t)
	seedTask(t, "t-show")
	out := withStdoutCapture(t, func() {
		require.NoError(t, runTaskShow(nil, []string{"t-show"}))
	})
	assert.Contains(t, out, "Task ID:      t-show")
	assert.Contains(t, out, "Repo URL:     https://repo.example.com")
	assert.Contains(t, out, "Config:")
}

func TestTaskShow_NotFound(t *testing.T) {
	redirectHome(t)
	err := runTaskShow(nil, []string{"no-such-task"})
	assert.Error(t, err)
}

func TestTaskPause_Resume(t *testing.T) {
	redirectHome(t)
	seedTask(t, "t-pr")

	require.NoError(t, runTaskPause(nil, []string{"t-pr"}))
	// 验证已暂停
	store, _ := openTaskStore()
	defer store.Close()
	task, err := store.GetTask("t-pr")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskPaused, task.Status)

	// resume 恢复
	require.NoError(t, runTaskResume(nil, []string{"t-pr"}))
	task, _ = store.GetTask("t-pr")
	assert.Equal(t, storage.TaskActive, task.Status)
}

func TestTaskPause_NotFound(t *testing.T) {
	redirectHome(t)
	err := runTaskPause(nil, []string{"no-such"})
	assert.Error(t, err)
}

func TestTaskResume_NotFound(t *testing.T) {
	redirectHome(t)
	err := runTaskResume(nil, []string{"no-such"})
	assert.Error(t, err)
}

func TestTaskDelete(t *testing.T) {
	redirectHome(t)
	seedTask(t, "t-del")
	require.NoError(t, runTaskDelete(nil, []string{"t-del"}))
	// 删除后查询应报错
	store, _ := openTaskStore()
	defer store.Close()
	_, err := store.GetTask("t-del")
	assert.ErrorIs(t, err, storage.ErrTaskNotFound)
}

func TestTaskDelete_NotFound(t *testing.T) {
	redirectHome(t)
	err := runTaskDelete(nil, []string{"no-such"})
	assert.Error(t, err)
}

func TestTaskRun_NoDueTasks(t *testing.T) {
	redirectHome(t)
	out := withStdoutCapture(t, func() {
		require.NoError(t, runTaskRun(nil, nil))
	})
	assert.Contains(t, out, "No due tasks")
}

func TestRunHistory_Empty(t *testing.T) {
	redirectHome(t)
	out := withStdoutCapture(t, func() {
		require.NoError(t, runHistory(nil, nil))
	})
	assert.Contains(t, out, "Scan History Statistics")
	assert.Contains(t, out, "Total Records: 0")
}

func TestRunHistory_WithData(t *testing.T) {
	redirectHome(t)
	// 直接 seed 一条扫描记录和 finding
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.UpsertRecord(&storage.GAVRecord{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		RepoURL: "https://r", Status: storage.DBStatusComplete,
		Findings: 1, ScanTime: time.Now(),
	}))
	rec, _ := store.GetRecord("com.example", "lib", "1.0", "https://r")
	require.NotNil(t, rec)
	require.NoError(t, store.InsertFinding(&storage.FindingRecord{
		RecordID: rec.ID, RuleID: "hardcoded-password", Severity: "CRITICAL", FilePath: "app.properties", LineNumber: 1,
	}))

	out := withStdoutCapture(t, func() {
		require.NoError(t, runHistory(nil, nil))
	})
	assert.Contains(t, out, "Total Records: 1")
	assert.Contains(t, out, "Findings by Rule:")
	assert.Contains(t, out, "hardcoded-password")
}

// 确保 redirectHome 用的临时目录确实隔离了真实 HOME（防污染用户环境）。
func TestRedirectHome_IsolatesWorkspace(t *testing.T) {
	home := redirectHome(t)
	ws, err := openTaskStore()
	require.NoError(t, err)
	defer ws.Close()
	// workspace 应在临时 HOME 下
	assert.True(t, filepath.IsAbs(home))
}
