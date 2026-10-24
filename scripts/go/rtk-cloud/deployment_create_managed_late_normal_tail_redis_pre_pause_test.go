package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

// A genuine isolated parent/old90/180/360 chain plus one accepted claim and
// fully applied helper whose only failure is the original workload acceptance.
func redisPrePauseFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createServiceSessionRecovery, createFoundationRecoveryArtifact) {
	t.Helper()
	store, e, r, a, _ := normalTailExtensionFixture(t)
	a.Plan.Plan.ConfigurationSHA256 = strings.Repeat("1", 64)
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": e.Journal.Stack}
	defs, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(env))
	if err != nil || len(defs) != 1 {
		t.Fatal(err)
	}
	original := defs[0].Object
	image := "valkey/valkey@sha256:" + strings.Repeat("a", 64)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["image"] = image
	d := &createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, ParentSHA: e.Journal.PlanSHA256, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), NormalAuthoritySHA: deploymentplan.Digest(r.normalTail.Authority), SourceLeases: 149, AcceptedPlanSHA: strings.Repeat("7", 64), ResourceID: deploymentplan.ID(original), UID: "11111111-1111-4111-8111-111111111111", Image: image, Original: deploymentplan.Resource{ID: deploymentplan.ID(original), UID: "11111111-1111-4111-8111-111111111111", Desired: original}, OriginalVolumes: map[string]environmentCreateVolumeOwner{}, source: e.Journal, SupplementIntentSHA: managedLateNormalTailSupplementIntent(r), ExtensionIntentSHA: managedLateNormalTailExtensionIntent(r)}
	for i := 1; i <= 18; i++ {
		o := environmentCreateVolumeOwner{NativeID: fmt.Sprint(i), PVName: fmt.Sprint("pv-", i), PVUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i), PVCName: fmt.Sprint("claim-", i), PVCUID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", i), Namespace: e.Journal.Stack + "-video-cloud"}
		d.OriginalVolumes["PersistentVolumeClaim/"+o.Namespace+"/"+o.PVCName] = o
	}
	d.OriginalVolumesSHA = deploymentplan.Digest(d.OriginalVolumes)
	d.Claim, d.Helper, d.Switch, err = managedLateRedisRecipes(d, deploymentConfig{Values: map[string]string{"REDIS_DATA_STORAGE": "5Gi", "DURABLE_DATA_STORAGE_CLASS": "selected-retain"}})
	if err != nil {
		t.Fatal(err)
	}
	a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] = managedLateRedisDurableDigest(d)
	a.Plan.Materials[managedLateRedisDurableMaterial+"original-volumes"] = d.OriginalVolumesSHA
	resealNormalTailExtensionFixture(r, &a)
	d.QualifiedReview = a.Qualification.PlanSHA256
	saveNormalTailExtensionQualification(t, store, r, a)
	if err := store.write(filepath.Join("deployment/create/openbao-recovery", d.QualifiedReview, managedLateRedisDurableFilename), mustProviderRecoveryJSON(d), false); err != nil {
		t.Fatal(err)
	}
	phase, err := newManagedLateNormalTailExtensionAuthority(r, a, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)), time.Now().UTC().Add(360*time.Minute), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
		t.Fatal(err)
	}
	r.normalSupplement.Extension.Authority = phase
	saved := managedLateRedisDurableSaved{Descriptor: *d, Source: e.Journal, QualificationSHA: deploymentplan.Digest(a), QualifiedBytesSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)), SupplementAuthoritySHA: deploymentplan.Digest(r.normalSupplement.Authority), ExtensionAuthoritySHA: deploymentplan.Digest(phase)}
	if _, err := writeManagedLateRedisDurableSource(store, saved); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	outer := mustProviderRecoveryJSON(deploymentplan.Object{"descriptor_sha256": managedLateRedisDurableDigest(d), "review_sha256": d.QualifiedReview, "authority_sha256": d.NormalAuthoritySHA})
	if err := e.BeginEntry(managedLateRedisDurableKey, "redis-durable", outer, now); err != nil {
		t.Fatal(err)
	}
	add := func(stage string, body deploymentplan.Object, uid, leaseUID string, accepted bool) (*deploymentplan.Plan, deploymentplan.Journal) {
		definitions := []deploymentplan.Definition{{Object: body, Phase: "workload"}}
		if stage == "normal-redis-durable-claim" {
			definitions[0].Phase = "storage"
		}
		inputs := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: a.Plan.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(a.Plan.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions)}
		plan, err := deploymentplan.Compile(inputs, definitions, nil)
		if err != nil {
			t.Fatal(err)
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.BeginEntry("kubernetes/"+stage, stage, intent, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		lease := deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: leaseUID, Holder: plan.SHA256() + ":owned"}
		e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, lease)
		if err := e.save(); err != nil {
			t.Fatal(err)
		}
		j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Completed: []string{plan.Resources[0].ID}, Owners: map[string]string{plan.Resources[0].ID: uid}, Accepted: accepted, AcceptanceStarted: true}
		if !accepted {
			j.Failed = "deployment.post-deploy.workloads"
		}
		for name, value := range map[string]any{"plan.json": plan, "journal.json": j} {
			if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
				t.Fatal(err)
			}
		}
		if accepted {
			if err := e.RecordReceipt("kubernetes/"+stage, mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": j.Owners})); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("kubernetes/"+stage, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		return plan, j
	}
	claim, cj := add("normal-redis-durable-claim", d.Claim, "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", true)
	volume := environmentCreateVolumeOwner{NativeID: "99", PVName: "pvc-original-redis", PVUID: "44444444-4444-4444-8444-444444444444", PVCName: "redis-data", PVCUID: cj.Owners[claim.Resources[0].ID], Namespace: e.Journal.Stack + "-platform"}
	if err := store.write(filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "volume.json"), mustProviderRecoveryJSON(volume), false); err != nil {
		t.Fatal(err)
	}
	add("normal-redis-durable-target", d.Helper, "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", false)
	proof, _, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		t.Fatal("genuine437 fixture", err)
	}
	r.normalTail.CurrentSHA = deploymentplan.Digest(e.Journal)
	d.Resume = proof
	r.redisDurable = d
	a.Plan.ParentJournalSHA = deploymentplan.Digest(e.Journal)
	a.Plan.Materials[managedLateRedisDurableMaterial+"resume"] = deploymentplan.Digest(proof)
	a.QualifiedAt = time.Now().UTC().Add(-time.Second)
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	a.Plan.CheckerSHA, err = environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	resealNormalTailExtensionFixture(r, &a)
	saveNormalTailExtensionQualification(t, store, r, a)
	if err := store.write(filepath.Join("deployment/create/openbao-recovery", a.Qualification.PlanSHA256, managedLateRedisDurableFilename), mustProviderRecoveryJSON(d), false); err != nil {
		t.Fatal(err)
	}
	return store, e, r, a
}

