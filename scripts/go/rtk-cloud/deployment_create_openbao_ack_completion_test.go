package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createOpenBaoACKCompletionFixture struct {
	image           *createOpenBaoACKImageFixture
	original        environmentRelease
	ack             *createOpenBaoACKRecovery
	native          deploymentplan.Object
	owner           environmentCreateOwner
	before, current environmentCreateJournal
	artifact        createFoundationRecoveryArtifact
	review          string
}

func newCreateOpenBaoACKCompletionFixture(t *testing.T) *createOpenBaoACKCompletionFixture {
	t.Helper()
	f := &createOpenBaoACKCompletionFixture{image: newCreateOpenBaoACKImageFixture(t)}
	f.image.cfg.Values = map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	f.original = environmentRelease{Schema: "rtk-cloud.release/v1", Environment: "dev", Stack: "video-cloud-dev", WorkspaceRevision: strings.Repeat("1", 40), Images: map[string]environmentReleaseImage{
		"LKE_OPENBAO_PKI_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("2", 64), SourceRevision: strings.Repeat("3", 40)},
		"LKE_VIDEO_CLOUD_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("4", 64), SourceRevision: strings.Repeat("3", 40)},
	}}
	image, selection, manifest, err := loadCreateOpenBaoACKRepairImageWithCommand(context.Background(), f.image.cfg, f.image.store, f.image.parent, f.image.command(t))
	if err != nil {
		t.Fatal(err)
	}
	uid := "11111111-1111-4111-8111-111111111111"
	body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": "video-cloud-dev-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{
		"shareProcessNamespace": true,
		"initContainers":        []any{deploymentplan.Object{"name": "openbao-pki-install", "image": f.original.Images["LKE_OPENBAO_PKI_IMAGE"].Reference}},
		"containers":            []any{deploymentplan.Object{"name": "openbao", "image": "ghcr.io/openbao/openbao@sha256:" + strings.Repeat("5", 64), "command": []any{"/bin/sh", "-ec", `umask 077; printf '%s\n' "$$" > /run/openbao-pki/private/openbao.pid; exec bao server -config=/openbao/config/file.hcl`}}, deploymentplan.Object{"name": "openbao-pki", "image": f.original.Images["LKE_OPENBAO_PKI_IMAGE"].Reference}},
	}}}}
	originalPlan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ReleaseSHA256: deploymentplan.Digest(f.original)}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(body), UID: uid, Phase: "workload", Action: "update", Desired: body}}}
	for n := 0; n < 6; n++ {
		object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": fmt.Sprintf("original-%d", n), "namespace": "video-cloud-dev-secrets"}, "data": deploymentplan.Object{"public": "retained"}}
		originalPlan.Resources = append(originalPlan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(object), UID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", n), Phase: "configuration", Action: "retain", Desired: object})
	}
	originalJournal := deploymentplan.Journal{PlanSHA256: originalPlan.SHA256(), Accepted: true, Owners: map[string]string{}}
	for _, r := range originalPlan.Resources {
		originalJournal.Completed = append(originalJournal.Completed, r.ID)
		originalJournal.Owners[r.ID] = r.UID
	}
	f.ack = &createOpenBaoACKRecovery{Image: image, SelectionSHA: selection, ManifestSHA: manifest, OriginalPlanSHA: originalPlan.SHA256(), OriginalJournalSHA: deploymentplan.Digest(originalJournal), SourcePodUID: "33333333-3333-4333-8333-333333333333", Original: originalPlan.Resources[0], SourceProcess: deploymentplan.Object{"pid": "24", "start_time": "13380439", "executable_sha256": strings.Repeat("6", 64), "executable": "/bin/bao", "uid": "100", "pid_marker_sha256": environmentPreflightBytesSHA256([]byte("$\n"))}}
	defs, err := createOpenBaoACKDefinitions(f.ack)
	if err != nil {
		t.Fatal(err)
	}
	f.ack.Candidate = &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ReleaseSHA256: deploymentplan.Digest(f.original), BindingsSHA256: environmentCreateStageBindingsSHA("openbao-host-ack-forward", defs)}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(body), UID: uid, Phase: "workload", Action: "update", Desired: defs[0].Object, Baseline: deploymentplan.Clone(body)}}}
	now := time.Now().UTC().Add(-time.Minute)
	f.owner = environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: f.image.parent, OwnerToken: strings.Repeat("7", 64), Journal: filepath.Join("deployment/create/executions", f.image.parent, "execution-isolated/journal.json"), StartedAt: now.Add(-time.Minute)}
	f.before = environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: f.owner.Environment, Stack: f.owner.Stack, PlanSHA256: f.owner.PlanSHA256, OwnerToken: f.owner.OwnerToken, AdmittedAt: f.owner.StartedAt, ArtifactSHA256: strings.Repeat("8", 64), Entries: []environmentCreateJournalEntry{{Key: "original-completed", Phase: "bootstrap", State: "completed", IntentSHA256: strings.Repeat("9", 64), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"original": "native"}), StartedAt: now.Add(-time.Second), CompletedAt: now}}}
	materials := map[string]string{}
	for key, digest := range map[string]string{"selection": selection, "manifest": manifest, "image": deploymentplan.Digest(image), "original-release": deploymentplan.Digest(f.original), "original-plan": f.ack.OriginalPlanSHA, "original-journal": f.ack.OriginalJournalSHA, "candidate-plan": f.ack.Candidate.SHA256(), "source-pod": deploymentplan.Digest(f.ack.SourcePodUID), "source-process": deploymentplan.Digest(f.ack.SourceProcess), "source": deploymentplan.Digest(f.ack)} {
		materials["accepted-service-core-openbao-ack-"+key] = digest
	}
	preview := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ReleaseSHA256: deploymentplan.Digest(f.original), BindingsSHA256: strings.Repeat("b", 64)}}
	for n := 0; n < 26; n++ {
		object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": fmt.Sprintf("app-owner-%d", n), "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"public": "qualified"}}
		preview.Resources = append(preview.Resources, deploymentplan.Resource{ID: deploymentplan.ID(object), Phase: "configuration", Action: "create", Desired: object})
	}
	materials["accepted-service-core-managed-application-forward-plan"] = preview.SHA256()
	f.artifact = createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1", ParentSHA: f.image.parent, ParentOwnerSHA: deploymentplan.Digest(f.owner), ParentJournalSHA: deploymentplan.Digest(f.before), Materials: materials, Plan: environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", Release: f.original}}, QualifiedAt: now, ValidUntil: now.Add(time.Minute)}
	f.review = deploymentplan.Digest(f.artifact.Plan)
	f.artifact.Qualification = deploymentplan.Qualification{PlanSHA256: f.review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		f.artifact.Qualification.Gates = append(f.artifact.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: f.review})
	}
	f.current = f.before
	f.current.Entries = append([]environmentCreateJournalEntry{}, f.before.Entries...)
	admission := deploymentplan.Object{"review_sha256": f.review, "qualification_sha256": deploymentplan.Digest(f.artifact), "original_parent_journal_sha256": f.artifact.Plan.ParentJournalSHA}
	f.current.Entries = append(f.current.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + f.review, Phase: "recovery", State: "completed", IntentSHA256: activeTransportJournalDigest(admission), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": f.review}), StartedAt: now, CompletedAt: now})
	child := *f.ack.Candidate
	child.BindingsSHA256 = environmentCreateStageBindingsSHA("openbao-host-ack-forward-"+f.review, defs)
	f.addStage(t, "openbao-host-ack-forward-"+f.review, &child, now)
	f.addStage(t, "managed-application-owners-forward-"+f.review, preview, now)
	subjects, _ := environmentCreateBootstrapSubjects(f.image.cfg)
	f.native = deploymentplan.Object{"parent": f.image.parent, "review": f.review, "session_id": "44444444-4444-4444-8444-444444444444", "statefulset_uid": uid, "pod_uid": "55555555-5555-4555-8555-555555555555", "child_plan_sha256": child.SHA256(), "subjects": subjects, "installed_subjects": subjects}
	for _, key := range []string{"process_sha256", "clients_sha256", "servers_sha256", "volume_sha256", "session_sha256", "original_init_sha256", "audit_sha256"} {
		f.native[key] = strings.Repeat("c", 64)
	}
	f.current.Entries = append(f.current.Entries, environmentCreateJournalEntry{Key: "openbao-host-ack-verified-" + f.review, Phase: "openbao-host-ack-verification", State: "completed", IntentSHA256: activeTransportJournalDigest(f.native), Receipt: mustProviderRecoveryJSON(f.native), StartedAt: now, CompletedAt: now})
	base := filepath.Join("deployment/create/openbao-recovery", f.review)
	for path, value := range map[string]any{filepath.Join(base, "qualification.json"): f.artifact, filepath.Join(base, "source-owner.json"): f.owner, filepath.Join(base, "source-journal.json"): f.before, filepath.Join(base, "openbao-host-ack-source.json"): f.ack, filepath.Join(base, "openbao-root-trust-plan.json"): preview, filepath.Join("deployment/rollback", originalPlan.SHA256(), "plan.json"): originalPlan, filepath.Join("deployment/rollback", originalPlan.SHA256(), "journal.json"): originalJournal, "deployment/.execution-active/owner.json": f.owner, f.owner.Journal: f.current} {
		f.write(t, path, value)
	}
	return f
}

