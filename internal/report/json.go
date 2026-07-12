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
	// Report contains only JSON-serializable types (strings, ints, slices,
	// pointers to such), so MarshalIndent cannot fail in practice — the error
	// is intentionally ignored.
	data, _ := json.MarshalIndent(j.report, "", "  ")

	if j.outputFile == "" || j.outputFile == "-" {
		fmt.Println(string(data))
		return nil
	}

	return os.WriteFile(j.outputFile, data, 0644)
}
