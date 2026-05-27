package accountvideosmoke

import (
	"regexp"
	"strings"
)

var redactors = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(Bearer )[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)([A-Za-z0-9_]*(token|secret|password|passwd|dsn|key)[A-Za-z0-9_]*=)[^[:space:]]+`),
	regexp.MustCompile(`(?i)([?&][^=[:space:]]*(token|secret|password|passwd|dsn|key|auth)[^=[:space:]]*=)[^&[:space:]]+`),
}

func Redact(input string) string {
	out := input
	for _, re := range redactors {
		out = re.ReplaceAllString(out, `${1}<redacted>`)
	}
	out = redactPEMBlock(out, "PRIVATE KEY")
	out = redactPEMBlock(out, "CERTIFICATE")
	out = redactPEMBlock(out, "CERTIFICATE REQUEST")
	return out
}

func redactPEMBlock(input, suffix string) string {
	lines := strings.Split(input, "\n")
	inBlock := false
	for i, line := range lines {
		if strings.HasPrefix(line, "-----BEGIN ") && strings.Contains(line, suffix+"-----") {
			inBlock = true
			lines[i] = line + " <redacted>"
			continue
		}
		if inBlock {
			lines[i] = "<redacted>"
			if strings.HasPrefix(line, "-----END ") && strings.Contains(line, suffix+"-----") {
				inBlock = false
			}
		}
	}
	return strings.Join(lines, "\n")
}
