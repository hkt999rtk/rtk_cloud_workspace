package main

import (
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitialListenerRoleAbsenceRequiresNativeEvidence(t *testing.T) {
	for _, raw := range []string{`{}`, `[]`, `null`, `["unrelated-role"]`} {
		if !initialListenerRoleAbsent([]byte(raw), "pki-controller-dev") {
			t.Fatal("native absence rejected", raw)
		}
	}
	for _, raw := range []string{``, `garbage`, `{"errors":["permission denied"]}`, `["pki-controller-dev"]`, `{"keys":["pki-controller-dev"]}`} {
		if initialListenerRoleAbsent([]byte(raw), "pki-controller-dev") {
			t.Fatal("occupied or unproven role accepted", raw)
		}
	}
	if initialListenerPolicy != "path \"*\" { capabilities = [\"deny\"] }\n" {
		t.Fatal("initial listener was granted PKI authority")
	}
}

func TestVerifiedListenerDefinitionsCompareByIdentityAndContent(t *testing.T) {
	cfg, p, _, _ := createServiceRecipeFixture(t, "dev")
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: p.Image}}}
	defs, err := renderEnvironmentCreateInitialPKIListeners(cfg, release, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", map[string]string{"PKI_OPERATOR_SIGNER_REF": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	reordered := append([]deploymentplan.Definition(nil), defs...)
	for i, j := 0, len(reordered)-1; i < j; i, j = i+1, j-1 {
		reordered[i], reordered[j] = reordered[j], reordered[i]
	}
	if !sameEnvironmentCreateDefinitions(defs, reordered) {
		t.Fatal("unchanged accepted listeners rejected solely for execution ordering")
	}
	compiled, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var canonical []deploymentplan.Definition
	for _, r := range compiled.Resources {
		canonical = append(canonical, deploymentplan.Definition{Object: r.Desired, Phase: r.Phase})
	}
	if sameEnvironmentCreateDefinitions(canonical, defs) {
		t.Fatal("fixture did not exercise compiler-added original ownership labels")
	}
	for _, d := range canonical {
		labels := deploymentplan.Map(deploymentplan.Map(d.Object["metadata"])["labels"])
		if labels["rtk.realtek.com/stack"] != p.Stack || labels["app.kubernetes.io/managed-by"] != "rtk-cloud" {
			t.Fatal("compiled ownership was not retained")
		}
	}
	changed := append([]deploymentplan.Definition(nil), reordered...)
	changed[0].Object = deploymentplan.Clone(changed[0].Object)
	deploymentplan.Map(changed[0].Object["metadata"])["namespace"] = "foreign-environment"
	if sameEnvironmentCreateDefinitions(defs, changed) {
		t.Fatal("foreign identity accepted")
	}
	changed = append([]deploymentplan.Definition(nil), reordered...)
	changed[0] = changed[1]
	if sameEnvironmentCreateDefinitions(defs, changed) {
		t.Fatal("duplicate listener identity accepted")
	}
	changed = append([]deploymentplan.Definition(nil), reordered...)
	changed[0].Phase = "foreign-phase"
	if sameEnvironmentCreateDefinitions(defs, changed) {
		t.Fatal("different execution phase accepted")
	}
}

func TestInitialCertIssuerAuthSeparationChangesOnlyItsOwnRole(t *testing.T) {
	cfg, _, runtime, _ := createServiceRecipeFixture(t, "dev")
	original := deploymentplan.Digest(runtime.InitialListeners)
	updated, selected, err := initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil || len(selected) != 1 || deploymentplan.ID(selected[0].Object) != "Deployment/video-cloud-dev-video-cloud/certissuer" || deploymentplan.Digest(runtime.InitialListeners) != original {
		t.Fatal("auth separation changed source or unrelated owners", err)
	}
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(selected[0].Object)["containers"])[0])
	if mqttFoundationEnv(container, "OPENBAO_KUBERNETES_ROLE") != "pki-initial-certissuer-dev" {
		t.Fatal("initial CertIssuer still inherits Controller role")
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"OPENBAO_KUBERNETES_ROLE": "pki-controller-dev"}))
	if !sameEnvironmentCreateDefinitions(runtime.InitialListeners, updated) {
		t.Fatal("auth separation changed images, serving material, namespaces or other settings")
	}
	if _, _, err := initialCertIssuerAuthDefinitions(cfg, nil); err == nil {
		t.Fatal("missing original owner accepted")
	}
}

func TestInitialListenerCheckpointRejectsUncertainOrForeignWrites(t *testing.T) {
	for _, mode := range []string{"known-rollout-failure", "in-flight", "missing-owner", "foreign-failure", "accepted", "changed-intent"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			plan := deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped"}}
			journal := deploymentplan.Journal{Owners: map[string]string{}, Failed: "Deployment/video-cloud-dev-video-cloud/pki-controller"}
			for _, name := range []string{"pki-controller", "certissuer"} {
				for _, kind := range []string{"Deployment", "Service", "ServiceAccount"} {
					id := kind + "/video-cloud-dev-video-cloud/" + name
					r := deploymentplan.Resource{ID: id, Action: "create", Desired: deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": name}}}
					plan.Resources = append(plan.Resources, r)
					journal.Owners[id] = "original-owner-" + name + "-" + kind
					if id != journal.Failed {
						journal.Completed = append(journal.Completed, id)
					}
				}
			}
			sha := plan.SHA256()
			journal.PlanSHA256 = sha
			switch mode {
			case "in-flight":
				journal.InFlight = journal.Failed
			case "missing-owner":
				delete(journal.Owners, journal.Failed)
			case "foreign-failure":
				journal.Failed = "foreign-stage"
			case "accepted":
				journal.Accepted = true
			}
			for name, value := range map[string]any{"plan.json": plan, "journal.json": journal} {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/rollback", sha, name), mustProviderRecoveryJSON(value)); err != nil {
					t.Fatal(err)
				}
			}
			intent, err := environmentCreateKubernetesStageIntent(&plan)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := environmentCreatePlanSHA256(intent)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed-intent" {
				digest = strings.Repeat("b", 64)
			}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{KubernetesLeases: []deploymentplan.LeaseOwner{{Holder: sha + ":original-owner"}}, Entries: []environmentCreateJournalEntry{{Key: "kubernetes/pki-initial-listeners", State: "pending", IntentSHA256: digest}}}}
			p := createFoundationRecoveryPlan{Plan: environmentCreatePlan{Stack: "video-cloud-dev"}}
			got, _, err := loadInitialListenerCheckpoint(store, e, &p)
			if mode == "known-rollout-failure" {
				if err != nil || got.SHA256() != sha || p.CheckpointPlanSHA != sha {
					t.Fatal("known original write checkpoint rejected", err)
				}
			} else if err == nil {
				t.Fatal("unsafe listener checkpoint accepted", mode)
			}
		})
	}
}
