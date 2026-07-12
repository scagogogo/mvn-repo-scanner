package scanner

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
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

// ---- looksBinary 分支 ----

func TestLooksBinary_NULByte(t *testing.T) {
	bin, _, err := looksBinary(bytes.NewReader([]byte{'a', 0, 'b'}))
	require.NoError(t, err)
	assert.True(t, bin)
}

func TestLooksBinary_Empty(t *testing.T) {
	bin, _, err := looksBinary(bytes.NewReader(nil))
	require.NoError(t, err)
	assert.False(t, bin)
}

func TestLooksBinary_HighNonTextRatio(t *testing.T) {
	// 全部是控制字符（< 9）→ 非 NUL 但 nonText 占比 > 30% → binary
	body := bytes.Repeat([]byte{1}, 200)
	bin, _, err := looksBinary(bytes.NewReader(body))
	require.NoError(t, err)
	assert.True(t, bin)
}

func TestLooksBinary_PureText(t *testing.T) {
	bin, _, err := looksBinary(bytes.NewReader([]byte("hello world plain text")))
	require.NoError(t, err)
	assert.False(t, bin)
}

func TestLooksBinary_ReadError(t *testing.T) {
	// 用一个总是报错的 reader 触发读错误分支
	bin, _, err := looksBinary(errReader{})
	assert.Error(t, err)
	assert.False(t, bin)
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, fmt.Errorf("simulated read error") }

// ---- scanNestedTarEntry（嵌套 tar/.tar.gz/.jar）----

// makeNestedTarGz writes a .tar.gz at path containing a single nested entry
// "nested.<ext>" whose content is itself a valid archive of that extension.
func makeNestedTarGz(t *testing.T, path string, nestedExt string, nestedEntries map[string]string) {
	t.Helper()
	// Build the inner archive into a buffer.
	var innerBuf bytes.Buffer
	switch nestedExt {
	case ".jar", ".zip":
		iw := zip.NewWriter(&innerBuf)
		for name, content := range nestedEntries {
			fw, err := iw.Create(name)
			require.NoError(t, err)
			_, err = fw.Write([]byte(content))
			require.NoError(t, err)
		}
		require.NoError(t, iw.Close())
	case ".tar":
		itw := tar.NewWriter(&innerBuf)
		for name, content := range nestedEntries {
			hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}
			require.NoError(t, itw.WriteHeader(hdr))
			_, err := itw.Write([]byte(content))
			require.NoError(t, err)
		}
		require.NoError(t, itw.Close())
	}

	// Outer .tar.gz with the nested archive as a single entry.
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "nested" + nestedExt, Mode: 0644, Size: int64(innerBuf.Len())}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write(innerBuf.Bytes())
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
}

func TestScanArchive_NestedTarGzWithInnerJar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outer.tar.gz")
	makeNestedTarGz(t, path, ".jar", map[string]string{
		"application.properties": "db.password = nested-tar-secret\n",
	})

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(path, "outer.tar.gz")
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "nested jar inside tar.gz should be scanned")
}

// 注：isNestedArchive 只识别 .jar/.war/.ear/.zip，不识别 .tar/.tar.gz，
// 所以 tar 内嵌 tar 不会被递归扫描——这是设计行为，不另写测试。

func TestScanNestedTarEntry_UnknownExt(t *testing.T) {
	// 未知扩展名 → 返回 (nil, nil)
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	findings, err := s.scanNestedTarEntry(bytes.NewReader([]byte("whatever")), "data.bin", 0, st)
	require.NoError(t, err)
	assert.Nil(t, findings)
}

// ---- scanTarArchive 错误分支 ----

func TestScanTarArchive_OpenError(t *testing.T) {
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err := s.scanTarArchive("/nonexistent/path.tar.gz", true, 0, &archiveScanState{})
	assert.Error(t, err)
}

