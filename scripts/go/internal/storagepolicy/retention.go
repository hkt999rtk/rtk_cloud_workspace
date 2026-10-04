package storagepolicy

import (
	"fmt"
	"strings"
)

// RetentionDays implements the disposable-prefix defaults from the canonical
// workspace policy. Zero means there is no automatic expiration default.
func RetentionDays(prefix string) int {
	switch prefix {
	case "ci/", "reports/":
		return 30
	case "tmp/":
		return 7
	case "__rtk_cloud_validation__/":
		return 1
	default:
		return 0
	}
}

type Expiration struct {
	ID     string
	Prefix string
	Days   int
}

// Expirations produces only registered disposable namespaces. It does not
// authorize applying rules to existing objects or changing existing lifecycle.
func Expirations(purpose, configuredPrefix string) ([]Expiration, error) {
	if purpose != "artifacts" && purpose != "media" && purpose != "ota" {
		return nil, fmt.Errorf("unsupported storage retention purpose %q", purpose)
	}
	if err := ValidatePrefix(configuredPrefix); err != nil {
		return nil, err
	}
	var result []Expiration
	if purpose == "artifacts" {
		for _, prefix := range []string{"ci/", "tmp/", "reports/"} {
			result = append(result, Expiration{ID: "rtk-" + strings.TrimSuffix(prefix, "/"), Prefix: prefix, Days: RetentionDays(prefix)})
		}
	}
	canary := "__rtk_cloud_validation__/"
	if configuredPrefix != "" {
		canary = configuredPrefix + "/" + canary
	}
	return append(result, Expiration{ID: "rtk-storage-canary", Prefix: canary, Days: RetentionDays("__rtk_cloud_validation__/")}), nil
}
