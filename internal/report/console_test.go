package report

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsoleReporter_PrintFinding(t *testing.T) {
	var buf bytes.Buffer
	cr := &ConsoleReporter{writer: &buf}

	fd := FindingDetail{
		Artifact:    "com.example:mylib:1.0",
		File:        "config.properties",
		RuleID:      "hardcoded-password",
		RuleName:    "Hardcoded Password",
		Severity:    "CRITICAL",
		LineNumber:  42,
		LineContent: "db.password=S3cretP@ss",
		Match:       "S3cretP@ss",
		Description: "Detects hardcoded passwords",
	}
	cr.PrintFinding(fd)

	output := buf.String()
	assert.Contains(t, output, "hardcoded-password")
	assert.Contains(t, output, "config.properties:42")
	assert.Contains(t, output, "com.example:mylib:1.0")
	assert.Contains(t, output, "CRITICAL")
}

func TestConsoleReporter_PrintFinding_LongMatch(t *testing.T) {
	var buf bytes.Buffer
	cr := &ConsoleReporter{writer: &buf}

	longMatch := strings.Repeat("A", 200)
	fd := FindingDetail{
		Artifact:   "g:a:v",
		File:       "f.properties",
		RuleID:     "test",
		RuleName:   "Test",
		Severity:   "HIGH",
		LineNumber: 1,
		Match:      longMatch,
	}
	cr.PrintFinding(fd)

	output := buf.String()
	assert.Contains(t, output, "AAA", "should contain beginning of match")
}

func TestConsoleReporter_PrintSummary_WithFindings(t *testing.T) {
	var buf bytes.Buffer
	cr := &ConsoleReporter{writer: &buf}

	summary := scanner.NewSummary()
	summary.TotalDiscovered = 200
	summary.TotalScanned = 190
	summary.TotalFailed = 10
	summary.TotalFindings = 5

	cr.PrintSummary(summary)

	output := buf.String()
	assert.Contains(t, output, "200")
	assert.Contains(t, output, "190")
	assert.Contains(t, output, "10")
	assert.Contains(t, output, "5")
}

func TestSeverityColor_AllLevels(t *testing.T) {
	tests := []struct {
		sev      string
		contains string
	}{
		{"CRITICAL", "CRITICAL"},
		{"HIGH", "HIGH"},
		{"MEDIUM", "MEDIUM"},
		{"LOW", "LOW"},
	}
	for _, tt := range tests {
		result := severityColor(tt.sev)
		assert.Contains(t, result, tt.contains, "severityColor(%s) should contain %s", tt.sev, tt.contains)
	}
}

func TestSeverityColor_Unknown(t *testing.T) {
	result := severityColor("UNKNOWN")
	assert.Equal(t, "UNKNOWN", result, "unknown severity should not be colorized")
}

func TestNewConsoleReporterWithWriter(t *testing.T) {
	var buf bytes.Buffer
	cr := NewConsoleReporterWithWriter(&buf)
	require.NotNil(t, cr)
	assert.Equal(t, &buf, cr.writer)
}

func TestNewConsoleReporterWithWriter_Nil(t *testing.T) {
	cr := NewConsoleReporterWithWriter(nil)
	require.NotNil(t, cr)
	assert.Equal(t, os.Stdout, cr.writer)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abcde...", truncate("abcdef", 5))
	assert.Equal(t, "", truncate("", 5))
}
