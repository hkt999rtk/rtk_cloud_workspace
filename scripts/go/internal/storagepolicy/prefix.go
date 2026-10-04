package storagepolicy

import (
	"fmt"
	"regexp"
	"strings"
)

var prefixSegment = regexp.MustCompile(`^[a-z0-9._-]+$`)

// ValidatePrefix checks a configured namespace without silently changing it.
// Empty selects the bucket root; callers that require an owned namespace must
// separately require a nonempty value.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "." || part == ".." || !prefixSegment.MatchString(part) {
			return fmt.Errorf("storage prefix must contain lowercase alphanumeric, dot, underscore or hyphen segments separated by single slashes")
		}
	}
	return nil
}
