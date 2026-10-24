package main

import (
	"context"
	"crypto/ecdsa"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceRootCRLFixture(t *testing.T) (createDeviceRootCRLPlan, *ecdsa.PrivateKey, time.Time) {
	t.Helper()
	root, key, snapshot, now := rootCRLFixture(t)
	root.ID, root.Domain = createDeviceRootCRLID, "device"
	root.Reference = "pki-issuers/device/" + root.ID + "/v1"
	root.Document = deploymentplan.Object{"signer_provider": "openbao"}
	snapshot.Prior = signedRootCRLFixture(t, root, key, createRootCRLRequest{root.ID, root.RootFingerprint, "3", now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), []createRootCRLRevocation{{"63", now.Add(-49 * time.Hour), 1}}})
	p := createDeviceRootCRLPlan{
		Schema: createDeviceRootCRLSchema, Parent: strings.Repeat("1", 64), SourceSHA: strings.Repeat("2", 64), OwnerSHA: strings.Repeat("3", 64), SourcePins: strings.Repeat("4", 64),
		CheckerSHA: strings.Repeat("5", 64), ConfigurationSHA: strings.Repeat("6", 64), KubeconfigSHA: strings.Repeat("7", 64), ProviderSHA: strings.Repeat("8", 64), ProviderReview: strings.Repeat("9", 64),
		Configuration: deploymentConfig{Values: map[string]string{"CLOUD_STACK_NAME": "isolated-device"}}, Root: root, Snapshot: snapshot,
		Consumers: []createRootCRLStateTarget{
			{ID: "Deployment/isolated-device-video-cloud/video-cloud-api-pki", UID: "11111111-1111-4111-8111-111111111111", Desired: deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"role": "api"}}, PodUID: "22222222-2222-4222-8222-222222222222", ClaimID: "PersistentVolumeClaim/isolated-device-video-cloud/api-trust"},
			{ID: "Deployment/isolated-device-video-cloud/mqtt-pki", UID: "33333333-3333-4333-8333-333333333333", Desired: deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"role": "broker"}}, PodUID: "44444444-4444-4444-8444-444444444444", ClaimID: "PersistentVolumeClaim/isolated-device-video-cloud/broker-trust"},
		}, Volumes: map[string]environmentCreateVolumeOwner{},
	}
	return p, key, now
}

func deviceRootCRLArtifactFixture(p createDeviceRootCRLPlan, now time.Time) createDeviceRootCRLArtifact {
	review := deploymentplan.Digest(p)
	a := createDeviceRootCRLArtifact{Schema: createDeviceRootCRLSchema + "/artifact", Plan: p, QualifiedAt: now.Add(-5 * time.Minute), ValidUntil: now.Add(10 * time.Minute), Qualification: deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}}
	for _, id := range createDeviceRootCRLGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	return a
}

func deviceRootCRLJournalFixture(t *testing.T) (createDeviceRootCRLArtifact, createDeviceRootCRLJournal, time.Time) {
	t.Helper()
	p, key, now := deviceRootCRLFixture(t)
	a := deviceRootCRLArtifactFixture(p, now)
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{p.Root.ID, p.Root.RootFingerprint, "4", now.Add(-3 * time.Minute), now.Add(24 * time.Hour), entries})
	j := createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: deploymentplan.Digest(p), SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "completed", StartedAt: now.Add(-4 * time.Minute), Deadline: now.Add(26 * time.Minute), RotationStartedAt: now.Add(-3 * time.Minute), RotatedAt: now.Add(-2 * time.Minute), ImportedAt: now.Add(-time.Minute), CompletedAt: now, Lease: deploymentplan.LeaseOwner{ResourceID: "Lease/isolated-device-video-cloud/rtk-deployment-execution", UID: "55555555-5555-4555-8555-555555555555", Holder: "isolated-qualified-repair"}, LeaseReleased: true, Signed: signed}
	j.Evidence = deviceRootCRLEvidenceFixture(p, 2)
	return a, j, now
}

