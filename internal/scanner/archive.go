package scanner

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
)

// Archive scanning safety limits. These defend against malicious or
// pathological archives (zip bombs, degenerate tarballs) that could otherwise
// exhaust memory or CPU during extraction.
const (
	// maxDecompressedPerArchive caps the total bytes an archive may expand to.
	// A 50MB download expanding past 1GB is almost certainly a bomb.
	maxDecompressedPerArchive int64 = 1 << 30 // 1 GiB
	// maxEntriesPerArchive caps the number of files walked inside one archive,
	// preventing slow-DoS via archives declaring hundreds of thousands of entries.
	maxEntriesPerArchive = 50000
	// maxNestedDepth limits recursive descent into archives-within-archives
	// (a jar containing a war containing a jar...). 5 is generous for real
	// uber-jars while bounding pathological recursion.
	maxNestedDepth = 5
	// maxCompressionRatio flags an entry whose decompressed size dwarfs its
	// compressed size — the classic zip-bomb signature.
	maxCompressionRatio = 100
)

// scannableExts is the set of archive-entry extensions whose content we scan.
// Lifted to a package-level set so it is allocated once, not per file.
//
// Files with no extension are matched against scannableNoExt by base name
// (Dockerfile, Jenkinsfile, etc.), since many CI/config files are extensionless.
var (
	scannableExts = map[string]bool{
		// 配置文件
		".properties": true, ".xml": true, ".yml": true, ".yaml": true,
		".json": true, ".conf": true, ".cfg": true, ".ini": true,
		".config": true, ".toml": true, ".env": true,
		// 构建文件
		".gradle": true, ".mvn": true,
		// 包管理器
		".npmrc": true,
		// 密钥/证书
		".pem": true, ".key": true, ".p12": true, ".jks": true, ".pfx": true, ".crt": true, ".cer": true,
		// 脚本文件
		".sh": true, ".bat": true, ".cmd": true, ".ps1": true,
		// 文档/文本
		".txt": true, ".md": true, ".rst": true,
		// 源代码
		".java": true, ".kt": true, ".groovy": true, ".scala": true,
		// Docker/云配置
		".dockerfile": true, ".policy": true,
		// 备份配置
		".bak": true, ".orig": true, ".old": true,
	}
	// scannableNoExt matches extensionless files by base name (case-insensitive).
	scannableNoExt = map[string]bool{
		"dockerfile": true, "jenkinsfile": true, "license": true,
		"env": true, "gitignore": true, "npmrc": true,
	}
	// nestedArchiveExts marks archive types we recurse into when found inside
	// another archive (e.g. WEB-INF/lib/*.jar inside a war).
	nestedArchiveExts = map[string]bool{
		".jar": true, ".war": true, ".ear": true, ".zip": true,
	}
)

// archiveScanState tracks per-archive safety counters across nested recursion.
type archiveScanState struct {
	totalDecompressed int64
	totalEntries      int
}

// isScannableFile checks if a file inside an archive (or a downloaded artifact)
// should be scanned. Extensionless files are matched by base name so common
// CI/config files without extensions (Dockerfile, Jenkinsfile) are covered.
func isScannableFile(name string) bool {
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	if ext != "" {
		return scannableExts[ext]
	}
	// No extension: match well-known base names.
	lowerBase := strings.ToLower(base)
	return scannableNoExt[lowerBase]
}

// isNestedArchive reports whether name is an archive we should recurse into.
func isNestedArchive(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return nestedArchiveExts[ext]
}

// looksBinary peeks at the first chunk of a reader and reports whether it
// appears to be binary (contains a NUL byte or a high ratio of non-text bytes).
// Text files occasionally contain a stray non-printable byte, so we use NUL as
// the decisive signal (mirrors git's heuristic). The reader is rewound via the
// returned unread prefix so callers don't lose the peeked bytes.
func looksBinary(r io.Reader) (bool, []byte, error) {
	peek := make([]byte, 1024)
	n, err := io.ReadFull(r, peek)
	peek = peek[:n]
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, peek, err
	}
	if n == 0 {
		return false, peek, nil
	}
	// A NUL byte is the strongest binary signal.
	for _, b := range peek {
		if b == 0 {
			return true, peek, nil
		}
	}
	// Fallback: if >30% of bytes are non-text control chars, treat as binary.
	nonText := 0
	for _, b := range peek {
		if b < 9 || (b > 13 && b < 32) {
			nonText++
		}
	}
	return float64(nonText)/float64(n) > 0.30, peek, nil
}

