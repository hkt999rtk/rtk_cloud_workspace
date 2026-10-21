package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func findEnvironmentCreateDomainPolicy(p environmentCreateBootstrapPlan, domain string) environmentCreateIssuerPolicy {
	for _, policy := range p.Domains {
		if policy.Domain == domain {
			return policy
		}
	}
	return environmentCreateIssuerPolicy{}
}

func installEnvironmentCreateOpenBaoPolicy(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, name, text string) error {
	if runtime.Execution == nil || !secretEnvironmentPattern.MatchString(name) || text == "" || len(text) > 64<<10 {
		return environmentCreateBootstrapBlocker("exact recorded native initial OpenBao policy required")
	}
	intent := deploymentplan.Object{"environment": cfg.Environment, "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(text))}
	body, _ := json.Marshal(intent)
	key := "openbao-policy-" + name
	reuse := false
	closeRecorded := false
	observePending := false
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key != key {
			continue
		}
		if reuse || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intent) || entry.StartedAt.IsZero() {
			return environmentCreateBootstrapBlocker("policy has an uncertain or changed original outcome")
		}
		switch entry.State {
		case "pending":
			if runtime.ProductIdentityScope == "" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return environmentCreateBootstrapBlocker("policy has an uncertain or changed original outcome")
			}
			observePending = true
		case "allocated", "completed":
			if (entry.State == "completed") == entry.CompletedAt.IsZero() || activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(intent) {
				return environmentCreateBootstrapBlocker("policy has an uncertain or changed original outcome")
			}
		default:
			return environmentCreateBootstrapBlocker("policy has an uncertain or changed original outcome")
		}
		reuse = true
		closeRecorded = entry.State != "completed"
	}
	if !reuse {
		if err := runtime.Execution.BeginEntry(key, "bootstrap", body, time.Now().UTC()); err != nil {
			return err
		}
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original selected OpenBao policy owner cannot be read")
	}
	if observePending {
		// A successful native list distinguishes definitive absence from a
		// failed read. Only that known zero-effect prefix can perform the
		// original write; an existing exact policy needs readback only.
		list, er := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy list -format=json\n")
		var names []string
		if er != nil || json.Unmarshal(list, &names) != nil || names == nil {
			return environmentCreateBootstrapBlocker("original product policy existence cannot be verified")
		}
		if !slices.Contains(names, name) {
			if err = runtime.Execution.Check(ctx); err != nil {
				return err
			}
			reuse = false
		}
	}
	script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy write " + name + " - >/dev/null\nbao policy read " + name + "\n"
	if reuse {
		script = "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read " + name + "\n"
	}
	actual, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(append(bytes.TrimSpace(token), '\n'), []byte(text)...), script)
	if err != nil || strings.TrimSpace(string(actual)) != strings.TrimSpace(text) {
		return environmentCreateBootstrapBlocker("actual installed bootstrap policy differs from exact native policy")
	}
	if reuse {
		if err := runtime.Execution.Check(ctx); err != nil {
			return err
		}
		if closeRecorded {
			if observePending {
				if err := runtime.Execution.RecordReceipt(key, body); err != nil {
					return err
				}
			}
			return runtime.Execution.CompleteEntry(key, time.Now().UTC())
		}
		return nil
	}
	if err := runtime.Execution.RecordReceipt(key, body); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

func environmentCreateControllerPolicies(runtime environmentCreateBootstrapRuntime) []string {
	var policies []string
	if runtime.Execution != nil {
		for _, entry := range runtime.Execution.Journal.Entries {
			if strings.HasPrefix(entry.Key, "openbao-policy-create-") && strings.HasSuffix(entry.Key, "-controller") && entry.State == "completed" {
				policies = append(policies, strings.TrimPrefix(entry.Key, "openbao-policy-"))
			}
		}
	}
	slices.Sort(policies)
	return policies
}

func allocateEnvironmentCreateUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func renderEnvironmentCreateCustodyReader(cfg deploymentConfig, p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony) deploymentplan.Definition {
	labels := deploymentplan.Object{"app.kubernetes.io/name": s.CustodyReader}
	env := environmentCreateEnv(map[string]string{"PKI_ENVIRONMENT": cfg.Environment, "PKI_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID, "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"})
	env = append(env, deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-migration-database", "key": "url"}}})
	container := deploymentplan.Object{"name": "custody", "image": p.Image, "command": []any{"/bin/sh", "-ec", "exec sleep 3600"}, "env": env, "securityContext": deploymentplan.Object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}, "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": "/run/pki-bootstrap"}}}
	pod := deploymentplan.Object{"automountServiceAccountToken": false, "imagePullSecrets": []any{deploymentplan.Object{"name": "ghcr-pull"}}, "securityContext": deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}, "containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": s.BootstrapClaim}}}}
	return deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": s.CustodyReader, "namespace": s.BootstrapCredentialNamespace}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": pod}}}}
}

// A native one-shot Job issues the original session credential. This adapter
// observes and copies that exact key; it cannot generate a replacement or mark
// an installation acknowledged on a workload's behalf.
func createEnvironmentServiceSession(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, subjects []string, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	if s.Root.Status != "active" || s.Intermediate.Status != "active" || len(subjects) == 0 {
		return s, environmentCreateBootstrapBlocker("actual active Service lineage and exact intended workload subjects required")
	}
	for _, subject := range subjects {
		if !slices.Contains(findEnvironmentCreateDomainPolicy(p, "service").ServiceClientIDs, subject) || subject == "service:deployment-bootstrap" {
			return s, environmentCreateBootstrapBlocker("bootstrap session subject is outside its registered exact workload policy")
		}
	}
	var err error
	var policy environmentCreateOpenBaoIssuerPolicies
	if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.Renewal {
		s, err = executeOriginalKeySessionRenewal(ctx, cfg, store, runtime, s, apply)
		if err != nil {
			return s, err
		}
		raw, readErr := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
		if readErr != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.IssuerID != s.Intermediate.ID || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
			return s, environmentCreateBootstrapBlocker("original active Service signer policy cannot be read")
		}
	} else if runtime.ServiceSessionRecovery != nil {
		intent := runtime.ServiceSessionRecovery.Intent
		s.BootstrapSessionID = intent.SessionID
		s.BootstrapCredentialNamespace = p.Stack + "-video-cloud"
		s.BootstrapCredentialNamespaces = append([]string(nil), intent.Namespaces...)
		s.BootstrapCredentialSecret = "service-bootstrap-" + s.BootstrapSessionID
		s.BootstrapClaim = intent.Claim
		s.CustodyReader = "pki-bootstrap-custody-" + runtime.ExecutionSHA256[:12]
		if err := applyRecoveredServiceSession(ctx, runtime, apply); err != nil {
			return s, err
		}
		raw, readErr := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
		if readErr != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.IssuerID != s.Intermediate.ID || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
			return s, environmentCreateBootstrapBlocker("original active Service signer policy cannot be read")
		}
	} else {
		s.BootstrapSessionID, err = allocateEnvironmentCreateUUID()
		if err != nil {
			return s, err
		}
		s.BootstrapCredentialNamespace = p.Stack + "-video-cloud"
		s.BootstrapCredentialNamespaces = []string{s.BootstrapCredentialNamespace, p.Stack + "-account-manager", p.Stack + "-secrets"}
		s.BootstrapCredentialSecret = "service-bootstrap-" + s.BootstrapSessionID
		s.BootstrapClaim = "pki-service-bootstrap-state"
		s.CustodyReader = "pki-bootstrap-custody-" + runtime.ExecutionSHA256[:12]
		bootstrapRole, bootstrapAccount := "pki-service-bootstrap-"+cfg.Environment, "pki-service-bootstrap"
		if runtime.ProductIdentityScope != "" {
			if !environmentPreflightDigest.MatchString(runtime.ProductIdentityScope) || runtime.PKIClient == nil || runtime.PKIClient.mutationScope != runtime.ProductIdentityScope {
				return s, environmentCreateBootstrapBlocker("product session requires the exact qualified existing-PKI lifecycle")
			}
			s.BootstrapClaim = "pki-product-bootstrap-" + runtime.ProductIdentityScope[:12]
			s.CustodyReader = "pki-product-custody-" + runtime.ProductIdentityScope[:12]
			s.BootstrapCredentialNamespaces = []string{s.BootstrapCredentialNamespace}
			bootstrapRole, bootstrapAccount = "pki-product-bootstrap-"+runtime.ProductIdentityScope[:12], "pki-product-bootstrap-"+runtime.ProductIdentityScope[:12]
			path, pathErr := store.safePath(filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "session-intent.json"))
			if pathErr != nil {
				return s, pathErr
			}
			if _, statErr := os.Lstat(path); statErr == nil {
				var original struct {
					Session    string   `json:"session_id"`
					Claim      string   `json:"claim"`
					Subjects   []string `json:"subjects"`
					Issuer     string   `json:"issuer_id"`
					Root       string   `json:"root_sha256"`
					Namespaces []string `json:"credential_namespaces"`
				}
				raw, readErr := readBoundedEnvironmentFile(path, true)
				if readErr != nil || decodeEnvironmentJSON(raw, &original) != nil || !environmentBootstrapOwnerUUID.MatchString(original.Session) || original.Claim != s.BootstrapClaim || !slices.Equal(original.Subjects, subjects) || original.Issuer != s.Intermediate.ID || original.Root != s.Root.RootFingerprint || !slices.Equal(original.Namespaces, s.BootstrapCredentialNamespaces) {
					return s, environmentCreateBootstrapBlocker("original product session intent changed; no replacement session is permitted")
				}
				s.BootstrapSessionID = original.Session
				s.BootstrapCredentialSecret = "service-bootstrap-" + original.Session
			} else if !os.IsNotExist(statErr) {
				return s, environmentCreateBootstrapBlocker("original product session intent cannot be inspected")
			}
		}
		writer := writeEnvironmentCreateBootstrapJSON
		if runtime.ProductIdentityScope != "" {
			writer = writeProductArtifact
		}
		if _, err := writer(store, filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "session-intent.json"), deploymentplan.Object{"session_id": s.BootstrapSessionID, "claim": s.BootstrapClaim, "subjects": subjects, "issuer_id": s.Intermediate.ID, "root_sha256": s.Root.RootFingerprint, "credential_namespaces": s.BootstrapCredentialNamespaces}); err != nil {
			return s, err
		}
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
		if err != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.IssuerID != s.Intermediate.ID || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
			return s, environmentCreateBootstrapBlocker("native active Service signer policy cannot be read")
		}
		sessionPolicy := "rtk-" + cfg.Environment + "-service-bootstrap-" + s.BootstrapSessionID
		if err := installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, sessionPolicy, policy.ServiceClientSignerPolicy); err != nil {
			return s, err
		}
		if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, bootstrapRole, []string{bootstrapAccount}, []string{sessionPolicy}); err != nil {
			return s, err
		}
		definitions, err := renderEnvironmentCreatePKIPhase(ctx, cfg, p, "service-bootstrap", "--openbao-ca-configmap", "pki-openbao-transport-ca", "--service-bootstrap-subject", "service:deployment-bootstrap", "--service-bootstrap-subjects", strings.Join(subjects, ","), "--service-bootstrap-issuer-id", s.Intermediate.ID, "--service-bootstrap-issuer-mount", s.Intermediate.Reference, "--service-bootstrap-root-sha256", s.Root.RootFingerprint, "--service-bootstrap-deployment-id", "create-"+runtime.ExecutionSHA256[:32], "--service-bootstrap-session-id", s.BootstrapSessionID, "--service-bootstrap-state-claim", s.BootstrapClaim)
		if err != nil {
			return s, err
		}
		if runtime.ProductIdentityScope != "" {
			for _, d := range definitions {
				if d.Object["kind"] == "ServiceAccount" {
					deploymentplan.Map(d.Object["metadata"])["name"] = bootstrapAccount
				}
				if d.Object["kind"] == "Job" {
					pod := environmentPKIPod(d.Object)
					pod["serviceAccountName"] = bootstrapAccount
					container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
					container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"OPENBAO_KUBERNETES_ROLE": bootstrapRole, "PKI_BOOTSTRAP_DEPLOYMENT_ID": "product-" + runtime.ProductIdentityScope[:48]}))
				}
			}
		}
		if runtime.ProductIdentityOpenBaoTrustProfile != "" {
			if runtime.ProductIdentityScope == "" || runtime.ProductIdentityOpenBaoTrustProfile != productIdentityManagedOpenBaoTrustProfile {
				return s, environmentCreateBootstrapBlocker("Service session has an unknown managed OpenBao trust profile")
			}
			definitions, err = bindEnvironmentCreateManagedOpenBaoTrust(definitions, p.Stack)
			if err != nil {
				return s, err
			}
		}
		if err := apply(ctx, "service-session", definitions); err != nil {
			return s, err
		}
	}
	reader := renderEnvironmentCreateCustodyReader(cfg, p, s)
	if r := runtime.ServiceSessionRecovery; r != nil && r.Renewal {
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(reader.Object)["containers"])[0])
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": r.RenewedCertPath}))
	}
	if err := apply(ctx, renewalStage(runtime, "service-custody-reader"), []deploymentplan.Definition{reader}); err != nil {
		return s, err
	}
	material := map[string]string{}
	for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		path := "/run/pki-bootstrap/" + name
		if r := runtime.ServiceSessionRecovery; r != nil && r.Renewal && name == "bootstrap.crt" {
			path = r.RenewedCertPath
		}
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
			return s, environmentCreateBootstrapBlocker("original completed bootstrap custody material is unavailable; it is never regenerated")
		}
		material[name] = string(raw)
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.Renewal && (environmentPreflightBytesSHA256([]byte(material["bootstrap.key"])) != r.OriginalKeySHA || environmentPreflightBytesSHA256([]byte(material["bootstrap.csr"])) != r.OriginalCSRSHA) {
		return s, environmentCreateBootstrapBlocker("bootstrap renewal changed the original private key or CSR")
	}
	identity, err := tls.X509KeyPair([]byte(material["bootstrap.crt"]), []byte(material["bootstrap.key"]))
	if err != nil || len(identity.Certificate) < 2 {
		return s, environmentCreateBootstrapBlocker("native original session certificate and key differ")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(identity.Certificate[len(identity.Certificate)-1]) != s.Root.RootFingerprint {
		return s, environmentCreateBootstrapBlocker("native original session credential belongs to another caller or lineage")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'"
	sessionRaw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var observed struct {
		SessionID   string    `json:"session_id"`
		IssuerID    string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Fingerprint string    `json:"bootstrap_fingerprint"`
		Status      string    `json:"status"`
		Subjects    []string  `json:"subjects"`
		Expires     time.Time `json:"expires_at"`
	}
	if err != nil || decodeEnvironmentJSON(sessionRaw, &observed) != nil || observed.SessionID != s.BootstrapSessionID || observed.IssuerID != s.Intermediate.ID || observed.Root != s.Root.RootFingerprint || observed.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || observed.Status != "active" || !slices.Equal(observed.Subjects, subjects) || !observed.Expires.After(time.Now().UTC()) {
		return s, environmentCreateBootstrapBlocker("actual native session differs from original credential and workload policy")
	}
	var secrets []deploymentplan.Definition
	for _, namespace := range s.BootstrapCredentialNamespaces {
		definition := environmentCreateSecret(namespace, s.BootstrapCredentialSecret, map[string]string{"tls.crt": material["bootstrap.crt"], "tls.key": material["bootstrap.key"], "ca.crt": s.Root.Certificate})
		definition.Object["immutable"] = true
		deploymentplan.Map(definition.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "rtk.cloud/bootstrap-session": s.BootstrapSessionID}
		secrets = append(secrets, definition)
	}
	verifier, err := runtime.Backend.Read(ctx, "Secret/"+s.BootstrapCredentialNamespace+"/pki-verifier-database")
	if err != nil || verifier == nil {
		return s, environmentCreateBootstrapBlocker("original runtime verifier credential is unavailable")
	}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		if namespace == s.BootstrapCredentialNamespace {
			continue
		}
		secrets = append(secrets, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-verifier-database", "namespace": namespace}, "type": "Opaque", "data": deploymentplan.Map(verifier["data"])}})
		manifest, err := environmentCreateServiceManifest(s.Root, s.Intermediate)
		if err != nil {
			return s, err
		}
		secrets = append(secrets, environmentCreateServiceTrustDefinition(namespace, s.Root, runtime.TransportCAPEM, manifest))
	}
	if err := apply(ctx, renewalStage(runtime, "service-credential-secrets"), secrets); err != nil {
		return s, err
	}
	s.CredentialUIDs = map[string]string{}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		actual, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		if err != nil || actual == nil {
			return s, environmentCreateBootstrapBlocker("original actual session credential UID cannot be recorded")
		}
		uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return s, environmentCreateBootstrapBlocker("actual session credential lacks canonical native owner")
		}
		s.CredentialUIDs[namespace] = uid
	}
	namespace, err := runtime.Backend.Read(ctx, "Namespace//"+s.BootstrapCredentialNamespace)
	if err != nil || namespace == nil {
		return s, environmentCreateBootstrapBlocker("original bootstrap namespace UID cannot be observed")
	}
	var frozen struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(runtime.Backend.KubeconfigContent, &frozen) != nil || len(frozen.Clusters) != 1 {
		return s, environmentCreateBootstrapBlocker("original frozen Kubernetes CA cannot be bound to session cleanup")
	}
	s.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: cfg.Environment, SessionID: s.BootstrapSessionID, NamespaceUID: deploymentplan.String(deploymentplan.Map(namespace["metadata"])["uid"]), SecretUID: s.CredentialUIDs[s.BootstrapCredentialNamespace], PolicySHA256: environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)), KubernetesCA: deploymentplan.String(frozen.Clusters[0].Cluster["certificate-authority-data"])}
	if r := runtime.ServiceSessionRecovery; runtime.ProductIdentityScope != "" || r != nil && (r.OrderForward || r.ApplicationForward) {
		s.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + s.BootstrapSessionID[:12]
	}
	if err := validateEnvironmentBootstrapCleanupBinding(&s.CleanupBinding, cfg.Environment); err != nil {
		return s, err
	}
	sessionPolicy := "rtk-" + cfg.Environment + "-service-bootstrap-" + s.BootstrapSessionID
	cleanupText := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+sessionPolicy)
	if err := installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, "create-"+s.BootstrapSessionID+"-cleanup-controller", cleanupText); err != nil {
		return s, err
	}
	if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, environmentCreateControllerPolicies(runtime)); err != nil {
		return s, err
	}
	observedPath := filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "session-observed.json")
	if r := runtime.ServiceSessionRecovery; r != nil && r.Renewal {
		observedPath = filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "observed.json")
	}
	value := deploymentplan.Object{"session": json.RawMessage(sessionRaw), "cleanup_binding": s.CleanupBinding, "credential_uids": s.CredentialUIDs, "custody_reader": s.CustodyReader}
	writer := writeEnvironmentCreateBootstrapJSON
	if runtime.ProductIdentityScope != "" {
		writer = writeProductArtifact
	} else {
		// A completed retry still reobserves the native session, original
		// custody, credential owners and cleanup policy above. Retain only an
		// identical original observation; never rewrite completion evidence.
		path, pathErr := privateDeploymentStatePath(store.Environment, observedPath)
		if pathErr != nil || path != filepath.Join(store.Root, observedPath) || runtime.Execution == nil || runtime.Execution.Store.Root != store.Root {
			return s, environmentCreateBootstrapBlocker("original session observation requires its private canonical owner")
		}
		if err := runtime.Execution.Check(ctx); err != nil {
			return s, err
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			raw, readErr := readBoundedEnvironmentFile(path, true)
			var original, expected deploymentplan.Object
			encoded, encodeErr := json.Marshal(value)
			if readErr != nil || encodeErr != nil || decodeEnvironmentJSON(raw, &original) != nil || decodeEnvironmentJSON(encoded, &expected) != nil || deploymentplan.Digest(original) != deploymentplan.Digest(expected) {
				return s, environmentCreateBootstrapBlocker("original session observation changed or cannot be inspected")
			}
			return s, runtime.Execution.Check(ctx)
		} else if !os.IsNotExist(statErr) {
			return s, environmentCreateBootstrapBlocker("original session observation cannot be inspected")
		}
	}
	if _, err := writer(store, observedPath, value); err != nil {
		return s, err
	}
	return s, nil
}