func deviceRootCRLEvidenceFixture(p createDeviceRootCRLPlan, count int) map[string]string {
	evidence := map[string]string{}
	for _, target := range p.Consumers[:count] {
		evidence["consumer-owner-"+target.ID] = deploymentplan.Digest(target.Desired)
		evidence["consumer-Pod-"+target.ID] = deploymentplan.Digest(target.PodUID)
		evidence["consumer-volume-"+target.ClaimID] = deploymentplan.Digest(p.Volumes[target.ClaimID])
		evidence["cache-"+target.ID] = strings.Repeat("a", 64)
		evidence["native-ACKs-"+p.Root.ID] = strings.Repeat("b", 64)
	}
	return evidence
}

func TestDeviceRootCRLOriginalOnlineKeyPreservesCompleteRevocationUnion(t *testing.T) {
	p, key, now := deviceRootCRLFixture(t)
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil || len(entries) != 2 || entries[0].Serial != "63" || entries[0].Reason != 1 || entries[1].Serial != "64" {
		t.Fatalf("original signed history and current revocation must both survive: %v", err)
	}
	if _, err := validateDeviceRootCRL(p.Root, p.Snapshot.Prior, "3", true, now); err == nil {
		t.Fatal("expired prior became current trust")
	}
	request := createRootCRLRequest{p.Root.ID, p.Root.RootFingerprint, "4", now, now.Add(24 * time.Hour), entries}
	signed := signedRootCRLFixture(t, p.Root, key, request)
	accepted, err := validateDeviceRootCRLSuccessor(p, []byte(signed.PEM), now, now)
	if err != nil || deploymentplan.Digest(accepted) != deploymentplan.Digest(signed) {
		t.Fatalf("same original key and exact union rejected: %v", err)
	}
	for _, mode := range []string{"missing-prior", "missing-current", "extra-revocation", "changed-reason", "rollback-number", "stale-successor", "predates-intent", "foreign-key", "foreign-root", "foreign-provider", "extra-PEM"} {
		t.Run(mode, func(t *testing.T) {
			local := p
			candidate := request
			candidate.Entries = append([]createRootCRLRevocation(nil), request.Entries...)
			signer, authority := key, p.Root
			switch mode {
			case "missing-prior":
				candidate.Entries = candidate.Entries[1:]
			case "missing-current":
				candidate.Entries = candidate.Entries[:1]
			case "extra-revocation":
				candidate.Entries = append(candidate.Entries, createRootCRLRevocation{"65", now.Add(-time.Hour), 0})
			case "changed-reason":
				candidate.Entries[0].Reason = 2
			case "rollback-number":
				candidate.Number = "3"
			case "stale-successor":
				candidate.NextUpdate = now.Add(44 * time.Minute)
			case "predates-intent":
				candidate.ThisUpdate = now.Add(-2 * time.Second)
			case "foreign-key":
				foreign, otherKey, _, _ := rootCRLFixture(t)
				authority, signer = foreign, otherKey
			case "foreign-root":
				local.Root.ID = "66666666-6666-4666-8666-666666666666"
			case "foreign-provider":
				local.Root.Document = deploymentplan.Object{"signer_provider": "offline"}
			}
			value := signedRootCRLFixture(t, authority, signer, candidate)
			raw := []byte(value.PEM)
			if mode == "extra-PEM" {
				raw = append(raw, []byte(value.PEM)...)
			}
			if _, err := validateDeviceRootCRLSuccessor(local, raw, now, now); err == nil {
				t.Fatal("changed authority, issuance sequence or revocation content accepted")
			}
		})
	}
}

