package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCompletedManagedSessionRecoveryObservesDeclaredClientGrowth(t *testing.T) {
	f := newBootstrapCleanupLineageFixture(t, true)
	r := f.runtime.ServiceSessionRecovery
	proofPath := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "managed-clients-preserved.json")
	preserved := map[string]string{"original_session_id": r.Intent.SessionID, "renewed_session_id": r.RenewedIntent.SessionID, "clients_sha256": f.q.Renewal.ClientsSHA, "original_key_sha256": r.OriginalKeySHA, "original_csr_sha256": r.OriginalCSRSHA}
	if err := recordManagedSessionClients(f.store, proofPath, preserved); err != nil {
		t.Fatal(err)
	}
	preservedBytes, err := os.ReadFile(filepath.Join(f.store.Root, proofPath))
	if err != nil {
		t.Fatal(err)
	}
	// Persist the same seven native SELECT fields as the real session producer.
	observedPath := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "observed.json")
	var observed createServiceSessionObserved
	if err := readExecutionJSON(observedPath, &observed); err != nil {
		t.Fatal(err)
	}
	native := jsonRawObject(observed.Session)
	delete(native, "installed_subjects")
	delete(native, "other_active")
	observed.Session = mustProviderRecoveryJSON(native)
	if err := os.WriteFile(observedPath, mustProviderRecoveryJSON(observed), 0600); err != nil {
		t.Fatal(err)
	}
	clientPath := filepath.Join(f.nativeDir, "cleanup-clients.json")
	var original, products []managedSessionClient
	if readExecutionJSON(clientPath, &original) != nil || readExecutionJSON(filepath.Join(f.nativeDir, "cleanup-product-clients.json"), &products) != nil || len(original) != 9 || len(products) != 5 {
		t.Fatal("fixture must retain nine signed clients and five later declared products")
	}
	grown := append(slices.Clone(original), products...)
	if err := os.WriteFile(clientPath, mustProviderRecoveryJSON(grown), 0600); err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	apply := func(context.Context, string, []deploymentplan.Definition) error {
		applyCalls++
		return errors.New("completed native session producer replayed")
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	beforeCustody := createBootstrapStateSnapshot(t, filepath.Join(f.nativeDir, "cleanup-custody"))
	got, err := executeManagedSessionRecovery(t.Context(), f.cfg, f.parent.Bootstrap, f.store, f.runtime, apply)
	if err != nil {
		t.Fatal("completed original session must observe declared product growth", err)
	}
	if applyCalls != 0 || deploymentplan.Digest(got) != deploymentplan.Digest(f.service) {
		t.Fatal("completed producer changed its original ceremony or replayed native stages")
	}
	if observed, completed, err := observeCompletedManagedSessionRecovery(t.Context(), f.cfg, f.parent.Bootstrap, f.store, f.runtime); err != nil || !completed || deploymentplan.Digest(observed) != deploymentplan.Digest(got) {
		t.Fatal("preflight and execution did not share the completed producer observation", err)
	}
	if err := completePartialSessionAdmission(t.Context(), f.cfg, f.store, f.runtime, got); err != nil {
		t.Fatal("completed session admission rejected later declared clients", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if !reflect.DeepEqual(beforeCustody, createBootstrapStateSnapshot(t, filepath.Join(f.nativeDir, "cleanup-custody"))) {
		t.Fatal("completed observation changed native key/CSR/cert custody")
	}
	for _, mode := range []string{"foreign-row", "changed-original-row", "changed-proof", "foreign-credential-uid", "pending-batch"} {
		t.Run(mode, func(t *testing.T) {
			rows := slices.Clone(grown)
			proof := map[string]string{}
			for k, v := range preserved {
				proof[k] = v
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-batches-"+f.review)
			savedEntry := *entry
			credentialPath := filepath.Join(f.nativeDir, "Secret_"+f.service.BootstrapCredentialNamespace+"_"+f.service.BootstrapCredentialSecret+".json")
			credentialBytes, err := os.ReadFile(credentialPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				*entry = savedEntry
				if os.WriteFile(clientPath, mustProviderRecoveryJSON(grown), 0600) != nil || os.WriteFile(filepath.Join(f.store.Root, proofPath), preservedBytes, 0600) != nil || os.WriteFile(credentialPath, credentialBytes, 0600) != nil {
					t.Fatal("cannot restore isolated original proof")
				}
			})
			switch mode {
			case "foreign-row":
				rows[len(rows)-1].Subject = "service:foreign"
			case "changed-original-row":
				rows[0].Request = "foreign-original-request"
			case "changed-proof":
				proof["clients_sha256"] = deploymentplan.Digest(grown)
			case "foreign-credential-uid":
				var credential deploymentplan.Object
				if decodeEnvironmentJSON(credentialBytes, &credential) != nil {
					t.Fatal("native credential fixture unavailable")
				}
				deploymentplan.Map(credential["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				if err := os.WriteFile(credentialPath, mustProviderRecoveryJSON(credential), 0600); err != nil {
					t.Fatal(err)
				}
			case "pending-batch":
				entry.State, entry.Receipt = "pending", nil
				entry.CompletedAt = time.Time{}
			}
			if os.WriteFile(clientPath, mustProviderRecoveryJSON(rows), 0600) != nil || os.WriteFile(filepath.Join(f.store.Root, proofPath), mustProviderRecoveryJSON(proof), 0600) != nil {
				t.Fatal("cannot prepare isolated negative observation")
			}
			if _, err := executeManagedSessionRecovery(t.Context(), f.cfg, f.parent.Bootstrap, f.store, f.runtime, apply); err == nil {
				t.Fatal("changed original completion or foreign clients accepted", mode)
			}
			if applyCalls != 0 {
				t.Fatal("unqualified completion replayed native stages", mode)
			}
		})
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}