func TestManagedLateNormalTailRedisPrePauseEarlyAndExistingAuthority(t *testing.T) {
	store, e, r, a := redisPrePauseFixture(t)
	before, _ := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256)
	old, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	base, _ := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256)
	if !managedLateNormalTailSelected(e.Journal) || !managedLateNormalTailControllerSelected(e.Journal) {
		t.Fatal("exact437 routing refused")
	}
	historical, err := managedLateNormalTailHistoricalSource(store, e)
	if err != nil || len(historical.Journal.Entries) != 434 || historical == e || len(e.Journal.Entries) != 437 {
		t.Fatal("strong437 pure434 projection", err)
	}
	if historical.Check(context.Background()) == nil {
		t.Fatal("pure434 view gained global current authority")
	}
	if err := prepareManagedLateNormalTailSupplement(context.Background(), deploymentConfig{Environment: "dev"}, store, e, r); err != nil {
		t.Fatal("existing chain prepare437", err)
	}
	ctx, cancel, err := managedLateNormalTailSupplementCallerContext(context.Background(), store, e, 360*time.Minute, true)
	if err != nil {
		t.Fatal("437 bounded caller", err)
	}
	defer cancel()
	end, _ := ctx.Deadline()
	if !end.Equal(r.normalSupplement.Extension.Authority.Deadline) {
		t.Fatal("new caller reset original end")
	}
	raw := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
	bounded, close, err := admitManagedLateNormalTailExtension(ctx, store, e, r, a, raw)
	if err != nil {
		t.Fatal("fresh437 same grant", err)
	}
	defer close()
	actual, _ := bounded.Deadline()
	if !actual.Equal(end) {
		t.Fatal("same grant deadline changed")
	}
	after, _ := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256)
	afterOld, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	afterBase, _ := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256)
	if !bytes.Equal(before, after) || !bytes.Equal(old, afterOld) || !bytes.Equal(base, afterBase) {
		t.Fatal("437 reuse rewrote grant history")
	}
	if _, err := newManagedLateNormalTailExtensionAuthority(r, a, raw, time.Now().Add(360*time.Minute), time.Now().UTC()); err == nil {
		t.Fatal("437 constructed a new grant")
	}
}
func TestManagedLateNormalTailRedisPrePauseFreshGOAndScopeRefusals(t *testing.T) {
	store, e, r, a := redisPrePauseFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, mode := range []string{"failed-gate", "old434-source", "missing-resume", "wrong-resume", "wrong-checker", "expired"} {
		t.Run(mode, func(t *testing.T) {
			q := a
			q.Plan.Materials = map[string]string{}
			for k, v := range a.Plan.Materials {
				q.Plan.Materials[k] = v
			}
			q.Qualification.Gates = slices.Clone(a.Qualification.Gates)
			switch mode {
			case "failed-gate":
				q.Qualification.Gates[0].Status = "BLOCKED"
			case "old434-source":
				q.Plan.ParentJournalSHA = r.normalSupplement.Extension.Approval.SourceSHA
			case "missing-resume":
				delete(q.Plan.Materials, managedLateRedisDurableMaterial+"resume")
			case "wrong-resume":
				q.Plan.Materials[managedLateRedisDurableMaterial+"resume"] = strings.Repeat("f", 64)
			case "wrong-checker":
				q.Plan.CheckerSHA = strings.Repeat("f", 64)
			case "expired":
				q.ValidUntil = time.Now().UTC()
			}
			q.Qualification.PlanSHA256 = deploymentplan.Digest(q.Plan)
			for i := range q.Qualification.Gates {
				q.Qualification.Gates[i].EvidenceSHA256 = q.Qualification.PlanSHA256
			}
			if _, _, err := admitManagedLateNormalTailExtension(ctx, store, e, r, q, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(q))); err == nil {
				t.Fatal("unqualified437 retry accepted")
			}
		})
	}
	original := e.Journal
	for _, mode := range []string{"foreign-owner", "unknown-suffix", "pending-claim"} {
		t.Run(mode, func(t *testing.T) {
			j := original
			j.Entries = slices.Clone(original.Entries)
			switch mode {
			case "foreign-owner":
				j.OwnerToken = "foreign"
			case "unknown-suffix":
				j.Entries[436].Key = "kubernetes/foreign"
			case "pending-claim":
				j.Entries[435].State = "pending"
			}
			e.Journal = j
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := managedLateNormalTailHistoricalSource(store, e); err == nil {
				t.Fatal("unproved437 ancestry accepted")
			}
			e.Journal = original
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
