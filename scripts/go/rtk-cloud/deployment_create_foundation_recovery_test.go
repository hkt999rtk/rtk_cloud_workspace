package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshOnDeleteWaitUsesCapturedWriteOwnerWithoutRestart(t *testing.T) {
	for _, mode := range []string{"valid", "missing-owner", "uncertain-write", "foreign-plan", "wrong-stage"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			sha := strings.Repeat("b", 64)
			r := deploymentplan.Resource{ID: "StatefulSet/video-cloud-dev-secrets/openbao", Action: "create", Desired: deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": "video-cloud-dev-secrets"}, "spec": deploymentplan.Object{"updateStrategy": deploymentplan.Object{"type": "OnDelete"}}}}
			j := deploymentplan.Journal{PlanSHA256: sha, Owners: map[string]string{r.ID: "captured-original-write-uid"}}
			switch mode {
			case "missing-owner":
				j.Owners = nil
			case "uncertain-write":
				j.InFlight = r.ID
			case "foreign-plan":
				j.PlanSHA256 = strings.Repeat("f", 64)
			}
			if err := store.write(filepath.Join("deployment/rollback", sha, "journal.json"), mustProviderRecoveryJSON(j), false); err != nil {
				t.Fatal(err)
			}
			b := environmentCreateKubernetesBackend{execution: e, cfg: cfg, store: store, stagePlanSHA256: sha}
			if mode == "wrong-stage" {
				b.stagePlanSHA256 = "unqualified"
			}
			actual, err := b.initialOpenBaoWaitResource(context.Background(), r)
			if mode == "valid" {
				if err != nil || actual.Action != "retain" || actual.UID != "captured-original-write-uid" || r.Action != "create" || r.UID != "" {
					t.Fatal("initial readiness lost its captured owner or mutated the frozen create intent", err)
				}
			} else if err == nil {
				t.Fatal("uncertain or unrelated write owner admitted")
			}
		})
	}
}

