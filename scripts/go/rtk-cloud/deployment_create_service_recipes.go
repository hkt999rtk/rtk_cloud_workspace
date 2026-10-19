package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCreateServiceState = "/var/lib/pki-service"
const environmentCreateServiceTrust = "/run/pki-service"

func environmentCreateServiceOwnerIdentityPath(role, filename string) string {
	directory := "private"
	if role == "pki-controller" {
		directory = "identity"
	}
	return environmentCreateServiceState + "/" + directory + "/" + filename
}

// Each identity has one native private owner. These claims are independent of
// the session credential claim and are never used to copy keys between owners.
func environmentCreateServiceClaimNames(p environmentCreateBootstrapPlan) []string {
	return []string{"pki-controller-state", "certissuer-service-state", "account-manager-service-state"}
}

func environmentCreateServiceOwnerClaim(role string) string {
	switch role {
	case "pki-controller":
		return "pki-controller-state"
	case "certissuer":
		return "certissuer-service-state"
	case "account-manager":
		return "account-manager-service-state"
	}
	return ""
}

func validateEnvironmentCreateServiceRecipe(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, root environmentCreateRegisteredIssuer) error {
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Stack != "video-cloud-"+cfg.Environment || (cfg.Environment != "dev" && cfg.Environment != "staging") || !immutableEnvironmentImage.MatchString(p.Image) || !immutableEnvironmentImage.MatchString(p.AccountManagerImage) {
		return environmentCreateBootstrapBlocker("Service recipe requires the exact qualified non-production target and immutable native images")
	}
	if root.Environment != cfg.Environment || root.Domain != "service" || root.Kind != "root" || (root.Status != "ready" && root.Status != "active") || !environmentBootstrapOwnerUUID.MatchString(root.ID) || !environmentPreflightDigest.MatchString(root.RootFingerprint) || environmentPreflightBytesSHA256([]byte(root.ChainPEM)) != root.BundleVersion {
		return environmentCreateBootstrapBlocker("Service recipe lacks original registered Root metadata")
	}
	block, rest := pem.Decode([]byte(root.Certificate))
	certificate, err := func() (*x509.Certificate, error) {
		if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return nil, fmt.Errorf("invalid certificate")
		}
		return x509.ParseCertificate(block.Bytes)
	}()
	if err != nil || !certificate.IsCA || certificate.CheckSignatureFrom(certificate) != nil || environmentPreflightBytesSHA256(certificate.Raw) != root.RootFingerprint {
		return environmentCreateBootstrapBlocker("Service Root certificate differs from its registered fingerprint")
	}
	if root.Document == nil || deploymentplan.String(root.Document["issuer_id"]) != root.ID || deploymentplan.String(root.Document["certificate_pem"]) != root.Certificate || deploymentplan.String(root.Document["environment"]) != root.Environment || deploymentplan.String(root.Document["trust_domain"]) != root.Domain || deploymentplan.String(root.Document["status"]) != root.Status {
		return environmentCreateBootstrapBlocker("original complete registered Service Root document is required")
	}
	if runtime.TransportCAPEM == "" || !environmentPreflightDigest.MatchString(runtime.TransportRootSHA256) || runtime.TransportRootSHA256 == root.RootFingerprint || runtime.TransportTrustConfigMap == "" {
		return environmentCreateBootstrapBlocker("separate original management transport authority is required before Service owner conversion")
	}
	transport, rest := pem.Decode([]byte(runtime.TransportCAPEM))
	if transport == nil || transport.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return environmentCreateBootstrapBlocker("management transport authority is malformed")
	}
	tc, err := x509.ParseCertificate(transport.Bytes)
	if err != nil || !tc.IsCA || tc.CheckSignatureFrom(tc) != nil || environmentPreflightBytesSHA256(tc.Raw) != runtime.TransportRootSHA256 {
		return environmentCreateBootstrapBlocker("management transport certificate differs from its original fingerprint")
	}
	for _, role := range []string{"pki-controller", "certissuer", "account-manager"} {
		if runtime.TransportSecrets[role] != "pki-create-"+role+"-transport" {
			return environmentCreateBootstrapBlocker("initial Service listener does not have its exact declared transport owner")
		}
	}
	return nil
}

func validateEnvironmentCreateServiceCeremony(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) error {
	if p.PublicCertIssuerHost != "" {
		if p.PublicCertIssuerHost != lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(cfg)).PublicHost {
			return environmentCreateBootstrapBlocker("CertIssuer public host differs from frozen selected intent")
		}
		found := false
		for _, policy := range p.Domains {
			if policy.Domain == "service" && slices.Contains(policy.ServerDNSNames, p.PublicCertIssuerHost) {
				found = true
			}
		}
		if !found {
			return environmentCreateBootstrapBlocker("CertIssuer public host is outside approved Service issuer DNS policy")
		}
	}

	if err := validateEnvironmentCreateServiceRecipe(cfg, p, runtime, s.Root); err != nil {
		return err
	}
	if s.Intermediate.Document == nil || deploymentplan.String(s.Intermediate.Document["issuer_id"]) != s.Intermediate.ID || deploymentplan.String(s.Intermediate.Document["parent_issuer_id"]) != s.Root.ID || deploymentplan.String(s.Intermediate.Document["certificate_pem"]) != s.Intermediate.Certificate || deploymentplan.String(s.Intermediate.Document["chain_pem"]) != s.Intermediate.ChainPEM || deploymentplan.String(s.Intermediate.Document["status"]) != s.Intermediate.Status || environmentPreflightBytesSHA256([]byte(s.Intermediate.ChainPEM)) != s.Intermediate.BundleVersion {
		return environmentCreateBootstrapBlocker("original complete registered Service intermediate document is required")
	}
	if s.Root.Status != "active" || s.Intermediate.Environment != cfg.Environment || s.Intermediate.Domain != "service" || s.Intermediate.Kind != "intermediate" || s.Intermediate.Status != "active" || s.Intermediate.RootFingerprint != s.Root.RootFingerprint || !environmentBootstrapOwnerUUID.MatchString(s.Intermediate.ID) || !environmentBootstrapOwnerUUID.MatchString(s.BootstrapSessionID) || s.BootstrapCredentialSecret != "service-bootstrap-"+s.BootstrapSessionID || s.BootstrapCredentialNamespace != p.Stack+"-video-cloud" || len(s.BootstrapCredentialNamespaces) == 0 || s.RootCRLSecret == "" || s.RootCRLPEM == "" {
		return environmentCreateBootstrapBlocker("Service enrollment requires actual active issuers, the original sealed session credential and native Root CRL")
	}
	return nil
}

