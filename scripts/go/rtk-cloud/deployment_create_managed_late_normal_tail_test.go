package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func normalTailAuthorityFixture(t *testing.T) (*createManagedLateNormalTail, createFoundationRecoveryArtifact, time.Time, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-10 * time.Minute)
	sha := strings.Repeat("a", 64)
	n := &createManagedLateNormalTail{Schema: managedLateNormalTailSchema, SourceSHA: strings.Repeat("b", 64), OwnerSHA: strings.Repeat("c", 64), OriginalAuthoritySHA: strings.Repeat("d", 64), CompletedProofSHA: strings.Repeat("e", 64), PendingSHA: strings.Repeat("f", 64), IdentityPlanSHA: strings.Repeat("1", 64), CredentialMode: "non-expiring-kubernetes-token", CredentialSHA: strings.Repeat("2", 64), Approval: &createManagedLateNormalTailApproval{ParentSHA: sha, ProposalSHA: strings.Repeat("3", 64), ApprovalSHA: strings.Repeat("4", 64), ApprovedAt: started.Add(-time.Minute), FirstCallerLimit: managedLateOverallLimit}}
	n.CurrentSHA = n.SourceSHA
	n.KubeconfigSHA = strings.Repeat("9", 64)
	n.NativeEvidence = map[string]string{}
	for _, key := range []string{"owners", "all-volumes", "clients", "host-servers", "api-servers", "ca-registry", "current-crls", "sealed-session", "revocation", "service-crl", "service-crl-acks", "copies", "key", "csr"} {
		n.NativeEvidence[key] = sha
	}
	n.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(n)
	p := createFoundationRecoveryPlan{Schema: managedLateNormalTailSchema, ParentSHA: sha, ParentOwnerSHA: n.OwnerSHA, ParentJournalSHA: n.SourceSHA, CheckerSHA: strings.Repeat("5", 64), KubeconfigSHA: n.KubeconfigSHA, Materials: map[string]string{managedLateNormalTailMaterial + "source": managedLateNormalTailDigest(n), managedLateNormalTailMaterial + "policy": managedLateNormalTailPolicySHA()}}
	n.Review = deploymentplan.Digest(p)
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: p, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute), Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: n.Review}}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: n.Review})
	}
	return n, a, started, now
}
func TestManagedLateNormalTailAuthorityKeepsOnceFirstCaller(t *testing.T) {
	n, a, started, now := normalTailAuthorityFixture(t)
	rawSHA := strings.Repeat("9", 64)
	phase, err := newManagedLateNormalTailAuthority(n, a, rawSHA, started, now)
	if err != nil || phase == nil || !phase.StartedAt.Equal(started) || !phase.Deadline.Equal(started.Add(90*time.Minute)) || !phase.AdmittedAt.Equal(now) {
		t.Fatal("fixed first90 scope refused", err)
	}
	for _, mode := range []string{"deadline-reset", "start-reset", "credential-hash", "credential-expiry", "parent", "proof", "identity", "pending"} {
		t.Run(mode, func(t *testing.T) {
			copy := *phase
			switch mode {
			case "deadline-reset":
				copy.Deadline = copy.Deadline.Add(time.Minute)
			case "start-reset":
				copy.StartedAt = now
			case "credential-hash":
				copy.CredentialSHA = strings.Repeat("8", 64)
			case "credential-expiry":
				copy.CredentialExpires = now
			case "parent":
				copy.ParentSHA = strings.Repeat("8", 64)
			case "proof":
				copy.CompletedProofSHA = strings.Repeat("8", 64)
			case "identity":
				copy.IdentityPlanSHA = strings.Repeat("8", 64)
			case "pending":
				copy.PendingSHA = strings.Repeat("8", 64)
			}
			if managedLateNormalTailAuthorityMatches(n, &copy) {
				t.Fatal("changed first authority accepted")
			}
		})
	}
}
func TestManagedLateNormalTailAuthorityRequiresFreshSixAndCredentialReserve(t *testing.T) {
	for _, mode := range []string{"expired-go", "future-go", "wrong-window", "gate-fail", "gate-not-required", "gate-proof", "missing-gate", "source", "old-schema", "raw-hash", "first-reserve", "finite-short-credential", "unknown-credential-mode", "credential-config-drift"} {
		t.Run(mode, func(t *testing.T) {
			n, a, started, now := normalTailAuthorityFixture(t)
			rawSHA := strings.Repeat("9", 64)
			switch mode {
			case "expired-go":
				now = a.ValidUntil
			case "future-go":
				a.QualifiedAt = now.Add(time.Second)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			case "wrong-window":
				a.ValidUntil = a.ValidUntil.Add(time.Minute)
			case "gate-fail":
				a.Qualification.Gates[0].Status = "BLOCKED"
			case "gate-not-required":
				a.Qualification.Gates[0].Required = false
			case "gate-proof":
				a.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("8", 64)
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "source":
				a.Plan.ParentJournalSHA = strings.Repeat("8", 64)
			case "old-schema":
				a.Plan.Schema = managedLateExpiredRenewalSchema
			case "raw-hash":
				rawSHA = ""
			case "first-reserve":
				started = now.Add(-76 * time.Minute)
			case "finite-short-credential":
				n.CredentialMode = "expiring-kubernetes-token"
				n.CredentialExpires = started.Add(94 * time.Minute)
				n.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(n)
			case "unknown-credential-mode":
				n.CredentialMode = "arbitrary"
			case "credential-config-drift":
				a.Plan.KubeconfigSHA = strings.Repeat("8", 64)
			}
			if _, err := newManagedLateNormalTailAuthority(n, a, rawSHA, started, now); err == nil {
				t.Fatal("unqualified normal authority accepted")
			}
		})
	}
}
func TestManagedLateNormalTailPairNeverResetsOrRepairsOrphan(t *testing.T) {
	for _, mode := range []string{"first-pair", "orphan-primary", "orphan-witness", "different", "unsafe-mode", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store, parent, _, _ := terminalWitnessFixture(t)
			primary := filepath.Join(store.Root, managedLateNormalTailPath(parent, false))
			witness := filepath.Join(store.Root, managedLateNormalTailPath(parent, true))
			n, a, started, now := normalTailAuthorityFixture(t)
			phase, err := newManagedLateNormalTailAuthority(n, a, strings.Repeat("9", 64), started, now)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(phase)
			if err := writeManagedLateNormalTailPair(store, parent, raw); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "orphan-primary":
				err = os.Remove(primary)
			case "orphan-witness":
				err = os.Remove(witness)
			case "different":
				err = os.WriteFile(witness, []byte(`{"reset":true}`), 0600)
			case "unsafe-mode":
				err = os.Chmod(witness, 0644)
			case "symlink":
				err = os.Remove(witness)
				if err == nil {
					err = os.Symlink(primary, witness)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			observed, readErr := readManagedLateNormalTailPair(store, parent)
			if mode == "first-pair" {
				if readErr != nil || !bytes.Equal(observed, raw) {
					t.Fatal("first pair changed", readErr)
				}
			} else if readErr == nil {
				t.Fatal("partial/foreign/unsafe pair accepted")
			}
			if writeManagedLateNormalTailPair(store, parent, raw) == nil {
				t.Fatal("first admission reset or repaired")
			}
		})
	}
}

