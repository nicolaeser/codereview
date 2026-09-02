package review

import "regexp"

const redactedPlaceholder = "[redacted]"

var (
	pemPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	awsAccessKey  = regexp.MustCompile(`(^|[^A-Z0-9])AKIA[0-9A-Z]{16}`)
	bearerToken   = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]{20,}`)
	forgePAT      = regexp.MustCompile(`(^|[^A-Za-z0-9_])(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})`)
	slackToken    = regexp.MustCompile(`(^|[^A-Za-z0-9_])(xox[baprs]-[A-Za-z0-9-]{10,})`)
	// Keep the key name so "secret" in prose is untouched; only assignment values match.
	secretAssign = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])((?:api[_-]?key|secret)["']?\s*[=:]\s*["']?)[A-Za-z0-9._\-+/=]{16,}["']?`)
)

func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = pemPrivateKey.ReplaceAllString(s, redactedPlaceholder)
	s = awsAccessKey.ReplaceAllString(s, "${1}"+redactedPlaceholder)
	s = bearerToken.ReplaceAllString(s, redactedPlaceholder)
	s = forgePAT.ReplaceAllString(s, "${1}"+redactedPlaceholder)
	s = slackToken.ReplaceAllString(s, "${1}"+redactedPlaceholder)
	s = secretAssign.ReplaceAllString(s, "${1}${2}"+redactedPlaceholder)
	return s
}
