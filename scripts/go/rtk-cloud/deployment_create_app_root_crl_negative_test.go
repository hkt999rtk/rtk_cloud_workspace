package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestAppRootCRLNegativeSameRootPublicationPreservesEveryRevocation(t *testing.T) {
	root, key, snapshot, now := rootCRLFixture(t)
	root.Domain = "app"
	request, err := buildManagedLateAppRootCRLRequest(root, snapshot, now)
	if err != nil || request.Number != "2" || len(request.Entries) != 2 {
		t.Fatal("original App Root history not preserved", err)
	}
	signed := signedRootCRLFixture(t, root, key, request)
	if _, err := validateManagedLateAppRootCRLSigned(root, snapshot, request, []byte(signed.PEM), now); err != nil {
		t.Fatal("genuine same-Root App publication rejected", err)
	}
	if _, err := parseManagedLateAppRootCRL(root, snapshot.Prior, true, now); err == nil {
		t.Fatal("expired original App publication became ready trust")
	}
	for _, mode := range []string{"lose-published-revocation", "lose-native-revocation", "change-published-reason", "change-published-time", "extra-unreviewed-revocation", "repeat-prior-number", "skip-number", "wrong-root-signature", "future-this-update", "expired-next-update"} {
		t.Run(mode, func(t *testing.T) {
			changed := request
			changed.Entries = slices.Clone(request.Entries)
			signer := key
			signingRoot := root
			switch mode {
			case "lose-published-revocation":
				changed.Entries = changed.Entries[1:]
			case "lose-native-revocation":
				changed.Entries = changed.Entries[:1]
			case "change-published-reason":
				changed.Entries[0].Reason = 5
			case "change-published-time":
				changed.Entries[0].RevokedAt = changed.Entries[0].RevokedAt.Add(time.Second)
			case "extra-unreviewed-revocation":
				changed.Entries = append(changed.Entries, createRootCRLRevocation{"65", now.Add(-time.Minute), 0})
			case "repeat-prior-number":
				changed.Number = "1"
			case "skip-number":
				changed.Number = "3"
			case "wrong-root-signature":
				signingRoot, signer, _, _ = rootCRLFixture(t)
			case "future-this-update":
				changed.ThisUpdate = now.Add(time.Minute)
			case "expired-next-update":
				changed.ThisUpdate = now.Add(-time.Hour)
				changed.NextUpdate = now.Add(-time.Minute)
			}
			bad := signedRootCRLFixture(t, signingRoot, signer, changed)
			bad.IssuerID = root.ID
			if _, err := validateManagedLateAppRootCRLSigned(root, snapshot, request, []byte(bad.PEM), now); err == nil {
				t.Fatal("unreviewed App Root publication admitted", mode)
			}
		})
	}
}

func TestAppRootCRLNegativePriorAndIssuerCannotBecomeAnotherAuthority(t *testing.T) {
	for _, mode := range []string{"foreign-domain", "foreign-environment", "intermediate", "inactive-root", "online-signer", "changed-root-fingerprint", "changed-prior-root", "changed-prior-digest", "changed-prior-number", "changed-prior-time", "missing-prior", "changed-native-reason", "changed-native-time", "remove-reason", "foreign-revoked-certificate"} {
		t.Run(mode, func(t *testing.T) {
			root, _, snapshot, now := rootCRLFixture(t)
			root.Domain = "app"
			switch mode {
			case "foreign-domain":
				root.Domain = "device"
			case "foreign-environment":
				root.Environment = "staging"
			case "intermediate":
				root.Kind = "intermediate"
			case "inactive-root":
				root.Status = "revoked"
			case "online-signer":
				root.Document["signer_provider"] = "openbao"
			case "changed-root-fingerprint":
				root.RootFingerprint = strings.Repeat("f", 64)
			case "changed-prior-root":
				snapshot.Prior.IssuerID = "22222222-2222-4222-8222-222222222222"
			case "changed-prior-digest":
				snapshot.Prior.Digest = strings.Repeat("f", 64)
			case "changed-prior-number":
				snapshot.Prior.Number = "2"
			case "changed-prior-time":
				snapshot.Prior.ThisUpdate = snapshot.Prior.ThisUpdate.Add(time.Second)
			case "missing-prior":
				snapshot.Prior.PEM = ""
			case "changed-native-reason":
				snapshot.Bindings = append(snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-49 * time.Hour), 2})
			case "changed-native-time":
				snapshot.Bindings = append(snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-time.Hour), 0})
			case "remove-reason":
				snapshot.Bindings[0].Reason = 8
			case "foreign-revoked-certificate":
				other, _, _, _ := rootCRLFixture(t)
				snapshot.Certificates = append(snapshot.Certificates, struct {
					PEM       string    `json:"certificate_pem"`
					RevokedAt time.Time `json:"revoked_at"`
				}{other.Certificate, now.Add(-time.Hour)})
			}
			if _, err := buildManagedLateAppRootCRLRequest(root, snapshot, now); err == nil {
				t.Fatal("foreign or changed App prior authority admitted", mode)
			}
		})
	}
}

