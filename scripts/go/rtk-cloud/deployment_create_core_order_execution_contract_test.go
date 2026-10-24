package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newManagedCoreOrderExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	f := newAcceptedCoreExecutionContract(t, true)
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	// The historical producer waited serially after each native write and had
	// no controller-before-account ordering. Retain that frozen ordering while
	// using maintained resource bodies and the real guarded execution journal.
	for i := range defs {
		defs[i].Dependencies = nil
		defs[i].Phase = "configuration"
		if defs[i].Object["kind"] == "Deployment" {
			defs[i].Phase = "migration"
		}
	}
	child, j := f.interruptedStage(t, "kubernetes/service-managed-core", defs, "Deployment/"+f.p.Plan.Stack+"-account-manager/account-manager")
	if !knownManagedCoreOrderFailure(child, j, f.p.Plan.Stack) {
		t.Fatalf("original core failure differs: completed=%d owners=%d failed=%s", len(j.Completed), len(j.Owners), j.Failed)
	}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	return f, child, j
}

func TestManagedCoreOrderExecutionContractQualifiesExpiredSameKeyRenewalWithoutWrites(t *testing.T) {
	f, child, j := newManagedCoreOrderExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true)
	if err != nil {
		t.Fatal("authenticated expired partial Core history rejected", err)
	}
	if !r.OrderForward || r.PartialCore.SHA256() != child.SHA256() || deploymentplan.Digest(r.PartialCoreJournal) != deploymentplan.Digest(j) || r.Candidate == nil || len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" || r.RenewedIntent.SessionID != sessionRenewalUUID(f.e.Journal.PlanSHA256, child.SHA256(), f.session) {
		t.Fatal("partial Core renewal lost original failure or proposed another identity")
	}
	env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(r.Candidate.Resources[0].Desired)["containers"])[0]))
	deadline := f.e.Journal.Entries[len(f.e.Journal.Entries)-1].StartedAt.Add(3 * time.Hour).UTC().Truncate(time.Second)
	if env["PKI_BOOTSTRAP_KEY_PATH"] != "/run/pki-bootstrap/bootstrap.key" || env["PKI_BOOTSTRAP_CSR_PATH"] != "/run/pki-bootstrap/bootstrap.csr" || env["PKI_BOOTSTRAP_CERT_PATH"] != "/run/pki-bootstrap/renew-"+child.SHA256()[:12]+"/bootstrap.crt" || env["PKI_BOOTSTRAP_EXPIRES_AT"] != deadline.Format(time.RFC3339) || p.Materials["accepted-service-core-order-renewal-plan"] != r.Candidate.SHA256() {
		t.Fatal("renewal changed original key/CSR paths or reset fixed acknowledgement window")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestManagedCoreOrderExecutionContractRefusesChangedWindowAndAttemptedOwners(t *testing.T) {
	for _, mutation := range []string{"acknowledgement-window", "unattempted-controller-owner", "original-intent"} {
		t.Run(mutation, func(t *testing.T) {
			f, child, j := newManagedCoreOrderExecutionContract(t)
			switch mutation {
			case "acknowledgement-window":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].StartedAt = time.Now().UTC().Add(-2 * time.Hour)
			case "unattempted-controller-owner":
				j.Owners["Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller"] = "99999999-9999-4999-8999-999999999999"
				f.write(t, filepath.Join("deployment/rollback", child.SHA256(), "journal.json"), j)
			case "original-intent":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].IntentSHA256 = deploymentplan.Digest("different original child")
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if _, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true); err == nil {
				t.Fatal("changed partial Core source accepted", mutation)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
