package repo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// CursorFrame represents one level of the traversal cursor.
// A cursor is a stack of frames; the top frame is the directory currently
// being enumerated. nextIdx is the index into that directory's sorted child
// list to visit next. This makes traversal resumable: persisting the cursor
// (depth = tree depth, ~7 for Maven Central) is enough to continue exactly
// where we left off, with O(depth) state instead of O(visited) or O(total).
type CursorFrame struct {
	DirPath string `json:"dir_path"` // repository-relative path, e.g. "com/example/lib"
	NextIdx int    `json:"next_idx"` // index into the sorted child list to visit next
}

// Cursor is the resumable traversal state: a stack of CursorFrame.
type Cursor []CursorFrame

// clone returns a deep copy of the cursor.
func (c Cursor) clone() Cursor {
	out := make(Cursor, len(c))
	copy(out, c)
	return out
}

// PageFetcher fetches the HTML listing of a directory URL.
// Abstracted from HTTP so traversal can be tested with a mock fetcher.
type PageFetcher interface {
	// FetchPage returns the raw HTML listing for the given directory URL.
	FetchPage(ctx context.Context, dirURL string) (string, error)
}

// dirNode holds the parsed, sorted children of a directory, cached so that
// resuming a cursor only re-fetches each frame's directory once.
type dirNode struct {
	entries []entry // sorted by Name
}

// CursorWalker traverses a Maven repository tree using an ordered DFS with
// an explicit cursor stack. It holds at most O(tree depth) nodes in memory
// at any time (the cursor stack), making it ideal for resumable scans:
// persist the cursor, and on resume re-fetch only the ~depth directories on
// the stack, then continue.
//
// Ordering: children are sorted by name (dirs and files interleaved by name,
// matching Apache-style listings) so that NextIdx is stable across runs
// regardless of server-side ordering.
type CursorWalker struct {
	fetcher PageFetcher
	baseURL string

	// includeSources/skipPom mirror Browser's classifier filters so streaming
	// discovery (the default path) honors the same --include-sources/--skip-pom
	// flags as batched discovery.
	includeSources bool
	skipPom        bool

	// onDirFailed, if set, is invoked when a directory listing cannot be
	// fetched during a walk. The walk still skips the directory (popping the
	// frame and continuing with siblings) so a single transient failure does
	// not abort the whole scan — but the callback lets the caller persist the
	// failed directory so a later resume can re-visit it instead of silently
	// losing the entire subtree. Kept as an unexported callback so this package
	// stays free of any state-package dependency.
	onDirFailed func(dirPath string)

	mu    sync.Mutex
	cache map[string]*dirNode // dirPath -> parsed children (session cache)
}

// NewCursorWalker creates a walker over baseURL using the given fetcher.
func NewCursorWalker(fetcher PageFetcher, baseURL string) *CursorWalker {
	return &CursorWalker{
		fetcher: fetcher,
		baseURL: baseURL,
		cache:   make(map[string]*dirNode),
	}
}

// SetClassifierFilters mirrors Browser.WithClassifierFilters so the streaming
// discovery path honors the same --include-sources/--skip-pom flags.
func (w *CursorWalker) SetClassifierFilters(includeSources, skipPom bool) {
	w.includeSources = includeSources
	w.skipPom = skipPom
}

// SetOnDirFailed installs a callback invoked when a directory listing fetch
// fails during Walk. See CursorWalker.onDirFailed for the contract.
func (w *CursorWalker) SetOnDirFailed(cb func(dirPath string)) {
	w.onDirFailed = cb
}

