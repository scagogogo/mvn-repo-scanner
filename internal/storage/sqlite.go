// Package storage provides SQLite-based scan history persistence and workspace management.
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DBScanStatus represents the scan status of a GAV coordinate.
type DBScanStatus string

const (
	DBStatusPending  DBScanStatus = "pending"
	DBStatusScanning DBScanStatus = "scanning"
	DBStatusComplete DBScanStatus = "complete"
	DBStatusFailed   DBScanStatus = "failed"
)

// GAVRecord represents a scanned artifact's history record.
type GAVRecord struct {
	ID          int64       `json:"id"`
	GroupID     string      `json:"group_id"`
	ArtifactID  string      `json:"artifact_id"`
	Version     string      `json:"version"`
	RepoURL     string      `json:"repo_url"`
	Status      DBScanStatus `json:"status"`
	Findings    int         `json:"findings_count"`
	ScanTime    time.Time   `json:"scan_time"`
	DurationMs  int64       `json:"duration_ms"`
	Error       string      `json:"error,omitempty"`
	RuleMatches string      `json:"rule_matches,omitempty"`
	FileHash    string      `json:"file_hash,omitempty"`
}

// FindingRecord represents a single finding stored in the database.
type FindingRecord struct {
	ID          int64  `json:"id"`
	RecordID    int64  `json:"record_id"`
	RuleID      string `json:"rule_id"`
	RuleName    string `json:"rule_name"`
	Severity    string `json:"severity"`
	FilePath    string `json:"file_path"`
	LineNumber  int    `json:"line_number"`
	LineContent string `json:"line_content"`
	Match       string `json:"match"`
}

// Stats holds aggregate statistics from the database.
type Stats struct {
	TotalRecords  int `json:"total_records"`
	Completed     int `json:"completed"`
	Failed        int `json:"failed"`
	TotalFindings int `json:"total_findings"`
	CriticalCount int `json:"critical_count"`
	HighCount     int `json:"high_count"`
	DBSizeMB      int `json:"db_size_mb"`
}