func TestAppRootCRLNegativeCannotQualifySelfConsistentLossOrUnreviewedRequest(t *testing.T) {
	root, key, snapshot, now := rootCRLFixture(t)
	root.Domain = "app"
	request, err := buildManagedLateAppRootCRLRequest(root, snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"omit-prior", "omit-native", "empty-union", "changed-reason", "changed-revocation-time", "added-serial", "jump-number", "wrong-issuer", "wrong-fingerprint", "shortened-next-update"} {
		t.Run(mode, func(t *testing.T) {
			altered := request
			altered.Entries = slices.Clone(request.Entries)
			switch mode {
			case "omit-prior":
				altered.Entries = altered.Entries[1:]
			case "omit-native":
				altered.Entries = altered.Entries[:1]
			case "empty-union":
				altered.Entries = nil
			case "changed-reason":
				altered.Entries[0].Reason = 5
			case "changed-revocation-time":
				altered.Entries[0].RevokedAt = altered.Entries[0].RevokedAt.Add(time.Second)
			case "added-serial":
				altered.Entries = append(altered.Entries, createRootCRLRevocation{"65", now.Add(-time.Minute), 0})
			case "jump-number":
				altered.Number = "3"
			case "wrong-issuer":
				altered.IssuerID = "22222222-2222-4222-8222-222222222222"
			case "wrong-fingerprint":
				altered.Fingerprint = strings.Repeat("f", 64)
			case "shortened-next-update":
				altered.NextUpdate = altered.NextUpdate.Add(-time.Hour)
			}
			// Both the reviewed object and DER are changed together. Rebuilding
			// the native union must still detect a lost or invented revocation.
			signed := signedRootCRLFixture(t, root, key, altered)
			if _, err := validateManagedLateAppRootCRLSigned(root, snapshot, altered, []byte(signed.PEM), now); err == nil {
				t.Fatal("self-consistent but unqualified App publication admitted", mode)
			}
		})
	}
}