func environmentCreateServiceManifest(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) (string, error) {
	refs := []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}
	seen := map[string]bool{root.ID: true}
	for _, i := range additional {
		if i.Environment != root.Environment || i.Domain != "service" || i.Kind != "intermediate" || (i.Status != "ready" && i.Status != "active") || i.RootFingerprint != root.RootFingerprint || seen[i.ID] || !environmentBootstrapOwnerUUID.MatchString(i.ID) || environmentPreflightBytesSHA256([]byte(i.ChainPEM)) != i.BundleVersion {
			return "", environmentCreateBootstrapBlocker("Service bundle recipe differs from actual registered lineage")
		}
		seen[i.ID] = true
		refs = append(refs, map[string]string{"issuer_id": i.ID, "trust_bundle_version": i.BundleVersion})
	}
	raw, err := json.Marshal(refs)
	return string(raw), err
}

func environmentCreateServiceTrustDefinition(namespace string, root environmentCreateRegisteredIssuer, transportPEM, manifest string) deploymentplan.Definition {
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": namespace}, "data": deploymentplan.Object{"root.pem": root.Certificate, "client-roots.pem": root.Certificate + "\n" + transportPEM, "bundles.json": manifest}}}
}

func environmentCreateServiceClaimDefinition(cfg deploymentConfig, namespace, claim string) deploymentplan.Definition {
	return deploymentplan.Definition{Phase: "storage", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain"), "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "1Gi"}}}}}
}

func environmentCreateServiceMount(name, path string, readOnly bool) any {
	return deploymentplan.Object{"name": name, "mountPath": path, "readOnly": readOnly}
}
func environmentCreateServiceVolume(name, kind, reference string) any {
	key := "name"
	if kind == "secret" {
		key = "secretName"
	}
	if kind == "persistentVolumeClaim" {
		key = "claimName"
	}
	value := deploymentplan.Object{key: reference}
	if kind == "secret" {
		value["defaultMode"] = float64(0440)
	}
	return deploymentplan.Object{"name": name, kind: value}
}

func environmentCreateServicePrivateInit(image string, sockets bool) any {
	command := "install -d -m 0700 -o 10001 -g 10001 /var/lib/pki-service/private /var/lib/pki-service/private/crl"
	mounts := []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false)}
	if sockets {
		command += " /run/account-pki"
		mounts = append(mounts, environmentCreateServiceMount("pki-socket", "/run/account-pki", false))
	}
	return deploymentplan.Object{"name": "pki-private-permissions", "image": image, "command": []any{"/bin/sh", "-ec", command}, "securityContext": deploymentplan.Object{"runAsUser": float64(0), "runAsNonRoot": false, "allowPrivilegeEscalation": false, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}, "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}, "volumeMounts": mounts}
}

// These definitions are frozen maintained recipes from foundation construction,
// never an observed Deployment copied back as desired state.
func environmentCreateServiceInitialListeners(runtime environmentCreateBootstrapRuntime, p environmentCreateBootstrapPlan) ([]deploymentplan.Definition, error) {
	var out []deploymentplan.Definition
	found := map[string]bool{}
	for _, d := range runtime.InitialListeners {
		o := deploymentplan.Clone(d.Object)
		if o["kind"] != "Deployment" {
			out = append(out, deploymentplan.Definition{Phase: d.Phase, Object: o})
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
		if name != "pki-controller" && name != "certissuer" {
			return nil, environmentCreateBootstrapBlocker("initial listener recipe contains an undeclared Service owner")
		}
		if deploymentplan.ID(o) != "Deployment/"+p.Stack+"-video-cloud/"+name || found[name] {
			return nil, environmentCreateBootstrapBlocker("initial listener recipe owner differs")
		}
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(o)["containers"])[0])
		if c["image"] != p.Image {
			return nil, environmentCreateBootstrapBlocker("initial listener image differs from qualification")
		}
		found[name] = true
		out = append(out, deploymentplan.Definition{Phase: d.Phase, Object: o})
	}
	if !found["pki-controller"] || !found["certissuer"] {
		return nil, environmentCreateBootstrapBlocker("both initial native listeners are required")
	}
	return out, nil
}

