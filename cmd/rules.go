package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/spf13/cobra"
)

var rulesLevel string

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "List and manage detection rules",
	Long:  "Display available detection rules, their IDs, severity, and descriptions.",
	RunE:  runRules,
}

func init() {
	rootCmd.AddCommand(rulesCmd)
	rulesCmd.Flags().StringVarP(&rulesLevel, "level", "l", "all", "rule set to show: core, extended, all")
}

func runRules(cmd *cobra.Command, args []string) error {
	var rules []*detector.Rule

	switch rulesLevel {
	case "core":
		rules = detector.DefaultRules()
	case "extended":
		rules = detector.ExtendedRules()
	default:
		rules = detector.AllRules()
	}

	fmt.Printf("Detection Rules (%s, %d rules)\n\n", rulesLevel, len(rules))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSEVERITY\tNAME\tENABLED")
	for _, r := range rules {
		status := "enabled"
		if !r.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Severity, r.Name, status)
	}
	w.Flush()

	fmt.Printf("\nTotal: %d rules\n", len(rules))
	return nil
}
