package storage

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStore opens a fresh store in a temp dir for tasks tests.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	return store
}

func TestTasks_SaveAndGet(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	cfg := map[string]interface{}{"repo": "https://repo.example.com", "concurrency": 5}
	cfgJSON, _ := json.Marshal(cfg)

	task := &TaskRecord{
		TaskID:          "task-001",
		RepoURL:         "https://repo.example.com",
		GroupFilter:     "com.example",
		Config:          cfgJSON,
		ScanIntervalSec: 3600,
		Status:          TaskActive,
		StateFile:       "/tmp/state.json",
	}
	require.NoError(t, store.SaveTask(task))

	loaded, err := store.GetTask("task-001")
	require.NoError(t, err)
	assert.Equal(t, "task-001", loaded.TaskID)
	assert.Equal(t, "https://repo.example.com", loaded.RepoURL)
	assert.Equal(t, "com.example", loaded.GroupFilter)
	assert.Equal(t, int64(3600), loaded.ScanIntervalSec)
	assert.Equal(t, TaskActive, loaded.Status)
	assert.Equal(t, 0, loaded.ScanCount)

	var loadedCfg map[string]interface{}
	require.NoError(t, json.Unmarshal(loaded.Config, &loadedCfg))
	assert.Equal(t, "https://repo.example.com", loadedCfg["repo"])
}

func TestTasks_Upsert(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	task := &TaskRecord{
		TaskID:      "task-upsert",
		RepoURL:     "https://repo.example.com",
		Config:      json.RawMessage(`{}`),
		Status:      TaskActive,
	}
	require.NoError(t, store.SaveTask(task))

	// Update same task_id
	task.Status = TaskPaused
	task.ScanIntervalSec = 7200
	require.NoError(t, store.SaveTask(task))

	loaded, err := store.GetTask("task-upsert")
	require.NoError(t, err)
	assert.Equal(t, TaskPaused, loaded.Status)
	assert.Equal(t, int64(7200), loaded.ScanIntervalSec)
}

func TestTasks_UpdateRunSchedulesNextRun(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	task := &TaskRecord{
		TaskID:          "task-recurring",
		RepoURL:         "https://repo.example.com",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 600, // 10 min
		Status:          TaskActive,
	}
	require.NoError(t, store.SaveTask(task))

	ranAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpdateTaskRun("task-recurring", "scanned=10", ranAt))

	loaded, err := store.GetTask("task-recurring")
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.ScanCount)
	assert.Equal(t, "scanned=10", loaded.LastRunStatus)
	require.True(t, loaded.LastRunAt.Valid)
	assert.Equal(t, ranAt, loaded.LastRunAt.Time)
	require.True(t, loaded.NextRunAt.Valid)
	assert.Equal(t, ranAt.Add(10*time.Minute), loaded.NextRunAt.Time)
	assert.Equal(t, TaskActive, loaded.Status, "recurring task should remain active")
}

func TestTasks_UpdateRunOneShotCompletes(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	task := &TaskRecord{
		TaskID:          "task-onshot",
		RepoURL:         "https://repo.example.com",
		Config:          json.RawMessage(`{}`),
		ScanIntervalSec: 0, // one-shot
		Status:          TaskActive,
	}
	require.NoError(t, store.SaveTask(task))

	require.NoError(t, store.UpdateTaskRun("task-onshot", "done", time.Now()))

	loaded, err := store.GetTask("task-onshot")
	require.NoError(t, err)
	assert.Equal(t, TaskCompleted, loaded.Status)
	assert.False(t, loaded.NextRunAt.Valid, "one-shot task has no next run")
}

