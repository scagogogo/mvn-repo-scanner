package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/spf13/cobra"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage scheduled scan tasks",
	Long:  "List, show, pause, resume, and delete recurring scan tasks persisted in the local SQLite database.",
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scan tasks",
	RunE:  runTaskList,
}

var taskShowCmd = &cobra.Command{
	Use:   "show [task-id]",
	Short: "Show details of a scan task",
	Args:  cobra.ExactArgs(1),
	RunE:  runTaskShow,
}

var taskPauseCmd = &cobra.Command{
	Use:   "pause [task-id]",
	Short: "Pause a scheduled scan task",
	Args:  cobra.ExactArgs(1),
	RunE:  runTaskPause,
}

var taskResumeCmd = &cobra.Command{
	Use:   "resume [task-id]",
	Short: "Resume a paused scan task",
	Args:  cobra.ExactArgs(1),
	RunE:  runTaskResume,
}

var taskDeleteCmd = &cobra.Command{
	Use:   "delete [task-id]",
	Short: "Delete a scan task",
	Args:  cobra.ExactArgs(1),
	RunE:  runTaskDelete,
}

var taskRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run all due scan tasks (for cron-style execution)",
	RunE:  runTaskRun,
}

func init() {
	rootCmd.AddCommand(taskCmd)
	taskCmd.AddCommand(taskListCmd)
	taskCmd.AddCommand(taskShowCmd)
	taskCmd.AddCommand(taskPauseCmd)
	taskCmd.AddCommand(taskResumeCmd)
	taskCmd.AddCommand(taskDeleteCmd)
	taskCmd.AddCommand(taskRunCmd)
}