func appRootCRLNegativeTailFixture(t *testing.T) (createAppRootCRLArtifact, environmentCreateJournal, environmentCreateJournal) {
	t.Helper()
	root, signer, snapshot, now := rootCRLFixture(t)
	root.Domain, root.ID = "app", managedLateAppRootID
	snapshot.Prior.IssuerID = root.ID
	request, err := buildManagedLateAppRootCRLRequest(root, snapshot, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, root, signer, request)
	sha := strings.Repeat("a", 64)
	source := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: sha, OwnerToken: "isolated-App-owner"}
	source.Entries = []environmentCreateJournalEntry{{Key: "isolated-completed-ancestry", Phase: "bootstrap", State: "completed", IntentSHA256: sha, StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Receipt: json.RawMessage(`{"isolated_source":true}`)}}
	p := createAppRootCRLPlan{Schema: createAppRootCRLSchema, Parent: sha, SourceJournalSHA: deploymentplan.Digest(source), SourceOwnerSHA: sha, CheckerSHA: sha, ConfigurationSHA: sha, KubeconfigSHA: sha, Configuration: deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}, Root: root, Snapshot: snapshot, Request: request, Signed: signed, Intermediate: environmentCreateRegisteredIssuer{ID: "88888888-8888-4888-8888-888888888888"}, Candidate: "deployment/repairs/app-root-crl/isolated-negative", QualifiedAt: now.Add(-30 * time.Second), Operator: initialOperatorBinding{Schema: "isolated-binding", Parent: sha, UserID: "22222222-2222-4222-8222-222222222222", AccountUID: "33333333-3333-4333-8333-333333333333", EmailSHA: sha, OriginalConfiguredSHA: sha}, OriginalVolumes: map[string]environmentCreateVolumeOwner{}, ConsumerVolumes: map[string]environmentCreateVolumeOwner{}}
	evidence := map[string]string{}
	receipts := []map[string]string{}
	for i, name := range []string{"video-cloud-api-app-pki", "mqtt-pki"} {
		path, claim, container := "/run/pki-state/app-root-crl.json", "video-cloud-api-app-pki-trust", "app"
		if i == 1 {
			path, claim, container = "/run/pki-state/app-crl.json", "mqtt-pki-trust", "pkibroker"
		}
		target := createRootCRLStateTarget{ID: "Deployment/video-cloud-dev-video-cloud/" + name, UID: "44444444-4444-4444-8444-444444444444", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}}, Container: container, PodUID: "55555555-5555-4555-8555-555555555555", PodName: name + "-0", ClaimID: "PersistentVolumeClaim/video-cloud-dev-video-cloud/" + claim, ClaimUID: "66666666-6666-4666-8666-666666666666", Paths: []string{path, strings.TrimSuffix(path, ".json") + "-" + p.Intermediate.ID + ".json"}}
		p.Consumers = append(p.Consumers, target)
		owner := environmentCreateVolumeOwner{Namespace: "video-cloud-dev-video-cloud", PVCName: claim, PVCUID: target.ClaimUID}
		p.ConsumerVolumes[target.ClaimID], p.OriginalVolumes[target.ClaimID] = owner, owner
		observed := map[string]string{"consumer-owner-" + target.ID: deploymentplan.Digest(target.Desired), "consumer-Pod-" + target.ID: deploymentplan.Digest(target.PodUID), "consumer-volume-" + target.ClaimID: deploymentplan.Digest(owner), "cache-" + target.ID + "-" + filepath.Base(target.Paths[0]): sha, "cache-" + target.ID + "-" + filepath.Base(target.Paths[1]): sha, "native-ACKs-" + root.ID: sha, "native-ACKs-" + p.Intermediate.ID: sha}
		receipts = append(receipts, observed)
		for k, v := range observed {
			evidence[k] = v
		}
	}
	review := deploymentplan.Digest(p)
	a := createAppRootCRLArtifact{Schema: createAppRootCRLSchema + "/artifact", Plan: p, QualifiedAt: p.QualifiedAt, ValidUntil: p.QualifiedAt.Add(15 * time.Minute), Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}}
	for _, id := range createAppRootCRLGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	current := source
	current.Entries = slices.Clone(source.Entries)
	current.KubernetesLeases = []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "77777777-7777-4777-8777-777777777777", Holder: rootCRLLeasePlan(p.Configuration, review).SHA256() + ":isolated-App-execution"}}
	key := "app-root-crl-renewal-" + review
	keys := []string{key, key + "/native-import", key + "/native-runtime-video-cloud-api-app-pki", key + "/native-runtime-mqtt-pki"}
	intents := []any{p, appRootCRLImportIntent(p), appRootCRLRuntimeIntent(p, p.Consumers[0]), appRootCRLRuntimeIntent(p, p.Consumers[1])}
	values := []any{appRootCRLReceipt(a, evidence), deploymentplan.Object{"native_crl": p.Signed, "request_sha256": deploymentplan.Digest(p.Request), "operator_binding_sha256": deploymentplan.Digest(p.Operator)}, receipts[0], receipts[1]}
	start := now.Add(-20 * time.Second)
	for i, k := range keys {
		started, completed := start.Add(time.Duration(i)*time.Second), start.Add(time.Duration(i+1)*time.Second)
		if i == 0 {
			completed = start.Add(4 * time.Second)
		}
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: k, Phase: "bootstrap", IntentSHA256: activeTransportJournalDigest(intents[i]), State: "completed", StartedAt: started, CompletedAt: completed, Receipt: mustProviderRecoveryJSON(values[i])})
	}
	return a, source, current
}

