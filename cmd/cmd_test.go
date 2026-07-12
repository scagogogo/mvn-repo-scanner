package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/config"
	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
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
	// GetConfig 返回包级 cfg，其初值由 init() 设为 DefaultConfig()。其他测试可能
	// 修改全局 cfg，所以这里只验证 GetConfig 返回非 nil 且与当前 cfg 同一对象，
	// 默认值由 config.DefaultConfig() 直接断言（不依赖全局状态）。
	c := GetConfig()
	require.NotNil(t, c)
	assert.Same(t, cfg, c)
	def := config.DefaultConfig()
	assert.Equal(t, 10, def.Concurrency)
	assert.Equal(t, "core", def.RulesLevel)
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

// Execute() 的 nil 路径：rootCmd.Execute() 成功（--help）→ 不走 err 块。
func TestExecute_NoError(t *testing.T) {
	rootCmd.SetArgs([]string{"--help"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	// osExit 保持 os.Exit，但 nil 路径不调它
	Execute()
}

// Execute() 的 err 路径（line 21-24）：未知命令 → rootCmd.Execute() 返回 err →
// fmt.Fprintln + osExit(1)。替换 osExit 避免真正退出进程。
func TestExecute_ErrorPath(t *testing.T) {
	// 捕获 stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// 替换 osExit 避免终止测试进程
	called := false
	savedExit := osExit
	osExit = func(code int) { called = true; _ = code }
	defer func() { osExit = savedExit }()

	rootCmd.SetArgs([]string{"unknown-command-xyz"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	Execute()

	w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()

	assert.True(t, called, "osExit should be called on rootCmd error")
	assert.Contains(t, buf.String(), "unknown-command-xyz")
}

// runRules 的 disabled rule 分支（line 44-46）：注入 rulesForLevelFn 返回含
// 一条 disabled 规则的列表（内置规则全 enabled，分支否则不可达）。
func TestRulesCmd_DisabledRule(t *testing.T) {
	saved := rulesForLevelFn
	rulesForLevelFn = func(level string) []*detector.Rule {
		return []*detector.Rule{
			{ID: "on-rule", Name: "On Rule", Severity: detector.SeverityHigh, Enabled: true},
			{ID: "off-rule", Name: "Off Rule", Severity: detector.SeverityMedium, Enabled: false},
		}
	}
	t.Cleanup(func() { rulesForLevelFn = saved })

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"rules", "-l", "core"})
		err := rootCmd.Execute()
		require.NoError(t, err)
	})
	assert.Contains(t, output, "disabled")
	assert.Contains(t, output, "off-rule")
}