func (f *createOpenBaoACKCompletionFixture) write(t *testing.T, path string, value any) {
	t.Helper()
	if err := f.image.store.write(path, mustProviderRecoveryJSON(value), true); err != nil {
		t.Fatal(err)
	}
}
func (f *createOpenBaoACKCompletionFixture) addStage(t *testing.T, stage string, plan *deploymentplan.Plan, at time.Time) {
	t.Helper()
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, Owners: map[string]string{}}
	for n, r := range plan.Resources {
		uid := r.UID
		if uid == "" {
			uid = fmt.Sprintf("%08d-6666-4666-8666-666666666666", n)
		}
		j.Completed = append(j.Completed, r.ID)
		j.Owners[r.ID] = uid
	}
	f.write(t, filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
	f.write(t, filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), j)
	intent, _ := environmentCreateKubernetesStageIntent(plan)
	f.current.Entries = append(f.current.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "completed", IntentSHA256: activeTransportJournalDigest(jsonRawObject(intent)), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": j.Owners}), StartedAt: at, CompletedAt: at})
}
func (f *createOpenBaoACKCompletionFixture) publish(t *testing.T) error {
	t.Helper()
	return publishCreateOpenBaoACKCompletionWithCommand(context.Background(), f.image.cfg, f.image.store, f.image.parent, f.review, f.original, f.ack, f.native, f.image.command(t))
}
func (f *createOpenBaoACKCompletionFixture) effective(t *testing.T) (environmentRelease, error) {
	t.Helper()
	return effectiveCreateOpenBaoACKReleaseWithCommand(context.Background(), f.image.cfg, f.image.store, f.image.parent, f.original, f.image.command(t))
}