func environmentCreateServiceAccountDefinitions(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime) ([]deploymentplan.Definition, error) {
	release := environmentRelease{Environment: p.Environment, Stack: p.Stack, Images: map[string]environmentReleaseImage{"LKE_ACCOUNT_MANAGER_IMAGE": {Reference: p.AccountManagerImage}, "LKE_VIDEO_CLOUD_IMAGE": {Reference: p.Image}}}
	if runtime.Execution != nil {
		store, operator, err := readEnvironmentCreateOperator(cfg)
		if err != nil {
			return nil, err
		}
		if store.Root != runtime.Execution.Store.Root || store.Root != runtime.Backend.Store.Root {
			return nil, createProviderRecoveryBlock("original canonical runtime required for Service Account Manager recipe")
		}
		return renderEnvironmentCreateAccountFoundationWithRuntime(cfg, release, operator, store)
	}
	return renderEnvironmentCreateAccountFoundation(cfg, release, map[string]string{})
}

func renderEnvironmentCreateFirstTrustDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateServiceRecipe(cfg, p, runtime, root); err != nil {
		return nil, err
	}
	manifest, err := environmentCreateServiceManifest(root, additional...)
	if err != nil {
		return nil, err
	}
	out, err := environmentCreateServiceInitialListeners(runtime, p)
	if err != nil {
		return nil, err
	}
	for _, d := range out {
		if d.Object["kind"] != "Deployment" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		environmentCreateServiceBundleRollout(d.Object, manifest)
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_CLIENT_CA": environmentCreateServiceTrust + "/client-roots.pem", "PKI_SERVICE_CLIENT_ROOT_SHA256": root.RootFingerprint, "PKI_SERVICE_CLIENT_BUNDLE_MANIFEST": environmentCreateServiceTrust + "/bundles.json", "PKI_SERVICE_CLIENT_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", "PKI_SERVICE_CLIENT_MANAGEMENT_CA": "/run/pki-create/ca.crt", "PKI_SERVICE_CLIENT_MANAGEMENT_CERT": "/run/pki-create/tls.crt", "PKI_SERVICE_CLIENT_MANAGEMENT_KEY": "/run/pki-create/tls.key"}))
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
		for _, issuer := range additional {
			if issuer.Status == "active" && c["name"] == "pki-controller" {
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_SERVER_CRL_DOMAIN": "service"}))
			}
		}
	}
	account, err := environmentCreateServiceAccountDefinitions(cfg, p, runtime)
	if err != nil {
		return nil, err
	}
	for _, d := range account {
		if d.Object["kind"] == "Deployment" {
			if err := environmentCreateServiceAccountSidecar(d.Object, p, runtime, root, manifest, true, environmentCreateServiceCeremony{}); err != nil {
				return nil, err
			}
		}
		if d.Object["kind"] == "Service" {
			environmentCreateAccountMTLSPort(d.Object)
		}
	}
	out = append(out, account...)
	if runtime.Execution != nil {
		password, err := runtime.Backend.Store.read(filepath.Join("deployment/create/material", runtime.Execution.Journal.PlanSHA256, "database/verifier.password"))
		if err != nil || password == "" {
			return nil, environmentCreateBootstrapBlocker("original verifier credential required for the Account Manager namespace")
		}
		original, err := runtime.Backend.Read(ctx, "Secret/"+p.Stack+"-video-cloud/pki-verifier-database")
		if err != nil {
			return nil, environmentCreateBootstrapBlocker("original verified database Secret differs from its credential binding")
		}
		binding, err := environmentCreateAccountVerifierDefinition(p.Stack, password, original)
		if err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	for _, namespace := range []string{p.Stack + "-video-cloud", p.Stack + "-account-manager"} {
		out = append(out, environmentCreateServiceTrustDefinition(namespace, root, runtime.TransportCAPEM, manifest))
	}
	out = append(out, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-account-manager", "account-manager-service-state"))
	return out, nil
}

func environmentCreateAccountVerifierDefinition(stack, password string, original deploymentplan.Object) (deploymentplan.Definition, error) {
	dsn := "postgres://" + url.UserPassword("rtk_pki_verifier_login", password).String() + "@postgresql." + stack + "-platform.svc:5432/video_cloud?sslmode=disable"
	if password == "" || original == nil || deploymentplan.ID(original) != "Secret/"+stack+"-video-cloud/pki-verifier-database" || deploymentplan.Map(original["data"])["url"] != base64.StdEncoding.EncodeToString([]byte(dsn)) {
		return deploymentplan.Definition{}, environmentCreateBootstrapBlocker("original verified database Secret differs from its credential binding")
	}
	return environmentCreateSecret(stack+"-account-manager", "pki-verifier-database", map[string]string{"url": dsn}), nil
}

func environmentCreateServiceAccountSidecar(object deploymentplan.Object, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, root environmentCreateRegisteredIssuer, manifest string, first bool, s environmentCreateServiceCeremony) error {
	pod := environmentPKIPod(object)
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if main["name"] != "app" {
		return environmentCreateBootstrapBlocker("Account Manager desired owner is unavailable")
	}
	// The public process sees only the local Unix socket; only the sidecar owns
	// Service client/server state and its one-time transport key.
	main["env"] = environmentCreateServiceWithoutEnv(main["env"], "PKI_CONTROLLER_CLIENT_CERT", "PKI_CONTROLLER_CLIENT_KEY", "PKI_CONTROLLER_CA")
	main["env"] = mergeEnvironmentNamedFields(main["env"], environmentCreateEnv(map[string]string{"PKI_CONTROLLER_SOCKET": "/run/account-pki/private/controller.sock", "APP_CERT_ISSUER_SOCKET": "/run/account-pki/private/controller.sock", "APP_CERT_ISSUER_BASE_URL": "https://certissuer." + p.Stack + "-video-cloud.svc:18445", "APP_CERT_ISSUER_CLIENT_CERT": "", "APP_CERT_ISSUER_CLIENT_KEY": "", "APP_CERT_ISSUER_CA_FILE": ""}))
	main["volumeMounts"] = environmentCreateServiceWithoutNamed(main["volumeMounts"], "pki-create-transport")
	main["volumeMounts"] = mergeEnvironmentNamedFields(main["volumeMounts"], []any{environmentCreateServiceMount("pki-socket", "/run/account-pki", false)})
	controller := "pki-controller." + p.Stack + "-video-cloud.svc"
	issuer := "certissuer." + p.Stack + "-video-cloud.svc"
	account := "account-manager." + p.Stack + "-account-manager.svc.cluster.local"
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_MANAGEMENT_SOCKET": "/run/account-pki/controller.sock", "PKI_MANAGEMENT_IDENTITY_STATE": environmentCreateServiceState + "/private/client.json", "PKI_MANAGEMENT_IDENTITY_ROOT_SHA256": root.RootFingerprint, "PKI_MANAGEMENT_ISSUER_URL": "https://" + issuer + ":18445", "PKI_MANAGEMENT_CONTROLLER_URL": "https://" + controller + ":18446", "PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR": ":18443", "PKI_MANAGEMENT_ACCOUNT_UPSTREAM": "http://127.0.0.1:8080", "PKI_MANAGEMENT_ACCOUNT_CLIENT_CA": environmentCreateServiceTrust + "/root.pem", "PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256": root.RootFingerprint, "PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE": environmentCreateServiceState + "/private/server.json", "PKI_MANAGEMENT_ACCOUNT_HOST_NAME": account, "PKI_MANAGEMENT_ACCOUNT_HOST_DNS_NAMES": account, "PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256": root.RootFingerprint, "PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_URL": "https://" + issuer + ":18445", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST": environmentCreateServiceTrust + "/bundles.json"}
	settings["PKI_MANAGEMENT_SOCKET"] = "/run/account-pki/private/controller.sock"
	if first {
		settings["PKI_MANAGEMENT_FIRST_TRUST_ONLY"] = "true"
		settings["PKI_MANAGEMENT_BOOTSTRAP_CERT"] = "/run/pki-create/tls.crt"
		settings["PKI_MANAGEMENT_BOOTSTRAP_KEY"] = "/run/pki-create/tls.key"
		settings["PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_ROOT_SHA256"] = runtime.TransportRootSHA256
		settings["PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_NAME"] = controller
		settings["PKI_MANAGEMENT_CONTROLLER_TLS_CA"] = "/run/pki-create/ca.crt"
		settings["PKI_MANAGEMENT_ACCOUNT_HOST_SEED_CERT"] = "/run/pki-create/tls.crt"
		settings["PKI_MANAGEMENT_ACCOUNT_HOST_SEED_KEY"] = "/run/pki-create/tls.key"
	} else {
		for _, prefix := range []string{"PKI_MANAGEMENT_ISSUER", "PKI_MANAGEMENT_CONTROLLER", "PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL"} {
			name := issuer
			if prefix == "PKI_MANAGEMENT_CONTROLLER" {
				name = controller
			}
			environmentCreateServiceHTTPSettings(settings, prefix, name, root.RootFingerprint, true, p)
		}
		settings["PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/listener-crls.json"
		settings["PKI_BOOTSTRAP_SESSION_ID"] = s.BootstrapSessionID
	}
	env := environmentCreateEnv(settings)
	env = append(env, environmentCreateServiceDatabaseEnv("pki-verifier-database"))
	mounts := []any{environmentCreateServiceMount("pki-socket", "/run/account-pki", false), environmentCreateServiceMount("service-state", environmentCreateServiceState, false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)}
	if first {
		mounts = append(mounts, environmentCreateServiceMount("pki-create-transport", "/run/pki-create", true))
	}
	sidecar := deploymentplan.Object{"name": "pkimanagement", "image": p.Image, "command": []any{"/app/pkimanagement"}, "env": env, "ports": []any{deploymentplan.Object{"name": "service-mtls", "containerPort": float64(18443)}}, "readinessProbe": deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": "service-mtls"}, "periodSeconds": float64(5)}, "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": mounts}
	pod["containers"] = []any{main, sidecar}
	pod["securityContext"] = environmentCreateServicePodSecurity()
	pod["automountServiceAccountToken"] = false
	pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{deploymentplan.Object{"name": "pki-socket", "emptyDir": deploymentplan.Object{}}, environmentCreateServiceVolume("service-state", "persistentVolumeClaim", "account-manager-service-state"), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
	if !first {
		pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "pki-create-transport")
	}
	if !immutableEnvironmentImage.MatchString(p.Images["LKE_ALPINE_IMAGE"]) {
		return environmentCreateBootstrapBlocker("Account Manager socket ownership requires the qualified immutable Alpine image")
	}
	// The sidecar creates its own absent private state directories as UID10001.
	// The only canonical Account initializer prepares the shared private socket.
	pod["initContainers"] = []any{deploymentplan.Object{"name": "management-socket-directory", "image": p.Images["LKE_ALPINE_IMAGE"], "command": []any{"/bin/sh", "-ec", accountManagerSocketPreparation}, "volumeMounts": []any{environmentCreateServiceMount("pki-socket", "/socket", false)}, "securityContext": environmentCreateServiceContainerSecurity()}}
	// The managed identity store holds a process-lifetime exclusive lock. Two
	// generations of this sidecar cannot share its retained PVC concurrently.
	environmentCreateServiceBundleRollout(object, manifest)
	setEnvironmentCreateExclusiveAccountOwner(object)
	return nil
}

func setEnvironmentCreateExclusiveAccountOwner(object deploymentplan.Object) {
	deploymentplan.Map(object["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
	template := deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])
	meta := deploymentplan.Map(template["metadata"])
	if meta == nil {
		meta = deploymentplan.Object{}
		template["metadata"] = meta
	}
	annotations := deploymentplan.Map(meta["annotations"])
	if annotations == nil {
		annotations = deploymentplan.Object{}
		meta["annotations"] = annotations
	}
	// Also retire a backed-off ReplicaSet from an interrupted rolling update.
	annotations["rtk.realtek.com/pki-identity-owner-mode"] = "exclusive"
}

func environmentCreateServiceWithoutEnv(value any, names ...string) []any {
	return environmentCreateServiceWithoutNamed(value, names...)
}

func environmentCreateServiceBundleRollout(object deploymentplan.Object, manifest string) {
	template := deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])
	meta := deploymentplan.Map(template["metadata"])
	if meta == nil {
		meta = deploymentplan.Object{}
		template["metadata"] = meta
	}
	annotations := deploymentplan.Map(meta["annotations"])
	if annotations == nil {
		annotations = deploymentplan.Object{}
		meta["annotations"] = annotations
	}
	annotations["rtk.realtek.com/pki-create-bundle-sha256"] = environmentPreflightBytesSHA256([]byte(manifest))
}

func environmentCreateAccountMTLSPort(object deploymentplan.Object) {
	spec := deploymentplan.Map(object["spec"])
	spec["ports"] = mergeEnvironmentNamedFields(spec["ports"], []any{deploymentplan.Object{"name": "service-mtls", "port": float64(18443), "targetPort": "service-mtls"}})
}
func environmentCreateServiceWithoutNamed(value any, names ...string) []any {
	out := []any{}
	for _, v := range certIssuerObjectList(value) {
		if !slices.Contains(names, deploymentplan.String(deploymentplan.Map(v)["name"])) {
			out = append(out, v)
		}
	}
	return out
}
func environmentCreateServiceDatabaseEnv(secret string) any {
	return deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": secret, "key": "url"}}}
}
func environmentCreateServicePodSecurity() deploymentplan.Object {
	return deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001), "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}
}
func environmentCreateServiceContainerSecurity() deploymentplan.Object {
	return deploymentplan.Object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}
}

