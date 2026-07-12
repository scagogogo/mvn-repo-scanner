package scanner

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeZipWithUnsupportedMethod writes a zip whose entry uses compression method
// 1 (Shrunk) — unsupported by archive/zip, so f.Open() returns ErrAlgorithm
// with a nil rc. Used to cover the f.Open() error branches in scanZipReader
// (line 218) and scanNestedZipEntry (line 238). entryName controls whether the
// entry is a scannable file (scanZipReader path) or a nested archive
// (scanNestedZipEntry path).
func makeZipWithUnsupportedMethod(t *testing.T, path, entryName string) {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	fw, err := zw.Create(entryName)
	require.NoError(t, err)
	_, err = fw.Write([]byte("db.password = secret\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	data := buf.Bytes()

	// Patch both the local file header (method at PK\x03\x04 + 8) and the
	// central directory header (method at PK\x01\x02 + 10) to method 1.
	lh := bytes.Index(data, []byte("PK\x03\x04"))
	require.NotEqual(t, -1, lh)
	binary.LittleEndian.PutUint16(data[lh+8:], 1)
	cd := bytes.Index(data, []byte("PK\x01\x02"))
	require.NotEqual(t, -1, cd)
	binary.LittleEndian.PutUint16(data[cd+10:], 1)

	require.NoError(t, os.WriteFile(path, data, 0644))
}

// scanZipReader 的 scanEntryContent err 分支：Store 方法 + CreateRaw 声明 size 大于实际
// → f.Open 成功但 io.ReadFull 读到 unexpected EOF → looksBinary 返回 err。
func TestScanZipReader_ScanEntryContentError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.zip")
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	hdr := &zip.FileHeader{Name: "app.properties", Method: zip.Store, CompressedSize64: 5, UncompressedSize64: 20, CRC32: 0}
	fw, err := zw.CreateRaw(hdr)
	require.NoError(t, err)
	_, _ = fw.Write([]byte("short")) // 声明 20 但写 5
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()

	findings, err := s.scanZipReader(r.File, path, 0, &archiveScanState{})
	require.NoError(t, err) // scanEntryContent err 只 log 不返回
	assert.Empty(t, findings)
}

// scanTarArchive 的 !isScannableFile drain 分支：tar 含 .class（不可扫）文件，
// 需 io.Copy(io.Discard, tr) 排空 reader 前进到下一个 entry。
func TestScanTarArchive_UnscannableEntryDrained(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	// .class 不可扫 + .properties 可扫，验证 drain 后能继续扫到 .properties
	makePlainTar(t, path, map[string]string{
		"Foo.class":         "\xCA\xFE\xBA\xBE binary class",
		"app.properties":    "db.password = secret123456\n",
	})
	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanTarArchive(path, false, 0, &archiveScanState{})
	require.NoError(t, err)
	assert.NotEmpty(t, findings, "app.properties should still be scanned after draining .class")
}

// scanTarArchive 的 scanEntryContent err 分支：用 gzip 包装损坏内容，
// 让 tar reader 读取 entry 内容时失败。构造一个 entry 的 size 与实际数据不符，
// 使 scanEntryContent(tr,...) 读到 unexpected EOF。
func TestScanTarArchive_ScanEntryContentError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	// 手写 tar：header 声明 size=100 但只写 5 字节内容，不写 EOF 块
	// → tr.Next() 第一次返回 header，读内容时 io.ReadFull 不足 → scanEntryContent err
	rawBuf := &bytes.Buffer{}
	tw := tar.NewWriter(rawBuf)
	hdr := &tar.Header{Name: "app.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: 100}
	require.NoError(t, tw.WriteHeader(hdr))
	_, _ = rawBuf.Write([]byte("short")) // 声明 100 但写 5
	// 不调 tw.Close()（会校验 size），直接追加 EOF 块
	rawBuf.Write(bytes.Repeat([]byte{0}, 1024))
	require.NoError(t, os.WriteFile(path, rawBuf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanTarArchive(path, false, 0, &archiveScanState{})
	require.NoError(t, err) // scanEntryContent err 只 log 不返回
	assert.Empty(t, findings)
}

// scanNestedZipEntry 的 io.Copy err 分支 + scanZipReader 调它时的 log 路径：
// 嵌套 .jar 用 Deflate 但数据损坏 → f.Open 成功但 io.Copy(tmp, rc) 读失败。
func TestScanZipReader_NestedEntryCopyError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outer.zip")
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	hdr := &zip.FileHeader{Name: "nested.jar", Method: zip.Deflate, CompressedSize64: 10, UncompressedSize64: 100, CRC32: 0}
	fw, err := zw.CreateRaw(hdr)
	require.NoError(t, err)
	_, _ = fw.Write([]byte("XXXXXXXXXX")) // 损坏 deflate
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()

	// scanNestedZipEntry 直接调用：io.Copy 失败返回 err
	_, err = s.scanNestedZipEntry(r.File[0], 0, &archiveScanState{})
	assert.Error(t, err)

	// scanZipReader 调它：err 被 log，不返回
	findings, err := s.scanZipReader(r.File, path, 0, &archiveScanState{})
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// scanNestedZipEntry / scanNestedTarEntry 的 os.CreateTemp err 分支：
// TMPDIR 指向文件 → CreateTemp 失败。
func TestScanNestedZipEntry_CreateTempError(t *testing.T) {
	// 构造一个合法的嵌套 .jar（合法 zip 数据）让 f.Open 成功、走到 CreateTemp
	dir := t.TempDir()
	innerPath := filepath.Join(dir, "inner.jar")
	makeZip(t, innerPath, map[string]string{"app.properties": "x"})
	innerData, err := os.ReadFile(innerPath)
	require.NoError(t, err)
	outerPath := filepath.Join(dir, "outer.jar")
	makeZip(t, outerPath, map[string]string{"nested.jar": string(innerData)})

	// TMPDIR 指向文件 → os.CreateTemp 失败
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0644))
	t.Setenv("TMPDIR", tmpFile)

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(outerPath)
	require.NoError(t, err)
	defer r.Close()
	// 找到 nested.jar entry
	var nested *zip.File
	for _, f := range r.File {
		if f.Name == "nested.jar" {
			nested = f
		}
	}
	require.NotNil(t, nested)
	_, err = s.scanNestedZipEntry(nested, 0, &archiveScanState{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// scanNestedTarEntry 的 os.CreateTemp err 分支：TMPDIR 指向文件。
func TestScanNestedTarEntry_CreateTempError(t *testing.T) {
	// 用一个合法的 tar reader 作为嵌套 entry 内容
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "inner.tar")
	makePlainTar(t, tarPath, map[string]string{"app.properties": "x"})
	f, err := os.Open(tarPath)
	require.NoError(t, err)
	defer f.Close()

	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0644))
	t.Setenv("TMPDIR", tmpFile)

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	_, err = s.scanNestedTarEntry(f, "inner.tar", 0, &archiveScanState{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// scanTarArchive 调 scanNestedTarEntry 时 err 的 log 路径（line 322-325）：
// tar 含嵌套 .jar 但 TMPDIR 坏 → CreateTemp 失败 → err 被 log。
func TestScanTarArchive_NestedEntryErrorLogged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	// 构造一个含嵌套 inner.jar 的 tar
	innerJarPath := filepath.Join(dir, "inner.jar")
	makeZip(t, innerJarPath, map[string]string{"app.properties": "x"})
	innerData, err := os.ReadFile(innerJarPath)
	require.NoError(t, err)
	makePlainTar(t, path, map[string]string{"inner.jar": string(innerData)})

	// TMPDIR 坏 → scanNestedTarEntry 的 CreateTemp 失败
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0644))
	t.Setenv("TMPDIR", tmpFile)

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanTarArchive(path, false, 0, &archiveScanState{})
	require.NoError(t, err) // err 只 log 不返回
	assert.Empty(t, findings)
}

// scanTarArchive 的 scanEntryContent err 分支（line 337-340）：构造一个 raw tar，
// entry size 声明 1400 字节但文件截断到 header(512) + 200 内容 → 读 entry 内容时 EOF。
func TestScanTarArchive_ScanEntryContentError_Gzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tar")
	f, err := os.Create(path)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	content := bytes.Repeat([]byte("x plain line\n"), 100) // 1400 字节
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app.properties", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}))
	_, err = tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
	// 截断到 header(512) + 200 内容，让 scanEntryContent 读内容中途 EOF
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data[:712], 0644))

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	findings, err := s.scanTarArchive(path, false, 0, &archiveScanState{})
	require.NoError(t, err) // scanEntryContent err 只 log 不返回
	assert.Empty(t, findings)
}

