package report

import (
	"bytes"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReport(t *testing.T) {
	r := NewReport("scan-001", "https://repo.example.com", "com.example", 10)
	assert.Equal(t, "scan-001", r.ScanID)
	assert.Equal(t, 10, r.Config.Concurrency)
	assert.Equal(t, "com.example", r.Config.GroupFilter)
	assert.NotNil(t, r.Findings)
}

func TestConsoleReporter_PrintSummary(t *testing.T) {
	var buf bytes.Buffer
	cr := &ConsoleReporter{writer: &buf}

	summary := scanner.NewSummary()
	summary.TotalDiscovered = 100
	summary.TotalScanned = 95
	summary.TotalFailed = 5
	summary.TotalFindings = 3

	cr.PrintSummary(summary)

	output := buf.String()
	assert.Contains(t, output, "Discovered: 100")
	assert.Contains(t, output, "Scanned:    95")
	assert.Contains(t, output, "Findings:   3")
}

func TestJSONReporter_WriteToBuffer(t *testing.T) {
	r := NewReport("scan-002", "https://repo.example.com", "", 5)
	r.Summary = scanner.NewSummary()
	r.Summary.TotalScanned = 10

	jr := NewJSONReporter("-", r)
	require.NoError(t, jr.Write())
}

func TestSeverityColor(t *testing.T) {
	assert.Contains(t, severityColor("CRITICAL"), "CRITICAL")
	assert.Contains(t, severityColor("HIGH"), "HIGH")
	assert.Contains(t, severityColor("MEDIUM"), "MEDIUM")
	assert.Contains(t, severityColor("LOW"), "LOW")
	assert.Equal(t, "UNKNOWN", severityColor("UNKNOWN"))
}
