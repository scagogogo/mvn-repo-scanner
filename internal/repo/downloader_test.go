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

func TestDownloader_Download_PermanentErrorBreaks(t *testing.T) {
	// 404 不可重试 → isRetryable=false → break（line 94-96），不耗尽 retry
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusNotFound) // 404 永久错误
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(5*time.Second, 3, 0, tmpDir, 0)
	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}
	_, err := d.Download(context.Background(), artifact)
	assert.Error(t, err)
	// 永久错误只调用一次就 break，不会重试 3 次
	assert.Equal(t, 1, callCount, "404 should not be retried")
}

func TestDownloader_Download_CanceledDuringBackoff(t *testing.T) {
	// 首次 503 失败 → 进入 retry 退避 → ctx 取消 → return nil, ctx.Err()（line 80-81）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	d := NewDownloader(5*time.Second, 5, 0, tmpDir, 0)
	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: server.URL + "/lib-1.0.jar",
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := d.Download(ctx, artifact)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDownloader_Download_CopyFailsWithMaxBytes(t *testing.T) {
	// server 返回 200 + chunked body，写入中途 hijack 关闭连接 → io.Copy 失败
	// 配合 maxBytes>0 → 走 line 179-182（maxBytes 分支的 io.Copy err）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, _ := hj.Hijack()
		defer conn.Close()
		// 声明 0x64=100 字节 chunk 但只发 10 字节后关闭
		buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n64\r\n")
		buf.WriteString(strings.Repeat("x", 10))
		buf.Flush()
	}))
	defer srv.Close()
	tmpDir := t.TempDir()
	// retries=0 避免重试拖延；maxBytes=1MB 进入 maxBytes 分支
	d := NewDownloader(2*time.Second, 0, 0, tmpDir, 1024*1024)
	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: srv.URL + "/lib-1.0.jar",
	}
	_, err := d.Download(context.Background(), artifact)
	assert.Error(t, err)
}

func TestDownloader_Download_CopyFailsNoMaxBytes(t *testing.T) {
	// 同上但 maxBytes=0 → 走 line 188-191（无 maxBytes 分支的 io.Copy err）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, _ := hj.Hijack()
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n64\r\n")
		buf.WriteString(strings.Repeat("x", 10))
		buf.Flush()
	}))
	defer srv.Close()
	tmpDir := t.TempDir()
	d := NewDownloader(2*time.Second, 0, 0, tmpDir, 0) // maxBytes=0
	artifact := Artifact{
		GroupID: "com.example", ArtifactID: "lib",
		Version: "1.0", FileName: "lib-1.0.jar",
		DownloadURL: srv.URL + "/lib-1.0.jar",
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

func TestDownloader_Download_ExceedsMaxFileSize_ChunkedBody(t *testing.T) {
	// 强制 chunked（无 Content-Length）：分多次 Flush 写入 → 走 written > maxBytes 分支（line 183-186）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		// 写超过 maxBytes 的数据，分块 flush 避免 server 推断 Content-Length
		for i := 0; i < 4; i++ {
			w.Write([]byte(strings.Repeat("x", 512)))
			if fl != nil {
				fl.Flush()
			}
		}
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
	entries, _ := os.ReadDir(tmpDir)
	assert.Equal(t, 0, len(entries), "temp file should be removed on size exceeded")
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

func TestNewDownloaderWithAuth_EmptyTempDir(t *testing.T) {
	// tempDir="" → 回退到 os.TempDir()
	d := NewDownloaderWithAuth(0, 0, 0, "", 0, AuthConfig{Token: "t"}, 0)
	assert.Equal(t, os.TempDir(), d.tempDir)
	assert.True(t, d.auth.IsSet())
}

func TestNewDownloaderWithAuth_QPSLimiter(t *testing.T) {
	// qps>0 → 创建 limiter
	d := NewDownloaderWithAuth(0, 0, 5, "", 0, AuthConfig{}, 0)
	require.NotNil(t, d.limiter)
}

// TestNewDownloaderWithAuth_MaxConnsPerHost 验证 maxConnsPerHost 被应用到 transport。
func TestNewDownloaderWithAuth_MaxConnsPerHost(t *testing.T) {
	d := NewDownloaderWithAuth(0, 0, 0, "", 0, AuthConfig{}, 96)
	tr, ok := d.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 96, tr.MaxConnsPerHost)
}

func TestDownloader_BackoffFor_429RetryAfter(t *testing.T) {
	d := NewDownloader(0, 0, 0, t.TempDir(), 0)
	// 429 + RetryAfter=10s → 直接返回 10s
	got := d.backoffFor(0, &HTTPError{StatusCode: 429, RetryAfter: 10 * time.Second})
	assert.Equal(t, 10*time.Second, got)
	// 非 429 错误 → 指数退避，attempt=0 → base=1s，jitter 0~0.5s
	got2 := d.backoffFor(0, &HTTPError{StatusCode: 500})
	assert.True(t, got2 >= time.Second && got2 < 1500*time.Millisecond)
}

func TestDownloader_BackoffFor_CappedAt30s(t *testing.T) {
	// attempt=6 → 2^6=64s > 30s → base 被 cap 到 30s（line 125-127）
	d := NewDownloader(0, 0, 0, t.TempDir(), 0)
	got := d.backoffFor(6, &HTTPError{StatusCode: 500})
	// base=30s + jitter[0,15s) → [30s, 45s)
	assert.True(t, got >= 30*time.Second && got < 45*time.Second, "got %v", got)
}

func TestDownloader_SanitizePath(t *testing.T) {
	assert.Equal(t, "unsafe", sanitizePath("."))
	assert.Equal(t, "unsafe", sanitizePath(".."))
	assert.Equal(t, "lib-1.0.jar", sanitizePath("lib-1.0.jar"))
}

func TestDownloader_DoDownload_BadURL(t *testing.T) {
	d := NewDownloader(0, 0, 0, t.TempDir(), 0)
	// 非法 URL → NewRequestWithContext 失败
	_, err := d.doDownload(context.Background(), Artifact{
		GroupID: "com", ArtifactID: "x", Version: "1", FileName: "x-1.jar",
		DownloadURL: "http://[::1]:named:invalid",
	})
	assert.Error(t, err)
}

func TestDownloader_DoDownload_CreateFails(t *testing.T) {
	// tempDir 不可写 → os.Create 失败
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	d := NewDownloader(0, 0, 0, "/proc/cannot/create/file/here", 0)
	_, err := d.Download(context.Background(), Artifact{
		GroupID: "com", ArtifactID: "x", Version: "1", FileName: "x-1.jar",
		DownloadURL: srv.URL + "/x-1.jar",
	})
	assert.Error(t, err)
}

func TestDownloader_Download_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := NewDownloader(0, 0, 0, t.TempDir(), 0)
	_, err := d.Download(ctx, Artifact{
		GroupID: "com", ArtifactID: "x", Version: "1", FileName: "x-1.jar",
		DownloadURL: srv.URL + "/x-1.jar",
	})
	assert.Error(t, err)
}

func TestDownloader_Download_RateLimiterError(t *testing.T) {
	// qps limiter + 已取消的 ctx → limiter.Wait 立即失败
	d := NewDownloader(0, 0, 1, t.TempDir(), 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Download(ctx, Artifact{DownloadURL: "http://x"})
	assert.Error(t, err)
}
