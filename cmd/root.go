package cmd

import (
	"fmt"
	"os"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfg *config.Config

var rootCmd = &cobra.Command{
	Use:   "mvn-repo-scanner",
	Short: "Maven repository sensitive content scanner",
	Long:  "A CLI tool to scan Maven repositories (central or private) for sensitive content like passwords, API keys, and certificates.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cfg = config.DefaultConfig()

	rootCmd.PersistentFlags().BoolVarP(&cfg.Verbose, "verbose", "v", false, "verbose output")

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.mvn-repo-scanner")
}

// GetConfig returns the current application config.
func GetConfig() *config.Config {
	return cfg
}
