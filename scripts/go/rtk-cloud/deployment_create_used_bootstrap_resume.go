package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Core advances the shared trust ConfigMaps after credential distribution. A
// retry can observe the accepted union, but must never replay the older desired
// ConfigMaps over those original, now managed owners.
func resumeAcceptedUsedBootstrapCore(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime) (environmentCreateServiceCeremony, bool, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	var empty environmentCreateServiceCeremony
	if r == nil || e == nil || !environmentPreflightDigest.MatchString(r.Review) {
		return empty, false, environmentCreateBootstrapBlocker("original used renewal execution required")
	}
	coreKey := "kubernetes/service-managed-used-renewal-" + r.Review
	entry := createOpenBaoACKCompletionEntry(e.Journal, coreKey)
	if entry == nil || entry.State != "completed" {
		return empty, false, nil
	}
	bad := func(stage string) (environmentCreateServiceCeremony, bool, error) {
		return empty, true, environmentCreateBootstrapBlocker("accepted used renewal Core, original custody or native owners changed: " + stage)
	}
	if store.Root != e.Store.Root || store.Environment != cfg.Environment || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || !r.CoreAccepted || !r.PostServiceResume || !r.PostServiceRenewal || r.RenewalCandidate == nil || len(r.RenewalCandidate.Resources) != 1 || e.Check(ctx) != nil {
		return bad("execution-owner")
	}
	a, _, err := loadAdmittedCreateRecovery(store, e, r.Review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-application-trust-recovery/v1" || a.Plan.Materials["accepted-service-core-used-renewal-plan"] != r.RenewalCandidate.SHA256() || deploymentplan.Digest(a.Plan.Plan.Bootstrap) != deploymentplan.Digest(p) {
		return bad("admitted-renewal")
	}
	// The first admitted source binds the original persistent key and CSR.
	var first struct {
		Stage string `json:"actual_qualified_forward_stage"`
	}
	for _, v := range e.Journal.Entries {
		if v.Key == "kubernetes/service-issuer-server-enrollment" && json.Unmarshal(v.Receipt, &first) != nil {
			return bad("original-alias")
		}
	}
	firstReview := strings.TrimPrefix(first.Stage, "kubernetes/service-issuer-server-enrollment-renewal-")
	if !environmentPreflightDigest.MatchString(firstReview) || first.Stage != "kubernetes/service-issuer-server-enrollment-renewal-"+firstReview {
		return bad("original-review")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", firstReview, "qualification.json"), true)
	var firstSource createFoundationRecoveryArtifact
	if err != nil || environmentPreflightBytesSHA256(raw) != a.Plan.Materials["accepted-service-core-source-qualification"] || json.Unmarshal(raw, &firstSource) != nil || deploymentplan.Digest(firstSource.Plan) != firstReview || firstSource.Plan.ParentSHA != e.Journal.PlanSHA256 || firstSource.Plan.Materials["original-service-session-custody-bootstrap.key"] != r.OriginalKeySHA || firstSource.Plan.Materials["original-service-session-custody-bootstrap.csr"] != r.OriginalCSRSHA {
		return bad("original-custody-binding")
	}
	s := r.Ceremony
	s.BootstrapSessionID, s.BootstrapCredentialSecret = r.RenewedIntent.SessionID, "service-bootstrap-"+r.RenewedIntent.SessionID
	path := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "observed.json")
	private, err := privateDeploymentStatePath(store.Environment, path)
	if err != nil || private != filepath.Join(store.Root, path) || validateEnvironmentPreflightDirectories(store, filepath.Dir(private)) != nil {
		return bad("private-observation-path")
	}
	raw, err = readBoundedEnvironmentFile(private, true)
	var observed createServiceSessionObserved
	if err != nil || json.Unmarshal(raw, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Cleanup.SessionID != s.BootstrapSessionID || observed.Cleanup.NamespaceUID != s.CleanupBinding.NamespaceUID || observed.Cleanup.KubernetesCA != s.CleanupBinding.KubernetesCA || observed.Reader != s.CustodyReader || len(observed.UIDs) != len(s.BootstrapCredentialNamespaces) {
		return bad("original-observation")
	}
	s.CleanupBinding, s.CredentialUIDs = observed.Cleanup, observed.UIDs
	latest, owners := map[string]deploymentplan.Resource{}, map[string]string{}
	var core *deploymentplan.Plan
	for _, stage := range []struct {
		key  string
		size int
	}{{"kubernetes/service-session-original-key-renewal-" + r.Review, 1}, {"kubernetes/service-custody-reader-renewal-" + r.Review, 1}, {"kubernetes/service-credential-secrets-renewal-" + r.Review, 7}, {coreKey, 15}} {
		plan, journal, err := acceptedCreateStage(store, e, stage.key)
		if err != nil || len(plan.Resources) != stage.size || plan.Environment != cfg.Environment || plan.Stack != p.Stack || plan.ConfigurationSHA256 != a.Plan.Plan.ConfigurationSHA256 || plan.ReleaseSHA256 != deploymentplan.Digest(a.Plan.Plan.Release) {
			return bad("accepted-child")
		}
		if stage.key == coreKey {
			core = plan
		}
		if stage.size == 1 && strings.Contains(stage.key, "service-session-original-key-renewal-") && deploymentplan.Digest(deploymentplan.Projection(plan.Resources[0].Desired)) != deploymentplan.Digest(deploymentplan.Projection(r.RenewalCandidate.Resources[0].Desired)) {
			return bad("renewal-recipe")
		}
		for _, resource := range plan.Resources {
			uid := journal.Owners[resource.ID]
			if owners[resource.ID] != "" && owners[resource.ID] != uid {
				return bad("owner-continuity")
			}
			latest[resource.ID], owners[resource.ID] = resource, uid
		}
	}
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, s)
	if err != nil {
		return bad("core-renderer")
	}
	for _, d := range defs {
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = r.GrantImage.Reference
				}
			}
		}
	}
	rendered, err := deploymentplan.Compile(core.Inputs, defs, nil)
	if err != nil {
		return bad("core-renderer-normalization")
	}
	expected := map[string]string{}
	for _, resource := range rendered.Resources {
		expected[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(resource.Desired))
	}
	for _, resource := range core.Resources {
		if expected[resource.ID] != deploymentplan.Digest(deploymentplan.Projection(resource.Desired)) {
			return bad("core-recipe")
		}
		delete(expected, resource.ID)
	}
	if len(expected) != 0 {
		return bad("core-inventory")
	}
	live, err := runtime.Backend.Inventory(ctx, p.Stack)
	if err != nil {
		return empty, true, err
	}
	if err := verifyAcceptedCreateSnapshot(latest, owners, live); err != nil {
		return empty, true, err
	}
	combined := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack}}
	for id, resource := range latest {
		resource.Action, resource.UID = "retain", owners[id]
		combined.Resources = append(combined.Resources, resource)
	}
	if err := verifyOriginalMigrationCheckpointInventory(ctx, store, combined, deploymentplan.Journal{Owners: owners}, live); err != nil {
		return empty, true, err
	}
	material := map[string]string{}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		id := "Secret/" + namespace + "/" + s.BootstrapCredentialSecret
		resource, ok := latest[id]
		if !ok || resource.Desired["immutable"] != true || owners[id] != observed.UIDs[namespace] || !environmentBootstrapOwnerUUID.MatchString(owners[id]) {
			return bad("credential-owner")
		}
		values := map[string]string{}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			value, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(resource.Desired["data"])[key]))
			if err != nil || len(value) == 0 {
				return bad("credential-data")
			}
			values[key] = string(value)
		}
		if len(material) != 0 && deploymentplan.Digest(material) != deploymentplan.Digest(values) {
			return bad("credential-copies")
		}
		material = values
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || len(pair.Certificate) < 2 || material["ca.crt"] != s.Root.Certificate || environmentPreflightBytesSHA256([]byte(material["tls.key"])) != r.OriginalKeySHA {
		return bad("original-private-key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	issuer, issuerErr := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	state, _, stateErr := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
	var saved managedSessionState
	if err != nil || issuerErr != nil || stateErr != nil || json.Unmarshal(observed.Session, &saved) != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.IsCA || leaf.CheckSignatureFrom(issuer) != nil || leaf.NotBefore.After(time.Now()) || !leaf.NotAfter.After(time.Now()) || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != s.Root.RootFingerprint || state.Session != s.BootstrapSessionID || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint || state.Status != "active" || state.OtherActive != 0 || !slices.Equal(state.Subjects, r.RenewedIntent.Subjects) || state.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !state.Expires.Equal(leaf.NotAfter) || saved.Status != "active" || saved.OtherActive != 0 || !slices.Equal(saved.Subjects, state.Subjects) || saved.Session != state.Session || saved.Issuer != state.Issuer || saved.Root != state.Root || saved.Fingerprint != state.Fingerprint || !saved.Expires.Equal(state.Expires) {
		return bad("native-session")
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(state.Subjects, subject) {
			return bad("native-installed-subjects")
		}
		seen[subject] = true
	}
	query := "SELECT COALESCE(json_agg(subject ORDER BY subject),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	issued, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var subjects []string
	if err != nil || json.Unmarshal(issued, &subjects) != nil {
		return bad("native-issued-subjects")
	}
	for _, subject := range subjects {
		if !slices.Contains(state.Subjects, subject) {
			return bad("native-issued-subjects")
		}
	}
	for _, file := range []string{"/run/pki-bootstrap/bootstrap.key", "/run/pki-bootstrap/bootstrap.csr", r.RenewedCertPath} {
		value, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", file)
		if err != nil || file == "/run/pki-bootstrap/bootstrap.key" && (string(value) != material["tls.key"] || environmentPreflightBytesSHA256(value) != r.OriginalKeySHA) || file == "/run/pki-bootstrap/bootstrap.csr" && environmentPreflightBytesSHA256(value) != r.OriginalCSRSHA || file == r.RenewedCertPath && string(value) != material["tls.crt"] {
			return bad("custody")
		}
	}
	if observed.Cleanup.SecretUID != observed.UIDs[s.BootstrapCredentialNamespace] {
		return bad("cleanup-secret")
	}
	namespace, err := runtime.Backend.Read(ctx, "Namespace//"+s.BootstrapCredentialNamespace)
	if err != nil || namespace == nil || deploymentplan.Map(namespace["metadata"])["uid"] != observed.Cleanup.NamespaceUID {
		return bad("cleanup-namespace")
	}
	authentication := *r
	authentication.Ceremony, authentication.Intent = s, r.RenewedIntent
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, runtime.Backend, &authentication); err != nil {
		return empty, true, err
	}
	return s, true, e.Check(ctx)
}
