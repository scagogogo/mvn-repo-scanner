package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTMLListingParsing(t *testing.T) {
	html := `<html><body>
		<a href="../">../</a>
		<a href="com/">com/</a>
		<a href="org/">org/</a>
		<a href="maven-metadata.xml">maven-metadata.xml</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/maven2/", html)

	assert.Equal(t, 3, len(entries), "should parse 3 entries (excluding ../)")
	assert.Equal(t, "com", entries[0].Name)
	assert.True(t, entries[0].IsDir)
	assert.Equal(t, "maven-metadata.xml", entries[2].Name)
	assert.False(t, entries[2].IsDir)
}

func TestHTMLListingParsing_Empty(t *testing.T) {
	entries := parseHTMLListing("https://example.com/", "")
	assert.Equal(t, 0, len(entries))
}

func TestArtifactFileDetection(t *testing.T) {
	assert.True(t, isArtifactFile("lib-1.0.jar"))
	assert.True(t, isArtifactFile("lib-1.0.pom"))
	assert.True(t, isArtifactFile("lib-1.0.war"))
	assert.False(t, isArtifactFile("lib-1.0-sources.jar.sha1"))
	assert.True(t, isArtifactFile("maven-metadata.xml")) // .xml is an artifact file
}

func TestBuildArtifact(t *testing.T) {
	b := &Browser{}
	entry := entry{Name: "mylib-1.0.jar", IsDir: false, URL: "https://repo.example.com/com/example/mylib/1.0/mylib-1.0.jar"}
	artifact := b.buildArtifact([]string{"com", "example", "mylib", "1.0"}, entry)

	assert.NotNil(t, artifact)
	assert.Equal(t, "com.example", artifact.GroupID)
	assert.Equal(t, "mylib", artifact.ArtifactID)
	assert.Equal(t, "1.0", artifact.Version)
}

func TestArtifact_String(t *testing.T) {
	a := Artifact{GroupID: "com.example", ArtifactID: "mylib", Version: "1.0"}
	assert.Equal(t, "com.example:mylib:1.0", a.String())
}

func TestArtifact_Path(t *testing.T) {
	a := Artifact{GroupID: "com.example", ArtifactID: "mylib", Version: "1.0"}
	assert.Equal(t, "com/example/mylib/1.0", a.Path())
}

func TestAuthConfig_IsSet(t *testing.T) {
	assert.False(t, AuthConfig{}.IsSet())
	assert.True(t, AuthConfig{Username: "u"}.IsSet())
	assert.True(t, AuthConfig{Token: "t"}.IsSet())
	assert.False(t, AuthConfig{HeaderName: "X"}.IsSet())              // HeaderName 但无 Value
	assert.False(t, AuthConfig{HeaderValue: "v"}.IsSet())            // Value 但无 HeaderName
	assert.True(t, AuthConfig{HeaderName: "X", HeaderValue: "v"}.IsSet())
}

func TestAuthConfig_apply(t *testing.T) {
	t.Run("bearer token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://x.example.com", nil)
		AuthConfig{Token: "abc"}.apply(req)
		assert.Equal(t, "Bearer abc", req.Header.Get("Authorization"))
	})
	t.Run("basic auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://x.example.com", nil)
		AuthConfig{Username: "u", Password: "p"}.apply(req)
		user, pass, ok := req.BasicAuth()
		require.True(t, ok)
		assert.Equal(t, "u", user)
		assert.Equal(t, "p", pass)
	})
	t.Run("custom header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://x.example.com", nil)
		AuthConfig{HeaderName: "X-Key", HeaderValue: "v", Token: "ignored"}.apply(req)
		assert.Equal(t, "v", req.Header.Get("X-Key"))
		// HeaderName 设置时 Token 不走 Bearer
		assert.Equal(t, "", req.Header.Get("Authorization"))
	})
}

// 用本地 HTTP server 模拟一个最小 Maven 仓库目录树，覆盖
// NewBrowser/NewBrowserWithAuth/Discover/walk/FetchPage/fetchPage。
func TestBrowser_Discover(t *testing.T) {
	pages := map[string]string{
		"/":             `<a href="../">../</a><a href="com/">com/</a>`,
		"/com/":         `<a href="../">../</a><a href="example/">example/</a>`,
		"/com/example/": `<a href="../">../</a><a href="lib/">lib/</a>`,
		"/com/example/lib/":         `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		"/com/example/lib/1.0/":     `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a><a href="lib-1.0.pom">lib-1.0.pom</a>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		html, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html))
	}))
	defer srv.Close()

	b := NewBrowser(0, "") // 无 groupFilter，从 root 开始
	arts, err := b.Discover(context.Background(), srv.URL+"/")
	require.NoError(t, err)
	require.Len(t, arts, 2)
	assert.Equal(t, "com.example", arts[0].GroupID)
	assert.Equal(t, "lib", arts[0].ArtifactID)
	assert.Equal(t, "1.0", arts[0].Version)
}

func TestBrowser_Discover_WithGroupFilter(t *testing.T) {
	pages := map[string]string{
		"/com/example/":        `<a href="../">../</a><a href="lib/">lib/</a>`,
		"/com/example/lib/":    `<a href="../">../</a><a href="1.0/">1.0/</a>`,
		"/com/example/lib/1.0/": `<a href="../">../</a><a href="lib-1.0.jar">lib-1.0.jar</a>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		html, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html))
	}))
	defer srv.Close()

	b := NewBrowser(0, "com.example")
	arts, err := b.Discover(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, "lib-1.0.jar", arts[0].FileName)
}