func TestScanTarArchive_BadGzip(t *testing.T) {
	// 文件存在但不是 gzip → gzip.NewReader 失败
	dir := t.TempDir()
	path := filepath.Join(dir, "not-gzip.gz")
	require.NoError(t, os.WriteFile(path, []byte("not actually gzipped"), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err := s.scanTarArchive(path, true, 0, &archiveScanState{})
	assert.Error(t, err)
}

// ---- scanEntryContent 错误分支 ----

func TestScanEntryContent_ReadError(t *testing.T) {
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// looksBinary 读 errReader → 返回 error
	_, err := s.scanEntryContent(errReader{}, "app.properties")
	assert.Error(t, err)
}

// ---- isNestedArchive ----

func TestIsNestedArchive(t *testing.T) {
	assert.True(t, isNestedArchive("lib/inner.jar"))
	assert.True(t, isNestedArchive("x.war"))
	assert.True(t, isNestedArchive("x.ear"))
	assert.True(t, isNestedArchive("x.zip"))
	assert.False(t, isNestedArchive("x.tar"))     // tar 不在 nestedArchiveExts
	assert.False(t, isNestedArchive("x.tar.gz"))  // ext=.gz，不在 map
	assert.False(t, isNestedArchive("x.txt"))
	assert.False(t, isNestedArchive("x.properties"))
}

// ---- scanArchiveFile 分支 ----

func TestScanArchiveFile_PlainFileFallback(t *testing.T) {
	// 非归档扩展名 → default 分支 → detector.ScanFile
	dir := t.TempDir()
	path := filepath.Join(dir, "app.properties")
	require.NoError(t, os.WriteFile(path, []byte("db.password = plain-secret\n"), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(path, "app.properties")
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}

func TestScanArchiveFile_ZipOpenError(t *testing.T) {
	// .jar 但文件不存在 → zip.OpenReader 失败
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err := s.scanArchiveFile("/nonexistent/x.jar", "x.jar")
	assert.Error(t, err)
}

func TestScanArchiveFile_TarPlain(t *testing.T) {
	// .tar（非 gzip）→ scanTarArchive gzipped=false
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.tar")
	makePlainTar(t, path, map[string]string{
		"application.properties": "db.password = tar-secret\n",
	})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(path, "plain.tar")
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}

// makePlainTar writes an uncompressed .tar with the given entries.
func makePlainTar(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
}

func TestScanZipReader_DirEntrySkipped(t *testing.T) {
	// 含目录 entry 的 zip → 目录被跳过，文件被扫
	dir := t.TempDir()
	path := filepath.Join(dir, "with-dir.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	// 目录 entry
	_, err = w.Create("META-INF/")
	require.NoError(t, err)
	// 文件 entry
	fw, err := w.Create("META-INF/application.properties")
	require.NoError(t, err)
	_, err = fw.Write([]byte("db.password = zip-dir-secret\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(path, "with-dir.zip")
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}

func TestScanZipReader_UnscannableEntrySkipped(t *testing.T) {
	// 含不可扫文件（.class）的 zip → 该 entry 跳过
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.zip")
	makeZip(t, path, map[string]string{
		"MyClass.class":          "binary garbage here",
		"application.properties": "db.password = mixed-secret\n",
	})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanArchiveFile(path, "mixed.zip")
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}

func TestScanNestedZipEntry_UnknownExt(t *testing.T) {
	// 嵌套 entry 扩展名未知 → 返回 (nil,nil)
	// 构造一个 zip，里面放 inner.bin（未知扩展名，但 isNestedArchive 返回 false
	// 所以不会进 scanNestedZipEntry）。要直接测 scanNestedZipEntry 需构造 zip.File。
	// 改用 .jar 内嵌 .bin 测试：isNestedArchive(.bin)=false，不会进嵌套。
	// 直接构造一个含 nested .zip 的外层 zip 来覆盖 scanNestedZipEntry 的 default 分支。
	dir := t.TempDir()
	outer := filepath.Join(dir, "outer.jar")
	// 内层 zip 内容
	var innerBuf bytes.Buffer
	iw := zip.NewWriter(&innerBuf)
	fw, err := iw.Create("data.bin")
	require.NoError(t, err)
	_, err = fw.Write([]byte("not a real archive"))
	require.NoError(t, err)
	require.NoError(t, iw.Close())
	// 外层 zip 含 nested.zip
	f, err := os.Create(outer)
	require.NoError(t, err)
	ow := zip.NewWriter(f)
	fw2, err := ow.Create("nested.zip")
	require.NoError(t, err)
	_, err = fw2.Write(innerBuf.Bytes())
	require.NoError(t, err)
	require.NoError(t, ow.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// nested.zip 是合法 zip，递归后内层 data.bin 不可扫 → 无 findings 但无错误
	findings, err := s.scanArchiveFile(outer, "outer.jar")
	require.NoError(t, err)
	// data.bin 不可扫，无 findings
	assert.Empty(t, findings)
}

// openZipFiles 打开 zip 文件并返回其 []*zip.File（用于直接测 scanZipReader）。
func openZipFiles(t *testing.T, path string) []*zip.File {
	t.Helper()
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })
	return r.File
}

func TestScanZipReader_EntryCountGuard(t *testing.T) {
	// 预设 st.totalEntries = maxEntriesPerArchive → 进入循环立即 break（line 179-182）
	dir := t.TempDir()
	path := filepath.Join(dir, "x.zip")
	makeZip(t, path, map[string]string{"app.properties": "db.password=x\n"})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{totalEntries: maxEntriesPerArchive}
	_, err := s.scanZipReader(openZipFiles(t, path), path, 0, st)
	require.NoError(t, err)
}

func TestScanZipReader_CumulativeSizeGuard(t *testing.T) {
	// 预设 st.totalDecompressed 接近上限 → entry 累加超限 break（line 196-200）
	dir := t.TempDir()
	path := filepath.Join(dir, "x.zip")
	makeZip(t, path, map[string]string{"app.properties": "db.password=x\n"})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// 留 1 字节余量，app.properties 解压后 > 1 字节 → 触发 cumulative guard
	st := &archiveScanState{totalDecompressed: maxDecompressedPerArchive - 1}
	_, err := s.scanZipReader(openZipFiles(t, path), path, 0, st)
	require.NoError(t, err)
}

func TestScanNestedZipEntry_TarGzBranch(t *testing.T) {
	// 嵌套 .tar.gz entry → scanNestedZipEntry 的 .gz 分支（line 262-263）
	dir := t.TempDir()
	// 手写一个 tar.gz：内层 tar 含 app.properties
	innerTarGz := filepath.Join(dir, "inner.tar.gz")
	tarBuf := &bytes.Buffer{}
	tw := tar.NewWriter(tarBuf)
	content := "db.password=tgzsecret\n"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	gzFile, err := os.Create(innerTarGz)
	require.NoError(t, err)
	gz := gzip.NewWriter(gzFile)
	_, err = gz.Write(tarBuf.Bytes())
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	gzFile.Close()

	// 把 inner.tar.gz 作为 entry 嵌入外层 zip
	outer := filepath.Join(dir, "outer.zip")
	innerBytes, err := os.ReadFile(innerTarGz)
	require.NoError(t, err)
	f, err := os.Create(outer)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("inner.tar.gz")
	require.NoError(t, err)
	_, err = fw.Write(innerBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	files := openZipFiles(t, outer)
	require.Equal(t, 1, len(files))
	findings, err := s.scanNestedZipEntry(files[0], 0, st)
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should recurse into tar.gz and find password")
}

func TestScanNestedZipEntry_TarBranch(t *testing.T) {
	// 嵌套 .tar entry → scanNestedZipEntry 的 .tar 分支（line 260-261）
	dir := t.TempDir()
	innerTar := filepath.Join(dir, "inner.tar")
	makePlainTar(t, innerTar, map[string]string{"app.properties": "db.password=tarsecret\n"})
	outer := filepath.Join(dir, "outer.zip")
	innerBytes, _ := os.ReadFile(innerTar)
	f, err := os.Create(outer)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("inner.tar")
	require.NoError(t, err)
	_, err = fw.Write(innerBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	findings, err := s.scanNestedZipEntry(openZipFiles(t, outer)[0], 0, st)
	require.NoError(t, err)
	assert.NotEmpty(t, findings)
}

func TestScanNestedTarEntry_TarAndGzBranches(t *testing.T) {
	// 直接测 scanNestedTarEntry 的 .tar 分支：reader 是 inner.tar 的字节流
	s := newScannerWithDetector(newDetectorForArchiveTest(t))

	dir := t.TempDir()
	// 内层 tar（含 .properties）
	innerTarPath := filepath.Join(dir, "inner.tar")
	makePlainTar(t, innerTarPath, map[string]string{"app.properties": "password = tarred-secret-789"})
	innerTarBytes, err := os.ReadFile(innerTarPath)
	require.NoError(t, err)

	// 直接把 inner.tar 字节流作为 entry reader 传入
	st := &archiveScanState{}
	findings, err := s.scanNestedTarEntry(bytes.NewReader(innerTarBytes), "inner.tar", 0, st)
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should find password in nested tar")
}

func TestScanNestedTarEntry_GzBranch(t *testing.T) {
	// scanNestedTarEntry 的 .gz 分支（line 368-369）：name=".gz" → scanTarArchive(true)
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	// 构造一个 inner.tar.gz（gzip 包装的 tar）
	tarBuf := &bytes.Buffer{}
	tw := tar.NewWriter(tarBuf)
	content := "password = gz-secret-789"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}))
	_, _ = tw.Write([]byte(content))
	require.NoError(t, tw.Close())
	gzBuf := &bytes.Buffer{}
	gz := gzip.NewWriter(gzBuf)
	_, _ = gz.Write(tarBuf.Bytes())
	require.NoError(t, gz.Close())

	st := &archiveScanState{}
	findings, err := s.scanNestedTarEntry(bytes.NewReader(gzBuf.Bytes()), "inner.tar.gz", 0, st)
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should find password in nested tar.gz")
}

func TestScanNestedTarEntry_CopyError(t *testing.T) {
	// scanNestedTarEntry 的 io.Copy err 分支（line 356-358）：reader 读失败
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	// errReader 总返回错误
	_, err := s.scanNestedTarEntry(errReader{}, "inner.tar", 0, st)
	assert.Error(t, err)
}

func TestScanNestedZipEntry_OpenError(t *testing.T) {
	// scanNestedZipEntry 的 f.Open err 分支（line 237-239）：构造一个外层 zip，
	// 其 nested entry 用 CreateRaw 声明 size 但数据不符 → f.Open 校验失败
	path := filepath.Join(t.TempDir(), "outer.zip")
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	hdr := &zip.FileHeader{Name: "nested.jar", Method: zip.Store, CompressedSize64: 5, UncompressedSize64: 10, CRC32: 0}
	fw, err := zw.CreateRaw(hdr)
	require.NoError(t, err)
	_, _ = fw.Write([]byte("short")) // 声明 10 但写 5
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()
	require.Equal(t, 1, len(r.File))
	// nested.jar 是 isNestedArchive，调 scanNestedZipEntry；f.Open 校验 size 失败
	_, err = s.scanNestedZipEntry(r.File[0], 0, &archiveScanState{})
	assert.Error(t, err)
}

func TestScanTarArchive_EntryCountGuard(t *testing.T) {
	// 预设 st.totalEntries = maxEntriesPerArchive → 进入循环立即 break（line 292-295）
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	makePlainTar(t, path, map[string]string{"app.properties": "db.password=x\n"})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{totalEntries: maxEntriesPerArchive}
	_, err := s.scanTarArchive(path, false, 0, st)
	require.NoError(t, err)
}

func TestScanTarArchive_CumulativeSizeGuard(t *testing.T) {
	// 预设 st.totalDecompressed 接近上限 → cumulative 超 break（line 314-317）
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	makePlainTar(t, path, map[string]string{"app.properties": "db.password=x\n"})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{totalDecompressed: maxDecompressedPerArchive - 1}
	_, err := s.scanTarArchive(path, false, 0, st)
	require.NoError(t, err)
}

func TestScanTarArchive_NonRegularEntrySkipped(t *testing.T) {
	// tar 含目录 entry（Typeflag != TypeReg）→ continue（line 305-307）
	dir := t.TempDir()
	path := filepath.Join(dir, "with-dir.tar")
	f, err := os.Create(path)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	// 目录 entry（非 TypeReg）→ continue
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "subdir/", Typeflag: tar.TypeDir, Mode: 0755}))
	// 文件 entry（深度 0，scanNestedArchive 不触发，因 isNestedArchive 对 .properties=false）
	content := "db.password=tarsecret\n"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	f.Close()

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	findings, err := s.scanTarArchive(path, false, 0, st)
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "should find password in app.properties entry")
}

func TestScanTarArchive_BadGzipHeader(t *testing.T) {
	// gzipped=true 但内容不是 gzip → gzip.NewReader 失败（line 281-283）
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.tar.gz")
	require.NoError(t, os.WriteFile(path, []byte("not gzip data"), 0644))
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err := s.scanTarArchive(path, true, 0, &archiveScanState{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "gzip")
}

func TestScanTarArchive_ReadHeaderError(t *testing.T) {
	// 损坏的 tar（无效 header）→ tr.Next 返回非 EOF 错误 → log + break（line 301-303）
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.tar")
	// 写一个 512 字节块（tar header 大小）但 magic 字段无效 → tr.Next 报错
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{0xff}, 512), 0644))
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err := s.scanTarArchive(path, false, 0, &archiveScanState{})
	// read header err 只 log + break，函数返回 nil err
	require.NoError(t, err)
}

