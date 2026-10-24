package main

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original signed authority is an explicit native fixture input. Its
// reservation and activation use the maintained authenticated SDK and journal,
// before any Service session producer or later recovery captures this lineage.
// This is not a claim to execute OpenBao's intermediate provisioning ceremony.
func publishHistoricalOriginalServiceIntermediate(t *testing.T, f *acceptedCoreExecutionContract, service environmentCreateServiceCeremony) {
	t.Helper()
	root, err := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil || intermediate.CheckSignatureFrom(root) != nil || !intermediate.IsCA || intermediate.KeyUsage&x509.KeyUsageCertSign == 0 || service.Intermediate.Document["parent_issuer_id"] != service.Root.ID {
		t.Fatal("original native Service intermediate lost its signed root lineage", err)
	}
	policy := findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, "service")
	request := environmentCreateIssuerRequest{Environment: f.cfg.Environment, Domain: "service", Kind: "intermediate", ParentID: service.Root.ID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs}
	operation := environmentCreateIssuerOperation{ID: "88888888-8888-4888-8888-888888888888", IssuerID: service.Intermediate.ID, Status: "requested", Digest: deploymentplan.Digest(request)}
	reserved, activated, reads := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login" {
			var login map[string]string
			if json.NewDecoder(r.Body).Decode(&login) != nil || len(login) != 2 || login["email"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || login["password"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "original native operator differs", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(deploymentplan.Object{"user": deploymentplan.Object{"id": f.operator["PKI_OPERATOR_USER_ID"], "email": login["email"]}, "tokens": deploymentplan.Object{"access_token": "isolated-original-service-authority"}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer isolated-original-service-authority" {
			http.Error(w, "original native authentication required", http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/platform/pki/operations":
			var actual environmentCreateIssuerRequest
			if json.NewDecoder(r.Body).Decode(&actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(request) || reserved != 0 {
				http.Error(w, "original Service reservation differs or was replayed", http.StatusConflict)
				return
			}
			reserved++
			_ = json.NewEncoder(w).Encode(operation)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/platform/pki/operations/"+operation.ID+"/activate":
			if reserved != 1 || activated != 0 {
				http.Error(w, "original reservation must precede activation", http.StatusConflict)
				return
			}
			activated++
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/platform/pki/issuers/"+operation.IssuerID:
			if activated != 1 {
				http.Error(w, "original authority is not active", http.StatusConflict)
				return
			}
			reads++
			_ = json.NewEncoder(w).Encode(service.Intermediate.Document)
		default:
			http.Error(w, "unqualified native authority operation", http.StatusForbidden)
		}
	}))
	defer server.Close()
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), f.cfg.Environment, f.operator["PKI_OPERATOR_USER_ID"])
	if err != nil {
		t.Fatal(err)
	}
	client.Execution = f.e
	if _, err := client.Authenticate(t.Context(), f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal("actual original authority authentication", err)
	}
	actual, err := client.ReserveIssuer(t.Context(), "original-service-intermediate-"+f.e.Journal.PlanSHA256[:12], request)
	if err != nil || actual != operation {
		t.Fatal("actual original Service intermediate reservation", err)
	}
	if err := client.ActivateIssuer(t.Context(), actual); err != nil {
		t.Fatal("actual original Service intermediate activation", err)
	}
	if reserved != 1 || activated != 1 || reads != 1 {
		t.Fatal("original native authority lifecycle was omitted or replayed")
	}
	for key, expected := range map[string]any{"pki-reserve-service-intermediate": operation, "pki-activate-" + operation.ID: service.Intermediate.Document} {
		entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
		if entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(jsonRawObject(mustProviderRecoveryJSON(expected))) {
			t.Fatal("actual native authority lacks its original complete receipt", key)
		}
	}
}

func TestHistoricalOriginalServiceIntermediatePreservesAuthenticatedReservationAndActivation(t *testing.T) {
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, historicalOriginalFoundationHooks(t), historicalCompleteDomainPolicy(t))
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	root := map[string]environmentCreateRegisteredIssuer{"service": f.service.Root}
	intermediate := map[string]environmentCreateRegisteredIssuer{"service": f.service.Intermediate}
	// The focused read authenticates the actual Service reservation/activation
	// pair; the all-domain reader is exercised by the connected transport case.
	var operation environmentCreateIssuerOperation
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-reserve-service-intermediate")
	if entry == nil || decodeEnvironmentJSON(entry.Receipt, &operation) != nil || operation.IssuerID != intermediate["service"].ID {
		t.Fatal("original Service reservation cannot be independently read")
	}
	activated := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-activate-"+operation.ID)
	if activated == nil || deploymentplan.Digest(jsonRawObject(activated.Receipt)) != deploymentplan.Digest(intermediate["service"].Document) {
		t.Fatal("original Service activation does not match actual reservation")
	}
	var saved deploymentplan.Object
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/intermediate-active.json"), &saved); err != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(intermediate["service"].Document) || intermediate["service"].RootFingerprint != root["service"].RootFingerprint {
		t.Fatal("original persisted Service authority changed", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("source read wrote native resources")
	}
}

func TestHistoricalManagedPublicationReaderBindsOriginalOperationsAndCustody(t *testing.T) {
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	for _, mode := range []string{"complete", "missing-reservation", "changed-activation", "changed-custody"} {
		t.Run(mode, func(t *testing.T) {
			view := *f.e
			view.Journal.Entries = slices.Clone(f.e.Journal.Entries)
			switch mode {
			case "missing-reservation":
				for i := range view.Journal.Entries {
					if view.Journal.Entries[i].Key == "pki-reserve-service-intermediate" {
						view.Journal.Entries[i].Key = "unselected-historical-operation"
					}
				}
			case "changed-activation":
				for i := range view.Journal.Entries {
					if view.Journal.Entries[i].Key == "pki-activate-88888888-8888-4888-8888-888888888888" {
						doc := deploymentplan.Clone(intermediates["service"].Document)
						doc["status"] = "ready"
						view.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(doc)
					}
				}
			case "changed-custody":
				path := filepath.Join("pki/custody/roots", roots["service"].ID, "ca-key.encrypted.pem")
				if err := f.store.write(path, []byte("changed isolated custody"), true); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			writes := deploymentplan.Digest(f.native.writes)
			err := verifyManagedPublicationJournal(f.store, &view, roots, intermediates)
			if mode == "complete" {
				if err != nil {
					t.Fatal("all original signed managed publications lost their actual native provenance", err)
				}
			} else {
				cause := map[string]string{"missing-reservation": "reservation-pin", "changed-activation": "activation-receipt", "changed-custody": "custody-file-pin-key"}[mode]
				if err == nil || !strings.Contains(err.Error(), cause) {
					t.Fatal("changed original authority evidence was not refused at its exact boundary", err)
				}
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("publication provenance read replayed native authority operations")
			}
		})
	}
}
