package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/report"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan a Maven repository for sensitive content",
	Long:  "Traverse a Maven repository (central or private) and scan artifacts for sensitive content like passwords, API keys, and certificates.",
	RunE:  runScan,
}

func init() {
	rootCmd.AddCommand(scanCmd)

	scanCmd.Flags().StringVarP(&cfg.RepoURL, "repo", "r", cfg.RepoURL, "Maven repository URL")
	scanCmd.Flags().StringVarP(&cfg.GroupFilter, "group", "g", "", "groupId prefix filter (e.g. com.example)")
	scanCmd.Flags().IntVarP(&cfg.Concurrency, "concurrency", "c", cfg.Concurrency, "number of concurrent goroutines")
	scanCmd.Flags().IntVar(&cfg.QPS, "qps", cfg.QPS, "max requests per second (0=unlimited)")
	scanCmd.Flags().BoolVar(&cfg.Resume, "resume", cfg.Resume, "resume from previous scan checkpoint")
	scanCmd.Flags().StringVar(&cfg.StateFile, "state-file", cfg.StateFile, "scan state file path")
	scanCmd.Flags().StringVar(&cfg.RulesFile, "rules", cfg.RulesFile, "custom rules YAML file")
	scanCmd.Flags().StringVarP(&cfg.Output, "output", "o", cfg.Output, "output format: console, json")
	scanCmd.Flags().StringVar(&cfg.OutputFile, "output-file", cfg.OutputFile, "output file path (default: stdout)")
	scanCmd.Flags().StringVar(&cfg.MaxFileSize, "max-file-size", cfg.MaxFileSize, "max file size to scan")
	scanCmd.Flags().DurationVarP(&cfg.Timeout, "timeout", "t", cfg.Timeout, "HTTP request timeout")
	scanCmd.Flags().IntVar(&cfg.Retries, "retries", cfg.Retries, "download retry count")
	scanCmd.Flags().StringSliceVar(&cfg.Exclude, "exclude", cfg.Exclude, "artifact patterns to exclude")
	scanCmd.Flags().IntVar(&cfg.CheckpointInterval, "checkpoint-interval", cfg.CheckpointInterval, "save state every N artifacts (0=every artifact)")
	scanCmd.Flags().StringVar(&cfg.RulesLevel, "rules-level", cfg.RulesLevel, "rule set: core (6 rules), extended (32 rules), all (38 rules)")
	scanCmd.Flags().BoolVar(&cfg.RulesMerge, "rules-merge", cfg.RulesMerge, "merge custom --rules onto built-in rules by ID (default: custom rules override built-in entirely)")
	scanCmd.Flags().IntVar(&cfg.DownloadConcurrency, "download-concurrency", cfg.DownloadConcurrency, "download goroutines (0=same as concurrency)")
	scanCmd.Flags().IntVar(&cfg.ScanConcurrency, "scan-concurrency", cfg.ScanConcurrency, "scan goroutines for CPU-bound extract+detect (0=same as concurrency)")
	scanCmd.Flags().BoolVar(&cfg.IncludeSources, "include-sources", cfg.IncludeSources, "scan -sources.jar (contains .java source, main source of real leaks on Maven Central)")
	scanCmd.Flags().BoolVar(&cfg.SkipPom, "skip-pom", cfg.SkipPom, "skip .pom/.xml files (faster, may miss metadata leaks)")
	scanCmd.Flags().IntVar(&cfg.BrowserConcurrency, "browser-concurrency", cfg.BrowserConcurrency, "discovery connection pool cap (0=auto max(concurrency,32))")
	scanCmd.Flags().IntVar(&cfg.DiskBudgetMB, "disk-budget", cfg.DiskBudgetMB, "max MB for temp files during scan (0=unlimited)")
	scanCmd.Flags().BoolVar(&cfg.RetryFailed, "retry-failed", cfg.RetryFailed, "retry previously failed artifacts on resume")
	scanCmd.Flags().BoolVar(&cfg.Rediscover, "rediscover", cfg.Rediscover, "force re-discovery of artifacts (ignore cached discovery)")
	scanCmd.Flags().StringVar(&cfg.AuthUsername, "auth-username", cfg.AuthUsername, "private repository username (HTTP Basic Auth)")
	scanCmd.Flags().StringVar(&cfg.AuthPassword, "auth-password", cfg.AuthPassword, "private repository password (HTTP Basic Auth)")
	scanCmd.Flags().StringVar(&cfg.AuthToken, "auth-token", cfg.AuthToken, "private repository bearer token")
	scanCmd.Flags().DurationVar(&cfg.ScanInterval, "scan-interval", cfg.ScanInterval, "schedule this scan as a recurring task with this interval (0=one-shot)")
	scanCmd.Flags().StringVar(&cfg.TaskID, "task-id", cfg.TaskID, "associate this run with a persisted task ID (for interval scheduling)")
}

