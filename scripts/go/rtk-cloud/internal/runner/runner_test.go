package runner

import "testing"

func TestSpecs(t *testing.T) {
	got := Specs()
	want := []string{
		"rtk-shared-linux-ci|rtk-ci-account-manager|hkt999rtk/rtk_account_manager|g6-standard-4|account-manager-ci",
		"rtk-shared-linux-ci|rtk-ci-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-4|rtk-cloud-admin-ci",
		"rtk-shared-linux-ci|rtk-ci-cloud-frontend|hkt999rtk/rtk_cloud_frontend|g6-standard-4|rtk_cloud_frontend,go",
		"rtk-shared-linux-ci|rtk-ci-cloud-client-linux|hkt999rtk/rtk_cloud_client|g6-standard-4|client-sdk-ci",
		"rtk-shared-linux-ci|rtk-ci-cloud-logger|hkt999rtk/rtk_cloud_logger|g6-standard-4|rtk-cloud-logger-ci",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d specs, want %d", len(got), len(want))
	}
	for i, spec := range got {
		line := spec.HostLabel + "|" + spec.RunnerName + "|" + spec.Repo + "|" + spec.Type + "|" + spec.CustomLabel
		if line != want[i] {
			t.Fatalf("spec %d mismatch\ngot:  %s\nwant: %s", i, line, want[i])
		}
	}
}
