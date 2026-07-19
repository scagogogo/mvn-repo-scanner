// Package state manages persistent scan state for checkpoint and resume support.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// CurrentVersion is the current state file format version.
const CurrentVersion = 1

// ErrStateNotFound is returned by LoadScanState when the state file does not exist.
var ErrStateNotFound = errors.New("scan state file not found")

// ErrVersionMismatch is returned when the state file version is not supported.
var ErrVersionMismatch = errors.New("scan state version not supported")

// flushHook is a test-only injection point to simulate a crash mid-flush
// (e.g. power loss after writing .tmp but before rename). When non-nil it is
// invoked after the .tmp file is written (and fsynced) but before the .bak
// backup and rename. A panic from the hook mimics a process killed mid-write
// so tests can verify the main file is not left corrupt and .bak survives.
// Production code leaves this nil; it is not exported to keep the surface
// internal. Set via testfile in the same package.
var flushHook func(filePath string)

// ScanStatus represents the overall status of a scan session.
type ScanStatus string

const (
	ScanRunning     ScanStatus = "running"
	ScanCompleted   ScanStatus = "completed"   // scan ran to full completion
	ScanInterrupted ScanStatus = "interrupted" // aborted by signal/error
	ScanFinished    ScanStatus = "finished"    // stopped normally before completion (e.g. limit reached); safe to resume
)

// ConfigSnapshot captures critical config at scan creation time for resume validation.
type ConfigSnapshot struct {
	RepoURL     string `json:"repo_url"`
	GroupFilter string `json:"group_filter"`
	RulesLevel  string `json:"rules_level"`
	RulesFile   string `json:"rules_file,omitempty"`
	RulesMerge  bool   `json:"rules_merge,omitempty"`
	MaxFileSize string `json:"max_file_size"`
}

// FailedEntry records a failed artifact with its error.
type FailedEntry struct {
	Path         string `json:"path"`
	Error        string `json:"error"`
	Retries      int    `json:"retries"`
	LastFailedAt string `json:"last_failed_at,omitempty"`
}

// CursorFrameJSON is one level of the resumable discovery cursor, stored in
// ScanState so the discovery phase itself can be resumed. Only O(tree depth)
// frames are ever persisted (~7 for Maven Central).
type CursorFrameJSON struct {
	DirPath string `json:"dir_path"`
	NextIdx int    `json:"next_idx"`
}

// ScanState represents the persistent state of a scan session.
type ScanState struct {
	Version            int              `json:"version"`
	Status             ScanStatus       `json:"status"`
	ScanID             string           `json:"scan_id"`
	RepoURL            string           `json:"repo_url"`
	GroupFilter        string           `json:"group_filter,omitempty"`
	ConfigSnapshot     ConfigSnapshot   `json:"config_snapshot"`
	StartedAt          string           `json:"started_at"`
	LastUpdated        string           `json:"last_updated"`
	CompletedPaths     []string         `json:"completed_artifacts"`
	FailedEntries      []FailedEntry    `json:"failed_artifacts"`
	InFlightPaths      []string         `json:"in_flight_artifacts,omitempty"`
	DiscoveredArtifacts []string        `json:"discovered_artifacts,omitempty"`
	DiscoveryCursor    []CursorFrameJSON `json:"discovery_cursor,omitempty"`
	// FailedDirs records directories whose listing could not be fetched during
	// discovery (transient network/5xx). Unlike artifact-level failures, a
	// directory fetch failure silently skips the whole subtree because the
	// cursor advances past it. We persist these so resume can re-visit them
	// rather than permanently losing every artifact under them.
	FailedDirs  []string `json:"failed_dirs,omitempty"`
	MaxRetries         int              `json:"max_retries"`
	TotalDiscovered    int              `json:"total_discovered,omitempty"`
	TotalScanned       int              `json:"total_scanned,omitempty"`
	TotalFailed        int              `json:"total_failed,omitempty"`

	mu              sync.RWMutex
	filePath        string
	dirtyCount      int
	checkpointEvery int
	completedSet    map[string]bool
	inFlightSet     map[string]bool
	failedSet       map[string]bool
	failedDirSet    map[string]bool
}