func TestDeviceRootCRLRotationReadbackAcceptsOnlyCanonicalAlreadyIssuedSuccessor(t *testing.T) {
	p, key, now := deviceRootCRLFixture(t)
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	intent := createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: deploymentplan.Digest(p), SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "rotating", StartedAt: now.Add(-10 * time.Second), Deadline: now.Add(30*time.Minute - 10*time.Second), RotationStartedAt: now.Add(-5 * time.Second), LeaseReleased: true, Lease: deploymentplan.LeaseOwner{ResourceID: "Lease/isolated-device-video-cloud/rtk-deployment-execution", UID: "55555555-5555-4555-8555-555555555555", Holder: "original-qualified-rotation"}}
	request := createRootCRLRequest{p.Root.ID, p.Root.RootFingerprint, "5", now.Add(-2 * time.Second), now.Add(24 * time.Hour), entries}
	issued := signedRootCRLFixture(t, p.Root, key, request)
	if err := validateDeviceRootCRLRotationReadback(p, intent, issued, now); err != nil {
		t.Fatalf("single admitted rotation's genuine signed5 readback rejected: %v", err)
	}
	if err := validateDeviceRootCRLCurrent(p, issued, now); err != nil {
		t.Fatalf("signed5 was not validated as current trust: %v", err)
	}
	for _, mode := range []string{"wrong-review", "wrong-source", "wrong-owner", "wrong-stage", "completion-review", "completion-started", "unreleased-lease", "foreign-lease", "missing-intent", "future-intent", "reset-deadline", "already-signed", "already-imported", "arbitrary-evidence", "wrong-envelope-number", "wrong-envelope-digest", "wrong-envelope-issuer", "predates-intent", "expired-five", "lost-revocation", "still-three"} {
		t.Run(mode, func(t *testing.T) {
			original, provider := intent, issued
			candidate := request
			candidate.Entries = append([]createRootCRLRevocation(nil), entries...)
			resign := false
			switch mode {
			case "wrong-review":
				original.Review = strings.Repeat("f", 64)
			case "wrong-source":
				original.SourceSHA = strings.Repeat("f", 64)
			case "wrong-owner":
				original.OwnerSHA = strings.Repeat("f", 64)
			case "wrong-stage":
				original.Stage = "admitted"
			case "completion-review":
				original.CompletionReview = strings.Repeat("f", 64)
			case "completion-started":
				original.CompletionStartedAt = now
			case "unreleased-lease":
				original.LeaseReleased = false
			case "foreign-lease":
				original.Lease.ResourceID = "Lease/other/rtk-deployment-execution"
			case "missing-intent":
				original.RotationStartedAt = time.Time{}
			case "future-intent":
				original.RotationStartedAt = now.Add(time.Second)
			case "reset-deadline":
				original.Deadline = original.Deadline.Add(time.Minute)
			case "already-signed":
				original.Signed = issued
			case "already-imported":
				original.ImportedAt = now
			case "arbitrary-evidence":
				original.Evidence = map[string]string{"claimed": strings.Repeat("f", 64)}
			case "wrong-envelope-number":
				provider.Number = "4"
			case "wrong-envelope-digest":
				provider.Digest = strings.Repeat("f", 64)
			case "wrong-envelope-issuer":
				provider.IssuerID = "11111111-1111-4111-8111-111111111111"
			case "predates-intent":
				candidate.ThisUpdate, resign = original.RotationStartedAt.Add(-2*time.Second), true
			case "expired-five":
				candidate.NextUpdate, resign = now.Add(-time.Second), true
			case "lost-revocation":
				candidate.Entries, resign = candidate.Entries[1:], true
			case "still-three":
				provider = p.Snapshot.Prior
			}
			if resign {
				provider = signedRootCRLFixture(t, p.Root, key, candidate)
			}
			if err := validateDeviceRootCRLRotationReadback(p, original, provider, now); err == nil {
				t.Fatal("uncertain rotation accepted changed historical intent or unproved provider outcome")
			}
		})
	}
}

func TestDeviceRootCRLCompletedJournalPinsActualFiveWithoutRewritingOriginalPlan(t *testing.T) {
	p, key, now := deviceRootCRLFixture(t)
	a, j, _ := deviceRootCRLJournalFixture(t)
	// Use the same isolated authority and original admitted Plan for the signed
	// successor. Only actual outcome fields belong to the repair journal.
	a = deviceRootCRLArtifactFixture(p, now)
	j.Parent, j.Review, j.SourceSHA, j.OwnerSHA = p.Parent, deploymentplan.Digest(p), p.SourceSHA, p.OwnerSHA
	j.StartedAt, j.Deadline, j.RotationStartedAt = now.Add(-4*time.Minute), now.Add(26*time.Minute), now.Add(-3*time.Minute)
	j.RotatedAt, j.ImportedAt, j.CompletedAt = now.Add(-2*time.Minute), now.Add(-time.Minute), now
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	j.Signed = signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{p.Root.ID, p.Root.RootFingerprint, "5", j.RotationStartedAt, now.Add(24 * time.Hour), entries})
	j.Evidence = deviceRootCRLEvidenceFixture(p, 2)
	before := deploymentplan.Digest(a.Plan)
	if !deviceRootCRLJournalValid(a, j, now) || validateDeviceRootCRLCurrent(p, j.Signed, now) != nil || deploymentplan.Digest(a.Plan) != before {
		t.Fatal("genuine5 outcome lost strict current evidence or changed original qualification")
	}
	j.Signed.Number = "4"
	if deviceRootCRLJournalValid(a, j, now) {
		t.Fatal("current5 proof accepted a fabricated4 envelope")
	}
}