func TestAppRootCRLNegativeFiniteTailRejectsUnknownAndChangedAuthority(t *testing.T) {
	a, source, original := appRootCRLNegativeTailFixture(t)
	if !managedLateAppRootCRLProgressValid(a, source, source, false) || !managedLateAppRootCRLProgressValid(a, source, original, true) {
		t.Fatal("genuine finite isolated App tail rejected")
	}
	for _, mode := range []string{"extra-tail", "unknown-top", "unknown-native-import", "reordered-consumers", "changed-source-receipt", "changed-source-owner", "changed-journal-metadata", "missing-lease", "foreign-lease", "extra-lease", "zero-start", "zero-completion", "future-completion", "expired-GO", "failed-gate", "missing-gate", "changed-principal", "changed-custody", "changed-prior", "changed-root", "changed-current-CRL", "changed-import-operator", "changed-import-request", "missing-import-prior", "lost-native-import", "allocated-child-followed-by-runtime", "pending-top-with-completion"} {
		t.Run(mode, func(t *testing.T) {
			var artifact createAppRootCRLArtifact
			var current environmentCreateJournal
			if json.Unmarshal(mustProviderRecoveryJSON(a), &artifact) != nil || json.Unmarshal(mustProviderRecoveryJSON(original), &current) != nil {
				t.Fatal("isolated clone")
			}
			switch mode {
			case "extra-tail":
				current.Entries = append(current.Entries, current.Entries[len(current.Entries)-1])
			case "unknown-top":
				current.Entries[1].Key = "app-root-crl-renewal-" + strings.Repeat("f", 64)
			case "unknown-native-import":
				current.Entries[2].Key += "-foreign"
			case "reordered-consumers":
				current.Entries[3], current.Entries[4] = current.Entries[4], current.Entries[3]
			case "changed-source-receipt":
				current.Entries[0].Receipt = json.RawMessage(`{"changed":true}`)
			case "changed-source-owner":
				current.OwnerToken = "another-owner"
			case "changed-journal-metadata":
				current.OriginalJournalSHA256 = strings.Repeat("f", 64)
			case "missing-lease":
				current.KubernetesLeases = nil
			case "foreign-lease":
				current.KubernetesLeases[0].Holder = "foreign"
			case "extra-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, current.KubernetesLeases[0])
			case "zero-start":
				current.Entries[2].StartedAt = time.Time{}
			case "zero-completion":
				current.Entries[2].CompletedAt = time.Time{}
			case "future-completion":
				current.Entries[4].CompletedAt = time.Now().UTC().Add(time.Minute)
			case "expired-GO":
				current.Entries[1].StartedAt = a.ValidUntil
			case "failed-gate":
				artifact.Qualification.Gates[1].Status = "FAIL"
			case "missing-gate":
				artifact.Qualification.Gates = artifact.Qualification.Gates[:5]
			case "changed-principal":
				artifact.Plan.Operator.UserID = "99999999-9999-4999-8999-999999999999"
			case "changed-custody":
				artifact.Plan.Material = map[string]string{"pki/custody/app-root-v1-key-passphrase": strings.Repeat("f", 64)}
			case "changed-prior":
				artifact.Plan.Snapshot.Prior.Digest = strings.Repeat("f", 64)
			case "changed-root":
				artifact.Plan.Root.ID = "99999999-9999-4999-8999-999999999999"
			case "changed-current-CRL":
				artifact.Plan.Signed.Digest = strings.Repeat("f", 64)
			case "changed-import-operator", "changed-import-request", "missing-import-prior":
				expected := deploymentplan.Object{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request), "operator_binding_sha256": deploymentplan.Digest(a.Plan.Operator)}
				if mode == "changed-import-operator" {
					expected["operator_binding_sha256"] = strings.Repeat("f", 64)
				}
				if mode == "changed-import-request" {
					expected["request_sha256"] = strings.Repeat("f", 64)
				}
				if mode == "missing-import-prior" {
					delete(expected, "native_crl")
				}
				current.Entries[2].Receipt = mustProviderRecoveryJSON(expected)
			case "lost-native-import":
				current.Entries = append(current.Entries[:2], current.Entries[3:]...)
			case "allocated-child-followed-by-runtime":
				current.Entries[2].State = "allocated"
				current.Entries[2].CompletedAt = time.Time{}
			case "pending-top-with-completion":
				current.Entries[1].State = "pending"
			}
			if managedLateAppRootCRLProgressValid(artifact, source, current, true) {
				t.Fatal("unqualified App publication tail admitted", mode)
			}
		})
	}
}