// Store wraps a SQLite database for scan history.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite database at the given path.
func OpenStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set wal mode: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := s.migrateTasksTable(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate tasks: %w", err)
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping verifies the database connection is alive.
func (s *Store) Ping() error {
	return s.db.Ping()
}

// migrate creates the database schema if it doesn't exist.
func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS scan_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id TEXT NOT NULL,
		artifact_id TEXT NOT NULL,
		version TEXT NOT NULL,
		repo_url TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		findings_count INTEGER NOT NULL DEFAULT 0,
		scan_time DATETIME NOT NULL,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT DEFAULT '',
		rule_matches TEXT DEFAULT '[]',
		file_hash TEXT DEFAULT '',
		UNIQUE(group_id, artifact_id, version, repo_url)
	);
	CREATE INDEX IF NOT EXISTS idx_records_gav ON scan_records(group_id, artifact_id, version);
	CREATE INDEX IF NOT EXISTS idx_records_status ON scan_records(status);
	CREATE INDEX IF NOT EXISTS idx_records_time ON scan_records(scan_time);

	CREATE TABLE IF NOT EXISTS findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		record_id INTEGER NOT NULL,
		rule_id TEXT NOT NULL,
		rule_name TEXT NOT NULL,
		severity TEXT NOT NULL,
		file_path TEXT NOT NULL,
		line_number INTEGER NOT NULL DEFAULT 0,
		line_content TEXT DEFAULT '',
		match TEXT DEFAULT '',
		FOREIGN KEY (record_id) REFERENCES scan_records(id)
	);
	CREATE INDEX IF NOT EXISTS idx_findings_record ON findings(record_id);
	CREATE INDEX IF NOT EXISTS idx_findings_rule ON findings(rule_id);
	CREATE INDEX IF NOT EXISTS idx_findings_severity ON findings(severity);
	`
	_, err := s.db.Exec(schema)
	return err
}

// IsScanned checks if a GAV has been successfully scanned.
func (s *Store) IsScanned(groupID, artifactID, version, repoURL string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(1) FROM scan_records WHERE group_id=? AND artifact_id=? AND version=? AND repo_url=? AND status=?",
		groupID, artifactID, version, repoURL, DBStatusComplete,
	).Scan(&count)
	return count > 0, err
}

// UpsertRecord creates or updates a scan record.
func (s *Store) UpsertRecord(rec *GAVRecord) error {
	// Delete old findings for existing record before upsert to prevent
	// duplicate accumulation on re-scan.
	var existingID int64
	err := s.db.QueryRow(
		"SELECT id FROM scan_records WHERE group_id = ? AND artifact_id = ? AND version = ? AND repo_url = ?",
		rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL,
	).Scan(&existingID)
	if err == nil {
		_, _ = s.db.Exec("DELETE FROM findings WHERE record_id = ?", existingID)
	}

	_, err = s.db.Exec(`
		INSERT INTO scan_records (group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms, error, rule_matches, file_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_id, artifact_id, version, repo_url) DO UPDATE SET
			status=excluded.status, findings_count=excluded.findings_count,
			scan_time=excluded.scan_time, duration_ms=excluded.duration_ms,
			error=excluded.error, rule_matches=excluded.rule_matches,
			file_hash=excluded.file_hash`,
		rec.GroupID, rec.ArtifactID, rec.Version, rec.RepoURL,
		rec.Status, rec.Findings, rec.ScanTime, rec.DurationMs,
		rec.Error, rec.RuleMatches, rec.FileHash,
	)
	return err
}

// InsertFinding adds a finding for a scan record.
func (s *Store) InsertFinding(f *FindingRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO findings (record_id, rule_id, rule_name, severity, file_path, line_number, line_content, match)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.RecordID, f.RuleID, f.RuleName, f.Severity,
		f.FilePath, f.LineNumber, f.LineContent, f.Match,
	)
	return err
}

// GetRecord retrieves a scan record by GAV.
func (s *Store) GetRecord(groupID, artifactID, version, repoURL string) (*GAVRecord, error) {
	rec := &GAVRecord{}
	err := s.db.QueryRow(`
		SELECT id, group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms, error, rule_matches
		FROM scan_records WHERE group_id=? AND artifact_id=? AND version=? AND repo_url=?`,
		groupID, artifactID, version, repoURL,
	).Scan(&rec.ID, &rec.GroupID, &rec.ArtifactID, &rec.Version, &rec.RepoURL,
		&rec.Status, &rec.Findings, &rec.ScanTime, &rec.DurationMs, &rec.Error, &rec.RuleMatches)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// GetFindings retrieves all findings for a scan record.
func (s *Store) GetFindings(recordID int64) ([]FindingRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, record_id, rule_id, rule_name, severity, file_path, line_number, line_content, match
		FROM findings WHERE record_id=?`, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []FindingRecord
	for rows.Next() {
		var f FindingRecord
		if err := rows.Scan(&f.ID, &f.RecordID, &f.RuleID, &f.RuleName, &f.Severity,
			&f.FilePath, &f.LineNumber, &f.LineContent, &f.Match); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// GetStats returns aggregate statistics from the database.
func (s *Store) GetStats() (*Stats, error) {
	st := &Stats{}
	err := s.db.QueryRow(`
		SELECT COUNT(1),
		       COALESCE(SUM(CASE WHEN status='complete' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(findings_count), 0)
		FROM scan_records`).Scan(&st.TotalRecords, &st.Completed, &st.Failed, &st.TotalFindings)
	if err != nil {
		return nil, err
	}

	s.db.QueryRow(`SELECT COUNT(1) FROM findings WHERE severity='CRITICAL'`).Scan(&st.CriticalCount)
	s.db.QueryRow(`SELECT COUNT(1) FROM findings WHERE severity='HIGH'`).Scan(&st.HighCount)
	return st, nil
}

// RecentRecords returns the most recent N scan records.
func (s *Store) RecentRecords(limit int) ([]GAVRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, group_id, artifact_id, version, repo_url, status, findings_count, scan_time, duration_ms
		FROM scan_records ORDER BY scan_time DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []GAVRecord
	for rows.Next() {
		var r GAVRecord
		if err := rows.Scan(&r.ID, &r.GroupID, &r.ArtifactID, &r.Version, &r.RepoURL,
			&r.Status, &r.Findings, &r.ScanTime, &r.DurationMs); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

// FindingsByRule returns findings grouped by rule ID.
func (s *Store) FindingsByRule() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT rule_id, COUNT(1) FROM findings GROUP BY rule_id ORDER BY COUNT(1) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var ruleID string
		var count int
		if err := rows.Scan(&ruleID, &count); err != nil {
			return nil, err
		}
		result[ruleID] = count
	}
	return result, nil
}

// FindingsBySeverity returns findings grouped by severity.
func (s *Store) FindingsBySeverity() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT severity, COUNT(1) FROM findings GROUP BY severity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var sev string
		var count int
		if err := rows.Scan(&sev, &count); err != nil {
			return nil, err
		}
		result[sev] = count
	}
	return result, nil
}

// DeleteOldRecords deletes scan records older than the given number of days.
func (s *Store) DeleteOldRecords(olderThanDays int) (int64, error) {
	result, err := s.db.Exec(`
		DELETE FROM scan_records WHERE scan_time < datetime('now', ?)`,
		fmt.Sprintf("-%d days", olderThanDays))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ExportFindingsJSON exports all findings as JSON.
func (s *Store) ExportFindingsJSON() ([]byte, error) {
	rows, err := s.db.Query(`
		SELECT r.group_id, r.artifact_id, r.version, f.rule_id, f.rule_name, f.severity, f.file_path, f.line_number, f.line_content, f.match
		FROM findings f JOIN scan_records r ON f.record_id = r.id
		ORDER BY f.severity, r.group_id, r.artifact_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type exportRow struct {
		GAV         string `json:"gav"`
		RuleID      string `json:"rule_id"`
		RuleName    string `json:"rule_name"`
		Severity    string `json:"severity"`
		FilePath    string `json:"file_path"`
		LineNumber  int    `json:"line_number"`
		LineContent string `json:"line_content"`
		Match       string `json:"match"`
	}

	var exportRows []exportRow
	for rows.Next() {
		var r exportRow
		var gid, aid, ver string
		if err := rows.Scan(&gid, &aid, &ver, &r.RuleID, &r.RuleName, &r.Severity,
			&r.FilePath, &r.LineNumber, &r.LineContent, &r.Match); err != nil {
			return nil, err
		}
		r.GAV = gid + ":" + aid + ":" + ver
		exportRows = append(exportRows, r)
	}
	return json.MarshalIndent(exportRows, "", "  ")
}
