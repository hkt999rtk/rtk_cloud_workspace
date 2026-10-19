package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func loadOriginalReadyServiceRoot(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, active ...bool) (environmentCreateRegisteredIssuer, environmentCreateIssuerOperation, error) {
	return loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "service", active...)
}

func loadOriginalReadyCreateRoot(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, domain string, active ...bool) (environmentCreateRegisteredIssuer, environmentCreateIssuerOperation, error) {
	if domain != "service" && domain != "mqtt" && domain != "openbao_tls" && domain != "app" {
		return environmentCreateRegisteredIssuer{}, environmentCreateIssuerOperation{}, createProviderRecoveryBlock("unsupported original ready Root domain")
	}
	var root environmentCreateRegisteredIssuer
	var operation environmentCreateIssuerOperation
	bad := func() (environmentCreateRegisteredIssuer, environmentCreateIssuerOperation, error) {
		return root, operation, createProviderRecoveryBlock("original ready Service Root lacks exact native registry, request, custody or completed ceremony evidence")
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "pki-reserve-"+domain+"-root" && entry.State == "completed" {
			if json.Unmarshal(entry.Receipt, &operation) != nil {
				return bad()
			}
		}
	}
	if !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) || operation.Status != "requested" || operation.Digest != deploymentplan.Digest(environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: domain, Kind: "root"}) {
		return bad()
	}
	for _, key := range []string{"pki-authorize-" + operation.ID, "pki-provision-" + operation.ID, "pki-import-" + operation.ID} {
		complete := false
		for _, entry := range e.Journal.Entries {
			if entry.Key == key && entry.State == "completed" && len(entry.Receipt) > 0 {
				complete = true
			}
		}
		if !complete {
			return bad()
		}
	}
	base := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/"+domain+"/ready.json")
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base), true)
	if err != nil {
		return bad()
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		return bad()
	}
	root, err = environmentCreateRegisteredIssuerFromMap(document, "")
	if err != nil || root.ID != operation.IssuerID || root.Environment != cfg.Environment || root.Domain != domain || root.Kind != "root" || root.Status != "ready" || document["signer_provider"] != "offline" {
		return bad()
	}
	p.Materials["original-"+domain+"-root-ready-document"] = environmentPreflightBytesSHA256(raw)
	manifestPath := filepath.Join("pki/custody/roots", root.ID, "manifest.json")
	raw, err = readBoundedEnvironmentFile(filepath.Join(store.Root, manifestPath), true)
	if err != nil {
		return bad()
	}
	var manifest struct {
		Schema      string            `json:"schema"`
		Environment string            `json:"environment"`
		Domain      string            `json:"trust_domain"`
		Issuer      string            `json:"root_issuer_id"`
		Fingerprint string            `json:"certificate_fingerprint_sha256"`
		Operator    string            `json:"operator_user_id"`
		Operation   string            `json:"ceremony_operation_id"`
		Paths       map[string]string `json:"paths"`
		Hashes      map[string]string `json:"file_sha256"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != "rtk-root-custody/v1" || manifest.Environment != cfg.Environment || manifest.Domain != domain || manifest.Issuer != root.ID || manifest.Fingerprint != root.RootFingerprint || manifest.Operation != operation.ID || len(manifest.Paths) != 4 || len(manifest.Hashes) != 4 {
		return bad()
	}
	paths := map[string]string{"key": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem")), "certificate": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "certificate.pem")), "csr": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "csr.pem")), "passphrase": "pki/custody/" + domain + "-root-v1-key-passphrase"}
	for key, path := range paths {
		file, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
		if err != nil || manifest.Paths[key] != path || manifest.Hashes[key] != environmentPreflightBytesSHA256(file) {
			return bad()
		}
		if key == "certificate" && string(file) != root.Certificate {
			return bad()
		}
		p.Materials["original-"+domain+"-root-custody-"+key] = manifest.Hashes[key]
	}
	p.Materials["original-"+domain+"-root-custody-manifest"] = environmentPreflightBytesSHA256(raw)
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil || binding.UserID != manifest.Operator {
		return bad()
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return bad()
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return bad()
	}
	if _, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return bad()
	}
	actual, err := client.ReadIssuer(ctx, root.ID)
	expected := root.Document
	wantCounts := "1|1"
	if domain == "mqtt" {
		wantCounts = "3|3"
	}
	if len(active) >= 1 && active[0] {
		var activated map[string]any
		if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/"+domain+"/root-active.json"), &activated) != nil {
			return bad()
		}
		bound := false
		for _, entry := range e.Journal.Entries {
			var receipt map[string]any
			if entry.Key == "pki-activate-"+operation.ID && entry.State == "completed" && json.Unmarshal(entry.Receipt, &receipt) == nil && deploymentplan.Digest(receipt) == deploymentplan.Digest(activated) {
				bound = true
			}
		}
		registered, parseErr := environmentCreateRegisteredIssuerFromMap(activated, "")
		if !bound || parseErr != nil || registered.Status != "active" || registered.ID != root.ID || registered.Domain != domain || registered.Environment != cfg.Environment || registered.Kind != "root" || registered.Certificate != root.Certificate || registered.ChainPEM != root.ChainPEM || registered.RootFingerprint != root.RootFingerprint || registered.BundleVersion != root.BundleVersion || registered.Reference != root.Reference {
			return bad()
		}
		root, expected, wantCounts = registered, activated, "2|2"
		if len(active) == 2 && active[1] {
			wantCounts = "3|3"
		}
		p.Materials["original-"+domain+"-root-active-document"] = deploymentplan.Digest(activated)
	}
	if p.Materials["accepted-service-core-openbao-trust-checkpoint"] != "" {
		wantCounts = "5|5"
	}
	if p.Materials["accepted-service-core-device-order-source"] != "" {
		wantCounts = "6|6"
	}
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(expected) {
		return bad()
	}
	if p.Materials["accepted-service-core-device-trust-source"] != "" {
		if err := verifyDeviceTrustRegistry(ctx, cfg, store, e, b, p); err != nil {
			return root, operation, err
		}
		return root, operation, nil
	}
	script := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c 'SELECT (SELECT count(*) FROM pki_issuers),(SELECT count(*) FROM pki_operations)'"
	counts, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", script)
	if err != nil || strings.TrimSpace(string(counts)) != wantCounts {
		return bad()
	}
	return root, operation, nil
}

func loadPartialServiceRootTrust(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, journal, createProviderRecoveryBlock("original partial trust lacks exact known writes, owners and failed rollout evidence")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || journal.Accepted || journal.InFlight != "" || journal.Failed != "Deployment/"+p.Plan.Stack+"-account-manager/account-manager" || len(plan.Resources) != 11 || len(journal.Owners) != 9 || len(journal.Completed) != 8 {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil {
		return bad()
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if !strings.HasPrefix(entry.Key, "kubernetes/service-root-trust-admission-recovery-") || entry.State != "pending" || len(entry.Receipt) != 0 || entry.IntentSHA256 != hash {
		return bad()
	}
	p.Materials["original-partial-root-trust-plan"] = sha
	p.Materials["original-partial-root-trust-journal"] = deploymentplan.Digest(journal)
	return &plan, journal, nil
}

func verifyPartialServiceRootTrust(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	for _, r := range plan.Resources {
		actual, err := b.Read(ctx, r.ID)
		if err != nil || actual == nil {
			return createProviderRecoveryBlock("partial trust resource is missing or unobservable")
		}
		uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
		if owner := journal.Owners[r.ID]; owner != "" {
			if uid != owner || !deploymentplan.Matches(r.Desired, actual) {
				return createProviderRecoveryBlock("known partial trust owner or written body changed")
			}
		} else if r.Action != "update" || uid != r.UID || deploymentplan.Digest(deploymentplan.Projection(actual)) != deploymentplan.Digest(r.Baseline) {
			return createProviderRecoveryBlock("unwritten partial trust target differs from its original baseline")
		}
	}
	return nil
}