func TestDeviceRootCRLUnionRejectsConflictingOrUnprovenHistory(t *testing.T) {
	for _, mode := range []string{"changed-time", "changed-reason", "future-time", "noncanonical-serial", "remove-from-CRL", "wrong-digest", "wrong-issuer", "missing-CRL"} {
		t.Run(mode, func(t *testing.T) {
			p, _, now := deviceRootCRLFixture(t)
			switch mode {
			case "changed-time":
				p.Snapshot.Bindings = append(p.Snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-time.Hour), 0})
			case "changed-reason":
				p.Snapshot.Bindings = append(p.Snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-49 * time.Hour), 2})
			case "future-time":
				p.Snapshot.Bindings[0].RevokedAt = now.Add(time.Hour)
			case "noncanonical-serial":
				p.Snapshot.Bindings[0].Serial = "064"
			case "remove-from-CRL":
				p.Snapshot.Bindings[0].Reason = 8
			case "wrong-digest":
				p.Snapshot.Prior.Digest = strings.Repeat("f", 64)
			case "wrong-issuer":
				p.Snapshot.Prior.IssuerID = "66666666-6666-4666-8666-666666666666"
			case "missing-CRL":
				p.Snapshot.Prior.PEM = ""
			}
			if _, err := deviceRootCRLUnion(p.Root, p.Snapshot); err == nil {
				t.Fatal("conflicting or unproven prior accepted")
			}
		})
	}
}

func TestDeviceRootCRLJournalRequiresRealStagesAndExactConsumerProof(t *testing.T) {
	a, j, now := deviceRootCRLJournalFixture(t)
	if !deviceRootCRLJournalValid(a, j, now) {
		t.Fatal("genuine completed standalone repair rejected")
	}
	for _, mode := range []string{"wrong-review", "wrong-source", "wrong-owner", "unknown-stage", "late-admission", "reset-deadline", "future-rotation", "missing-import-time", "missing-completion-time", "unreleased-lease", "foreign-lease", "missing-evidence", "missing-API-cache", "foreign-owner", "foreign-Pod", "foreign-volume", "arbitrary-extra-proof", "invalid-ACK-hash", "signed-number", "signed-digest", "signed-issuer", "signed-time"} {
		t.Run(mode, func(t *testing.T) {
			candidate := j
			candidate.Evidence = map[string]string{}
			for k, v := range j.Evidence {
				candidate.Evidence[k] = v
			}
			switch mode {
			case "wrong-review":
				candidate.Review = strings.Repeat("f", 64)
			case "wrong-source":
				candidate.SourceSHA = strings.Repeat("f", 64)
			case "wrong-owner":
				candidate.OwnerSHA = strings.Repeat("f", 64)
			case "unknown-stage":
				candidate.Stage = "import-assumed"
			case "late-admission":
				candidate.StartedAt = a.ValidUntil
				candidate.Deadline = candidate.StartedAt.Add(30 * time.Minute)
			case "reset-deadline":
				candidate.Deadline = candidate.Deadline.Add(time.Minute)
			case "future-rotation":
				candidate.RotatedAt = now.Add(time.Second)
			case "missing-import-time":
				candidate.ImportedAt = time.Time{}
			case "missing-completion-time":
				candidate.CompletedAt = time.Time{}
			case "unreleased-lease":
				candidate.LeaseReleased = false
			case "foreign-lease":
				candidate.Lease.ResourceID = "Lease/other/rtk-deployment-execution"
			case "missing-evidence":
				candidate.Evidence = nil
			case "missing-API-cache":
				delete(candidate.Evidence, "cache-"+a.Plan.Consumers[0].ID)
			case "foreign-owner":
				candidate.Evidence["consumer-owner-"+a.Plan.Consumers[0].ID] = strings.Repeat("f", 64)
			case "foreign-Pod":
				candidate.Evidence["consumer-Pod-"+a.Plan.Consumers[0].ID] = strings.Repeat("f", 64)
			case "foreign-volume":
				candidate.Evidence["consumer-volume-"+a.Plan.Consumers[0].ClaimID] = strings.Repeat("f", 64)
			case "arbitrary-extra-proof":
				candidate.Evidence["unexpected-proof"] = strings.Repeat("f", 64)
			case "invalid-ACK-hash":
				candidate.Evidence["native-ACKs-"+a.Plan.Root.ID] = "not-a-digest"
			case "signed-number":
				candidate.Signed.Number = "5"
			case "signed-digest":
				candidate.Signed.Digest = strings.Repeat("f", 64)
			case "signed-issuer":
				candidate.Signed.IssuerID = "66666666-6666-4666-8666-666666666666"
			case "signed-time":
				candidate.Signed.NextUpdate = candidate.Signed.NextUpdate.Add(time.Minute)
			}
			if deviceRootCRLJournalValid(a, candidate, now.Add(20*time.Minute)) {
				t.Fatal("changed standalone state, chronology or genuine consumer evidence accepted")
			}
		})
	}
	// The GO authorizes admission, not the whole already-admitted lifecycle.
	if !deviceRootCRLJournalValid(a, j, a.ValidUntil.Add(time.Hour)) {
		t.Fatal("completed genuine journal incorrectly demanded the old GO remain unexpired")
	}
}

