package repo

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewHTTPClient_DefaultMaxConns(t *testing.T) {
	// maxConnsPerHost <= 0 → 默认 32（line 20-22 分支）
	c := newHTTPClient(5*time.Second, 0)
	assert.NotNil(t, c)
	assert.Equal(t, 5*time.Second, c.Timeout)
	// transport 应设置 MaxConnsPerHost=32
	tr, ok := c.Transport.(*http.Transport)
	if assert.True(t, ok, "transport should be *http.Transport") {
		assert.Equal(t, 32, tr.MaxConnsPerHost)
	}
}

func TestNewHTTPClient_ExplicitMaxConns(t *testing.T) {
	// maxConnsPerHost 显式 > 0 → 不走默认
	c := newHTTPClient(0, 10)
	tr, ok := c.Transport.(*http.Transport)
	if assert.True(t, ok) {
		assert.Equal(t, 10, tr.MaxConnsPerHost)
	}
}
