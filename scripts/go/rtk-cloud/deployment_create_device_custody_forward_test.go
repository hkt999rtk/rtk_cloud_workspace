package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestDeviceCustodyReadyRootRequiresOriginalIntentAndReceipt(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	cfg, _, root := createDeviceRecipeFixture(t, "dev")
	parent := strings.Repeat("a", 64)
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"environment": "dev", "issuer_id": root.ID, "bundle_consumers": []string{"pkibroker", "video-cloud-api"}})
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("deployment", "create", parent, "bootstrap", "device")
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "root-intent.json"), json.RawMessage(intent)); err != nil {
		t.Fatal(err)
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "root-ready.json"), root.Document); err != nil {
		t.Fatal(err)
	}
	receipt := deploymentplan.Object{"environment": root.Environment, "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion}
	e := &environmentCreateExecution{Journal: environmentCreateJournal{PlanSHA256: parent, Entries: []environmentCreateJournalEntry{{Key: "device-root-bootstrap", State: "allocated", IntentSHA256: intentSHA, Receipt: mustProviderRecoveryJSON(receipt)}}}}
	if got, err := readDeviceCustodyReadyRoot(cfg, store, e); err != nil || got.ID != root.ID {
		t.Fatal("original READY root rejected", err)
	}
	e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
	if _, err := readDeviceCustodyReadyRoot(cfg, store, e); err == nil {
		t.Fatal("foreign intent accepted")
	}
}

func TestDeviceCustodyContinuationRecognizesOnlyOriginalPendingParent(t *testing.T) {
	j := deviceTrustCheckpointFixture()
	parent := environmentCreateJournalEntry{Key: issuerEnrollmentContinuationPrefix + strings.Repeat("a", 64), Phase: "continuation", State: "pending", StartedAt: time.Now()}
	j.Entries = append([]environmentCreateJournalEntry{parent}, j.Entries...)
	if got, ok := deviceCustodyContinuationEntry(j); !ok || got.Key != parent.Key {
		t.Fatal("original pending parent and Device trust checkpoint not recognized")
	}
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "foreign", State: "pending", IntentSHA256: strings.Repeat("c", 64)})
	if _, ok := deviceCustodyContinuationEntry(j); ok {
		t.Fatal("recognized a foreign pending write")
	}
}

func TestDeviceCustodyForwardRetainsOwnersAndChangesOnlyInitializer(t *testing.T) {
	stack := "video-cloud-dev"
	ns := stack + "-video-cloud"
	broker := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki", "namespace": ns}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pkibroker", "env": environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao"})}}, "initContainers": []any{deploymentplan.Object{"name": deviceBrokerTrustInitializer}, deploymentplan.Object{"name": "other"}}}}}}
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "device-trust", "namespace": ns}}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	original, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: config, Phase: "configuration"}, {Object: broker, Phase: "workload"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: original.SHA256(), Failed: deploymentplan.ID(broker), Owners: map[string]string{}}
	var live []deploymentplan.Object
	for _, resource := range original.Resources {
		object := deploymentplan.Clone(resource.Desired)
		uid := "original-" + resource.ID
		deploymentplan.Map(object["metadata"])["uid"] = uid
		j.Completed = append(j.Completed, resource.ID)
		j.Owners[resource.ID] = uid
		live = append(live, object)
	}
	forward, err := compileDeviceCustodyForward(original, j, live)
	if err != nil {
		t.Fatal(err)
	}
	for i, resource := range forward.Resources {
		expected := deploymentplan.Clone(original.Resources[i].Desired)
		action := "retain"
		if resource.ID == j.Failed {
			action = "update"
			pod := environmentPKIPod(expected)
			pod["initContainers"] = environmentCreateServiceWithoutNamed(pod["initContainers"], deviceBrokerTrustInitializer)
		}
		if resource.Action != action || resource.UID != j.Owners[resource.ID] || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(expected) {
			t.Fatal("unexpected resource or owner change", resource.ID)
		}
	}
	deploymentplan.Map(live[1]["metadata"])["uid"] = "replacement"
	if _, err := compileDeviceCustodyForward(original, j, live); err == nil {
		t.Fatal("replacement broker owner accepted")
	}
}
