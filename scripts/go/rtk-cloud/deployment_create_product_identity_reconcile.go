package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A recorded response can close its original parent entry only after the same
// native identity is read back. This never retries the native POST.
func reconcileProductMutation(ctx context.Context, c *environmentCreatePKIClient, key string, intent any, receipt json.RawMessage, freshProof ...func(context.Context) error) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("recorded product response has no exact fresh native readback")
	}
	if c == nil || c.Execution == nil || c.Execution.Check(ctx) != nil {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if strings.HasPrefix(key, "observe-") || strings.HasPrefix(key, "approve-") {
		if len(freshProof) != 1 || freshProof[0] == nil {
			return bad()
		}
		return freshProof[0](ctx)
	}
	if len(freshProof) != 0 {
		return bad()
	}
	var original environmentCreateIssuerOperation
	wantedStatus := ""
	if key == "pki-reserve-service-intermediate" {
		if decodeEnvironmentJSON(receipt, &original) != nil || original.Status != "requested" {
			return bad()
		}
		wantedStatus = "requested"
	} else {
		reservation := createOpenBaoACKCompletionEntry(c.Execution.Journal, c.mutationKey("pki-reserve-service-intermediate"))
		if reservation == nil || reservation.State != "completed" || decodeEnvironmentJSON(reservation.Receipt, &original) != nil {
			return bad()
		}
		switch key {
		case "pki-authorize-" + original.ID:
			wantedStatus = "approved"
		case "pki-provision-" + original.ID:
			wantedStatus = "provisioning"
		case "pki-import-" + original.ID:
			wantedStatus = "ready"
		case "pki-activate-" + original.ID:
			wantedStatus = "active"
		default:
			return bad()
		}
	}
	if !environmentBootstrapOwnerUUID.MatchString(original.ID) || !environmentBootstrapOwnerUUID.MatchString(original.IssuerID) || !environmentPreflightDigest.MatchString(original.Digest) {
		return bad()
	}
	var current struct {
		environmentCreateIssuerOperation
		CreatedBy string `json:"created_by"`
		Action    string `json:"action"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/platform/pki/operations/"+original.ID, "", nil, &current); err != nil {
		return err
	}
	if current.ID != original.ID || current.IssuerID != original.IssuerID || current.Digest != original.Digest || current.Status != wantedStatus || current.CreatedBy != c.OperatorID || current.Action != "provision" {
		return bad()
	}
	issuer, err := c.ReadIssuer(ctx, original.IssuerID)
	if err != nil {
		return err
	}
	if issuer["trust_domain"] != "service" || issuer["kind"] != "intermediate" || issuer["status"] != wantedStatus {
		return bad()
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if key == "pki-reserve-service-intermediate" {
		var request environmentCreateIssuerRequest
		if decodeEnvironmentJSON(raw, &request) != nil || request.Environment != c.Environment || request.Domain != "service" || request.Kind != "intermediate" || request.ParentID != issuer["parent_issuer_id"] || deploymentplan.Digest(request) != original.Digest {
			return bad()
		}
		var clients, names []string
		clientRaw, _ := json.Marshal(issuer["service_client_ids"])
		nameRaw, _ := json.Marshal(issuer["server_dns_names"])
		if json.Unmarshal(clientRaw, &clients) != nil || json.Unmarshal(nameRaw, &names) != nil || !sameProductStringSet(clients, request.ServiceClientIDs) || !sameProductStringSet(names, request.ServerDNSNames) {
			return bad()
		}
	} else {
		if activeTransportJournalDigest(jsonRawObject(receipt)) != activeTransportJournalDigest(issuer) {
			return bad()
		}
		switch wantedStatus {
		case "approved", "active":
			var operation environmentCreateIssuerOperation
			if decodeEnvironmentJSON(raw, &operation) != nil || operation != original {
				return bad()
			}
		case "provisioning", "ready":
			var request struct {
				Operation string `json:"operation_id"`
				Issuer    string `json:"issuer_id"`
				CSR       string `json:"csr_sha256,omitempty"`
				Cert      string `json:"certificate_sha256,omitempty"`
			}
			if decodeEnvironmentJSON(raw, &request) != nil || request.Operation != original.ID || request.Issuer != original.IssuerID || wantedStatus == "provisioning" && (request.CSR != environmentPreflightBytesSHA256(nil) || request.Cert != "") || wantedStatus == "ready" && (request.Cert != environmentPreflightBytesSHA256([]byte(deploymentplan.String(issuer["certificate_pem"]))) || request.CSR != "") {
				return bad()
			}
		}
	}
	return c.Execution.Check(ctx)
}

func sameProductStringSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func verifyProductWorkloadApproval(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, intent deploymentplan.Object) error {
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.Backend.Store.Environment != cfg.Environment || runtime.Execution.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("original product approval inspection is unavailable")
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	query := "SELECT COALESCE(json_agg(json_build_object('service_id',service_id,'instance_id',instance_id,'certificate_subject',certificate_subject,'issuer_fingerprint_sha256',issuer_fingerprint_sha256,'allowed_option_codes',allowed_option_codes,'approved_by',approved_by::text,'revoked',revoked_at IS NOT NULL)),'[]'::json) FROM platform_service_workloads WHERE environment=" + quote(cfg.Environment) + " AND certificate_subject=" + quote(deploymentplan.String(intent["certificate_subject"])) + " AND issuer_fingerprint_sha256=" + quote(deploymentplan.String(intent["issuer_fingerprint_sha256"]))
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=10000", "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d rtk_account_manager -v ON_ERROR_STOP=1 -At -c "$1"`, "product-approval-read", query)
	if err != nil || len(raw) > 256<<10 {
		return environmentCreateBootstrapBlocker("native original product approval cannot be read")
	}
	var rows []struct {
		Service  string   `json:"service_id"`
		Instance string   `json:"instance_id"`
		Subject  string   `json:"certificate_subject"`
		Issuer   string   `json:"issuer_fingerprint_sha256"`
		Options  []string `json:"allowed_option_codes"`
		Operator string   `json:"approved_by"`
		Revoked  bool     `json:"revoked"`
	}
	wanted, _ := json.Marshal(intent["allowed_option_codes"])
	var options []string
	if decodeEnvironmentJSON(raw, &rows) != nil || len(rows) != 1 || json.Unmarshal(wanted, &options) != nil || rows[0].Service != intent["service_id"] || rows[0].Instance != intent["instance_id"] || rows[0].Subject != intent["certificate_subject"] || rows[0].Issuer != intent["issuer_fingerprint_sha256"] || rows[0].Operator != runtime.PKIClient.OperatorID || rows[0].Revoked || !sameProductStringSet(rows[0].Options, options) {
		return environmentCreateBootstrapBlocker("native product approval differs from its recorded original response")
	}
	return runtime.Execution.Check(ctx)
}