func TestScanZipReader_EntrySizeGuard(t *testing.T) {
	// 单 entry UncompressedSize64 > maxDecompressedPerArchive → skip（line 186-190）
	// 用 CreateRaw 声明巨大 UncompressedSize 但写小数据
	dir := t.TempDir()
	path := filepath.Join(dir, "big.zip")
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	hdr := &zip.FileHeader{
		Name:               "big.properties",
		Method:             zip.Store,
		CompressedSize64:   5,
		UncompressedSize64: uint64(maxDecompressedPerArchive) * 2, // 2 GiB
		CRC32:              0,
	}
	fw, err := zw.CreateRaw(hdr)
	require.NoError(t, err)
	_, _ = fw.Write([]byte("short"))
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	_, err = s.scanZipReader(openZipFiles(t, path), path, 0, st)
	require.NoError(t, err) // guard 只 skip 不报错
}

func TestScanZipReader_CompressionRatioGuard(t *testing.T) {
	// UncompressedSize/CompressedSize > 100 → skip（line 191-195）
	// CompressedSize=1, UncompressedSize=200 → ratio 200
	dir := t.TempDir()
	path := filepath.Join(dir, "ratio.zip")
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	hdr := &zip.FileHeader{
		Name:               "ratio.properties",
		Method:             zip.Store,
		CompressedSize64:   1,
		UncompressedSize64: 200,
		CRC32:              0,
	}
	fw, err := zw.CreateRaw(hdr)
	require.NoError(t, err)
	_, _ = fw.Write([]byte("x")) // 写 1 字节匹配 CompressedSize
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	st := &archiveScanState{}
	_, err = s.scanZipReader(openZipFiles(t, path), path, 0, st)
	require.NoError(t, err)
}

