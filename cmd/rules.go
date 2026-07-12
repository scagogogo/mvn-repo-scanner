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

// rulesForLevelFn resolves the rule list for the current --level. Indirected as
// a package-level variable so tests can inject a list containing a disabled
// rule, to cover runRules's "disabled" status branch (the built-in rule sets
// are all enabled, so the branch is otherwise unreachable).
var rulesForLevelFn = func(level string) []*detector.Rule {
	switch level {
	case "core":
		return detector.DefaultRules()
	case "extended":
		return detector.ExtendedRules()
	default:
		return detector.AllRules()
	}
}

func runRules(cmd *cobra.Command, args []string) error {
	rules := rulesForLevelFn(rulesLevel)

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