// NewScanState creates a new scan state.
func NewScanState(scanID, repoURL, groupFilter, filePath string) *ScanState {
	return &ScanState{
		Version:        CurrentVersion,
		Status:         ScanRunning,
		ScanID:         scanID,
		RepoURL:        repoURL,
		GroupFilter:    groupFilter,
		ConfigSnapshot: ConfigSnapshot{RepoURL: repoURL, GroupFilter: groupFilter},
		StartedAt:      time.Now().Format(time.RFC3339),
		LastUpdated:    time.Now().Format(time.RFC3339),
		CompletedPaths: []string{},
		FailedEntries:  []FailedEntry{},
		InFlightPaths:  []string{},
		filePath:       filePath,
		checkpointEvery: 50,
		completedSet:   make(map[string]bool),
		inFlightSet:    make(map[string]bool),
		failedSet:      make(map[string]bool),
		failedDirSet:   make(map[string]bool),
	}
}

// NewScanStateWithCheckpoint creates a scan state with custom checkpoint interval.
func NewScanStateWithCheckpoint(scanID, repoURL, groupFilter, filePath string, checkpointEvery int) *ScanState {
	s := NewScanState(scanID, repoURL, groupFilter, filePath)
	s.checkpointEvery = checkpointEvery
	return s
}

// NewScanStateWithConfig creates a scan state with a config snapshot for resume validation.
func NewScanStateWithConfig(scanID, repoURL, groupFilter, filePath string, checkpointEvery int, cfg ConfigSnapshot) *ScanState {
	s := NewScanStateWithCheckpoint(scanID, repoURL, groupFilter, filePath, checkpointEvery)
	s.ConfigSnapshot = cfg
	s.MaxRetries = 0 // default: no auto-retry, use --retry-failed flag
	return s
}

// LoadScanState loads scan state from a file. Returns nil if file doesn't exist.
// If the primary file is corrupt (unparseable), it falls back to the .bak
// backup written by the previous successful flush, so a single bad write
// never loses all resume progress.
func LoadScanState(filePath string) (*ScanState, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	loaded, perr := parseAndFinalize(data, filePath)
	if perr == nil {
		return loaded, nil
	}

	// Primary file is corrupt — try the .bak backup before giving up.
	bak, bakErr := os.ReadFile(filePath + ".bak")
	if bakErr != nil {
		return nil, perr // no backup, surface the original parse error
	}
	bakLoaded, bakParseErr := parseAndFinalize(bak, filePath)
	if bakParseErr != nil {
		return nil, perr // backup also bad — surface the primary error
	}
	log.Printf("Warning: state file %s corrupt (%v), recovered from .bak backup", filePath, perr)
	return bakLoaded, nil
}

// parseAndFinalize unmarshals one state blob, runs version check + migration,
// and rebuilds the in-memory lookup sets. Shared by the primary and .bak
// recovery paths. Returns (nil, parse/version err) on failure so LoadScanState
// can decide whether to fall back to .bak.
func parseAndFinalize(data []byte, filePath string) (*ScanState, error) {
	var s ScanState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}

	// Version check: reject future versions
	if s.Version > CurrentVersion {
		return nil, fmt.Errorf("%w: state file version %d, supported up to %d", ErrVersionMismatch, s.Version, CurrentVersion)
	}

	// Migrate version 0 (old format without version field) to version 1
	if s.Version == 0 {
		s.Version = CurrentVersion
		if s.Status == "" {
			s.Status = ScanInterrupted // old state files without status are treated as interrupted
		}
	}

	s.filePath = filePath
	s.checkpointEvery = 50
	s.completedSet = make(map[string]bool)
	s.inFlightSet = make(map[string]bool)
	s.failedSet = make(map[string]bool)
	s.failedDirSet = make(map[string]bool)
	for _, p := range s.CompletedPaths {
		s.completedSet[p] = true
	}
	for _, p := range s.InFlightPaths {
		s.inFlightSet[p] = true
	}
	for _, e := range s.FailedEntries {
		s.failedSet[e.Path] = true
	}
	for _, d := range s.FailedDirs {
		s.failedDirSet[d] = true
	}
	return &s, nil
}

// IsCompleted checks if an artifact path has already been scanned (O(1) lookup).
func (s *ScanState) IsCompleted(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.completedSet[path]
}

// IsFailed checks if an artifact path has been recorded as failed.
func (s *ScanState) IsFailed(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failedSet[path]
}

// IsInFlight checks if an artifact is currently being processed.
func (s *ScanState) IsInFlight(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inFlightSet[path]
}

