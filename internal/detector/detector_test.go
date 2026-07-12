package detector

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// osWriteFile is a small wrapper to keep test bodies tidy.
func osWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func TestDetector_ScanContent_Password(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)

	content := strings.NewReader(`
# application.properties
db.host=localhost
db.password=S3cretP@ssw0rd
db.user=admin
`)
	findings, err := d.ScanContent(content, "application.properties")
	require.NoError(t, err)

	assert.True(t, len(findings) >= 1, "should find at least 1 password")
	assert.Equal(t, "hardcoded-password", findings[0].RuleID)
	assert.Equal(t, SeverityCritical, findings[0].Severity)
}

func TestDetector_ScanContent_PrivateKey(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)

	content := strings.NewReader(`-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHB7
-----END RSA PRIVATE KEY-----`)
	findings, err := d.ScanContent(content, "id_rsa.pem")
	require.NoError(t, err)

	assert.True(t, len(findings) >= 1)
	assert.Equal(t, "private-key", findings[0].RuleID)
}

func TestDetector_ScanContent_NoMatch(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)

	content := strings.NewReader(`server.port=8080
spring.application.name=myapp
`)
	findings, err := d.ScanContent(content, "application.properties")
	require.NoError(t, err)
	assert.Equal(t, 0, len(findings), "should not find sensitive content in safe config")
}

func TestDetector_FilePatternFiltering(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)

	// .class files should not match any rule's file pattern
	findings, err := d.ScanContent(strings.NewReader("password=secret"), "MyClass.class")
	require.NoError(t, err)
	assert.Equal(t, 0, len(findings), ".class files should be skipped")
}

func TestSeverity_IsValid(t *testing.T) {
	assert.True(t, SeverityCritical.IsValid())
	assert.True(t, SeverityHigh.IsValid())
	assert.True(t, SeverityMedium.IsValid())
	assert.True(t, SeverityLow.IsValid())
	assert.False(t, Severity("UNKNOWN").IsValid())
}

func TestRule_String(t *testing.T) {
	r := Rule{ID: "test-rule", Severity: SeverityCritical, Name: "Test Rule"}
	assert.Equal(t, "test-rule [CRITICAL] Test Rule", r.String())
}

func TestFinding_String(t *testing.T) {
	f := Finding{
		FilePath:   "application.properties",
		LineNumber: 5,
		Severity:   SeverityHigh,
		RuleID:     "aws-secret-key",
		Match:      "AKIAIOSFODNN7EXAMPLE",
	}
	assert.Equal(t, "application.properties:5 [HIGH] aws-secret-key: AKIAIOSFODNN7EXAMPLE", f.String())
}

// ---- NewDetector 错误分支 ----

func TestNewDetector_BadFilePattern(t *testing.T) {
	// 非法正则 file pattern → compileFilePatterns 失败
	rules := []*Rule{{ID: "r1", Enabled: true, FilePatterns: []string{"["}}}
	_, err := NewDetector(rules)
	assert.Error(t, err)
}

func TestNewDetector_BadRegexPattern(t *testing.T) {
	// engine=regex 但 pattern 非法 → RegexEngine.Compile 失败
	rules := []*Rule{{ID: "r1", Enabled: true, Engine: string(EngineRegex), Patterns: []string{"["}}}
	_, err := NewDetector(rules)
	assert.Error(t, err)
}

