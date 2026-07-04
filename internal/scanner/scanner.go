// Package scanner orchestrates the full scan pipeline: discover, download, detect, and report.
package scanner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
	"github.com/scagogogo/mvn-repo-scanner/internal/state"
)

// ArtifactBrowser discovers artifacts in a Maven repository.
type ArtifactBrowser interface {
	Discover(ctx context.Context, repoURL string) ([]repo.Artifact, error)
}

// ArtifactDownloader downloads artifact files from a repository.
type ArtifactDownloader interface {
	Download(ctx context.Context, artifact repo.Artifact) (*repo.DownloadResult, error)
	Cleanup(localPath string)
}

// ContentDetector scans content for sensitive information.
type ContentDetector interface {
	ScanFile(filePath string) ([]detector.Finding, error)
	ScanContent(reader io.Reader, filePath string) ([]detector.Finding, error)
}

// StateTracker tracks scan progress for resume support.
type StateTracker interface {
	IsCompleted(path string) bool
	IsInFlight(path string) bool
	MarkCompleted(path string) error
	MarkFailed(path, errMsg string, retries int) error
	MarkInFlight(path string)
	// GetInFlightPaths returns the paths of artifacts that have been yielded
	// to the scan pipeline but not yet completed/failed. The cursor rollback
	// logic uses this to avoid persisting a cursor that would skip them.
	GetInFlightPaths() []string
	// MarkDirFailed records a directory whose listing could not be fetched
	// during discovery, so resume can re-visit it instead of permanently
	// losing the subtree. Called by the cursor walker's onDirFailed callback.
	MarkDirFailed(dirPath string)
	// GetFailedDirs returns directories recorded as fetch-failed, to be
	// re-visited before the main discovery walk resumes.
	GetFailedDirs() []string
	// ClearDirFailed removes a directory from the failed set after a re-visit
	// during resume succeeds in fetching its listing.
	ClearDirFailed(dirPath string)
}

// DiscoveryCacher caches and retrieves discovery results for resume.
type DiscoveryCacher interface {
	HasDiscoveryCache() bool
	GetDiscoveredArtifacts() []string
	SetDiscoveredArtifacts(paths []string)
	ClearDiscoveryCache()
}

// FailedRetryer provides access to retryable failed artifacts.
type FailedRetryer interface {
	GetRetryableFailures(maxRetries int) []state.FailedEntry
	ClearFailedEntry(path string)
}

// CursorSaver persists the discovery traversal cursor for resume.
// Implementations (typically ScanState) save O(tree depth) frames.
type CursorSaver interface {
	SetDiscoveryCursor(c []state.CursorFrameJSON)
	GetDiscoveryCursor() []state.CursorFrameJSON
	HasDiscoveryCursor() bool
	ClearDiscoveryCursor()
}

// PageFetcherProvider supplies a PageFetcher for cursor-based traversal.
// Typically the Browser itself implements repo.PageFetcher.
type PageFetcherProvider interface {
	repo.PageFetcher
}

// CacheCleaner enforces cache size limits during scanning.
type CacheCleaner interface {
	EnforceCacheLimit() error
}

// ProgressCallback is called when an artifact scan completes.
type ProgressCallback func(result ArtifactResult)

// ScannerOption configures a Scanner.
type ScannerOption func(*Scanner)

// WithBrowser sets the artifact browser.
func WithBrowser(b ArtifactBrowser) ScannerOption {
	return func(s *Scanner) { s.browser = b }
}

// WithDownloader sets the artifact downloader.
func WithDownloader(d ArtifactDownloader) ScannerOption {
	return func(s *Scanner) { s.downloader = d }
}

// WithDetector sets the content detector.
func WithDetector(d ContentDetector) ScannerOption {
	return func(s *Scanner) { s.detector = d }
}

// WithState sets the state tracker for resume support.
func WithState(st StateTracker) ScannerOption {
	return func(s *Scanner) { s.state = st }
}

// WithDiscoveryCacher sets the discovery cacher for resume support.
func WithDiscoveryCacher(dc DiscoveryCacher) ScannerOption {
	return func(s *Scanner) { s.discoveryCacher = dc }
}

// WithFailedRetryer sets the failed artifact retryer.
func WithFailedRetryer(fr FailedRetryer) ScannerOption {
	return func(s *Scanner) { s.failedRetryer = fr }
}

