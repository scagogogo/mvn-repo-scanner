package detector

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// EngineName identifies which engine produced a finding.
type EngineName string

const (
	EngineRegex   EngineName = "regex"
	EngineEntropy EngineName = "entropy"
)

// RuleEngine is the abstraction over a detection strategy. Each engine knows
// how to compile its own rule fields and how to match a line of content.
//
// Engines are stateless per-rule: Compile is called once per rule at detector
// construction, Match is called per line during scanning. An engine may read
// any field on the Rule (e.g. EntropyEngine reads rule.Entropy, RegexEngine
// reads rule.compiledPatterns) but must not mutate it.
type RuleEngine interface {
	// Name returns the engine identifier, surfaced in findings for traceability.
	Name() EngineName
	// Compile prepares the rule for use by this engine. Called once per rule.
	// Must be idempotent and safe to call on rules that won't actually be
	// matched by this engine (engines should no-op if the rule isn't theirs).
	Compile(rule *Rule) error
	// Match evaluates a single line against the rule and returns any findings.
	// filePath/lineNum/severity are provided so the engine can fill a complete
	// Finding without needing to re-derive them.
	Match(line string, rule *Rule, filePath string, lineNum int) []Finding
}

// engineRegistry maps engine names to their implementations. The Detector
// holds one of these and dispatches each rule to its declared engine.
type engineRegistry struct {
	engines map[EngineName]RuleEngine
}

func newEngineRegistry() *engineRegistry {
	r := &engineRegistry{engines: make(map[EngineName]RuleEngine)}
	// Built-in engines. Custom engines can be registered via RegisterEngine.
	r.Register(&RegexEngine{})
	r.Register(&EntropyEngine{})
	return r
}

// Register adds (or replaces) an engine in the registry.
func (r *engineRegistry) Register(e RuleEngine) {
	r.engines[e.Name()] = e
}

// Get returns the engine for a name, falling back to the regex engine when
// unset (the historical default — every rule used to be a regex rule).
func (r *engineRegistry) Get(name EngineName) RuleEngine {
	if e, ok := r.engines[name]; ok {
		return e
	}
	return r.engines[EngineRegex]
}

// RegexEngine matches lines using compiled regular expressions. This is the
// original detection strategy; it supports Ignorecase (compile-time (?i) wrap)
// and CaptureGroup (extract a named/numbered group as the Match instead of the
// full match) to reduce noise in reports.
type RegexEngine struct{}

func (e *RegexEngine) Name() EngineName { return EngineRegex }

func (e *RegexEngine) Compile(rule *Rule) error {
	for _, p := range rule.Patterns {
		expr := p
		if rule.Ignorecase && !strings.Contains(expr, "(?i)") {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return fmt.Errorf("rule %s pattern %q: %w", rule.ID, p, err)
		}
		rule.compiledPatterns = append(rule.compiledPatterns, re)
	}
	// File patterns are compiled centrally by NewDetector; do not duplicate.
	return nil
}

func (e *RegexEngine) Match(line string, rule *Rule, filePath string, lineNum int) []Finding {
	var findings []Finding
	for _, re := range rule.compiledPatterns {
		// FindAllStringSubmatch gives access to capture groups; index 0 is the
		// full match, index 1+ are groups.
		matches := re.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			fullMatch := m[0]
			matchStr := fullMatch
			// If the rule designates a capture group, extract that as the
			// reported match (helps report just the secret, not the whole line
			// context the regex anchored on).
			if rule.CaptureGroup >= 0 && rule.CaptureGroup < len(m) {
				if g := m[rule.CaptureGroup]; g != "" {
					matchStr = g
				}
			}
			if isAllowlisted(matchStr, rule) {
				continue
			}
			if !withinLengthBounds(matchStr, rule) {
				continue
			}
			findings = append(findings, Finding{
				RuleID:      rule.ID,
				RuleName:    rule.Name,
				Severity:    rule.Severity,
				FilePath:    filePath,
				LineNumber:  lineNum,
				LineContent: truncate(line, 200),
				Match:       truncate(matchStr, 100),
				Description: rule.Description,
				EngineName:  string(EngineRegex),
			})
		}
	}
	return findings
}

// EntropyEngine detects high-entropy strings, which often indicate randomly
// generated secrets (API keys, tokens) that fixed-shape regex rules miss. It
// computes the Shannon entropy of candidate tokens and reports those above a
// configured threshold.
//
// A rule using this engine declares:
//
//	engine: entropy
//	entropy:
//	  threshold: 4.5        # bits/char; ~4.5+ is suspicious for base64/hex
//	  window: 32            # token length to slide over (0 = whole line)
//	  min_length: 20        # ignore tokens shorter than this
//	  charset: base64       # base64 | hex | ascii (default base64)
//
// The engine splits each line on whitespace/common delimiters, scores each
// token of at least min_length, and reports the highest-scoring window.
type EntropyEngine struct{}

func (e *EntropyEngine) Name() EngineName { return EngineEntropy }

