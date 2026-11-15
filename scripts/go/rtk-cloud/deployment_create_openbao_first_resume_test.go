package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoFirstResumeNeverRegeneratesMissingIdentity(t *testing.T) {
	cfg, store, e, operator := openBaoFirstQualificationFixture(t, "dev")
	b := environmentKubernetesBackend{Store: store}
	q, err := qualifyOpenBaoFirstRecovery(t.Context(), cfg, store, e, b, operator)
	if err != nil || q.OpenBao != "not-started" || q.Management != "absent" || q.Signer != "absent" {
		t.Fatal("isolated original checkpoint", err)
	}
	before := deploymentplan.Digest(e.Journal)
	_, cleanup, err := resumeOpenBaoFirstInitialized(t.Context(), cfg, store, e, b, operator, deploymentplan.Digest(q), io.Discard)
	cleanup()
	if err == nil || deploymentplan.Digest(e.Journal) != before {
		t.Fatal("unstarted or missing identity was resumed or its original journal changed")
	}
	for _, rel := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key", "pki/account-manager-jwt/access.key", "pki/account-manager-jwt/refresh.key", filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "management-transport", "identities.json")} {
		if _, err := os.Lstat(filepath.Join(store.Root, rel)); !os.IsNotExist(err) {
			t.Fatal("missing original identity was created or cannot be inspected", rel)
		}
	}
}