// WithRetryFailed enables retrying previously failed artifacts.
func WithRetryFailed(maxRetries int) ScannerOption {
	return func(s *Scanner) { s.retryFailed = true; s.maxRetries = maxRetries }
}

// WithRediscover forces re-discovery even if cached results exist.
func WithRediscover() ScannerOption {
	return func(s *Scanner) { s.rediscover = true }
}

// WithCursorSaver sets the discovery cursor persister for resumable discovery.
func WithCursorSaver(cs CursorSaver) ScannerOption {
	return func(s *Scanner) { s.cursorSaver = cs }
}

// WithPageFetcher sets the fetcher used for cursor-based streaming discovery.
// When set together with a cursor saver, the scanner streams artifacts directly
// from the tree walk instead of buffering a full discovery list, and the
// traversal cursor is checkpointed periodically for resume.
func WithPageFetcher(pf repo.PageFetcher) ScannerOption {
	return func(s *Scanner) { s.pageFetcher = pf }
}

// WithStreamingDiscovery enables cursor-based streaming discovery, which
// yields artifacts as they are found and checkpoints the traversal cursor
// so the discovery phase itself is resumable with O(tree depth) state.
func WithStreamingDiscovery() ScannerOption {
	return func(s *Scanner) { s.useStreamingDiscovery = true }
}

// WithProgressCallback sets the progress callback.
func WithProgressCallback(cb ProgressCallback) ScannerOption {
	return func(s *Scanner) { s.onResult = cb }
}

// WithDiskWatcher sets the disk budget watcher for throttling downloads.
func WithDiskWatcher(dw *DiskWatcher) ScannerOption {
	return func(s *Scanner) { s.diskWatcher = dw }
}

// WithCacheCleaner sets the cache cleaner for periodic cache enforcement.
func WithCacheCleaner(cc CacheCleaner) ScannerOption {
	return func(s *Scanner) { s.cacheCleaner = cc }
}

// downloadJob wraps an artifact with its local file path and size after download.
type downloadJob struct {
	artifact  repo.Artifact
	localPath string
	fileSize  int64
}

// Scanner orchestrates the full scan pipeline using a four-stage goroutine model.
type Scanner struct {
	cfg             *config.Config
	browser         ArtifactBrowser
	downloader      ArtifactDownloader
	detector        ContentDetector
	state           StateTracker
	discoveryCacher DiscoveryCacher
	failedRetryer   FailedRetryer
	cursorSaver     CursorSaver
	pageFetcher     repo.PageFetcher
	onResult        ProgressCallback
	diskWatcher     *DiskWatcher
	cacheCleaner    CacheCleaner
	retryFailed     bool
	maxRetries      int
	rediscover      bool
	useStreamingDiscovery bool
}

