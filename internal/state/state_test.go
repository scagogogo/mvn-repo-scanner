package state

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanState_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-001", "https://repo.example.com", "com.example", stateFile)

	err := s.MarkCompleted("com/example/lib-a/1.0")
	require.NoError(t, err)
	err = s.MarkCompleted("com/example/lib-b/2.0")
	require.NoError(t, err)
	// Force flush since batch checkpoint may not have triggered
	require.NoError(t, s.Flush())

	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, CurrentVersion, loaded.Version)
	assert.Equal(t, "scan-001", loaded.ScanID)
	assert.Equal(t, 2, loaded.CompletedCount())
	assert.True(t, loaded.IsCompleted("com/example/lib-a/1.0"))
	assert.False(t, loaded.IsCompleted("com/example/lib-c/3.0"))
}

func TestScanState_LoadNonExistent(t *testing.T) {
	loaded, err := LoadScanState("/nonexistent/path/state.json")
	assert.ErrorIs(t, err, ErrStateNotFound)
	assert.Nil(t, loaded)
}

func TestScanState_MarkFailed(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-002", "https://repo.example.com", "", stateFile)
	err := s.MarkFailed("com/example/lib/1.0", "HTTP 503", 3)
	require.NoError(t, err)
	require.NoError(t, s.Flush())

	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, 1, len(loaded.FailedEntries))
	assert.Equal(t, "HTTP 503", loaded.FailedEntries[0].Error)
	assert.NotEmpty(t, loaded.FailedEntries[0].LastFailedAt, "LastFailedAt should be set")
}

func TestScanState_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-003", "https://repo.example.com", "", stateFile)
	s.MarkCompleted("com/example/lib/1.0")
	require.NoError(t, s.Flush())

	_, err := os.Stat(stateFile)
	require.NoError(t, err, "state file should exist")

	err = s.Delete()
	require.NoError(t, err)

	_, err = os.Stat(stateFile)
	assert.True(t, os.IsNotExist(err), "state file should be deleted")
}

func TestScanState_BatchCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Set checkpoint every 5 changes
	s := NewScanStateWithCheckpoint("scan-batch", "https://repo.example.com", "", stateFile, 5)

	// Add 4 items — should NOT trigger checkpoint (dirtyCount < 5)
	for i := 0; i < 4; i++ {
		err := s.MarkCompleted("com/example/lib/" + string(rune('A'+i)))
		require.NoError(t, err)
	}

	// State file should not exist yet (dirtyCount < checkpointEvery)
	_, err := os.Stat(stateFile)
	assert.True(t, os.IsNotExist(err), "state file should not exist before checkpoint threshold")

	// Add 5th item — triggers checkpoint
	err = s.MarkCompleted("com/example/lib/E")
	require.NoError(t, err)

	// State file should now exist
	_, err = os.Stat(stateFile)
	require.NoError(t, err, "state file should exist after checkpoint threshold")

	// Verify loaded state has all 5 items
	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, 5, loaded.CompletedCount())
}

func TestScanState_FailedCount(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-fc", "https://repo.example.com", "", stateFile)
	s.MarkFailed("com/example/lib/1.0", "HTTP 503", 3)
	s.MarkFailed("com/example/lib/2.0", "timeout", 1)

	assert.Equal(t, 2, s.FailedCount())
	assert.Equal(t, 0, s.CompletedCount())
}

func TestScanState_ForceFlush(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-flush", "https://repo.example.com", "", stateFile, 100)

	// Add 1 item — not enough to trigger auto-checkpoint
	s.MarkCompleted("com/example/lib/1.0")

	_, err := os.Stat(stateFile)
	assert.True(t, os.IsNotExist(err), "state file should not exist before flush")

	// Force flush
	require.NoError(t, s.Flush())

	_, err = os.Stat(stateFile)
	require.NoError(t, err, "state file should exist after force flush")
}

func TestScanState_SetCheckpointInterval(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	s := NewScanState("test-scan", "https://repo.example.com", "", stateFile)
	assert.Equal(t, 50, s.checkpointEvery)
	s.SetCheckpointInterval(10)
	assert.Equal(t, 10, s.checkpointEvery)
	s.SetCheckpointInterval(0)
	assert.Equal(t, 0, s.checkpointEvery)
}

