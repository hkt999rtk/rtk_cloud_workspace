package cloudvalidation

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(access_token|refresh_token|claim_token|password|private_key|client_secret)(\"?\s*[:=]\s*\"?)[^\"\s,}]+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

func Redact(value string) string {
	redacted := value
	for _, pattern := range secretPatterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			if idx := strings.IndexAny(match, ":="); idx >= 0 {
				return match[:idx+1] + "<redacted>"
			}
			return "<redacted>"
		})
	}
	return redacted
}
