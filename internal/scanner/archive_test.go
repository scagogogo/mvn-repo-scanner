package scanner

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scagogogo/mvn-repo-scanner/internal/detector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDetectorForArchiveTest builds a Detector with the core rules so archive
// scanning tests can assert real findings (e.g. hardcoded-password).
func newDetectorForArchiveTest(t *testing.T) *detector.Detector {
	t.Helper()
	det, err := detector.NewDetector(detector.DefaultRules())
	require.NoError(t, err)
	return det
}

// makeZip writes a zip archive to path whose entries are the given (name, content) pairs.
func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	w := zip.NewWriter(f)
	for name, content := range entries {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}

// makeNestedZip writes a zip at path that contains an inner zip entry
// "inner.jar" whose own entries are innerEntries.
func makeNestedZip(t *testing.T, path string, innerEntries map[string]string) {
	t.Helper()
	// Build the inner zip into a buffer.
	var buf bytes.Buffer
	iw := zip.NewWriter(&buf)
	for name, content := range innerEntries {
		fw, err := iw.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, iw.Close())

	// Outer zip with inner.jar as a single entry.
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	ow := zip.NewWriter(f)
	fw, err := ow.Create("WEB-INF/lib/inner.jar")
	require.NoError(t, err)
	_, err = fw.Write(buf.Bytes())
	require.NoError(t, err)
	require.NoError(t, ow.Close())
}

// makeTarGz writes a .tar.gz at path with the given entries.
func makeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
}

func newScannerWithDetector(det *detector.Detector) *Scanner {
	return &Scanner{detector: det, cfg: nil}
}

func TestScanArchive_DetectsPasswordInProperties(t *testing.T) {
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "app.jar")
	makeZip(t, jarPath, map[string]string{
		"application.properties": "db.password = supersecret123",
		"META-INF/MANIFEST.MF":   "Manifest-Version: 1.0",
	})

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(jarPath, "app.jar")
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should detect hardcoded password in properties inside jar")
	found := false
	for _, f := range findings {
		if f.RuleID == "hardcoded-password" {
			found = true
		}
	}
	assert.True(t, found, "hardcoded-password rule should fire")
}

func TestScanArchive_RecursesIntoNestedJar(t *testing.T) {
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "uber.jar")
	// The secret lives only inside the inner jar — without nesting support it
	// would be missed entirely.
	makeNestedZip(t, jarPath, map[string]string{
		"deep.properties": "password = nested-secret-xyz",
	})

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(jarPath, "uber.jar")
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should detect secret inside nested jar")
}

func TestScanArchive_TarGzSupported(t *testing.T) {
	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "dist.tar.gz")
	makeTarGz(t, tgzPath, map[string]string{
		"config/conf.properties": "password = tarred-secret-789",
	})

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(tgzPath, "dist.tar.gz")
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should detect secret inside tar.gz")
}

func TestScanArchive_SkipsBinaryByContent(t *testing.T) {
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "mixed.jar")
	// A .json-named entry whose bytes are actually binary (NUL bytes). The
	// binary pre-check should skip it rather than feed it to the regex engine.
	binaryJSON := make([]byte, 256)
	for i := range binaryJSON {
		binaryJSON[i] = byte(i % 256) // plenty of NUL and control bytes
	}
	entries := map[string]string{
		"normal.properties": "password = plaintext-secret-1",
		"weird.json":        string(binaryJSON),
	}
	makeZip(t, jarPath, entries)

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(jarPath, "mixed.jar")
	require.NoError(t, err)
	// Should still find the password in the text properties file.
	assert.NotEmpty(t, findings, "text entry should still be scanned")
	// And should not crash on the binary entry (no error, no panic).
}

