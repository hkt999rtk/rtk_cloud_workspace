package storagepolicy

import (
	"strings"
	"testing"
)

func TestBucketIdentity(t *testing.T) {
	for _, c := range []struct{ scope, purpose, region, want string }{
		{"dev", "runtime", "us-sea", "rtk-cloud-dev-runtime-us-sea"},
		{"staging", "ota-firmware", "sg-sin-2", "rtk-cloud-staging-ota-firmware-sg-sin-2"},
		{"shared", "artifacts", "us-sea", "rtk-cloud-shared-artifacts-us-sea"},
		{"qa-team", "pki-backup", "us-iad", "rtk-cloud-qa-team-pki-backup-us-iad"},
	} {
		got, err := Bucket(c.scope, c.purpose, c.region)
		if err != nil || got != c.want {
			t.Fatalf("Bucket(%+v)=%q,%v", c, got, err)
		}
		if err := Validate(got, c.scope, c.purpose, c.region); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRejectInvalidIdentity(t *testing.T) {
	for _, c := range [][3]string{{"Dev", "runtime", "us-sea"}, {"dev", "media", "us-sea"}, {"shared", "runtime", "us-sea"}, {"dev", "runtime", "us-sea-1.linodeobjects.com"}, {"dev", "runtime", ""}, {strings.Repeat("a", 60), "runtime", "us-sea"}} {
		if _, err := Bucket(c[0], c[1], c[2]); err == nil {
			t.Fatalf("accepted invalid identity %v", c)
		}
	}
	if err := Validate("rtk-cloud-prod-runtime-us-sea", "dev", "runtime", "us-sea"); err == nil {
		t.Fatal("accepted another environment's bucket")
	}
}