func TestScanTarArchive_EntrySizeGuard(t *testing.T) {
	// tar entry hdr.Size > maxDecompressedPerArchive → skip（line 310-313）
	// 手写一个 tar header 声明 size=2GiB，不写内容（tar.Reader 读 header 后看到 size 超 limit 直接 skip）
	dir := t.TempDir()
	path := filepath.Join(dir, "big.tar")
	// tar header 是 512 字节块。用 tar.Format 写一个 header 但不写内容。
	// 简化：用 tar.Writer 写 header 后不 Close（直接 Close 会报 missed bytes），
	// 改用 raw bytes 构造：写 512 字节 header + 0 字节内容 + 2 个 512 字节全零块（EOF 标记）
	f, err := os.Create(path)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	hdr := &tar.Header{Name: "big.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(maxDecompressedPerArchive) * 2}
	// 写 header 到底层（不通过 WriteHeader，因它会期望后续 Write）
	// 实际上 WriteHeader 只写 header，Close 才校验 size
	require.NoError(t, tw.WriteHeader(hdr))
	// 不写内容，直接 flush tw 的内部缓冲到文件（不调 Close 避免校验）
	// tar.Writer.Close() 会写 EOF 块但先校验 size — 用 raw 方式
	f.Close()

	// 重新用 raw 写完整 tar（header + EOF）
	rawBuf := &bytes.Buffer{}
	rtw := tar.NewWriter(rawBuf)
	require.NoError(t, rtw.WriteHeader(&tar.Header{Name: "big.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(maxDecompressedPerArchive) * 2}))
	// 不写数据，直接 Close 会报 missed bytes — 所以不用 Close，手动追加 EOF 块
	// tar.Writer 内部缓冲已含 header；flush 它
	// 简化：直接写 2 个 512 全零块作为 EOF
	rawBuf.Write(bytes.Repeat([]byte{0}, 1024))
	require.NoError(t, os.WriteFile(path, rawBuf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err = s.scanTarArchive(path, false, 0, &archiveScanState{})
	require.NoError(t, err) // guard 只 skip 不报错
}