func TestCreateOpenBaoACKCompletionOverlaysOnlyRoleAndSurvivesOwnerArchive(t *testing.T) {
	f := newCreateOpenBaoACKCompletionFixture(t)
	before := deploymentplan.Digest(f.original)
	if err := f.publish(t); err != nil {
		t.Fatal(err)
	}
	if err := f.publish(t); err != nil {
		t.Fatal("completion publication is not repeat-safe", err)
	}
	got, err := f.effective(t)
	if err != nil || got.Images["LKE_OPENBAO_PKI_IMAGE"] != f.ack.Image || deploymentplan.Digest(f.original) != before {
		t.Fatal("completed overlay changed original release or lost formal role", err)
	}
	got.Images["LKE_OPENBAO_PKI_IMAGE"] = f.original.Images["LKE_OPENBAO_PKI_IMAGE"]
	if deploymentplan.Digest(got) != before {
		t.Fatal("completion changed an unrelated release field")
	}
	for _, relative := range []string{createOpenBaoACKCompletionPath(f.image.parent), filepath.Join("deployment/create/openbao-host-ack", f.review, "completed.json")} {
		info, err := os.Stat(filepath.Join(f.image.store.Root, relative))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("completion is not private", err)
		}
	}
	f.current.Entries = append(f.current.Entries, environmentCreateJournalEntry{Key: "later-finalized-native-stage", Phase: "bootstrap", State: "completed", IntentSHA256: strings.Repeat("d", 64), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"later": "genuine"}), StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()})
	f.current.CompletedAt = time.Now().UTC()
	f.write(t, f.owner.Journal, f.current)
	archive := filepath.Join(filepath.Dir(f.owner.Journal), "owner/owner.json")
	f.write(t, archive, f.owner)
	if err := os.Remove(filepath.Join(f.image.store.Root, "deployment/.execution-active/owner.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.effective(t); err != nil {
		t.Fatal("genuine completed role proof failed after original fence archive", err)
	}
}

func TestCreateOpenBaoACKCompletionAbsentPointerDoesNotSelectPreparedImage(t *testing.T) {
	f := newCreateOpenBaoACKCompletionFixture(t)
	f.image.calls = 0
	before := createOpenBaoACKStoreSnapshot(t, f.image.store)
	got, err := f.effective(t)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(f.original) || f.image.calls != 0 || !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.image.store)) {
		t.Fatal("prepared selection activated an absent completion or wrote evidence", err)
	}
}