func environmentCreateServiceHTTPSettings(settings map[string]string, prefix, name, pin string, consumer bool, p environmentCreateBootstrapPlan) {
	settings[prefix+"_SERVER_PKI_ROOT_SHA256"] = pin
	settings[prefix+"_SERVER_PKI_NAME"] = name
	settings[prefix+"_TLS_CA"] = environmentCreateServiceTrust + "/root.pem"
	if consumer {
		settings[prefix+"_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/outbound-crls.json"
		settings[prefix+"_PKI_CONTROLLER_URL"] = "https://pki-controller." + p.Stack + "-video-cloud.svc:18446"
		settings[prefix+"_MANAGEMENT_CA"] = environmentCreateServiceTrust + "/root.pem"
	}
}

func renderEnvironmentCreateServiceIssuerDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return nil, err
	}
	initial, err := environmentCreateServiceInitialListeners(runtime, p)
	if err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, d := range initial {
		if deploymentplan.ID(d.Object) != "Deployment/"+p.Stack+"-video-cloud/certissuer" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		c["command"] = []any{"/app/certissuer"}
		c["env"] = environmentCreateServiceIssuerEnv(cfg, p, s)
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
		out = append(out, d)
	}
	return out, nil
}

func environmentCreateServiceIssuerEnv(cfg deploymentConfig, p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony) []any {
	var policy environmentCreateIssuerPolicy
	for _, candidate := range p.Domains {
		if candidate.Domain == "service" {
			policy = candidate
		}
	}
	settings := map[string]string{"VIDEO_CLOUD_ENV": p.Environment, "PKI_ENVIRONMENT": p.Environment, "CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY": "true", "CERT_ISSUER_LISTEN_ADDR": ":18445", "CERT_ISSUER_SERVER_CERT": "/run/pki-create/tls.crt", "CERT_ISSUER_SERVER_KEY": "/run/pki-create/tls.key", "CERT_ISSUER_CLIENT_CA": environmentCreateServiceTrust + "/root.pem", "CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED": "true", "CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_ISSUER_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVER_PKI_DOMAIN": "service", "CERT_ISSUER_SERVER_PKI_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVER_PKI_ISSUER_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256": s.Root.RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER": "service:deployment-bootstrap", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS": strings.Join(environmentCreateServiceEnrollmentSubjects(policy), ","), "CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN": "^service:deployment-bootstrap$", "CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN": "^service:(deployment-bootstrap|account-manager|certissuer|pki-controller)$", "CERT_ISSUER_GATEWAY_DNS_NAMES": strings.Join(policy.ServerDNSNames, ","), "CERT_ISSUER_MIN_TTL_DAYS": "1", "CERT_ISSUER_MAX_TTL_DAYS": "30", "CERT_ISSUER_DEFAULT_TTL_DAYS": "30", "CERT_ISSUER_SIGNER_PROVIDER": "openbao", "CERT_ISSUER_OPENBAO_PKI_MOUNT": s.Intermediate.Reference, "CERT_ISSUER_OPENBAO_PKCS11_ENABLED": "false", "CERT_ISSUER_OPENBAO_SERVICE_CLIENT_ROLE": "service-client", "CERT_ISSUER_OPENBAO_GATEWAY_ROLE": "gateway", "OPENBAO_ADDR": "https://openbao." + p.Stack + "-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt", "OPENBAO_AUTH_METHOD": "kubernetes", "OPENBAO_KUBERNETES_ROLE": "certissuer-" + cfg.Environment, "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": "/run/openbao-auth/token"}
	callers := make([]string, 0, len(policy.ServiceClientIDs))
	for _, subject := range policy.ServiceClientIDs {
		callers = append(callers, regexp.QuoteMeta(subject))
	}
	settings["CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN"] = "^(" + strings.Join(callers, "|") + ")$"
	env := environmentCreateEnv(settings)
	env = append(env, deploymentplan.Object{"name": "CERT_ISSUER_DB_DSN", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-issuer-database", "key": "url"}}})
	return env
}

func environmentCreateServiceEnrollmentSubjects(policy environmentCreateIssuerPolicy) []string {
	var out []string
	for _, s := range policy.ServiceClientIDs {
		if s != "service:deployment-bootstrap" {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

func environmentCreateClientEnrollmentDefinition(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace, role, claim string) (deploymentplan.Definition, error) {
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return deploymentplan.Definition{}, err
	}
	if !slices.Contains(s.BootstrapCredentialNamespaces, namespace) || !environmentCreateConsumerID.MatchString(role) || claim == "" {
		return deploymentplan.Definition{}, environmentCreateBootstrapBlocker("client enrollment owner or exact namespace credential is undeclared")
	}
	issuer := "certissuer." + p.Stack + "-video-cloud.svc"
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_CLIENT_IDENTITY_STATE": environmentCreateServiceOwnerIdentityPath(role, "client.json"), "PKI_CLIENT_ROOT_SHA256": s.Root.RootFingerprint, "PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT": "/run/pki-bootstrap/tls.crt", "PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY": "/run/pki-bootstrap/tls.key", "PKI_CLIENT_ISSUER_URL": "https://" + issuer + ":18445", "PKI_CLIENT_ISSUER_SERVER_PKI_ROOT_SHA256": s.Root.RootFingerprint, "PKI_CLIENT_ISSUER_SERVER_PKI_NAME": issuer, "PKI_CLIENT_ISSUER_TLS_CA": environmentCreateServiceTrust + "/root.pem"}
	return environmentCreateServiceEnrollmentJob(p, namespace, role+"-client", claim, s, []any{"/app/serviceidentity-bootstrap", "service:" + role}, settings, false), nil
}

func environmentCreateServiceServerEnrollmentDefinition(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace, role, claim string, temporary bool) (deploymentplan.Definition, error) {
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return deploymentplan.Definition{}, err
	}
	if !slices.Contains(s.BootstrapCredentialNamespaces, namespace) || environmentCreateServiceOwnerClaim(role) != claim {
		return deploymentplan.Definition{}, environmentCreateBootstrapBlocker("server enrollment owner or exact namespace credential is undeclared")
	}
	name := role + "." + p.Stack + "-video-cloud.svc"
	if role == "account-manager" {
		name = role + "." + p.Stack + "-account-manager.svc.cluster.local"
	}
	issuer := "certissuer." + p.Stack + "-video-cloud.svc"
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_SERVER_DOMAIN": "service", "PKI_SERVER_NAME": name, "PKI_SERVER_DNS_NAMES": strings.Join(environmentCreateServiceServerDNSNames(p, role, name), ","), "PKI_SERVER_ROOT_SHA256": s.Root.RootFingerprint, "PKI_SERVER_IDENTITY_STATE": environmentCreateServiceOwnerIdentityPath(role, "server.json"), "PKI_SERVER_BOOTSTRAP_REQUEST_ID": s.BootstrapSessionID + ":" + role + ":server", "PKI_CLIENT_ISSUER_URL": "https://" + issuer + ":18445", "PKI_CLIENT_ISSUER_SERVER_PKI_NAME": issuer, "PKI_CLIENT_ISSUER_TLS_CA": environmentCreateServiceTrust + "/root.pem", "PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT": "/run/pki-bootstrap/tls.crt", "PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY": "/run/pki-bootstrap/tls.key"}
	if temporary {
		settings["PKI_CLIENT_ISSUER_TLS_CA"] = "/run/pki-create/ca.crt"
	}
	return environmentCreateServiceEnrollmentJob(p, namespace, role+"-server", claim, s, []any{"/app/serviceidentity-bootstrap", "server-bootstrap"}, settings, temporary), nil
}

func environmentCreateServiceEnrollmentJob(p environmentCreateBootstrapPlan, namespace, key, claim string, s environmentCreateServiceCeremony, command []any, settings map[string]string, temporary bool) deploymentplan.Definition {
	env := environmentCreateEnv(settings)
	env = append(env, environmentCreateServiceDatabaseEnv("pki-verifier-database"))
	volumes := []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", claim), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust"), environmentCreateServiceVolume("bootstrap", "secret", s.BootstrapCredentialSecret)}
	mounts := []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true), environmentCreateServiceMount("bootstrap", "/run/pki-bootstrap", true)}
	if temporary {
		volumes = append(volumes, environmentCreateServiceVolume("transport", "configMap", "pki-create-transport-trust"))
		mounts = append(mounts, environmentCreateServiceMount("transport", "/run/pki-create", true))
	}
	name := "pki-enroll-" + key + "-" + strings.ReplaceAll(s.BootstrapSessionID, "-", "")[:12]
	permissions := deploymentplan.Map(environmentCreateServicePrivateInit(p.Image, false))
	if strings.HasPrefix(key, "pki-controller-") {
		permissions["command"] = []any{"/bin/sh", "-ec", "install -d -m 0700 -o 10001 -g 10001 /var/lib/pki-service/identity /var/lib/pki-service/private /var/lib/pki-service/private/crl"}
	}
	return deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(180), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}}, "spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "imagePullSecrets": []any{deploymentplan.Object{"name": "ghcr-pull"}}, "securityContext": environmentCreateServicePodSecurity(), "initContainers": []any{permissions}, "containers": []any{deploymentplan.Object{"name": "enroll", "image": p.Image, "command": command, "env": env, "volumeMounts": mounts, "securityContext": environmentCreateServiceContainerSecurity()}}, "volumes": volumes}}}}}
}

func renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job, err := environmentCreateServiceServerEnrollmentDefinition(cfg, p, runtime, s, p.Stack+"-video-cloud", "certissuer", "certissuer-service-state", true)
	if err != nil {
		return nil, err
	}
	return []deploymentplan.Definition{environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", "certissuer-service-state"), job}, nil
}

func renderEnvironmentCreateManagedServiceIssuerDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	out, err := renderEnvironmentCreateServiceIssuerDefinitions(ctx, cfg, p, runtime, s)
	if err != nil {
		return nil, err
	}
	for _, d := range out {
		pod := environmentPKIPod(d.Object)
		deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] = "OnRootMismatch"
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		settings := map[string]string{}
		environmentCreateServiceHostSettings(settings, "CERT_ISSUER", "certissuer."+p.Stack+"-video-cloud.svc", p, s.Root.RootFingerprint)
		settings["CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT"] = "/run/pki-bootstrap/tls.crt"
		settings["CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY"] = "/run/pki-bootstrap/tls.key"
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false), environmentCreateServiceMount("bootstrap", "/run/pki-bootstrap", true)})
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", "certissuer-service-state"), environmentCreateServiceVolume("bootstrap", "secret", s.BootstrapCredentialSecret)})
		pod["initContainers"] = mergeEnvironmentNamedFields(pod["initContainers"], []any{environmentCreateServicePrivateInit(p.Image, false)})
	}
	return out, nil
}

