package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Prepare the existing managed renewal mechanism from the original session
// stage when the first seed expires after some native identities were issued.
// This function is read-only; the caller must bind and admit the renewal plan.
func preparePartialIssuerSessionRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("expired session recovery requires the original session, credential owners and matching key/CSR custody")
	}
	if r == nil || r.ManagedTransportRecovery == nil || e == nil || e.Store.Root != store.Root {
		return bad()
	}
	var intent createServiceSessionIntent
	var observed createServiceSessionObserved
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if readExecutionJSON(filepath.Join(base, "session-intent.json"), &intent) != nil || readExecutionJSON(filepath.Join(base, "session-observed.json"), &observed) != nil ||
		intent.SessionID != r.Ceremony.BootstrapSessionID || intent.Claim != r.Ceremony.BootstrapClaim || !slices.Equal(intent.Namespaces, r.Ceremony.BootstrapCredentialNamespaces) {
		return bad()
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, intent.SessionID)
	if err != nil || !issuerEnrollmentSessionExpired(intent, state, time.Now().UTC()) {
		return bad()
	}
	_, credentials, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets")
	if err != nil || len(intent.Namespaces) == 0 {
		return bad()
	}
	var key, cert, ca []byte
	for _, ns := range intent.Namespaces {
		id := "Secret/" + ns + "/service-bootstrap-" + intent.SessionID
		secret, readErr := b.Read(ctx, id)
		uid := credentials.Owners[id]
		if readErr != nil || secret == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || observed.UIDs[ns] != uid || deploymentplan.Map(secret["metadata"])["uid"] != uid {
			return bad()
		}
		data := deploymentplan.Map(secret["data"])
		k, e1 := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
		c, e2 := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
		root, e3 := base64.StdEncoding.DecodeString(deploymentplan.String(data["ca.crt"]))
		if e1 != nil || e2 != nil || e3 != nil || len(k) == 0 || len(c) == 0 || len(root) == 0 || key != nil && (!bytes.Equal(key, k) || !bytes.Equal(cert, c) || !bytes.Equal(ca, root)) {
			return bad()
		}
		key, cert, ca = k, c, root
	}
	reader := r.Ceremony.CustodyReader
	for name, want := range map[string][]byte{"bootstrap.key": key, "bootstrap.crt": cert} {
		actual, readErr := b.command(ctx, nil, "--namespace", intent.Namespaces[0], "exec", "deployment/"+reader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/"+name)
		if readErr != nil || !bytes.Equal(actual, want) {
			return bad()
		}
	}
	csrPEM, err := b.command(ctx, nil, "--namespace", intent.Namespaces[0], "exec", "deployment/"+reader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.csr")
	if err != nil || !partialSessionCustodyValid(key, cert, ca, csrPEM, r.Ceremony, state, time.Now().UTC()) {
		return bad()
	}
	if _, err := readManagedSessionInventory(ctx, cfg, runtime, r.Ceremony, intent.Subjects, state, true); err != nil {
		return err
	}
	r.Intent, r.RenewedIntent = intent, intent
	r.Renewal, r.PostServiceRenewal = true, true
	r.SourceRenewalStage = "kubernetes/service-session"
	r.RenewedCertPath = "/run/pki-bootstrap/bootstrap.crt"
	r.OriginalKeySHA, r.OriginalCSRSHA = environmentPreflightBytesSHA256(key), environmentPreflightBytesSHA256(csrPEM)
	r.ManagedTransportRecovery.PartialSessionRenewal = true
	return e.Check(ctx)
}

func partialSessionCustodyValid(key, cert, ca, csrPEM []byte, service environmentCreateServiceCeremony, state managedSessionState, now time.Time) bool {
	return partialSessionCertificateValid(key, cert, ca, csrPEM, service, state, now, true)
}

func partialSessionSuccessorCustodyValid(key, cert, ca, csrPEM []byte, service environmentCreateServiceCeremony, state managedSessionState, now time.Time) bool {
	return partialSessionCertificateValid(key, cert, ca, csrPEM, service, state, now, false)
}

func partialSessionCertificateValid(key, cert, ca, csrPEM []byte, service environmentCreateServiceCeremony, state managedSessionState, now time.Time, expired bool) bool {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) < 2 || string(ca) != service.Root.Certificate {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	issuer, issuerErr := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil || issuerErr != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.CheckSignatureFrom(issuer) != nil || now.Before(leaf.NotBefore) || expired && leaf.NotAfter.After(now) || !expired && !leaf.NotAfter.After(now) || leaf.NotAfter.Before(state.Expires) || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != service.Root.RootFingerprint {
		return false
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return false
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	return err == nil && csr.CheckSignature() == nil && csr.Subject.CommonName == leaf.Subject.CommonName && bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo)
}

// Earlier accepted transport stages already own the trust ConfigMaps and
// verifier credentials. Renewing only the session must not roll those owners
// back to the old enrollment template.
func partialSessionCredentialDefinitions(definitions []deploymentplan.Definition, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if !environmentBootstrapOwnerUUID.MatchString(service.BootstrapSessionID) || service.BootstrapCredentialSecret != "service-bootstrap-"+service.BootstrapSessionID || len(service.BootstrapCredentialNamespaces) == 0 {
		return nil, environmentCreateBootstrapBlocker("partial session renewal requires its exact original credential namespaces")
	}
	wanted := map[string]bool{}
	for _, ns := range service.BootstrapCredentialNamespaces {
		id := "Secret/" + ns + "/" + service.BootstrapCredentialSecret
		if wanted[id] {
			return nil, environmentCreateBootstrapBlocker("partial session credential namespace is duplicated")
		}
		wanted[id] = true
	}
	var selected []deploymentplan.Definition
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		pending, intended := wanted[id]
		if !intended {
			continue
		}
		if !pending {
			return nil, environmentCreateBootstrapBlocker("partial session credential was rendered more than once")
		}
		if definition.Object["kind"] != "Secret" || definition.Object["immutable"] != true {
			return nil, environmentCreateBootstrapBlocker("partial session credential is not the intended immutable Secret")
		}
		wanted[id] = false
		selected = append(selected, definition)
	}
	if len(selected) != len(service.BootstrapCredentialNamespaces) {
		return nil, environmentCreateBootstrapBlocker("partial session renewal did not render each original namespace credential")
	}
	return selected, nil
}

func verifyPartialSessionOpenBaoTrust(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, root environmentCreateRegisteredIssuer) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("managed OpenBao trust requires the original recorded owner and active root")
	}
	if e == nil || e.Store.Root != store.Root || !environmentPreflightDigest.MatchString(root.RootFingerprint) {
		return bad()
	}
	resources, err := completedInitialTransportSuccessorResources(ctx, store, e, b)
	if err != nil {
		return "", err
	}
	id := "ConfigMap/" + e.Journal.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	var owner *activeTransportRenewalResource
	for i := range resources {
		if resources[i].ID == id {
			if owner != nil {
				return bad()
			}
			owner = &resources[i]
		}
	}
	if owner == nil || !environmentBootstrapOwnerUUID.MatchString(owner.UID) {
		return bad()
	}
	actual, err := b.Read(ctx, id)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, actual) || !managedLateSeedForwardRoots(deploymentplan.String(deploymentplan.Map(actual["data"])["roots.pem"]), root.RootFingerprint, time.Now().UTC()) {
		return bad()
	}
	return deploymentplan.Digest(owner), e.Check(ctx)
}