func TestBrowser_Discover_FetchError(t *testing.T) {
	// server 直接返回 500 → fetchPage 返回 HTTPError → walk 返回错误 → Discover 返回 error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewBrowser(0, "")
	_, err := b.Discover(context.Background(), srv.URL+"/")
	assert.Error(t, err)
}

func TestBrowser_Discover_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<a href="com/">com/</a>`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 提前取消

	b := NewBrowser(0, "")
	_, err := b.Discover(ctx, srv.URL+"/")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestBrowser_FetchPage_BadURL(t *testing.T) {
	b := NewBrowser(0, "")
	// 非法 URL → NewRequestWithContext 失败
	_, err := b.FetchPage(context.Background(), "http://[::1]:named:invalid")
	assert.Error(t, err)
}

func TestNewBrowserWithAuth(t *testing.T) {
	b := NewBrowserWithAuth(0, "com.example", AuthConfig{Token: "t"})
	require.NotNil(t, b)
	assert.Equal(t, "com.example", b.groupFilter)
	assert.True(t, b.auth.IsSet())
}

func TestBrowser_buildArtifact_TooFewSegments(t *testing.T) {
	b := &Browser{}
	// pathSegments < 3 → 返回 nil
	assert.Nil(t, b.buildArtifact([]string{"com", "example"}, entry{Name: "x.jar"}))
}

func TestBrowser_FetchPage_UnreachableHost(t *testing.T) {
	// 合法 URL 但不可达 → client.Do 失败（line 170-172）
	b := NewBrowser(0, "")
	// 端口 1 通常无服务监听，连接被拒绝
	_, err := b.FetchPage(context.Background(), "http://127.0.0.1:1/page")
	assert.Error(t, err)
}

func TestBrowser_FetchPage_ReadBodyError(t *testing.T) {
	// server 返回 200 但 chunked body 写一半关闭连接 → io.ReadAll 失败（line 180-182）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, _ := hj.Hijack()
		defer conn.Close()
		// 写一个不完整的 chunked 响应：声明 100 字节 chunk 但只发 10 字节后关闭
		buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n64\r\n")
		buf.WriteString(strings.Repeat("x", 10)) // 声明 100 (0x64) 但只发 10
		buf.Flush()
	}))
	defer srv.Close()
	b := NewBrowser(0, "")
	_, err := b.FetchPage(context.Background(), srv.URL+"/x")
	assert.Error(t, err)
}

func TestBrowser_Walk_SubdirFetchFailsContinues(t *testing.T) {
	// 顶层目录列出两个子目录：一个可 fetch（含深层 artifact），一个不可 fetch
	// 子目录 walk 失败 → continue 跳过（line 122-124），整体 Discover 不报错
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(200)
			w.Write([]byte(`<a href="good/">good/</a><a href="bad/">bad/</a>`))
		case "/good/":
			w.WriteHeader(200)
			w.Write([]byte(`<a href="good-1.0.jar">good-1.0.jar</a>`))
		case "/bad/":
			// 返回 500 → fetchPage 报 HTTPError → walk 子调用返回 err → continue
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	b := NewBrowserWithAuth(0, "", AuthConfig{})
	// bad 子目录 fetch 失败被 continue 跳过，Discover 仍正常返回（不报错）
	arts, err := b.Discover(context.Background(), srv.URL)
	require.NoError(t, err)
	// good 子目录虽无完整 GAV 段（buildArtifact 返回 nil），但 walk continue 分支已被触发
	_ = arts
}
