// Package config defines application configuration and validation for the Maven repository scanner.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration.
type Config struct {
	RepoURL             string        `mapstructure:"repo"`
	GroupFilter         string        `mapstructure:"group"`
	Concurrency         int           `mapstructure:"concurrency"`
	QPS                 int           `mapstructure:"qps"`
	Resume              bool          `mapstructure:"resume"`
	StateFile           string        `mapstructure:"state-file"`
	RulesFile           string        `mapstructure:"rules"`
	Output              string        `mapstructure:"output"`
	OutputFile          string        `mapstructure:"output-file"`
	MaxFileSize         string        `mapstructure:"max-file-size"`
	Timeout             time.Duration `mapstructure:"timeout"`
	Retries             int           `mapstructure:"retries"`
	Exclude             []string      `mapstructure:"exclude"`
	Verbose             bool          `mapstructure:"verbose"`
	CheckpointInterval  int           `mapstructure:"checkpoint-interval"`
	RulesLevel          string        `mapstructure:"rules-level"`
	RulesMerge          bool          `mapstructure:"rules-merge"`
	DownloadConcurrency int           `mapstructure:"download-concurrency"`
	ScanConcurrency     int           `mapstructure:"scan-concurrency"`
	DiskBudgetMB        int           `mapstructure:"disk-budget"`
	RetryFailed         bool          `mapstructure:"retry-failed"`
	Rediscover          bool          `mapstructure:"rediscover"`
	// Private repository auth
	AuthUsername string `mapstructure:"auth-username"`
	AuthPassword string `mapstructure:"auth-password"`
	AuthToken    string `mapstructure:"auth-token"`
	// Scan interval for recurring/periodic scans (0 = one-shot)
	ScanInterval time.Duration `mapstructure:"scan-interval"`
	// TaskID associates this run with a persisted task (for interval scheduling)
	TaskID string `mapstructure:"task-id"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		RepoURL:            "https://repo.maven.apache.org/maven2",
		Concurrency:        10,
		QPS:                0,
		Resume:             false,
		StateFile:          ".mvn-scan-state.json",
		Output:             "console",
		MaxFileSize:        "50MB",
		Timeout:            30 * time.Second,
		Retries:            3,
		Verbose:            false,
		CheckpointInterval: 50,
		RulesLevel:          "core",
		DownloadConcurrency: 0,
		ScanConcurrency:     0, // 0 = fall back to Concurrency (CPU-bound scan decoupled from IO-bound download)
		DiskBudgetMB:        1000, // 1GB default disk budget for temp files
	}
}

// Validate checks that the Config fields contain valid values.
func (c *Config) Validate() error {
	if c.RepoURL == "" {
		return fmt.Errorf("repo URL is required")
	}
	if !strings.HasPrefix(c.RepoURL, "http://") && !strings.HasPrefix(c.RepoURL, "https://") {
		return fmt.Errorf("repo URL must start with http:// or https://")
	}
	if c.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be greater than 0")
	}
	if c.QPS < 0 {
		return fmt.Errorf("QPS must be non-negative")
	}
	if c.Retries < 0 {
		return fmt.Errorf("retries must be non-negative")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than 0")
	}
	if c.CheckpointInterval < 0 {
		return fmt.Errorf("checkpoint-interval must be >= 0 (0 means save every artifact)")
	}
	validRulesLevels := map[string]bool{"core": true, "extended": true, "all": true}
	if !validRulesLevels[c.RulesLevel] {
		return fmt.Errorf("rules level must be one of core, extended, all; got %q", c.RulesLevel)
	}
	if c.DownloadConcurrency < 0 {
		return fmt.Errorf("download concurrency must be non-negative")
	}
	if c.ScanConcurrency < 0 {
		return fmt.Errorf("scan concurrency must be non-negative (0 = use concurrency)")
	}
	if c.DiskBudgetMB < 0 {
		return fmt.Errorf("disk budget must be non-negative")
	}
	if _, err := c.ParseMaxFileSize(); err != nil {
		return fmt.Errorf("invalid max file size: %w", err)
	}
	return nil
}

// ParseMaxFileSize parses a human-readable file size string (e.g. "50MB", "10KB")
// and returns the size in bytes. An empty string means no limit and returns 0.
func (c *Config) ParseMaxFileSize() (int64, error) {
	s := strings.TrimSpace(c.MaxFileSize)
	if s == "" {
		return 0, nil
	}

	s = strings.ToUpper(s)

	var multiplier int64
	var numPart string

	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1 << 30
		numPart = s[:len(s)-2]
	case strings.HasSuffix(s, "MB"):
		multiplier = 1 << 20
		numPart = s[:len(s)-2]
	case strings.HasSuffix(s, "KB"):
		multiplier = 1 << 10
		numPart = s[:len(s)-2]
	case strings.HasSuffix(s, "B"):
		multiplier = 1
		numPart = s[:len(s)-1]
	default:
		return 0, fmt.Errorf("invalid max file size %q: missing unit suffix (B, KB, MB, GB)", c.MaxFileSize)
	}

	num, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid max file size %q: %w", c.MaxFileSize, err)
	}
	return num * multiplier, nil
}