// NewScanner creates a new Scanner with the given configuration and options.
func NewScanner(cfg *config.Config, opts ...ScannerOption) *Scanner {
	s := &Scanner{cfg: cfg}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// New creates a new Scanner. Deprecated: use NewScanner with functional options.
func New(cfg *config.Config, browser *repo.Browser, dl *repo.Downloader, det *detector.Detector, scanState *state.ScanState) *Scanner {
	opts := []ScannerOption{
		WithBrowser(browser),
		WithDownloader(dl),
		WithDetector(det),
	}
	if scanState != nil {
		opts = append(opts, WithState(scanState))
		opts = append(opts, WithDiscoveryCacher(scanState))
		opts = append(opts, WithFailedRetryer(scanState))
		opts = append(opts, WithCursorSaver(scanState))
	}
	// Browser implements PageFetcher — enable streaming discovery by default.
	if browser != nil {
		opts = append(opts, WithPageFetcher(browser), WithStreamingDiscovery())
	}
	return NewScanner(cfg, opts...)
}

// OnResult registers a callback for scan results.
func (s *Scanner) OnResult(cb ProgressCallback) {
	s.onResult = cb
}

// Run executes the full scan pipeline with four stages connected by channels:
//
//	Stage 1: Discovery goroutine — stream artifacts into artifactCh
//	Stage 2: Download pool (N download workers) — download to temp files, push to downloadCh
//	Stage 3: Scan pool (M scan workers) — extract + scan, push to resultCh
//	Stage 4: Collector — read results, update summary, call onResult callback
func (s *Scanner) Run(ctx context.Context) (*Summary, error) {
	if s.cfg.Verbose {
		log.Printf("Discovering artifacts in %s ...", s.cfg.RepoURL)
	}

	dlConcurrency := s.cfg.Concurrency
	if s.cfg.DownloadConcurrency > 0 {
		dlConcurrency = s.cfg.DownloadConcurrency
	}
	// Scan (extract+detect) is CPU-bound; download is IO-bound. They peak at
	// different worker counts, so scan concurrency is tunable independently.
	// 0 means fall back to the general concurrency for backward compat.
	scanConcurrency := s.cfg.Concurrency
	if s.cfg.ScanConcurrency > 0 {
		scanConcurrency = s.cfg.ScanConcurrency
	}

	// Channels between stages — buffered for backpressure. The producer side
	// of each channel gets 2× the consumer count so a fast stage doesn't stall
	// waiting on a slow one within a batch.
	artifactCh := make(chan repo.Artifact, dlConcurrency*2)
	downloadCh := make(chan downloadJob, scanConcurrency*2)
	// resultCh is read by a single collector; cap it at scanConcurrency so a
	// briefly-slow collector (e.g. SQLite upsert) doesn't block scan workers
	// immediately, while bounding memory under heavy findings.
	resultCh := make(chan ArtifactResult, scanConcurrency)

	summary := NewSummary()

	// Stage 1: Discovery goroutine
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(artifactCh)

		if s.useStreamingDiscovery && s.pageFetcher != nil {
			s.discoverStreaming(ctx, artifactCh, summary)
			return
		}
		s.discoverBatched(ctx, artifactCh, summary)
	}()

	// Stage 2: Download pool
	var dlWg sync.WaitGroup
	for i := 0; i < dlConcurrency; i++ {
		dlWg.Add(1)
		go func() {
			defer dlWg.Done()
			for artifact := range artifactCh {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Mark as in-flight before downloading
				artifactPath := artifact.Path()
				if s.state != nil {
					s.state.MarkInFlight(artifactPath)
				}

				// Acquire disk budget before downloading
				var reservedSize int64
				if s.diskWatcher != nil {
					rs, err := s.diskWatcher.Acquire(ctx, 0)
					if err != nil {
						select {
						case resultCh <- ArtifactResult{Artifact: artifact, Status: StatusFailed, Error: fmt.Errorf("disk budget: %w", err)}:
						case <-ctx.Done():
						}
						continue
					}
					reservedSize = rs
				}

				dlResult, err := s.downloader.Download(ctx, artifact)
				if err != nil {
					// Download failed — release the disk budget reservation
					if s.diskWatcher != nil {
						s.diskWatcher.Release(reservedSize)
					}
					select {
					case resultCh <- ArtifactResult{Artifact: artifact, Status: StatusFailed, Error: fmt.Errorf("download: %w", err)}:
					case <-ctx.Done():
						return
					}
					continue
				}

				// Get actual file size for disk budget tracking
				var fileSize int64
				if info, err := os.Stat(dlResult.LocalPath); err == nil {
					fileSize = info.Size()
				}

				// Update disk watcher with actual file size
				if s.diskWatcher != nil {
					s.diskWatcher.Update(reservedSize, fileSize) // replace reservation with actual size
				}

				select {
				case downloadCh <- downloadJob{artifact: artifact, localPath: dlResult.LocalPath, fileSize: fileSize}:
				case <-ctx.Done():
					s.downloader.Cleanup(dlResult.LocalPath)
					if s.diskWatcher != nil {
						s.diskWatcher.Release(fileSize)
					}
					return
				}
			}
		}()
	}
	go func() {
		dlWg.Wait()
		close(downloadCh)
	}()

	// Stage 3: Scan pool — uses processJob for panic-safe cleanup
	var scanWg sync.WaitGroup
	for i := 0; i < scanConcurrency; i++ {
		scanWg.Add(1)
		go func() {
			defer scanWg.Done()
			for job := range downloadCh {
				s.processJob(ctx, job, resultCh)
			}
		}()
	}
	go func() {
		scanWg.Wait()
		close(resultCh)
	}()

	// Stage 4: Collector
	cleanedCount := 0
	for result := range resultCh {
		if result.Status == StatusComplete {
			summary.TotalScanned++
			for _, f := range result.Findings {
				summary.AddFinding(f)
			}
		} else {
			summary.TotalFailed++
		}
		if s.onResult != nil {
			s.onResult(result)
		}

		// Periodic cache cleanup
		cleanedCount++
		if s.cacheCleaner != nil && cleanedCount%50 == 0 {
			s.cacheCleaner.EnforceCacheLimit()
		}
	}

	<-producerDone
	return summary, nil
}

