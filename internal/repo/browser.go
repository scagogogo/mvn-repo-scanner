// Package repo provides Maven repository browsing and artifact downloading over HTTP.
package repo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// AuthConfig holds optional credentials for private Maven repositories.
type AuthConfig struct {
	Username string
	Password string
	Token    string // bearer-style token sent as Authorization header
	// HeaderName/HeaderValue let callers inject an arbitrary auth header
	// (e.g. "X-JFrog-Art-Access-Key": "<key>").
	HeaderName  string
	HeaderValue string
}

// IsSet returns true if any credential is configured.
func (a AuthConfig) IsSet() bool {
	return a.Username != "" || a.Token != "" || (a.HeaderName != "" && a.HeaderValue != "")
}

// apply mutates the request to add configured auth credentials.
func (a AuthConfig) apply(req *http.Request) {
	if a.HeaderName != "" && a.HeaderValue != "" {
		req.Header.Set(a.HeaderName, a.HeaderValue)
	}
	if a.Token != "" && a.HeaderName == "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	if a.Username != "" || a.Password != "" {
		req.SetBasicAuth(a.Username, a.Password)
	}
}

// Artifact represents a discovered artifact in the repository.
type Artifact struct {
	GroupID     string
	ArtifactID  string
	Version     string
	FileName    string
	DownloadURL string
}

// String returns the Maven GAV coordinate in colon-separated format (groupID:artifactID:version).
func (a Artifact) String() string {
	return a.GroupID + ":" + a.ArtifactID + ":" + a.Version
}

// Path returns the Maven repository path in slash-separated format (groupID/artifactID/version).
func (a Artifact) Path() string {
	return strings.ReplaceAll(a.GroupID, ".", "/") + "/" + a.ArtifactID + "/" + a.Version
}

// Browser traverses a Maven repository's directory structure.
type Browser struct {
	client      *http.Client
	groupFilter string
	auth        AuthConfig
	limiter    *rate.Limiter // optional QPS throttle for discovery fetches
}

// NewBrowser creates a new repository browser.
func NewBrowser(timeout time.Duration, groupFilter string) *Browser {
	return &Browser{
		client:      newHTTPClient(timeout, 32),
		groupFilter: groupFilter,
	}
}

// NewBrowserWithAuth creates a browser for a private repository with credentials.
func NewBrowserWithAuth(timeout time.Duration, groupFilter string, auth AuthConfig) *Browser {
	b := NewBrowser(timeout, groupFilter)
	b.auth = auth
	return b
}

// WithLimiter attaches a rate limiter so discovery fetches are throttled. nil
// means unlimited (backward compatible). Returns the browser for chaining.
func (b *Browser) WithLimiter(l *rate.Limiter) *Browser {
	b.limiter = l
	return b
}

// WithMaxConnsPerHost rebuilds the HTTP client with a custom per-host connection
// cap, overriding the default 32. Useful when discovery concurrency is raised.
func (b *Browser) WithMaxConnsPerHost(timeout time.Duration, maxConns int) *Browser {
	b.client = newHTTPClient(timeout, maxConns)
	return b
}

// Discover walks the repository tree and returns all discoverable artifacts.
func (b *Browser) Discover(ctx context.Context, repoURL string) ([]Artifact, error) {
	var artifacts []Artifact
	startPath := repoURL

	// If group filter is set, convert com.example -> com/example
	var initialSegments []string
	if b.groupFilter != "" {
		path := strings.ReplaceAll(b.groupFilter, ".", "/")
		initialSegments = strings.Split(path, "/")
		startPath = strings.TrimRight(repoURL, "/") + "/" + path + "/"
	}

	err := b.walk(ctx, startPath, initialSegments, &artifacts)
	if err != nil {
		return nil, fmt.Errorf("discover artifacts: %w", err)
	}
	return artifacts, nil
}

// walk recursively traverses directory entries.
func (b *Browser) walk(ctx context.Context, url string, pathSegments []string, artifacts *[]Artifact) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	html, err := b.fetchPage(ctx, url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}

	entries := parseHTMLListing(url, html)

	for _, entry := range entries {
		if entry.IsDir {
			newSegments := append([]string{}, pathSegments...)
			newSegments = append(newSegments, entry.Name)
			if err := b.walk(ctx, entry.URL, newSegments, artifacts); err != nil {
				continue
			}
		} else if isArtifactFile(entry.Name) {
			artifact := b.buildArtifact(pathSegments, entry)
			if artifact != nil {
				*artifacts = append(*artifacts, *artifact)
			}
		}
	}
	return nil
}

// buildArtifact creates an Artifact from path segments and entry info.
func (b *Browser) buildArtifact(pathSegments []string, e entry) *Artifact {
	if len(pathSegments) < 3 {
		return nil
	}

	versionIdx := len(pathSegments) - 1
	artifactIdx := versionIdx - 1
	groupParts := pathSegments[:artifactIdx]

	return &Artifact{
		GroupID:     strings.Join(groupParts, "."),
		ArtifactID:  pathSegments[artifactIdx],
		Version:     pathSegments[versionIdx],
		FileName:    e.Name,
		DownloadURL: e.URL,
	}
}

// FetchPage implements PageFetcher. It fetches and returns the HTML content
// of a repository directory page, applying configured auth credentials.
func (b *Browser) FetchPage(ctx context.Context, url string) (string, error) {
	return b.fetchPage(ctx, url)
}

// fetchPage fetches and returns the HTML content of a repository page.
func (b *Browser) fetchPage(ctx context.Context, url string) (string, error) {
	// Discovery QPS throttle: if a limiter is attached, wait for a token before
	// each HTTP fetch so the discovery phase is polite to the repo (shared QPS
	// budget with downloads). nil limiter = unlimited (backward compatible).
	if b.limiter != nil {
		if err := b.limiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("rate limit wait: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "mvn-repo-scanner/1.0")
	b.auth.apply(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: resp.StatusCode, URL: url}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