// MarkCompleted records an artifact as successfully scanned.
// Uses batch checkpoint: only saves to disk every checkpointEvery changes.
// Also removes the path from in-flight set if present.
func (s *ScanState) MarkCompleted(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.CompletedPaths = append(s.CompletedPaths, path)
	s.completedSet[path] = true
	s.TotalScanned++

	// Remove from in-flight set if present
	if s.inFlightSet[path] {
		delete(s.inFlightSet, path)
		s.InFlightPaths = removeFromStringSlice(s.InFlightPaths, path)
	}

	// Remove from failed set if present (re-scan succeeded)
	if s.failedSet[path] {
		delete(s.failedSet, path)
		s.FailedEntries = removeFromFailedEntries(s.FailedEntries, path)
		s.TotalFailed--
	}

	s.LastUpdated = time.Now().Format(time.RFC3339)
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		return s.flush()
	}
	return nil
}

// MarkFailed records an artifact as failed.
// Also removes the path from in-flight set if present.
func (s *ScanState) MarkFailed(path, errMsg string, retries int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := FailedEntry{
		Path:         path,
		Error:        errMsg,
		Retries:      retries,
		LastFailedAt: time.Now().Format(time.RFC3339),
	}
	s.FailedEntries = append(s.FailedEntries, entry)
	s.failedSet[path] = true
	s.TotalFailed++

	// Remove from in-flight set if present
	if s.inFlightSet[path] {
		delete(s.inFlightSet, path)
		s.InFlightPaths = removeFromStringSlice(s.InFlightPaths, path)
	}

	s.LastUpdated = time.Now().Format(time.RFC3339)
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		return s.flush()
	}
	return nil
}

// MarkInFlight records an artifact as currently being processed.
// This ensures that if the scan is interrupted, these artifacts will be
// retried on resume rather than silently skipped.
func (s *ScanState) MarkInFlight(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inFlightSet[path] {
		return // already marked
	}
	s.InFlightPaths = append(s.InFlightPaths, path)
	s.inFlightSet[path] = true
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// RemoveInFlight removes an artifact from the in-flight set.
// This is called when an artifact completes or fails (MarkCompleted/MarkFailed
// handle this automatically, but this method is available for manual cleanup).
func (s *ScanState) RemoveInFlight(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.inFlightSet[path] {
		return
	}
	delete(s.inFlightSet, path)
	s.InFlightPaths = removeFromStringSlice(s.InFlightPaths, path)
}

// MarkInterrupted sets the scan status to interrupted.
func (s *ScanState) MarkInterrupted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = ScanInterrupted
}

// MarkCompletedStatus sets the scan status to completed.
func (s *ScanState) MarkCompletedStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = ScanCompleted
}

// MarkFinishedStatus sets the scan status to finished — the scan was stopped
// normally before reaching full completion (e.g. a scan-count limit was hit),
// so it is safe to resume later. Distinct from Interrupted (signal/error) and
// Completed (ran to the end).
func (s *ScanState) MarkFinishedStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = ScanFinished
}

// GetStatus returns the current scan status.
func (s *ScanState) GetStatus() ScanStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// GetRetryableFailures returns failed entries that can be retried
// (i.e., retries < maxRetries). If maxRetries is 0, all failures are returned
// (when --retry-failed is used, the caller provides the effective max).
func (s *ScanState) GetRetryableFailures(maxRetries int) []FailedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var retryable []FailedEntry
	for _, e := range s.FailedEntries {
		if maxRetries <= 0 || e.Retries < maxRetries {
			retryable = append(retryable, e)
		}
	}
	return retryable
}

// ClearFailedEntry removes a failed entry by path (used when re-scanning a failed artifact).
func (s *ScanState) ClearFailedEntry(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.failedSet[path] {
		return
	}
	delete(s.failedSet, path)
	s.FailedEntries = removeFromFailedEntries(s.FailedEntries, path)
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// MarkDirFailed records a directory whose listing could not be fetched during
// discovery, so resume can re-visit it instead of permanently skipping the
// subtree. Idempotent. Uses the same batch-checkpoint flush as other mutations.
func (s *ScanState) MarkDirFailed(dirPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failedDirSet[dirPath] {
		return // already marked
	}
	s.FailedDirs = append(s.FailedDirs, dirPath)
	s.failedDirSet[dirPath] = true
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// ClearDirFailed removes a directory from the failed set, called after a
// re-visit during resume succeeds in fetching its listing.
func (s *ScanState) ClearDirFailed(dirPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.failedDirSet[dirPath] {
		return
	}
	delete(s.failedDirSet, dirPath)
	s.FailedDirs = removeFromStringSlice(s.FailedDirs, dirPath)
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// IsDirFailed checks if a directory is recorded as fetch-failed.
func (s *ScanState) IsDirFailed(dirPath string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failedDirSet[dirPath]
}

// GetFailedDirs returns a snapshot copy of the failed-directory paths.
func (s *ScanState) GetFailedDirs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.FailedDirs) == 0 {
		return nil
	}
	out := make([]string, len(s.FailedDirs))
	copy(out, s.FailedDirs)
	return out
}

// SetDiscoveredArtifacts caches the full discovery result for resume.
func (s *ScanState) SetDiscoveredArtifacts(paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.DiscoveredArtifacts = paths
	s.TotalDiscovered = len(paths)
	s.dirtyCount++

	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// GetDiscoveredArtifacts returns the cached discovery result.
// Returns nil if no cache exists.
func (s *ScanState) GetDiscoveredArtifacts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.DiscoveredArtifacts) == 0 {
		return nil
	}
	result := make([]string, len(s.DiscoveredArtifacts))
	copy(result, s.DiscoveredArtifacts)
	return result
}