// discoverBatched is the legacy discovery mode: collect ALL artifacts into
// memory, then stream them. Supports the discovered-artifacts cache and
// failed-artifact retry. Used when streaming discovery is not enabled.
func (s *Scanner) discoverBatched(ctx context.Context, artifactCh chan<- repo.Artifact, summary *Summary) {
	artifacts, err := s.discoverArtifacts(ctx)
	if err != nil {
		log.Printf("Discovery failed: %v", err)
		return
	}
	summary.TotalDiscovered = len(artifacts)

	skipped := 0
	for _, a := range artifacts {
		artifactPath := a.Path()
		if s.state != nil && s.state.IsCompleted(artifactPath) {
			skipped++
			continue
		}
		select {
		case <-ctx.Done():
			return
		case artifactCh <- a:
		}
	}
	if s.cfg.Verbose {
		log.Printf("Discovered %d artifacts (skipped %d completed)", len(artifacts)-skipped, skipped)
	}
}

// discoverStreaming walks the repository tree with a cursor-based ordered DFS,
// yielding artifacts to artifactCh as they are found. The traversal cursor is
// checkpointed every checkpointInterval artifacts so the discovery phase itself
// is resumable with O(tree depth) state — no full discovered list is buffered.
//
// On resume, if a persisted cursor exists and --rediscover is not set, the
// walk continues from that cursor; otherwise it starts at the group-filter
// path (or root).
func (s *Scanner) discoverStreaming(ctx context.Context, artifactCh chan<- repo.Artifact, summary *Summary) {
	baseURL := s.cfg.RepoURL

	// Determine start cursor
	var startCursor repo.Cursor
	if s.cursorSaver != nil && !s.rediscover && s.cursorSaver.HasDiscoveryCursor() {
		// Resume from persisted cursor
		saved := s.cursorSaver.GetDiscoveryCursor()
		startCursor = make(repo.Cursor, len(saved))
		for i, f := range saved {
			startCursor[i] = repo.CursorFrame{DirPath: f.DirPath, NextIdx: f.NextIdx}
		}
	} else {
		// Fresh start: begin at the group-filter path if set, else root.
		if s.cfg.GroupFilter != "" {
			startPath := strings.ReplaceAll(s.cfg.GroupFilter, ".", "/")
			startCursor = repo.Cursor{{DirPath: startPath, NextIdx: 0}}
		} else {
			startCursor = repo.RootCursor()
		}
		if s.cursorSaver != nil && s.rediscover {
			s.cursorSaver.ClearDiscoveryCursor()
		}
	}

	walker := repo.NewCursorWalker(s.pageFetcher, baseURL)
	// Record fetch-failed directories so resume can re-visit them instead of
	// permanently losing the subtree (the walk itself skips them on error).
	if s.state != nil {
		walker.SetOnDirFailed(s.state.MarkDirFailed)
	}

	// makeYield builds a yield closure for the discovery walk.
	//
	// skipInFlight distinguishes the two callers:
	//   - The pre-resume re-visit pass uses skipInFlight=false: it MUST re-yield
	//     in-flight artifacts (that is its whole purpose — they were yielded
	//     to the previous run's channel but never completed).
	//   - The main walk uses skipInFlight=true: anything still in-flight was
	//     already (re-)yielded by the re-visit pass, so the main walk must not
	//     yield it a second time. This is what prevents duplicates when the
	//     main cursor has not yet advanced past a re-visited artifact.
	//
	// Both closures mark newly-yielded artifacts in-flight before sending, so a
	// cancellation while an artifact sits in the channel buffer is never lost.
	makeYield := func(skipInFlight bool) func(repo.Artifact) {
		return func(a repo.Artifact) {
			summary.TotalDiscovered++

			if s.state != nil {
				if s.state.IsCompleted(a.Path()) {
					return
				}
				if skipInFlight && s.state.IsInFlight(a.Path()) {
					// Already (re-)yielded by the re-visit pass — skip.
					return
				}
				s.state.MarkInFlight(a.Path())
			}

			select {
			case <-ctx.Done():
				return
			case artifactCh <- a:
			}
		}
	}
	noStop := func() bool { return false }

	// Pre-resume re-visit pass: re-walk any directory that was left in-flight
	// or fetch-failed when the previous run was interrupted, BEFORE continuing
	// the main cursor. This replaces the old cursor-rollback strategy, which
	// rolled the main cursor back to the shallowest in-flight ancestor and
	// could collapse to the repository root when in-flight artifacts spanned
	// disjoint subtrees — re-fetching every directory page from the root.
	// Re-visiting each directory with a single-frame cursor costs O((in-flight
	// + failed) × depth) fetches instead of O(scanned dirs).
	if s.state != nil {
		s.revisitPendingDirs(ctx, walker, makeYield(false), noStop)
	}

	yielded := 0
	checkpointEvery := s.cfg.CheckpointInterval
	if checkpointEvery == 0 {
		checkpointEvery = 50
	}
	mainYield := makeYield(true)

	// We checkpoint the cursor mid-walk by briefly pausing: stop the walk,
	// persist the returned cursor, then resume from it. To avoid excessive
	// pause/resume overhead, only pause every checkpointEvery artifacts.
	var cur repo.Cursor = startCursor
	var walkErr error
	for {
		var batchYielded int
		cur, walkErr = walker.Walk(ctx, cur, func(a repo.Artifact) {
			mainYield(a)
			yielded++
			batchYielded++
		}, func() bool {
			// Stop after every checkpointEvery artifacts to checkpoint the cursor.
			return batchYielded >= checkpointEvery || ctx.Err() != nil
		})

		// Persist cursor after each batch (and on exit).
		s.saveCursorFrom(cur)

		if walkErr != nil {
			log.Printf("Streaming discovery ended: %v", walkErr)
			break
		}
		if len(cur) == 0 {
			// Walk complete.
			break
		}
		if ctx.Err() != nil {
			break
		}
		// Otherwise loop to resume from cur for the next batch.
	}

	if s.cfg.Verbose {
		log.Printf("Streaming discovery yielded %d artifacts (total discovered counter: %d)",
			yielded, summary.TotalDiscovered)
	}
}

