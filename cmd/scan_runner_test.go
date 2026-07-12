package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errScanner 是 scanRunner 的 mock：OnResult 存回调，Run 返回预设 err。
// 用于覆盖 runScan 的 "scan.Run 返回 err" 分支（line 350-352 MarkInterrupted、
// 363-365 runStatus="error"、375-377 return err）——真实 Scanner.Run 容错不返回 err。
type errScanner struct {
	runErr error
	cb     scanner.ProgressCallback
}

func (e *errScanner) OnResult(cb scanner.ProgressCallback) { e.cb = cb }
func (e *errScanner) Run(ctx context.Context) (*scanner.Summary, error) {
	return nil, e.runErr
}

// runScan 的 scan.Run err 分支：注入 errScanner 让 Run 返回 err，覆盖
// MarkInterrupted（350-352）、runStatus="error"（363-365）、return err（375-377）。
// 需 taskEnabled 让 runStatus 分支走 + scanState 让 MarkInterrupted 走。
func TestRunScan_RunError(t *testing.T) {
	redirectHome(t)
	saved := newScannerFn
	newScannerFn = func(c *config.Config, opts ...scanner.ScannerOption) scanRunner {
		return &errScanner{runErr: errors.New("scan boom")}
	}
	t.Cleanup(func() { newScannerFn = saved })

	// 用一个能成功 discovery 的 mock repo（让 runScan 走到 scan.Run）
	tmpDir := t.TempDir()
	jarPath := tmpDir + "/leaky-1.0.jar"
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
		TaskID:       "runerr-task", // taskEnabled → runStatus 分支
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}
	err := runScan(scanCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan failed")

	// task 应记录 runStatus="error: scan boom"
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	task, err := store.GetTask("runerr-task")
	require.NoError(t, err)
	assert.Contains(t, task.LastRunStatus, "error: scan boom")
}

// runScan 的 Flush err log（line 369-371）：StateFile 在只读目录 → Flush 写 .tmp 失败。
// scanState != nil（runScan 新建）+ scan.Run 返回（用真实 scanner，mock repo）→
// 走到 MarkCompletedStatus + Flush → Flush err → log warning（不 return err）。
func TestRunScan_FlushError(t *testing.T) {
	redirectHome(t)
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "leaky-1.0.jar")
	makeLeakyJar(t, jarPath)
	srv := startMockMavenRepo(t, jarPath)
	defer srv.Close()

	// 只读目录放 StateFile
	roDir := filepath.Join(tmpDir, "ro")
	require.NoError(t, os.MkdirAll(roDir, 0755))
	require.NoError(t, os.Chmod(roDir, 0500))
	t.Cleanup(func() { os.Chmod(roDir, 0755) })

	cfg = &config.Config{
		RepoURL:     srv.URL,
		GroupFilter: "com.example",
		Concurrency: 1,
		Timeout:     10 * time.Second,
		MaxFileSize: "50MB",
		RulesLevel:  "core",
		StateFile:   filepath.Join(roDir, "state.json"), // 只读目录 → Flush .tmp 失败
		Output:      "console",
	}
	_ = withStdoutCapture(t, func() {
		err := runScan(scanCmd, nil)
		assert.NoError(t, err) // Flush err 只 log，不 return
	})
}

// mutatingErrScanner 在 Run 期间把 cfg.TaskID 改成一个不存在的值，使 runScan 后续
// 的 UpdateTaskRun(cfg.TaskID) 找不到 task → GetTask err → UpdateTaskRun err →
// 覆盖 line 386-388 的 "failed to update task run" warning log。
type mutatingErrScanner struct {
	cb          scanner.ProgressCallback
	newTaskID   string
	originalID  string
	returnedErr bool
}

func (m *mutatingErrScanner) OnResult(cb scanner.ProgressCallback) { m.cb = cb }
func (m *mutatingErrScanner) Run(ctx context.Context) (*scanner.Summary, error) {
	// SaveTask 已用原 TaskID 存好；改成一个不存在的，让 UpdateTaskRun 的 GetTask 失败。
	m.originalID = cfg.TaskID
	cfg.TaskID = m.newTaskID
	m.returnedErr = true
	return nil, errors.New("mutating boom")
}

// runScan 的 UpdateTaskRun err log（line 386-388）：mutatingErrScanner 在 Run 期间
// 把 cfg.TaskID 改成不存在的值 → SaveTask（原 ID）成功，但 UpdateTaskRun（新 ID）
// 的 GetTask 失败 → log warning。Run 返回 err → runScan return err（覆盖此路径）。
func TestRunScan_UpdateTaskRunError(t *testing.T) {
	redirectHome(t)
	saved := newScannerFn
	mut := &mutatingErrScanner{newTaskID: "nonexistent-task"}
	newScannerFn = func(c *config.Config, opts ...scanner.ScannerOption) scanRunner {
		return mut
	}
	t.Cleanup(func() {
		newScannerFn = saved
		cfg.TaskID = mut.originalID // 恢复
	})

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
		TaskID:       "updt-task", // taskEnabled → 走 UpdateTaskRun 分支
		ScanInterval: 1 * time.Hour,
		Output:       "console",
	}
	err := runScan(scanCmd, nil)
	require.Error(t, err)            // Run 返回 err → scan failed
	assert.True(t, mut.returnedErr) // 确认 Run 被调且改了 TaskID
}
