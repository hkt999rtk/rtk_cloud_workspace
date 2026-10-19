package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateRedisPrePauseSourceRetainsAllocationAndRejectsUnknownOutcomes(t *testing.T) {
	store, e, r, _ := redisPrePauseFixture(t)
	before, _ := os.ReadFile(e.JournalPath)
	resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	if resume.CurrentSHA != deploymentplan.Digest(e.Journal) || resume.SourceSHA != saved.Descriptor.SourceSHA || resume.CurrentSHA == resume.SourceSHA {
		t.Fatal("fresh437 confused with immutable434")
	}
	original, volume, err := readManagedLateRedisDurableVolumeAuthority(store, e)
	if err != nil || len(original) != 18 || volume == nil || volume.PVCName != "redis-data" {
		t.Fatal("genuine pre-pause allocation cannot project original18", err)
	}
	d := saved.Descriptor
	oldDigest := managedLateRedisDurableDigest(&d)
	d.Resume = resume
	d.QualifiedReview = "fresh-review"
	if managedLateRedisDurableDigest(&d) != oldDigest || managedLateRedisDurableDigest(r.redisDurable) != oldDigest {
		t.Fatal("resume changed original435/final descriptor")
	}
	childPath := filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "journal.json")
	childRaw, _ := os.ReadFile(childPath)
	var child deploymentplan.Journal
	if decodeEnvironmentJSON(childRaw, &child) != nil {
		t.Fatal("fixture child")
	}
	for _, mode := range []string{"unknown-write", "unapplied", "missing-owner", "foreign-failure", "accepted-without-attempt"} {
		t.Run(mode, func(t *testing.T) {
			j := child
			j.Completed = append([]string(nil), child.Completed...)
			j.Owners = map[string]string{}
			for k, v := range child.Owners {
				j.Owners[k] = v
			}
			switch mode {
			case "unknown-write":
				j.InFlight = j.Completed[0]
			case "unapplied":
				j.Completed = nil
			case "missing-owner":
				j.Owners = map[string]string{}
			case "foreign-failure":
				j.Failed = "deployment.post-deploy.pki"
			case "accepted-without-attempt":
				j.Accepted = true
				j.Failed = ""
			}
			if err := os.WriteFile(childPath, mustProviderRecoveryJSON(j), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readManagedLateRedisPrePauseSource(store, e); err == nil {
				t.Fatal("unknown child outcome admitted")
			}
			if err := os.WriteFile(childPath, childRaw, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	archive := filepath.Join(store.Root, managedLateRedisDurableBase(e.Journal.PlanSHA256), "source.rdb")
	if err := os.WriteFile(archive, []byte("unqualified snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readManagedLateRedisPrePauseSource(store, e); err == nil {
		t.Fatal("snapshot phase replay admitted")
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(e.JournalPath); string(after) != string(before) {
		t.Fatal("read-only source checks changed parent")
	}
}

type redisResumeFixtureBackend struct {
	object deploymentplan.Object
	held   bool
	writes int
	lease  deploymentplan.LeaseOwner
}

func (b *redisResumeFixtureBackend) Read(context.Context, string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(b.object), nil
}
func (b *redisResumeFixtureBackend) Write(context.Context, string, deploymentplan.Object, bool) (deploymentplan.Object, error) {
	b.writes++
	return nil, errors.New("resource replay forbidden")
}
func (b *redisResumeFixtureBackend) Wait(context.Context, deploymentplan.Resource) error { return nil }
func (b *redisResumeFixtureBackend) CheckParentExecution(ctx context.Context, _ *deploymentplan.Plan, _ string) error {
	return ctx.Err()
}
func (b *redisResumeFixtureBackend) RecordParentExecutionLease(context.Context, *deploymentplan.Plan, deploymentplan.LeaseOwner) error {
	return errors.New("mutation lease replay forbidden")
}
func (b *redisResumeFixtureBackend) AcquireExecution(context.Context, *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	b.held = true
	return redisResumeFixtureLease{b}, nil
}

type redisResumeFixtureLease struct{ b *redisResumeFixtureBackend }

func (l redisResumeFixtureLease) Owner() deploymentplan.LeaseOwner { return l.b.lease }
func (l redisResumeFixtureLease) Check(ctx context.Context) error {
	if !l.b.held {
		return errors.New("lease lost")
	}
	return ctx.Err()
}
func (l redisResumeFixtureLease) Release(context.Context) error { l.b.held = false; return nil }

func TestManagedLateRedisPrePauseGenuineAcceptanceCloses437BeforeSnapshot(t *testing.T) {
	store, e, _, _ := redisPrePauseFixture(t)
	resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	var plan deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "plan.json"), &plan) != nil {
		t.Fatal("fixture child")
	}
	live := deploymentplan.Clone(plan.Resources[0].Desired)
	deploymentplan.Map(live["metadata"])["uid"] = resume.HelperUID
	b := &redisResumeFixtureBackend{object: live, lease: deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "77777777-7777-4777-8777-777777777777", Holder: plan.SHA256() + ":acceptance"}}
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	j, err := plan.ResumeAppliedPodAcceptance(t.Context(), b, plan.SHA256(), resume.ChildJournalSHA, store.Root, fence, func(context.Context, *deploymentplan.Plan, map[string]string) error { return nil })
	if err != nil || b.writes != 0 || b.held {
		t.Fatal("acceptance replayed resource or lost completion", err)
	}
	if _, _, err := readManagedLateRedisPrePauseSource(store, e); err != nil {
		t.Fatal("accepted child with pending437 lost genuine proof", err)
	}
	if err := e.RecordReceipt("kubernetes/normal-redis-durable-target", mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": j.Owners})); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("kubernetes/normal-redis-durable-target", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !managedLateRedisPrePauseSelected(e.Journal) {
		t.Fatal("completed437 lost exact transition")
	}
	if _, _, err := readManagedLateRedisPrePauseSource(store, e); err != nil {
		t.Fatal("genuine completed437 cannot reauthenticate", err)
	}
	if _, _, err := readManagedLateRedisDurableVolumeAuthority(store, e); err != nil {
		t.Fatal("completed helper allocation lost18 proof", err)
	}
	if err := e.BeginEntry("normal-redis-durable-snapshot", "redis-durable", mustProviderRecoveryJSON(deploymentplan.Object{"descriptor_sha256": managedLateRedisDurableDigest(&saved.Descriptor), "state_sha256": deploymentplan.Digest("fresh INFO before pause")}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := managedLateRedisDurableSourcePrefix(saved.Source, e.Journal); err != nil {
		t.Fatal("ordinary snapshot438 did not retain genuine helper", err)
	}
	if len(e.Journal.KubernetesLeases) != 151 || e.Journal.Entries[434].State != "pending" {
		t.Fatal("original migration/lease history rewritten")
	}
}