// openTaskStore opens the workspace SQLite store for task management.
func openTaskStore() (*storage.Store, error) {
	ws, err := storage.NewWorkspace()
	if err != nil {
		return nil, fmt.Errorf("init workspace: %w", err)
	}
	store, err := storage.OpenStore(ws.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return store, nil
}

func runTaskList(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	tasks, err := store.ListTasks("")
	if err != nil {
		return err
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks. Use 'scan --scan-interval ...' to create a recurring task.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASKID\tREPO\tGROUP\tINTERVAL\tSTATUS\tNEXT_RUN\tSCANS")
	for _, t := range tasks {
		nextRun := "-"
		if t.NextRunAt.Valid {
			nextRun = t.NextRunAt.Time.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			t.TaskID, t.RepoURL, t.GroupFilter,
			storage.FormatInterval(t.ScanIntervalSec),
			t.Status, nextRun, t.ScanCount)
	}
	w.Flush()
	return nil
}

func runTaskShow(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := store.GetTask(args[0])
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}

	fmt.Printf("Task ID:      %s\n", task.TaskID)
	fmt.Printf("Repo URL:     %s\n", task.RepoURL)
	fmt.Printf("Group Filter: %s\n", task.GroupFilter)
	fmt.Printf("Interval:     %s\n", storage.FormatInterval(task.ScanIntervalSec))
	fmt.Printf("Status:       %s\n", task.Status)
	fmt.Printf("State File:   %s\n", task.StateFile)
	fmt.Printf("Created:      %s\n", task.CreatedAt.Format(time.RFC3339))
	if task.LastRunAt.Valid {
		fmt.Printf("Last Run:     %s (%s)\n", task.LastRunAt.Time.Format(time.RFC3339), task.LastRunStatus)
	}
	fmt.Printf("Scan Count:   %d\n", task.ScanCount)
	if task.NextRunAt.Valid {
		fmt.Printf("Next Run:     %s\n", task.NextRunAt.Time.Format(time.RFC3339))
	}

	// Pretty-print the config snapshot.
	var snap interface{}
	if err := json.Unmarshal(task.Config, &snap); err == nil {
		pretty, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Printf("Config:\n%s\n", string(pretty))
	}
	return nil
}

func runTaskPause(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.SetTaskStatus(args[0], storage.TaskPaused); err != nil {
		return fmt.Errorf("pause task: %w", err)
	}
	fmt.Printf("Task %s paused.\n", args[0])
	return nil
}

func runTaskResume(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.SetTaskStatus(args[0], storage.TaskActive); err != nil {
		return fmt.Errorf("resume task: %w", err)
	}
	fmt.Printf("Task %s resumed.\n", args[0])
	return nil
}

func runTaskDelete(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.DeleteTask(args[0]); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	fmt.Printf("Task %s deleted.\n", args[0])
	return nil
}

// runTaskRun executes all due tasks. This is intended for cron-style invocation:
// run `mvn-repo-scanner task run` on a schedule, and it will launch any task
// whose next_run_at has passed. Each due task runs a scan with its persisted
// config and resume state.
func runTaskRun(cmd *cobra.Command, args []string) error {
	store, err := openTaskStore()
	if err != nil {
		return err
	}
	defer store.Close()

	due, err := store.DueTasks(time.Now())
	if err != nil {
		return err
	}
	if len(due) == 0 {
		fmt.Println("No due tasks.")
		return nil
	}

	fmt.Printf("Running %d due task(s)...\n", len(due))
	for _, task := range due {
		fmt.Printf("\n=== Running task %s (repo=%s) ===\n", task.TaskID, task.RepoURL)
		// runTaskOnce -> runScan records the run result via UpdateTaskRun
		// (success, error, or interrupted), so we only log here.
		if err := runTaskOnce(task); err != nil {
			fmt.Fprintf(os.Stderr, "Task %s failed: %v\n", task.TaskID, err)
		}
	}
	return nil
}

// runTaskOnce reconstructs the scan config from a task record and runs a scan.
// It does so by temporarily swapping the global `cfg` (which runScan reads)
// with one rebuilt from the task's persisted config snapshot, then restoring it.
func runTaskOnce(task storage.TaskRecord) error {
	taskCfg := config.DefaultConfig()
	// Overlay persisted values from the config snapshot.
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(task.Config, &saved); err == nil {
		applyConfigSnapshot(taskCfg, saved)
	}
	// Task-scoped fields always win over the snapshot.
	taskCfg.RepoURL = task.RepoURL
	taskCfg.GroupFilter = task.GroupFilter
	taskCfg.StateFile = task.StateFile
	taskCfg.Resume = true // tasks always resume from their state file
	taskCfg.TaskID = task.TaskID
	taskCfg.ScanInterval = time.Duration(task.ScanIntervalSec) * time.Second

	if err := taskCfg.Validate(); err != nil {
		return fmt.Errorf("invalid task config: %w", err)
	}

	// Swap the global cfg so runScan picks up the task config, then restore.
	// runScan itself will record the run result and schedule the next run via
	// UpdateTaskRun, since taskCfg.TaskID is set.
	savedCfg := cfg
	cfg = taskCfg
	defer func() { cfg = savedCfg }()

	return runScan(scanCmd, nil)
}

// applyConfigSnapshot overlays recognized fields from a persisted config
// snapshot onto a Config. Unknown fields are ignored for forward compatibility.
func applyConfigSnapshot(c *config.Config, saved map[string]json.RawMessage) {
	// Helper to decode a single key into a target.
	dec := func(key string, target interface{}) {
		if raw, ok := saved[key]; ok {
			_ = json.Unmarshal(raw, target)
		}
	}
	dec("concurrency", &c.Concurrency)
	dec("qps", &c.QPS)
	dec("rules", &c.RulesFile)
	dec("output", &c.Output)
	dec("output-file", &c.OutputFile)
	dec("max-file-size", &c.MaxFileSize)
	dec("timeout", &c.Timeout)
	dec("retries", &c.Retries)
	dec("exclude", &c.Exclude)
	dec("verbose", &c.Verbose)
	dec("checkpoint-interval", &c.CheckpointInterval)
	dec("rules-level", &c.RulesLevel)
	dec("download-concurrency", &c.DownloadConcurrency)
	dec("disk-budget", &c.DiskBudgetMB)
	dec("retry-failed", &c.RetryFailed)
	dec("rediscover", &c.Rediscover)
	dec("auth-username", &c.AuthUsername)
	dec("auth-password", &c.AuthPassword)
	dec("auth-token", &c.AuthToken)
}
