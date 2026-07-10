package detector

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engine_branch_test.go 覆盖 engine.go 中复合布尔条件 (&&/||) 的短路组合，
// 把语句覆盖（已 100%）推进到分支/条件覆盖：每个 && / || 的两侧真值都至少触发一次。

// engine.go:78 `rule.Ignorecase && !strings.Contains(expr, "(?i)")`
// 覆盖关键短路组合：Ignorecase=true + 表达式已含 (?i) → !Contains 为 false → 短路不重复加前缀。
// （Ignorecase=false 不进 if、Ignorecase=true+不含(?i) 进 if 加前缀，两者已被 TestRegexEngine_Ignorecase 覆盖。）
func TestRegexEngine_Compile_IgnorecaseBranchCombos(t *testing.T) {
	// Ignorecase=true + 表达式已含 (?i) → 短路：不重复加前缀（(?i)(?i)SECRET 仍合法）
	rules := []*Rule{{
		ID: "r-already-ci", Enabled: true, Ignorecase: true,
		Patterns: []string{`(?i)SECRET`},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 短路分支仍应正确匹配小写 secret（证明前缀逻辑未破坏匹配）
	findings, _ := d.ScanContent(strings.NewReader("val=secret"), "f.txt")
	assert.NotEmpty(t, findings, "(?i) 前缀已存在时短路分支仍应正确匹配")
}

// engine.go:103-104 `CaptureGroup>=0 && CaptureGroup<len(m)` + `g != ""`
// 覆盖两个回退到 fullMatch 的组合：
//   - CaptureGroup 越界（>= len(m)）→ 第一个条件 false → 用 fullMatch
//   - CaptureGroup 有效但捕获组为空（g==""）→ 第二个条件 false → 用 fullMatch
func TestRegexEngine_CaptureGroup_BoundaryCombos(t *testing.T) {
	// 组合 1: CaptureGroup 越界 → 用 fullMatch
	rules := []*Rule{{
		ID: "r-oob", Enabled: true,
		Patterns:     []string{`password=(\S+)`},
		CaptureGroup: 5, // 越界：只有 1 个捕获组
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, _ := d.ScanContent(strings.NewReader("password=TopSecret123"), "f.txt")
	require.NotEmpty(t, findings)
	assert.Equal(t, "password=TopSecret123", findings[0].Match, "越界时回退到 fullMatch")

	// 组合 2: CaptureGroup 有效但组为空 → 用 fullMatch
	// (\d*) 在 abc 前匹配空串 → group 1 为空 → 回退 fullMatch
	rules2 := []*Rule{{
		ID: "r-emptygroup", Enabled: true,
		Patterns:     []string{`password=(\d*)`},
		CaptureGroup: 1,
	}}
	d2, err := NewDetector(rules2)
	require.NoError(t, err)
	f2, _ := d2.ScanContent(strings.NewReader("password=abc"), "f.txt")
	require.NotEmpty(t, f2)
	assert.Equal(t, "password=", f2[0].Match, "空捕获组回退到 fullMatch")
}

// engine.go:188 `shannonEntropy>=Threshold && charsetOnly` 与
// engine.go:205 `best>=Threshold && bestStr != ""`
// 覆盖短路两侧：
//   - 高熵但 charset 不纯（charsetOnly=false）→ 188 短路，best 不更新 → 205 因 bestStr=="" 不报告
//   - charset 符合但低熵（best<Threshold）→ 205 短路不报告
func TestEntropyEngine_BranchCombos(t *testing.T) {
	// 组合 1: 整 token 评分（Window=0 ⇒ 评分整 token），高熵但含非 hex 字符 → charsetOnly=false 短路
	rules := []*Rule{{
		ID: "r-charset-fail", Enabled: true, Engine: "entropy",
		Entropy: &EntropyConfig{Threshold: 3.0, Window: 0, MinLength: 10, Charset: "hex"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// abcdefghijklmnop 含 g~p 等非 hex 字符 → charsetOnly(hex) 返回 false → 188 短路 → 不报告
	f, _ := d.ScanContent(strings.NewReader("token=abcdefghijklmnop"), "f.txt")
	assert.Empty(t, f, "非 hex charset 的 token 应被 charsetOnly 短路")

	// 组合 2: charset 符合但熵低于 threshold → best(=0.0) < Threshold → 205 短路不报告
	rules2 := []*Rule{{
		ID: "r-low-entropy", Enabled: true, Engine: "entropy",
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 0, MinLength: 8, Charset: "hex"},
	}}
	d2, err := NewDetector(rules2)
	require.NoError(t, err)
	// 全 0 的 hex 串，熵=0 < 4.5 → 188 的 shannonEntropy>=Threshold 为 false → best 不更新
	f2, _ := d2.ScanContent(strings.NewReader("token=0000000000000000"), "f.txt")
	assert.Empty(t, f2, "低熵 hex 串应因 best<Threshold 不报告")
}

// engine.go:292 `strings.Contains(lower, strings.ToLower(a)) && len(a) >= 4`
// 精确匹配（lower==ToLower(a)）已在 TestIsAllowlisted 覆盖。这里补子串匹配的短路组合：
//   - 子串在 + len>=4 → true（EXAMPLE 在 my-EXAMPLE-token 中）
//   - 子串在 + len<4 → Contains=true 但 len<4 → false（a="a" 在 "xax" 中）
//   - 子串不在 → Contains=false → 短路（a="zz" 不在 match 中，精确匹配也失败）
func TestIsAllowlisted_SubstringBranchCombos(t *testing.T) {
	// 子串在 + len>=4 → true
	assert.True(t, isAllowlisted("my-EXAMPLE-token", &Rule{Allowlist: []string{"EXAMPLE"}}))
	// 子串在 + len<4 → Contains=true 但 len(a)=1 < 4 → false
	assert.False(t, isAllowlisted("xax", &Rule{Allowlist: []string{"a"}}))
	// 子串不在 → Contains=false 短路（精确匹配也失败）→ false
	assert.False(t, isAllowlisted("AKIAIOSFODNN7EXAMPLE", &Rule{Allowlist: []string{"zz"}}))
}
