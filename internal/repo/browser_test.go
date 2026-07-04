package repo

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