// revisitPendingDirs re-walks directories that were in-flight or fetch-failed
// when the previous scan was interrupted, before the main discovery cursor
// resumes. Each pending directory is walked with a single-frame cursor; the
// yield closure skips already-completed artifacts and re-marks in-flight ones.
//
// - In-flight directories: an artifact was yielded to the pipeline but not yet
//   completed/failed at interruption. Re-walking re-yields it.
// - Failed directories: their listing could not be fetched last time. If the
//   fetch succeeds now, artifacts under them are yielded and the directory is
//   cleared; if it still fails, the onDirFailed callback re-records it for the
//   next resume (so it is never permanently lost).
func (s *Scanner) revisitPendingDirs(ctx context.Context, walker *repo.CursorWalker,
	yield func(repo.Artifact), shouldStop func() bool) {

	// Collect pending directories, deduped. In-flight paths are GAV directories
	// (group/artifact/version); failed dirs are listing directories. Both are
	// valid single-frame walk roots.
	pending := make(map[string]struct{})
	for _, p := range s.state.GetInFlightPaths() {
		pending[p] = struct{}{}
	}
	for _, d := range s.state.GetFailedDirs() {
		pending[d] = struct{}{}
	}
	if len(pending) == 0 {
		return
	}

	// Snapshot the failed-dirs set so we can tell which pending entries came
	// from it: only those get ClearDirFailed before re-walking. (In-flight
	// dirs are cleared implicitly by MarkCompleted/MarkFailed as their
	// artifacts finish processing.)
	wasFailedSet := make(map[string]bool)
	for _, d := range s.state.GetFailedDirs() {
		wasFailedSet[d] = true
	}

	if s.cfg.Verbose {
		log.Printf("Re-visiting %d pending directories before resuming discovery", len(pending))
	}

	for dir := range pending {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wasFailed := wasFailedSet[dir]

		// Clear a stale failed marker BEFORE walking. If the fetch succeeds
		// this time, onDirFailed will not fire and the marker stays cleared.
		// If the fetch fails again, onDirFailed re-marks it (idempotent) so
		// it survives to the next resume. This makes IsDirFailed after Walk a
		// reliable success/failure signal for previously-failed directories.
		if wasFailed {
			s.state.ClearDirFailed(dir)
		}

		// A single-frame cursor at the directory root, starting from index 0.
		// Walk fetches the listing; on success it yields artifacts (completed
		// siblings are skipped by the yield closure); on failure onDirFailed
		// re-records the directory for the next resume.
		startCur := repo.Cursor{{DirPath: dir, NextIdx: 0}}
		walker.Walk(ctx, startCur, yield, shouldStop)
	}
}

