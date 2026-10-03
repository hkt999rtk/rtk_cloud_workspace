package storagepolicy

import "testing"

func TestValidatePrefix(t *testing.T) {
	for _, prefix := range []string{"", "environments/video-cloud-dev", "releases", "reports/producer_1/v1.2"} {
		if err := ValidatePrefix(prefix); err != nil {
			t.Fatalf("valid prefix %q: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"/", "/dev", "dev/", "environments//dev", ".", "..", "dev/../other", "dev/./other", "bad name", "dev\n", "Dev", "dev\\other", "dev/%2e%2e", "dev/中文"} {
		if err := ValidatePrefix(prefix); err == nil {
			t.Errorf("accepted ambiguous or noncanonical prefix %q", prefix)
		}
	}
}