func TestManagedLateNormalTailAdmissionInvalidatesHistoricalScopeBeforePendingResume(t *testing.T) {
	cfg, p, release, store, e, _, _ := createPendingIngressFixture(t)
	before := deploymentplan.Digest(e.Journal)
	if err := ensurePrivateDirectory(filepath.Dir(filepath.Join(store.Root, managedLateNormalTailPath(e.Journal.PlanSHA256, false)))); err != nil {
		t.Fatal(err)
	}
	scope := &managedLateSourceReuse{values: map[string]json.RawMessage{"pure-ancestry": json.RawMessage(`{"accepted":true}`)}}
	e.lateSourceReuse = scope
	view := *e
	if !managedLateSourceReuseActive(e) || !managedLateSourceReuseActive(&view) {
		t.Fatal("isolated shared historical capability missing")
	}
	now := time.Now().UTC()
	phase := &createManagedLateNormalTailAuthority{Schema: managedLateNormalTailSchema, ParentSHA: e.Journal.PlanSHA256, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), StartedAt: now, AdmittedAt: now, Deadline: now.Add(managedLateOverallLimit)}
	if err := publishManagedLateNormalTailAuthority(t.Context(), e, phase); err != nil {
		t.Fatal(err)
	}
	if managedLateSourceReuseActive(e) || managedLateSourceReuseActive(&view) || scope.values != nil {
		t.Fatal("local grant write retained pre-write immutable authority")
	}
	raw, err := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	if err != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(phase)) {
		t.Fatal("paired first authority not retained", err)
	}
	if _, _, err := loadEnvironmentCreatePendingIngressAdmission(t.Context(), cfg, p, release, store, e); err != nil {
		t.Fatal("grant publication changed original pending ingress material/UID proof", err)
	}
	if before != deploymentplan.Digest(e.Journal) {
		t.Fatal("local authority publication rewrote pending432 or accepted433")
	}
}