// HasDiscoveryCache returns true if discovery results are cached.
func (s *ScanState) HasDiscoveryCache() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.DiscoveredArtifacts) > 0
}

// ClearDiscoveryCache removes the cached discovery results.
func (s *ScanState) ClearDiscoveryCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DiscoveredArtifacts = nil
}

// SetDiscoveryCursor persists the current discovery traversal cursor so the
// discovery phase can be resumed exactly where it paused. The cursor is
// O(tree depth) — far smaller than caching the full discovered list.
func (s *ScanState) SetDiscoveryCursor(c []CursorFrameJSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DiscoveryCursor = c
	s.dirtyCount++
	if s.checkpointEvery == 0 || s.dirtyCount >= s.checkpointEvery {
		_ = s.flush()
	}
}

// GetDiscoveryCursor returns the persisted discovery cursor, or nil if none.
func (s *ScanState) GetDiscoveryCursor() []CursorFrameJSON {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.DiscoveryCursor) == 0 {
		return nil
	}
	out := make([]CursorFrameJSON, len(s.DiscoveryCursor))
	copy(out, s.DiscoveryCursor)
	return out
}

// HasDiscoveryCursor returns true if a discovery cursor is persisted.
func (s *ScanState) HasDiscoveryCursor() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.DiscoveryCursor) > 0
}

// ClearDiscoveryCursor removes the discovery cursor (used after discovery
// completes, or when forcing rediscovery).
func (s *ScanState) ClearDiscoveryCursor() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DiscoveryCursor = nil
}

// SetCheckpointInterval updates the checkpoint flush interval.
func (s *ScanState) SetCheckpointInterval(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointEvery = n
}

// SetMaxRetries sets the maximum retry count for failed artifacts.
func (s *ScanState) SetMaxRetries(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MaxRetries = n
}

// Flush forces an immediate save of the current state to disk.
func (s *ScanState) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}

// CompletedCount returns the number of completed artifacts.
func (s *ScanState) CompletedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.CompletedPaths)
}

// FailedCount returns the number of failed artifacts.
func (s *ScanState) FailedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.FailedEntries)
}

// InFlightCount returns the number of in-flight artifacts.
func (s *ScanState) InFlightCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.InFlightPaths)
}

// GetInFlightPaths returns a copy of the in-flight artifact paths.
func (s *ScanState) GetInFlightPaths() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.InFlightPaths))
	copy(out, s.InFlightPaths)
	return out
}

// ValidateConfig checks that the provided config matches the stored config snapshot.
// Returns nil if compatible, or an error describing the mismatch.
func (s *ScanState) ValidateConfig(cfg ConfigSnapshot) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.ConfigSnapshot.RepoURL != "" && s.ConfigSnapshot.RepoURL != cfg.RepoURL {
		return fmt.Errorf("state file RepoURL (%s) does not match current RepoURL (%s)", s.ConfigSnapshot.RepoURL, cfg.RepoURL)
	}
	if s.ConfigSnapshot.GroupFilter != "" && s.ConfigSnapshot.GroupFilter != cfg.GroupFilter {
		return fmt.Errorf("state file GroupFilter (%s) does not match current GroupFilter (%s)", s.ConfigSnapshot.GroupFilter, cfg.GroupFilter)
	}
	// Rules config drift changes what gets flagged — refuse resume across a
	// different rules setup so findings stay comparable to the prior run.
	if s.ConfigSnapshot.RulesLevel != "" && s.ConfigSnapshot.RulesLevel != cfg.RulesLevel {
		return fmt.Errorf("state file RulesLevel (%s) does not match current RulesLevel (%s)", s.ConfigSnapshot.RulesLevel, cfg.RulesLevel)
	}
	if s.ConfigSnapshot.RulesFile != cfg.RulesFile {
		return fmt.Errorf("state file RulesFile (%q) does not match current RulesFile (%q)", s.ConfigSnapshot.RulesFile, cfg.RulesFile)
	}
	if s.ConfigSnapshot.RulesMerge != cfg.RulesMerge {
		return fmt.Errorf("state file RulesMerge (%v) does not match current RulesMerge (%v)", s.ConfigSnapshot.RulesMerge, cfg.RulesMerge)
	}
	// MaxFileSize drift changes which files get scanned — a previously-skipped
	// oversized artifact would now be scanned (or vice versa), making the
	// resumed result set incomparable. Refuse resume across a different cap.
	if s.ConfigSnapshot.MaxFileSize != "" && s.ConfigSnapshot.MaxFileSize != cfg.MaxFileSize {
		return fmt.Errorf("state file MaxFileSize (%q) does not match current MaxFileSize (%q)", s.ConfigSnapshot.MaxFileSize, cfg.MaxFileSize)
	}
	return nil
}

