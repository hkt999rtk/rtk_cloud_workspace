package main

import (
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFinalizedFreshContinuationUsesPermanentStateAfterCredentialAndPreparationRetirement(t *testing.T) {
	runFreshFourteenProductProtocol(t, false, func(t *testing.T, f *productProtocolFixture, s environmentCreateServiceCeremony, _ []deploymentplan.Definition) {
		f.afterRetirement = func() {
			before := deploymentplan.Digest(f.e.Journal)
			ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
			needsRenewal, minted := false, false
			if err := verifyAcceptedIssuerEnrollmentContinuation(ctx, f.cfg, f.store, f.e, f.runtime.Backend, true, issuerEnrollmentSessionAllowance{NeedsRenewal: &needsRenewal, Minted: &minted}); err != nil {
				t.Fatal("completed fresh PKI recovery demanded deleted temporary credentials", err)
			}
			if !needsRenewal || !minted {
				t.Fatal("completed historical session entered a new renewal path")
			}
			got, done, err := observeCompletedManagedSessionRecovery(ctx, f.cfg, f.p, f.store, f.runtime)
			if err != nil || !done || got.BootstrapSessionID != s.BootstrapSessionID {
				t.Fatal("completed native recovery lost original finalization", err)
			}
			if before != deploymentplan.Digest(f.e.Journal) {
				t.Fatal("read-only post-finalization qualification changed original history")
			}
			id := f.p.ProductIdentities[0].ID
			metadata := deploymentplan.Map(f.b.objects[id]["metadata"])
			uid := metadata["uid"]
			metadata["uid"] = "99999999-9999-4999-8999-999999999999"
			err = verifyAcceptedIssuerEnrollmentContinuation(ctx, f.cfg, f.store, f.e, f.runtime.Backend, true)
			metadata["uid"] = uid
			if err == nil {
				t.Fatal("finalized qualification adopted a replacement permanent owner")
			}
			proof := f.final
			f.final.CleanupSHA256 = strings.Repeat("f", 64)
			err = verifyAcceptedIssuerEnrollmentContinuation(ctx, f.cfg, f.store, f.e, f.runtime.Backend, true)
			f.final = proof
			if err == nil {
				t.Fatal("finalized qualification accepted mismatched native cleanup")
			}
			acks := slices.Clone(f.installed)
			f.installed = f.installed[:len(f.installed)-1]
			err = verifyAcceptedIssuerEnrollmentContinuation(ctx, f.cfg, f.store, f.e, f.runtime.Backend, true)
			f.installed = acks
			if err == nil || before != deploymentplan.Digest(f.e.Journal) {
				t.Fatal("finalized qualification accepted missing ACKs or changed original history")
			}
		}
	})
}
