package cmd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenHomeEnv 把 HOME 指向一个文件（非目录），让 NewWorkspace 的 MkdirAll 失败，
// 覆盖 openTaskStore 的 "init workspace" err 分支（line 74-76）以及所有 runTask*
// 函数里 openTaskStore err 的 return（86-88, 119-121, 155-157, 169-171, 183-185, 201-203）。
func brokenHomeEnv(t *testing.T) {
	t.Helper()
	// HOME 指向一个已存在的文件 → UserHomeDir 成功返回该路径，但 MkdirAll 失败。
	filePath := t.TempDir() + "/not-a-home"
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0644))
	t.Setenv("HOME", filePath)
}

// runTaskList 的 openTaskStore err 分支 + ListTasks err 分支 + 非空任务输出。
func TestRunTaskList_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskList(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskShow 的 openTaskStore err 分支。
func TestRunTaskShow_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskShow(nil, []string{"t1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskPause 的 openTaskStore err 分支。
func TestRunTaskPause_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskPause(nil, []string{"t1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskResume 的 openTaskStore err 分支。
func TestRunTaskResume_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskResume(nil, []string{"t1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskDelete 的 openTaskStore err 分支。
func TestRunTaskDelete_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskDelete(nil, []string{"t1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskRun 的 openTaskStore err 分支。
func TestRunTaskRun_OpenStoreError(t *testing.T) {
	brokenHomeEnv(t)
	err := runTaskRun(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runTaskShow 的 LastRunAt.Valid + NextRunAt.Valid 输出分支（line 136-138, 140-142）：
// seed 一个带 LastRunAt 和 NextRunAt 的 task。
func TestRunTaskShow_WithRunTimestamps(t *testing.T) {
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	now := time.Now()
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:          "ts-task",
		RepoURL:         "https://repo.example.com",
		GroupFilter:     "com.example",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 3600,
		Status:          storage.TaskActive,
		StateFile:       "/tmp/state.json",
		CreatedAt:       now,
		LastRunAt:       sql.NullTime{Valid: true, Time: now},
		LastRunStatus:   "ok",
		NextRunAt:       sql.NullTime{Valid: true, Time: now.Add(time.Hour)},
	}))

	out := withStdoutCapture(t, func() {
		err := runTaskShow(nil, []string{"ts-task"})
		assert.NoError(t, err)
	})
	assert.Contains(t, out, "Last Run:")
	assert.Contains(t, out, "Next Run:")
}

// runTaskList 的 ListTasks err 分支（line 92-94）：用 closed store 无法注入（runTaskList
// 自建 store）。改为覆盖 ListTasks 非空 + NextRunAt.Valid 输出（line 105-107）。
func TestRunTaskList_WithNextRun(t *testing.T) {
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	now := time.Now()
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:          "list-task",
		RepoURL:         "https://repo.example.com",
		GroupFilter:     "com.example",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 3600,
		Status:          storage.TaskActive,
		StateFile:       "/tmp/state.json",
		CreatedAt:       now,
		NextRunAt:       sql.NullTime{Valid: true, Time: now.Add(time.Hour)},
	}))

	out := withStdoutCapture(t, func() {
		err := runTaskList(nil, nil)
		assert.NoError(t, err)
	})
	assert.Contains(t, out, "list-task")
}

// runTaskRun 的 runTaskOnce err log 分支（line 220-222）：seed 一个 due task 但
// RepoURL 空 → runTaskOnce Validate 失败 → runTaskRun 走 err log（stderr）。
func TestRunTaskRun_DueTaskFails(t *testing.T) {
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	defer store.Close()
	// past NextRunAt → due；RepoURL 空 → runTaskOnce Validate err
	require.NoError(t, store.SaveTask(&storage.TaskRecord{
		TaskID:          "due-task",
		RepoURL:         "", // 空 → Validate err
		GroupFilter:     "com.example",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 3600,
		Status:          storage.TaskActive,
		StateFile:       "/tmp/nonexistent-state.json",
		CreatedAt:       time.Now(),
		NextRunAt:       sql.NullTime{Valid: true, Time: time.Now().Add(-time.Hour)},
	}))

	// runTaskRun 调 runTaskOnce 失败 → 走 stderr log。捕获 stderr。
	err = runTaskRun(nil, nil)
	require.NoError(t, err) // runTaskRun 本身不返回 err（task err 只 log）
}

// runHistory 的 NewWorkspace err 分支（line 28-30）：HOME 坏。
func TestRunHistory_WorkspaceError(t *testing.T) {
	brokenHomeEnv(t)
	err := runHistory(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init workspace")
}

// runHistory 的 OpenStore err 分支（line 33-35）：注入 openStoreFn 失败。
func TestRunHistory_OpenStoreError(t *testing.T) {
	redirectHome(t)
	saved := openStoreFn
	openStoreFn = func(path string) (*storage.Store, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { openStoreFn = saved })
	err := runHistory(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open database")
}

// closedStoreFnForRun 注入 openStoreFn，返回一个已关闭的 store，让 runHistory/runScan
// 后续的 SQL 操作（GetStats/RecentRecords/SaveTask/UpdateTaskRun 等）失败。
func closedStoreFnForRun(t *testing.T) {
	t.Helper()
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	require.NoError(t, store.Close())
	saved := openStoreFn
	openStoreFn = func(path string) (*storage.Store, error) { return store, nil }
	t.Cleanup(func() { openStoreFn = saved })
}

// runHistory 的 GetStats err 分支（line 40-42）：closed store → GetStats 失败。
func TestRunHistory_GetStatsError(t *testing.T) {
	closedStoreFnForRun(t)
	err := runHistory(nil, nil)
	require.Error(t, err)
}

// runHistory 的 RecentRecords err 分支（line 72-74）：GetStats 成功但 RecentRecords
// 的 Scan 失败。用坏数据触发：findings_count 列存文本 → RecentRecords Scan int 失败，
// 而 GetStats 的 COUNT/SUM 不读该值故仍成功。
func TestRunHistory_RecentRecordsError(t *testing.T) {
	redirectHome(t)
	// 打开正常 store，seed 一行 findings_count='NOT_AN_INT' 的坏数据
	store, err := openTaskStore()
	require.NoError(t, err)
	_, err = store.DB().Exec(`INSERT INTO scan_records (group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms) VALUES ('a','b','1','r','complete','NOT_AN_INT','2020-01-01',0)`)
	require.NoError(t, err)
	// 注入：openStoreFn 返回这个含坏数据的 store
	saved := openStoreFn
	openStoreFn = func(path string) (*storage.Store, error) { return store, nil }
	t.Cleanup(func() { openStoreFn = saved; store.Close() })

	err = runHistory(nil, nil)
	require.Error(t, err) // RecentRecords Scan 失败 → runHistory return err
}

// closedTaskStoreFn 替换 openTaskStoreFn，返回一个已关闭的 store，让所有后续
// SQL 操作（ListTasks/GetTask/SetTaskStatus/DeleteTask/DueTasks）失败，覆盖各 runTask*
// 的 store err 转发分支。t.Cleanup 恢复 openTaskStoreFn。
func closedTaskStoreFn(t *testing.T) {
	t.Helper()
	redirectHome(t)
	store, err := openTaskStore()
	require.NoError(t, err)
	require.NoError(t, store.Close()) // 关闭 → 后续 Query 失败
	saved := openTaskStoreFn
	openTaskStoreFn = func() (*storage.Store, error) { return store, nil }
	t.Cleanup(func() { openTaskStoreFn = saved })
}

// runTaskList 的 ListTasks err 分支（line 92-94）：closed store → ListTasks 失败。
func TestRunTaskList_ListTasksError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskList(nil, nil)
	require.Error(t, err)
}

// runTaskShow 的 GetTask err 分支（line 124-127）：closed store → GetTask 失败。
func TestRunTaskShow_GetTaskError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskShow(nil, []string{"any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get task")
}

// runTaskPause 的 SetTaskStatus err 分支（line 160-162）：closed store。
func TestRunTaskPause_SetStatusError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskPause(nil, []string{"any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pause task")
}

// runTaskResume 的 SetTaskStatus err 分支（line 174-176）：closed store。
func TestRunTaskResume_SetStatusError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskResume(nil, []string{"any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resume task")
}

// runTaskDelete 的 DeleteTask err 分支（line 188-190）：closed store。
func TestRunTaskDelete_DeleteError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskDelete(nil, []string{"any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete task")
}

// runTaskRun 的 DueTasks err 分支（line 207-209）：closed store → DueTasks 失败。
func TestRunTaskRun_DueTasksError(t *testing.T) {
	closedTaskStoreFn(t)
	err := runTaskRun(nil, nil)
	require.Error(t, err)
}

// runTaskOnce 的 Validate err 分支（line 245-247）：task config 无效。
func TestRunTaskOnce_InvalidConfig(t *testing.T) {
	redirectHome(t)
	task := storage.TaskRecord{
		TaskID:          "bad-task",
		RepoURL:         "", // 空 RepoURL 触发 Validate err
		GroupFilter:     "com.example",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 3600,
		Status:          storage.TaskActive,
		StateFile:       "/tmp/state.json",
		CreatedAt:       time.Now(),
	}
	err := runTaskOnce(task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task config")
}

// openTaskStore 的 OpenStore err 分支（line 83-85）：注入 openStoreFn 失败。
// workspace 路径下 storage.OpenStore 几乎不失败，所以用包级间接注入。
func TestOpenTaskStore_OpenStoreError(t *testing.T) {
	redirectHome(t)
	saved := openStoreFn
	openStoreFn = func(path string) (*storage.Store, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { openStoreFn = saved })
	_, err := openTaskStore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open database")
}
