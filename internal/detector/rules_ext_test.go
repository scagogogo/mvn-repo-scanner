package detector

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtendedRules_Count(t *testing.T) {
	rules := ExtendedRules()
	assert.Equal(t, 32, len(rules), "ExtendedRules should return 32 rules")
}

func TestExtendedRules_AllCompile(t *testing.T) {
	rules := ExtendedRules()
	det, err := NewDetector(rules)
	require.NoError(t, err, "all extended rules should compile without error")
	assert.NotNil(t, det)
}

func TestAllRules_AllCompile(t *testing.T) {
	rules := AllRules()
	det, err := NewDetector(rules)
	require.NoError(t, err, "all 38 rules should compile without error")
	assert.NotNil(t, det)
}

func TestExtendedRules_HaveValidSeverities(t *testing.T) {
	rules := ExtendedRules()
	validSev := map[Severity]bool{
		SeverityCritical: true,
		SeverityHigh:     true,
		SeverityMedium:   true,
		SeverityLow:      true,
	}
	for _, r := range rules {
		assert.True(t, validSev[r.Severity], "rule %s has invalid severity: %s", r.ID, r.Severity)
	}
}

func TestExtendedRules_HaveUniqueIDs(t *testing.T) {
	rules := ExtendedRules()
	seen := make(map[string]bool)
	for _, r := range rules {
		assert.False(t, seen[r.ID], "duplicate rule ID: %s", r.ID)
		seen[r.ID] = true
	}
}

func TestExtendedRules_NoDuplicateIDsWithCore(t *testing.T) {
	coreRules := DefaultRules()
	extRules := ExtendedRules()
	coreIDs := make(map[string]bool)
	for _, r := range coreRules {
		coreIDs[r.ID] = true
	}
	for _, r := range extRules {
		assert.False(t, coreIDs[r.ID], "extended rule %s collides with core rule ID", r.ID)
	}
}

func TestExtendedRules_SampleDetection(t *testing.T) {
	rules := ExtendedRules()
	det, err := NewDetector(rules)
	require.NoError(t, err)

	// Google API Key
	content := strings.NewReader(`my_key=AIzaSyA12345678901234567890123456789012`)
	findings, err := det.ScanContent(content, "config.properties")
	require.NoError(t, err)
	assert.True(t, len(findings) >= 1, "should detect Google API key")
	found := false
	for _, f := range findings {
		if f.RuleID == "google-api-key" {
			found = true
		}
	}
	assert.True(t, found, "should find google-api-key rule match")

	// Slack Token — 测试样本通过运行时拼接构造，避免源码中出现完整 token 字面量被误判为真实凭证
	slackToken := "xoxb-" + "0000000000-0000000000-" + "EXAMPLETESTPLACEHOLDER000"
	content2 := strings.NewReader("token=" + slackToken)
	findings2, err := det.ScanContent(content2, "config.json")
	require.NoError(t, err)
	assert.True(t, len(findings2) >= 1, "should detect Slack token")
}

func TestExtendedRules_GCPServiceAccount(t *testing.T) {
	rules := ExtendedRules()
	det, err := NewDetector(rules)
	require.NoError(t, err)

	content := strings.NewReader(`{
  "type": "service_account",
  "project_id": "my-project"
}`)
	findings, err := det.ScanContent(content, "service-account.json")
	require.NoError(t, err)
	assert.True(t, len(findings) >= 1, "should detect GCP service account")
}

func TestExtendedRules_MavenPassword(t *testing.T) {
	rules := ExtendedRules()
	det, err := NewDetector(rules)
	require.NoError(t, err)

	content := strings.NewReader(`<server><password>myS3cret!</password></server>`)
	findings, err := det.ScanContent(content, "settings.xml")
	require.NoError(t, err)
	assert.True(t, len(findings) >= 1, "should detect Maven password in XML")
}
