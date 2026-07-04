package report

import (
	"encoding/json"
	"fmt"
	"os"
)

// JSONReporter generates JSON format reports.
type JSONReporter struct {
	outputFile string
	report     *Report
}

// NewJSONReporter creates a JSON reporter.
func NewJSONReporter(outputFile string, report *Report) *JSONReporter {
	return &JSONReporter{
		outputFile: outputFile,
		report:     report,
	}
}

// Write writes the report to a JSON file or stdout.
func (j *JSONReporter) Write() error {
	data, err := json.MarshalIndent(j.report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	if j.outputFile == "" || j.outputFile == "-" {
		fmt.Println(string(data))
		return nil
	}

	return os.WriteFile(j.outputFile, data, 0644)
}
