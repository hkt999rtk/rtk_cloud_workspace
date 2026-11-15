package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestIssuerEnrollmentContinuationRequiresActiveOriginalSession(t *testing.T) {
	now := time.Now()
	intent := createServiceSessionIntent{SessionID: "original", IssuerID: "issuer", RootSHA: "root", Subjects: []string{"service:certissuer"}}
	original := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: strings.Repeat("a", 64), Status: "active", Expires: now.Add(time.Hour), Subjects: intent.Subjects}
	for _, mode := range []string{"original", "expired", "other-active", "changed-subjects", "finalized", "wrong-lineage"} {
		t.Run(mode, func(t *testing.T) {
			state := original
			switch mode {
			case "expired":
				state.Expires = now
			case "other-active":
				state.OtherActive = 1
			case "changed-subjects":
				state.Subjects = []string{"service:other"}
			case "finalized":
				state.Status = "finalized"
			case "wrong-lineage":
				state.Issuer = "replacement"
			}
			if issuerEnrollmentSessionUsable(intent, state, now) != (mode == "original") {
				t.Fatal("unexpected original session decision")
			}
		})
	}
}

func TestIssuerContinuationChecksTransportAgainstOriginalDeadline(t *testing.T) {
	now := time.Now()
	started := now.Add(-8 * time.Hour)
	ctx, cancel, err := boundIssuerContinuation(context.Background(), started, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(started.Add(24*time.Hour)) {
		t.Fatal("continuation reset its original deadline")
	}
	lifetime := createRecoveryTransportMinimumLife(ctx, 0, true)
	if lifetime < 16*time.Hour+14*time.Minute || lifetime > 16*time.Hour+15*time.Minute {
		t.Fatal("transport lifetime used a fresh timeout", lifetime)
	}
	if _, _, err := boundIssuerContinuation(context.Background(), started, 8*time.Hour, now); err == nil {
		t.Fatal("expired continuation was admitted")
	}
}

func TestIssuerResumeAdmissionReusesOriginalAndPreservesExtensions(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	now := time.Now().UTC()
	review := strings.Repeat("a", 64)
	key := "issuer-enrollment-continuation-" + strings.Repeat("b", 64)
	path := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "resume-admission.json")
	legacy := mustProviderRecoveryJSON(deploymentplan.Object{"review_sha256": review, "original_admission": key, "resumed_at": now, "deadline": now.Add(time.Hour)})
	if err := store.write(path, legacy, false); err != nil {
		t.Fatal(err)
	}
	if err := recordIssuerResumeAdmission(store, path, review, key, now.Add(time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal("same original admission rejected", err)
	}
	got, err := os.ReadFile(filepath.Join(store.Root, path))
	if err != nil || string(got) != string(legacy) {
		t.Fatal("retry rewrote original admission", err)
	}
	if err := recordIssuerResumeAdmission(store, path, review, key, now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal("authorized timeout extension was rejected", err)
	}
	got, err = os.ReadFile(filepath.Join(store.Root, path))
	if err != nil || string(got) != string(legacy) {
		t.Fatal("extension rewrote original admission", err)
	}
	if err := recordIssuerResumeAdmission(store, path, review, "foreign", now.Add(time.Minute), now.Add(time.Hour)); err == nil {
		t.Fatal("foreign original admission was accepted")
	}
}

func TestExpiredIssuerEnrollmentOnlyQualifiesOriginalSession(t *testing.T) {
	now := time.Now()
	intent := createServiceSessionIntent{SessionID: "original", IssuerID: "issuer", RootSHA: "root", Subjects: []string{"service:certissuer", "service:openbao"}}
	state := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: strings.Repeat("a", 64), Status: "active", Expires: now.Add(-time.Second), Subjects: intent.Subjects, Installed: []string{"service:certissuer"}}
	if !issuerEnrollmentSessionExpired(intent, state, now) {
		t.Fatal("original expired session rejected")
	}
	for _, mode := range []string{"active", "competing", "issuer", "subject", "installed", "finalized"} {
		t.Run(mode, func(t *testing.T) {
			modified := state
			switch mode {
			case "active":
				modified.Expires = now.Add(time.Second)
			case "competing":
				modified.OtherActive = 1
			case "issuer":
				modified.Issuer = "other"
			case "subject":
				modified.Subjects = []string{"service:other"}
			case "installed":
				modified.Installed = []string{"service:other"}
			case "finalized":
				modified.Status = "finalized"
			}
			if issuerEnrollmentSessionExpired(intent, modified, now) {
				t.Fatal("invalid recovery session accepted")
			}
		})
	}
}

