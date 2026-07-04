package repo

import (
	"fmt"
	"strconv"
	"time"
)

// HTTPError represents an HTTP error with a status code and URL.
type HTTPError struct {
	StatusCode int
	URL        string
	// RetryAfter is parsed from the Retry-After header on 429/503 responses;
	// zero means the header was absent or unparseable.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.URL)
}

// Temporary returns true if the error is likely transient (5xx or 429).
func (e *HTTPError) Temporary() bool {
	return e.StatusCode >= 500 || e.StatusCode == 429
}

// parseRetryAfter parses a Retry-After header value, which may be either an
// integer number of seconds or an HTTP-date. Returns 0 on any parse failure
// or if delay would exceed 5 minutes (defensive cap against a hostile server
// telling us to wait forever).
func parseRetryAfter(value string) time.Duration {
	value = trimHTTPWhitespace(value)
	if value == "" {
		return 0
	}
	// Try delta-seconds form first (most common).
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		d := time.Duration(secs) * time.Second
		if d > 5*time.Minute {
			return 5 * time.Minute
		}
		return d
	}
	// HTTP-date form. Ignore parse errors; fall back to 0 (caller will back off).
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		d := time.Until(t)
		if d > 5*time.Minute {
			return 5 * time.Minute
		}
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// trimHTTPWhitespace trims leading/trailing spaces and tabs (not newlines, to
// match HTTP field semantics where OWS is space/tab only).
func trimHTTPWhitespace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}