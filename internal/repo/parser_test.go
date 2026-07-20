package repo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTMLListingParsing_NestedDirs(t *testing.T) {
	html := `<html><body>
		<a href="../">../</a>
		<a href="com/">com/</a>
		<a href="org/apache/">org/apache/</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/maven2/", html)
	assert.Equal(t, 2, len(entries))
	assert.Equal(t, "com", entries[0].Name)
	assert.True(t, entries[0].IsDir)
	assert.Equal(t, "https://repo.example.com/maven2/com/", entries[0].URL)

	assert.Equal(t, "org/apache", entries[1].Name)
	assert.True(t, entries[1].IsDir)
}

func TestHTMLListingParsing_MultipleArtifacts(t *testing.T) {
	html := `<html><body>
		<a href="mylib-1.0.jar">mylib-1.0.jar</a>
		<a href="mylib-1.0.pom">mylib-1.0.pom</a>
		<a href="mylib-1.0-sources.jar">mylib-1.0-sources.jar</a>
		<a href="mylib-1.0.jar.sha1">mylib-1.0.jar.sha1</a>
		<a href="mylib-1.0.jar.md5">mylib-1.0.jar.md5</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/maven2/com/example/mylib/1.0/", html)
	assert.Equal(t, 5, len(entries))

	// Check none are directories
	for _, e := range entries {
		assert.False(t, e.IsDir, "%s should not be a directory", e.Name)
	}
}

func TestHTMLListingParsing_SpecialCharactersInURL(t *testing.T) {
	html := `<html><body>
		<a href="my-lib%2B1.0.jar">my-lib+1.0.jar</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/maven2/", html)
	assert.Equal(t, 1, len(entries))
	assert.Equal(t, "my-lib%2B1.0.jar", entries[0].Name)
}

func TestHTMLListingParsing_TrailingSlashBaseURL(t *testing.T) {
	html := `<html><body>
		<a href="file.jar">file.jar</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/path/", html)
	assert.Equal(t, 1, len(entries))
	assert.Equal(t, "https://repo.example.com/path/file.jar", entries[0].URL)
}

func TestHTMLListingParsing_NoTrailingSlashBaseURL(t *testing.T) {
	html := `<html><body>
		<a href="file.jar">file.jar</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/path", html)
	assert.Equal(t, 1, len(entries))
	assert.Equal(t, "https://repo.example.com/path/file.jar", entries[0].URL)
}

func TestHTMLListingParsing_ParentLinkOnly(t *testing.T) {
	html := `<html><body>
		<a href="../">../</a>
	</body></html>`

	entries := parseHTMLListing("https://repo.example.com/maven2/", html)
	assert.Equal(t, 0, len(entries), "parent link should be skipped")
}

func TestArtifactFileDetection_EarExtension(t *testing.T) {
	assert.True(t, isArtifactFile("app-1.0.ear"), ".ear should be an artifact file")
}

func TestArtifactFileDetection_NonArtifactExtensions(t *testing.T) {
	assert.False(t, isArtifactFile("readme.txt"))
	assert.False(t, isArtifactFile("logo.png"))
	assert.False(t, isArtifactFile("data.csv"))
	assert.False(t, isArtifactFile("lib-1.0.jar.sha1"))
	assert.False(t, isArtifactFile("lib-1.0.jar.md5"))
	assert.False(t, isArtifactFile("lib-1.0.pom.asc"))
}

func TestIsWantedArtifact_ClassifierFilters(t *testing.T) {
	tests := []struct {
		name           string
		includeSources bool
		skipPom        bool
		want           bool
	}{
		{"lib-1.0.jar", false, false, true},
		{"lib-1.0-sources.jar", false, false, false},  // sources 默认跳过
		{"lib-1.0-sources.jar", true, false, true},  // 显式开启
		{"lib-1.0-javadoc.jar", false, false, false},
		{"lib-1.0-tests.jar", false, false, false},
		{"lib-1.0.pom", false, false, true},          // pom 默认扫
		{"lib-1.0.pom", false, true, false},          // skip-pom 跳过
		{"lib-1.0.xml", false, true, false},
		{"lib-1.0.war", false, false, true},
		{"readme.txt", false, false, false},          // 非 artifact
		{"lib-1.0.jar.sha1", false, false, false},   // 非 artifact（.sha1 不在表）
	}
	for _, tt := range tests {
		got := isWantedArtifact(tt.name, tt.includeSources, tt.skipPom)
		assert.Equal(t, tt.want, got, "name=%s includeSources=%v skipPom=%v", tt.name, tt.includeSources, tt.skipPom)
	}
}

func TestIsClassifierJar(t *testing.T) {
	assert.True(t, isClassifierJar("lib-1.0-sources.jar"))
	assert.True(t, isClassifierJar("lib-1.0-javadoc.jar"))
	assert.True(t, isClassifierJar("lib-1.0-tests.jar"))
	assert.False(t, isClassifierJar("lib-1.0.jar"))
	assert.False(t, isClassifierJar("lib-1.0.pom"))
}
