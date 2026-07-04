// Package report generates scan output in console and JSON formats.
package report

import (
	"time"

	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
)

// Report represents the complete scan report.
type Report struct {
	ScanID    string           `json:"scan_id"`
	StartTime string           `json:"start_time"`
	EndTime   string           `json:"end_time"`
	RepoURL   string           `json:"repo_url"`
	Config    ScanConfig       `json:"config"`
	Summary   *scanner.Summary `json:"summary"`
	Findings  []FindingDetail  `json:"findings"`
}

// ScanConfig records the scan configuration used.
type ScanConfig struct {
	Concurrency int    `json:"concurrency"`
	GroupFilter string `json:"group_filter,omitempty"`
}

// FindingDetail is a flattened finding with artifact context.
type FindingDetail struct {
	Artifact    string `json:"artifact"`
	File        string `json:"file_path"`
	RuleID      string `json:"rule_id"`
	RuleName    string `json:"rule_name"`
	Severity    string `json:"severity"`
	LineNumber  int    `json:"line_number"`
	LineContent string `json:"line_content"`
	Match       string `json:"match"`
	Description string `json:"description"`
}

// NewReport creates a new report.
func NewReport(scanID, repoURL, groupFilter string, concurrency int) *Report {
	return &Report{
		ScanID:    scanID,
		StartTime: time.Now().Format(time.RFC3339),
		RepoURL:   repoURL,
		Config: ScanConfig{
			Concurrency: concurrency,
			GroupFilter: groupFilter,
		},
		Findings: []FindingDetail{},
	}
}
