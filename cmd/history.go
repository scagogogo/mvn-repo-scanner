package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/scagogogo/mvn-repo-scanner/internal/storage"
	"github.com/spf13/cobra"
)

var historyLimit int

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Show scan history and statistics",
	Long:  "Display recent scan history, statistics, and findings from the local SQLite database.",
	RunE:  runHistory,
}

func init() {
	rootCmd.AddCommand(historyCmd)
	historyCmd.Flags().IntVarP(&historyLimit, "limit", "n", 20, "number of recent records to show")
}

func runHistory(cmd *cobra.Command, args []string) error {
	ws, err := storage.NewWorkspace()
	if err != nil {
		return fmt.Errorf("init workspace: %w", err)
	}

	store, err := openStoreFn(ws.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	// Show stats
	stats, err := store.GetStats()
	if err != nil {
		return err
	}

	fmt.Println("=== Scan History Statistics ===")
	fmt.Printf("  Database: %s\n", ws.DBPath)
	fmt.Printf("  Total Records: %d\n", stats.TotalRecords)
	fmt.Printf("  Completed:     %d\n", stats.Completed)
	fmt.Printf("  Failed:        %d\n", stats.Failed)
	fmt.Printf("  Total Findings: %d (CRITICAL: %d, HIGH: %d)\n",
		stats.TotalFindings, stats.CriticalCount, stats.HighCount)

	// Show findings by rule
	byRule, _ := store.FindingsByRule()
	if len(byRule) > 0 {
		fmt.Println("\n  Findings by Rule:")
		for rule, count := range byRule {
			fmt.Printf("    %-30s %d\n", rule, count)
		}
	}

	// Show findings by severity
	bySev, _ := store.FindingsBySeverity()
	if len(bySev) > 0 {
		fmt.Println("\n  Findings by Severity:")
		for sev, count := range bySev {
			fmt.Printf("    %-15s %d\n", sev, count)
		}
	}

	// Show recent records
	records, err := store.RecentRecords(historyLimit)
	if err != nil {
		return err
	}

	if len(records) > 0 {
		fmt.Printf("\n=== Recent %d Scans ===\n", len(records))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "GROUPID\tARTIFACTID\tVERSION\tSTATUS\tFINDINGS\tTIME")
		for _, r := range records {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
				r.GroupID, r.ArtifactID, r.Version, r.Status, r.Findings, r.ScanTime.Format("2006-01-02 15:04"))
		}
		w.Flush()
	}

	return nil
}
