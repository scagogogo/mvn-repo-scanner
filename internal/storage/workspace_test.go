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
