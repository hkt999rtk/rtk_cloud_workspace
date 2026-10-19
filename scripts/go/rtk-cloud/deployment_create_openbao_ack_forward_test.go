package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateOpenBaoACKDefinitionChangesOnlyQualifiedNativeImagesAndPIDToken(t *testing.T) {
	old := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	script := `umask 077; printf '%s\n' "$$" > /run/openbao-pki/private/openbao.pid; exec bao server -config=/openbao/config/file.hcl`
	original := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": "video-cloud-dev-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"shareProcessNamespace": true, "initContainers": []any{deploymentplan.Object{"name": "openbao-pki-install", "image": old, "args": []any{"install"}}}, "containers": []any{deploymentplan.Object{"name": "openbao", "image": "retained-provider-image", "command": []any{"/bin/sh", "-ec", script}}, deploymentplan.Object{"name": "openbao-pki", "image": old, "args": []any{"run"}}}, "volumes": []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-identity"}}}}}}}
	ack := createOpenBaoACKRecovery{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)}, SelectionSHA: strings.Repeat("c", 64), ManifestSHA: strings.Repeat("d", 64), OriginalPlanSHA: strings.Repeat("e", 64), OriginalJournalSHA: strings.Repeat("f", 64), SourcePodUID: "11111111-1111-4111-8111-111111111111", Original: deploymentplan.Resource{ID: deploymentplan.ID(original), UID: "22222222-2222-4222-8222-222222222222", Desired: original}, SourceProcess: deploymentplan.Object{"pid": "24", "start_time": "13380439", "executable_sha256": strings.Repeat("a", 64), "executable": "/bin/bao", "uid": "100", "pid_marker_sha256": environmentPreflightBytesSHA256([]byte("$\n"))}}
	before := deploymentplan.Digest(original)
	defs, err := createOpenBaoACKDefinitions(&ack)
	if err != nil || len(defs) != 1 || deploymentplan.Digest(original) != before {
		t.Fatal("original owned recipe changed", err)
	}
	wanted := deploymentplan.Clone(original)
	pod := environmentPKIPod(wanted)
	init, _ := environmentPKINamedFields(pod["initContainers"])
	cs, _ := environmentPKINamedFields(pod["containers"])
	init["openbao-pki-install"]["image"] = ack.Image.Reference
	cs["openbao-pki"]["image"] = ack.Image.Reference
	cs["openbao"]["command"] = []any{"/bin/sh", "-ec", strings.Replace(script, `"$$"`, `"$$$$"`, 1)}
	if deploymentplan.Digest(defs[0].Object) != deploymentplan.Digest(wanted) || defs[0].Phase != "workload" || len(defs[0].Dependencies) != 0 {
		t.Fatal("repair modified provider, keys, claims or an unrelated recipe")
	}
	for _, mode := range []string{"wrong-owner", "new-pod", "changed-marker", "extra-provider-token", "foreign-install-image", "foreign-provider-command", "missing-shared-process"} {
		t.Run(mode, func(t *testing.T) {
			changed := ack
			changed.Original.Desired = deploymentplan.Clone(original)
			changed.SourceProcess = deploymentplan.Clone(ack.SourceProcess)
			p := environmentPKIPod(changed.Original.Desired)
			c, _ := environmentPKINamedFields(p["containers"])
			i, _ := environmentPKINamedFields(p["initContainers"])
			switch mode {
			case "wrong-owner":
				changed.Original.ID = "StatefulSet/other-secrets/openbao"
			case "new-pod":
				changed.SourcePodUID = ""
			case "changed-marker":
				changed.SourceProcess["pid_marker_sha256"] = strings.Repeat("0", 64)
			case "extra-provider-token":
				c["openbao"]["command"] = []any{"/bin/sh", "-ec", script + "; " + script}
			case "foreign-install-image":
				i["openbao-pki-install"]["image"] = "other"
			case "foreign-provider-command":
				c["openbao"]["command"] = []any{"/bin/sh", "-ec", "exec other-provider"}
			case "missing-shared-process":
				p["shareProcessNamespace"] = false
			}
			if _, err := createOpenBaoACKDefinitions(&changed); err == nil {
				t.Fatal("changed original custody or image role escaped guard")
			}
		})
	}
}
