package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONReporter_WriteToStdout(t *testing.T) {
	r := NewReport("scan-stdout", "https://repo.example.com", "com.example", 5)
	r.Summary = scanner.NewSummary()
	r.Summary.TotalScanned = 42

	jr := NewJSONReporter("-", r)
	require.NoError(t, jr.Write())
}

func TestJSONReporter_WriteToFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "report.json")

	r := NewReport("scan-file", "https://repo.example.com", "", 10)
	r.Summary = scanner.NewSummary()
	r.Summary.TotalScanned = 10
	r.Findings = append(r.Findings, FindingDetail{
		Artifact:   "com.example:lib:1.0",
		File:       "app.properties",
		RuleID:     "hardcoded-password",
		RuleName:   "Hardcoded Password",
		Severity:   "CRITICAL",
		LineNumber: 5,
		Match:      "password=secret123",
	})

	jr := NewJSONReporter(outputPath, r)
	require.NoError(t, jr.Write())

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)

	var parsed Report
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "scan-file", parsed.ScanID)
	assert.Equal(t, 1, len(parsed.Findings))
	assert.Equal(t, "hardcoded-password", parsed.Findings[0].RuleID)
	assert.Equal(t, "CRITICAL", parsed.Findings[0].Severity)
}

func TestJSONReporter_WriteToEmptyPath(t *testing.T) {
	r := NewReport("scan-empty", "https://repo.example.com", "", 1)
	r.Summary = scanner.NewSummary()

	jr := NewJSONReporter("", r)
	require.NoError(t, jr.Write(), "empty output file should write to stdout")
}

func TestJSONReporter_ReportStructure(t *testing.T) {
	r := NewReport("scan-struct", "https://repo.example.com", "com.test", 20)
	r.Summary = scanner.NewSummary()
	r.Summary.TotalScanned = 100
	r.Summary.TotalFailed = 2
	r.Summary.TotalFindings = 3

	assert.Equal(t, "scan-struct", r.ScanID)
	assert.Equal(t, 20, r.Config.Concurrency)
	assert.Equal(t, "com.test", r.Config.GroupFilter)
	assert.NotNil(t, r.Findings)
}

func TestJSONReporter_WriteToEmptyPath_PrintsToStdout(t *testing.T) {
	// outputFile="" → 走 stdout 分支（Println），不报错
	r := NewReport("scan-empty", "https://repo.example.com", "", 1)
	r.Summary = scanner.NewSummary()
	jr := NewJSONReporter("", r) // 空字符串
	require.NoError(t, jr.Write())
}

func TestJSONReporter_WriteToFile_BadDir(t *testing.T) {
	// outputFile 指向不存在的目录 → os.WriteFile 失败分支
	r := NewReport("scan-baddir", "https://repo.example.com", "", 1)
	r.Summary = scanner.NewSummary()
	jr := NewJSONReporter("/nonexistent/dir/report.json", r)
	err := jr.Write()
	assert.Error(t, err)
}
