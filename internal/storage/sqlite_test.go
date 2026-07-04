package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_OpenAndMigrate(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := OpenStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	var count int
	err = store.db.QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name IN ('scan_records', 'findings')").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestStore_UpsertAndQuery(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	rec := &GAVRecord{
		GroupID:    "com.example",
		ArtifactID: "my-lib",
		Version:    "1.0",
		RepoURL:    "https://repo.example.com",
		Status:     DBStatusComplete,
		Findings:   3,
		ScanTime:   time.Now(),
		DurationMs: 1500,
	}
	require.NoError(t, store.UpsertRecord(rec))

	scanned, err := store.IsScanned("com.example", "my-lib", "1.0", "https://repo.example.com")
	require.NoError(t, err)
	assert.True(t, scanned)

	scanned, err = store.IsScanned("com.other", "lib", "2.0", "https://repo.example.com")
	require.NoError(t, err)
	assert.False(t, scanned)

	got, err := store.GetRecord("com.example", "my-lib", "1.0", "https://repo.example.com")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 3, got.Findings)
	assert.Equal(t, DBStatusComplete, got.Status)
}

func TestStore_InsertFindings(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	rec := &GAVRecord{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		RepoURL: "https://repo.example.com", Status: DBStatusComplete,
		Findings: 2, ScanTime: time.Now(),
	}
	require.NoError(t, store.UpsertRecord(rec))

	got, _ := store.GetRecord("com.example", "lib", "1.0", "https://repo.example.com")
	require.NotNil(t, got)

	f1 := &FindingRecord{RecordID: got.ID, RuleID: "hardcoded-password", Severity: "CRITICAL", FilePath: "app.properties", LineNumber: 10}
	f2 := &FindingRecord{RecordID: got.ID, RuleID: "aws-secret-key", Severity: "HIGH", FilePath: "config.yml", LineNumber: 5}
	require.NoError(t, store.InsertFinding(f1))
	require.NoError(t, store.InsertFinding(f2))

	findings, err := store.GetFindings(got.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, len(findings))
	assert.Equal(t, "hardcoded-password", findings[0].RuleID)
}

func TestStore_Ping(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Ping())
}

func TestStore_Stats(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	store.UpsertRecord(&GAVRecord{GroupID: "a", ArtifactID: "b", Version: "1", RepoURL: "r", Status: DBStatusComplete, Findings: 5, ScanTime: time.Now()})
	store.UpsertRecord(&GAVRecord{GroupID: "c", ArtifactID: "d", Version: "2", RepoURL: "r", Status: DBStatusFailed, Findings: 0, ScanTime: time.Now()})

	stats, err := store.GetStats()
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalRecords)
	assert.Equal(t, 1, stats.Completed)
	assert.Equal(t, 1, stats.Failed)
	assert.Equal(t, 5, stats.TotalFindings)
}

func TestStore_RecentRecords(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	store.UpsertRecord(&GAVRecord{GroupID: "a", ArtifactID: "b", Version: "1", RepoURL: "r", Status: DBStatusComplete, ScanTime: time.Now()})
	store.UpsertRecord(&GAVRecord{GroupID: "c", ArtifactID: "d", Version: "2", RepoURL: "r", Status: DBStatusComplete, ScanTime: time.Now()})

	records, err := store.RecentRecords(10)
	require.NoError(t, err)
	assert.Equal(t, 2, len(records))
}

func TestStore_UpsertRecord_NoDuplicateFindings(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	rec := &GAVRecord{
		GroupID: "com.example", ArtifactID: "lib", Version: "1.0",
		RepoURL: "https://repo.example.com", Status: DBStatusComplete,
		ScanTime: time.Now(),
	}

	// First insert
	require.NoError(t, store.UpsertRecord(rec))
	id1, err := store.GetRecord(rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL)
	require.NoError(t, err)

	// Insert 2 findings
	require.NoError(t, store.InsertFinding(&FindingRecord{RecordID: id1.ID, RuleID: "r1", Severity: "HIGH"}))
	require.NoError(t, store.InsertFinding(&FindingRecord{RecordID: id1.ID, RuleID: "r2", Severity: "CRITICAL"}))

	// Second Upsert (re-scan)
	require.NoError(t, store.UpsertRecord(rec))
	id2, err := store.GetRecord(rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL)
	require.NoError(t, err)

	// Insert 1 new finding
	require.NoError(t, store.InsertFinding(&FindingRecord{RecordID: id2.ID, RuleID: "r3", Severity: "MEDIUM"}))

	// Should have only 1 finding (old 2 deleted, new 1 kept)
	findings, err := store.GetFindings(id2.ID)
	require.NoError(t, err)
	assert.Len(t, findings, 1)
	assert.Equal(t, "r3", findings[0].RuleID)
}

func TestStore_ExportFindingsJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer store.Close()

	store.UpsertRecord(&GAVRecord{GroupID: "com.example", ArtifactID: "lib", Version: "1.0", RepoURL: "r", Status: DBStatusComplete, ScanTime: time.Now()})
	got, _ := store.GetRecord("com.example", "lib", "1.0", "r")
	require.NotNil(t, got)
	store.InsertFinding(&FindingRecord{RecordID: got.ID, RuleID: "test-rule", Severity: "HIGH", FilePath: "config.yml", LineNumber: 5})

	data, err := store.ExportFindingsJSON()
	require.NoError(t, err)
	assert.Contains(t, string(data), "com.example:lib:1.0")
	assert.Contains(t, string(data), "test-rule")
}