func TestAppRootCRLNegativeNativeIssuerCannotReplaceOriginal(t *testing.T) {
	root, _, _, _ := rootCRLFixture(t)
	root.Domain = "app"
	root.Reference = "isolated-offline-App-Root-reference"
	root.Document["signer_reference"] = root.Reference
	root.Document["parent_issuer_id"] = ""
	actual := map[string]any{"issuer_id": root.ID, "environment": "dev", "trust_domain": "app", "kind": "root", "status": "active", "certificate_fingerprint_sha256": root.RootFingerprint, "certificate_pem": root.Certificate, "signer_provider": "offline", "chain_pem": root.ChainPEM, "trust_bundle_version": root.BundleVersion, "signer_reference": root.Reference, "parent_issuer_id": root.Document["parent_issuer_id"]}
	if !appRootCRLNativeIssuerMatches(actual, root) {
		t.Fatal("original native App issuer rejected")
	}
	for _, field := range []string{"issuer_id", "environment", "trust_domain", "kind", "status", "certificate_fingerprint_sha256", "certificate_pem", "signer_provider", "chain_pem", "trust_bundle_version", "signer_reference", "parent_issuer_id"} {
		t.Run(field, func(t *testing.T) {
			changed := map[string]any{}
			for k, v := range actual {
				changed[k] = v
			}
			changed[field] = "foreign"
			if appRootCRLNativeIssuerMatches(changed, root) {
				t.Fatal("changed current native issuer admitted", field)
			}
		})
	}
	for _, mode := range []string{"legacy-domain-only", "legacy-root-hash-only", "both-legacy-aliases", "missing-domain", "missing-fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			changed := map[string]any{}
			for k, v := range actual {
				changed[k] = v
			}
			if mode == "legacy-domain-only" || mode == "both-legacy-aliases" || mode == "missing-domain" {
				delete(changed, "trust_domain")
				if mode != "missing-domain" {
					changed["domain"] = "app"
				}
			}
			if mode == "legacy-root-hash-only" || mode == "both-legacy-aliases" || mode == "missing-fingerprint" {
				delete(changed, "certificate_fingerprint_sha256")
				if mode != "missing-fingerprint" {
					changed["root_sha256"] = root.RootFingerprint
				}
			}
			if appRootCRLNativeIssuerMatches(changed, root) {
				t.Fatal("legacy aliases replaced actual native wire evidence", mode)
			}
		})
	}
}

func appRootCRLNegativeSourceBoundary(store secretStore, parent string) (*environmentCreateExecution, createAppRootCRLPlan) {
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-App-source-owner"}
	e := &environmentCreateExecution{Store: store, Journal: source, owner: environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: source.Stack, PlanSHA256: parent, OwnerToken: source.OwnerToken}}
	plan := createAppRootCRLPlan{Schema: createAppRootCRLSchema, Parent: parent, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: deploymentplan.Digest(source), Configuration: deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": source.Stack}}, Root: environmentCreateRegisteredIssuer{ID: managedLateAppRootID, Domain: "app", Document: deploymentplan.Object{"signer_provider": "offline"}}, Snapshot: createRootCRLNativeSnapshot{Prior: createRootCRLRecord{IssuerID: managedLateAppRootID, Digest: managedLateAppRootPriorSHA, Number: "1"}}}
	return e, plan
}

func TestAppRootCRLNegativeSourceBoundaryCannotSelectAnotherRootOrHistory(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	e, original := appRootCRLNegativeSourceBoundary(store, strings.Repeat("a", 64))
	// This minimal isolated source deliberately lacks the full historical
	// chain. Its exact early boundary passes, then the ancestry reader denies.
	if _, err := validateManagedLateAppRootCRLSource(store, e, original); err == nil || !strings.Contains(err.Error(), "authenticate original App post-mint history") {
		t.Fatal("isolated source did not reach its separate ancestry guard", err)
	}
	for _, mode := range []string{"nil-source", "schema", "parent", "source-sha", "owner-sha", "root-id", "root-domain", "online-custody", "prior-number", "prior-body", "changed-current-owner", "changed-current-journal", "post-installation-clock"} {
		t.Run(mode, func(t *testing.T) {
			var plan createAppRootCRLPlan
			if json.Unmarshal(mustProviderRecoveryJSON(original), &plan) != nil {
				t.Fatal("isolated plan clone")
			}
			current := *e
			current.owner = e.owner
			current.Journal = e.Journal
			selected := &current
			switch mode {
			case "nil-source":
				selected = nil
			case "schema":
				plan.Schema = "foreign-schema"
			case "parent":
				plan.Parent = strings.Repeat("f", 64)
			case "source-sha":
				plan.SourceJournalSHA = strings.Repeat("f", 64)
			case "owner-sha":
				plan.SourceOwnerSHA = strings.Repeat("f", 64)
			case "root-id":
				plan.Root.ID = "99999999-9999-4999-8999-999999999999"
			case "root-domain":
				plan.Root.Domain = "device"
			case "online-custody":
				plan.Root.Document["signer_provider"] = "openbao"
			case "prior-number":
				plan.Snapshot.Prior.Number = "2"
			case "prior-body":
				plan.Snapshot.Prior.Digest = strings.Repeat("f", 64)
			case "changed-current-owner":
				current.owner.OwnerToken = "foreign-owner"
			case "changed-current-journal":
				current.Journal.Entries = append(current.Journal.Entries, environmentCreateJournalEntry{Key: "unqualified-installation", State: "pending"})
			case "post-installation-clock":
				plan.PostSource.InstallationClock = &createManagedLateInstallationClock{}
			}
			if _, err := validateManagedLateAppRootCRLSource(store, selected, plan); err == nil || !strings.Contains(err.Error(), "stage=original-source") {
				t.Fatal("changed initial App source crossed its exact boundary", mode, err)
			}
		})
	}
}

