package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Candidate persistence has no authority to execute a plan or admit a source.
// These tests supply compiled models and verify their private serialization;
// the command tests separately exercise source validation and admission.
func recoveryCandidateWriterContract(t *testing.T, kind string) (secretStore, string, *createServiceSessionRecovery, map[string]any) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	compile := func(name string) *deploymentplan.Plan {
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("original configuration"), ReleaseSHA256: deploymentplan.Digest("original release"), BindingsSHA256: deploymentplan.Digest(name)}, []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"original": name}}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := compile("original-candidate"), compile("original-auxiliary")
	r := &createServiceSessionRecovery{Candidate: a}
	expected := map[string]any{"service-session-plan.json": a}
	switch kind {
	case "identity-lock":
		r.IdentityLockForward = true
		expected = map[string]any{"service-managed-core-plan.json": a}
	case "post-service":
		r.PostServiceResume = true
		expected = map[string]any{"mqtt-root-trust-plan.json": a}
	case "post-transport":
		r.PostServiceResume, r.PostTransportResume = true, true
		expected = map[string]any{"openbao-root-trust-plan.json": a}
	case "installation-renewal-ack-grants":
		r.InstallationCandidate, r.QuiesceCandidate, r.RenewalCandidate = b, a, b
		r.OpenBaoACK = &createOpenBaoACKRecovery{Candidate: b}
		r.GrantCandidate = a
		expected["current-installation-plan.json"], expected["current-owner-quiesce-plan.json"] = b, a
		expected["used-bootstrap-renewal-plan.json"], expected["openbao-host-ack-plan.json"] = b, b
		expected["openbao-host-ack-source.json"], expected["service-trust-grants-plan.json"] = r.OpenBaoACK, a
	case "managed-core":
		r.ManagedCorePartial = &createManagedCorePartialRecovery{OriginalRepairReview: deploymentplan.Digest("original repair"), CertIssuerPrepare: &createCertIssuerPrepareImageRecovery{}}
		expected = map[string]any{"managed-complete-issuer-forward-plan.json": a, managedCoreCertIssuerPrepareFilename: r.ManagedCorePartial.CertIssuerPrepare}
	case "managed-late":
		r.ManagedLateSession = &createManagedLateSessionRecovery{OwnerCandidate: a, CustodyCandidate: b, LogStopCandidate: a, LogHelperCandidate: b, LogRetireCandidate: a, LogRestoreCandidate: b}
		expected = map[string]any{"managed-late-session-source.json": r.ManagedLateSession, managedLateSessionOwnerPlanFilename: a, "managed-late-session-custody-plan.json": b, "managed-late-log-stop-plan.json": a, "managed-late-log-helper-plan.json": b, "managed-late-log-retire-plan.json": a, "managed-late-log-restore-plan.json": b}
	case "seed-forward":
		r.ManagedLateSeedForward = &createManagedLateSeedForward{Candidate: b}
		expected = map[string]any{managedLateSeedForwardSourceFilename: r.ManagedLateSeedForward, managedLateSeedForwardPlanFilename: b}
	case "post-mint":
		r.ManagedLatePostMint = &createManagedLatePostMint{Candidate: b}
		expected = map[string]any{managedLatePostMintSourceFilename: r.ManagedLatePostMint, managedLatePostMintPlanFilename: b}
	case "expired-renewal":
		r.ManagedLateExpiredRenewal = &createManagedLateExpiredRenewal{Candidate: b}
		expected[managedLateExpiredRenewalSourceFilename], expected[managedLateExpiredRenewalPlanFilename] = r.ManagedLateExpiredRenewal, b
	}
	return store, deploymentplan.Digest("isolated qualified review " + kind), r, expected
}