func TestCreateOpenBaoACKCompletionRejectsPartialAndChangedProvenance(t *testing.T) {
	for _, problem := range []string{"malformed-pointer", "missing-mirror", "changed-mirror", "pending-native", "changed-native-receipt", "duplicate-native", "changed-owner", "changed-source", "changed-source-receipt", "half-child", "changed-child-uid", "changed-candidate", "changed-app-preview", "changed-formal-attempt", "private-parent-symlink", "missing-one-ack", "duplicate-ack"} {
		t.Run(problem, func(t *testing.T) {
			f := newCreateOpenBaoACKCompletionFixture(t)
			if err := f.publish(t); err != nil {
				t.Fatal(err)
			}
			last := len(f.current.Entries) - 1
			switch problem {
			case "malformed-pointer":
				f.write(t, createOpenBaoACKCompletionPath(f.image.parent), deploymentplan.Object{"schema": createOpenBaoACKCompletionSchema})
			case "missing-mirror":
				_ = os.Remove(filepath.Join(f.image.store.Root, "deployment/create/openbao-host-ack", f.review, "completed.json"))
			case "changed-mirror":
				f.write(t, filepath.Join("deployment/create/openbao-host-ack", f.review, "completed.json"), deploymentplan.Object{"foreign": "proof"})
			case "pending-native":
				f.current.Entries[last].State = "pending"
				f.current.Entries[last].CompletedAt = time.Time{}
				f.write(t, f.owner.Journal, f.current)
			case "changed-native-receipt":
				f.current.Entries[last].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"fake": "ack"})
				f.write(t, f.owner.Journal, f.current)
			case "duplicate-native":
				f.current.Entries = append(f.current.Entries, f.current.Entries[last])
				f.write(t, f.owner.Journal, f.current)
			case "changed-owner":
				o := f.owner
				o.OwnerToken = "foreign"
				f.write(t, "deployment/.execution-active/owner.json", o)
			case "changed-source":
				f.before.OwnerToken = "foreign"
				f.write(t, filepath.Join("deployment/create/openbao-recovery", f.review, "source-journal.json"), f.before)
			case "changed-source-receipt":
				f.current.Entries[0].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"fake": "original"})
				f.write(t, f.owner.Journal, f.current)
			case "half-child", "changed-child-uid":
				sha := deploymentplan.String(f.native["child_plan_sha256"])
				var j deploymentplan.Journal
				_ = createOpenBaoACKCompletionRead(f.image.store, filepath.Join("deployment/rollback", sha, "journal.json"), &j)
				if problem == "half-child" {
					j.Accepted = false
					j.InFlight = f.ack.Original.ID
				} else {
					j.Owners[f.ack.Original.ID] = "99999999-9999-4999-8999-999999999999"
				}
				f.write(t, filepath.Join("deployment/rollback", sha, "journal.json"), j)
			case "changed-candidate":
				f.ack.Candidate.Resources[0].Desired["unknown"] = "unqualified"
				f.write(t, filepath.Join("deployment/create/openbao-recovery", f.review, "openbao-host-ack-source.json"), f.ack)
			case "changed-app-preview":
				f.write(t, filepath.Join("deployment/create/openbao-recovery", f.review, "openbao-root-trust-plan.json"), deploymentplan.Object{"fake": "apps"})
			case "changed-formal-attempt":
				f.image.runInfo["run_attempt"] = 3
			case "private-parent-symlink":
				dir := filepath.Join(f.image.store.Root, "deployment/create/openbao-host-ack", f.review)
				aside := dir + "-retained"
				if err := os.Rename(dir, aside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(aside, dir); err != nil {
					t.Fatal(err)
				}
			case "missing-one-ack", "duplicate-ack":
				var proof createOpenBaoACKCompletion
				_ = createOpenBaoACKCompletionRead(f.image.store, createOpenBaoACKCompletionPath(f.image.parent), &proof)
				subjects, _ := environmentCreateBootstrapSubjects(f.image.cfg)
				if problem == "missing-one-ack" {
					subjects = subjects[:8]
				} else {
					subjects[8] = subjects[0]
				}
				proof.Native["installed_subjects"] = subjects
				f.write(t, createOpenBaoACKCompletionPath(f.image.parent), proof)
				f.write(t, filepath.Join("deployment/create/openbao-host-ack", f.review, "completed.json"), proof)
			}
			before := createOpenBaoACKStoreSnapshot(t, f.image.store)
			if _, err := f.effective(t); err == nil {
				t.Fatal("partial or changed completed ACK evidence activated the image")
			}
			if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.image.store)) {
				t.Fatal("rejected overlay mutated retained evidence")
			}
		})
	}
}

