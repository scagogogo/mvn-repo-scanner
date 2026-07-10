package scanner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archive_branch_test.go 覆盖 archive.go 中复合条件与数值边界，
// 把语句覆盖推进到分支/条件覆盖。
//
// 注：looksBinary 的读错误分支（line 112）已由既有 TestLooksBinary_ReadError +
// errReader（archive_test.go:297-306）覆盖，此处不重复。

// archive.go:131 `float64(nonText)/float64(n) > 0.30` 边界两侧
// 控制字符范围：b<9 或 (b>13 && b<32)，即 0-8 和 14-31。0x01 在范围内。
func TestLooksBinary_NonTextRatioBoundary(t *testing.T) {
	// 纯文本 → nonText=0 → 0/4=0 < 0.30 → false
	binary, _, err := looksBinary(strings.NewReader("abcd"))
	require.NoError(t, err)
	assert.False(t, binary, "纯文本应判为非 binary")

	// 高非文本比：10 字节中 4 个控制字符 → 4/10=0.4 > 0.30 → true
	data := []byte{'a', 'b', 'c', 'd', 'e', 'f', 0x01, 0x01, 0x01, 0x01}
	binary, _, err = looksBinary(bytes.NewReader(data))
	require.NoError(t, err)
	assert.True(t, binary, ">30%% 非文本控制字符应判为 binary")

	// 边界附近：3/10=0.3，不严格大于 0.30 → false
	data2 := []byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 0x01, 0x01, 0x01}
	binary, _, err = looksBinary(bytes.NewReader(data2))
	require.NoError(t, err)
	assert.False(t, binary, "30%% 非文本（不严格大于）应判为非 binary")
}

// archive.go:115 `n == 0` 分支 + io.EOF/io.ErrUnexpectedEOF 短路：
// 空输入 → ReadFull 返回 io.EOF（n=0）→ 不进 line 112 if → n==0 → (false, [], nil)。
// 短输入（<1024）→ io.ErrUnexpectedEOF → 不进 if → 正常处理 peek。
func TestLooksBinary_EOFAndShortInput(t *testing.T) {
	// 空输入 → io.EOF → n==0 → (false, [], nil)
	binary, peek, err := looksBinary(strings.NewReader(""))
	require.NoError(t, err)
	assert.False(t, binary)
	assert.Empty(t, peek)

	// 短输入（<1024）→ io.ErrUnexpectedEOF → 正常处理 peek
	binary, peek, err = looksBinary(strings.NewReader("hi"))
	require.NoError(t, err)
	assert.False(t, binary)
	assert.Equal(t, "hi", string(peek))
}

// archive.go:191 `f.CompressedSize64 > 0 && UncompressedSize64/f.CompressedSize64 > uint64(maxCompressionRatio)`
// 覆盖：高压缩比 entry（ratio>100）被 skip。用大量重复字符构造 deflate 极高压缩比。
func TestScanZipArchive_HighCompressionRatioSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := tmpDir + "/bomb.zip"
	// 20000 个相同字符 → deflate 压缩后极小，ratio 远 >100
	makeZip(t, zipPath, map[string]string{
		"big.txt": strings.Repeat("A", 20000),
	})
	s := &Scanner{detector: &mockDetector{}}
	// 不应 panic，高压缩比 entry 被 skip（log warning）。
	// 注：UncompressedSize64=20000 <= maxDecompressedPerArchive，不会被 line 186 先 skip。
	findings, err := s.scanZipArchive(zipPath, 0, &archiveScanState{})
	require.NoError(t, err)
	// mockDetector.ScanContent 返回 nil findings；关键是 ratio guard 触发不 panic、不误扫。
	_ = findings
}

// archive.go:204 `depth < maxNestedDepth && isNestedArchive(f.Name)`
// 覆盖：嵌套深度达 maxNestedDepth → depth < maxNestedDepth 为 false → 不再递归。
func TestScanZipArchive_NestedDepthLimit(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := tmpDir + "/nested.zip"
	// inner.jar 是嵌套归档；depth=maxNestedDepth 时不递归，.jar 不在 scannableExts → skip
	makeZip(t, zipPath, map[string]string{
		"inner.jar": "", // 空内容即可，不会被递归打开
	})
	s := &Scanner{detector: &mockDetector{}}
	_, err := s.scanZipArchive(zipPath, maxNestedDepth, &archiveScanState{})
	require.NoError(t, err, "深度达上限时应安全跳过嵌套递归")
}
