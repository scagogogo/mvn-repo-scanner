package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout temporarily redirects os.Stdout and returns the captured output.
// This is needed because version and rules commands write directly via fmt.Printf/os.Stdout
// rather than through Cobra's command output writer.
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

func TestRootCmd_Help(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "mvn-repo-scanner")
	assert.Contains(t, output, "Usage:")
}

func TestVersionCmd(t *testing.T) {
	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"version"})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	assert.Contains(t, output, "mvn-repo-scanner")
	assert.Contains(t, output, "v0.1.0")
}

func TestRulesCmd_Core(t *testing.T) {
	// Reset rules level flag to avoid state leakage from previous tests
	rulesCmd.Flags().Set("level", "core")

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"rules", "-l", "core"})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	assert.Contains(t, output, "hardcoded-password")
	assert.Contains(t, output, "6 rules")
}

func TestRulesCmd_All(t *testing.T) {
	// Reset rules level flag to avoid state leakage from previous tests
	rulesCmd.Flags().Set("level", "all")

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"rules", "-l", "all"})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	assert.Contains(t, output, "38 rules")
}

func TestRulesCmd_Extended(t *testing.T) {
	// Reset rules level flag to avoid state leakage from previous tests
	rulesCmd.Flags().Set("level", "extended")

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"rules", "-l", "extended"})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})

	assert.Contains(t, output, "32 rules")
}

func TestScanCmd_Help(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"scan", "--help"})
	err := rootCmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "scan")
	assert.Contains(t, output, "--repo")
	assert.Contains(t, output, "--group")
	assert.Contains(t, output, "--concurrency")
	assert.Contains(t, output, "--rules-level")
}

func TestGetConfig(t *testing.T) {
	cfg := GetConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 10, cfg.Concurrency)
	assert.Equal(t, "core", cfg.RulesLevel)
}

func TestScanCmd_HelpShowsFlags(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"scan", "--help"})
	err := rootCmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.True(t, strings.Contains(output, "--repo") || strings.Contains(output, "scan"),
		"scan help should mention repo or scan")
}