func TestScanArchive_ZipBombCompressionRatioRejected(t *testing.T) {
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "bomb.jar")

	// Build a zip with one entry that claims a huge uncompressed size relative
	// to compressed — we do this by writing a highly compressible buffer but
	// we want to test the declared-ratio guard, so craft via the zip writer
	// with a real high-ratio entry (a long run of the same byte).
	f, err := os.Create(jarPath)
	require.NoError(t, err)
	defer f.Close()
	w := zip.NewWriter(f)
	fw, err := w.Create("huge.properties")
	require.NoError(t, err)
	// 5 MB of repeated bytes compresses to a few KB → ratio >> 100:1.
	_, err = fw.Write(bytes.Repeat([]byte("password = aaaaaaaaaaaaaaaa\n"), 200000))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// Should not error, but should skip the bomb entry. The important assertion
	// is that we don't hang or OOM; the ratio guard logs and continues.
	_, err = s.scanArchiveFile(jarPath, "bomb.jar")
	require.NoError(t, err)
}

func TestScanArchive_NoEntries(t *testing.T) {
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "empty.jar")
	// A valid zip with zero entries.
	f, err := os.Create(jarPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	require.NoError(t, w.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(jarPath, "empty.jar")
	require.NoError(t, err)
	assert.Empty(t, findings)
}

func TestScanArchive_LongLineDoesNotTruncateScan(t *testing.T) {
	// bufio.Scanner without an explicit buffer hits bufio.ErrTooLong at 64KB.
	// Our scanReader now buffers up to 1 MiB, so a 100KB single-line properties
	// value must still be scanned (and matched). We test ScanContent directly
	// (rather than via a zipped fixture) because any long run of bytes is highly
	// compressible and would trip the zip-bomb compression-ratio guard, which
	// is itself correct behavior — tested separately in TestScanArchive_ZipBomb.
	det := newDetectorForArchiveTest(t)
	var sb strings.Builder
	sb.WriteString("password = ")
	for i := 0; i < 100*1024; i++ { // 100 KB single line, well over the 64KB default
		sb.WriteByte(byte('a' + (i % 26)))
	}
	findings, err := det.ScanContent(strings.NewReader(sb.String()), "application.properties")
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "long line should still be scanned, not dropped by ErrTooLong")
}

func TestIsArchiveFile(t *testing.T) {
	assert.True(t, isArchiveFile("foo.jar"))
	assert.True(t, isArchiveFile("foo.WAR"))
	assert.True(t, isArchiveFile("foo.ear"))
	assert.True(t, isArchiveFile("foo.zip"))
	assert.True(t, isArchiveFile("foo.tar"))
	assert.True(t, isArchiveFile("foo.tar.gz"))
	assert.True(t, isArchiveFile("foo.tgz"))
	assert.False(t, isArchiveFile("foo.pom"))
	assert.False(t, isArchiveFile("foo.xml"))
	assert.False(t, isArchiveFile("foo"))
}

func TestIsScannableFile_Extensionless(t *testing.T) {
	assert.True(t, isScannableFile("Dockerfile"))
	assert.True(t, isScannableFile("path/to/Jenkinsfile"))
	assert.True(t, isScannableFile("LICENSE"))
	assert.False(t, isScannableFile("random-no-ext"))
	assert.True(t, isScannableFile("app.properties"))
	assert.True(t, isScannableFile("META-INF/maven/pom.xml"))
}

// TestScanEntryContent_BinaryPeek verifies the binary pre-check rejects a
// NUL-containing reader and skips it, returning no findings and no error.
func TestScanEntryContent_BinaryPeek(t *testing.T) {
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// Reader whose first 1024 bytes contain a NUL.
	body := bytes.Repeat([]byte{'A'}, 2048)
	body[10] = 0 // NUL byte
	findings, err := s.scanEntryContent(bytes.NewReader(body), "fake.json")
	require.NoError(t, err)
	assert.Empty(t, findings, "binary content should be skipped")
}

// TestScanEntryContent_TextPassthrough verifies text content (with no NUL) is
// passed through to the detector.
func TestScanEntryContent_TextPassthrough(t *testing.T) {
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	body := []byte("password = plaintext-987\n")
	findings, err := s.scanEntryContent(bytes.NewReader(body), "app.properties")
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}
