package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TaskStatus represents the lifecycle status of a scheduled scan task.
type TaskStatus string

const (
	TaskActive     TaskStatus = "active"     // task is scheduled and will run on interval
	TaskPaused     TaskStatus = "paused"     // task is paused, won't run until resumed
	TaskCompleted  TaskStatus = "completed"  // one-shot task finished
	TaskError      TaskStatus = "error"      // task encountered a terminal error
)

// TaskRecord stores a recurring/one-shot scan task: its full configuration
// (so it can be re-run without re-supplying flags) and its scan progress.
// This is the SQLite-backed counterpart to the JSON ScanState, kept under the
// home workspace so tasks survive across runs and can be queried/managed.
type TaskRecord struct {
	ID              int64          `json:"id"`
	TaskID          string         `json:"task_id"`          // user-facing ID (from --task-id or generated)
	RepoURL         string         `json:"repo_url"`
	GroupFilter     string         `json:"group_filter"`
	Config          json.RawMessage `json:"config"`          // full Config snapshot as JSON
	ScanIntervalSec int64          `json:"scan_interval_sec"` // 0 = one-shot
	Status          TaskStatus     `json:"status"`
	StateFile       string         `json:"state_file"`        // path to the JSON ScanState for resume
	CreatedAt       time.Time      `json:"created_at"`
	LastRunAt       sql.NullTime   `json:"last_run_at"`
	NextRunAt       sql.NullTime   `json:"next_run_at"`
	LastRunStatus   string         `json:"last_run_status"`   // summary of last run
	ScanCount       int            `json:"scan_count"`        // how many times this task has run
}

// migrateTasksTable creates the tasks table (idempotent).
func (s *Store) migrateTasksTable() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS scan_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL UNIQUE,
		repo_url TEXT NOT NULL,
		group_filter TEXT DEFAULT '',
		config TEXT NOT NULL DEFAULT '{}',
		scan_interval_sec INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		state_file TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		last_run_at DATETIME,
		next_run_at DATETIME,
		last_run_status TEXT DEFAULT '',
		scan_count INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_status ON scan_tasks(status);
	CREATE INDEX IF NOT EXISTS idx_tasks_next_run ON scan_tasks(next_run_at);
	`)
	return err
}

// ErrTaskNotFound is returned when a task is not found.
var ErrTaskNotFound = errors.New("task not found")

// SaveTask creates or updates a task record (upsert by task_id).
func (s *Store) SaveTask(t *TaskRecord) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`
	INSERT INTO scan_tasks (task_id, repo_url, group_filter, config, scan_interval_sec, status, state_file, created_at, last_run_at, next_run_at, last_run_status, scan_count)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(task_id) DO UPDATE SET
		repo_url=excluded.repo_url,
		group_filter=excluded.group_filter,
		config=excluded.config,
		scan_interval_sec=excluded.scan_interval_sec,
		status=excluded.status,
		state_file=excluded.state_file,
		last_run_at=excluded.last_run_at,
		next_run_at=excluded.next_run_at,
		last_run_status=excluded.last_run_status,
		scan_count=excluded.scan_count`,
		t.TaskID, t.RepoURL, t.GroupFilter, string(t.Config), t.ScanIntervalSec,
		t.Status, t.StateFile, t.CreatedAt, t.LastRunAt, t.NextRunAt, t.LastRunStatus, t.ScanCount,
	)
	return err
}

// GetTask retrieves a task by its task_id.
func (s *Store) GetTask(taskID string) (*TaskRecord, error) {
	t := &TaskRecord{}
	var configStr string
	err := s.db.QueryRow(`
		SELECT id, task_id, repo_url, group_filter, config, scan_interval_sec, status, state_file,
		       created_at, last_run_at, next_run_at, last_run_status, scan_count
		FROM scan_tasks WHERE task_id = ?`, taskID,
	).Scan(&t.ID, &t.TaskID, &t.RepoURL, &t.GroupFilter, &configStr, &t.ScanIntervalSec,
		&t.Status, &t.StateFile, &t.CreatedAt, &t.LastRunAt, &t.NextRunAt, &t.LastRunStatus, &t.ScanCount)
	if err == sql.ErrNoRows {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Config = json.RawMessage(configStr)
	return t, nil
}

// ListTasks returns all tasks, optionally filtered by status.
func (s *Store) ListTasks(statusFilter string) ([]TaskRecord, error) {
	q := `SELECT id, task_id, repo_url, group_filter, config, scan_interval_sec, status, state_file,
	             created_at, last_run_at, next_run_at, last_run_status, scan_count
	      FROM scan_tasks`
	var args []interface{}
	if statusFilter != "" {
		q += ` WHERE status = ?`
		args = append(args, statusFilter)
	}
	q += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var configStr string
		if err := rows.Scan(&t.ID, &t.TaskID, &t.RepoURL, &t.GroupFilter, &configStr, &t.ScanIntervalSec,
			&t.Status, &t.StateFile, &t.CreatedAt, &t.LastRunAt, &t.NextRunAt, &t.LastRunStatus, &t.ScanCount); err != nil {
			return nil, err
		}
		t.Config = json.RawMessage(configStr)
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// UpdateTaskRun records the result of a task run and schedules the next run
// based on the scan interval. For one-shot tasks (interval=0), marks completed.
func (s *Store) UpdateTaskRun(taskID string, runStatus string, ranAt time.Time) error {
	t, err := s.GetTask(taskID)
	if err != nil {
		return err
	}
	t.LastRunAt = sql.NullTime{Time: ranAt, Valid: true}
	t.LastRunStatus = runStatus
	t.ScanCount++

	if t.ScanIntervalSec > 0 {
		t.NextRunAt = sql.NullTime{Time: ranAt.Add(time.Duration(t.ScanIntervalSec) * time.Second), Valid: true}
		t.Status = TaskActive
	} else {
		// One-shot task: no next run.
		t.NextRunAt = sql.NullTime{}
		t.Status = TaskCompleted
	}
	return s.SaveTask(t)
}

// DeleteTask removes a task by task_id.
func (s *Store) DeleteTask(taskID string) error {
	res, err := s.db.Exec(`DELETE FROM scan_tasks WHERE task_id = ?`, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// SetTaskStatus updates only the status field (e.g. pause/resume).
func (s *Store) SetTaskStatus(taskID string, status TaskStatus) error {
	res, err := s.db.Exec(`UPDATE scan_tasks SET status = ? WHERE task_id = ?`, status, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// DueTasks returns active tasks whose next_run_at has passed (or NULL and
// never run), i.e. tasks that should run now.
func (s *Store) DueTasks(now time.Time) ([]TaskRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, repo_url, group_filter, config, scan_interval_sec, status, state_file,
		       created_at, last_run_at, next_run_at, last_run_status, scan_count
		FROM scan_tasks
		WHERE status = 'active'
		  AND (next_run_at IS NULL OR next_run_at <= ?)
		ORDER BY next_run_at ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var configStr string
		if err := rows.Scan(&t.ID, &t.TaskID, &t.RepoURL, &t.GroupFilter, &configStr, &t.ScanIntervalSec,
			&t.Status, &t.StateFile, &t.CreatedAt, &t.LastRunAt, &t.NextRunAt, &t.LastRunStatus, &t.ScanCount); err != nil {
			return nil, err
		}
		t.Config = json.RawMessage(configStr)
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// FormatDuration converts a scan interval (seconds) to a human-readable string.
func FormatInterval(sec int64) string {
	if sec <= 0 {
		return "one-shot"
	}
	d := time.Duration(sec) * time.Second
	return fmt.Sprintf("%s", d.Round(time.Second))
}
