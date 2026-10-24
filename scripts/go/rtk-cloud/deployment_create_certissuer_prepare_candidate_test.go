package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestManagedCoreCertIssuerPreparationRetainsSuccessfulControllerAndOnlyUpdatesFormalImage(t *testing.T) {
	core := managedCoreInterruptedFixture(t)
	core.CertIssuerPrepare = &createCertIssuerPrepareImageRecovery{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("d", 64), SourceRevision: strings.Repeat("c", 40)}, SelectionSHA: strings.Repeat("a", 64), ManifestSHA: strings.Repeat("b", 64)}
	before := deploymentplan.Digest(core.Plan)
	defs, err := managedCorePartialDefinitions(core)
	if err != nil {
		t.Fatal(err)
	}
	current, j, err := managedCorePartialCurrentPlan(core)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for i, res := range current.Resources {
		body := deploymentplan.Clone(res.Desired)
		uid := j.Owners[res.ID]
		if i == 15 {
			body = deploymentplan.Clone(res.Baseline)
			uid = res.UID
		}
		deploymentplan.Map(body["metadata"])["uid"] = uid
		live = append(live, body)
	}
	candidate, err := deploymentplan.Compile(current.Inputs, defs, live)
	if err != nil || validateManagedCorePartialCandidate(core, candidate) != nil {
		t.Fatal("exact15retain/1update rejected", err)
	}
	updates := 0
	for i, res := range candidate.Resources {
		if res.Action == "update" {
			updates++
			if i != 14 {
				t.Fatal("updated completed controller or unrelated resource")
			}
		}
	}
	if updates != 1 || deploymentplan.Digest(core.Plan) != before || mqttFoundationEnv(mqttFoundationPrimary(candidate.Resources[13].Desired), "PKI_FIXED_DEVICE_ROOT_TRUST") != "true" {
		t.Fatal("replayed old controller or changed original authority")
	}
	for _, mode := range []string{"controller-action", "controller-image", "issuer-env", "issuer-image", "issuer-uid", "issuer-private-path", "dependency", "baseline", "account-action"} {
		t.Run(mode, func(t *testing.T) {
			var changed deploymentplan.Plan
			json.Unmarshal(mustProviderRecoveryJSON(candidate), &changed)
			switch mode {
			case "controller-action":
				changed.Resources[13].Action = "update"
			case "controller-image":
				mqttFoundationPrimary(changed.Resources[13].Desired)["image"] = core.CertIssuerPrepare.Image.Reference
			case "issuer-env":
				mqttFoundationPrimary(changed.Resources[14].Desired)["env"] = []any{deploymentplan.Object{"name": "UNREVIEWED", "value": "true"}}
			case "issuer-image":
				mqttFoundationPrimary(changed.Resources[14].Desired)["image"] = "registry.invalid/latest:tag"
			case "issuer-uid":
				changed.Resources[14].UID = "11111111-1111-4111-8111-111111111999"
			case "issuer-private-path":
				environmentPKIPod(changed.Resources[14].Desired)["volumes"] = []any{deploymentplan.Object{"name": "foreign"}}
			case "dependency":
				changed.Resources[14].Dependencies = nil
			case "baseline":
				mqttFoundationPrimary(changed.Resources[14].Baseline)["image"] = core.CertIssuerPrepare.Image.Reference
			case "account-action":
				changed.Resources[15].Action = "update"
			}
			if validateManagedCorePartialCandidate(core, &changed) == nil {
				t.Fatal("accepted unqualified scope drift")
			}
		})
	}
}
