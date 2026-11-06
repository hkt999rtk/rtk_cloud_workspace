package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingTransportMaterialStage = "staging-temporary-transport-material"
const stagingTransportReloadStage = "staging-temporary-transport-consumers"

func stagingMQTTSourceEntries(entries []environmentCreateJournalEntry) []environmentCreateJournalEntry {
	for len(entries) > 0 {
		switch entries[len(entries)-1].Key {
		case "kubernetes/" + stagingTransportMaterialStage, "kubernetes/" + stagingTransportReloadStage:
			entries = entries[:len(entries)-1]
		default:
			return entries
		}
	}
	return entries
}

func prepareStagingMQTTTransport(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, b environmentKubernetesBackend) (environmentCreateTransport, string, error) {
	var empty environmentCreateTransport
	bad := createProviderRecoveryBlock("original staging bootstrap TLS or its recorded owners could not be verified")
	source := stagingMQTTSourceEntries(e.Journal.Entries)
	boundary, err := validateStagingMQTTRootTail(source)
	if err != nil {
		return empty, "", err
	}
	failed, _, err := originalCreateChildAtIntent(store, e, source[boundary+4])
	if err != nil || len(source) != boundary+7 {
		return empty, "", bad
	}
	for i, stage := range []string{"mqtt-root-trust-rwo-", "mqtt-root-trust-rwo-spread-"} {
		entry := source[boundary+5+i]
		if entry.Key != "kubernetes/"+stage+failed.SHA256()[:12] {
			return empty, "", bad
		}
	}
	original := map[string]environmentCreateTransport{}
	parent := e.Journal.PlanSHA256
	for _, role := range []string{"management", "openbao"} {
		var value environmentCreateTransport
		path := filepath.Join("deployment/create/material", parent, role+"-transport")
		if readExecutionJSON(filepath.Join(store.Root, path, "identities.json"), &value) != nil {
			return empty, "", bad
		}
		ca, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path, "ca.crt"), true)
		if err != nil || string(ca) != value.CACert {
			return empty, "", bad
		}
		original[role] = value
	}
	originalCA, err := parseFirstCertificatePEM([]byte(original["openbao"].CACert))
	deadline, bounded := ctx.Deadline()
	if err != nil || !bounded {
		return empty, "", bad
	}
	if len(source) == len(e.Journal.Entries) && originalCA.NotAfter.After(deadline.Add(15*time.Minute)) {
		_, management, err := foundationRecoveryMaterialsForExecution(store, parent, e, createRecoveryTransportMinimumLife(ctx, 0, true))
		return management, filepath.Join(store.Root, "deployment/create/material", parent, "openbao-transport/ca.crt"), err
	}
	if _, _, err := activeTransportOriginalIdentity(ctx, cfg, store, e, b); err != nil {
		return empty, "", err
	}
	fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.staging.temporary-tls")
	base := filepath.Join("deployment/create", parent, "service-authorization-recovery/temporary-transport")
	candidate := filepath.Join(base, "candidate.json")
	var renewed map[string]environmentCreateTransport
	if _, err := os.Lstat(filepath.Join(store.Root, candidate)); os.IsNotExist(err) {
		if len(source) != len(e.Journal.Entries) {
			return empty, "", bad
		}
		renewed = map[string]environmentCreateTransport{}
		for _, role := range []string{"management", "openbao"} {
			value, _, err := renewInitialTransportMaterial(original[role], time.Now().UTC())
			if err != nil {
				return empty, "", err
			}
			renewed[role] = value
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, candidate, mustProviderRecoveryJSON(renewed)); err != nil {
			return empty, "", err
		}
	} else if err != nil || readExecutionJSON(filepath.Join(store.Root, candidate), &renewed) != nil {
		return empty, "", bad
	}
	for _, role := range []string{"management", "openbao"} {
		before, after := original[role], renewed[role]
		ca, err := parseFirstCertificatePEM([]byte(after.CACert))
		if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || ca.Subject.CommonName != "rtk-create-"+cfg.Environment+"-"+role || !ca.NotAfter.After(deadline.Add(15*time.Minute)) || len(before.Identities) != len(after.Identities) {
			return empty, "", bad
		}
		for name, old := range before.Identities {
			next := after.Identities[name]
			leaf, leafErr := parseFirstCertificatePEM([]byte(next.Certificate))
			oldLeaf, oldErr := parseFirstCertificatePEM([]byte(old.Certificate))
			if leafErr != nil || oldErr != nil || next.Key != old.Key || leaf.CheckSignatureFrom(ca) != nil || string(leaf.RawSubjectPublicKeyInfo) != string(oldLeaf.RawSubjectPublicKeyInfo) || string(leaf.RawSubject) != string(oldLeaf.RawSubject) || deploymentplan.Digest(leaf.DNSNames) != deploymentplan.Digest(oldLeaf.DNSNames) || deploymentplan.Digest(leaf.IPAddresses) != deploymentplan.Digest(oldLeaf.IPAddresses) || !leaf.NotAfter.After(deadline.Add(15*time.Minute)) {
				return empty, "", bad
			}
		}
	}
	caPath := filepath.Join(base, "openbao-ca.crt")
	if err := writeEnvironmentCreateDerivedPrivateFile(store, caPath, []byte(renewed["openbao"].CACert)); err != nil {
		return empty, "", err
	}
	if err := verifyActiveTransportMounted(ctx, cfg, b, original["openbao"], renewed["openbao"], true); err != nil {
		return empty, "", err
	}
	foundation, journal, err := acceptedCreateStage(store, e, "kubernetes/foundation")
	if err != nil {
		return empty, "", err
	}
	reconciling := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingTransportMaterialStage) != nil
	var materials []deploymentplan.Definition
	for _, resource := range foundation.Resources {
		object, changed := initialTransportOverride(resource.Desired, renewed)
		if !changed {
			continue
		}
		actual, err := b.Read(ctx, resource.ID)
		uid := journal.Owners[resource.ID]
		if err != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(actual["metadata"])["uid"] != uid ||
			!deploymentplan.Matches(resource.Desired, actual) && (!reconciling || !deploymentplan.Matches(object, actual)) {
			return empty, "", bad
		}
		deploymentplan.Map(object["metadata"])["uid"] = uid
		materials = append(materials, deploymentplan.Definition{Object: object, Phase: resource.Phase})
	}
	if len(materials) != 10 {
		return empty, "", bad
	}
	if err := applyEnvironmentCreateStage(ctx, cfg, store, plan, e, stagingTransportMaterialStage, materials, os.Stdout); err != nil {
		return empty, "", err
	}
	leaf := renewed["openbao"].Identities["openbao"]
	script := "set -eu; for n in $(seq 1 90); do cert=$(sha256sum /openbao/transport/tls.crt | cut -d' ' -f1); ca=$(sha256sum /openbao/transport/ca.crt | cut -d' ' -f1); key=$(sha256sum /openbao/transport/tls.key | cut -d' ' -f1); if [ \"$cert\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Certificate)) + "\" ] && [ \"$ca\" = \"" + environmentPreflightBytesSHA256([]byte(renewed["openbao"].CACert)) + "\" ] && [ \"$key\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Key)) + "\" ]; then kill -HUP 1; exit 0; fi; sleep 2; done; exit 1"
	if err := e.Check(ctx); err != nil {
		return empty, "", err
	}
	if _, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script); err != nil {
		return empty, "", err
	}
	if _, _, err := verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, b); err != nil {
		return empty, "", err
	}
	core, coreJournal, err := acceptedCreateStage(store, e, "kubernetes/service-managed-core")
	if err != nil {
		return empty, "", err
	}
	var consumers []deploymentplan.Definition
	for _, resource := range core.Resources {
		if !strings.HasPrefix(resource.ID, "Deployment/"+plan.Stack+"-video-cloud/") ||
			!strings.HasSuffix(resource.ID, "/pki-controller") && !strings.HasSuffix(resource.ID, "/certissuer") {
			continue
		}
		object := activeTransportConsumerRestart(resource.Desired, renewed["openbao"].CACert)
		actual, err := b.Read(ctx, resource.ID)
		uid := coreJournal.Owners[resource.ID]
		if err != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(actual["metadata"])["uid"] != uid ||
			!deploymentplan.Matches(resource.Desired, actual) && (createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingTransportReloadStage) == nil || !deploymentplan.Matches(object, actual)) {
			return empty, "", bad
		}
		deploymentplan.Map(object["metadata"])["uid"] = uid
		consumers = append(consumers, deploymentplan.Definition{Object: object, Phase: resource.Phase})
	}
	if len(consumers) != 2 {
		return empty, "", bad
	}
	if err := applyEnvironmentCreateStage(ctx, cfg, store, plan, e, stagingTransportReloadStage, consumers, os.Stdout); err != nil {
		return empty, "", err
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.temporary-tls original-serving-keys-preserved")
	return renewed["management"], filepath.Join(store.Root, caPath), nil
}
