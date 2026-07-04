// Package detector scans file content for sensitive information using configurable regex rules.
package detector

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Severity represents the severity level of a finding.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
)

// IsValid returns true if the severity is one of the defined constants.
func (s Severity) IsValid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	default:
		return false
	}
}

// String returns a human-readable summary of the rule.
func (r Rule) String() string {
	return fmt.Sprintf("%s [%s] %s", r.ID, r.Severity, r.Name)
}

// String returns a human-readable summary of the finding.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d [%s] %s: %s", f.FilePath, f.LineNumber, f.Severity, f.RuleID, f.Match)
}

// Finding represents a single sensitive content detection result.
type Finding struct {
	RuleID      string   `json:"rule_id"`
	RuleName    string   `json:"rule_name"`
	Severity    Severity `json:"severity"`
	FilePath    string   `json:"file_path"`
	LineNumber  int      `json:"line_number"`
	LineContent string   `json:"line_content"`
	Match       string   `json:"match"`
	Description string   `json:"description"`
	// EngineName records which engine produced this finding (regex/entropy/...).
	// Surfaced in reports so reviewers can weight findings by source.
	EngineName string  `json:"engine_name,omitempty"`
	// Entropy is the Shannon entropy score, populated only by the entropy engine.
	Entropy float64 `json:"entropy,omitempty"`
}

// EntropyConfig configures the entropy engine for a rule.
type EntropyConfig struct {
	// Threshold is the minimum bits-per-char to flag a token (default 4.5).
	Threshold float64 `yaml:"threshold" json:"threshold"`
	// Window is the sliding-window length (0 or >= len(token) ⇒ score whole token).
	Window int `yaml:"window" json:"window"`
	// MinLength ignores tokens shorter than this (default 20).
	MinLength int `yaml:"min_length" json:"min_length"`
	// MaxLength ignores tokens longer than this (0 = no cap).
	MaxLength int `yaml:"max_length" json:"max_length"`
	// Charset constrains candidate tokens: base64 | hex | ascii (default base64).
	Charset string `yaml:"charset" json:"charset"`
}

// Rule defines a single detection rule. A rule is dispatched to a RuleEngine
// based on Engine (default "regex"); the engine reads the fields it needs
// (RegexEngine reads Patterns, EntropyEngine reads Entropy).
type Rule struct {
	ID           string   `yaml:"id" json:"id"`
	Name         string   `yaml:"name" json:"name"`
	Severity     Severity `yaml:"severity" json:"severity"`
	Description  string   `yaml:"description" json:"description"`
	Patterns     []string `yaml:"patterns" json:"patterns"`
	FilePatterns []string `yaml:"file_patterns" json:"file_patterns"`
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	// Engine selects which engine evaluates this rule: "regex" (default) or
	// "entropy". A regex rule uses Patterns; an entropy rule uses the Entropy
	// block. Custom engines can be registered via RegisterEngine on the Detector.
	Engine string `yaml:"engine" json:"engine,omitempty"`
	// Ignorecase wraps regex patterns in (?i) at compile time, so users don't
	// need to inline (?i) in every pattern.
	Ignorecase bool `yaml:"ignorecase" json:"ignorecase,omitempty"`
	// CaptureGroup, when >= 0, reports that capture-group index as the Match
	// instead of the full regex match (0 = full match). Useful when the regex
	// anchors on surrounding context but only the secret should be reported.
	CaptureGroup int `yaml:"capture_group" json:"capture_group,omitempty"`
	// Allowlist suppresses findings whose Match equals (or contains, for
	// entries >= 4 chars) a known-safe placeholder such as "EXAMPLE" or "test".
	Allowlist []string `yaml:"allowlist" json:"allowlist,omitempty"`
	// Tags are free-form labels (e.g. "cloud", "database") for selective
	// enable/disable and report grouping.
	Tags []string `yaml:"tags" json:"tags,omitempty"`
	// MinLength/MaxLength constrain the reported Match length, reducing
	// false positives from short or oversized matches.
	MinLength int `yaml:"min_length" json:"min_length,omitempty"`
	MaxLength int `yaml:"max_length" json:"max_length,omitempty"`
	// Entropy configures the entropy engine. Ignored unless Engine == "entropy".
	Entropy *EntropyConfig `yaml:"entropy" json:"entropy,omitempty"`

	// Compiled fields (not serialized). Populated by the rule's engine at
	// detector construction.
	compiledPatterns     []*regexp.Regexp
	compiledFilePatterns []*regexp.Regexp
}