// saveCursorFrom persists a cursor to the CursorSaver.
//
// To guarantee no artifact is lost on resume, the cursor must not point past
// any artifact that has been yielded but not yet completed (in-flight). Such
// artifacts are rediscovered by re-walking their directory from index 0 —
// completed siblings are skipped via IsCompleted, so this is safe and cheap.
// We therefore roll the cursor back to the directory frame of the deepest
// in-flight artifact and reset that frame's index to 0.
func (s *Scanner) saveCursorFrom(cur repo.Cursor) {
	if s.cursorSaver == nil {
		return
	}
	cur = s.rollbackForInFlight(cur)
	if len(cur) == 0 {
		// Walk finished — clear any stale cursor.
		s.cursorSaver.ClearDiscoveryCursor()
		return
	}
	frames := make([]state.CursorFrameJSON, len(cur))
	for i, f := range cur {
		frames[i] = state.CursorFrameJSON{DirPath: f.DirPath, NextIdx: f.NextIdx}
	}
	s.cursorSaver.SetDiscoveryCursor(frames)
}

// rollbackForInFlight previously rolled the main cursor back to the shallowest
// in-flight ancestor so resume would re-yield in-flight artifacts. That could
// collapse to the repository root when in-flight artifacts spanned disjoint
// subtrees, re-fetching every directory page from the root on resume.
//
// In-flight coverage is now handled by revisitPendingDirs, which re-walks each
// in-flight directory with a single-frame cursor before the main walk resumes.
// The main cursor therefore no longer needs to roll back — it persists exactly
// where the main traversal paused. This function is kept as a no-op hook so
// saveCursorFrom's call site stays explicit and a future strategy can plug in
// here without touching the discovery loop.
func (s *Scanner) rollbackForInFlight(cur repo.Cursor) repo.Cursor {
	return cur
}

// discoverArtifacts handles the legacy batched discovery phase with cache support.
// If discovery cache exists and rediscover is not forced, returns cached results.
// Otherwise, performs fresh discovery and caches the results.
func (s *Scanner) discoverArtifacts(ctx context.Context) ([]repo.Artifact, error) {
	// Check for cached discovery results
	if s.discoveryCacher != nil && !s.rediscover && s.discoveryCacher.HasDiscoveryCache() {
		if s.cfg.Verbose {
			log.Printf("Using cached discovery results (use --rediscover to force re-discovery)")
		}
		cachedPaths := s.discoveryCacher.GetDiscoveredArtifacts()
		return s.pathsToArtifacts(cachedPaths), nil
	}

	// Clear cache if forcing rediscover
	if s.discoveryCacher != nil && s.rediscover {
		s.discoveryCacher.ClearDiscoveryCache()
	}

	// Perform fresh discovery
	artifacts, err := s.browser.Discover(ctx, s.cfg.RepoURL)
	if err != nil {
		return nil, err
	}

	// Cache discovery results
	if s.discoveryCacher != nil {
		paths := make([]string, len(artifacts))
		for i, a := range artifacts {
			paths[i] = a.Path()
		}
		s.discoveryCacher.SetDiscoveredArtifacts(paths)
	}

	// If retry-failed is enabled, add retryable failed artifacts back
	if s.failedRetryer != nil && s.retryFailed {
		s.addRetryableFailed(artifacts)
	}

	return artifacts, nil
}

