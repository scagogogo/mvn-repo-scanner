package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.NotNil(t, cfg)
	assert.Equal(t, "https://repo.maven.apache.org/maven2", cfg.RepoURL)
	assert.Equal(t, 10, cfg.Concurrency)
	assert.Equal(t, 0, cfg.QPS)
	assert.False(t, cfg.Resume)
	assert.Equal(t, ".mvn-scan-state.json", cfg.StateFile)
	assert.Equal(t, "", cfg.RulesFile)
	assert.Equal(t, "console", cfg.Output)
	assert.Equal(t, "50MB", cfg.MaxFileSize)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	assert.Equal(t, 3, cfg.Retries)
	assert.False(t, cfg.Verbose)
	assert.Equal(t, 50, cfg.CheckpointInterval)
	assert.Equal(t, "core", cfg.RulesLevel)
	assert.Equal(t, 0, cfg.DownloadConcurrency)
}

func TestDefaultConfig_ConcurrencyPositive(t *testing.T) {
	cfg := DefaultConfig()
	assert.Greater(t, cfg.Concurrency, 0, "concurrency should be positive")
}

func TestDefaultConfig_TimeoutPositive(t *testing.T) {
	cfg := DefaultConfig()
	assert.Greater(t, cfg.Timeout, time.Duration(0), "timeout should be positive")
}

func TestDefaultConfig_RetriesNonNegative(t *testing.T) {
	cfg := DefaultConfig()
	assert.GreaterOrEqual(t, cfg.Retries, 0, "retries should be non-negative")
}

func TestDefaultConfig_RulesLevelValid(t *testing.T) {
	cfg := DefaultConfig()
	validLevels := map[string]bool{"core": true, "extended": true, "all": true}
	assert.True(t, validLevels[cfg.RulesLevel], "default rules level should be valid")
}

func TestDefaultConfig_CheckpointIntervalPositive(t *testing.T) {
	cfg := DefaultConfig()
	assert.GreaterOrEqual(t, cfg.CheckpointInterval, 0, "checkpoint interval should be non-negative")
}

func TestConfig_Validate_ValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_EmptyRepoURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RepoURL = ""
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_ZeroConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Concurrency = 0
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_NegativeRetries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Retries = -1
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_ZeroTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timeout = 0
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_InvalidRulesLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RulesLevel = "invalid"
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_NegativeDownloadConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DownloadConcurrency = -1
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_InvalidMaxFileSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFileSize = "abc"
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_CheckpointIntervalZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CheckpointInterval = 0
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_CheckpointIntervalNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CheckpointInterval = -1
	assert.Error(t, cfg.Validate())
}

func TestDefaultConfig_DiskBudgetMB(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 1000, cfg.DiskBudgetMB)
}

func TestConfig_Validate_NegativeDiskBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiskBudgetMB = -1
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_ZeroDiskBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiskBudgetMB = 0
	assert.NoError(t, cfg.Validate())
}

func TestConfig_ParseMaxFileSize_MB(t *testing.T) {
	cfg := &Config{MaxFileSize: "50MB"}
	size, err := cfg.ParseMaxFileSize()
	assert.NoError(t, err)
	assert.Equal(t, int64(52428800), size)
}

func TestConfig_ParseMaxFileSize_KB(t *testing.T) {
	cfg := &Config{MaxFileSize: "10KB"}
	size, err := cfg.ParseMaxFileSize()
	assert.NoError(t, err)
	assert.Equal(t, int64(10240), size)
}

func TestConfig_ParseMaxFileSize_GB(t *testing.T) {
	cfg := &Config{MaxFileSize: "1GB"}
	size, err := cfg.ParseMaxFileSize()
	assert.NoError(t, err)
	assert.Equal(t, int64(1073741824), size)
}

func TestConfig_ParseMaxFileSize_Bytes(t *testing.T) {
	cfg := &Config{MaxFileSize: "1024B"}
	size, err := cfg.ParseMaxFileSize()
	assert.NoError(t, err)
	assert.Equal(t, int64(1024), size)
}

func TestConfig_ParseMaxFileSize_Empty(t *testing.T) {
	cfg := &Config{MaxFileSize: ""}
	size, err := cfg.ParseMaxFileSize()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), size)
}

func TestConfig_ParseMaxFileSize_Invalid(t *testing.T) {
	cfg := &Config{MaxFileSize: "abc"}
	size, err := cfg.ParseMaxFileSize()
	assert.Error(t, err)
	assert.Equal(t, int64(0), size)
}

func TestConfig_ParseMaxFileSize_NoSuffix(t *testing.T) {
	cfg := &Config{MaxFileSize: "100"}
	size, err := cfg.ParseMaxFileSize()
	assert.Error(t, err)
	assert.Equal(t, int64(0), size)
}