func TestTasks_DueTasks(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	now := time.Now()

	// Task A: never run (next_run_at NULL) -> due
	require.NoError(t, store.SaveTask(&TaskRecord{
		TaskID: "task-a", RepoURL: "https://a.example.com",
		Config: json.RawMessage(`{}`), ScanIntervalSec: 3600, Status: TaskActive,
	}))

	// Task B: next_run in the past -> due
	b := &TaskRecord{
		TaskID: "task-b", RepoURL: "https://b.example.com",
		Config: json.RawMessage(`{}`), ScanIntervalSec: 3600, Status: TaskActive,
	}
	require.NoError(t, store.SaveTask(b))
	require.NoError(t, store.UpdateTaskRun("task-b", "ok", now.Add(-2*time.Hour)))

	// Task C: next_run in the future -> not due
	c := &TaskRecord{
		TaskID: "task-c", RepoURL: "https://c.example.com",
		Config: json.RawMessage(`{}`), ScanIntervalSec: 3600, Status: TaskActive,
	}
	require.NoError(t, store.SaveTask(c))
	require.NoError(t, store.UpdateTaskRun("task-c", "ok", now.Add(-30*time.Minute)))

	// Task D: paused -> not due even if next_run passed
	d := &TaskRecord{
		TaskID: "task-d", RepoURL: "https://d.example.com",
		Config: json.RawMessage(`{}`), ScanIntervalSec: 3600, Status: TaskActive,
	}
	require.NoError(t, store.SaveTask(d))
	require.NoError(t, store.UpdateTaskRun("task-d", "ok", now.Add(-2*time.Hour)))
	require.NoError(t, store.SetTaskStatus("task-d", TaskPaused))

	due, err := store.DueTasks(now)
	require.NoError(t, err)
	ids := make(map[string]bool)
	for _, t := range due {
		ids[t.TaskID] = true
	}
	assert.True(t, ids["task-a"], "never-run task should be due")
	assert.True(t, ids["task-b"], "past-due task should be due")
	assert.False(t, ids["task-c"], "future task should not be due")
	assert.False(t, ids["task-d"], "paused task should not be due")
}

func TestTasks_SetTaskStatus(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	require.NoError(t, store.SaveTask(&TaskRecord{
		TaskID: "task-status", RepoURL: "https://x.example.com",
		Config: json.RawMessage(`{}`), Status: TaskActive,
	}))

	require.NoError(t, store.SetTaskStatus("task-status", TaskPaused))
	loaded, err := store.GetTask("task-status")
	require.NoError(t, err)
	assert.Equal(t, TaskPaused, loaded.Status)

	require.NoError(t, store.SetTaskStatus("task-status", TaskActive))
	loaded, err = store.GetTask("task-status")
	require.NoError(t, err)
	assert.Equal(t, TaskActive, loaded.Status)
}

func TestTasks_DeleteTask(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	require.NoError(t, store.SaveTask(&TaskRecord{
		TaskID: "task-del", RepoURL: "https://x.example.com",
		Config: json.RawMessage(`{}`), Status: TaskActive,
	}))

	require.NoError(t, store.DeleteTask("task-del"))
	_, err := store.GetTask("task-del")
	assert.ErrorIs(t, err, ErrTaskNotFound)

	// Delete non-existent
	err = store.DeleteTask("nonexistent")
	assert.ErrorIs(t, err, ErrTaskNotFound)
}

func TestTasks_ListTasks(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	require.NoError(t, store.SaveTask(&TaskRecord{
		TaskID: "t1", RepoURL: "https://a.example.com",
		Config: json.RawMessage(`{}`), Status: TaskActive,
	}))
	require.NoError(t, store.SaveTask(&TaskRecord{
		TaskID: "t2", RepoURL: "https://b.example.com",
		Config: json.RawMessage(`{}`), Status: TaskPaused,
	}))

	all, err := store.ListTasks("")
	require.NoError(t, err)
	assert.Equal(t, 2, len(all))

	active, err := store.ListTasks(string(TaskActive))
	require.NoError(t, err)
	assert.Equal(t, 1, len(active))
	assert.Equal(t, "t1", active[0].TaskID)
}

func TestFormatInterval(t *testing.T) {
	assert.Equal(t, "one-shot", FormatInterval(0))
	assert.Equal(t, "10s", FormatInterval(10))
	assert.Equal(t, "1m0s", FormatInterval(60))
	assert.Equal(t, "1h0m0s", FormatInterval(3600))
}