func TestDeviceRootCRLJournalStagesCannotSkipConsumerProof(t *testing.T) {
	a, completed, now := deviceRootCRLJournalFixture(t)
	for i, stage := range []string{"admitted", "rotating", "rotated", "imported", "api-verified", "consumers-verified", "completed"} {
		t.Run(stage, func(t *testing.T) {
			j := completed
			j.Stage = stage
			j.CompletedAt = time.Time{}
			j.LeaseReleased = false
			count := 0
			if i >= 4 {
				count = 1
			}
			if i >= 5 {
				count = 2
			}
			j.Evidence = deviceRootCRLEvidenceFixture(a.Plan, count)
			if i < 3 {
				j.ImportedAt = time.Time{}
			}
			if i < 2 {
				j.RotatedAt = time.Time{}
				j.Signed = createRootCRLRecord{}
			}
			if i < 1 {
				j.RotationStartedAt = time.Time{}
			}
			if i == 6 {
				j.CompletedAt, j.LeaseReleased = completed.CompletedAt, true
			}
			if !deviceRootCRLJournalValid(a, j, now) {
				t.Fatal("actual admitted partial stage rejected")
			}
			if count > 0 {
				j.Evidence = nil
				if deviceRootCRLJournalValid(a, j, now) {
					t.Fatal("partial stage could skip genuine consumer evidence")
				}
			}
		})
	}
}

func TestDeviceRootCRLQualificationPinsAllSixGatesAndUnchangedFifteenMinutes(t *testing.T) {
	p, _, now := deviceRootCRLFixture(t)
	for _, mode := range []string{"genuine", "NO-GO", "missing-gate", "optional-gate", "reordered-gate", "foreign-evidence", "changed-input", "longer-window"} {
		t.Run(mode, func(t *testing.T) {
			a := deviceRootCRLArtifactFixture(p, now)
			switch mode {
			case "NO-GO":
				a.Qualification.Decision = "NO-GO"
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "optional-gate":
				a.Qualification.Gates[0].Required = false
			case "reordered-gate":
				a.Qualification.Gates[0], a.Qualification.Gates[1] = a.Qualification.Gates[1], a.Qualification.Gates[0]
			case "foreign-evidence":
				a.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("f", 64)
			case "changed-input":
				a.Plan.ProviderSHA = strings.Repeat("f", 64)
			case "longer-window":
				a.ValidUntil = a.ValidUntil.Add(time.Second)
			}
			if deviceRootCRLQualificationValid(a) != (mode == "genuine") {
				t.Fatal("qualification did not bind exact six gates, immutable inputs and 15-minute admission")
			}
		})
	}
}

