package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ordinaryPendingHistoryBackend struct {
	freshStageBackend
	waits    int
	denyWait bool
}

func (b *ordinaryPendingHistoryBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	b.waits++
	if b.denyWait {
		return errors.New("read-only history must not require readiness")
	}
	return b.freshStageBackend.Wait(ctx, r)
}

// Both requests and their child journals are produced by the ordinary engine.
// Only the API boundary is in memory; no accepted or failed receipt is invented.
func ordinaryPendingHistoryFixture(t *testing.T, stop string) (secretStore, *environmentCreateExecution, *deploymentplan.Plan, *deploymentplan.Plan, *ordinaryPendingHistoryBackend) {
	t.Helper()
	store, e, first, native := freshStageFixture(t)
	b := &ordinaryPendingHistoryBackend{freshStageBackend: native}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	execute := func(stage string, p *deploymentplan.Plan, fence, accept func(context.Context, *deploymentplan.Plan) error) error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, fence, accept, func(context.Context) error { return nil }, io.Discard)
	}
	if err := execute("first-accepted", first, check, check); err != nil {
		t.Fatal(err)
	}
	id := first.Resources[0].ID
	body := deploymentplan.Clone(b.objects[id])
	deploymentplan.Map(body["data"])["root"] = "ordinary-written-successor"
	tail := deploymentplan.Clone(first.Resources[0].Desired)
	deploymentplan.Map(tail["metadata"])["name"] = "unwritten-later-owner"
	second, err := deploymentplan.Compile(first.Inputs, []deploymentplan.Definition{{Object: body, Phase: "configuration"}, {Object: tail, Phase: "migration"}}, []deploymentplan.Object{b.objects[id]})
	if err != nil || second.Resources[0].Action != "update" {
		t.Fatal("ordinary same-owner update did not compile", err)
	}
	fence, accept := check, check
	switch stop {
	case "wait":
		b.failure = "after-write-configuration"
	case "acceptance":
		accept = func(context.Context, *deploymentplan.Plan) error { return errors.New("actual acceptance interruption") }
	case "next-phase":
		fence = func(ctx context.Context, p *deploymentplan.Plan) error {
			if deploymentplan.Map(b.objects[id]["data"])["root"] == "ordinary-written-successor" {
				return errors.New("actual next-phase interruption")
			}
			return check(ctx, p)
		}
	case "unknown-response":
		b.failure = "unknown-write"
	default:
		t.Fatal("unknown interruption")
	}
	if err := execute("ordinary-pending", second, fence, accept); err == nil {
		t.Fatal("ordinary interruption was not reached")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/ordinary-pending")
	if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || deploymentplan.Map(b.objects[id]["data"])["root"] != "ordinary-written-successor" {
		t.Fatal("fixture did not preserve the actual pending native write")
	}
	b.waits, b.denyWait = 0, true
	return store, e, first, second, b
}

func TestRecordedOrdinaryPendingWritesObserveWithoutReadinessOrAcceptance(t *testing.T) {
	for _, stop := range []string{"wait", "next-phase", "acceptance"} {
		t.Run(stop, func(t *testing.T) {
			store, e, first, second, b := ordinaryPendingHistoryFixture(t, stop)
			j, exists, err := second.ReadExecution(store.Root)
			if err != nil || !exists || j.Accepted || j.InFlight != "" {
				t.Fatal("not a known unaccepted write", err)
			}
			if stop == "next-phase" && (len(j.Completed) != 1 || j.Failed != second.Resources[1].ID || j.Owners[j.Failed] != "") {
				t.Fatal("did not reproduce written prefix followed by failed unwritten owner")
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			writes, leases := deploymentplan.Digest(b.writes), b.leases
			observed, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/first-accepted")
			if err != nil || observed == nil || observed.SHA256() != first.SHA256() {
				t.Fatal("accepted original owner did not follow its ordinary pending write", err)
			}
			pending, err := authenticatedRecordedPendingWrites(t.Context(), store, e, b)
			stage, ok := pending["kubernetes/ordinary-pending"]
			if err != nil || !ok || !stage.Written || stage.Plan.SHA256() != second.SHA256() || deploymentplan.Digest(stage.Journal) != deploymentplan.Digest(j) {
				t.Fatal("literal original pending input/journal not exposed as writes", err)
			}
			if b.waits != 0 || writes != deploymentplan.Digest(b.writes) || leases != b.leases {
				t.Fatal("history observation waited, replayed a write or acquired a lease")
			}
			assertRecoveryContractReadOnly(t, store, before)
		})
	}
}

func TestRecordedOrdinaryPendingWritesRefuseUnknownOrUnauthenticatedHistory(t *testing.T) {
	for _, mode := range []string{"foreign-uid", "changed-body", "missing-input", "changed-parent-intent", "missing-lease", "unknown-response"} {
		t.Run(mode, func(t *testing.T) {
			stop := "wait"
			if mode == "unknown-response" {
				stop = mode
			}
			store, e, _, second, b := ordinaryPendingHistoryFixture(t, stop)
			id := second.Resources[0].ID
			switch mode {
			case "foreign-uid":
				deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign-owner"
			case "changed-body":
				deploymentplan.Map(b.objects[id]["data"])["root"] = "unrecorded-native-body"
			case "missing-input":
				if err := os.Remove(environmentCreateStageInputPath(e, "ordinary-pending")); err != nil {
					t.Fatal(err)
				}
			case "changed-parent-intent":
				createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/ordinary-pending").IntentSHA256 = deploymentplan.Digest("foreign-intent")
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "missing-lease":
				leases := e.Journal.KubernetesLeases[:0]
				for _, lease := range e.Journal.KubernetesLeases {
					if !strings.HasPrefix(lease.Holder, second.SHA256()+":") {
						leases = append(leases, lease)
					}
				}
				e.Journal.KubernetesLeases = leases
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/first-accepted"); err == nil {
				t.Fatal("unproven ordinary native history was observed", mode)
			}
			if b.waits != 0 {
				t.Fatal("rejected history invoked native readiness")
			}
			assertRecoveryContractReadOnly(t, store, before)
		})
	}
}

func TestRecordedOrdinaryPendingWritesFollowLaterAcceptedUpdate(t *testing.T) {
	store, e, first, _, b := ordinaryPendingHistoryFixture(t, "wait")
	id := first.Resources[0].ID
	latest := deploymentplan.Clone(b.objects[id])
	deploymentplan.Map(latest["data"])["root"] = "later-accepted-successor"
	p, err := deploymentplan.Compile(first.Inputs, []deploymentplan.Definition{{Object: latest, Phase: "configuration"}}, []deploymentplan.Object{b.objects[id]})
	if err != nil {
		t.Fatal(err)
	}
	b.denyWait, b.failure = false, ""
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "later-accepted", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	b.waits, b.denyWait = 0, true
	before := createBootstrapStateSnapshot(t, store.Root)
	writes, leases := deploymentplan.Digest(b.writes), b.leases
	if observed, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/first-accepted"); err != nil || observed == nil || observed.SHA256() != first.SHA256() {
		t.Fatal("historical pending body was required to remain live after an accepted update", err)
	}
	if b.waits != 0 || writes != deploymentplan.Digest(b.writes) || leases != b.leases {
		t.Fatal("observing the chronological chain replayed a native operation")
	}
	assertRecoveryContractReadOnly(t, store, before)
}
