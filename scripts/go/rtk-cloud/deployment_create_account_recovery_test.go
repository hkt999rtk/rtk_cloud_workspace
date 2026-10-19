package main

import (
	"encoding/base64"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateAccountMigrationUsesFormalImageAndBoundRuntime(t *testing.T) {
	cfg, _, _, _ := createServiceRecipeFixture(t, "dev")
	_, _, release := completeEnvironmentRenderFixture("dev")
	definitions, err := renderEnvironmentCreateAccountMigration(cfg, release)
	if err != nil {
		t.Fatal(err)
	}
	job := definitions[0].Object
	spec := deploymentplan.Map(job["spec"])
	pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if container["image"] != release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference || deploymentplan.Digest(container["command"]) != deploymentplan.Digest([]string{"/app/rtk-account-manager-migrate"}) {
		t.Fatal("migration did not use the native binary and exact formal image")
	}
	ref := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(container["envFrom"])[0])["secretRef"])
	if ref["name"] != "account-manager-runtime" || container["env"] != nil || spec["backoffLimit"] != float64(0) || spec["activeDeadlineSeconds"] != float64(300) || pod["restartPolicy"] != "Never" || pod["automountServiceAccountToken"] != false {
		t.Fatal("migration lost its bound runtime or gained automatic replay")
	}
}

func TestCatalogEmailKeyMatchesNativeEncoding(t *testing.T) {
	a, err := generateCatalogRuntimeSecret("email-outbox-encryption")
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateCatalogRuntimeSecret("email-outbox-encryption")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{a, b} {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			t.Fatal("email key is not standard base64 of 32 bytes")
		}
	}
	if a == b {
		t.Fatal("new keys reused identity")
	}
}