func TestDeviceRootCRLPriorAuthorityNeverAppliesWithoutExactScopedContext(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), context.WithValue(context.Background(), managedLateDeviceRootCRLObservationKey{}, managedLateDeviceRootCRLObservation{})} {
		for _, id := range []string{"service", "mqtt", "openbao_tls", "foreign-device-issuer"} {
			if _, handled, err := managedLateDeviceRootCRLPrior(ctx, secretStore{}, nil, nil, managedLateCRLRecord{IssuerID: id}); err != nil || handled {
				t.Fatal("Device prior authority leaked to another issuer/domain")
			}
		}
	}
	if _, handled, err := managedLateDeviceRootCRLPrior(context.Background(), secretStore{}, nil, nil, managedLateCRLRecord{IssuerID: createDeviceRootCRLID}); err != nil || handled {
		t.Fatal("ordinary native observation gained expired Device authority")
	}
}

func TestDeviceRootCRLAuthenticatedCallbackBindsUnchangedCommandSource(t *testing.T) {
	p, _, now := deviceRootCRLFixture(t)
	store := secretStore{Root: t.TempDir()}
	e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: p.Parent, Entries: make([]environmentCreateJournalEntry, 374)}}
	p.SourceSHA, p.OwnerSHA, p.ConfigurationSHA = deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.owner), deploymentplan.Digest(p.Configuration)
	f := &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema, ParentSHA: p.Parent, SourceSHA: p.SourceSHA, SourceOwnerSHA: p.OwnerSHA}
	p.SourcePins = managedLateExpiredRenewalSourcePins(f)
	r := &createServiceSessionRecovery{ManagedLateExpiredRenewal: f, ManagedTransportRecovery: &createManagedTransportRecovery{Device: p.Root}}
	observation := managedLateDeviceRootCRLObservation{Plan: p, Recovery: r, Source: f, SourcePins: p.SourcePins}
	if !deviceRootCRLObservationSourceValid(observation, store, e, r) {
		t.Fatal("already authenticated exact source context lost its authority pins")
	}
	ctx := context.WithValue(context.Background(), managedLateDeviceRootCRLObservationKey{}, observation)
	if _, handled, err := managedLateDeviceRootCRLPrior(ctx, store, e, r, deviceRootCRLRecord(p.Snapshot.Prior)); err == nil || !handled {
		t.Fatal("carried context without an active authenticated capability skipped fresh source derivation")
	}
	for _, mode := range []string{"journal", "owner", "parent", "appended-entry", "completed-parent", "store", "configuration", "source-pins", "provider-ancestor", "root"} {
		t.Run(mode, func(t *testing.T) {
			copy := *e
			copy.Journal.Entries = append([]environmentCreateJournalEntry(nil), e.Journal.Entries...)
			current := observation
			selected := store
			switch mode {
			case "journal":
				copy.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "owner":
				copy.owner.OwnerToken = "changed-command-owner"
			case "parent":
				copy.Journal.PlanSHA256 = strings.Repeat("f", 64)
			case "appended-entry":
				copy.Journal.Entries = append(copy.Journal.Entries, environmentCreateJournalEntry{})
			case "completed-parent":
				copy.Journal.CompletedAt = now
			case "store":
				selected.Root += "/other"
			case "configuration":
				current.Plan.ConfigurationSHA = strings.Repeat("f", 64)
			case "source-pins":
				current.SourcePins = strings.Repeat("f", 64)
			case "provider-ancestor":
				current.Plan.ProviderReview = ""
			case "root":
				current.Plan.Root.RootFingerprint = strings.Repeat("f", 64)
			}
			if deviceRootCRLObservationSourceValid(current, selected, &copy, r) {
				t.Fatal("authenticated callback reused after source authority changed")
			}
		})
	}
}