func (e *EntropyEngine) Compile(rule *Rule) error {
	// Entropy rules don't compile regex patterns; they're driven by the
	// rule.Entropy config block. Defaults are applied at match time so a rule
	// can declare only `threshold` and rely on defaults for the rest.
	if rule.Entropy == nil {
		// If a rule sets engine: entropy but no entropy block, use defaults.
		rule.Entropy = &EntropyConfig{}
	}
	if rule.Entropy.Threshold <= 0 {
		rule.Entropy.Threshold = 4.5
	}
	if rule.Entropy.Window <= 0 {
		rule.Entropy.Window = 32
	}
	if rule.Entropy.MinLength <= 0 {
		rule.Entropy.MinLength = 20
	}
	if rule.Entropy.Charset == "" {
		rule.Entropy.Charset = "base64"
	}
	return nil
}

func (e *EntropyEngine) Match(line string, rule *Rule, filePath string, lineNum int) []Finding {
	cfg := rule.Entropy
	tokens := splitEntropyTokens(line)
	var findings []Finding
	for _, tok := range tokens {
		if len(tok) < cfg.MinLength {
			continue
		}
		if !withinLengthBounds(tok, rule) {
			continue
		}
		// If window >= len(tok), score the whole token; otherwise slide.
		best := 0.0
		bestStr := ""
		if cfg.Window >= len(tok) {
			if shannonEntropy(tok, cfg.Charset) >= cfg.Threshold && charsetOnly(tok, cfg.Charset) {
				best = shannonEntropy(tok, cfg.Charset)
				bestStr = tok
			}
		} else {
			for i := 0; i+cfg.Window <= len(tok); i++ {
				win := tok[i : i+cfg.Window]
				if !charsetOnly(win, cfg.Charset) {
					continue
				}
				h := shannonEntropy(win, cfg.Charset)
				if h > best {
					best = h
					bestStr = win
				}
			}
		}
		if best >= cfg.Threshold && bestStr != "" {
			if isAllowlisted(bestStr, rule) {
				continue
			}
			findings = append(findings, Finding{
				RuleID:      rule.ID,
				RuleName:    rule.Name,
				Severity:    rule.Severity,
				FilePath:    filePath,
				LineNumber:  lineNum,
				LineContent: truncate(line, 200),
				Match:       truncate(bestStr, 100),
				Description: rule.Description,
				EngineName:  string(EngineEntropy),
				Entropy:     math.Round(best*1000) / 1000,
			})
		}
	}
	return findings
}

// splitEntropyTokens breaks a line into candidate tokens on whitespace and
// common secret-adjacent delimiters (=, :, ", ', comma, semicolon). This is
// intentionally coarse: entropy scoring itself is the real filter.
func splitEntropyTokens(line string) []string {
	splitFn := func(r rune) bool {
		return r == ' ' || r == '\t' || r == '=' || r == ':' || r == '"' ||
			r == '\'' || r == ',' || r == ';' || r == '<' || r == '>' || r == '|'
	}
	return strings.FieldsFunc(line, splitFn)
}

// charsetOnly reports whether a token consists exclusively of characters in
// the named charset. Non-conforming tokens can't be base64/hex secrets.
func charsetOnly(token, charset string) bool {
	for _, r := range token {
		switch charset {
		case "hex":
			if !(unicode.IsDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		case "ascii":
			if r < 33 || r > 126 {
				return false
			}
		default: // base64
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '/' || r == '=') {
				return false
			}
		}
	}
	return true
}

// shannonEntropy computes the Shannon entropy (bits per character) of a string
// over the given charset. Higher entropy ⇒ more random ⇒ more likely a secret.
func shannonEntropy(s, charset string) float64 {
	if len(s) == 0 {
		return 0
	}
	counts := make(map[rune]int)
	for _, r := range s {
		counts[r]++
	}
	var entropy float64
	n := float64(len([]rune(s)))
	for _, c := range counts {
		p := float64(c) / n
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}
	return entropy
}

// isAllowlisted reports whether a matched string is a known-safe value that
// should be suppressed (e.g. "EXAMPLE", "test", placeholder AWS keys). This is
// the primary false-positive reduction lever for both engines.
func isAllowlisted(match string, rule *Rule) bool {
	if len(rule.Allowlist) == 0 {
		return false
	}
	lower := strings.ToLower(match)
	for _, a := range rule.Allowlist {
		if lower == strings.ToLower(a) {
			return true
		}
		if strings.Contains(lower, strings.ToLower(a)) && len(a) >= 4 {
			// Substring match for placeholders like "EXAMPLE" inside
			// "AKIAIOSFODNN7EXAMPLE". Require length >= 4 so short allowlist
			// entries don't over-match.
			return true
		}
	}
	return false
}

// withinLengthBounds enforces MinLength/MaxLength on the matched value.
func withinLengthBounds(match string, rule *Rule) bool {
	if rule.MinLength > 0 && len(match) < rule.MinLength {
		return false
	}
	if rule.MaxLength > 0 && len(match) > rule.MaxLength {
		return false
	}
	return true
}