func TestScanState_StatusTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-status", "https://repo.example.com", "", stateFile)
	assert.Equal(t, ScanRunning, s.GetStatus(), "new scan should be running")

	s.MarkInterrupted()
	assert.Equal(t, ScanInterrupted, s.GetStatus(), "should be interrupted after MarkInterrupted")

	s.MarkCompletedStatus()
	assert.Equal(t, ScanCompleted, s.GetStatus(), "should be completed after MarkCompletedStatus")

	// Verify persistence
	require.NoError(t, s.Flush())
	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, ScanCompleted, loaded.GetStatus())
}

func TestScanState_InFlight(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-inflight", "https://repo.example.com", "", stateFile)

	// Mark in-flight
	s.MarkInFlight("com/example/lib/1.0")
	assert.True(t, s.IsInFlight("com/example/lib/1.0"))
	assert.False(t, s.IsInFlight("com/example/lib/2.0"))
	assert.Equal(t, 1, s.InFlightCount())

	// Mark completed should remove from in-flight
	err := s.MarkCompleted("com/example/lib/1.0")
	require.NoError(t, err)
	assert.False(t, s.IsInFlight("com/example/lib/1.0"))
	assert.True(t, s.IsCompleted("com/example/lib/1.0"))
	assert.Equal(t, 0, s.InFlightCount())
}

func TestScanState_InFlightPreservedOnInterrupt(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-if-int", "https://repo.example.com", "", stateFile, 0)

	s.MarkInFlight("com/example/lib/1.0")
	s.MarkInFlight("com/example/lib/2.0")
	s.MarkCompleted("com/example/lib/3.0")

	require.NoError(t, s.Flush())

	// Load state and verify in-flight is preserved
	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.True(t, loaded.IsInFlight("com/example/lib/1.0"))
	assert.True(t, loaded.IsInFlight("com/example/lib/2.0"))
	assert.True(t, loaded.IsCompleted("com/example/lib/3.0"))
	assert.Equal(t, 2, loaded.InFlightCount())
}

func TestScanState_FailedDirs(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-dirs", "https://repo.example.com", "", stateFile, 0)

	// MarkDirFailed is idempotent.
	s.MarkDirFailed("com/example/lib")
	s.MarkDirFailed("com/example/lib")
	s.MarkDirFailed("org/apache/commons")
	assert.True(t, s.IsDirFailed("com/example/lib"))
	assert.True(t, s.IsDirFailed("org/apache/commons"))
	assert.False(t, s.IsDirFailed("com/example"))

	dirs := s.GetFailedDirs()
	sort.Strings(dirs)
	assert.Equal(t, []string{"com/example/lib", "org/apache/commons"}, dirs)

	// GetFailedDirs returns a copy — mutating it must not affect state.
	dirs[0] = "mutated"
	assert.True(t, s.IsDirFailed("com/example/lib"), "snapshot copy must not alias internal slice")

	// ClearDirFailed removes only the targeted directory.
	s.ClearDirFailed("com/example/lib")
	assert.False(t, s.IsDirFailed("com/example/lib"))
	assert.True(t, s.IsDirFailed("org/apache/commons"))
	assert.Equal(t, []string{"org/apache/commons"}, s.GetFailedDirs())

	// ClearDirFailed on an unknown path is a no-op.
	s.ClearDirFailed("never/failed")
	assert.Equal(t, []string{"org/apache/commons"}, s.GetFailedDirs())

	// Persist + reload rebuilds the failedDirSet.
	require.NoError(t, s.Flush())
	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.True(t, loaded.IsDirFailed("org/apache/commons"))
	assert.False(t, loaded.IsDirFailed("com/example/lib"))
	assert.Equal(t, []string{"org/apache/commons"}, loaded.GetFailedDirs())
}

