package scanner

import (
	"fmt"
	"strings"

	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/repo"
)

// ScanStatus represents the status of a single artifact scan.
type ScanStatus string

const (
	StatusPending  ScanStatus = "pending"
	StatusScanning ScanStatus = "scanning"
	StatusComplete ScanStatus = "complete"
	StatusFailed   ScanStatus = "failed"
)

// IsValid returns true if the status is one of the defined constants.
func (s ScanStatus) IsValid() bool {
	switch s {
	case StatusPending, StatusScanning, StatusComplete, StatusFailed:
		return true
	default:
		return false
	}
}

// ArtifactResult holds the result of scanning a single artifact.
type ArtifactResult struct {
	Artifact repo.Artifact
	Status   ScanStatus
	Findings []detector.Finding
	Error    error
}

// String returns a human-readable summary of the artifact result.
func (r ArtifactResult) String() string {
	return fmt.Sprintf("%s %s (%d findings)", r.Artifact.String(), r.Status, len(r.Findings))
}

// Summary holds aggregate scan statistics.
type Summary struct {
	TotalDiscovered int                       `json:"total_discovered"`
	TotalScanned    int                       `json:"total_scanned"`
	TotalFailed     int                       `json:"total_failed"`
	TotalFindings   int                       `json:"total_findings"`
	BySeverity      map[detector.Severity]int `json:"by_severity"`
	ByRule          map[string]int            `json:"by_rule"`
}

// NewSummary creates an initialized Summary.
func NewSummary() *Summary {
	return &Summary{
		BySeverity: make(map[detector.Severity]int),
		ByRule:     make(map[string]int),
	}
}

// AddFinding records a finding in the summary.
func (s *Summary) AddFinding(f detector.Finding) {
	s.TotalFindings++
	s.BySeverity[f.Severity]++
	s.ByRule[f.RuleID]++
}

// String returns a human-readable summary.
func (s *Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scan Summary:\n")
	fmt.Fprintf(&b, "  Discovered: %d\n", s.TotalDiscovered)
	fmt.Fprintf(&b, "  Scanned:    %d\n", s.TotalScanned)
	fmt.Fprintf(&b, "  Failed:     %d\n", s.TotalFailed)
	fmt.Fprintf(&b, "  Findings:   %d\n", s.TotalFindings)
	if len(s.BySeverity) > 0 {
		fmt.Fprintf(&b, "  By Severity:\n")
		for _, sev := range []detector.Severity{detector.SeverityCritical, detector.SeverityHigh, detector.SeverityMedium, detector.SeverityLow} {
			if count, ok := s.BySeverity[sev]; ok {
				fmt.Fprintf(&b, "    %s: %d\n", sev, count)
			}
		}
	}
	return b.String()
}