func TestManagedLateNormalTailFiniteCurrentCredentialCoversWholeFirstClock(t *testing.T) {
	for _, mode := range []string{"exact-five-minute-reserve", "one-nanosecond-short", "metadata-proof-drift"} {
		t.Run(mode, func(t *testing.T) {
			n, a, started, now := normalTailAuthorityFixture(t)
			n.CredentialMode = "kubernetes-client-certificate"
			n.CredentialExpires = started.Add(managedLateOverallLimit + managedLateExpiryReserve)
			if mode == "one-nanosecond-short" {
				n.CredentialExpires = n.CredentialExpires.Add(-time.Nanosecond)
			}
			n.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(n)
			if mode == "metadata-proof-drift" {
				n.NativeEvidence["current-credential"] = strings.Repeat("7", 64)
			}
			a.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
			n.Review = deploymentplan.Digest(a.Plan)
			a.Qualification.PlanSHA256 = n.Review
			for i := range a.Qualification.Gates {
				a.Qualification.Gates[i].EvidenceSHA256 = n.Review
			}
			phase, err := newManagedLateNormalTailAuthority(n, a, strings.Repeat("9", 64), started, now)
			if mode == "exact-five-minute-reserve" {
				if err != nil || phase == nil || !phase.Deadline.Equal(started.Add(managedLateOverallLimit)) {
					t.Fatal("exact current-credential reserve refused", err)
				}
			} else if err == nil {
				t.Fatal("short or mismatched current credential authorized full90")
			}
		})
	}
}

