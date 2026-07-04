package repo

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHTTPError_Error(t *testing.T) {
	err := &HTTPError{StatusCode: 404, URL: "https://example.com/file.jar"}
	assert.Equal(t, "HTTP 404: https://example.com/file.jar", err.Error())
}

func TestHTTPError_Temporary(t *testing.T) {
	assert.True(t, (&HTTPError{StatusCode: 500}).Temporary())
	assert.True(t, (&HTTPError{StatusCode: 503}).Temporary())
	assert.True(t, (&HTTPError{StatusCode: 429}).Temporary())
	assert.False(t, (&HTTPError{StatusCode: 404}).Temporary())
	assert.False(t, (&HTTPError{StatusCode: 403}).Temporary())
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int // seconds, -1 = "don't care, just >0"
	}{
		{"empty", "", 0},
		{"seconds", "30", 30},
		{"zero", "0", 0},
		{"capped at 5min", "99999", 300},
		{"negative treated as zero", "-5", 0},
		{"garbage", "not-a-date", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.in)
			if tt.want == 0 {
				assert.Equal(t, time.Duration(0), got)
			} else {
				assert.Equal(t, time.Duration(tt.want)*time.Second, got)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	// Transient HTTP errors retry.
	assert.True(t, isRetryable(&HTTPError{StatusCode: 500}))
	assert.True(t, isRetryable(&HTTPError{StatusCode: 429}))
	assert.True(t, isRetryable(&HTTPError{StatusCode: 503}))
	// Permanent HTTP errors fail fast.
	assert.False(t, isRetryable(&HTTPError{StatusCode: 404}))
	assert.False(t, isRetryable(&HTTPError{StatusCode: 403}))
	// Non-HTTP errors (network) are treated as transient.
	assert.True(t, isRetryable(fmt.Errorf("connection reset")))
}