// scanArchiveFile dispatches a downloaded archive to the right extractor by
// extension. .jar/.war/.ear/.zip use the ZIP path; .tar/.tar.gz/.tgz use the
// tar path. It is the single entry point from the scanner for archive scanning.
func (s *Scanner) scanArchiveFile(archivePath, fileName string) ([]detector.Finding, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".jar", ".war", ".ear", ".zip":
		return s.scanZipArchive(archivePath, 0, &archiveScanState{})
	case ".tar":
		return s.scanTarArchive(archivePath, false, 0, &archiveScanState{})
	case ".gz", ".tgz":
		// .tar.gz or .tgz → gunzip then tar. (.gz of a non-tar is rare for
		// Maven artifacts; we still try the tar path, which no-ops cleanly if
		// it isn't a valid tar.)
		return s.scanTarArchive(archivePath, true, 0, &archiveScanState{})
	default:
		// Not a recognized archive — fall back to plain file scan.
		return s.detector.ScanFile(archivePath)
	}
}

// scanZipArchive extracts a ZIP-based archive and scans text entries, recursing
// into nested archives up to maxNestedDepth. Safety limits (total decompressed
// size, entry count, compression ratio) are enforced via st.
func (s *Scanner) scanZipArchive(archivePath string, depth int, st *archiveScanState) ([]detector.Finding, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	return s.scanZipReader(r.File, archivePath, depth, st)
}

// scanZipReader scans entries from an already-opened zip.Reader (used for both
// top-level files and nested archives read via f.Open).
func (s *Scanner) scanZipReader(files []*zip.File, archivePath string, depth int, st *archiveScanState) ([]detector.Finding, error) {
	var allFindings []detector.Finding

	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}

		// Entry-count guard.
		if st.totalEntries >= maxEntriesPerArchive {
			log.Printf("archive %s: exceeded %d entries, stopping (possible zip-bomb)", archivePath, maxEntriesPerArchive)
			break
		}
		st.totalEntries++

		// Zip-bomb guards on declared sizes.
		if f.UncompressedSize64 > uint64(maxDecompressedPerArchive) {
			log.Printf("archive %s: entry %s uncompressed size %d exceeds limit, skipping",
				archivePath, f.Name, f.UncompressedSize64)
			continue
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > uint64(maxCompressionRatio) {
			log.Printf("archive %s: entry %s compression ratio %d:1 exceeds limit, skipping (possible zip-bomb)",
				archivePath, f.Name, f.UncompressedSize64/f.CompressedSize64)
			continue
		}
		if st.totalDecompressed+int64(f.UncompressedSize64) > maxDecompressedPerArchive {
			log.Printf("archive %s: cumulative decompressed size would exceed %d, stopping",
				archivePath, maxDecompressedPerArchive)
			break
		}
		st.totalDecompressed += int64(f.UncompressedSize64)

		// Recurse into nested archives (fat-jar WEB-INF/lib/*.jar etc.).
		if depth < maxNestedDepth && isNestedArchive(f.Name) {
			nested, err := s.scanNestedZipEntry(f, depth, st)
			if err != nil {
				log.Printf("archive %s: nested entry %s: %v", archivePath, f.Name, err)
			}
			allFindings = append(allFindings, nested...)
			continue
		}

		if !isScannableFile(f.Name) {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			log.Printf("archive %s: open entry %s: %v", archivePath, f.Name, err)
			continue
		}
		findings, err := s.scanEntryContent(rc, f.Name)
		rc.Close()
		if err != nil {
			log.Printf("archive %s: scan entry %s: %v", archivePath, f.Name, err)
			continue
		}
		allFindings = append(allFindings, findings...)
	}

	return allFindings, nil
}