// The omitted historical selector keeps every accepted one-name recipe literal.
func environmentCreateServiceServerDNSNames(p environmentCreateBootstrapPlan, role, name string) []string {
	names := []string{name}
	if role == "certissuer" && p.PublicCertIssuerHost != "" {
		names = append(names, p.PublicCertIssuerHost)
		slices.Sort(names)
	}
	return names
}

func environmentCreateServiceHostSettings(settings map[string]string, prefix, name string, p environmentCreateBootstrapPlan, pin string) {
	settings[prefix+"_HOST_IDENTITY_STATE"] = environmentCreateServiceState + "/private/server.json"
	settings[prefix+"_HOST_NAME"] = name
	names := []string{name}
	if prefix == "CERT_ISSUER" {
		names = environmentCreateServiceServerDNSNames(p, "certissuer", name)
	}
	settings[prefix+"_HOST_DNS_NAMES"] = strings.Join(names, ",")
	settings[prefix+"_HOST_ROOT_SHA256"] = pin
	settings[prefix+"_HOST_RENEWAL_URL"] = "https://certissuer." + p.Stack + "-video-cloud.svc:18445"
	environmentCreateServiceHTTPSettings(settings, prefix+"_HOST_RENEWAL", "certissuer."+p.Stack+"-video-cloud.svc", pin, false, p)
}

