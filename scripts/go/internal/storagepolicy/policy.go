// Package storagepolicy implements the naming rules in docs/object-storage-policy.md.
package storagepolicy

import (
	"fmt"
	"regexp"
	"strings"
)

var label = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var purposes = map[string]bool{"runtime": true, "artifacts": true, "backup": true, "billing-backup": true, "pki-backup": true, "test": true, "reports": true, "ota-firmware": true}

// Bucket returns a stable managed bucket name. Region is the provider region ID,
// never an endpoint hostname or a logical deployment-location alias.
func Bucket(scope, purpose, region string) (string, error) {
	for _, part := range []string{scope, purpose, region} {
		if !label.MatchString(part) {
			return "", fmt.Errorf("storage scope, purpose and region must use lowercase alphanumeric labels with hyphens")
		}
	}
	if !purposes[purpose] {
		return "", fmt.Errorf("unregistered storage purpose %q", purpose)
	}
	if scope == "shared" && purpose != "artifacts" && purpose != "test" && purpose != "reports" {
		return "", fmt.Errorf("purpose %s must be environment-owned", purpose)
	}
	name := strings.Join([]string{"rtk-cloud", scope, purpose, region}, "-")
	if len(name) > 63 {
		return "", fmt.Errorf("storage bucket name exceeds 63 characters")
	}
	return name, nil
}

// Validate compares the entire resource identity rather than parsing a name's
// variable-length fields or assuming that a prefix establishes ownership.
func Validate(name, scope, purpose, region string) error {
	expected, err := Bucket(scope, purpose, region)
	if err != nil {
		return err
	}
	if name != expected {
		return fmt.Errorf("storage bucket must be %s for its scope, purpose and provider region", expected)
	}
	return nil
}