func TestIssuerCoreContinuationSelectsOnlyCompletedOriginalCRLCheckpoint(t *testing.T) {
	now := time.Now()
	for _, mode := range []string{"completed", "failed-crls", "foreign-stage", "extra-pending", "invalid-admission"} {
		t.Run(mode, func(t *testing.T) {
			j := environmentCreateJournal{Environment: "dev"}
			complete := func(key string) environmentCreateJournalEntry {
				return environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: now, Receipt: json.RawMessage(`{}`)}
			}
			if mode == "extra-pending" {
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "another-pending", State: "pending", IntentSHA256: strings.Repeat("c", 64)})
			}
			j.Entries = append(j.Entries, complete("kubernetes/service-issuer-server-enrollment"))
			entry := environmentCreateJournalEntry{Key: issuerEnrollmentContinuationPrefix + strings.Repeat("a", 64), State: "pending", Phase: "continuation", StartedAt: now, IntentSHA256: strings.Repeat("b", 64)}
			if mode == "invalid-admission" {
				entry.Phase = "foreign"
			}
			j.Entries = append(j.Entries, entry)
			for _, key := range issuerCoreCheckpointStages {
				j.Entries = append(j.Entries, complete(key))
			}
			switch mode {
			case "failed-crls":
				j.Entries[len(j.Entries)-1].State = "pending"
			case "foreign-stage":
				j.Entries[len(j.Entries)-1].Key = "kubernetes/unknown"
			}
			profile, _ := createRecoveryCheckpointProfile(j)
			if (profile == acceptedIssuerCoreCRLsProfile) != (mode == "completed") {
				t.Fatalf("unexpected checkpoint profile: %s", profile)
			}
		})
	}
}

func TestIssuerLogTrustContinuationRequiresOriginalCAInterruption(t *testing.T) {
	now := time.Now()
	completed := func(key string) environmentCreateJournalEntry {
		return environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: now, Receipt: json.RawMessage(`{}`)}
	}
	pending := func(key, phase string) environmentCreateJournalEntry {
		return environmentCreateJournalEntry{Key: key, State: "pending", Phase: phase, StartedAt: now, IntentSHA256: strings.Repeat("b", 64)}
	}
	j := environmentCreateJournal{Environment: "dev", Entries: []environmentCreateJournalEntry{
		completed("kubernetes/service-issuer-server-enrollment"), pending(issuerEnrollmentContinuationPrefix+strings.Repeat("a", 64), "continuation"),
	}}
	for _, key := range issuerCoreCheckpointStages {
		j.Entries = append(j.Entries, completed(key))
	}
	for _, key := range issuerMQTTCheckpointStages {
		j.Entries = append(j.Entries, completed(key))
	}
	for _, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-original", "pki-provision-original", "pki-import-original"} {
		j.Entries = append(j.Entries, completed(key))
	}
	j.Entries = append(j.Entries, pending("kubernetes/mqtt-root-trust", "mqtt-root-trust"))
	if profile, _ := createRecoveryCheckpointProfile(j); profile != issuerMQTTCustodyCAProfile {
		t.Fatalf("original rollout must choose the CA repair, got %q", profile)
	}
	j.Entries = append(j.Entries, pending("kubernetes/"+mqttCustodyCAStage, mqttCustodyCAStage))
	if profile, _ := createRecoveryCheckpointProfile(j); profile != issuerMQTTLogTrustProfile {
		t.Fatalf("interrupted CA repair must choose the Log Ingester repair, got %q", profile)
	}
	j.Entries = append(j.Entries, pending("kubernetes/foreign", "foreign"))
	if profile, _ := createRecoveryCheckpointProfile(j); profile != "" {
		t.Fatalf("unexpected foreign stage must block qualification, got %q", profile)
	}
}

