package storagepolicy

import "testing"

func TestRunPrefixes(t *testing.T) {
	got, err := CIKeyPrefix("rtk_cloud_client", "123", "2", "android-emulator")
	if err != nil || got != "ci/rtk_cloud_client/123/2/android-emulator" {
		t.Fatalf("CIKeyPrefix = %q, %v", got, err)
	}
	got, err = TmpKeyPrefix("rtk-cloud-client-sdk", "123", "2")
	if err != nil || got != "tmp/rtk-cloud-client-sdk/123/2" {
		t.Fatalf("TmpKeyPrefix = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "owner/repo", "..", "run?x", "a b", "a\\b"} {
		if _, err := CIKeyPrefix(invalid, "123", "2", "linux"); err == nil {
			t.Errorf("accepted invalid repository %q", invalid)
		}
		if _, err := TmpKeyPrefix("sdk", invalid, "2"); err == nil {
			t.Errorf("accepted invalid run %q", invalid)
		}
	}
}