func TestNewDetector_UnknownEngine(t *testing.T) {
	// Engine 指向不存在的引擎 → engineRegistry.Get 会 fallback 到 regex 引擎
	// （历史默认行为），所以不会因 unknown engine 报错；pattern 合法即应成功。
	rules := []*Rule{{ID: "r1", Enabled: true, Engine: "nonexistent", Patterns: []string{"x"}}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	require.NotNil(t, d)
}

func TestNewDetector_EngineRegistryEmpty(t *testing.T) {
	// 通过 option 清空 engines map（模拟 Get 返回 nil 的极端情况）
	// 覆盖 line 161-163 的 engine==nil 防御分支
	clearEngines := func(d *Detector) {
		d.engines.engines = make(map[EngineName]RuleEngine)
	}
	rules := []*Rule{{ID: "r1", Enabled: true, Patterns: []string{"x"}}}
	_, err := NewDetector(rules, clearEngines)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine")
}

func TestDetector_ScanReader_NilEngineSkipped(t *testing.T) {
	// scanReader 中 eng==nil continue 分支（line 231-233）
	// 构造一个 engines 被清空的 Detector，调用 ScanContent
	d := &Detector{
		rules:   []*Rule{{ID: "r1", Enabled: true, Patterns: []string{"x"}}},
		engines: &engineRegistry{engines: make(map[EngineName]RuleEngine)}, // 空
	}
	// ScanContent → matchingRules 返回规则 → scanReader → Get 返回 nil → continue
	findings, err := d.ScanContent(strings.NewReader("x"), "f.txt")
	require.NoError(t, err)
	assert.Nil(t, findings)
}

func TestNewDetector_DisabledRuleSkipped(t *testing.T) {
	// disabled 规则即使 pattern 非法也不应报错（被跳过）
	rules := []*Rule{{ID: "r1", Enabled: false, Patterns: []string{"["}}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	require.NotNil(t, d)
}

func TestNewDetector_DefaultEngine(t *testing.T) {
	// Engine="" → 默认 regex
	rules := []*Rule{{ID: "r1", Enabled: true, Patterns: []string{"secret"}}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, err := d.ScanContent(strings.NewReader("secret=abc"), "f.txt")
	require.NoError(t, err)
	assert.True(t, len(findings) >= 1)
}

func TestWithEngine_CustomEngine(t *testing.T) {
	// 注册一个自定义引擎
	custom := &capturingEngine{}
	rules := []*Rule{{ID: "r1", Enabled: true, Engine: "custom", Patterns: []string{"x"}}}
	d, err := NewDetector(rules, WithEngine(custom))
	require.NoError(t, err)
	// 自定义引擎被调用
	findings, _ := d.ScanContent(strings.NewReader("hello"), "f.txt")
	assert.NotEmpty(t, findings)
	assert.Equal(t, "custom", findings[0].EngineName)
}

// capturingEngine 是个测试用 RuleEngine，匹配任何行返回一个 finding。
type capturingEngine struct{}

func (e *capturingEngine) Name() EngineName             { return "custom" }
func (e *capturingEngine) Compile(rule *Rule) error      { return nil }
func (e *capturingEngine) Match(line string, rule *Rule, fp string, ln int) []Finding {
	return []Finding{{RuleID: rule.ID, FilePath: fp, LineNumber: ln, Match: line, EngineName: "custom"}}
}

// ---- ScanFile ----

func TestDetector_ScanFile(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)

	// 写一个临时文件包含密码
	tmpDir := t.TempDir()
	path := tmpDir + "/app.properties"
	require.NoError(t, osWriteFile(path, []byte("db.password=S3cretP@ssw0rd\n"), 0644))

	findings, err := d.ScanFile(path)
	require.NoError(t, err)
	assert.True(t, len(findings) >= 1)
}

func TestDetector_ScanFile_OpenError(t *testing.T) {
	rules := DefaultRules()
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 不存在的文件
	_, err = d.ScanFile("/nonexistent/path/file.properties")
	assert.Error(t, err)
}

func TestDetector_ScanFile_NoMatchingRules(t *testing.T) {
	// 用一条 FilePatterns 只匹配 .properties 的规则；扫 .class 文件 → matchingRules
	// 为空 → ScanFile 直接返回 (nil,nil) 不读文件
	rules := []*Rule{{
		ID: "r1", Enabled: true,
		Patterns:     []string{"password="},
		FilePatterns: []string{`.*\.properties`},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, err := d.ScanFile("/nonexistent/MyClass.class")
	require.NoError(t, err)
	assert.Nil(t, findings)
}

// ---- RegexEngine 分支 ----

func TestDetector_ScanContent_NoMatchingRules(t *testing.T) {
	// ScanContent 路径：matchingRules 为空 → 直接返回 (nil,nil) 不读 reader
	rules := []*Rule{{
		ID: "r1", Enabled: true,
		Patterns:     []string{"password="},
		FilePatterns: []string{`.*\.properties`},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, err := d.ScanContent(strings.NewReader("password=x"), "/nonexistent/MyClass.class")
	require.NoError(t, err)
	assert.Nil(t, findings)
}

func TestRegexEngine_Ignorecase(t *testing.T) {
	rules := []*Rule{{ID: "r1", Enabled: true, Ignorecase: true, Patterns: []string{"SECRET"}}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 大小写不敏感 → 应匹配小写
	findings, _ := d.ScanContent(strings.NewReader("value=secret_here"), "f.txt")
	assert.NotEmpty(t, findings)
}

func TestRegexEngine_CaptureGroup(t *testing.T) {
	// CaptureGroup=1 → 报告捕获组而非整行
	rules := []*Rule{{
		ID: "r1", Enabled: true,
		Patterns:     []string{`password=(\S+)`},
		CaptureGroup: 1,
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, _ := d.ScanContent(strings.NewReader("password=TopSecret123"), "f.txt")
	require.NotEmpty(t, findings)
	assert.Equal(t, "TopSecret123", findings[0].Match)
}

func TestRegexEngine_Allowlist(t *testing.T) {
	// 匹配值在 allowlist 中 → 被抑制
	rules := []*Rule{{
		ID: "r1", Enabled: true,
		Patterns:  []string{`(AKIA[A-Z0-9]+EXAMPLE)`},
		Allowlist: []string{"EXAMPLE"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	findings, _ := d.ScanContent(strings.NewReader("key=AKIAIOSFODNN7EXAMPLE"), "f.txt")
	assert.Empty(t, findings)
}

func TestRegexEngine_LengthBounds(t *testing.T) {
	// MinLength/MaxLength 过滤
	rules := []*Rule{{
		ID: "r1", Enabled: true,
		Patterns:  []string{`token=(\S+)`},
		CaptureGroup: 1,
		MinLength: 50, // 要求至少 50 字符
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 短 token 被过滤
	findings, _ := d.ScanContent(strings.NewReader("token=short"), "f.txt")
	assert.Empty(t, findings)
	// 长 token 通过
	findings, _ = d.ScanContent(strings.NewReader("token="+strings.Repeat("a", 60)), "f.txt")
	assert.NotEmpty(t, findings)
}

// ---- EntropyEngine 分支 ----

func TestEntropyEngine_DefaultsApplied(t *testing.T) {
	// Entropy=nil → Compile 应用默认值
	rule := &Rule{ID: "e1", Enabled: true, Engine: string(EngineEntropy)}
	e := &EntropyEngine{}
	require.NoError(t, e.Compile(rule))
	assert.Equal(t, 4.5, rule.Entropy.Threshold)
	assert.Equal(t, 32, rule.Entropy.Window)
	assert.Equal(t, 20, rule.Entropy.MinLength)
	assert.Equal(t, "base64", rule.Entropy.Charset)
}

func TestEntropyEngine_HighEntropy(t *testing.T) {
	// 高熵 base64 token → 命中
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 32, MinLength: 20, Charset: "base64"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 32 字符高熵 base64
	tok := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYE"
	findings, _ := d.ScanContent(strings.NewReader("key="+tok), "f.txt")
	assert.NotEmpty(t, findings)
	assert.Equal(t, "entropy", findings[0].EngineName)
}

func TestEntropyEngine_SlidingWindow(t *testing.T) {
	// Window < token 长度 → 走滑动窗口分支
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy: &EntropyConfig{Threshold: 3.5, Window: 16, MinLength: 20, Charset: "hex"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 16 字符窗口内需有足够多的不同字符才达熵 3.5；用前 16 个 hex 字符凑高熵
	tok := "0123456789abcdef0123456789abcdef" // 32 chars hex, 每 16 窗口熵 ≈ 4.0
	findings, _ := d.ScanContent(strings.NewReader("key="+tok), "f.txt")
	assert.NotEmpty(t, findings)
}

func TestEntropyEngine_Allowlist(t *testing.T) {
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy:  &EntropyConfig{Threshold: 4.5, Window: 32, MinLength: 20, Charset: "base64"},
		Allowlist: []string{"wJalrXUtnFEMI"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	tok := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYE"
	findings, _ := d.ScanContent(strings.NewReader("key="+tok), "f.txt")
	assert.Empty(t, findings)
}

func TestEntropyEngine_CharsetFilter(t *testing.T) {
	// charset=hex 但 token 含非 hex 字符 → charsetOnly 返回 false 不命中
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 0, MinLength: 20, Charset: "hex"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 含 g/h 等非 hex 字符
	findings, _ := d.ScanContent(strings.NewReader("key=ghijklmnopqrstuvwx"), "f.txt")
	assert.Empty(t, findings)
}

// ---- 工具函数 ----

func TestEntropyEngine_SlidingWindow_CharsetSkip(t *testing.T) {
	// Window < token 长度且部分窗口含非 charset 字符 → 走 sliding charsetOnly continue
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy: &EntropyConfig{Threshold: 3.5, Window: 8, MinLength: 20, Charset: "hex"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 前缀含非 hex 字符（ghij），后缀全 hex；滑动窗口在前缀区会因 charsetOnly=false 被 continue
	tok := "ghijklmnop0123456789abcdef"
	findings, _ := d.ScanContent(strings.NewReader("key="+tok), "f.txt")
	_ = findings
}

func TestEntropyEngine_TokenShorterThanMinLength(t *testing.T) {
	// token 长度 < cfg.MinLength → line 178 continue
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy),
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 32, MinLength: 50, Charset: "base64"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 短 token（len=10 < 50）应被跳过，无 finding
	findings, _ := d.ScanContent(strings.NewReader("key=shorttoken"), "f.txt")
	assert.Empty(t, findings)
}

func TestEntropyEngine_RuleLengthBoundsRejects(t *testing.T) {
	// rule 级 MaxLength 过滤：token 超过 MaxLength → withinLengthBounds=false → continue
	rules := []*Rule{{
		ID: "e1", Enabled: true, Engine: string(EngineEntropy), MaxLength: 5,
		Entropy: &EntropyConfig{Threshold: 4.5, Window: 32, MinLength: 1, Charset: "base64"},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// 长 token 超过 MaxLength=5 → 被过滤
	findings, _ := d.ScanContent(strings.NewReader("key="+strings.Repeat("a", 60)), "f.txt")
	assert.Empty(t, findings)
}

func TestDetector_MatchingRules_DisabledRule(t *testing.T) {
	// matchingRules 中 !rule.Enabled continue 分支
	rules := []*Rule{{
		ID: "r1", Enabled: false,
		Patterns: []string{"password="},
	}}
	d, err := NewDetector(rules)
	require.NoError(t, err)
	// disabled 规则不参与 matchingRules → ScanContent 无匹配返回 nil
	findings, err := d.ScanContent(strings.NewReader("password=x"), "f.properties")
	require.NoError(t, err)
	assert.Nil(t, findings)
}

func TestCharsetOnly(t *testing.T) {
	assert.True(t, charsetOnly("a1b2c3d4", "hex"))
	assert.False(t, charsetOnly("xyz", "hex"))
	assert.True(t, charsetOnly("abc123+/", "base64"))
	assert.False(t, charsetOnly("abc def", "base64")) // 空格非法
	assert.True(t, charsetOnly("abc123", "ascii"))
	assert.False(t, charsetOnly("ab\tc", "ascii")) // tab 非法（<33）
}

func TestShannonEntropy(t *testing.T) {
	assert.Equal(t, 0.0, shannonEntropy("", "base64"))
	// 全相同字符 → 熵为 0
	assert.Equal(t, 0.0, shannonEntropy("aaaa", "base64"))
	// 高熵字符串 → 熵 > 3
	assert.True(t, shannonEntropy("wJalrXUtnFEMI", "base64") > 3)
}

func TestIsAllowlisted(t *testing.T) {
	// 空 allowlist → 总是 false
	assert.False(t, isAllowlisted("anything", &Rule{}))
	// 精确匹配（大小写不敏感）
	assert.True(t, isAllowlisted("EXAMPLE", &Rule{Allowlist: []string{"example"}}))
	// 子串匹配，allowlist 项长度 >= 4
	assert.True(t, isAllowlisted("AKIAIOSFODNN7EXAMPLE", &Rule{Allowlist: []string{"EXAMPLE"}}))
	// 子串匹配但 allowlist 项长度 < 4 → 不匹配
	assert.False(t, isAllowlisted("xxxaxxx", &Rule{Allowlist: []string{"a"}}))
}

func TestWithinLengthBounds(t *testing.T) {
	assert.False(t, withinLengthBounds("ab", &Rule{MinLength: 5}))
	assert.False(t, withinLengthBounds("abcdef", &Rule{MaxLength: 3}))
	assert.True(t, withinLengthBounds("abc", &Rule{MinLength: 1, MaxLength: 5}))
	assert.True(t, withinLengthBounds("abc", &Rule{})) // 无限制
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("  abc  ", 10))   // trim 后短于 max
	assert.Equal(t, "ab...", truncate("abcdef", 2))    // 超长截断
	assert.Equal(t, "", truncate("    ", 5))           // trim 后空
}
