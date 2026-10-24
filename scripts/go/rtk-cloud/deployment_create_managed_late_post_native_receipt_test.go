package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLatePostNativeCompletionReceiptBindsExactRawTombstone(t *testing.T) {
	for _, mode := range []string{"genuine", "different-valid-sha", "missing-sha", "changed-native", "missing-qualified-proof"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _ := managedLatePostNativeAuditFixture(t)
			post := r.PostNativeCleanup
			native, sha := post.Native, post.TombstoneSHA
			switch mode {
			case "different-valid-sha":
				sha = strings.Repeat("9", 64)
			case "missing-sha":
				sha = ""
			case "changed-native":
				native.CleanupSHA256 = strings.Repeat("9", 64)
			case "missing-qualified-proof":
				post = nil
			}
			before := deploymentplan.Digest(r)
			if managedLatePostNativeCompletionReceiptMatches(post, native, sha) != (mode == "genuine") || before != deploymentplan.Digest(r) {
				t.Fatal("durable receipt accepted substituted raw tombstone or changed source")
			}
		})
	}
}