// Detector scans file content against detection rules. It dispatches each rule
// to the RuleEngine named by rule.Engine (default: regex). Built-in engines are
// regex and entropy; additional engines can be added via the RegisterEngine
// option, so a custom YAML rule can declare `engine: my-engine` and be routed
// to a user-supplied implementation.
type Detector struct {
	rules   []*Rule
	engines *engineRegistry
}

// DetectorOption configures a Detector.
type DetectorOption func(*Detector)

// WithEngine registers an additional RuleEngine under its Name(), making it
// available to rules that declare `engine: <name>`. Built-in engines (regex,
// entropy) are always registered; this only adds or replaces custom ones.
func WithEngine(e RuleEngine) DetectorOption {
	return func(d *Detector) { d.engines.Register(e) }
}

// NewDetector creates a new detector with the given rules. Each rule is
// compiled by the engine it declares (default regex); a compile error on any
// enabled rule aborts construction.
func NewDetector(rules []*Rule, opts ...DetectorOption) (*Detector, error) {
	d := &Detector{
		rules:   rules,
		engines: newEngineRegistry(),
	}
	for _, opt := range opts {
		opt(d)
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		// File patterns are engine-agnostic — every engine honors them. Compile
		// them once here so entropy/regex/custom engines all see the same filter.
		if err := compileFilePatterns(rule); err != nil {
			return nil, err
		}
		engineName := EngineName(rule.Engine)
		if rule.Engine == "" {
			engineName = EngineRegex
		}
		engine := d.engines.Get(engineName)
		if engine == nil {
			return nil, fmt.Errorf("rule %s: unknown engine %q", rule.ID, rule.Engine)
		}
		if err := engine.Compile(rule); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// compileFilePatterns compiles a rule's FilePatterns into compiledFilePatterns.
// Shared by all engines since file-type filtering happens before engine dispatch.
func compileFilePatterns(rule *Rule) error {
	for _, fp := range rule.FilePatterns {
		re, err := regexp.Compile(fp)
		if err != nil {
			return fmt.Errorf("rule %s file pattern %q: %w", rule.ID, fp, err)
		}
		rule.compiledFilePatterns = append(rule.compiledFilePatterns, re)
	}
	return nil
}

// ScanFile scans a single file against all enabled rules.
func (d *Detector) ScanFile(filePath string) ([]Finding, error) {
	matchingRules := d.matchingRules(filePath)
	if len(matchingRules) == 0 {
		return nil, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	return d.scanReader(f, filePath, matchingRules)
}

// ScanContent scans a reader's content against the given rules.
func (d *Detector) ScanContent(reader io.Reader, filePath string) ([]Finding, error) {
	matchingRules := d.matchingRules(filePath)
	if len(matchingRules) == 0 {
		return nil, nil
	}
	return d.scanReader(reader, filePath, matchingRules)
}

// scanReader reads line by line and dispatches each matching rule to its engine.
func (d *Detector) scanReader(reader io.Reader, filePath string, rules []*Rule) ([]Finding, error) {
	var findings []Finding
	// A 1 MiB token buffer avoids bufio.ErrTooLong on minified JS / single-line
	// JSON / binary-as-text, which previously silently dropped the rest of the
	// file. Lines longer than 1 MiB are truncated (their head still scans).
	buf := make([]byte, 0, 64*1024)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(buf, 1024*1024)

	// Pre-resolve each rule's engine once (avoids a map lookup per line).
	type resolved struct {
		rule   *Rule
		engine RuleEngine
	}
	resolvedRules := make([]resolved, 0, len(rules))
	for _, rule := range rules {
		name := EngineName(rule.Engine)
		if rule.Engine == "" {
			name = EngineRegex
		}
		eng := d.engines.Get(name)
		if eng == nil {
			continue
		}
		resolvedRules = append(resolvedRules, resolved{rule: rule, engine: eng})
	}

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		for _, r := range resolvedRules {
			findings = append(findings, r.engine.Match(line, r.rule, filePath, lineNum)...)
		}
	}
	return findings, scanner.Err()
}

// matchingRules returns rules whose file patterns match the given file path.
func (d *Detector) matchingRules(filePath string) []*Rule {
	var matched []*Rule
	baseName := filepath.Base(filePath)

	for _, rule := range d.rules {
		if !rule.Enabled {
			continue
		}
		if len(rule.compiledFilePatterns) == 0 {
			matched = append(matched, rule)
			continue
		}
		for _, fp := range rule.compiledFilePatterns {
			if fp.MatchString(baseName) {
				matched = append(matched, rule)
				break
			}
		}
	}
	return matched
}

// truncate shortens a string to maxLen.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
