package detector

import (
	"os"

	"gopkg.in/yaml.v3"
)

// rulesConfig is the YAML structure for rules configuration files.
type rulesConfig struct {
	Rules []*Rule `yaml:"rules"`
}

// DefaultRules returns the built-in detection rules.
func DefaultRules() []*Rule {
	return []*Rule{
		{
			ID:          "hardcoded-password",
			Name:        "Hardcoded Password",
			Severity:    SeverityCritical,
			Description: "Detects hardcoded passwords in configuration files",
			Patterns:    []string{`(?i)(password|passwd|pwd)\s*[=:]\s*["\']?[^\s"\'{}]{6,}`},
			FilePatterns: []string{
				`\.properties$`, `\.xml$`, `\.ya?ml$`,
				`\.json$`, `\.conf$`, `\.cfg$`, `\.ini$`,
			},
			Enabled: true,
		},
		{
			ID:          "aws-secret-key",
			Name:        "AWS Secret Key",
			Severity:    SeverityHigh,
			Description: "Detects AWS secret access keys",
			Patterns:    []string{`(?:A3T[A-Z0-9]|AKIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ASIA)[A-Z0-9]{16}`},
			FilePatterns: []string{`\..+$`},
			Enabled:     true,
		},
		{
			ID:          "private-key",
			Name:        "Private Key",
			Severity:    SeverityCritical,
			Description: "Detects PEM format private keys",
			Patterns:    []string{`-----BEGIN (?:RSA |EC |DSA )?PRIVATE KEY-----`},
			FilePatterns: []string{`\..+$`},
			Enabled:     true,
		},
		{
			ID:          "jdbc-credentials",
			Name:        "JDBC Credentials",
			Severity:    SeverityHigh,
			Description: "Detects credentials in JDBC connection strings",
			Patterns:    []string{`jdbc:[a-z]+://[^\s]+[?&](?:user|password)=[^\s&]+`},
			FilePatterns: []string{`\.properties$`, `\.xml$`, `\.ya?ml$`},
			Enabled:     true,
		},
		{
			ID:          "github-token",
			Name:        "GitHub Token",
			Severity:    SeverityHigh,
			Description: "Detects GitHub personal access tokens",
			Patterns:    []string{`gh[ps]_[A-Za-z0-9_]{36,255}`},
			FilePatterns: []string{`\..+$`},
			Enabled:     true,
		},
		{
			ID:          "generic-api-key",
			Name:        "Generic API Key",
			Severity:    SeverityMedium,
			Description: "Detects generic API key patterns",
			Patterns:    []string{`(?i)(api[_-]?key|apikey|secret[_-]?key)\s*[=:]\s*["\']?[A-Za-z0-9+/=]{20,}`},
			FilePatterns: []string{`\.properties$`, `\.xml$`, `\.ya?ml$`, `\.json$`, `\.conf$`},
			Enabled:     true,
		},
	}
}

// LoadRulesFromFile loads detection rules from a YAML file.
func LoadRulesFromFile(path string) ([]*Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config rulesConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config.Rules, nil
}

// LoadRules loads rules from file if specified, otherwise returns defaults.
func LoadRules(rulesFile string) ([]*Rule, error) {
	if rulesFile != "" {
		return LoadRulesFromFile(rulesFile)
	}
	return DefaultRules(), nil
}

// LoadRulesWithLevel loads rules by level: "core", "extended", or "all".
// core = 6 built-in rules, extended = 32 additional rules (incl. 1 entropy), all = both combined (38).
//
// When rulesFile is set:
//   - If merge is false (default, historical behavior): the file's rules
//     completely replace the level's built-in rules.
//   - If merge is true: the file's rules are layered on top of the built-in
//     rules for the level, with custom rules overriding built-in rules that
//     share the same ID (so users can tweak a built-in rule without forking
//     the whole set). This lets a user write "38 built-in + my 5 extra".
func LoadRulesWithLevel(rulesFile, level string, merge bool) ([]*Rule, error) {
	// builtinRulesForLevel never returns an error (it falls back to core for
	// unknown levels), so the error is intentionally ignored here.
	builtin, _ := builtinRulesForLevel(level)
	if rulesFile == "" {
		return builtin, nil
	}
	custom, err := LoadRulesFromFile(rulesFile)
	if err != nil {
		return nil, err
	}
	if !merge {
		return custom, nil
	}
	return MergeRules(builtin, custom), nil
}

// builtinRulesForLevel returns the built-in rule set for a level string.
func builtinRulesForLevel(level string) ([]*Rule, error) {
	switch level {
	case "core":
		return DefaultRules(), nil
	case "extended":
		return ExtendedRules(), nil
	case "all":
		return AllRules(), nil
	default:
		return DefaultRules(), nil
	}
}

// MergeRules layers custom rules on top of builtin rules. Rules with the same
// ID in both sets are replaced by the custom version (so a user can override a
// built-in rule's severity or patterns without dropping the rest). Rules unique
// to either set are kept. Order is builtin-first, custom-appended, so merged
// rule lists stay stable for the override case.
func MergeRules(builtin, custom []*Rule) []*Rule {
	byID := make(map[string]int, len(builtin)+len(custom))
	merged := make([]*Rule, 0, len(builtin)+len(custom))
	for _, r := range builtin {
		if _, exists := byID[r.ID]; !exists {
			byID[r.ID] = len(merged)
			merged = append(merged, r)
		}
	}
	for _, r := range custom {
		if idx, exists := byID[r.ID]; exists {
			merged[idx] = r // override built-in with custom
		} else {
			byID[r.ID] = len(merged)
			merged = append(merged, r)
		}
	}
	return merged
}
