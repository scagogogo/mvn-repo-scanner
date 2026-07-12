package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	AppName           = ".mvn-repo-scanner"
	DefaultCacheMaxMB = 500
)

// Workspace manages the application's working directory under ~/.mvn-repo-scanner/.
type Workspace struct {
	BaseDir    string
	DBPath     string
	CacheDir   string
	RulesDir   string
	CacheMaxMB int
}

// NewWorkspace creates or opens the workspace at ~/.mvn-repo-scanner/.
func NewWorkspace() (*Workspace, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	return newWorkspaceAt(filepath.Join(home, AppName))
}

// NewWorkspaceAt creates or opens the workspace at a custom path (for testing).
func NewWorkspaceAt(baseDir string) (*Workspace, error) {
	return newWorkspaceAt(baseDir)
}

func newWorkspaceAt(baseDir string) (*Workspace, error) {
	w := &Workspace{
		BaseDir:    baseDir,
		DBPath:     filepath.Join(baseDir, "scan.db"),
		CacheDir:   filepath.Join(baseDir, "cache"),
		RulesDir:   filepath.Join(baseDir, "rules"),
		CacheMaxMB: DefaultCacheMaxMB,
	}

	dirs := []string{baseDir, w.CacheDir, w.RulesDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", d, err)
		}
	}

	return w, nil
}

// CacheUsageMB returns the current cache directory size in megabytes.
// The walk callback always returns nil (tolerating per-entry errors), so
// filepath.Walk never returns a non-nil error here — the error result is
// intentionally ignored.
func (w *Workspace) CacheUsageMB() (int, error) {
	var total int64
	_ = filepath.Walk(w.CacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return int(total / (1024 * 1024)), nil
}

// CleanCache removes all files from the cache directory.
func (w *Workspace) CleanCache() error {
	entries, err := os.ReadDir(w.CacheDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		os.RemoveAll(filepath.Join(w.CacheDir, e.Name()))
	}
	return nil
}

// cachedFile holds info about a cached file for sorting.
type cachedFile struct {
	path    string
	modTime time.Time
	size    int64
}

// EnforceCacheLimit removes oldest files until cache is under the limit.
// CacheUsageMB never returns an error (see its doc comment), so we can
// treat usage as authoritative here.
func (w *Workspace) EnforceCacheLimit() error {
	usage, _ := w.CacheUsageMB()
	if usage <= w.CacheMaxMB {
		return nil
	}

	// CacheUsageMB walked the dir successfully (usage > max implies it exists
	// and is readable), so ReadDir here cannot fail in practice — the error
	// is intentionally ignored.
	entries, _ := os.ReadDir(w.CacheDir)

	var files []cachedFile
	for _, e := range entries {
		// e.Info() for entries returned by os.ReadDir is built from data
		// already read by ReadDir; it does not perform an extra stat and so
		// cannot fail in practice — the error is intentionally ignored.
		info, _ := e.Info()
		files = append(files, cachedFile{
			path:    filepath.Join(w.CacheDir, e.Name()),
			modTime: info.ModTime(),
			size:    info.Size(),
		})
	}

	// Sort oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	for _, f := range files {
		if usage <= w.CacheMaxMB {
			break
		}
		os.Remove(f.path)
		usage -= int(f.size / (1024 * 1024))
	}
	return nil
}

// DBSizeMB returns the database file size in megabytes.
func (w *Workspace) DBSizeMB() (int, error) {
	info, err := os.Stat(w.DBPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return int(info.Size() / (1024 * 1024)), nil
}