func TestAppRootCRLNegativeSourceCannotRunAfterInstallationClockOrUnknownMarker(t *testing.T) {
	for _, mode := range []string{"genuine-installed-clock", "pending-malformed-marker", "symlink-marker", "public-marker"} {
		t.Run(mode, func(t *testing.T) {
			store, post, artifact, overall, epoch := managedLateInstallationNegativeFixture(t)
			e, plan := appRootCRLNegativeSourceBoundary(store, artifact.Plan.ParentSHA)
			if _, err := validateManagedLateAppRootCRLSource(store, e, plan); err == nil || !strings.Contains(err.Error(), "authenticate original App post-mint history") {
				t.Fatal("absent marker did not pass separate no-clock boundary", err)
			}
			clock, err := newManagedLateInstallationClock(post, artifact, post.SourceOwnerSHA, overall, epoch)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = writeManagedLateInstallationClock(store, clock); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(store.Root, managedLateInstallationClockPath(artifact.Plan.ParentSHA))
			before, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "pending-malformed-marker":
				if os.WriteFile(marker, []byte(`{"uncertain_first_installation":true}`), 0600) != nil {
					t.Fatal("fixture marker")
				}
			case "symlink-marker":
				if os.Rename(marker, marker+".original") != nil || os.Symlink(marker+".original", marker) != nil {
					t.Fatal("fixture symlink")
				}
			case "public-marker":
				if os.Chmod(marker, 0644) != nil {
					t.Fatal("fixture mode")
				}
			}
			if _, err := validateManagedLateAppRootCRLSource(store, e, plan); err == nil || strings.Contains(err.Error(), "stage=original-App-source-and-authority") {
				t.Fatal("App repair ran past committed or uncertain installation marker", mode, err)
			}
			if mode == "genuine-installed-clock" {
				actual, err := os.ReadFile(marker)
				if err != nil || string(actual) != string(before) {
					t.Fatal("App source check rewrote genuine installation clock")
				}
			}
		})
	}
}