func TestIssuerEnrollmentForwardPlanPreservesOriginalImagesAndReplacesOnlyPublishedRoles(t *testing.T) {
	api := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:"
	emqx := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:"
	openbao := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:"
	old, newDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	roles := map[string]string{"LKE_VIDEO_CLOUD_IMAGE": api + old, "LKE_MQTT_PKI_IMAGE": emqx + old, "LKE_OPENBAO_PKI_IMAGE": openbao + old, "LKE_ACCOUNT_MANAGER_IMAGE": "other@sha256:" + old}
	original := environmentCreatePlan{Bootstrap: environmentCreateBootstrapPlan{Image: api + old, Images: roles}, Release: environmentRelease{Images: map[string]environmentReleaseImage{}}}
	for role, ref := range roles {
		original.Release.Images[role] = environmentReleaseImage{Reference: ref}
	}
	published := map[string]environmentReleaseImage{
		"video-cloud-api":         {Reference: api + newDigest, SourceRevision: strings.Repeat("c", 40)},
		"video-cloud-emqx-pki":    {Reference: emqx + newDigest, SourceRevision: strings.Repeat("c", 40)},
		"video-cloud-openbao-pki": {Reference: openbao + newDigest, SourceRevision: strings.Repeat("c", 40)},
	}
	before := deploymentplan.Digest(original)
	plan, replacements, err := issuerEnrollmentForwardPlan(original, published)
	if err != nil || before != deploymentplan.Digest(original) || len(replacements) != 3 ||
		plan.Bootstrap.Image != api+newDigest || plan.Release.Images["LKE_MQTT_PKI_IMAGE"].Reference != emqx+newDigest ||
		plan.Bootstrap.Images["LKE_OPENBAO_PKI_IMAGE"] != openbao+newDigest || plan.Release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference != roles["LKE_ACCOUNT_MANAGER_IMAGE"] {
		t.Fatalf("forward release did not isolate exact image replacements: %v", err)
	}
	published["video-cloud-api"] = published["video-cloud-openbao-pki"]
	if _, _, err := issuerEnrollmentForwardPlan(original, published); err == nil {
		t.Fatal("foreign repository accepted for API repair")
	}
}