// addRetryableFailed adds previously failed artifacts that can be retried
// back into the artifact list, clearing their failed status so they will
// be re-scanned.
func (s *Scanner) addRetryableFailed(artifacts []repo.Artifact) []repo.Artifact {
	retryable := s.failedRetryer.GetRetryableFailures(s.maxRetries)
	if len(retryable) == 0 {
		return artifacts
	}

	if s.cfg.Verbose {
		log.Printf("Retrying %d previously failed artifacts", len(retryable))
	}

	// Build a set of existing artifact paths for dedup
	existingPaths := make(map[string]bool)
	for _, a := range artifacts {
		existingPaths[a.Path()] = true
	}

	added := 0
	for _, entry := range retryable {
		if existingPaths[entry.Path] {
			continue // already in the list
		}

		// Parse path back to artifact components
		// Path format: group/artifact/version
		parts := strings.Split(entry.Path, "/")
		if len(parts) < 3 {
			continue
		}

		versionIdx := len(parts) - 1
		artifactIdx := versionIdx - 1
		groupParts := parts[:artifactIdx]
		groupID := strings.Join(groupParts, ".")
		artifactID := parts[artifactIdx]
		version := parts[versionIdx]

		artifact := repo.Artifact{
			GroupID:     groupID,
			ArtifactID:  artifactID,
			Version:     version,
			FileName:    "", // will be discovered during download
			DownloadURL: "", // will be constructed by downloader
		}

		artifacts = append(artifacts, artifact)
		s.failedRetryer.ClearFailedEntry(entry.Path)
		added++
	}

	if s.cfg.Verbose && added > 0 {
		log.Printf("Added %d retryable artifacts to scan queue", added)
	}

	return artifacts
}

// pathsToArtifacts converts cached paths back to Artifact objects.
// Note: DownloadURL and FileName may be empty — the downloader will construct them.
func (s *Scanner) pathsToArtifacts(paths []string) []repo.Artifact {
	artifacts := make([]repo.Artifact, 0, len(paths))
	for _, p := range paths {
		parts := strings.Split(p, "/")
		if len(parts) < 3 {
			continue
		}

		versionIdx := len(parts) - 1
		artifactIdx := versionIdx - 1
		groupParts := parts[:artifactIdx]
		groupID := strings.Join(groupParts, ".")
		artifactID := parts[artifactIdx]
		version := parts[versionIdx]

		artifacts = append(artifacts, repo.Artifact{
			GroupID:    groupID,
			ArtifactID: artifactID,
			Version:    version,
		})
	}
	return artifacts
}

// processJob scans a downloaded artifact and sends the result.
// Uses defer to ensure temp file cleanup and disk budget release even on panic.
func (s *Scanner) processJob(ctx context.Context, job downloadJob, resultCh chan<- ArtifactResult) {
	// Recover from panics in scan logic so the goroutine doesn't crash the whole program
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in scan of %s: %v", job.artifact.String(), r)
		}
	}()

	// Always cleanup temp file and release disk budget when done
	defer func() {
		s.downloader.Cleanup(job.localPath)
		if s.diskWatcher != nil {
			s.diskWatcher.Release(job.fileSize)
		}
	}()

	select {
	case <-ctx.Done():
		return
	default:
	}

	result := s.scanDownloadedArtifact(ctx, job)

	select {
	case resultCh <- result:
	case <-ctx.Done():
	}
}

// scanDownloadedArtifact scans an already-downloaded artifact file. Archives
// (.jar/.war/.ear/.zip/.tar/.tar.gz/.tgz) are extracted and scanned inside;
// other files (.pom/.xml/etc.) are scanned directly as text.
func (s *Scanner) scanDownloadedArtifact(ctx context.Context, job downloadJob) ArtifactResult {
	result := ArtifactResult{Artifact: job.artifact, Status: StatusScanning}

	var findings []detector.Finding
	var err error
	if isArchiveFile(job.artifact.FileName) {
		findings, err = s.scanArchiveFile(job.localPath, job.artifact.FileName)
	} else {
		findings, err = s.detector.ScanFile(job.localPath)
	}

	if err != nil {
		result.Status = StatusFailed
		result.Error = fmt.Errorf("scan: %w", err)
		return result
	}

	result.Findings = findings
	result.Status = StatusComplete
	return result
}

// isArchiveFile reports whether fileName is a recognized archive type that
// scanArchiveFile can extract. Kept here (rather than in archive.go) because
// the scan router needs it before the archive package path is chosen.
func isArchiveFile(fileName string) bool {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".jar"), strings.HasSuffix(lower, ".war"),
		strings.HasSuffix(lower, ".ear"), strings.HasSuffix(lower, ".zip"),
		strings.HasSuffix(lower, ".tar"), strings.HasSuffix(lower, ".tar.gz"),
		strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".gz"):
		return true
	}
	return false
}