func TestAppRootCRLNegativeConsumerReceiptRequiresExactActualEvidence(t *testing.T) {
	a, source, original := appRootCRLNegativeTailFixture(t)
	for index, target := range a.Plan.Consumers {
		var valid map[string]string
		if decodeEnvironmentJSON(original.Entries[len(source.Entries)+2+index].Receipt, &valid) != nil {
			t.Fatal("fixture consumer receipt")
		}
		if !managedLateAppRootCRLConsumerEvidenceValid(a.Plan, target, valid) {
			t.Fatal("actual producer evidence rejected")
		}
		for _, mode := range []string{"missing-owner", "missing-Pod", "missing-volume", "missing-root-ACK", "missing-ICA-ACK", "missing-root-cache", "foreign-key", "wrong-owner", "wrong-Pod", "wrong-volume", "non-sha-cache", "non-sha-ACK", "empty-receipt", "one-field-receipt"} {
			t.Run(deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])+"/"+mode, func(t *testing.T) {
				evidence := map[string]string{}
				for k, v := range valid {
					evidence[k] = v
				}
				ownerKey, podKey, volumeKey := "consumer-owner-"+target.ID, "consumer-Pod-"+target.ID, "consumer-volume-"+target.ClaimID
				rootACK, icaACK := "native-ACKs-"+a.Plan.Root.ID, "native-ACKs-"+a.Plan.Intermediate.ID
				cache := "cache-" + target.ID + "-" + filepath.Base(target.Paths[0])
				switch mode {
				case "missing-owner":
					delete(evidence, ownerKey)
				case "missing-Pod":
					delete(evidence, podKey)
				case "missing-volume":
					delete(evidence, volumeKey)
				case "missing-root-ACK":
					delete(evidence, rootACK)
				case "missing-ICA-ACK":
					delete(evidence, icaACK)
				case "missing-root-cache":
					delete(evidence, cache)
				case "foreign-key":
					delete(evidence, cache)
					evidence["unverified-cache"] = strings.Repeat("a", 64)
				case "wrong-owner":
					evidence[ownerKey] = strings.Repeat("f", 64)
				case "wrong-Pod":
					evidence[podKey] = strings.Repeat("f", 64)
				case "wrong-volume":
					evidence[volumeKey] = strings.Repeat("f", 64)
				case "non-sha-cache":
					evidence[cache] = "observed"
				case "non-sha-ACK":
					evidence[rootACK] = "true"
				case "empty-receipt":
					evidence = map[string]string{}
				case "one-field-receipt":
					evidence = map[string]string{"invented": strings.Repeat("a", 64)}
				}
				if managedLateAppRootCRLConsumerEvidenceValid(a.Plan, target, evidence) {
					t.Fatal("incomplete actual consumer proof admitted", mode)
				}
				current := original
				current.Entries = slices.Clone(original.Entries)
				current.Entries[len(source.Entries)+2+index].Receipt = mustProviderRecoveryJSON(evidence)
				combined := map[string]string{}
				for _, entry := range current.Entries[len(source.Entries)+2:] {
					var observed map[string]string
					if decodeEnvironmentJSON(entry.Receipt, &observed) != nil {
						t.Fatal("isolated evidence")
					}
					for k, v := range observed {
						combined[k] = v
					}
				}
				// Recompute the top receipt too; the exact per-consumer
				// semantics must reject it, not merely a digest mismatch.
				current.Entries[len(source.Entries)].Receipt = mustProviderRecoveryJSON(appRootCRLReceipt(a, combined))
				if managedLateAppRootCRLProgressValid(a, source, current, true) {
					t.Fatal("self-consistent but incomplete runtime evidence admitted", mode)
				}
			})
		}
	}
}