func TestScanState_MarkFailedRemovesInFlight(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-fail-if", "https://repo.example.com", "", stateFile, 0)

	s.MarkInFlight("com/example/lib/1.0")
	assert.True(t, s.IsInFlight("com/example/lib/1.0"))

	err := s.MarkFailed("com/example/lib/1.0", "download failed", 1)
	require.NoError(t, err)

	assert.False(t, s.IsInFlight("com/example/lib/1.0"))
	assert.True(t, s.IsFailed("com/example/lib/1.0"))
	assert.Equal(t, 0, s.InFlightCount())
}

func TestScanState_MarkCompletedRemovesFailed(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-comp-fail", "https://repo.example.com", "", stateFile, 0)

	err := s.MarkFailed("com/example/lib/1.0", "timeout", 1)
	require.NoError(t, err)
	assert.True(t, s.IsFailed("com/example/lib/1.0"))
	assert.Equal(t, 1, s.FailedCount())

	// Re-scan succeeds — should remove from failed
	err = s.MarkCompleted("com/example/lib/1.0")
	require.NoError(t, err)
	assert.False(t, s.IsFailed("com/example/lib/1.0"))
	assert.True(t, s.IsCompleted("com/example/lib/1.0"))
	assert.Equal(t, 0, s.FailedCount())
}

func TestScanState_DiscoveryCache(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-disco", "https://repo.example.com", "", stateFile, 0)

	assert.False(t, s.HasDiscoveryCache())
	assert.Nil(t, s.GetDiscoveredArtifacts())

	artifacts := []string{"com/example/lib-a/1.0", "com/example/lib-b/2.0", "com/example/lib-c/3.0"}
	s.SetDiscoveredArtifacts(artifacts)

	assert.True(t, s.HasDiscoveryCache())
	cached := s.GetDiscoveredArtifacts()
	assert.Equal(t, 3, len(cached))
	assert.Equal(t, "com/example/lib-a/1.0", cached[0])

	// Verify persistence
	require.NoError(t, s.Flush())
	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.True(t, loaded.HasDiscoveryCache())
	loadedCached := loaded.GetDiscoveredArtifacts()
	assert.Equal(t, artifacts, loadedCached)
}

func TestScanState_ClearDiscoveryCache(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-disco-clear", "https://repo.example.com", "", stateFile, 0)
	s.SetDiscoveredArtifacts([]string{"com/example/lib/1.0"})
	assert.True(t, s.HasDiscoveryCache())

	s.ClearDiscoveryCache()
	assert.False(t, s.HasDiscoveryCache())
}

func TestScanState_GetRetryableFailures(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-retry", "https://repo.example.com", "", stateFile, 0)

	s.MarkFailed("com/example/lib/1.0", "timeout", 1)
	s.MarkFailed("com/example/lib/2.0", "HTTP 503", 3)
	s.MarkFailed("com/example/lib/3.0", "HTTP 500", 2)

	// With maxRetries=3, only entry with retries < 3 should be retryable
	retryable := s.GetRetryableFailures(3)
	assert.Equal(t, 2, len(retryable), "entries with retries < 3 should be retryable")

	// With maxRetries=0 (retry all), all should be retryable
	retryable = s.GetRetryableFailures(0)
	assert.Equal(t, 3, len(retryable), "all failures should be retryable with maxRetries=0")

	// With maxRetries=1, only entry with retries < 1 should be retryable
	retryable = s.GetRetryableFailures(1)
	assert.Equal(t, 0, len(retryable), "no failures should be retryable with maxRetries=1")
}

func TestScanState_ClearFailedEntry(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-clear-fail", "https://repo.example.com", "", stateFile, 0)
	s.MarkFailed("com/example/lib/1.0", "timeout", 1)
	s.MarkFailed("com/example/lib/2.0", "HTTP 503", 2)

	assert.Equal(t, 2, s.FailedCount())
	assert.True(t, s.IsFailed("com/example/lib/1.0"))

	s.ClearFailedEntry("com/example/lib/1.0")
	assert.False(t, s.IsFailed("com/example/lib/1.0"))
	assert.True(t, s.IsFailed("com/example/lib/2.0"))
	assert.Equal(t, 1, s.FailedCount())
}

