package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkspaceAt(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, w)

	assert.Equal(t, filepath.Join(tmpDir, "scan.db"), w.DBPath)
	assert.Equal(t, filepath.Join(tmpDir, "cache"), w.CacheDir)
	assert.Equal(t, filepath.Join(tmpDir, "rules"), w.RulesDir)

	for _, d := range []string{tmpDir, w.CacheDir, w.RulesDir} {
		info, err := os.Stat(d)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	}
}

func TestWorkspace_CacheUsageMB(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	usage, err := w.CacheUsageMB()
	require.NoError(t, err)
	assert.Equal(t, 0, usage)

	data := make([]byte, 2*1024*1024)
	err = os.WriteFile(filepath.Join(w.CacheDir, "test.bin"), data, 0644)
	require.NoError(t, err)

	usage, err = w.CacheUsageMB()
	require.NoError(t, err)
	assert.Equal(t, 2, usage)
}

func TestWorkspace_CleanCache(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	os.WriteFile(filepath.Join(w.CacheDir, "a.bin"), []byte("data"), 0644)
	os.WriteFile(filepath.Join(w.CacheDir, "b.bin"), []byte("data"), 0644)

	err = w.CleanCache()
	require.NoError(t, err)

	entries, _ := os.ReadDir(w.CacheDir)
	assert.Equal(t, 0, len(entries))
}

func TestWorkspace_DBSizeMB_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	size, err := w.DBSizeMB()
	require.NoError(t, err)
	assert.Equal(t, 0, size)
}

func TestNewWorkspace(t *testing.T) {
	// NewWorkspace 用 home 目录，这里通过 HOME 环境变量重定向到临时目录
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w, err := NewWorkspace()
	require.NoError(t, err)
	require.NotNil(t, w)
	assert.Contains(t, w.BaseDir, AppName)

	// 再次调用应幂等（目录已存在）
	w2, err := NewWorkspace()
	require.NoError(t, err)
	assert.Equal(t, w.BaseDir, w2.BaseDir)
}

func TestWorkspace_DBSizeMB_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	// 创建一个 db 文件
	require.NoError(t, os.WriteFile(w.DBPath, make([]byte, 1024), 0644))
	size, err := w.DBSizeMB()
	require.NoError(t, err)
	assert.Equal(t, 0, size) // 1KB < 1MB
}

func TestWorkspace_EnforceCacheLimit_NoOp(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)
	w.CacheMaxMB = 10

	// 写入 2MB 文件，未超限
	require.NoError(t, os.WriteFile(filepath.Join(w.CacheDir, "a.bin"), make([]byte, 2*1024*1024), 0644))
	require.NoError(t, w.EnforceCacheLimit())

	entries, _ := os.ReadDir(w.CacheDir)
	assert.Equal(t, 1, len(entries))
}

func TestWorkspace_EnforceCacheLimit_RemovesOldest(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)
	w.CacheMaxMB = 1 // 限制 1MB

	// 写入 3MB 文件，超限 → 应被删
	require.NoError(t, os.WriteFile(filepath.Join(w.CacheDir, "big.bin"), make([]byte, 3*1024*1024), 0644))
	require.NoError(t, w.EnforceCacheLimit())

	// usage 3MB > 1MB，删 big.bin (3MB)，usage -= 3 → 0MB ≤ 1MB，停。文件应被删
	entries, _ := os.ReadDir(w.CacheDir)
	assert.Equal(t, 0, len(entries))
}

func TestWorkspace_CleanCache_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := NewWorkspaceAt(tmpDir)
	require.NoError(t, err)

	// 空目录，CleanCache 不应报错
	require.NoError(t, w.CleanCache())
}

func TestWorkspace_CacheUsageMB_NonExistentDir(t *testing.T) {
	// CacheDir 不存在时：filepath.Walk 对根 err 调用 walkFn，walkFn 返回 nil，故 Walk 返回 nil、usage=0
	w := &Workspace{
		BaseDir:  "/nonexistent/path/that/does/not/exist",
		CacheDir: "/nonexistent/path/that/does/not/exist/cache",
	}
	usage, err := w.CacheUsageMB()
	require.NoError(t, err)
	assert.Equal(t, 0, usage)
}

func TestNewWorkspaceAt_MkdirFails(t *testing.T) {
	// baseDir 的父路径是一个文件（不是目录）→ MkdirAll 失败
	tmpFile := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0644))
	_, err := NewWorkspaceAt(filepath.Join(tmpFile, "sub"))
	assert.Error(t, err)
}
