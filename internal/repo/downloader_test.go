package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloader_Download_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test content"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 2, 0, tmpDir, 0)

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "test-lib",
		Version: "1.0", FileName: "test-lib-1.0.jar",
		DownloadURL: server.URL + "/test-lib-1.0.jar",
	}

	result, err := d.Download(context.Background(), artifact)
	require.NoError(t, err)
	assert.Contains(t, result.LocalPath, tmpDir)

	content, err := os.ReadFile(result.LocalPath)
	require.NoError(t, err)
	assert.Equal(t, "test content", string(content))

	d.Cleanup(result.LocalPath)
}

func TestDownloader_Download_Retry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 3, 0, tmpDir, 0)

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	result, err := d.Download(context.Background(), artifact)
	require.NoError(t, err)
	assert.Equal(t, "success", readFile(t, result.LocalPath))
	assert.True(t, callCount >= 3)
	d.Cleanup(result.LocalPath)
}

func TestDownloader_Download_AllRetriesFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(5*time.Second, 1, 0, tmpDir, 0)

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	_, err := d.Download(context.Background(), artifact)
	assert.Error(t, err)
}

func TestDownloader_Download_ExceedsMaxFileSize_ContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 0, 0, tmpDir, 1024) // max 1KB

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	_, err := d.Download(context.Background(), artifact)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max file size")
}

func TestDownloader_Download_ExceedsMaxFileSize_Streaming(t *testing.T) {
	// Server sends 2KB body without Content-Length header (chunked transfer)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 0, 0, tmpDir, 1024) // max 1KB

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	_, err := d.Download(context.Background(), artifact)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max file size")

	// Temp file should have been removed
	entries, _ := os.ReadDir(tmpDir)
	assert.Equal(t, 0, len(entries))
}

func TestDownloader_Download_WithinMaxFileSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("small content"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 0, 0, tmpDir, 1024*1024) // max 1MB

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	result, err := d.Download(context.Background(), artifact)
	require.NoError(t, err)
	assert.Equal(t, "small content", readFile(t, result.LocalPath))
	d.Cleanup(result.LocalPath)
}

func TestDownloader_Download_ZeroMaxFileSize_NoLimit(t *testing.T) {
	// maxBytes=0 means no limit — even large files should succeed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(10*time.Second, 0, 0, tmpDir, 0) // no limit

	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}

	result, err := d.Download(context.Background(), artifact)
	require.NoError(t, err)
	assert.Equal(t, 4096, len(readFile(t, result.LocalPath)))
	d.Cleanup(result.LocalPath)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
