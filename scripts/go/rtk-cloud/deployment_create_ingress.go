package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This finite profile is frozen before allocation; admission is separate from
// Product PKI. Only this admitted parent may initialize its first private key.
const environmentCreateIngressAdmissionProfile = "native-nginx-admission-p256-v1"
const environmentCreateIngressAdmissionCAValidity = 2 * 365 * 24 * time.Hour
const environmentCreateIngressAdmissionServerValidity = 365 * 24 * time.Hour
const environmentCreateIngressAdmissionSecret = "ingress-nginx-admission"
const environmentCreateIngressAdmissionEntry = "bootstrap/ingress-admission"

type environmentCreateIngressAdmissionMaterial struct {
	Schema         string    `json:"schema"`
	Environment    string    `json:"environment"`
	Stack          string    `json:"stack"`
	Profile        string    `json:"profile"`
	PlanSHA256     string    `json:"plan_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	CAPEM          string    `json:"ca_pem"`
	CertificatePEM string    `json:"certificate_pem"`
	PrivateKeyPEM  string    `json:"private_key_pem"`
}

func environmentCreateIngressAdmissionNames(stack string) []string {
	return []string{"ingress-nginx-controller-admission." + stack + "-ingress.svc", "ingress-nginx-controller-admission." + stack + "-ingress.svc.cluster.local"}
}

func environmentCreateIngressAdmissionMaterialPath(planSHA string) string {
	return filepath.Join("deployment", "create", "material", planSHA, "ingress-admission", "identity.json")
}

func environmentCreateIngressAdmissionIntent(p environmentCreateBootstrapPlan) deploymentplan.Object {
	return deploymentplan.Object{"profile": p.IngressAdmissionProfile, "controller_image": p.Images["LKE_INGRESS_IMAGE"], "secret": "Secret/" + p.Stack + "-ingress/" + environmentCreateIngressAdmissionSecret, "dns_names": environmentCreateIngressAdmissionNames(p.Stack), "ca_validity_hours": environmentCreateIngressAdmissionCAValidity.Hours(), "server_validity_hours": environmentCreateIngressAdmissionServerValidity.Hours()}
}

func validateEnvironmentCreateIngressAdmissionRecipe(cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease) error {
	image := release.Images["LKE_INGRESS_IMAGE"].Reference
	if cfg.Environment != "dev" && cfg.Environment != "staging" || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Stack != "video-cloud-"+cfg.Environment || release.Environment != p.Environment || release.Stack != p.Stack || p.IngressAdmissionProfile != environmentCreateIngressAdmissionProfile || p.Images["LKE_INGRESS_IMAGE"] != image || !immutableEnvironmentImage.MatchString(image) || environmentImageRepository(image) != "registry.k8s.io/ingress-nginx/controller" {
		return ingressAdmissionBlocker(p.Stack, "fresh admission requires the frozen native profile and exact official ingress controller image")
	}
	return nil
}

func validateEnvironmentCreateIngressAdmissionMaterial(p environmentCreateBootstrapPlan, planSHA string, m environmentCreateIngressAdmissionMaterial, now time.Time) error {
	block := func() error {
		return ingressAdmissionBlocker(p.Stack, "original generated admission material has invalid scope, key, validity or exact Service DNS policy")
	}
	if m.Schema != "rtk-cloud.native-ingress-admission/v1" || m.Environment != p.Environment || m.Stack != p.Stack || m.Profile != environmentCreateIngressAdmissionProfile || m.PlanSHA256 != planSHA || !environmentPreflightDigest.MatchString(planSHA) || m.CreatedAt.IsZero() || m.CreatedAt.After(now) {
		return block()
	}
	caBlock, rest := pem.Decode([]byte(m.CAPEM))
	if caBlock == nil || caBlock.Type != "CERTIFICATE" || len(rest) != 0 {
		return block()
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || ca.CheckSignatureFrom(ca) != nil || !ca.NotAfter.Equal(m.CreatedAt.Truncate(time.Second).Add(environmentCreateIngressAdmissionCAValidity)) {
		return block()
	}
	pair, err := tls.X509KeyPair([]byte(m.CertificatePEM), []byte(m.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) != 1 {
		return block()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || leaf.CheckSignatureFrom(ca) != nil || !slices.Equal(leaf.DNSNames, environmentCreateIngressAdmissionNames(p.Stack)) || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) || !leaf.NotAfter.Equal(m.CreatedAt.Truncate(time.Second).Add(environmentCreateIngressAdmissionServerValidity)) {
		return block()
	}
	if key, ok := pair.PrivateKey.(*ecdsa.PrivateKey); !ok || key.Curve != elliptic.P256() {
		return block()
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, name := range environmentCreateIngressAdmissionNames(p.Stack) {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now}); err != nil {
			return block()
		}
	}
	return nil
}

func prepareEnvironmentCreateIngressAdmissionMaterial(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, execution *environmentCreateExecution) (environmentCreateIngressAdmissionMaterial, error) {
	var m environmentCreateIngressAdmissionMaterial
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if execution == nil || execution.Store.Root != store.Root || store.Environment != cfg.Environment || execution.Journal.Environment != cfg.Environment || execution.Journal.Stack != p.Stack || !environmentPreflightDigest.MatchString(execution.Journal.PlanSHA256) {
		return m, ingressAdmissionBlocker(p.Stack, "new admission material requires its original canonical parent execution")
	}
	if err := execution.Check(ctx); err != nil {
		return m, err
	}
	if err := validateEnvironmentCreateIngressAdmissionRecipe(cfg, p, release); err != nil {
		return m, err
	}
	relative := environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256)
	file, err := store.safePath(relative)
	if err != nil {
		return m, err
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		return m, ingressAdmissionBlocker(p.Stack, "recorded or conflicting admission material cannot be replaced by fresh initialization")
	}
	intent, _ := json.Marshal(environmentCreateIngressAdmissionIntent(p))
	if err := execution.BeginEntry(environmentCreateIngressAdmissionEntry, "ingress-admission", intent, time.Now().UTC()); err != nil {
		return m, err
	}
	now := time.Now().UTC()
	m = environmentCreateIngressAdmissionMaterial{Schema: "rtk-cloud.native-ingress-admission/v1", Environment: cfg.Environment, Stack: p.Stack, Profile: p.IngressAdmissionProfile, PlanSHA256: execution.Journal.PlanSHA256, CreatedAt: now}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return m, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return m, err
	}
	serial := func() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
	caSerial, err := serial()
	if err != nil {
		return m, err
	}
	leafSerial, err := serial()
	if err != nil {
		return m, err
	}
	ca := &x509.Certificate{SerialNumber: caSerial, Subject: pkix.Name{CommonName: p.Stack + " ingress admission CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(environmentCreateIngressAdmissionCAValidity), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, MaxPathLen: 0, MaxPathLenZero: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return m, err
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		return m, err
	}
	names := environmentCreateIngressAdmissionNames(p.Stack)
	leaf := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(environmentCreateIngressAdmissionServerValidity), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		return m, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return m, err
	}
	m.CAPEM, m.CertificatePEM, m.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	if err := validateEnvironmentCreateIngressAdmissionMaterial(p, execution.Journal.PlanSHA256, m, time.Now().UTC()); err != nil {
		return m, err
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, m); err != nil {
		return m, err
	}
	return m, nil
}

func renderEnvironmentCreateIngressAdmission(cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, m environmentCreateIngressAdmissionMaterial) ([]deploymentplan.Definition, error) {
	if err := validateEnvironmentCreateIngressAdmissionRecipe(cfg, p, release); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateIngressAdmissionMaterial(p, m.PlanSHA256, m, time.Now().UTC()); err != nil {
		return nil, err
	}
	binding := &environmentAdmissionBinding{SecretName: environmentCreateIngressAdmissionSecret, WebhookName: p.Stack + "-ingress-admission", CABundle: base64.StdEncoding.EncodeToString([]byte(m.CAPEM))}
	definitions, err := renderEnvironmentIngress(environmentCreateProviderValues(cfg), binding, release)
	if err != nil {
		return nil, err
	}
	secret := environmentCreateSecret(p.Stack+"-ingress", binding.SecretName, map[string]string{"ca": m.CAPEM, "cert": m.CertificatePEM, "key": m.PrivateKeyPEM})
	secret.Object["immutable"] = true
	deploymentplan.Map(secret.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	return append([]deploymentplan.Definition{secret}, definitions...), nil
}

func verifyEnvironmentCreateIngressAdmissionSecret(p environmentCreateBootstrapPlan, m environmentCreateIngressAdmissionMaterial, secret deploymentplan.Object, originalUID string) error {
	meta := deploymentplan.Map(secret["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if deploymentplan.ID(secret) != "Secret/"+p.Stack+"-ingress/"+environmentCreateIngressAdmissionSecret || uid == "" || originalUID != "" && uid != originalUID || meta["deletionTimestamp"] != nil || deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] != p.Stack || secret["immutable"] != true || len(deploymentplan.Map(secret["data"])) != 3 {
		return ingressAdmissionBlocker(p.Stack, "admission Secret lost its original immutable Kubernetes owner")
	}
	for key, expected := range map[string]string{"ca": m.CAPEM, "cert": m.CertificatePEM, "key": m.PrivateKeyPEM} {
		decoded, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
		if err != nil || string(decoded) != expected {
			return ingressAdmissionBlocker(p.Stack, "actual admission Secret differs from its original persisted native identity")
		}
	}
	return nil
}

func environmentCreateIngressAdmissionOwner(execution *environmentCreateExecution, stage, id string) (string, error) {
	for _, entry := range execution.Journal.Entries {
		if entry.Key != "kubernetes/"+stage || entry.State != "completed" {
			continue
		}
		var receipt struct {
			PlanSHA256 string            `json:"plan_sha256"`
			Owners     map[string]string `json:"owners"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && environmentPreflightDigest.MatchString(receipt.PlanSHA256) && receipt.Owners[id] != "" {
			return receipt.Owners[id], nil
		}
	}
	return "", ingressAdmissionBlocker(execution.Journal.Stack, "native admission readback lacks its original accepted parent stage owner")
}

