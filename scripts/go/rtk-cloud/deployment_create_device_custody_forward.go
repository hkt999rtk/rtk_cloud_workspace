package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func readDeviceCustodyReadyRoot(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (environmentCreateRegisteredIssuer, error) {
	var root environmentCreateRegisteredIssuer
	bad := func() (environmentCreateRegisteredIssuer, error) {
		return root, createProviderRecoveryBlock("original Device Root intent, native READY document and allocated receipt do not match")
	}
	base := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "bootstrap", "device")
	raw, err := readBoundedEnvironmentFile(filepath.Join(base, "root-intent.json"), true)
	var intent struct {
		Environment string   `json:"environment"`
		ID          string   `json:"issuer_id"`
		Consumers   []string `json:"bundle_consumers"`
	}
	if err != nil || json.Unmarshal(raw, &intent) != nil || intent.Environment != cfg.Environment || !environmentBootstrapOwnerUUID.MatchString(intent.ID) || !slices.Equal(intent.Consumers, []string{"pkibroker", "video-cloud-api"}) {
		return bad()
	}
	intentSHA, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return bad()
	}
	var document map[string]any
	if readExecutionJSON(filepath.Join(base, "root-ready.json"), &document) != nil {
		return bad()
	}
	root, err = environmentCreateRegisteredIssuerFromMap(document, "")
	if err != nil || validateEnvironmentCreateDeviceRoot(cfg, root) != nil || root.Status != "ready" || root.ID != intent.ID {
		return bad()
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key != "device-root-bootstrap" || entry.State != "allocated" || entry.IntentSHA256 != intentSHA {
			continue
		}
		var receipt map[string]any
		expected := deploymentplan.Object{"environment": root.Environment, "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion}
		if json.Unmarshal(entry.Receipt, &receipt) == nil && deploymentplan.Digest(receipt) == deploymentplan.Digest(expected) {
			return root, nil
		}
	}
	return bad()
}

