package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailDNSOutputDoesNotChangeFrozenConfiguration(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	h := &createNormalTailHandoff{Execution: &environmentCreateExecution{Store: store}}
	ctx := withCreateNormalTailHandoff(context.Background(), h)
	before := environmentDeploymentSnapshot{DNSStateSHA256: deploymentplan.Digest("before publication"), Secrets: map[string]string{"original": "same private value"}}
	after := before
	after.DNSStateSHA256 = deploymentplan.Digest("genuine recorded DNS publication")
	if environmentDeploymentSnapshotDigest(ctx, cfg, before) != environmentDeploymentSnapshotDigest(ctx, cfg, after) {
		t.Fatal("the deployment's own DNS publication invalidated its frozen configuration")
	}
	if environmentDeploymentSnapshotDigest(context.Background(), cfg, before) == environmentDeploymentSnapshotDigest(context.Background(), cfg, after) {
		t.Fatal("ordinary updates lost their original DNS-state fence")
	}
	after.Secrets = map[string]string{"original": "changed private value"}
	if environmentDeploymentSnapshotDigest(ctx, cfg, before) == environmentDeploymentSnapshotDigest(ctx, cfg, after) {
		t.Fatal("a runtime credential change was hidden with the DNS output")
	}
	after = before
	after.StorageEvidenceSHA256 = deploymentplan.Digest("foreign allocation")
	if environmentDeploymentSnapshotDigest(ctx, cfg, before) == environmentDeploymentSnapshotDigest(ctx, cfg, after) {
		t.Fatal("a storage identity change was hidden with the DNS output")
	}
}

func TestCreateNormalTailDefersOnlyReviewedOriginalWorkloadReadiness(t *testing.T) {
	for _, mode := range []string{"completed-bootstrap", "fresh-create", "missing-handoff", "foreign-owner", "health"} {
		t.Run(mode, func(t *testing.T) {
			r := newDeploymentCheckRuntime(context.Background(), "dev")
			id := "Deployment/video-cloud-dev-video-cloud/video-cloud-api"
			p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "create", CreateParentSHA256: strings.Repeat("a", 64), CreateHandoffSHA256: strings.Repeat("b", 64)}, Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: "original"}}}
			r.preflightPlan = p
			var d liveDeployment
			d.Metadata.Name, d.Metadata.UID = "video-cloud-api", "original"
			switch mode {
			case "fresh-create":
				p.CreateParentSHA256, p.CreateHandoffSHA256 = "", ""
			case "missing-handoff":
				p.CreateHandoffSHA256 = ""
			case "foreign-owner":
				d.Metadata.UID = "replacement"
			case "health":
				r.preflightPlan = nil
			}
			err := r.checkPKIWorkloadPhase("video-cloud-dev-video-cloud", d)
			if (err == nil) != (mode == "completed-bootstrap") {
				t.Fatalf("unexpected readiness disposition: %v", err)
			}
		})
	}
}