func TestScanState_ValidateConfig(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	cfg := ConfigSnapshot{
		RepoURL:     "https://repo.example.com",
		GroupFilter: "com.example",
		RulesLevel:  "core",
		MaxFileSize: "50MB",
	}

	s := NewScanStateWithConfig("scan-cfg", "https://repo.example.com", "com.example", stateFile, 50, cfg)

	// Matching config should pass
	err := s.ValidateConfig(cfg)
	assert.NoError(t, err)

	// Mismatched RepoURL should fail
	err = s.ValidateConfig(ConfigSnapshot{RepoURL: "https://other.example.com", GroupFilter: "com.example"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "RepoURL")

	// Mismatched GroupFilter should fail
	err = s.ValidateConfig(ConfigSnapshot{RepoURL: "https://repo.example.com", GroupFilter: "org.other"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GroupFilter")
}

func TestScanState_ValidateConfig_EmptySnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-old", "https://repo.example.com", "", stateFile)
	// Old state without config snapshot should not error on validate
	err := s.ValidateConfig(ConfigSnapshot{RepoURL: "https://repo.example.com", GroupFilter: ""})
	assert.NoError(t, err)
}

func TestScanState_ProgressStats(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-stats", "https://repo.example.com", "", stateFile, 0)
	s.SetDiscoveredArtifacts([]string{"a", "b", "c"})
	s.MarkCompleted("a")
	s.MarkCompleted("b")
	s.MarkFailed("c", "error", 1)

	require.NoError(t, s.Flush())

	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)

	d, sc, f := loaded.GetProgressStats()
	assert.Equal(t, 3, d)
	assert.Equal(t, 2, sc)
	assert.Equal(t, 1, f)
}

func TestScanState_VersionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Write a state file with a future version
	data := `{"version": 999, "scan_id": "scan-future", "status": "running", "repo_url": "https://repo.example.com", "started_at": "2024-01-01T00:00:00Z", "last_updated": "2024-01-01T00:00:00Z", "completed_artifacts": [], "failed_artifacts": []}`
	require.NoError(t, os.WriteFile(stateFile, []byte(data), 0644))

	_, err := LoadScanState(stateFile)
	assert.ErrorIs(t, err, ErrVersionMismatch)
}

func TestScanState_VersionZeroMigration(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Write a state file without version field (old format)
	data := `{"scan_id": "scan-old", "repo_url": "https://repo.example.com", "started_at": "2024-01-01T00:00:00Z", "last_updated": "2024-01-01T00:00:00Z", "completed_artifacts": ["a/1.0"], "failed_artifacts": []}`
	require.NoError(t, os.WriteFile(stateFile, []byte(data), 0644))

	loaded, err := LoadScanState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, CurrentVersion, loaded.Version, "version 0 should be migrated to current version")
	assert.Equal(t, ScanInterrupted, loaded.GetStatus(), "old state without status should be treated as interrupted")
	assert.True(t, loaded.IsCompleted("a/1.0"))
}

func TestScanState_RemoveInFlight(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-rm-if", "https://repo.example.com", "", stateFile, 0)
	s.MarkInFlight("com/example/lib/1.0")
	assert.True(t, s.IsInFlight("com/example/lib/1.0"))

	s.RemoveInFlight("com/example/lib/1.0")
	assert.False(t, s.IsInFlight("com/example/lib/1.0"))
	assert.Equal(t, 0, s.InFlightCount())

	// Remove non-existent should be no-op
	s.RemoveInFlight("com/example/lib/nonexistent")
}

func TestScanState_DuplicateInFlight(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanStateWithCheckpoint("scan-dup-if", "https://repo.example.com", "", stateFile, 0)
	s.MarkInFlight("com/example/lib/1.0")
	s.MarkInFlight("com/example/lib/1.0") // duplicate should be no-op

	assert.Equal(t, 1, s.InFlightCount())
}

func TestScanState_SetMaxRetries(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	s := NewScanState("scan-retries", "https://repo.example.com", "", stateFile)
	s.SetMaxRetries(5)
	assert.Equal(t, 5, s.MaxRetries)
}