func TestIssuerLeaseRepairPatchFencesUIDAndVersionAndOnlyAddsLabels(t *testing.T) {
	actual := deploymentplan.Object{
		"metadata": deploymentplan.Object{"uid": "lease-uid", "resourceVersion": "42", "labels": deploymentplan.Object{"unrelated": "keep"}},
		"spec":     deploymentplan.Object{"leaseTransitions": 1},
	}
	labels := deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": "video-cloud-dev"}
	raw := issuerLeaseLabelPatch(actual, labels)
	var ops []struct {
		Op, Path string
		Value    any
	}
	if err := json.Unmarshal(raw, &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || ops[0].Op != "test" || ops[0].Path != "/metadata/uid" || ops[0].Value != "lease-uid" ||
		ops[1].Op != "test" || ops[1].Path != "/metadata/resourceVersion" || ops[1].Value != "42" ||
		ops[2].Op != "add" || ops[2].Path != "/metadata/labels" {
		t.Fatalf("unexpected lease repair patch %s", raw)
	}
	want := deploymentplan.Object{"unrelated": "keep", "app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": "video-cloud-dev"}
	if deploymentplan.Digest(ops[2].Value) != deploymentplan.Digest(want) || deploymentplan.Map(deploymentplan.Map(actual["metadata"])["labels"])["rtk.realtek.com/stack"] != nil {
		t.Fatal("patch failed to preserve existing labels or mutated the original observation")
	}
}

func TestIssuerLeaseRepairPersistsIntentAndRequiresExactReadback(t *testing.T) {
	for _, mode := range []string{"success", "changed-before-write", "existing-intent", "uncertain-write", "unexpected-readback"} {
		t.Run(mode, func(t *testing.T) {
			before := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-lease", "resourceVersion": "42"}}
			after := deploymentplan.Clone(before)
			deploymentplan.Map(after["metadata"])["labels"] = deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud"}
			p := issuerLeasePatchPlan{Resource: "Lease/isolated/pki-state", BeforeSHA: deploymentplan.Digest(before), AfterSHA: deploymentplan.Digest(deploymentplan.Projection(after))}
			var calls []string
			read := func(context.Context, string) (deploymentplan.Object, error) {
				calls = append(calls, "read")
				if mode == "changed-before-write" || len(calls) > 1 && mode != "unexpected-readback" {
					return after, nil
				}
				return before, nil
			}
			intent := func(context.Context) error {
				calls = append(calls, "intent")
				if mode == "existing-intent" {
					return errors.New("prior uncertain write")
				}
				return nil
			}
			patch := func(context.Context) (deploymentplan.Object, error) {
				calls = append(calls, "patch")
				if mode == "uncertain-write" {
					return nil, errors.New("API response lost")
				}
				return after, nil
			}
			complete := func(context.Context) error { calls = append(calls, "complete"); return nil }
			err := p.apply(t.Context(), read, intent, patch, complete)
			want := map[string]string{"success": "read,intent,patch,read,complete", "changed-before-write": "read", "existing-intent": "read,intent", "uncertain-write": "read,intent,patch", "unexpected-readback": "read,intent,patch,read"}
			if (err == nil) != (mode == "success") || strings.Join(calls, ",") != want[mode] {
				t.Fatalf("unexpected repair outcome %v, calls %v", err, calls)
			}
		})
	}
}

func TestIssuerLeaseEnrollmentRecoveryOnlyTrustsOriginalCompletedWrites(t *testing.T) {
	for _, mode := range []string{"original", "replacement-job", "pending-job", "replacement-lease", "active-holder", "conflicting-label", "unknown-write", "role-drift"} {
		t.Run(mode, func(t *testing.T) {
			const ns = "video-cloud-dev-video-cloud"
			leaseName := "pki-state-" + strings.Repeat("a", 32)
			plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped"}}
			journal := deploymentplan.Journal{Owners: map[string]string{}}
			live := map[string]deploymentplan.Object{}
			for _, fixture := range []struct{ kind, namespace, name, action string }{
				{"Lease", ns, leaseName, "create"},
				{"Role", ns, leaseName, "create"},
				{"RoleBinding", ns, leaseName, "create"},
				{"Job", ns, "pki-enroll-certissuer-server-123456789abc", "create"},
				{"ServiceAccount", ns, "certissuer", "retain"},
				{"ServiceAccount", ns, "pki-state-" + strings.Repeat("b", 32), "create"},
				{"NetworkPolicy", "video-cloud-dev-secrets", "allow-openbao-workload-custody", "retain"},
			} {
				meta := deploymentplan.Object{"name": fixture.name, "namespace": fixture.namespace, "labels": deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": plan.Stack}}
				body := deploymentplan.Object{"kind": fixture.kind, "metadata": meta}
				id := deploymentplan.ID(body)
				resource := deploymentplan.Resource{ID: id, Action: fixture.action, Phase: "bootstrap", Desired: body}
				actual := deploymentplan.Clone(body)
				actualMeta := deploymentplan.Map(actual["metadata"])
				uid := "11111111-1111-4111-8111-111111111111"
				actualMeta["uid"], actualMeta["resourceVersion"] = uid, "42"
				journal.Owners[id] = uid
				journal.Completed = append(journal.Completed, id)
				switch fixture.kind {
				case "Lease":
					resource.Desired["apiVersion"] = "coordination.k8s.io/v1"
					actual["apiVersion"] = "coordination.k8s.io/v1"
					delete(actualMeta, "labels")
					actual["spec"] = deploymentplan.Object{"leaseTransitions": 1}
					journal.Failed = id
					switch mode {
					case "replacement-lease":
						actualMeta["uid"] = "replaced"
					case "active-holder":
						deploymentplan.Map(actual["spec"])["holderIdentity"] = "new-process"
					case "conflicting-label":
						actualMeta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "another-stack"}
					}
				case "Job":
					actual["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
					if mode == "pending-job" {
						delete(actual, "status")
					}
					if mode == "replacement-job" {
						actualMeta["uid"] = "replaced"
					}
				case "Role":
					if mode == "role-drift" {
						delete(actualMeta, "labels")
					}
				}
				if fixture.action == "retain" {
					resource.UID = uid
					resource.Baseline = deploymentplan.Projection(actual)
				}
				live[id] = actual
				plan.Resources = append(plan.Resources, resource)
			}
			journal.PlanSHA256 = plan.SHA256()
			if mode == "unknown-write" {
				journal.InFlight = journal.Failed
			}
			check := issuerLeaseEnrollmentRecovery{plan: plan, journal: journal}
			err := check.verify(t.Context(), func(_ context.Context, id string) (deploymentplan.Object, error) { return live[id], nil })
			if (err == nil) != (mode == "original") {
				t.Fatalf("original recovery verification error = %v", err)
			}
			if mode == "original" {
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "deployment", "rollback", plan.SHA256())
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for name, v := range map[string]any{"plan.json": plan, "journal.json": journal} {
					raw, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				original := live[journal.Failed]
				repaired := deploymentplan.Clone(original)
				deploymentplan.Map(repaired["metadata"])["labels"] = deploymentplan.Map(plan.Resources[0].Desired["metadata"])["labels"]
				backend := issuerLeaseFixtureBackend{objects: live}
				if err := check.admitAfterLabelRepair(t.Context(), backend, root, original, repaired); err != nil {
					t.Fatal("complete original write set did not qualify without another write", err)
				}
				concurrent := deploymentplan.Clone(original)
				deploymentplan.Map(concurrent["metadata"])["resourceVersion"] = "43"
				live[journal.Failed] = concurrent
				if err := check.admitAfterLabelRepair(t.Context(), backend, root, original, repaired); err == nil {
					t.Fatal("dry-run evidence survived a live resource version change")
				}
				live[journal.Failed] = repaired
				fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return ctx.Err() }
				acceptanceRan := false
				accept := func(ctx context.Context, selected *deploymentplan.Plan) error {
					acceptanceRan = true
					return selected.Verify(ctx, backend)
				}
				if err := plan.ExecuteResumable(t.Context(), backend, plan.SHA256(), root, fence, fence, accept); err != nil {
					t.Fatal("original child failed to accept after label correction", err)
				}
				accepted, ok, err := plan.ReadExecution(root)
				if err != nil || !ok || !acceptanceRan || !accepted.Accepted || len(accepted.Completed) != 7 ||
					deploymentplan.Digest(accepted.Owners) != deploymentplan.Digest(journal.Owners) {
					t.Fatalf("original owners were not accepted: %v", err)
				}
			}
		})
	}
}

type issuerLeaseFixtureBackend struct {
	objects map[string]deploymentplan.Object
}

func (b issuerLeaseFixtureBackend) Read(_ context.Context, id string) (deploymentplan.Object, error) {
	return b.objects[id], nil
}

func (issuerLeaseFixtureBackend) Wait(context.Context, deploymentplan.Resource) error { return nil }

func (issuerLeaseFixtureBackend) Write(context.Context, string, deploymentplan.Object, bool) (deploymentplan.Object, error) {
	return nil, errors.New("qualification attempted to replay an original write")
}

func (issuerLeaseFixtureBackend) AcquireExecution(_ context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	return issuerLeaseFixtureLock{deploymentplan.LeaseOwner{
		ResourceID: "Lease/" + p.Stack + "-video-cloud/rtk-deployment-execution",
		UID:        "22222222-2222-4222-8222-222222222222", Holder: p.SHA256() + ":fixture",
	}}, nil
}

type issuerLeaseFixtureLock struct{ owner deploymentplan.LeaseOwner }

func (l issuerLeaseFixtureLock) Owner() deploymentplan.LeaseOwner { return l.owner }
func (issuerLeaseFixtureLock) Check(ctx context.Context) error    { return ctx.Err() }
func (issuerLeaseFixtureLock) Release(ctx context.Context) error  { return ctx.Err() }