// scanZipReader 的 f.Open() err 分支（line 218-220）：entry 用不支持的压缩方法
// (Shrunk) → f.Open 返回 ErrAlgorithm，rc 为 nil → log+continue，不 panic。
func TestScanZipReader_EntryOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.zip")
	makeZipWithUnsupportedMethod(t, path, "app.properties")

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()

	findings, err := s.scanZipReader(r.File, path, 0, &archiveScanState{})
	require.NoError(t, err) // f.Open err 只 log 不返回
	assert.Empty(t, findings)
}

// scanNestedZipEntry 的 f.Open() err 分支（line 238-240）：nested .jar 用不支持
// 的压缩方法 → f.Open 返回 ErrAlgorithm → 直接返回 err。
func TestScanNestedZipEntry_OpenUnsupportedMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outer.zip")
	makeZipWithUnsupportedMethod(t, path, "nested.jar")

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()
	require.Len(t, r.File, 1)

	_, err = s.scanNestedZipEntry(r.File[0], 0, &archiveScanState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported compression algorithm")
}

// scanNestedZipEntry 的 default return 分支（line 269）：直接调用并传入一个
// 扩展名既非 zip 系也非 tar 系的 entry（.bin）→ switch 无匹配 → return nil, nil。
// 正常调用链下 isNestedArchive 已过滤掉非归档扩展名，此处绕过契约直接覆盖 default。
func TestScanNestedZipEntry_DefaultReturn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.zip")
	makeZip(t, path, map[string]string{"data.bin": "not an archive"})

	s := newScannerWithDetector(newDetectorForArchiveTest(t))
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()
	require.Len(t, r.File, 1)

	findings, err := s.scanNestedZipEntry(r.File[0], 0, &archiveScanState{})
	require.NoError(t, err)
	assert.Nil(t, findings)
}