// GetProgressStats returns the persisted progress statistics.
func (s *ScanState) GetProgressStats() (discovered, scanned, failed int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalDiscovered, s.TotalScanned, s.TotalFailed
}

// GetResumeEstimate returns a coarse progress snapshot for resume logging:
// status, discovered (cached total), scanned, failed, and in-flight counts.
// Used by cmd to print a resume progress line without exposing internal slices.
func (s *ScanState) GetResumeEstimate() (status ScanStatus, discovered, scanned, failed, inFlight int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status, s.TotalDiscovered, s.TotalScanned, s.TotalFailed, len(s.InFlightPaths)
}

// flush writes the current state to disk atomically (caller must hold lock).
//
// Persistence hardening:
//   1. Write to a .tmp file, then fsync it so the bytes survive a crash
//      before the rename commits them. (Without fsync, a rename can land
//      while the data is still only in the kernel page cache — a power
//      loss yields a truncated/empty state file.)
//   2. Before overwriting, copy the previous good file to .bak so a corrupt
//      write or future parse failure can fall back to it in LoadScanState.
//      The previous file is validated before backup: if it is already corrupt
//      (e.g. a prior half-written rename survived as the main file), we skip
//      the backup rather than overwriting the last good .bak with garbage.
//   3. flushHook (test-only) fires after .tmp is durable but before .bak/rename,
//      so a panic there leaves .tmp on disk and the main file untouched —
//      modeling a kill mid-flush for crash-recovery tests.
func (s *ScanState) flush() error {
	s.LastUpdated = time.Now().Format(time.RFC3339)
	s.dirtyCount = 0

	// ScanState's exported fields are all JSON-serializable (strings, ints,
	// slices, structs of the same); unexported fields (mu, maps) are ignored by
	// encoding/json. MarshalIndent cannot fail in practice — the error is
	// intentionally ignored.
	data, _ := json.MarshalIndent(s, "", "  ")

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	// fsync the temp file so the committed bytes are durable on disk.
	if f, err := os.Open(tmpFile); err == nil {
		_ = f.Sync() // best-effort; some filesystems/OSes ignore fsync errors
		f.Close()
	}

	// Test-only crash injection: a panic here leaves .tmp on disk and the
	// main file + .bak untouched, modeling a process killed mid-flush.
	if flushHook != nil {
		flushHook(s.filePath)
	}

	// Back up the previous good state file (if any) before replacing it.
	// Validate it first: if the main file is already corrupt, do NOT copy it
	// over .bak (that would destroy the last good backup). Only well-formed
	// previous content is preserved as .bak.
	if prev, err := os.ReadFile(s.filePath); err == nil && len(prev) > 0 {
		if json.Valid(prev) {
			_ = os.WriteFile(s.filePath+".bak", prev, 0644)
		}
		// If prev is not valid JSON, .bak is left as-is (last good backup).
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Delete removes the state file.
func (s *ScanState) Delete() error {
	return os.Remove(s.filePath)
}

// removeFromStringSlice removes the first occurrence of val from slice.
func removeFromStringSlice(slice []string, val string) []string {
	for i, v := range slice {
		if v == val {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// removeFromFailedEntries removes the entry with the given path.
func removeFromFailedEntries(entries []FailedEntry, path string) []FailedEntry {
	for i, e := range entries {
		if e.Path == path {
			return append(entries[:i], entries[i+1:]...)
		}
	}
	return entries
}
