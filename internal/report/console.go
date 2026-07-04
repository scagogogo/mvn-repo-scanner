package report

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/scagogogo/mvn-repo-scanner/internal/scanner"
)

// ConsoleReporter outputs scan results to the terminal with colors.
type ConsoleReporter struct {
	writer io.Writer
}

// NewConsoleReporter creates a console reporter writing to stdout.
func NewConsoleReporter() *ConsoleReporter {
	return &ConsoleReporter{writer: os.Stdout}
}

// NewConsoleReporterWithWriter creates a console reporter writing to a custom writer.
func NewConsoleReporterWithWriter(w io.Writer) *ConsoleReporter {
	if w == nil {
		w = os.Stdout
	}
	return &ConsoleReporter{writer: w}
}

// PrintFinding prints a single finding in real-time.
func (c *ConsoleReporter) PrintFinding(fd FindingDetail) {
	sev := severityColor(fd.Severity)
	fmt.Fprintf(c.writer, "\n  %s [%s] %s\n", sev, fd.RuleID, fd.RuleName)
	fmt.Fprintf(c.writer, "    Artifact: %s\n", fd.Artifact)
	fmt.Fprintf(c.writer, "    File:     %s:%d\n", fd.File, fd.LineNumber)
	fmt.Fprintf(c.writer, "    Match:    %s\n", truncate(fd.Match, 80))
}

// PrintSummary prints the final scan summary.
func (c *ConsoleReporter) PrintSummary(summary *scanner.Summary) {
	fmt.Fprintf(c.writer, "\n%s\n", strings.Repeat("=", 60))
	fmt.Fprintf(c.writer, "%s\n", summary.String())
	fmt.Fprintf(c.writer, "%s\n", strings.Repeat("=", 60))
}

// severityColor returns a colorized severity string.
func severityColor(sev string) string {
	switch detector.Severity(sev) {
	case detector.SeverityCritical:
		return fmt.Sprintf("\033[1;31m%s\033[0m", sev)
	case detector.SeverityHigh:
		return fmt.Sprintf("\033[31m%s\033[0m", sev)
	case detector.SeverityMedium:
		return fmt.Sprintf("\033[33m%s\033[0m", sev)
	case detector.SeverityLow:
		return fmt.Sprintf("\033[36m%s\033[0m", sev)
	default:
		return sev
	}
}

// truncate shortens a string.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
