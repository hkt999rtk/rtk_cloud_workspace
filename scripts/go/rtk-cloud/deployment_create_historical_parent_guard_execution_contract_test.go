package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Scoped create stages bind their parent through the backend, original owner
// and journal. Keep that exact production fence and additionally freeze the
// expected original parent SHA independently of optional plan metadata.
type historicalAcceptedScopedBackend struct {
	*normalTailE2EBackend
	parent    environmentCreateKubernetesBackend
	parentSHA string
}

func (b historicalAcceptedScopedBackend) CheckParentExecution(ctx context.Context, p *deploymentplan.Plan, root string) error {
	if p == nil || b.e == nil || b.parent.execution != b.e || b.e.Journal.PlanSHA256 != b.parentSHA || p.CreateParentSHA256 != "" && p.CreateParentSHA256 != b.parentSHA {
		return errors.New("foreign historical scoped parent")
	}
	return b.parent.CheckParentExecution(ctx, p, root)
}

func (b historicalAcceptedScopedBackend) RecordParentExecutionLease(ctx context.Context, p *deploymentplan.Plan, owner deploymentplan.LeaseOwner) error {
	if err := b.CheckParentExecution(ctx, p, b.parent.store.Root); err != nil {
		return err
	}
	return b.parent.RecordParentExecutionLease(ctx, p, owner)
}

func TestHistoricalScopedParentExecutionContractPreservesMaintainedOwnerFence(t *testing.T) {
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "original-scoped-policy", "namespace": e.Journal.Stack + "-video-cloud"}, "data": deploymentplan.Object{"policy": "original"}}
	// These are the exact input fields of applyEnvironmentCreateStage. Its
	// original parent is validated by the backend rather than an added field.
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("isolated original release"), BindingsSHA256: deploymentplan.Digest(body)}, []deploymentplan.Definition{{Phase: "configuration", Object: body}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	native := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}
	backend := historicalAcceptedScopedBackend{normalTailE2EBackend: native, parent: environmentCreateKubernetesBackend{execution: e, cfg: cfg, store: store}, parentSHA: e.Journal.PlanSHA256}
	for _, mode := range []string{"maintained-inputs", "explicit-original-parent", "foreign-explicit-parent", "foreign-environment", "foreign-stack", "foreign-root", "changed-owner", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			candidate := *p
			candidate.Inputs = p.Inputs
			root := store.Root
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			original := e.owner
			switch mode {
			case "explicit-original-parent":
				candidate.CreateParentSHA256 = e.Journal.PlanSHA256
			case "foreign-explicit-parent":
				candidate.CreateParentSHA256 = deploymentplan.Digest("foreign")
			case "foreign-environment":
				candidate.Environment = "staging"
			case "foreign-stack":
				candidate.Stack = "foreign"
			case "foreign-root":
				root = filepath.Join(store.Root, "foreign")
			case "changed-owner":
				changed := original
				changed.OwnerToken = "foreign"
				if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(changed), true); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(original), true); err != nil {
						t.Fatal(err)
					}
				}()
			case "cancelled":
				cancel()
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := backend.CheckParentExecution(ctx, &candidate, root)
			positive := mode == "maintained-inputs" || mode == "explicit-original-parent"
			if (err == nil) != positive {
				t.Fatal("parent fence changed", mode, err)
			}
			if !positive && mode != "foreign-root" && backend.RecordParentExecutionLease(ctx, &candidate, deploymentplan.LeaseOwner{ResourceID: "Lease/foreign"}) == nil {
				t.Fatal("foreign parent recorded a lease")
			}
			assertRecoveryContractReadOnly(t, store, before)
		})
	}
}