func qualifyDeviceCustodyForward(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string) (*deploymentplan.Plan, *deploymentplan.Plan, issuerEnrollmentContinuationProof, error) {
	var empty issuerEnrollmentContinuationProof
	parent, ok := deviceCustodyContinuationEntry(e.Journal)
	if !ok {
		return nil, nil, empty, createProviderRecoveryBlock("original Device trust continuation checkpoint is unavailable")
	}
	proof, err := verifyIssuerCoreContinuationAdmission(store, e, parent)
	if err != nil {
		return nil, nil, empty, err
	}
	root, err := readDeviceCustodyReadyRoot(cfg, store, e)
	if err != nil {
		return nil, nil, empty, err
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return nil, nil, empty, err
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return nil, nil, empty, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return nil, nil, empty, err
	}
	if _, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return nil, nil, empty, err
	}
	actual, err := client.ReadIssuer(ctx, root.ID)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(root.Document) {
		return nil, nil, empty, createProviderRecoveryBlock("original Device Root registry record differs from its READY receipt")
	}
	token, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao", "root-token"), true)
	if err != nil {
		return nil, nil, empty, createProviderRecoveryBlock("original OpenBao operator credential is unavailable")
	}
	signer, err := environmentCreateOpenBaoExec(ctx, cfg, b, append([]byte(strings.TrimSpace(string(token))), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json "+root.Reference+"/issuer/default\n")
	if err != nil {
		return nil, nil, empty, createProviderRecoveryBlock("original Device signer is unavailable")
	}
	if _, err := deviceTrustSignerObservation(signer, root); err != nil {
		return nil, nil, empty, err
	}
	original, err := readEnvironmentCreateStageInput(store, e, "device-root-trust")
	if err != nil || original == nil {
		return nil, nil, empty, createProviderRecoveryBlock("original Device trust stage input is unavailable")
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, "device-root-trust", original)
	if err != nil || !exists {
		return nil, nil, empty, createProviderRecoveryBlock("original Device trust write journal is unavailable")
	}
	live, err := b.Inventory(ctx, original.Stack)
	if err != nil {
		return nil, nil, empty, err
	}
	forward, err := compileDeviceCustodyForward(original, journal, live)
	if err != nil {
		return nil, nil, empty, err
	}
	lease, err := b.Read(ctx, "Lease/"+original.Stack+"-video-cloud/rtk-deployment-execution")
	if err != nil || lease != nil {
		return nil, nil, empty, createProviderRecoveryBlock("original Device trust execution Lease is not released")
	}
	if err := forward.Admit(ctx, b); err != nil {
		return nil, nil, empty, err
	}
	return original, forward, proof, nil
}

// The parent continuation remains pending while its own Device trust child is
// interrupted. Authenticate the parent separately before admitting writes.
func deviceCustodyContinuationEntry(j environmentCreateJournal) (environmentCreateJournalEntry, bool) {
	var parent environmentCreateJournalEntry
	index := -1
	for i, entry := range j.Entries {
		if strings.HasPrefix(entry.Key, issuerEnrollmentContinuationPrefix) && entry.State == "pending" {
			if index >= 0 || entry.Phase != "continuation" || len(entry.Receipt) != 0 || entry.StartedAt.IsZero() || !environmentPreflightDigest.MatchString(strings.TrimPrefix(entry.Key, issuerEnrollmentContinuationPrefix)) {
				return parent, false
			}
			parent, index = entry, i
		}
	}
	if index < 0 {
		return parent, false
	}
	remaining := j
	remaining.Entries = slices.Clone(j.Entries)
	remaining.Entries = slices.Delete(remaining.Entries, index, index+1)
	return parent, isDeviceTrustCheckpoint(remaining)
}

// Continue only after every original Device trust write has a recorded owner.
// The failed broker keeps its UID, identity and volume bindings. Its obsolete
// PVC permissions initializer is the sole change in the forward plan.
func compileDeviceCustodyForward(original *deploymentplan.Plan, journal deploymentplan.Journal, live []deploymentplan.Object) (*deploymentplan.Plan, error) {
	if original == nil {
		return nil, createProviderRecoveryBlock("original Device trust plan is unavailable")
	}
	broker := "Deployment/" + original.Stack + "-video-cloud/mqtt-pki"
	if journal.PlanSHA256 != original.SHA256() || journal.Failed != broker || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || len(journal.Completed) != len(original.Resources) || len(journal.Owners) != len(original.Resources) {
		return nil, createProviderRecoveryBlock("Device trust requires its exact complete original write prefix and failed broker rollout")
	}
	observed := map[string]deploymentplan.Object{}
	for _, object := range live {
		observed[deploymentplan.ID(object)] = object
	}
	definitions := make([]deploymentplan.Definition, 0, len(original.Resources))
	found := false
	for i, resource := range original.Resources {
		object := observed[resource.ID]
		uid := journal.Owners[resource.ID]
		if journal.Completed[i] != resource.ID || uid == "" || deploymentplan.Map(object["metadata"])["uid"] != uid || !deploymentplan.Matches(resource.Desired, object) {
			return nil, createProviderRecoveryBlock("Device trust original owner or body changed: " + resource.ID)
		}
		desired := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(desired["metadata"])["uid"] = uid
		if resource.ID == broker {
			pod := environmentPKIPod(desired)
			containers := certIssuerObjectList(pod["containers"])
			if len(containers) != 1 || mqttFoundationEnv(deploymentplan.Map(containers[0]), "PKI_STATE_BACKEND") != "openbao" || managedNamedField(pod["initContainers"], deviceBrokerTrustInitializer) == nil {
				return nil, createProviderRecoveryBlock("original broker lacks the expected OpenBao custody and obsolete initializer")
			}
			pod["initContainers"] = environmentCreateServiceWithoutNamed(pod["initContainers"], deviceBrokerTrustInitializer)
			found = true
		}
		definitions = append(definitions, deploymentplan.Definition{Object: desired, Phase: resource.Phase, Dependencies: resource.Dependencies, Retain: resource.ID != broker})
	}
	if !found {
		return nil, createProviderRecoveryBlock("original Device trust broker is missing")
	}
	inputs := original.Inputs
	inputs.BindingsSHA256 = deploymentplan.Digest([]string{"device-custody-forward", original.SHA256(), deploymentplan.Digest(journal), environmentCreateStageBindingsSHA("device-custody-forward", definitions)})
	plan, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		return nil, err
	}
	for _, resource := range plan.Resources {
		expected := "retain"
		if resource.ID == broker {
			expected = "update"
		}
		if resource.Action != expected || resource.UID != journal.Owners[resource.ID] {
			return nil, createProviderRecoveryBlock("Device trust forward changed an original resource owner: " + resource.ID)
		}
	}
	return plan, nil
}
