package storagepolicy

import "testing"

func TestRetentionDefaultsOnlyExpireRegisteredDisposablePrefixes(t *testing.T) {
	for _, purpose := range []string{"media", "ota", "artifacts"} {
		rules, err := Expirations(purpose, "environments/video-cloud-dev")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"environments/video-cloud-dev/__rtk_cloud_validation__/": 1}
		if purpose == "artifacts" {
			want["ci/"], want["tmp/"], want["reports/"] = 30, 7, 30
		}
		if len(rules) != len(want) {
			t.Fatalf("%s: unexpected rules: %+v", purpose, rules)
		}
		for _, rule := range rules {
			if want[rule.Prefix] != rule.Days || rule.Days == 0 {
				t.Fatalf("unsafe expiration: %+v", rule)
			}
		}
	}
	for _, protected := range []string{"", "releases/", "sdk/", "holds/", "ota-billable-v1/", "clips/", "backups/", "ci", "ci-other/"} {
		if RetentionDays(protected) != 0 {
			t.Fatalf("protected prefix %q expires", protected)
		}
	}
}

func TestRetentionRejectsMalformedPrefix(t *testing.T) {
	for _, prefix := range []string{"/environments/dev", "environments/dev/", "environments//dev", "environments/../dev", "environments/./dev", "environments/dev with spaces", "environments/dev\n"} {
		if _, err := Expirations("media", prefix); err == nil {
			t.Fatalf("accepted prefix %q", prefix)
		}
	}
	if _, err := Expirations("backup", ""); err == nil {
		t.Fatal("backup expiration proposed")
	}
}
