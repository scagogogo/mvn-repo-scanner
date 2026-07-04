package detector

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
