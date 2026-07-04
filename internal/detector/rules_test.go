package detector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()
	assert.Equal(t, 6, len(rules), "DefaultRules should return 6 core rules")

	knownIDs := map[string]bool{
		"hardcoded-password": true,
		"aws-secret-key":     true,
		"private-key":        true,
		"jdbc-credentials":   true,
		"github-token":       true,
		"generic-api-key":   true,
	}
	for _, r := range rules {
		assert.True(t, knownIDs[r.ID], "unexpected rule ID: %s", r.ID)
		assert.True(t, r.Enabled, "default rule %s should be enabled", r.ID)
		assert.NotEmpty(t, r.Name)
		assert.NotEmpty(t, r.Severity)
		assert.NotEmpty(t, r.Patterns)
	}
}

func TestLoadRules_EmptyString(t *testing.T) {
	rules, err := LoadRules("")
	require.NoError(t, err)
	assert.Equal(t, 6, len(rules), "empty rulesFile should return default rules")
}

func TestLoadRules_InvalidFile(t *testing.T) {
	_, err := LoadRules("/nonexistent/path/rules.yaml")
	assert.Error(t, err, "loading nonexistent file should error")
}

func TestLoadRules_ValidYAML(t *testing.T) {
	yamlContent := `rules:
  - id: test-rule
    name: Test Rule
    severity: HIGH
    description: "A test rule"
    patterns:
      - 'testpattern\d+'
    file_patterns:
      - '\.txt$'
    enabled: true
`
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlContent), 0644))

	rules, err := LoadRules(yamlPath)
	require.NoError(t, err)
	assert.Equal(t, 1, len(rules))
	assert.Equal(t, "test-rule", rules[0].ID)
	assert.Equal(t, SeverityHigh, rules[0].Severity)
}

func TestLoadRulesWithLevel_Core(t *testing.T) {
	rules, err := LoadRulesWithLevel("", "core", false)
	require.NoError(t, err)
	assert.Equal(t, 6, len(rules))
}

func TestLoadRulesWithLevel_Extended(t *testing.T) {
	rules, err := LoadRulesWithLevel("", "extended", false)
	require.NoError(t, err)
	assert.Equal(t, 32, len(rules), "ExtendedRules should return 32 rules")
}

func TestLoadRulesWithLevel_All(t *testing.T) {
	rules, err := LoadRulesWithLevel("", "all", false)
	require.NoError(t, err)
	assert.Equal(t, 38, len(rules), "AllRules should return 38 rules (6+32)")
}

func TestLoadRulesWithLevel_UnknownLevel(t *testing.T) {
	rules, err := LoadRulesWithLevel("", "unknown", false)
	require.NoError(t, err)
	assert.Equal(t, 6, len(rules), "unknown level should fall back to core")
}

func TestLoadRulesWithLevel_CustomFileOverridesLevel(t *testing.T) {
	yamlContent := `rules:
  - id: custom-rule
    name: Custom Rule
    severity: LOW
    description: "custom"
    patterns:
      - 'custom'
    enabled: true
`
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "custom.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlContent), 0644))

	rules, err := LoadRulesWithLevel(yamlPath, "core", false)
	require.NoError(t, err)
	assert.Equal(t, 1, len(rules), "custom file should override level setting when merge=false")
	assert.Equal(t, "custom-rule", rules[0].ID)
}

func TestLoadRulesWithLevel_CustomFileMergesWithBuiltin(t *testing.T) {
	// A custom rule that overrides the built-in hardcoded-password severity,
	// plus a brand-new rule. With merge=true the result is 6 builtin + 1 new = 7
	// (the overridden rule replaces in place, not appended).
	yamlContent := `rules:
  - id: hardcoded-password
    name: Hardcoded Password (Custom)
    severity: HIGH
    description: "customized severity"
    patterns:
      - '(?i)(password|passwd)\s*=\s*\S+'
    file_patterns:
      - '\.properties$'
    enabled: true
  - id: my-company-token
    name: My Company Token
    severity: HIGH
    description: "internal token"
    patterns:
      - 'MYTOKEN-[A-Za-z0-9]{32}'
    file_patterns:
      - '\.properties$'
    enabled: true
`
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "custom.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlContent), 0644))

	rules, err := LoadRulesWithLevel(yamlPath, "core", true)
	require.NoError(t, err)
	assert.Equal(t, 7, len(rules), "merge should yield 6 builtin + 1 new custom = 7 (override replaces in place)")

	// The overridden rule should carry the custom severity.
	var hp *Rule
	for _, r := range rules {
		if r.ID == "hardcoded-password" {
			hp = r
		}
	}
	require.NotNil(t, hp, "hardcoded-password should still be present (overridden, not removed)")
	assert.Equal(t, SeverityHigh, hp.Severity, "custom rule with same ID overrides built-in")
	assert.Equal(t, "Hardcoded Password (Custom)", hp.Name)

	// The new rule should be appended.
	var tok *Rule
	for _, r := range rules {
		if r.ID == "my-company-token" {
			tok = r
		}
	}
	require.NotNil(t, tok, "new custom rule should be added on merge")
}
