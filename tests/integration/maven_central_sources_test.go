//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leakySourcesJar 构造一个含硬编码泄露的 -sources.jar：
// .java 源码里含 password= 与 ghp_ token，验证 --include-sources 能扫出 .java 泄露。
func leakySourcesJar(t *testing.T) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	javaSrc := []byte(`package com.example;
public class Config {
    // FIXME: remove before release
    private static final String PASSWORD = "HardcodedPassword123!";
    private static final String TOKEN = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789";
}
`)
	fw, err := w.Create("com/example/Config.java")
	require.NoError(t, err)
	_, err = fw.Write(javaSrc)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// startSourcesMockRepo 启动含 leaky-lib-1.0-sources.jar 的仓库。
func startSourcesMockRepo(t *testing.T, sourcesJar []byte) *httptest.Server {
	t.Helper()
	pages := map[string][]byte{
		"/":                                      []byte(`<a href="com/">com/</a>`),
		"/com/":                                  []byte(`<a href="example/">example/</a>`),
		"/com/example/":                          []byte(`<a href="leaky/">leaky/</a>`),
		"/com/example/leaky/":                    []byte(`<a href="1.0/">1.0/</a>`),
		"/com/example/leaky/1.0/":                []byte(`<a href="leaky-1.0-sources.jar">leaky-1.0-sources.jar</a>`),
		"/com/example/leaky/1.0/leaky-1.0-sources.jar": sourcesJar,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestScan_IncludeSources_FindsJavaLeak 验证 --include-sources 扫描 sources jar
// 里的 .java 源码并命中硬编码密码与 GitHub token。
// 不开 --include-sources 时 sources jar 被 discovery 跳过；开启后被扫描并命中泄露。
func TestScan_IncludeSources_FindsJavaLeak(t *testing.T) {
	if testBinaryPath == "" {
		t.Skip("test binary not built")
	}
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "sources.json")
	srv := startSourcesMockRepo(t, leakySourcesJar(t))

	// 不开 --include-sources：sources jar 被 discovery 跳过，0 completed artifact
	cmd1, _ := startScanSubprocess(t, srv.URL, stateFile)
	_ = cmd1.Wait()
	st1 := loadStateJSON(t, stateFile)
	completed1, _ := st1["completed_artifacts"].([]interface{})
	assert.Empty(t, completed1, "sources jar should be skipped without --include-sources")

	// 重置 state，开 --include-sources 重扫：应扫到 sources jar 并命中泄露
	os.Remove(stateFile)
	os.Remove(stateFile + ".bak")
	cmd2, buf := startScanSubprocess(t, srv.URL, stateFile, "--include-sources", "--rules-level", "all")
	done := make(chan error)
	go func() { done <- cmd2.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cmd2.Process.Kill()
		t.Fatal("include-sources scan did not finish in 30s")
	}

	st2 := loadStateJSON(t, stateFile)
	completed2, _ := st2["completed_artifacts"].([]interface{})
	assert.NotEmpty(t, completed2, "sources jar should be scanned with --include-sources")
	// 输出应含命中的泄露（password / ghp_ token / rule id）
	out := buf.String()
	assert.True(t,
		strings.Contains(out, "HardcodedPassword123") || strings.Contains(out, "hardcoded-password") || strings.Contains(out, "ghp_"),
		"include-sources scan should find the hardcoded leak in .java, got output: %s", out)
}

// TestScan_IncludeSources_ResumeValidatesClassifierDrift 验证 resume 时
// classifier 选项不一致会被 ValidateConfig 拒绝（config mismatch）。
func TestScan_IncludeSources_ResumeValidatesClassifierDrift(t *testing.T) {
	if testBinaryPath == "" {
		t.Skip("test binary not built")
	}
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "drift.json")
	srv := startSourcesMockRepo(t, leakySourcesJar(t))

	// 第一次用 --include-sources 跑完
	cmd1, _ := startScanSubprocess(t, srv.URL, stateFile, "--include-sources")
	_ = cmd1.Wait()

	// resume 不带 --include-sources → classifier 漂移 → 应非零退出
	resume, buf := startScanSubprocess(t, srv.URL, stateFile, "--resume")
	_ = resume.Wait()
	assert.NotEqual(t, 0, resume.ProcessState.ExitCode(),
		"resume with classifier drift (include-sources mismatch) should exit non-zero")
	out := strings.ToLower(buf.String())
	assert.True(t,
		strings.Contains(out, "include_sources") || strings.Contains(out, "include-sources") || strings.Contains(out, "config mismatch"),
		"output should mention classifier/config mismatch, got: %s", out)
}