func createOpenBaoACKInventoryFixture(t *testing.T, f *createOpenBaoACKCompletionFixture) []deploymentplan.Object {
	t.Helper()
	binding := environmentAppRoutingBinding{Schema: "rtk-cloud.native-app-routing/v1", Environment: "dev", Stack: "video-cloud-dev", Mode: "internal-service", CreatePlanSHA256: f.image.parent, DeclarationUID: "88888888-8888-4888-8888-888888888888", ServiceUID: "66666666-6666-4666-8666-666666666666", DeploymentUID: "77777777-7777-4777-8777-777777777777"}
	f.write(t, "pki/app/routing.json", binding)
	declaration := environmentAppRoutingDefinition(binding).Object
	deploymentplan.Map(declaration["metadata"])["uid"] = binding.DeclarationUID
	service := environmentAppInternalServiceDependencies(map[string]string{"CLOUD_STACK_NAME": binding.Stack})[0].Object
	deploymentplan.Map(service["metadata"])["uid"] = binding.ServiceUID
	app := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api-app-pki", "namespace": "video-cloud-dev-video-cloud", "uid": binding.DeploymentUID}}
	bao := deploymentplan.Clone(f.ack.Candidate.Resources[0].Desired)
	deploymentplan.Map(bao["metadata"])["uid"] = f.ack.Original.UID
	return []deploymentplan.Object{declaration, service, app, bao}
}

func TestCreateOpenBaoACKInventoryOverlayRequiresOriginalNativeAllocation(t *testing.T) {
	for _, problem := range []string{"genuine", "absent-completion", "duplicate-declaration", "mutable-declaration", "changed-parent", "changed-private-allocation", "changed-service", "changed-app-owner", "changed-bao-owner", "changed-bao-image", "missing-bao-owner", "unrelated-release"} {
		t.Run(problem, func(t *testing.T) {
			f := newCreateOpenBaoACKCompletionFixture(t)
			if problem != "absent-completion" {
				if err := f.publish(t); err != nil {
					t.Fatal(err)
				}
			}
			live := createOpenBaoACKInventoryFixture(t, f)
			original := f.original
			switch problem {
			case "duplicate-declaration":
				live = append(live, deploymentplan.Clone(live[0]))
			case "mutable-declaration":
				live[0]["immutable"] = false
			case "changed-parent":
				var binding environmentAppRoutingBinding
				_ = decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(live[0]["data"])["binding.json"])), &binding)
				binding.CreatePlanSHA256 = strings.Repeat("f", 64)
				deploymentplan.Map(live[0]["data"])["binding.json"] = string(mustProviderRecoveryJSON(binding))
			case "changed-private-allocation":
				f.write(t, "pki/app/routing.json", deploymentplan.Object{"foreign": "allocation"})
			case "changed-service":
				deploymentplan.Map(live[1]["metadata"])["uid"] = "foreign"
			case "changed-app-owner":
				deploymentplan.Map(live[2]["metadata"])["uid"] = "foreign"
			case "changed-bao-owner":
				deploymentplan.Map(live[3]["metadata"])["uid"] = "foreign"
			case "changed-bao-image":
				managedNamedField(environmentPKIPod(live[3])["containers"], "openbao-pki")["image"] = f.original.Images["LKE_OPENBAO_PKI_IMAGE"].Reference
			case "missing-bao-owner":
				live = live[:3]
			case "unrelated-release":
				original.WorkspaceRevision = strings.Repeat("a", 40)
			}
			before := createOpenBaoACKStoreSnapshot(t, f.image.store)
			got, err := effectiveCreateOpenBaoACKReleaseForInventoryWithCommand(context.Background(), f.image.cfg, f.image.store, original, live, f.image.command(t))
			if problem == "genuine" {
				if err != nil || got.Images["LKE_OPENBAO_PKI_IMAGE"] != f.ack.Image {
					t.Fatal("original native allocation lost completed role", err)
				}
			} else if problem == "absent-completion" {
				if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(original) {
					t.Fatal("prepared image activated through native inventory", err)
				}
			} else if err == nil {
				t.Fatal("changed original generation or unrelated release consumed the role overlay")
			}
			if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.image.store)) {
				t.Fatal("inventory overlay changed retained evidence")
			}
		})
	}
}
