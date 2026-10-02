package storagepolicy

import (
	"fmt"
	"regexp"
	"strings"
)

var keySegment = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// CIKeyPrefix identifies one repository, workflow attempt and platform. Use the
// repository basename, including its existing underscores, rather than an owner/repo pair.
func CIKeyPrefix(repo, run, attempt, platform string) (string, error) {
	return keyPrefix("ci", repo, run, attempt, platform)
}

// TmpKeyPrefix identifies intermediate objects owned by one producer attempt.
func TmpKeyPrefix(producer, run, attempt string) (string, error) {
	return keyPrefix("tmp", producer, run, attempt)
}

func keyPrefix(family string, parts ...string) (string, error) {
	for _, part := range parts {
		if !keySegment.MatchString(part) {
			return "", fmt.Errorf("invalid %s object key segment %q", family, part)
		}
	}
	return family + "/" + strings.Join(parts, "/"), nil
}
