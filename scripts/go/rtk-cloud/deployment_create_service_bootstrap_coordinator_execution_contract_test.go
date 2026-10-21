package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Start at the qualified expired-before-server-enrollment boundary. Original
// authentication separation is a runtime input, as in the maintained recovery
// caller; this test does not claim fresh Root creation or public preflight.
// The coordinator, original-key producer and all five Core stage executors run.
func TestServiceBootstrapCoordinatorRenewsOriginalSessionBeforeOwnedCoreStages(t *testing.T) {
	for _, stop := range []string{"complete", "service-session-original-key-renewal-", "service-issuer-server-enrollment-renewal-"} {
		t.Run(stop, func(t *testing.T) {
			// Produce the database prerequisite before the original source is
			// captured, so later recovery authenticates the unchanged chronology.
			hooks := coreExecutionContractHooks{BeforeSource: func(f *acceptedCoreExecutionContract, _ *environmentCreateBootstrapRuntime) {
				f.stage(t, "kubernetes/isolated-original-primary-verifier", []deploymentplan.Definition{environmentCreateSecret(f.service.BootstrapCredentialNamespace, "pki-verifier-database", map[string]string{"url": "postgres://isolated/verification"})})
			}}
			f, r := newExpiredSessionExecutionContractWithHooks(t, hooks, historicalCompleteDomainPolicy(t))
			p := f.p
			p.Materials = map[string]string{}
			if _, err := prepareExpiredServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
				t.Fatal("actual expired original source did not qualify", err)
			}
			r.Review = deploymentplan.Digest(p)
			originalPlan, originalJournal := r.Original.SHA256(), deploymentplan.Digest(r.Journal)
			originalKey, originalCSR := f.key, f.csr
			originalRows := deploymentplan.Digest(f.protocol["historical_sessions"])
			certificate, state := historicalTransportSeedLeaf(t, f, r.RenewedIntent.SessionID)
			installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
			owners := map[string]string{}
			for id, object := range f.native.objects {
				owners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			}
			runtime := f.runtime
			runtime.Backend, runtime.ServiceSessionRecovery, runtime.InitialAuthSeparated = f.backend, r, true
			runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
			updated, _, err := initialCertIssuerAuthDefinitions(f.cfg, runtime.InitialListeners)
			if err != nil {
				t.Fatal("maintained separated listener recipe", err)
			}
			runtime.InitialListeners = updated
			base := historicalTransportSessionApply(t, f, r, certificate, state)
			interrupted := errors.New("isolated process loss after accepted native stage")
			stopped := false
			var stages []string
			apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
				stages = append(stages, stage)
				if err := base(ctx, stage, definitions); err != nil {
					return err
				}
				if stop != "complete" && !stopped && stage == stop+r.Review {
					stopped = true
					return interrupted
				}
				return nil
			}
			actual, err := executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
			if stop != "complete" {
				if !errors.Is(err, interrupted) || !stopped || f.native.held || stages[len(stages)-1] != stop+r.Review {
					t.Fatal("coordinator advanced beyond its actual interrupted stage or retained a lease", err)
				}
				if stop == "service-issuer-server-enrollment-renewal-" {
					entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/service-issuer-server-enrollment")
					if entry == nil || entry.State != "pending" || !entry.CompletedAt.IsZero() {
						t.Fatal("original failed enrollment alias closed before its accepted forward returned")
					}
				}
				writes := map[string]int{}
				for id, count := range f.native.writes {
					writes[id] = count
				}
				stages = nil
				actual, err = executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
				for id, count := range writes {
					if strings.HasPrefix(id, "Job/") && f.native.writes[id] != count {
						t.Fatal("resumption replayed an already written one-shot Job", id)
					}
				}
			}
			if err != nil {
				t.Fatal("qualified Service coordinator did not complete", err)
			}
			want := []string{"service-session-original-key-renewal-" + r.Review, "service-custody-reader-renewal-" + r.Review, "service-credential-secrets-renewal-" + r.Review, "service-enrollment-issuer-renewal-" + r.Review, "service-issuer-server-enrollment-renewal-" + r.Review, "service-managed-enrollment-issuer", "service-core-enrollment", "service-managed-core"}
			if !slices.Equal(stages, want) {
				t.Fatal("coordinator changed original renewal and Core dependency order", stages)
			}
			if actual.BootstrapSessionID != state.Session || actual.Root.ID != f.service.Root.ID || actual.Intermediate.ID != f.service.Intermediate.ID || len(actual.CredentialUIDs) != 3 || r.Original.SHA256() != originalPlan || deploymentplan.Digest(r.Journal) != originalJournal {
				t.Fatal("coordinator replaced original source, authority or actual credential owners")
			}
			for _, stage := range want {
				plan, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
				if err != nil || !journal.Accepted || len(plan.Resources) == 0 || len(journal.Owners) != len(plan.Resources) {
					t.Fatal("coordinator omitted an independently accepted original stage", stage, err)
				}
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/service-issuer-server-enrollment")
			var alias map[string]string
			if entry == nil || entry.State != "completed" || decodeEnvironmentJSON(entry.Receipt, &alias) != nil || alias["original_failed_plan_sha256"] != originalPlan || alias["renewed_session_id"] != actual.BootstrapSessionID || alias["actual_qualified_forward_stage"] != "kubernetes/service-issuer-server-enrollment-renewal-"+r.Review {
				t.Fatal("original failed enrollment lost exact accepted-forward closure")
			}
			for id, uid := range owners {
				if deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != uid {
					t.Fatal("coordinator replaced an original native owner", id)
				}
			}
			custody := f.protocol["custody"].(map[string]string)
			if custody["/run/pki-bootstrap/bootstrap.key"] != originalKey || custody["/run/pki-bootstrap/bootstrap.csr"] != originalCSR || custody[r.RenewedCertPath] != certificate {
				t.Fatal("original key/CSR or selected renewed certificate changed")
			}
			rows := f.protocol["historical_sessions"].(map[string]managedSessionState)
			priorRows := map[string]managedSessionState{}
			for id, row := range rows {
				if id != state.Session {
					priorRows[id] = row
				}
			}
			if deploymentplan.Digest(priorRows) != originalRows || rows[state.Session].Fingerprint != state.Fingerprint {
				t.Fatal("coordinator replaced an original native session row")
			}
			var native map[string]any
			if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native); err != nil || native["policy_writes"] != float64(2) || native["role_writes"] != float64(2) {
				t.Fatal("coordinator replayed native signer/cleanup policy or role", err)
			}
			if !f.e.Journal.CompletedAt.IsZero() || f.native.held {
				t.Fatal("Core-only completion finalized the original deployment or retained its lease")
			}
		})
	}
}