// Walk traverses the tree starting from the given cursor (or from the root
// if the cursor is empty). For each artifact file discovered, it calls yield.
// If shouldStop returns true, walking pauses and the current cursor is
// returned so the caller can persist it and resume later.
//
// The cursor returned is the state needed to resume: its length equals the
// current tree depth, and each frame records which directory and which child
// index to continue from.
func (w *CursorWalker) Walk(ctx context.Context, start Cursor, yield func(artifact Artifact), shouldStop func() bool) (Cursor, error) {
	cur := start.clone()
	if len(cur) == 0 {
		cur = Cursor{{DirPath: "", NextIdx: 0}}
	}

	for len(cur) > 0 {
		select {
		case <-ctx.Done():
			return cur, ctx.Err()
		default:
		}

		if shouldStop() {
			return cur, nil
		}

		top := &cur[len(cur)-1]
		node, err := w.getNode(ctx, top.DirPath)
		if err != nil {
			// Notify the caller of the failed directory before popping, so it
			// can persist it for resume-time re-visit. The walk still skips
			// this directory (pop + continue) to preserve the "transient fetch
			// failure does not abort the scan" contract.
			if w.onDirFailed != nil {
				w.onDirFailed(top.DirPath)
			}
			cur = cur[:len(cur)-1]
			continue
		}

		if top.NextIdx >= len(node.entries) {
			// Done with this directory — pop.
			cur = cur[:len(cur)-1]
			continue
		}

		e := node.entries[top.NextIdx]
		top.NextIdx++

		if e.IsDir {
			cur = append(cur, CursorFrame{DirPath: joinPath(top.DirPath, e.Name), NextIdx: 0})
		} else if isWantedArtifact(e.Name, w.includeSources, w.skipPom) {
			art := w.buildArtifact(top.DirPath, e)
			if art != nil {
				yield(*art)
			}
		}
	}

	return cur, nil
}

// getNode returns the parsed+sorted children of a directory, caching per
// session so that cursor resume only fetches each directory once.
func (w *CursorWalker) getNode(ctx context.Context, dirPath string) (*dirNode, error) {
	w.mu.Lock()
	if n, ok := w.cache[dirPath]; ok {
		w.mu.Unlock()
		return n, nil
	}
	w.mu.Unlock()

	dirURL := w.pathToURL(dirPath)
	html, err := w.fetcher.FetchPage(ctx, dirURL)
	if err != nil {
		return nil, err
	}
	entries := parseHTMLListing(dirURL, html)
	// Sort by name for stable ordering across runs. Apache listings are
	// typically already sorted, but Nexus and other servers may not be.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	n := &dirNode{entries: entries}
	w.mu.Lock()
	w.cache[dirPath] = n
	w.mu.Unlock()
	return n, nil
}

// buildArtifact constructs an Artifact from a directory path and a file entry.
// Mirrors Browser.buildArtifact but works with the cursor's slash-separated
// dirPath instead of accumulated path segments.
func (w *CursorWalker) buildArtifact(dirPath string, e entry) *Artifact {
	// dirPath = "group/parts/artifact/version"
	parts := strings.Split(dirPath, "/")
	// Filter out empty segments (from leading slash or root "")
	var segs []string
	for _, p := range parts {
		if p != "" {
			segs = append(segs, p)
		}
	}
	if len(segs) < 3 {
		return nil
	}

	versionIdx := len(segs) - 1
	artifactIdx := versionIdx - 1
	groupParts := segs[:artifactIdx]

	return &Artifact{
		GroupID:     strings.Join(groupParts, "."),
		ArtifactID:  segs[artifactIdx],
		Version:     segs[versionIdx],
		FileName:    e.Name,
		DownloadURL: e.URL,
	}
}

// pathToURL converts a repository-relative dirPath to a full URL.
func (w *CursorWalker) pathToURL(dirPath string) string {
	base := w.baseURL
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	if dirPath == "" {
		return base
	}
	return base + dirPath + "/"
}

// joinPath joins a parent dirPath with a child name.
func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

// RootCursor returns a fresh cursor pointing at the repository root.
func RootCursor() Cursor {
	return Cursor{{DirPath: "", NextIdx: 0}}
}

// CursorDepth returns the number of frames in the cursor (== current tree depth).
func (c Cursor) CursorDepth() int { return len(c) }

// String returns a human-readable description for logging.
func (c Cursor) String() string {
	parts := make([]string, len(c))
	for i, f := range c {
		parts[i] = fmt.Sprintf("{%s@%d}", f.DirPath, f.NextIdx)
	}
	return fmt.Sprintf("cursor[%s]", strings.Join(parts, ","))
}