// scanNestedZipEntry materializes a nested zip entry to a temp file and scans
// it as its own archive. The temp file is removed afterwards.
func (s *Scanner) scanNestedZipEntry(f *zip.File, depth int, st *archiveScanState) ([]detector.Finding, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "mvnscan-nested-*.bin")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	// Re-dispatch by the nested entry's extension.
	ext := strings.ToLower(filepath.Ext(f.Name))
	switch ext {
	case ".jar", ".war", ".ear", ".zip":
		return s.scanZipArchive(tmpPath, depth+1, st)
	case ".tar":
		return s.scanTarArchive(tmpPath, false, depth+1, st)
	case ".gz", ".tgz":
		return s.scanTarArchive(tmpPath, true, depth+1, st)
	default:
		return nil, nil
	}
}

// scanTarArchive scans a (optionally gzip-compressed) tar archive. Used for
// Maven source/binary distributions shipped as .tar.gz.
func (s *Scanner) scanTarArchive(archivePath string, gzipped bool, depth int, st *archiveScanState) ([]detector.Finding, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open tar archive: %w", err)
	}
	defer f.Close()

	var reader io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	tr := tar.NewReader(reader)
	var allFindings []detector.Finding

	for {
		if st.totalEntries >= maxEntriesPerArchive {
			log.Printf("tar %s: exceeded %d entries, stopping", archivePath, maxEntriesPerArchive)
			break
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("tar %s: read header: %v", archivePath, err)
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		st.totalEntries++
		if hdr.Size > maxDecompressedPerArchive {
			log.Printf("tar %s: entry %s size %d exceeds limit, skipping", archivePath, hdr.Name, hdr.Size)
			continue
		}
		if st.totalDecompressed+hdr.Size > maxDecompressedPerArchive {
			log.Printf("tar %s: cumulative size would exceed %d, stopping", archivePath, maxDecompressedPerArchive)
			break
		}
		st.totalDecompressed += hdr.Size

		if depth < maxNestedDepth && isNestedArchive(hdr.Name) {
			// Materialize nested archive entry to a temp file and recurse.
			findings, err := s.scanNestedTarEntry(tr, hdr.Name, depth, st)
			if err != nil {
				log.Printf("tar %s: nested entry %s: %v", archivePath, hdr.Name, err)
			}
			allFindings = append(allFindings, findings...)
			continue
		}

		if !isScannableFile(hdr.Name) {
			// Still need to drain the reader to advance to the next entry.
			io.Copy(io.Discard, tr)
			continue
		}

		findings, err := s.scanEntryContent(tr, hdr.Name)
		if err != nil {
			log.Printf("tar %s: scan entry %s: %v", archivePath, hdr.Name, err)
			continue
		}
		allFindings = append(allFindings, findings...)
	}

	return allFindings, nil
}

// scanNestedTarEntry copies the current tar entry to a temp file and re-scans
// it as an archive.
func (s *Scanner) scanNestedTarEntry(r io.Reader, name string, depth int, st *archiveScanState) ([]detector.Finding, error) {
	tmp, err := os.CreateTemp("", "mvnscan-nested-*.bin")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jar", ".war", ".ear", ".zip":
		return s.scanZipArchive(tmpPath, depth+1, st)
	case ".tar":
		return s.scanTarArchive(tmpPath, false, depth+1, st)
	case ".gz", ".tgz":
		return s.scanTarArchive(tmpPath, true, depth+1, st)
	default:
		return nil, nil
	}
}

// scanEntryContent wraps the reader with a binary-content pre-check, then
// delegates to the detector. If the content looks binary, it is skipped (the
// extension whitelist already filters most binaries, but this catches
// mislabeled files like a .json that's actually gzipped).
func (s *Scanner) scanEntryContent(r io.Reader, filePath string) ([]detector.Finding, error) {
	binary, peek, err := looksBinary(r)
	if err != nil {
		return nil, err
	}
	if binary {
		return nil, nil
	}
	// Reconstruct a reader that includes the peeked bytes.
	full := io.MultiReader(bytes.NewReader(peek), r)
	return s.detector.ScanContent(full, filePath)
}