// scanRunner is the subset of *scanner.Scanner that runScan uses (OnResult +
// Run). Indirecting through this interface lets tests inject a scanner whose
// Run returns an error, to cover runScan's "scan failed" branches (which the
// real Scanner.Run never returns — it is fault-tolerant and always returns
// nil error).
type scanRunner interface {
	OnResult(fn scanner.ProgressCallback)
	Run(ctx context.Context) (*scanner.Summary, error)
}

// newScannerFn builds the real scanner. Indirected as a package-level variable
// so tests can swap in a failing runner.
var newScannerFn = func(c *config.Config, opts ...scanner.ScannerOption) scanRunner {
	return scanner.NewScanner(c, opts...)
}

func runScan(cmd *cobra.Command, args []string) error {
	// Setup graceful shutdown with state flush guarantee
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Initialize workspace and SQLite store
	ws, err := storage.NewWorkspace()
	if err != nil {
		return fmt.Errorf("init workspace: %w", err)
	}
	ws.EnforceCacheLimit()

	store, err := openStoreFn(ws.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	// If this run is being scheduled as a task (--scan-interval > 0 or --task-id
	// set), persist it to the SQLite tasks table so it can be resumed, listed,
	// paused, and run on interval via `task run`. The full config snapshot is
	// stored so the task can be re-run without re-supplying flags.
	taskEnabled := cfg.TaskID != "" || cfg.ScanInterval > 0
	if taskEnabled {
		// Auto-generate a task ID if none was supplied.
		if cfg.TaskID == "" {
			cfg.TaskID = fmt.Sprintf("task-%s", time.Now().Format("20060102-150405"))
		}
		// Give the task a stable state file under the workspace if the user
		// did not specify one explicitly, so resume across runs is consistent.
		if cfg.StateFile == "" || cfg.StateFile == config.DefaultConfig().StateFile {
			cfg.StateFile = filepath.Join(ws.BaseDir, "states", cfg.TaskID+".json")
			os.MkdirAll(filepath.Dir(cfg.StateFile), 0755)
		}
		// Persist the task record (full config snapshot). If the task already
		// exists, preserve its run statistics (ScanCount, LastRunAt, NextRunAt)
		// so re-running with the same --task-id doesn't reset the history.
		cfgJSON, _ := json.Marshal(cfg)
		taskRec := &storage.TaskRecord{
			TaskID:          cfg.TaskID,
			RepoURL:         cfg.RepoURL,
			GroupFilter:     cfg.GroupFilter,
			Config:          cfgJSON,
			ScanIntervalSec: int64(cfg.ScanInterval / time.Second),
			Status:          storage.TaskActive,
			StateFile:       cfg.StateFile,
		}
		if existing, err := store.GetTask(cfg.TaskID); err == nil && existing != nil {
			taskRec.ScanCount = existing.ScanCount
			taskRec.LastRunAt = existing.LastRunAt
			taskRec.NextRunAt = existing.NextRunAt
			taskRec.LastRunStatus = existing.LastRunStatus
			taskRec.CreatedAt = existing.CreatedAt
			// Preserve status unless it was completed/error (a new run reactivates).
			if existing.Status == storage.TaskCompleted || existing.Status == storage.TaskError {
				taskRec.Status = storage.TaskActive
			} else {
				taskRec.Status = existing.Status
			}
		}
		if err := store.SaveTask(taskRec); err != nil {
			return fmt.Errorf("save task: %w", err)
		}
		log.Printf("Task %s registered (interval=%s, state=%s)", cfg.TaskID, storage.FormatInterval(taskRec.ScanIntervalSec), cfg.StateFile)
	}

	// Load detection rules. With --rules-merge, custom rules are layered onto
	// the built-in set by ID; otherwise (default) custom rules replace it.
	rules, err := detector.LoadRulesWithLevel(cfg.RulesFile, cfg.RulesLevel, cfg.RulesMerge)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	det, err := detector.NewDetector(rules)
	if err != nil {
		return fmt.Errorf("init detector: %w", err)
	}

	// Setup state manager (before scanner creation so we can pass it)
	scanState, err := state.LoadScanState(cfg.StateFile)
	if err != nil && !errors.Is(err, state.ErrStateNotFound) {
		log.Printf("Warning: could not load state file: %v", err)
	}
	if scanState != nil && !cfg.Resume {
		log.Printf("Found existing state file (status: %s). Use --resume to continue, or it will be overwritten.", scanState.GetStatus())
		scanState = nil
	}

	if scanState != nil && cfg.Resume {
		// Validate config compatibility
		configSnap := state.ConfigSnapshot{
			RepoURL:     cfg.RepoURL,
			GroupFilter: cfg.GroupFilter,
			RulesLevel:  cfg.RulesLevel,
			RulesFile:   cfg.RulesFile,
			RulesMerge:  cfg.RulesMerge,
			MaxFileSize: cfg.MaxFileSize,
		}
		if err := scanState.ValidateConfig(configSnap); err != nil {
			return fmt.Errorf("config mismatch on resume: %w", err)
		}
		scanState.SetCheckpointInterval(cfg.CheckpointInterval)

		// Log resume info
		d, sc, f := scanState.GetProgressStats()
		log.Printf("Resuming scan %s (status: %s, discovered: %d, scanned: %d, failed: %d, in-flight: %d)",
			scanState.ScanID, scanState.GetStatus(), d, sc, f, scanState.InFlightCount())

		if scanState.GetStatus() == state.ScanCompleted {
			log.Printf("Previous scan was completed. Use --rediscover to start a new scan.")
		}
	}

	// Create components — pass state to scanner for resume filtering.
	// Auth config enables private repository access.
	auth := repo.AuthConfig{
		Username: cfg.AuthUsername,
		Password: cfg.AuthPassword,
		Token:    cfg.AuthToken,
	}
	browser := repo.NewBrowserWithAuth(cfg.Timeout, cfg.GroupFilter, auth)
	// Discovery QPS throttle: share the same QPS budget as downloads so the
	// whole scan is polite to the repo. qps=0 → no limiter (unlimited).
	if cfg.QPS > 0 {
		browser = browser.WithLimiter(rate.NewLimiter(rate.Limit(cfg.QPS), cfg.QPS))
	}
	// Discovery connection pool: auto = max(concurrency, 32), capped at 128.
	maxConns := cfg.BrowserConcurrency
	if maxConns <= 0 {
		maxConns = cfg.Concurrency
		if maxConns < 32 {
			maxConns = 32
		}
	}
	if maxConns > 128 {
		maxConns = 128
	}
	browser = browser.WithMaxConnsPerHost(cfg.Timeout, maxConns)
	maxBytes, _ := cfg.ParseMaxFileSize()
	dl := repo.NewDownloaderWithAuth(cfg.Timeout, cfg.Retries, cfg.QPS, ws.CacheDir, maxBytes, auth)

	// Create disk watcher for budget-based download throttling
	var diskWatcher *scanner.DiskWatcher
	if cfg.DiskBudgetMB > 0 {
		diskWatcher = scanner.NewDiskWatcher(int64(cfg.DiskBudgetMB) * 1024 * 1024)
	}

	// Build scanner options
	scannerOpts := []scanner.ScannerOption{
		scanner.WithBrowser(browser),
		scanner.WithDownloader(dl),
		scanner.WithDetector(det),
		scanner.WithDiskWatcher(diskWatcher),
		scanner.WithCacheCleaner(ws),
		// Streaming discovery: walk the tree with a cursor, checkpointing
		// progress so discovery itself is resumable with O(depth) state.
		scanner.WithPageFetcher(browser),
		scanner.WithStreamingDiscovery(),
	}

	if scanState != nil {
		scannerOpts = append(scannerOpts,
			scanner.WithState(scanState),
			scanner.WithDiscoveryCacher(scanState),
			scanner.WithFailedRetryer(scanState),
			scanner.WithCursorSaver(scanState),
		)
		if cfg.RetryFailed {
			scannerOpts = append(scannerOpts, scanner.WithRetryFailed(cfg.Retries))
		}
		if cfg.Rediscover {
			scannerOpts = append(scannerOpts, scanner.WithRediscover())
		}
	}

	scan := newScannerFn(cfg, scannerOpts...)

	// Setup reporting
	scanID := fmt.Sprintf("scan-%s", time.Now().Format("20060102-150405"))
	if scanState != nil {
		scanID = scanState.ScanID
	}
	rpt := report.NewReport(scanID, cfg.RepoURL, cfg.GroupFilter, cfg.Concurrency)
	console := report.NewConsoleReporter()

	// Initialize state for new scan (must be before OnResult callback)
	if scanState == nil {
		configSnap := state.ConfigSnapshot{
			RepoURL:     cfg.RepoURL,
			GroupFilter: cfg.GroupFilter,
			RulesLevel:  cfg.RulesLevel,
			RulesFile:   cfg.RulesFile,
			RulesMerge:  cfg.RulesMerge,
			MaxFileSize: cfg.MaxFileSize,
		}
		scanState = state.NewScanStateWithConfig(scanID, cfg.RepoURL, cfg.GroupFilter, cfg.StateFile, cfg.CheckpointInterval, configSnap)
		scanState.SetMaxRetries(cfg.Retries)

		// Re-apply state to scanner since we just created it
		scan = newScannerFn(cfg, append(scannerOpts,
			scanner.WithState(scanState),
			scanner.WithDiscoveryCacher(scanState),
			scanner.WithFailedRetryer(scanState),
			scanner.WithCursorSaver(scanState),
		)...)
	}

	// Register progress callback
	scan.OnResult(func(result scanner.ArtifactResult) {
		if cfg.Verbose {
			coord := result.Artifact.String()
			if result.Status == scanner.StatusFailed {
				log.Printf("FAIL %s: %v", coord, result.Error)
			} else if len(result.Findings) > 0 {
				log.Printf("FOUND %d issues in %s", len(result.Findings), coord)
			}
		}

		for _, f := range result.Findings {
			fd := report.FindingDetail{
				Artifact:    result.Artifact.String(),
				File:        f.FilePath,
				RuleID:      f.RuleID,
				RuleName:    f.RuleName,
				Severity:    string(f.Severity),
				LineNumber:  f.LineNumber,
				LineContent: f.LineContent,
				Match:       f.Match,
				Description: f.Description,
			}
			rpt.Findings = append(rpt.Findings, fd)
			console.PrintFinding(fd)
		}

		// Persist state to JSON checkpoint
		if scanState != nil {
			artifactPath := result.Artifact.Path()
			if result.Status == scanner.StatusComplete {
				scanState.MarkCompleted(artifactPath)
			} else {
				scanState.MarkFailed(artifactPath, result.Error.Error(), cfg.Retries)
			}
		}

		// Write to SQLite
		dbStatus := storage.DBStatusComplete
		errMsg := ""
		if result.Status == scanner.StatusFailed {
			dbStatus = storage.DBStatusFailed
			errMsg = result.Error.Error()
		}
		rec := &storage.GAVRecord{
			GroupID:    result.Artifact.GroupID,
			ArtifactID: result.Artifact.ArtifactID,
			Version:    result.Artifact.Version,
			RepoURL:    cfg.RepoURL,
			Status:     dbStatus,
			Findings:   len(result.Findings),
			ScanTime:   time.Now(),
		}
		if errMsg != "" {
			rec.Error = errMsg
		}
		store.UpsertRecord(rec)

		// Insert individual findings into SQLite
		if got, _ := store.GetRecord(rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL); got != nil {
			for _, f := range result.Findings {
				store.InsertFinding(&storage.FindingRecord{
					RecordID:    got.ID,
					RuleID:      f.RuleID,
					RuleName:    f.RuleName,
					Severity:    string(f.Severity),
					FilePath:    f.FilePath,
					LineNumber:  f.LineNumber,
					LineContent: f.LineContent,
					Match:       f.Match,
				})
			}
		}
	})

	// Signal handler: cancel context and ensure state is flushed
	go func() {
		sig := <-sigCh
		log.Printf("\nReceived %s, flushing state and shutting down...", sig)
		cancel()
	}()

	log.Printf("Starting scan: %s (concurrency=%d, rules=%s)", cfg.RepoURL, cfg.Concurrency, cfg.RulesLevel)
	summary, err := scan.Run(ctx)

	// Always flush state and set appropriate status after scan completes
	if scanState != nil {
		if ctx.Err() != nil {
			scanState.MarkInterrupted()
		} else if err == nil {
			scanState.MarkCompletedStatus()
		} else {
			scanState.MarkInterrupted()
		}
		if flushErr := scanState.Flush(); flushErr != nil {
			log.Printf("Warning: failed to flush state: %v", flushErr)
		}
	}

	// Record the task run result and schedule the next run (if applicable).
	// This is what makes --scan-interval actually recurring: each completed run
	// bumps next_run_at by the interval, and `task run` picks up due tasks.
	if taskEnabled {
		runStatus := "ok"
		if err != nil {
			runStatus = "error: " + err.Error()
		} else if ctx.Err() != nil {
			runStatus = "interrupted"
		} else if summary != nil {
			runStatus = fmt.Sprintf("scanned=%d, findings=%d", summary.TotalScanned, summary.TotalFindings)
		}
		if updErr := store.UpdateTaskRun(cfg.TaskID, runStatus, time.Now()); updErr != nil {
			log.Printf("Warning: failed to update task run: %v", updErr)
		}
	}

	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Finalize report
	rpt.Summary = summary
	rpt.EndTime = time.Now().Format(time.RFC3339)

	console.PrintSummary(summary)

	if cfg.Output == "json" {
		jr := report.NewJSONReporter(cfg.OutputFile, rpt)
		if err := jr.Write(); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}

	return nil
}