func TestFoundationRecoveryRejectsChangedOriginalTransportAndSigner(t *testing.T) {
	for _, mode := range []string{"valid", "changed-local-key", "changed-recorded-cert", "missing-key", "duplicate-binding", "missing-binding"} {
		t.Run(mode, func(t *testing.T) {
			_, store, _, artifact := createStateFixture(t, "dev")
			sha := artifact.PlanSHA256
			child := &deploymentplan.Plan{}
			secret := func(name string, values map[string]string) {
				data := deploymentplan.Object{}
				for key, value := range values {
					data[key] = base64.StdEncoding.EncodeToString([]byte(value))
				}
				child.Resources = append(child.Resources, deploymentplan.Resource{Desired: deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": name}, "data": data}})
			}
			for _, role := range []string{"management", "openbao"} {
				material := environmentCreateTransport{CACert: "original-" + role + "-ca", Identities: map[string]environmentCreateTransportIdentity{}}
				names := []string{"account-manager", "pki-controller", "certissuer", "video-cloud-api", "video-cloud-api-app-pki"}
				if role == "openbao" {
					names = []string{"openbao"}
				}
				for _, name := range names {
					identity := environmentCreateTransportIdentity{Certificate: "original-" + name + "-cert", Key: "original-" + name + "-key"}
					material.Identities[name] = identity
					secretName := "pki-create-" + name + "-transport"
					if role == "openbao" {
						secretName = "openbao-transport-tls"
					}
					secret(secretName, map[string]string{"ca.crt": material.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key})
				}
				if mode == "changed-local-key" && role == "management" {
					identity := material.Identities["account-manager"]
					identity.Key = "different-key"
					material.Identities["account-manager"] = identity
				}
				if err := store.write(filepath.Join("deployment/create/material", sha, role+"-transport/identities.json"), mustProviderRecoveryJSON(material), false); err != nil {
					t.Fatal(err)
				}
			}
			signer := map[string]string{}
			for _, key := range []string{"access.key", "access.pub", "refresh.key", "refresh.pub"} {
				signer[key] = "original-" + key
				if err := store.write(filepath.Join("pki/account-manager-jwt", key), []byte(signer[key]), false); err != nil {
					t.Fatal(err)
				}
			}
			secret("account-manager-jwt", signer)
			if mode == "changed-recorded-cert" {
				deploymentplan.Map(child.Resources[0].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte("different-cert"))
			}
			if mode == "missing-key" {
				if err := os.Remove(filepath.Join(store.Root, "pki/account-manager-jwt/access.key")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "duplicate-binding" {
				child.Resources = append(child.Resources, child.Resources[0])
			}
			if mode == "missing-binding" {
				child.Resources = child.Resources[1:]
			}
			err := verifyFoundationMaterialBindings(store, sha, child)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("unexpected original material binding decision: %v", err)
			}
			if mode == "missing-key" {
				if _, err := os.Lstat(filepath.Join(store.Root, "pki/account-manager-jwt/access.key")); !os.IsNotExist(err) {
					t.Fatal("missing signer was regenerated")
				}
			}
		})
	}
}

func TestCreateStageInventoryIncludesOnlyPlannedTargetsAndNamedDescendants(t *testing.T) {
	ns := "video-cloud-dev-platform"
	object := func(kind, name string, owners []any) deploymentplan.Object {
		return deploymentplan.Object{"kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns, "ownerReferences": owners}}
	}
	owner := func(kind, name, uid string) []any {
		return []any{deploymentplan.Object{"controller": true, "kind": kind, "name": name, "uid": uid}}
	}
	deployment := object("Deployment", "redis", nil)
	set := object("ReplicaSet", "redis-current", owner("Deployment", "redis", "original-controller"))
	pod := object("Pod", "redis-pod", owner("ReplicaSet", "redis-current", "original-replicaset"))
	foreignUIDPod := object("Pod", "redis-foreign-owner", owner("ReplicaSet", "redis-current", "different-replicaset"))
	unrelated := object("Deployment", "other-stage", nil)
	unrelatedPod := object("Pod", "other-pod", owner("Deployment", "other-stage", "other-controller"))
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(deployment), Desired: deployment}}}
	actual := environmentCreateStageInventory(plan, []deploymentplan.Object{deployment, set, pod, foreignUIDPod, unrelated, unrelatedPod})
	if len(actual) != 4 {
		t.Fatalf("stage selected %d resources", len(actual))
	}
	seen := map[string]bool{}
	for _, value := range actual {
		seen[deploymentplan.ID(value)] = true
	}
	if !seen[deploymentplan.ID(foreignUIDPod)] || seen[deploymentplan.ID(unrelatedPod)] {
		t.Fatal("foreign named owner escaped verification or another stage was counted as this stage")
	}
}

func TestCreateReviewedRetainedStagesHaveDistinctImmutablePlans(t *testing.T) {
	object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "original", "namespace": "video-cloud-dev-platform", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}}, "data": deploymentplan.Object{"setting": "original"}}
	live := deploymentplan.Clone(object)
	deploymentplan.Map(live["metadata"])["uid"] = "original-uid"
	definitions := []deploymentplan.Definition{{Object: object, Phase: "configuration", Retain: true}}
	plans := map[string]string{}
	for _, stage := range []string{"foundation-recovery-first-review", "foundation-recovery-second-review"} {
		plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: "original-config", ReleaseSHA256: "original-release", BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions)}, definitions, []deploymentplan.Object{live})
		if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "retain" || plan.Resources[0].UID != "original-uid" {
			t.Fatal("retained stage changed its original owner or requested mutation", err)
		}
		if plans[plan.SHA256()] != "" {
			t.Fatal("new reviewed stage reused an immutable failed plan")
		}
		plans[plan.SHA256()] = stage
	}
}
