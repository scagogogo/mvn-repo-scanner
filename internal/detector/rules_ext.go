package detector

// ExtendedRules returns additional rules inspired by Gitleaks, GitHub, and TruffleHog.
// These are grouped by category for selective enablement.
func ExtendedRules() []*Rule {
	return []*Rule{
		// === Cloud Provider Keys ===
		{
			ID: "google-api-key", Name: "Google API Key", Severity: SeverityHigh,
			Description:    "Detects Google API keys (AIza...)",
			Patterns:       []string{`AIza[0-9A-Za-z\-_]{35}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "google-oauth", Name: "Google OAuth Access Token", Severity: SeverityHigh,
			Description:    "Detects Google OAuth access tokens (ya29...)",
			Patterns:       []string{`ya29\.[0-9A-Za-z\-_]+`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "azure-storage-key", Name: "Azure Storage Account Key", Severity: SeverityHigh,
			Description:    "Detects Azure storage account shared keys",
			Patterns:       []string{`DefaultEndpointsProtocol=https;AccountName=[^;]+;AccountKey=[A-Za-z0-9+/=]{88}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "azure-tenant-secret", Name: "Azure Tenant Secret", Severity: SeverityHigh,
			Description:    "Detects Azure client secrets",
			Patterns:       []string{`(?i)azure[_\-]?client[_\-]?secret\s*[=:]\s*["\']?[A-Za-z0-9\-_.~]{34,42}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`},
			Enabled: true,
		},
		{
			ID: "gcp-service-account", Name: "GCP Service Account Key", Severity: SeverityCritical,
			Description:    "Detects GCP service account private key in JSON",
			Patterns:       []string{`"type"\s*:\s*"service_account"`},
			FilePatterns:   []string{`\.json$`},
			Enabled: true,
		},
		{
			ID: "digitalocean-token", Name: "DigitalOcean Token", Severity: SeverityHigh,
			Description:    "Detects DigitalOcean API tokens (dop_v1_...)",
			Patterns:       []string{`dop_v1_[a-f0-9]{64}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},

		// === Database Credentials ===
		{
			ID: "mongodb-connection", Name: "MongoDB Connection String", Severity: SeverityHigh,
			Description:    "Detects MongoDB connection strings with credentials",
			Patterns:       []string{`mongodb(\+srv)?://[^:\s]+:[^@\s]+@[^\s]+`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.conf$`},
			Enabled: true,
		},
		{
			ID: "mysql-connection", Name: "MySQL Connection String", Severity: SeverityHigh,
			Description:    "Detects MySQL connection strings with credentials",
			Patterns:       []string{`(?i)mysql://[^:\s]+:[^@\s]+@[^\s]+`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.conf$`},
			Enabled: true,
		},
		{
			ID: "postgres-connection", Name: "PostgreSQL Connection String", Severity: SeverityHigh,
			Description:    "Detects PostgreSQL connection strings with credentials",
			Patterns:       []string{`(?i)(?:postgres|postgresql)://[^:\s]+:[^@\s]+@[^\s]+`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.conf$`},
			Enabled: true,
		},
		{
			ID: "redis-connection", Name: "Redis Connection String", Severity: SeverityMedium,
			Description:    "Detects Redis connection strings with passwords",
			Patterns:       []string{`(?i)redis://:[^@\s]+@[^\s]+`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.conf$`},
			Enabled: true,
		},
		{
			ID: "sql-credential", Name: "SQL Credential in Config", Severity: SeverityHigh,
			Description:    "Detects SQL username/password pairs in config files",
			Patterns:       []string{`(?i)(?:db[_\-]?user|database[_\-]?user|sql[_\-]?user)\s*[=:]\s*["\']?[^\s"'<>]{3,}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.xml$`},
			Enabled: true,
		},

		// === Authentication Tokens ===
		{
			ID: "slack-token", Name: "Slack Token", Severity: SeverityHigh,
			Description:    "Detects Slack bot/user/workspace tokens (xoxb/xoxp/xoxa/xoxs-...)",
			Patterns:       []string{`xox[bpras]-[0-9]{10,13}-[0-9]{10,13}-[a-zA-Z0-9]{24,34}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "slack-webhook", Name: "Slack Webhook URL", Severity: SeverityMedium,
			Description:    "Detects Slack webhook URLs",
			Patterns:       []string{`https://hooks\.slack\.com/services/T[a-zA-Z0-9_]{8,}/B[a-zA-Z0-9_]{8,}/[a-zA-Z0-9_]{24}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "stripe-api-key", Name: "Stripe API Key", Severity: SeverityCritical,
			Description:    "Detects Stripe secret/live API keys (sk_live/rk_live)",
			Patterns:       []string{`(?:sk|rk)_live_[0-9a-zA-Z]{24,}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "sendgrid-api-key", Name: "SendGrid API Key", Severity: SeverityHigh,
			Description:    "Detects SendGrid API keys (SG...)",
			Patterns:       []string{`SG\.[0-9A-Za-z\-_]{22}\.[0-9A-Za-z\-_]{43}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "mailgun-api-key", Name: "Mailgun API Key", Severity: SeverityHigh,
			Description:    "Detects Mailgun API keys (key-...)",
			Patterns:       []string{`key-[0-9a-zA-Z]{32}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "twilio-api-key", Name: "Twilio API Key", Severity: SeverityHigh,
			Description:    "Detects Twilio API keys (SK...)",
			Patterns:       []string{`SK[0-9a-fA-F]{32}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "square-access-token", Name: "Square Access Token", Severity: SeverityHigh,
			Description:    "Detects Square access tokens (sq0atp-...)",
			Patterns:       []string{`sq0atp-[0-9A-Za-z\-_]{22}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "npm-token", Name: "NPM Access Token", Severity: SeverityHigh,
			Description:    "Detects NPM access tokens in .npmrc files",
			Patterns:       []string{`(?i)//registry\.npmjs\.org/:_authToken=[0-9a-f-]{36}`},
			FilePatterns:   []string{`\.npmrc$`},
			Enabled: true,
		},

		// === Cryptographic Keys & Certificates ===
		{
			ID: "ssh-private-key", Name: "SSH Private Key", Severity: SeverityCritical,
			Description:    "Detects SSH private keys (DSA/EC/RSA/OPENSSH/ED25519)",
			Patterns:       []string{`-----BEGIN (?:OPENSSH |EC |DSA |RSA |ED25519 )?PRIVATE KEY-----`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "pgp-private-key", Name: "PGP Private Key Block", Severity: SeverityCritical,
			Description:    "Detects PGP private key blocks",
			Patterns:       []string{`-----BEGIN PGP PRIVATE KEY BLOCK-----`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "jwt-secret", Name: "JWT Secret", Severity: SeverityHigh,
			Description:    "Detects JWT signing secrets in configuration files",
			Patterns:       []string{`(?i)(?:jwt[_\-]?secret|jwt[_\-]?key|token[_\-]?secret)\s*[=:]\s*["\']?[^\s"'<>{}]{16,}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.env$`},
			Enabled: true,
		},
		{
			ID: "encryption-key", Name: "Encryption Key in Config", Severity: SeverityHigh,
			Description:    "Detects hardcoded encryption/AES keys in config",
			Patterns:       []string{`(?i)(?:encryption[_\-]?key|aes[_\-]?key)\s*[=:]\s*["\']?[A-Za-z0-9+/=]{16,}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.env$`},
			Enabled: true,
		},

		// === Generic Patterns ===
		{
			ID: "basic-auth-header", Name: "Basic Auth Header", Severity: SeverityMedium,
			Description:    "Detects Basic authentication headers",
			Patterns:       []string{`(?i)Authorization\s*:\s*Basic\s+[A-Za-z0-9+/=]{6,}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		{
			ID: "bearer-token", Name: "Bearer Token", Severity: SeverityMedium,
			Description:    "Detects Bearer token in Authorization headers or config",
			Patterns:       []string{`(?i)(?:bearer[_\-]?token|auth[_\-]?token)\s*[=:]\s*["\']?[A-Za-z0-9\-_.~+/=]{20,}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.env$`},
			Enabled: true,
		},
		{
			ID: "oauth-client-secret", Name: "OAuth Client Secret", Severity: SeverityHigh,
			Description:    "Detects OAuth client secrets in configuration",
			Patterns:       []string{`(?i)(?:client[_\-]?secret|oauth[_\-]?secret)\s*[=:]\s*["\']?[A-Za-z0-9\-_.~]{20,}`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.env$`},
			Enabled: true,
		},
		{
			ID: "maven-password-xml", Name: "Maven Settings Password", Severity: SeverityCritical,
			Description:    "Detects passwords in Maven settings.xml",
			Patterns:       []string{`<password>\s*\S+\s*</password>`},
			FilePatterns:   []string{`\.xml$`},
			Enabled: true,
		},
		{
			ID: "maven-passphrase", Name: "Maven GPG Passphrase", Severity: SeverityHigh,
			Description:    "Detects GPG passphrases in Maven settings or CI config",
			Patterns:       []string{`(?i)(?:gpg[_\-]?passphrase|signing[_\-]?key[_\-]?password)\s*[=:]\s*["\']?[^\s"'<>]{6,}`},
			FilePatterns:   []string{`\.properties$`, `\.xml$`, `\.ya?ml$`, `\.gradle$`},
			Enabled: true,
		},
		{
			ID: "spring-credential", Name: "Spring Boot Credential", Severity: SeverityHigh,
			Description:    "Detects Spring Boot datasource credentials in properties",
			Patterns:       []string{`(?i)spring\.datasource\.(?:password|username)\s*=\s*\S+`},
			FilePatterns:   []string{`\.properties$`, `\.ya?ml$`},
			Enabled: true,
		},
		{
			ID: "docker-registry-auth", Name: "Docker Registry Auth", Severity: SeverityMedium,
			Description:    "Detects Docker registry authentication in config files",
			Patterns:       []string{`"(?:auth|email|password|username|serveraddress)"\s*:\s*"[^"]{5,}"`},
			FilePatterns:   []string{`\.json$`, `\.config$`},
			Enabled: true,
		},
		{
			ID: "firebase-key", Name: "Firebase API Key", Severity: SeverityMedium,
			Description:    "Detects Firebase web API keys (AIzaSy...)",
			Patterns:       []string{`AIzaSy[a-zA-Z0-9\-_]{33}`},
			FilePatterns:   []string{`\..+$`},
			Enabled: true,
		},
		// === Entropy Engine ===
		// This rule demonstrates the entropy engine: it flags any high-randomness
		// base64 token that fixed-shape regex rules would miss (e.g. an unnamed
		// service token with no known prefix). Lower confidence than a shape match,
		// hence MEDIUM severity and suppressed for common placeholder values.
		{
			ID:           "high-entropy-secret",
			Name:         "High Entropy Secret",
			Severity:     SeverityMedium,
			Description:  "Detects high-entropy (random) strings that look like secrets but match no known format",
			Engine:       "entropy",
			FilePatterns: []string{`\.properties$`, `\.ya?ml$`, `\.json$`, `\.conf$`, `\.env$`, `\.sh$`, `\.xml$`},
			Enabled:      true,
			Entropy: &EntropyConfig{
				Threshold: 4.5,
				Window:    32,
				MinLength: 24,
				Charset:   "base64",
			},
			Allowlist: []string{"EXAMPLE", "example", "changeme", "your-", "placeholder", "test"},
			Tags:      []string{"entropy", "generic"},
		},
	}
}

// AllRules returns core rules + extended rules combined.
func AllRules() []*Rule {
	return append(DefaultRules(), ExtendedRules()...)
}