// The normal tail may finish this sole pending admission only from its exact
// accepted identity child. Reading that original material grants no generation
// or identity-stage authority.
func loadEnvironmentCreatePendingIngressAdmission(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, execution *environmentCreateExecution) (environmentCreateIngressAdmissionMaterial, []deploymentplan.Definition, error) {
	return loadEnvironmentCreateIngressAdmissionSource(ctx, cfg, p, release, store, execution, false)
}

func loadEnvironmentCreateIngressAdmissionSource(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, execution *environmentCreateExecution, completed bool) (environmentCreateIngressAdmissionMaterial, []deploymentplan.Definition, error) {
	var m environmentCreateIngressAdmissionMaterial
	bad := func() (environmentCreateIngressAdmissionMaterial, []deploymentplan.Definition, error) {
		return m, nil, ingressAdmissionBlocker(p.Stack, "pending ingress requires its exact original material and accepted identity child")
	}
	count := 433
	if completed {
		count = 434
	}
	if completed && execution != nil && managedLateRedisPrePauseSelected(execution.Journal) {
		if err := verifyManagedLateRedisPrePause(ctx, store, execution); err != nil {
			return m, nil, err
		}
		count = 437
	}
	if execution == nil || execution.Store != store || execution.Check(ctx) != nil || len(execution.Journal.Entries) != count || !execution.Journal.CompletedAt.IsZero() || validateEnvironmentCreateIngressAdmissionRecipe(cfg, p, release) != nil {
		return bad()
	}
	entry, identity := execution.Journal.Entries[431], execution.Journal.Entries[432]
	if entry.Key != environmentCreateIngressAdmissionEntry || entry.Phase != "ingress-admission" || entry.StartedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(environmentCreateIngressAdmissionIntent(p)) || identity.Key != "kubernetes/ingress-admission-identity" || identity.State != "completed" || identity.CompletedAt.IsZero() || identity.CompletedAt.Before(identity.StartedAt) || identity.CompletedAt.After(time.Now()) {
		return bad()
	}
	pending := entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0
	closed := entry.State == "completed" && !entry.CompletedAt.IsZero() && !entry.CompletedAt.Before(entry.StartedAt) && !entry.CompletedAt.After(time.Now()) && len(entry.Receipt) != 0
	if completed && !pending && !closed || !completed && !pending {
		return bad()
	}
	path := filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256))
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || decodeEnvironmentJSON(raw, &m) != nil || validateEnvironmentCreateIngressAdmissionMaterial(p, execution.Journal.PlanSHA256, m, time.Now().UTC()) != nil || m.CreatedAt.Before(entry.StartedAt) || m.CreatedAt.After(identity.StartedAt) {
		return bad()
	}
	definitions, err := renderEnvironmentCreateIngressAdmission(cfg, p, release, m)
	if err != nil {
		return m, nil, err
	}
	child, journal, err := managedTransportPostAcceptedStage(store, execution, identity.Key)
	if err != nil || child == nil || len(child.Resources) != 1 || child.Resources[0].Action != "create" || child.Resources[0].UID != "" || child.Resources[0].ID != deploymentplan.ID(definitions[0].Object) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[child.Resources[0].ID]) {
		return bad()
	}
	expected, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}, definitions[:1], nil)
	if err != nil || len(expected.Resources) != 1 || child.Resources[0].Phase != expected.Resources[0].Phase || deploymentplan.Digest(child.Resources[0].Desired) != deploymentplan.Digest(expected.Resources[0].Desired) {
		return bad()
	}
	if err := execution.Check(ctx); err != nil {
		return m, nil, err
	}
	return m, definitions, nil
}