func TestManagedLateNormalTailRequalificationUsesOriginalPairClock(t *testing.T) {
	store, parent, _, _ := terminalWitnessFixture(t)
	n, artifact, started, now := normalTailAuthorityFixture(t)
	qraw := mustProviderRecoveryJSON(artifact)
	first, err := newManagedLateNormalTailAuthority(n, artifact, environmentPreflightBytesSHA256(qraw), started, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"qualification.json": qraw, managedLateNormalTailFilename: mustProviderRecoveryJSON(n)} {
		if err := store.write(filepath.Join("deployment/create/openbao-recovery", n.Review, name), raw, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeManagedLateNormalTailPair(store, parent, mustProviderRecoveryJSON(first)); err != nil {
		t.Fatal(err)
	}
	current := *n
	current.CurrentSHA = strings.Repeat("7", 64)
	current.Review = strings.Repeat("8", 64)
	current.NativeEvidence = map[string]string{"fresh-current-proof": strings.Repeat("9", 64)}
	observed, err := readManagedLateNormalTailCurrentAuthority(store, &current)
	if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(first) {
		t.Fatal("current checkpoint requalification changed the original clock/raw GO", err)
	}
	for _, mode := range []string{"source", "pending", "approval", "credential", "raw-kubeconfig"} {
		t.Run(mode, func(t *testing.T) {
			changed := current
			switch mode {
			case "source":
				changed.SourceSHA = strings.Repeat("6", 64)
			case "pending":
				changed.PendingSHA = strings.Repeat("6", 64)
			case "approval":
				approval := *current.Approval
				approval.ApprovalSHA = strings.Repeat("6", 64)
				changed.Approval = &approval
			case "credential":
				changed.CredentialSHA = strings.Repeat("6", 64)
			case "raw-kubeconfig":
				changed.KubeconfigSHA = strings.Repeat("6", 64)
			}
			if _, err := readManagedLateNormalTailCurrentAuthority(store, &changed); err == nil {
				t.Fatal("current checkpoint rewrote immutable original authority")
			}
		})
	}
}

func TestManagedLateNormalTailCheckpointPreservesOriginalPendingSource(t *testing.T) {
	_, _, _, _, e, _, _ := createCompletedIngressFixture(t)
	j := e.Journal
	j.Entries = append([]environmentCreateJournalEntry(nil), e.Journal.Entries...)
	name := "retire-pki-bootstrap-custody-" + j.PlanSHA256[:12]
	for i, key := range []string{"kubernetes/account-manager-registration", name, "kubernetes/" + name} {
		j.Entries[428+i].Key = key
		j.Entries[428+i].IntentSHA256 = deploymentplan.Digest(key)
	}
	j.KubernetesLeases = []deploymentplan.LeaseOwner{{UID: "11111111-1111-4111-8111-111111111111"}, {UID: "22222222-2222-4222-8222-222222222222"}}
	before := deploymentplan.Digest(j)
	original := managedLateNormalTailOriginalSource(j)
	if !managedLateNormalTailControllerSelected(j) || len(original.Entries) != 433 || len(original.KubernetesLeases) != 1 || original.Entries[431].State != "pending" || !original.Entries[431].CompletedAt.IsZero() || len(original.Entries[431].Receipt) != 0 || original.Entries[431].StartedAt != j.Entries[431].StartedAt || original.Entries[431].IntentSHA256 != j.Entries[431].IntentSHA256 {
		t.Fatal("accepted controller cannot preserve exact original pending source")
	}
	pending := j
	pending.Entries = append([]environmentCreateJournalEntry(nil), j.Entries...)
	pending.Entries[431] = original.Entries[431]
	if !managedLateNormalTailControllerSelected(pending) || deploymentplan.Digest(managedLateNormalTailOriginalSource(pending)) != deploymentplan.Digest(original) {
		t.Fatal("accepted controller with pending wrapper changes approved source")
	}
	if before != deploymentplan.Digest(j) {
		t.Fatal("pure source normalization changed completed source")
	}
	for _, mode := range []string{"foreign-child", "pending-child", "extra-child", "partial-wrapper", "wrapper-before-child", "completed433"} {
		t.Run(mode, func(t *testing.T) {
			changed := j
			changed.Entries = append([]environmentCreateJournalEntry(nil), j.Entries...)
			switch mode {
			case "foreign-child":
				changed.Entries[433].Key = "kubernetes/foreign"
			case "pending-child":
				changed.Entries[433].State = "pending"
			case "extra-child":
				changed.Entries = append(changed.Entries, changed.Entries[433])
			case "partial-wrapper":
				changed.Entries[431].State = "pending"
			case "wrapper-before-child":
				changed.Entries[431].CompletedAt = changed.Entries[433].StartedAt.Add(-time.Second)
			case "completed433":
				changed.Entries = changed.Entries[:433]
			}
			if managedLateNormalTailSelected(changed) {
				t.Fatal("unknown or incomplete checkpoint selected")
			}
		})
	}
}

func TestManagedLateNormalTailArchivedTombstonePreservesNativeBytes(t *testing.T) {
	for _, mode := range []string{"exact-native-newline", "no-native-newline", "changed-native", "changed-uids", "changed-tombstone", "unknown-field", "missing"} {
		t.Run(mode, func(t *testing.T) {
			store, parent, _, _ := terminalWitnessFixture(t)
			sha := strings.Repeat("b", 64)
			native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: "11111111-1111-4111-8111-111111111111", LeafSHA256: sha, CRLSHA256: sha, CleanupSHA256: sha, FinalizedAt: time.Now().UTC()}
			tombstone := managedLateCustodyTombstone{Schema: "rtk-cloud.service-bootstrap-custody-tombstone/v1", Finalization: native, Root: sha, Files: map[string]string{"key": sha, "csr": sha, "certificate": sha}}
			raw := append(mustProviderRecoveryJSON(tombstone), '\n')
			uids := map[string]string{"video": "22222222-2222-4222-8222-222222222222"}
			r := &createServiceSessionRecovery{normalTail: &createManagedLateNormalTail{}, Ceremony: environmentCreateServiceCeremony{CredentialUIDs: uids}, ManagedLateRemaining: &createManagedLateRemaining{ParentSHA: parent, PostNativeCleanup: &createManagedLatePostNativeCleanup{Native: native, TombstoneSHA: environmentPreflightBytesSHA256(raw)}}}
			archived := deploymentplan.Object{"native_finalization": native, "custody_tombstone": tombstone, "credential_uids": uids}
			switch mode {
			case "no-native-newline":
				r.ManagedLateRemaining.PostNativeCleanup.TombstoneSHA = environmentPreflightBytesSHA256(bytes.TrimSuffix(raw, []byte{'\n'}))
			case "changed-native":
				changed := native
				changed.CRLSHA256 = strings.Repeat("c", 64)
				archived["native_finalization"] = changed
			case "changed-uids":
				archived["credential_uids"] = map[string]string{"video": "33333333-3333-4333-8333-333333333333"}
			case "changed-tombstone":
				tombstone.Root = strings.Repeat("c", 64)
				archived["custody_tombstone"] = tombstone
			case "unknown-field":
				archived["new_field"] = true
			}
			if mode != "missing" {
				if err := store.write(filepath.Join("deployment/create", parent, "bootstrap/service/finalized.json"), mustProviderRecoveryJSON(archived), false); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := managedLateNormalTailArchivedTombstone(store, r)
			if mode == "exact-native-newline" {
				if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(tombstone) {
					t.Fatal("exact original native byte form not recovered", err)
				}
			} else if err == nil {
				t.Fatal("changed original native marker was accepted")
			}
		})
	}
}

func resealNormalTailFixture(n *createManagedLateNormalTail, a *createFoundationRecoveryArtifact) {
	a.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	n.Review = deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256 = n.Review
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = n.Review
	}
}

func TestManagedLateNormalTailAuthorityKeepsRawAndFrozenKubeconfigPinsDistinct(t *testing.T) {
	config := frozenKubernetesFixture(t)
	user := config["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
	user["token"] = managedLateResidualJWT(`{"alg":"RS256"}`, `{"sub":"isolated-normal-tail"}`)
	encoded, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("# original selected kubeconfig formatting\n"), encoded...)
	n, artifact, started, now := normalTailAuthorityFixture(t)
	backend, err := newEnvironmentKubernetesBackendAt(string(raw), "dev", now)
	if err != nil {
		t.Fatal(err)
	}
	metadata := &createManagedLateResidualCleanup{}
	if err := bindManagedLateResidualCredential(metadata, backend, now); err != nil {
		t.Fatal(err)
	}
	// Parsing frozen metadata is pure here; production captures it only after
	// these exact backend bytes authenticate the successful fresh Inventory.
	n.KubeconfigSHA = environmentPreflightBytesSHA256(raw)
	n.CredentialMode, n.CredentialSHA, n.CredentialExpires = metadata.CredentialMode, metadata.CredentialSHA, metadata.CredentialExpires
	n.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(n)
	artifact.Plan.KubeconfigSHA = n.KubeconfigSHA
	resealNormalTailFixture(n, &artifact)
	if n.KubeconfigSHA == n.CredentialSHA || bytes.Equal(raw, backend.KubeconfigContent) || n.CredentialSHA != environmentPreflightBytesSHA256(backend.KubeconfigContent) {
		t.Fatal("fixture failed to exercise original versus selected normalized bytes")
	}
	phase, err := newManagedLateNormalTailAuthority(n, artifact, strings.Repeat("8", 64), started, now)
	if err != nil || phase == nil || !managedLateNormalTailAuthorityMatches(n, phase) {
		t.Fatal("distinct authentic raw and frozen pins rejected", err)
	}
	for _, mode := range []string{"raw-plan", "raw-descriptor", "normalized-credential", "mode", "metadata-evidence", "missing-raw", "conflated-pins"} {
		t.Run(mode, func(t *testing.T) {
			changed := *n
			changed.NativeEvidence = map[string]string{}
			for key, value := range n.NativeEvidence {
				changed.NativeEvidence[key] = value
			}
			a := artifact
			a.Plan.Materials = map[string]string{}
			for key, value := range artifact.Plan.Materials {
				a.Plan.Materials[key] = value
			}
			a.Qualification.Gates = append([]deploymentplan.GateResult(nil), artifact.Qualification.Gates...)
			switch mode {
			case "raw-plan":
				a.Plan.KubeconfigSHA = strings.Repeat("7", 64)
			case "raw-descriptor":
				changed.KubeconfigSHA = strings.Repeat("7", 64)
			case "normalized-credential":
				changed.CredentialSHA = strings.Repeat("7", 64)
			case "mode":
				changed.CredentialMode = "kubernetes-client-certificate"
			case "metadata-evidence":
				changed.NativeEvidence["current-credential"] = strings.Repeat("7", 64)
			case "missing-raw":
				changed.KubeconfigSHA, a.Plan.KubeconfigSHA = "", ""
			case "conflated-pins":
				changed.KubeconfigSHA = changed.CredentialSHA
			}
			resealNormalTailFixture(&changed, &a)
			if _, err := newManagedLateNormalTailAuthority(&changed, a, strings.Repeat("8", 64), started, now); err == nil {
				t.Fatal("independently resealed raw or normalized credential drift accepted")
			}
		})
	}
}

func TestManagedLateNormalTailAuthoritySupportsActualDistinctWirePins(t *testing.T) {
	n, artifact, started, now := normalTailAuthorityFixture(t)
	// Public digests from the genuine failed f203 attempt. They identify two
	// byte representations, not different Kubernetes credentials.
	n.KubeconfigSHA = "d8ee172544ba20f7dd6668f8c77a763ae0b95b48d44ebc8923fa5a5b55979f0b"
	n.CredentialSHA = "03e43dec782b71ee0d40a0c7b28117ac93b373fa9569e45e90292fa1723cf398"
	n.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(n)
	if n.NativeEvidence["current-credential"] != "61f644eea54c57a2511f8a2f90ead18e0647c28ff38c0d654352259b95232aa7" {
		t.Fatal("actual normalized mode/hash/expiry metadata wire changed")
	}
	artifact.Plan.KubeconfigSHA = n.KubeconfigSHA
	resealNormalTailFixture(n, &artifact)
	phase, err := newManagedLateNormalTailAuthority(n, artifact, strings.Repeat("8", 64), started, now)
	if err != nil || phase == nil || phase.CredentialSHA != n.NativeEvidence["current-credential"] || !phase.Deadline.Equal(started.Add(90*time.Minute)) {
		t.Fatal("actual distinct wire digests cannot enter unchanged fixed90", err)
	}
}
