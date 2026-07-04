package repo

import (
	"regexp"
	"strings"
)

// entry represents a directory listing entry in a Maven repository.
type entry struct {
	Name  string
	IsDir bool
	URL   string
}

var hrefRegex = regexp.MustCompile(`<a\s+href="([^"]+)"[^>]*>([^<]+)</a>`)

// parseHTMLListing parses HTML directory listing from Maven repository.
// Works with standard Apache-style and Nexus-style listings.
func parseHTMLListing(baseURL, html string) []entry {
	var entries []entry
	matches := hrefRegex.FindAllStringSubmatch(html, -1)

	for _, m := range matches {
		href := m[1]
		text := m[2]

		// Skip parent directory link
		if text == "../" || text == "/" {
			continue
		}

		isDir := strings.HasSuffix(href, "/")
		name := strings.TrimSuffix(href, "/")

		// Build full URL
		fullURL := baseURL
		if !strings.HasSuffix(fullURL, "/") {
			fullURL += "/"
		}
		fullURL += href

		entries = append(entries, entry{
			Name:  name,
			IsDir: isDir,
			URL:   fullURL,
		})
	}
	return entries
}

// isArtifactFile checks if an entry name is a downloadable artifact file.
func isArtifactFile(name string) bool {
	return strings.HasSuffix(name, ".jar") ||
		strings.HasSuffix(name, ".war") ||
		strings.HasSuffix(name, ".ear") ||
		strings.HasSuffix(name, ".pom") ||
		strings.HasSuffix(name, ".xml")
}