func TestAppRootCRLNegativeHistoricalClockPermitsOnlyCompletedAuthenticTail(t *testing.T) {
	a, source, current := appRootCRLNegativeTailFixture(t)
	session := "a849212c-47ee-4b29-8f98-bb281fb45bb4"
	sha := strings.Repeat("a", 64)
	expiry := time.Now().UTC().Add(8 * time.Hour)
	a.Plan.PostSource.Ceremony.BootstrapSessionID = session
	a.Plan.PostSource.Fingerprint = sha
	a.Plan.PostSource.SessionExpires = expiry
	a.Plan.PostSource.LeafNotAfter = expiry
	// The actual App stage intent/review changes with the immutable source
	// authority. Rebind all four known intents before modeling its later clock.
	review := deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256 = review
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = review
	}
	key := "app-root-crl-renewal-" + review
	current.Entries[1].Key = key
	current.Entries[1].IntentSHA256 = activeTransportJournalDigest(a.Plan)
	current.Entries[2].Key = key + "/native-import"
	current.Entries[3].Key = key + "/native-runtime-video-cloud-api-app-pki"
	current.Entries[4].Key = key + "/native-runtime-mqtt-pki"
	current.KubernetesLeases[0].Holder = rootCRLLeasePlan(a.Plan.Configuration, review).SHA256() + ":isolated-App-execution"
	combined := map[string]string{}
	for _, entry := range current.Entries[3:] {
		var evidence map[string]string
		if decodeEnvironmentJSON(entry.Receipt, &evidence) != nil {
			t.Fatal("isolated evidence")
		}
		for k, v := range evidence {
			combined[k] = v
		}
	}
	current.Entries[1].Receipt = mustProviderRecoveryJSON(appRootCRLReceipt(a, combined))
	epoch := current.Entries[1].CompletedAt.Add(time.Second)
	clock := &createManagedLateInstallationClock{Schema: managedLateInstallationClockSchema, Parent: a.Plan.Parent, Review: sha, SourceSHA: deploymentplan.Digest(current), OwnerSHA: a.Plan.SourceOwnerSHA, CheckerSHA: a.Plan.CheckerSHA, SessionID: session, Fingerprint: sha, DescriptorSHA: sha, QualificationSHA: sha, StartedAt: epoch, OverallDeadline: epoch.Add(90 * time.Minute), SessionExpires: expiry, LeafNotAfter: expiry}
	clock.Deadline = managedLateInstallationClockDeadline(epoch, clock.OverallDeadline, expiry, expiry)
	if !managedLateAppRootCRLHistoricalClockAllowed(a, source, current, clock) {
		t.Fatal("genuine completed App history rejected after first installation clock")
	}
	for _, mode := range []string{"nil-clock", "pending-top", "pending-import", "missing-consumer", "changed-source", "changed-clock-source", "foreign-parent", "foreign-owner", "different-session", "changed-fingerprint", "changed-expiry", "clock-before-App-complete", "extended-clock", "unknown-App-tail", "raw-completed-parent"} {
		t.Run(mode, func(t *testing.T) {
			actual := current
			actual.Entries = slices.Clone(current.Entries)
			changedClock := *clock
			selected := &changedClock
			changedSource := source
			switch mode {
			case "nil-clock":
				selected = nil
			case "pending-top":
				actual.Entries[1].State = "pending"
				actual.Entries[1].Receipt = nil
				actual.Entries[1].CompletedAt = time.Time{}
			case "pending-import":
				actual.Entries[2].State = "pending"
				actual.Entries[2].Receipt = nil
				actual.Entries[2].CompletedAt = time.Time{}
			case "missing-consumer":
				actual.Entries = actual.Entries[:4]
			case "changed-source":
				changedSource.OwnerToken = "foreign-source-owner"
			case "changed-clock-source":
				changedClock.SourceSHA = strings.Repeat("f", 64)
			case "foreign-parent":
				changedClock.Parent = strings.Repeat("f", 64)
			case "foreign-owner":
				changedClock.OwnerSHA = strings.Repeat("f", 64)
			case "different-session":
				changedClock.SessionID = "99999999-9999-4999-8999-999999999999"
			case "changed-fingerprint":
				changedClock.Fingerprint = strings.Repeat("f", 64)
			case "changed-expiry":
				changedClock.SessionExpires = expiry.Add(time.Hour)
				changedClock.LeafNotAfter = changedClock.SessionExpires
			case "clock-before-App-complete":
				changedClock.StartedAt = actual.Entries[1].CompletedAt.Add(-time.Second)
				changedClock.OverallDeadline = changedClock.StartedAt.Add(90 * time.Minute)
				changedClock.Deadline = managedLateInstallationClockDeadline(changedClock.StartedAt, changedClock.OverallDeadline, expiry, expiry)
			case "extended-clock":
				changedClock.Deadline = changedClock.Deadline.Add(time.Minute)
			case "unknown-App-tail":
				actual.Entries[4].Key += "-foreign"
			case "raw-completed-parent":
				actual.CompletedAt = time.Now().UTC()
			}
			if managedLateAppRootCRLHistoricalClockAllowed(a, changedSource, actual, selected) {
				t.Fatal("pending or changed App history crossed installation clock", mode)
			}
		})
	}
}

func TestAppRootCRLNegativeOrphanCompletionPointerCannotInventParentTail(t *testing.T) {
	for _, mode := range []string{"valid-orphan-pointer", "malformed-orphan-pointer", "symlink-orphan-pointer"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			parent := strings.Repeat("a", 64)
			e, _ := appRootCRLNegativeSourceBoundary(store, parent)
			before := deploymentplan.Digest(e.Journal)
			selected, artifact, err := managedLateAppRootCRLFrame(store, e, false)
			if err != nil || selected != e || artifact != nil {
				t.Fatal("untouched source without App pointer or entry rejected", err)
			}
			pointer := appRootCRLPointer(parent)
			proof := createAppRootCRLCompletion{Schema: createAppRootCRLSchema + "/completion", Parent: parent, Review: strings.Repeat("b", 64), SourceSHA: before, FinalSHA: strings.Repeat("c", 64), EvidenceSHA: strings.Repeat("d", 64), SourceEntryCount: 370, FinalEntryCount: 374, Completed: true}
			if err := store.write(pointer, mustProviderRecoveryJSON(proof), false); err != nil {
				t.Fatal(err)
			}
			full := filepath.Join(store.Root, pointer)
			if mode == "malformed-orphan-pointer" {
				if os.WriteFile(full, []byte(`{"uncertain_import":true}`), 0600) != nil {
					t.Fatal("fixture pointer")
				}
			}
			if mode == "symlink-orphan-pointer" {
				if os.Rename(full, full+".original") != nil || os.Symlink(full+".original", full) != nil {
					t.Fatal("fixture pointer")
				}
			}
			if _, _, err := managedLateAppRootCRLFrame(store, e, false); err == nil {
				t.Fatal("orphan App completion bypassed literal parent history", mode)
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("orphan pointer check rewrote source history")
			}
		})
	}
}
