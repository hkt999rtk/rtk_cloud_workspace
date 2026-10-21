package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These bounded coordinator tests stop at a genuine interrupted native child.
// They do not model later certificate issuance or claim full bootstrap success.
func TestBootstrapCompletionExecutionContractRetainsInterruptedEnrollment(t *testing.T) {
	for _, stop := range []string{"application-client-enrollment", "transport-client-enrollment"} {
		t.Run(stop, func(t *testing.T) {
			f := newAcceptedCoreExecutionContract(t)
			runtime := f.runtime
			runtime.Backend = f.backend
			runtime.ServiceCeremony = &f.service
			runtime.PKIClient = &environmentCreatePKIClient{Execution: f.e}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			stages := []string{}
			interrupted := errors.New("isolated readiness interruption")
			var failed *deploymentplan.Plan
			apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
				stages = append(stages, stage)
				if stage != stop {
					f.acceptedHistoricalStage(t, stage, definitions)
					return nil
				}
				var owner string
				for _, d := range definitions {
					if d.Object["kind"] == "Job" {
						owner = deploymentplan.ID(d.Object)
						break
					}
				}
				if owner == "" {
					t.Fatal("enrollment has no actual native owner")
				}
				var journal deploymentplan.Journal
				failed, journal = f.interruptedStage(t, "kubernetes/"+stage, definitions, owner)
				if journal.Accepted || journal.InFlight != "" || journal.Failed != owner || len(journal.Owners) == 0 || f.native.held {
					t.Fatal("interrupted child lost its recorded native outcome")
				}
				return interrupted
			}
			_, err := completeEnvironmentCreateBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, runtime, f.service, apply)
			if !errors.Is(err, interrupted) || failed == nil {
				t.Fatal("coordinator advanced past failed original enrollment", err)
			}
			expected := []string{"application-client-enrollment"}
			if stop == "transport-client-enrollment" {
				expected = append(expected, stop)
			}
			if deploymentplan.Digest(stages) != deploymentplan.Digest(expected) {
				t.Fatal("bootstrap changed enrollment dependency order", stages)
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stop)
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				t.Fatal("failed original enrollment was silently accepted")
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, value := range before {
				if strings.HasPrefix(path, "pki/") || strings.HasPrefix(path, "openbao/") || strings.Contains(path, "/bootstrap/") {
					if after[path] != value {
						t.Fatal("coordinator replaced original identity/custody before recovery", path)
					}
				}
			}
		})
	}
}

func TestBootstrapCompletionExecutionContractRequiresOriginalRuntimeBeforeWrites(t *testing.T) {
	for _, mode := range []string{"execution", "registry", "ceremony", "executor", "store"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			runtime := environmentCreateBootstrapRuntime{Execution: &environmentCreateExecution{Store: store}, PKIClient: &environmentCreatePKIClient{}, ServiceCeremony: &environmentCreateServiceCeremony{}}
			apply := environmentCreateBootstrapApply(func(context.Context, string, []deploymentplan.Definition) error {
				t.Fatal("invalid runtime reached native stage")
				return nil
			})
			switch mode {
			case "execution":
				runtime.Execution = nil
			case "registry":
				runtime.PKIClient = nil
			case "ceremony":
				runtime.ServiceCeremony = nil
			case "executor":
				apply = nil
			case "store":
				runtime.Execution.Store.Root += "-foreign"
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if _, err := completeEnvironmentCreateBootstrap(t.Context(), deploymentConfig{Environment: "dev"}, environmentCreateBootstrapPlan{}, environmentRelease{}, store, nil, runtime, environmentCreateServiceCeremony{}, apply); err == nil {
				t.Fatal("unbound runtime accepted")
			}
			assertRecoveryContractReadOnly(t, store, before)
		})
	}
}