// A completed controller checkpoint is read back from its original child and
// wrapper receipt. It never enters either of the ingress installation stages.
func loadEnvironmentCreateCompletedIngressAdmission(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, execution *environmentCreateExecution) (environmentCreateIngressAdmissionMaterial, []deploymentplan.Definition, error) {
	m, definitions, err := loadEnvironmentCreateIngressAdmissionSource(ctx, cfg, p, release, store, execution, true)
	if err != nil {
		return m, nil, err
	}
	bad := func() (environmentCreateIngressAdmissionMaterial, []deploymentplan.Definition, error) {
		return m, nil, ingressAdmissionBlocker(p.Stack, "completed ingress requires its exact accepted controller and native transport receipt")
	}
	controllerEntry := execution.Journal.Entries[433]
	if controllerEntry.Key != "kubernetes/ingress-controller" || controllerEntry.State != "completed" || controllerEntry.StartedAt.Before(execution.Journal.Entries[432].CompletedAt) || controllerEntry.CompletedAt.IsZero() || controllerEntry.CompletedAt.Before(controllerEntry.StartedAt) || controllerEntry.CompletedAt.After(time.Now()) || execution.Journal.Entries[431].State == "completed" && execution.Journal.Entries[431].CompletedAt.Before(controllerEntry.CompletedAt) {
		return bad()
	}
	identity, identityJournal, err := managedTransportPostAcceptedStage(store, execution, "kubernetes/ingress-admission-identity")
	if err != nil {
		return m, nil, err
	}
	secretID := deploymentplan.ID(definitions[0].Object)
	secret := deploymentplan.Clone(identity.Resources[0].Desired)
	deploymentplan.Map(secret["metadata"])["uid"] = identityJournal.Owners[secretID]
	controllerDefinitions := slices.Clone(definitions)
	controllerDefinitions[0].Retain = true
	expected, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}, controllerDefinitions, []deploymentplan.Object{secret})
	controller, journal, childErr := managedTransportPostAcceptedStage(store, execution, controllerEntry.Key)
	if err != nil || childErr != nil || controller == nil || len(controller.Resources) != len(expected.Resources) {
		return bad()
	}
	uids := map[string]bool{}
	for i, resource := range controller.Resources {
		wanted := expected.Resources[i]
		uid := journal.Owners[resource.ID]
		if resource.ID != wanted.ID || resource.Phase != wanted.Phase || resource.Action != wanted.Action || resource.UID != wanted.UID || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(wanted.Desired) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(wanted.Dependencies) || !environmentBootstrapOwnerUUID.MatchString(uid) || uids[uid] {
			return bad()
		}
		uids[uid] = true
		if resource.ID == secretID {
			if uid != identityJournal.Owners[secretID] || verifyEnvironmentCreateIngressAdmissionSecret(p, m, resource.Baseline, uid) != nil {
				return bad()
			}
		} else if resource.Baseline != nil {
			return bad()
		}
	}
	if execution.Journal.Entries[431].State == "pending" {
		return m, definitions, execution.Check(ctx)
	}
	var receipt struct {
		Profile     string `json:"profile"`
		Image       string `json:"controller_image"`
		Secret      string `json:"secret_uid"`
		Controller  string `json:"controller_uid"`
		Webhook     string `json:"webhook_uid"`
		CA          string `json:"ca_sha256"`
		Certificate string `json:"certificate_sha256"`
		Key         string `json:"private_key_sha256"`
		Material    string `json:"material_sha256"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(execution.Journal.PlanSHA256)), true)
	if err != nil || decodeEnvironmentJSON(execution.Journal.Entries[431].Receipt, &receipt) != nil || receipt.Profile != p.IngressAdmissionProfile || receipt.Image != p.Images["LKE_INGRESS_IMAGE"] || receipt.Secret != identityJournal.Owners[secretID] || receipt.Controller != journal.Owners["Deployment/"+p.Stack+"-ingress/ingress-nginx-controller"] || receipt.Webhook != journal.Owners["ValidatingWebhookConfiguration//"+p.Stack+"-ingress-admission"] || receipt.CA != environmentPreflightBytesSHA256([]byte(m.CAPEM)) || receipt.Certificate != environmentPreflightBytesSHA256([]byte(m.CertificatePEM)) || receipt.Key != environmentPreflightBytesSHA256([]byte(m.PrivateKeyPEM)) || receipt.Material != environmentPreflightBytesSHA256(raw) {
		return bad()
	}
	return m, definitions, execution.Check(ctx)
}

func verifyEnvironmentCreateCompletedIngressAdmission(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, execution *environmentCreateExecution, backend environmentKubernetesBackend) error {
	m, definitions, err := loadEnvironmentCreateCompletedIngressAdmission(ctx, cfg, p, release, store, execution)
	if err != nil {
		return err
	}
	if backend.Store != store {
		return ingressAdmissionBlocker(p.Stack, "completed ingress must use the exact selected native backend")
	}
	controller, journal, err := managedTransportPostAcceptedStage(store, execution, "kubernetes/ingress-controller")
	if err != nil {
		return err
	}
	live, err := backend.Inventory(ctx, p.Stack)
	if err != nil {
		return err
	}
	checked := *controller
	checked.Resources = slices.Clone(controller.Resources)
	ids := map[string]bool{}
	for i := range checked.Resources {
		resource := &checked.Resources[i]
		resource.UID = journal.Owners[resource.ID]
		ids[resource.ID] = true
	}
	var relevant []deploymentplan.Object
	secretID := deploymentplan.ID(definitions[0].Object)
	for _, object := range live {
		id := deploymentplan.ID(object)
		if ids[id] || deploymentplan.Map(object["metadata"])["namespace"] == p.Stack+"-ingress" {
			relevant = append(relevant, object)
		}
		if id == secretID {
			if err := verifyEnvironmentCreateIngressAdmissionSecret(p, m, object, journal.Owners[secretID]); err != nil {
				return err
			}
		}
	}
	if err := verifyEnvironmentTargetWorkloads(ctx, &checked, relevant, func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	}); err != nil {
		return err
	}
	return execution.Check(ctx)
}

func environmentCreateIngressAdmissionReceipt(p environmentCreateBootstrapPlan, m environmentCreateIngressAdmissionMaterial, raw []byte, secretUID, controllerUID, webhookUID string) deploymentplan.Object {
	return deploymentplan.Object{"profile": p.IngressAdmissionProfile, "controller_image": p.Images["LKE_INGRESS_IMAGE"], "secret_uid": secretUID, "controller_uid": controllerUID, "webhook_uid": webhookUID, "ca_sha256": environmentPreflightBytesSHA256([]byte(m.CAPEM)), "certificate_sha256": environmentPreflightBytesSHA256([]byte(m.CertificatePEM)), "private_key_sha256": environmentPreflightBytesSHA256([]byte(m.PrivateKeyPEM)), "material_sha256": environmentPreflightBytesSHA256(raw)}
}

func finishEnvironmentCreateAcceptedIngressAdmission(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if err := verifyEnvironmentCreateCompletedIngressAdmission(ctx, cfg, p, release, store, runtime.Execution, runtime.Backend); err != nil {
		return err
	}
	if runtime.Execution.Journal.Entries[431].State == "completed" {
		return nil
	}
	// The accepted child is read-only. Only its original pending wrapper may
	// publish the fresh native readback, under the same admitted clock.
	if err := verifyManagedLateNormalTailRuntime(ctx, cfg, store, runtime); err != nil {
		return err
	}
	m, definitions, err := loadEnvironmentCreateCompletedIngressAdmission(ctx, cfg, p, release, store, runtime.Execution)
	if err != nil {
		return err
	}
	_, journal, err := managedTransportPostAcceptedStage(store, runtime.Execution, "kubernetes/ingress-controller")
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(runtime.ExecutionSHA256)), true)
	if err != nil {
		return err
	}
	secretID := deploymentplan.ID(definitions[0].Object)
	receipt, _ := json.Marshal(environmentCreateIngressAdmissionReceipt(p, m, raw, journal.Owners[secretID], journal.Owners["Deployment/"+p.Stack+"-ingress/ingress-nginx-controller"], journal.Owners["ValidatingWebhookConfiguration//"+p.Stack+"-ingress-admission"]))
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if !time.Now().Before(runtime.ServiceSessionRecovery.normalTail.Authority.Deadline) {
		return ingressAdmissionBlocker(p.Stack, "accepted ingress readback must finish inside its original normal-tail window")
	}
	if err := runtime.Execution.RecordReceipt(environmentCreateIngressAdmissionEntry, receipt); err != nil {
		return err
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	completedAt := time.Now().UTC()
	if !completedAt.Before(runtime.ServiceSessionRecovery.normalTail.Authority.Deadline) {
		return ingressAdmissionBlocker(p.Stack, "accepted ingress readback must finish inside its original normal-tail window")
	}
	return runtime.Execution.CompleteEntry(environmentCreateIngressAdmissionEntry, completedAt)
}

func executeEnvironmentCreateIngressAdmission(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	if runtime.Execution == nil || apply == nil || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || runtime.Execution.Store.Root != store.Root || store.Environment != cfg.Environment || runtime.Execution.Journal.Environment != cfg.Environment || runtime.Execution.Journal.Stack != p.Stack || runtime.Backend.Store.Root != store.Root || len(runtime.Backend.KubeconfigContent) == 0 {
		return ingressAdmissionBlocker(p.Stack, "native admission initialization requires the original parent and selected Kubernetes backend")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if err := validateEnvironmentCreateIngressAdmissionRecipe(cfg, p, release); err != nil {
		return err
	}
	resume := runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.normalTail != nil
	if resume {
		if err := verifyManagedLateNormalTailRuntime(ctx, cfg, store, runtime); err != nil {
			return err
		}
		if managedLateNormalTailControllerSelected(runtime.Execution.Journal) {
			return finishEnvironmentCreateAcceptedIngressAdmission(ctx, cfg, p, release, store, runtime)
		}
	}
	namespace := p.Stack + "-ingress"
	for _, id := range []string{"Secret/" + namespace + "/" + environmentCreateIngressAdmissionSecret, "ServiceAccount/" + namespace + "/ingress-nginx", "ConfigMap/" + namespace + "/ingress-nginx-controller", "Role/" + namespace + "/ingress-nginx", "RoleBinding/" + namespace + "/ingress-nginx", "ClusterRole//" + p.Stack + "-ingress-nginx", "ClusterRoleBinding//" + p.Stack + "-ingress-nginx", "IngressClass//" + p.Stack + "-nginx", "Service/" + namespace + "/ingress-nginx-controller", "Service/" + namespace + "/ingress-nginx-controller-admission", "Deployment/" + namespace + "/ingress-nginx-controller", "ValidatingWebhookConfiguration//" + p.Stack + "-ingress-admission", "NetworkPolicy/" + namespace + "/ingress-nginx-admission"} {
		if resume && id == "Secret/"+namespace+"/"+environmentCreateIngressAdmissionSecret {
			continue
		}
		current, err := runtime.Backend.Read(ctx, id)
		if err != nil || current != nil {
			return ingressAdmissionBlocker(p.Stack, "fresh ingress cannot adopt an existing or unreadable admission/controller owner")
		}
	}
	var m environmentCreateIngressAdmissionMaterial
	var definitions []deploymentplan.Definition
	var err error
	if resume {
		m, definitions, err = loadEnvironmentCreatePendingIngressAdmission(ctx, cfg, p, release, store, runtime.Execution)
	} else {
		m, err = prepareEnvironmentCreateIngressAdmissionMaterial(ctx, cfg, p, release, store, runtime.Execution)
		if err == nil {
			definitions, err = renderEnvironmentCreateIngressAdmission(cfg, p, release, m)
		}
		if err == nil {
			err = apply(ctx, "ingress-admission-identity", definitions[:1])
		}
	}
	if err != nil {
		return err
	}
	secret, err := runtime.Backend.Read(ctx, deploymentplan.ID(definitions[0].Object))
	if err != nil {
		return err
	}
	secretUID, err := environmentCreateIngressAdmissionOwner(runtime.Execution, "ingress-admission-identity", deploymentplan.ID(definitions[0].Object))
	if err != nil {
		return err
	}
	if err := verifyEnvironmentCreateIngressAdmissionSecret(p, m, secret, secretUID); err != nil {
		return err
	}
	controllerDefinitions := slices.Clone(definitions)
	controllerDefinitions[0].Retain = true
	if err := apply(ctx, "ingress-controller", controllerDefinitions); err != nil {
		return err
	}
	controllerID, webhookID := "Deployment/"+namespace+"/ingress-nginx-controller", "ValidatingWebhookConfiguration//"+p.Stack+"-ingress-admission"
	var desiredController, desiredWebhook deploymentplan.Object
	for _, definition := range definitions {
		if deploymentplan.ID(definition.Object) == controllerID {
			desiredController = definition.Object
		}
		if deploymentplan.ID(definition.Object) == webhookID {
			desiredWebhook = definition.Object
		}
	}
	controller, err := runtime.Backend.Read(ctx, controllerID)
	controllerUID, ownerErr := environmentCreateIngressAdmissionOwner(runtime.Execution, "ingress-controller", controllerID)
	if err != nil || ownerErr != nil || controller == nil || desiredController == nil || deploymentplan.String(deploymentplan.Map(controller["metadata"])["uid"]) != controllerUID || deploymentplan.Map(controller["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(desiredController, controller) {
		return ingressAdmissionBlocker(p.Stack, "actual ingress controller differs from its complete admitted native definition")
	}
	webhook, err := runtime.Backend.Read(ctx, webhookID)
	webhookUID, ownerErr := environmentCreateIngressAdmissionOwner(runtime.Execution, "ingress-controller", webhookID)
	if err != nil || ownerErr != nil || webhook == nil || desiredWebhook == nil || deploymentplan.String(deploymentplan.Map(webhook["metadata"])["uid"]) != webhookUID || deploymentplan.Map(webhook["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(desiredWebhook, webhook) {
		return ingressAdmissionBlocker(p.Stack, "actual ingress admission webhook has no exact authoritative native owner")
	}
	config := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(webhook["webhooks"])[0])["clientConfig"])
	if config["caBundle"] != base64.StdEncoding.EncodeToString([]byte(m.CAPEM)) {
		return ingressAdmissionBlocker(p.Stack, "actual admission webhook CA differs from the original generated authority")
	}
	finalSecret, err := runtime.Backend.Read(ctx, deploymentplan.ID(definitions[0].Object))
	if err != nil {
		return err
	}
	if err := verifyEnvironmentCreateIngressAdmissionSecret(p, m, finalSecret, deploymentplan.String(deploymentplan.Map(secret["metadata"])["uid"])); err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(runtime.ExecutionSHA256)), true)
	if err != nil {
		return err
	}
	var readback environmentCreateIngressAdmissionMaterial
	if decodeEnvironmentJSON(raw, &readback) != nil || deploymentplan.Digest(readback) != deploymentplan.Digest(m) {
		return ingressAdmissionBlocker(p.Stack, "original admission material changed during its native installation")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	receipt, _ := json.Marshal(environmentCreateIngressAdmissionReceipt(p, m, raw, deploymentplan.String(deploymentplan.Map(secret["metadata"])["uid"]), deploymentplan.String(deploymentplan.Map(controller["metadata"])["uid"]), deploymentplan.String(deploymentplan.Map(webhook["metadata"])["uid"])))
	if err := runtime.Execution.RecordReceipt(environmentCreateIngressAdmissionEntry, receipt); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(environmentCreateIngressAdmissionEntry, time.Now().UTC())
}