func TestRecoveryCandidateWriterPreservesEveryCompiledRecoveryVariant(t *testing.T) {
	for _, kind := range []string{"service", "identity-lock", "post-service", "post-transport", "installation-renewal-ack-grants", "managed-core", "managed-late", "seed-forward", "post-mint", "expired-renewal"} {
		t.Run(kind, func(t *testing.T) {
			store, review, r, expected := recoveryCandidateWriterContract(t, kind)
			if err := writeCreateServiceRecoveryCandidates(store, review, r, true); err != nil {
				t.Fatal("candidate model could not be saved", err)
			}
			base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != len(expected) {
				t.Fatal("candidate files differ from the selected recovery variant", err, len(entries), len(expected))
			}
			for name, value := range expected {
				path := filepath.Join(base, name)
				raw, err := readBoundedEnvironmentFile(path, true)
				info, statErr := os.Stat(path)
				if err != nil || statErr != nil || info.Mode().Perm() != 0600 || !bytes.Equal(raw, mustProviderRecoveryJSON(value)) {
					t.Fatal("saved candidate changed its frozen contents or private permissions", name, err, statErr)
				}
			}
			if r.ManagedCorePartial != nil {
				if r.Review != r.ManagedCorePartial.OriginalRepairReview || r.ManagedCorePartial.ForwardReview != review {
					t.Fatal("forward save replaced original repair provenance")
				}
			} else if r.ManagedLateSeedForward == nil && r.ManagedLatePostMint == nil && r.Review != review {
				t.Fatal("candidate omitted exact review binding")
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if err := writeCreateServiceRecoveryCandidates(store, review, r, true); err != nil || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("same candidate replay changed immutable evidence", err)
			}
		})
	}
}

func TestRecoveryCandidateWriterDoesNotReplaceConflictingOriginalArtifacts(t *testing.T) {
	for _, kind := range []string{"installation-renewal-ack-grants", "managed-core", "managed-late", "seed-forward", "post-mint", "expired-renewal"} {
		_, _, _, names := recoveryCandidateWriterContract(t, kind)
		for filename := range names {
			t.Run(kind+"/"+filename, func(t *testing.T) {
				store, review, r, _ := recoveryCandidateWriterContract(t, kind)
				relative := filepath.Join("deployment/create/openbao-recovery", review, filename)
				original := []byte("preserved unrelated original artifact")
				if err := store.write(relative, original, false); err != nil {
					t.Fatal(err)
				}
				if err := writeCreateServiceRecoveryCandidates(store, review, r, true); err == nil {
					t.Fatal("conflicting original artifact was accepted")
				}
				raw, err := os.ReadFile(filepath.Join(store.Root, relative))
				if err != nil || !bytes.Equal(raw, original) {
					t.Fatal("candidate save replaced an original artifact", err)
				}
				if _, err := os.Stat(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "qualification.json")); !os.IsNotExist(err) {
					t.Fatal("candidate serialization created execution admission")
				}
			})
		}
	}
}

func TestRecoveryCandidateWriterRefusesMissingExpiredPlanBeforeSaving(t *testing.T) {
	store, review, r, _ := recoveryCandidateWriterContract(t, "expired-renewal")
	r.ManagedLateExpiredRenewal.Candidate = nil
	before := createBootstrapStateSnapshot(t, store.Root)
	if err := writeCreateServiceRecoveryCandidates(store, review, r, true); err == nil {
		t.Fatal("missing compiled renewal plan was serialized as a null candidate")
	}
	if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
		t.Fatal("incomplete candidate changed private state")
	}
}

func TestManagedLateCandidateWriterPublishesDescriptorAsCommitMarker(t *testing.T) {
	store, review, r, _ := recoveryCandidateWriterContract(t, "managed-late")
	conflict := filepath.Join("deployment/create/openbao-recovery", review, "managed-late-log-restore-plan.json")
	if err := store.write(conflict, []byte(`{"foreign":true}`), false); err != nil {
		t.Fatal(err)
	}
	if err := writeCreateServiceRecoveryCandidates(store, review, r, true); err == nil {
		t.Fatal("conflicting preview unexpectedly committed the managed-late archive")
	}
	descriptor := filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "managed-late-session-source.json")
	if _, err := os.Lstat(descriptor); !os.IsNotExist(err) {
		t.Fatal("managed-late descriptor was published before every preview became durable", err)
	}
}
