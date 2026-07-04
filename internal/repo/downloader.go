package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"
)

// Downloader handles concurrent file downloads with retry support.
type Downloader struct {
	client   *http.Client
	retries  int
	limiter  *rate.Limiter
	tempDir  string
	maxBytes int64 // 0 = no limit
	auth     AuthConfig
}

// DownloadResult holds the result of a single download.
type DownloadResult struct {
	Artifact  Artifact
	LocalPath string
}

// NewDownloader creates a new downloader with the given configuration.
// maxFileSize limits individual file downloads (0 = no limit).
func NewDownloader(timeout time.Duration, retries int, qps int, tempDir string, maxFileSize int64) *Downloader {
	return NewDownloaderWithAuth(timeout, retries, qps, tempDir, maxFileSize, AuthConfig{})
}

// NewDownloaderWithAuth creates a downloader with credentials for private repositories.
func NewDownloaderWithAuth(timeout time.Duration, retries int, qps int, tempDir string, maxFileSize int64, auth AuthConfig) *Downloader {
	var limiter *rate.Limiter
	if qps > 0 {
		limiter = rate.NewLimiter(rate.Limit(qps), qps)
	}

	if tempDir == "" {
		tempDir = os.TempDir()
	}

	return &Downloader{
		client:   newHTTPClient(timeout, 64),
		retries:  retries,
		limiter:  limiter,
		tempDir:  tempDir,
		maxBytes: maxFileSize,
		auth:     auth,
	}
}

// Download downloads a single artifact file with retry logic.
//
// Retries are bounded and error-aware: only transient errors (network blips,
// 5xx, 429) are retried; permanent errors (4xx other than 429, e.g. 404) fail
// fast instead of burning the retry budget. 429 responses honor Retry-After.
// Backoff is exponential with jitter so concurrent workers retrying after a
// shared 429 don't synchronize into a thundering herd.
func (d *Downloader) Download(ctx context.Context, artifact Artifact) (*DownloadResult, error) {
	if d.limiter != nil {
		if err := d.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= d.retries; attempt++ {
		if attempt > 0 {
			backoff := d.backoffFor(attempt-1, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		localPath, err := d.doDownload(ctx, artifact)
		if err == nil {
			return &DownloadResult{Artifact: artifact, LocalPath: localPath}, nil
		}
		lastErr = err

		// Permanent errors (e.g. 404, 403) are not retried — they won't succeed
		// on the next attempt and only waste time/QPS budget.
		if !isRetryable(err) {
			break
		}
	}

	return nil, fmt.Errorf("after %d retries: %w", d.retries, lastErr)
}

// isRetryable reports whether an error is worth retrying. Transient network
// errors and HTTP 5xx / 429 qualify; 4xx (other than 429) do not.
func isRetryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Temporary()
	}
	// Non-HTTP errors (connection reset, TLS handshake, context deadline during
	// a single attempt) are treated as transient. A cancelled context is not,
	// but Download already checks ctx.Done() separately at the top of the loop.
	return true
}

// backoffFor returns the wait duration before the given attempt index (0-based)
// with jitter. For 429 with a Retry-After header, that value takes precedence.
func (d *Downloader) backoffFor(attempt int, lastErr error) time.Duration {
	var httpErr *HTTPError
	if errors.As(lastErr, &httpErr) && httpErr.StatusCode == 429 && httpErr.RetryAfter > 0 {
		return httpErr.RetryAfter
	}
	// Exponential backoff: 1s, 2s, 4s, 8s... capped at 30s, plus up to 50% jitter
	// to de-synchronize workers retrying after the same 429/5xx.
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	return base + jitter
}

// doDownload performs a single download attempt.
func (d *Downloader) doDownload(ctx context.Context, artifact Artifact) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "mvn-repo-scanner/1.0")
	d.auth.apply(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{
			StatusCode: resp.StatusCode,
			URL:        artifact.DownloadURL,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	// Early rejection based on Content-Length header
	if d.maxBytes > 0 && resp.ContentLength > 0 && resp.ContentLength > d.maxBytes {
		return "", fmt.Errorf("file size %d exceeds max file size %d (Content-Length)", resp.ContentLength, d.maxBytes)
	}

	safeName := fmt.Sprintf("%s-%s-%s-%s",
		sanitizePath(artifact.GroupID),
		sanitizePath(artifact.ArtifactID),
		sanitizePath(artifact.Version),
		sanitizePath(artifact.FileName),
	)
	localPath := filepath.Join(d.tempDir, safeName)

	f, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	var written int64
	if d.maxBytes > 0 {
		// Read up to maxBytes+1 — if we get more, the file exceeds the limit
		limited := io.LimitReader(resp.Body, d.maxBytes+1)
		written, err = io.Copy(f, limited)
		if err != nil {
			os.Remove(localPath)
			return "", fmt.Errorf("write file: %w", err)
		}
		if written > d.maxBytes {
			os.Remove(localPath)
			return "", fmt.Errorf("file size exceeds max file size %d", d.maxBytes)
		}
	} else {
		if _, err := io.Copy(f, resp.Body); err != nil {
			os.Remove(localPath)
			return "", fmt.Errorf("write file: %w", err)
		}
	}

	return localPath, nil
}

// Cleanup removes a downloaded temp file.
func (d *Downloader) Cleanup(localPath string) {
	os.Remove(localPath)
}

// sanitizePath replaces dots and unsafe chars for file system paths.
func sanitizePath(s string) string {
	result := filepath.Base(s)
	if result == "." || result == ".." {
		return "unsafe"
	}
	return result
}