func renderEnvironmentCreateServiceEnrollmentDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, role := range []string{"pki-controller", "certissuer", "account-manager"} {
		namespace := p.Stack + "-video-cloud"
		if role == "account-manager" {
			namespace = p.Stack + "-account-manager"
		}
		claim := environmentCreateServiceOwnerClaim(role)
		out = append(out, environmentCreateServiceClaimDefinition(cfg, namespace, claim))
		job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, s, namespace, role, claim)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
		if role != "certissuer" {
			job, err = environmentCreateServiceServerEnrollmentDefinition(cfg, p, runtime, s, namespace, role, claim, false)
			if err != nil {
				return nil, err
			}
			out = append(out, job)
		}
	}
	return out, nil
}

func environmentCreateServiceCRLManifest(i environmentCreateRegisteredIssuer, path string, additional ...environmentCreateRegisteredIssuer) string {
	entries := []any{deploymentplan.Object{"issuer": deploymentplan.Clone(i.Document), "state_path": path}}
	for _, issuer := range additional {
		entries = append(entries, deploymentplan.Object{"issuer": deploymentplan.Clone(issuer.Document), "state_path": strings.TrimSuffix(path, ".json") + "-" + issuer.ID + ".json"})
	}
	raw, _ := json.Marshal(entries)
	return string(raw)
}

func renderEnvironmentCreateManagedServiceDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return nil, err
	}
	manifest, err := environmentCreateServiceManifest(s.Root, s.Intermediate)
	if err != nil {
		return nil, err
	}
	out, err := environmentCreateServiceInitialListeners(runtime, p)
	if err != nil {
		return nil, err
	}
	for _, d := range out {
		if d.Object["kind"] != "Deployment" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		role := deploymentplan.String(c["name"])
		deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] = "OnRootMismatch"
		prefix := "PKI"
		client := "PKI_SERVICE_CLIENT"
		if role == "certissuer" {
			prefix = "CERT_ISSUER"
			client = "CERT_ISSUER_SERVICE_CLIENT"
			c["command"] = []any{"/app/certissuer"}
			c["env"] = environmentCreateServiceIssuerEnv(cfg, p, s)
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"CERT_ISSUER_REGISTRY_ONLY_ENABLED": "true"}))
		}
		c["env"] = environmentCreateServiceWithoutEnv(c["env"], "CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY", "PKI_SERVER_CERT", "PKI_SERVER_KEY", "CERT_ISSUER_SERVER_CERT", "CERT_ISSUER_SERVER_KEY", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA", "PKI_SERVICE_CLIENT_MANAGEMENT_CERT", "PKI_SERVICE_CLIENT_MANAGEMENT_KEY")
		settings := map[string]string{prefix + "_CLIENT_CA": environmentCreateServiceTrust + "/root.pem", client + "_IDENTITY_STATE": environmentCreateServiceOwnerIdentityPath(role, "client.json"), client + "_ROOT_SHA256": s.Root.RootFingerprint, client + "_BUNDLE_MANIFEST": environmentCreateServiceTrust + "/bundles.json", client + "_SERVER_CRL_MANIFEST": environmentCreateServiceTrust + "/listener-crls.json", client + "_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", client + "_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem", "PKI_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID}
		environmentCreateServiceHostSettings(settings, prefix, role+"."+p.Stack+"-video-cloud.svc", p, s.Root.RootFingerprint)
		settings[prefix+"_HOST_IDENTITY_STATE"] = environmentCreateServiceOwnerIdentityPath(role, "server.json")
		environmentCreateServiceHTTPSettings(settings, prefix+"_HOST_RENEWAL", "certissuer."+p.Stack+"-video-cloud.svc", s.Root.RootFingerprint, true, p)
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
		c["volumeMounts"] = environmentCreateServiceWithoutNamed(c["volumeMounts"], "transport")
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("service-state", environmentCreateServiceState, false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
		pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "transport")
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", environmentCreateServiceOwnerClaim(role)), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
		pod["initContainers"] = mergeEnvironmentNamedFields(pod["initContainers"], []any{environmentCreateServicePrivateInit(p.Image, false)})
		if role == "pki-controller" {
			// Native workers discover active issuers from their registered
			// references. Keep them running while later domains are created so
			// their first signed CRLs exist before consumers enable admission.
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_APP_CRL_WORKER_ENABLED": "true", "PKI_SERVER_CRL_DOMAIN": "service,mqtt,openbao_tls"}))
			pod["initContainers"] = []any{}
			release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: p.Images["LKE_ALPINE_IMAGE"]}}}
			if err := applyControllerIdentityPreparation(d.Object, release); err != nil {
				return nil, err
			}
			values := appendMap(cfg.Values, map[string]string{"CLOUD_ENV_NAME": cfg.Environment})
			cleanup, err := applyEnvironmentBootstrapCleanup(values, d.Object, s.CleanupBinding)
			if err != nil {
				return nil, err
			}
			out = append(out, cleanup...)
		}
	}
	account, err := environmentCreateServiceAccountDefinitions(cfg, p, runtime)
	if err != nil {
		return nil, err
	}
	for _, d := range account {
		if d.Object["kind"] == "Deployment" {
			if err := environmentCreateServiceAccountSidecar(d.Object, p, runtime, s.Root, manifest, false, s); err != nil {
				return nil, err
			}
		}
		if d.Object["kind"] == "Service" {
			environmentCreateAccountMTLSPort(d.Object)
		}
	}
	out = append(out, account...)
	for index, d := range out {
		id := deploymentplan.ID(d.Object)
		if id == "Deployment/"+p.Stack+"-video-cloud/certissuer" || id == "Deployment/"+p.Stack+"-account-manager/account-manager" {
			out[index].Dependencies = append(out[index].Dependencies, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
			if id == "Deployment/"+p.Stack+"-account-manager/account-manager" {
				out[index].Dependencies = append(out[index].Dependencies, "Deployment/"+p.Stack+"-video-cloud/certissuer")
			}
		}
	}
	for _, namespace := range []string{p.Stack + "-video-cloud", p.Stack + "-account-manager"} {
		trust := environmentCreateServiceTrustDefinition(namespace, s.Root, "", manifest)
		data := deploymentplan.Map(trust.Object["data"])
		data["listener-crls.json"] = environmentCreateServiceCRLManifest(s.Root, environmentCreateServiceState+"/private/crl/listener-root.json", s.Intermediate)
		data["outbound-crls.json"] = environmentCreateServiceCRLManifest(s.Root, environmentCreateServiceState+"/private/crl/outbound-root.json", s.Intermediate)
		out = append(out, trust)
	}
	return out, nil
}
