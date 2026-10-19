package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is a recipe for one genuine initial ceremony. It contains no future
// Kubernetes UIDs, generated keys, registered issuer IDs or success receipts.
type environmentCreateBootstrapPlan struct {
	AppRoutingMode           string                             `json:"app_routing_mode"`
	IngressAdmissionProfile  string                             `json:"ingress_admission_profile"`
	Environment              string                             `json:"environment"`
	Stack                    string                             `json:"stack"`
	Image                    string                             `json:"image"`
	AccountManagerImage      string                             `json:"account_manager_image"`
	Images                   map[string]string                  `json:"images"`
	Templates                map[string]string                  `json:"templates"`
	Domains                  []environmentCreateIssuerPolicy    `json:"domains"`
	Claims                   []string                           `json:"claims"`
	ProductIdentities        []environmentCreateProductIdentity `json:"product_identities,omitempty"`
	PublicCertIssuerHost     string                             `json:"public_certissuer_host,omitempty"`
	NativeConnectivitySHA256 string                             `json:"native_connectivity_sha256,omitempty"`
}

type environmentCreateIssuerPolicy struct {
	Domain           string   `json:"domain"`
	Consumers        []string `json:"consumers"`
	BundleConsumers  []string `json:"bundle_consumers"`
	ServerDNSNames   []string `json:"server_dns_names,omitempty"`
	ServiceClientIDs []string `json:"service_client_ids,omitempty"`
}

type environmentCreateBootstrapApply func(context.Context, string, []deploymentplan.Definition) error

// Only the orchestration owner can construct this after its native kubeconfig
// and selected Account Manager tunnel have been bound to the allocated cluster.
type environmentCreateBootstrapRuntime struct {
	APIOrigin                           string
	APIClient                           *http.Client
	Backend                             environmentKubernetesBackend
	ExecutionSHA256                     string
	InitialListeners                    []deploymentplan.Definition
	InitialAuthSeparated                bool
	ServiceRootReady                    *environmentCreateRegisteredIssuer
	ServiceRootOperation                *environmentCreateIssuerOperation
	ServiceRootTrustStage               string
	ServiceRootTrustAdditionalParentKey string
	TransportTrustConfigMap             string
	TransportSecrets                    map[string]string
	TransportRootSHA256                 string
	TransportCAPEM                      string
	OpenBao                             environmentCreateOpenBaoBootstrap
	Execution                           *environmentCreateExecution
	PKIClient                           *environmentCreatePKIClient
	ServiceCeremony                     *environmentCreateServiceCeremony
	ServiceSessionRecovery              *createServiceSessionRecovery
	ProductIdentityScope                string
	ProductIdentityOpenBaoTrustProfile  string
}

type environmentCreateRegisteredIssuer struct {
	ID, Environment, Domain, Kind, Status                 string
	Certificate, ChainPEM, RootFingerprint, BundleVersion string
	Reference                                             string
	Document                                              map[string]any
}

type environmentCreateServiceCeremony struct {
	Root, Intermediate                                                                          environmentCreateRegisteredIssuer
	BootstrapSessionID, BootstrapCredentialSecret, BootstrapCredentialNamespace, BootstrapClaim string
	BootstrapCredentialNamespaces                                                               []string
	RootCRLFile, RootCRLSecret                                                                  string
	RootCRLPEM                                                                                  string
	CleanupBinding                                                                              environmentBootstrapCleanupBinding
	CredentialUIDs                                                                              map[string]string
	CustodyReader                                                                               string
}

func environmentCreateRegisteredIssuerFromMap(value map[string]any, rootFingerprint string) (environmentCreateRegisteredIssuer, error) {
	i := environmentCreateRegisteredIssuer{ID: deploymentplan.String(value["issuer_id"]), Environment: deploymentplan.String(value["environment"]), Domain: deploymentplan.String(value["trust_domain"]), Kind: deploymentplan.String(value["kind"]), Status: deploymentplan.String(value["status"]), Certificate: deploymentplan.String(value["certificate_pem"]), ChainPEM: deploymentplan.String(value["chain_pem"]), RootFingerprint: rootFingerprint, BundleVersion: deploymentplan.String(value["trust_bundle_version"]), Reference: deploymentplan.String(value["signer_reference"])}
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &i.Document) != nil {
		return i, environmentCreateBootstrapBlocker("actual registered issuer public document cannot be preserved")
	}
	if i.Kind == "root" {
		i.RootFingerprint = deploymentplan.String(value["certificate_fingerprint_sha256"])
	}
	if !environmentBootstrapOwnerUUID.MatchString(i.ID) || !environmentPreflightDigest.MatchString(i.RootFingerprint) || !environmentPreflightDigest.MatchString(i.BundleVersion) || i.Certificate == "" || i.ChainPEM == "" || environmentPreflightBytesSHA256([]byte(i.ChainPEM)) != i.BundleVersion {
		return i, environmentCreateBootstrapBlocker("registered issuer lacks original certificate, lineage or bundle metadata")
	}
	return i, nil
}

var environmentCreateConsumerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type environmentCreateBootstrapRuntimeKey struct{}

func withEnvironmentCreateBootstrapRuntime(ctx context.Context, runtime environmentCreateBootstrapRuntime) context.Context {
	return context.WithValue(ctx, environmentCreateBootstrapRuntimeKey{}, runtime)
}

func environmentCreateBootstrapBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.bootstrap.pki", Cause: cause, Next: "verify the selected operator, original lineage, concrete bootstrap policy and native ceremony journal; never fabricate authorization or replace recorded identity"}
}

func buildEnvironmentCreateBootstrapPlan(ctx context.Context, cfg deploymentConfig, release environmentRelease, store secretStore, operator map[string]string) (environmentCreateBootstrapPlan, error) {
	p := environmentCreateBootstrapPlan{AppRoutingMode: "internal-service", IngressAdmissionProfile: environmentCreateIngressAdmissionProfile, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Image: release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, AccountManagerImage: release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference, Templates: map[string]string{}}
	p.Images = map[string]string{}
	for role, image := range release.Images {
		p.Images[role] = image.Reference
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if cfg.Environment != "dev" && cfg.Environment != "staging" || store.Environment != cfg.Environment || p.Stack != "video-cloud-"+cfg.Environment || release.Environment != cfg.Environment || release.Stack != p.Stack || !immutableEnvironmentImage.MatchString(p.Image) {
		return p, environmentCreateBootstrapBlocker("complete initial managed PKI requires an exact non-production environment and formal immutable image")
	}
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		return p, err
	}
	p.ProductIdentities = products
	if len(products) != 0 {
		p.NativeConnectivitySHA256 = deploymentplan.Digest(environmentNativeConnectivityMarker(cfg))
	}
	for _, name := range []string{"deploy/pki/render-staging-controller.py", "deploy/pki/render-staging-consumers.py", "deploy/pki/account-manager-managed.env.example", "deploy/pki/controller-service-clients.env.example", "deploy/pki/controller-host-identity.env.example", "deploy/pki/domain-consumers.env.example", "deploy/pki/server-crl-worker.env.example", "deploy/pki/app-crl-worker.env.example"} {
		raw, err := readBoundedEnvironmentFile(filepath.Join(cfg.Workspace, "repos", "rtk_video_cloud", filepath.FromSlash(name)), false)
		if err != nil {
			return p, environmentCreateBootstrapBlocker("maintained concrete managed PKI templates cannot be read")
		}
		p.Templates[name] = environmentPreflightBytesSHA256(raw)
	}
	endpoints := deploymentEndpointValues(cfg)
	p.PublicCertIssuerHost = lkeResolveCertIssuerTLSConfig(endpoints).PublicHost
	for _, domain := range managedPKIDomains {
		consumers := strings.Split(cfg.Values["PKI_REQUIRED_CONSUMERS_"+strings.ToUpper(domain)], ",")
		bundles := strings.Split(firstNonEmpty(cfg.Values["PKI_REQUIRED_BUNDLE_CONSUMERS_"+strings.ToUpper(domain)], strings.Join(consumers, ",")), ",")
		if len(consumers) == 0 || len(bundles) == 0 {
			return p, environmentCreateBootstrapBlocker("each managed trust domain requires an explicit consumer policy")
		}
		for _, list := range [][]string{consumers, bundles} {
			slices.Sort(list)
			for i, id := range list {
				if !environmentCreateConsumerID.MatchString(id) || i > 0 && list[i-1] == id {
					return p, environmentCreateBootstrapBlocker("managed trust consumer policy is malformed or duplicated")
				}
			}
		}
		policy := environmentCreateIssuerPolicy{Domain: domain, Consumers: consumers, BundleConsumers: bundles}
		switch domain {
		case "service":
			policy.ServerDNSNames = []string{"account-manager." + p.Stack + "-account-manager.svc", "account-manager." + p.Stack + "-account-manager.svc.cluster.local", "certissuer." + p.Stack + "-video-cloud.svc", "certissuer." + p.Stack + "-video-cloud.svc.cluster.local", "certissuer-service-pki." + p.Stack + "-video-cloud.svc", "pki-controller." + p.Stack + "-video-cloud.svc", "pki-controller." + p.Stack + "-video-cloud.svc.cluster.local"}
			for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
				policy.ServerDNSNames = append(policy.ServerDNSNames, name+"."+p.Stack+"-video-cloud.svc", name+"."+p.Stack+"-video-cloud.svc.cluster.local")
			}
			if endpoints["VIDEO_CLOUD_CERTISSUER_DOMAIN"] != "" {
				policy.ServerDNSNames = append(policy.ServerDNSNames, endpoints["VIDEO_CLOUD_CERTISSUER_DOMAIN"])
			}
			subjects, err := environmentCreateBootstrapSubjects(cfg)
			if err != nil {
				return p, err
			}
			for _, product := range products {
				subjects = append(subjects, product.Subject)
			}
			policy.ServiceClientIDs = append(subjects, "service:deployment-bootstrap")
			slices.Sort(policy.ServiceClientIDs)
		case "mqtt":
			policy.ServerDNSNames = []string{"mqtt-pki." + p.Stack + "-video-cloud.svc", "mqtt-pki." + p.Stack + "-video-cloud.svc.cluster.local"}
			if host := firstNonEmpty(cfg.Values["LKE_MQTT_PUBLIC_DOMAIN"], endpoints["VIDEO_CLOUD_DOMAIN"]); host != "" {
				policy.ServerDNSNames = append(policy.ServerDNSNames, host)
			}
		case "openbao_tls":
			policy.ServerDNSNames = slices.DeleteFunc(lkeOpenBaoDNSNames(map[string]string{"CLOUD_STACK_NAME": p.Stack}), func(name string) bool { return name == "openbao" })
		}
		slices.Sort(policy.ServerDNSNames)
		p.Domains = append(p.Domains, policy)
	}
	p.Claims = []string{"pki-service-bootstrap-state", "pki-controller-state", "certissuer-service-state", "factory-service-state", "video-cloud-api-service-state", "video-cloud-api-pki-controller-identity", "video-cloud-api-pki-trust", "video-cloud-api-app-service-state", "video-cloud-api-app-pki-trust", "video-cloud-logingester-service-state", "mqtt-pki-data", "mqtt-pki-trust", "mqtt-pki-host-identity", "account-manager-service-state", "openbao-pki-identity"}
	for i := range p.ProductIdentities {
		p.ProductIdentities[i].PolicySHA256 = deploymentplan.Digest(findEnvironmentCreateDomainPolicy(p, "service"))
		p.Claims = append(p.Claims, p.ProductIdentities[i].Claim)
	}
	return p, nil
}

func qualifyEnvironmentCreateBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateBootstrapLocalState(ctx, cfg, store, operator); err != nil {
		return err
	}
	return qualifyEnvironmentCreateBootstrapCapabilities(ctx, cfg, p, store, operator)
}

func qualifyEnvironmentCreateBootstrapCapabilities(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, preservedRoots ...string) error {
	return qualifyEnvironmentCreateBootstrapCapabilitiesWithPublications(ctx, cfg, p, store, operator, nil, preservedRoots...)
}

// Published issuer files are allowed only by the separately authenticated
// all-CA continuation. Ordinary fresh/bootstrap callers retain the empty guard.
func qualifyEnvironmentCreateBootstrapCapabilitiesWithPublications(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, publications map[string]string, preservedRoots ...string) error {
	return qualifyEnvironmentCreateBootstrapCapabilitiesWithRouting(ctx, cfg, p, store, operator, publications, "", preservedRoots...)
}

// Only the authenticated stopped-core profile may supply its already accepted
// App routing allocation. Issuer publications remain exactly five files.
func qualifyEnvironmentCreateBootstrapCapabilitiesWithRouting(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, publications map[string]string, routingSHA string, preservedRoots ...string) error {
	verifiedRoots := map[string]bool{}
	verifiedCustody := map[string]bool{"roots": true}
	if len(preservedRoots) > 4 {
		return environmentCreateBootstrapBlocker("only independently verified original Service, MQTT, OpenBao TLS and App Roots may be preserved")
	}
	for _, id := range preservedRoots {
		if !environmentBootstrapOwnerUUID.MatchString(id) || verifiedRoots[id] {
			return environmentCreateBootstrapBlocker("distinct independently verified original Roots required")
		}
		var manifest struct {
			Schema      string `json:"schema"`
			Environment string `json:"environment"`
			Domain      string `json:"trust_domain"`
			Issuer      string `json:"root_issuer_id"`
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "pki/custody/roots", id, "manifest.json"), true)
		if err != nil || json.Unmarshal(raw, &manifest) != nil {
			return environmentCreateBootstrapBlocker("verified original custody manifest cannot be read")
		}
		// The full manifest and its files were verified by the original-lineage
		// loader. Decode its bounded public fields without rejecting its hashes.
		if manifest.Schema != "rtk-root-custody/v1" || manifest.Environment != cfg.Environment || manifest.Issuer != id || (manifest.Domain != "service" && manifest.Domain != "mqtt" && manifest.Domain != "openbao_tls" && manifest.Domain != "app") || verifiedCustody[manifest.Domain+"-root-v1-key-passphrase"] {
			return environmentCreateBootstrapBlocker("unverified or duplicate custody domain")
		}
		verifiedRoots[id] = true
		verifiedCustody[manifest.Domain+"-root-v1-key-passphrase"] = true
	}
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || store.Environment != cfg.Environment || p.Image == "" {
		return environmentCreateBootstrapBlocker("initial PKI recipe belongs to another target")
	}
	if cfg.Values["PKI_REQUIRE_USER_MFA"] != "false" {
		return environmentCreateBootstrapBlocker("initial password-authenticated ceremony requires the explicit non-production operator policy; it cannot manufacture an MFA assertion")
	}
	if p.AppRoutingMode != "internal-service" || p.IngressAdmissionProfile != environmentCreateIngressAdmissionProfile || len(p.Domains) != len(managedPKIDomains) || len(p.Templates) != 8 || len(p.Claims) == 0 {
		return environmentCreateBootstrapBlocker("the initial recipe lacks complete native trust domains, templates or durable claims")
	}
	for _, key := range []string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "PKI_OPERATOR_SIGNER_REF"} {
		path, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("operator", "env", key))
		if err != nil {
			return environmentCreateBootstrapBlocker("initial configured operator credentials do not have canonical private permissions")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) != operator[key] {
			return environmentCreateBootstrapBlocker("initial configured operator credentials are missing or differ from the selected snapshot")
		}
	}
	for name, hash := range p.Templates {
		raw, err := readBoundedEnvironmentFile(filepath.Join(cfg.Workspace, "repos", "rtk_video_cloud", filepath.FromSlash(name)), false)
		if err != nil || environmentPreflightBytesSHA256(raw) != hash {
			return environmentCreateBootstrapBlocker("maintained PKI template changed after recipe qualification")
		}
	}
	if err := qualifyEnvironmentCreateBootstrapLineage(store, verifiedRoots, verifiedCustody, publications, routingSHA); err != nil {
		return err
	}
	// A previously published API image may not yet contain a newly required
	// ceremony binary. Check actual immutable image contents before allocation.
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", p.Image, "-ec", "test -x /app/pkicontroller; test -x /app/pkiceremony; test -x /app/serviceidentity-bootstrap; test -x /app/openbaopkihost; test -x /app/emqxpkihost; openssl version >/dev/null")
	if command.Run() != nil {
		return environmentCreateBootstrapBlocker("the exact formal API image lacks complete native bootstrap and encrypted custody ceremony capabilities")
	}
	capabilities := []environmentProductCapability{{p.AccountManagerImage, "/app/rtk-account-manager", "account-manager", []string{"deployment-bootstrap-finalize-v1"}}, {p.Image, "/app/pkimanagement", "pkimanagement", []string{"first-trust-v1", "deployment-bootstrap-finalize-v1"}}, {p.Image, "/app/certissuer", "certissuer", []string{"create-service-enrollment-v1", "registry-only-issuance-v1"}}, {p.Image, "/app/api", "api", []string{"fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1"}}, {p.Image, "/app/logingester", "logingester", []string{"fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1"}}, {p.Image, "/app/pkibroker", "pkibroker", []string{"fresh-bootstrap-trust-v1"}}, {p.Image, "/app/serviceidentity-bootstrap", "serviceidentity-bootstrap", []string{"owned-server-export-v1"}}, {p.Images["LKE_MQTT_PKI_IMAGE"], "/usr/local/bin/emqxpkihost", "emqxpkihost", managedMQTTRequiredCapabilities(cfg.Values["TEST_LAB_ENABLED"] == "true")}}
	productCaps, err := productRegistrationCapabilities(p.Image, p.ProductIdentities)
	if err != nil {
		return err
	}
	capabilities = append(capabilities, productCaps...)
	for _, capability := range capabilities {
		if !immutableEnvironmentImage.MatchString(capability.Image) {
			return environmentCreateBootstrapBlocker("the initial authentication capability has no exact formal immutable image")
		}
		command := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", capability.Binary, capability.Image, "--deployment-capabilities")
		raw, err := command.Output()
		var actual struct {
			Schema   string   `json:"schema"`
			Service  string   `json:"service"`
			Features []string `json:"features"`
		}
		if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &actual) != nil || actual.Schema != "rtk-cloud.deployment-capabilities/v1" || actual.Service != capability.Service {
			return environmentCreateBootstrapBlocker("the exact formal image has no verified native first-trust or authenticated finalization capability")
		}
		for _, feature := range capability.Features {
			if !slices.Contains(actual.Features, feature) {
				return environmentCreateBootstrapBlocker("the exact formal image lacks a required native initial-ceremony capability")
			}
		}
	}
	return nil
}

// A removed provider cluster is not evidence that retained identity may be
// regenerated. Managed continuation admits five pinned issuer files and, only
// for the stopped-core profile, one separately authenticated App routing file.
func qualifyEnvironmentCreateBootstrapLineage(store secretStore, verifiedRoots, verifiedCustody map[string]bool, publications map[string]string, routing ...string) error {
	routingSHA := ""
	if len(routing) > 1 {
		return environmentCreateBootstrapBlocker("managed App routing allowance must have one exact accepted file digest")
	}
	if len(routing) == 1 {
		routingSHA = routing[0]
	}
	if routingSHA != "" && (!environmentPreflightDigest.MatchString(routingSHA) || len(publications) != len(managedPKIDomains) || len(verifiedRoots) != 4) {
		return environmentCreateBootstrapBlocker("managed App routing allowance requires all original published issuers and custody owners")
	}
	if len(publications) != 0 {
		if len(publications) != len(managedPKIDomains) || len(verifiedRoots) != 4 {
			return environmentCreateBootstrapBlocker("managed publication allowance requires all five domains and four original custody owners")
		}
		for _, domain := range managedPKIDomains {
			if !environmentPreflightDigest.MatchString(publications["pki/"+domain+"/issuer.json"]) {
				return environmentCreateBootstrapBlocker("managed publication allowance lacks its exact qualified file digest")
			}
		}
	}
	for _, relative := range []string{"pki/services/issuer.json", "pki/service/issuer.json", "pki/device/issuer.json", "pki/mqtt/issuer.json", "pki/app/issuer.json", "pki/openbao_tls/issuer.json", "pki/custody/roots"} {
		path, err := store.safePath(filepath.FromSlash(relative))
		if err != nil {
			return environmentCreateBootstrapBlocker("canonical original PKI lineage cannot be inspected")
		}
		if hash := publications[relative]; hash != "" {
			if err := verifyCreateBootstrapPublishedFile(store, relative, hash); err != nil {
				return err
			}
			continue
		}
		_, err = os.Lstat(path)
		if len(verifiedRoots) > 0 && relative == "pki/custody/roots" {
			entries, readErr := os.ReadDir(path)
			if readErr != nil || len(entries) != len(verifiedRoots) {
				return environmentCreateBootstrapBlocker("custody contains another or unverified Root")
			}
			for _, entry := range entries {
				if !verifiedRoots[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					return environmentCreateBootstrapBlocker("custody contains another or unverified Root")
				}
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return environmentCreateBootstrapBlocker("original PKI issuer or custody lineage exists or cannot be inspected; a fresh ceremony cannot replace it")
		}
	}
	for _, relative := range []string{"pki/custody", "pki/services", "pki/service", "pki/device", "pki/mqtt", "pki/certissuer", "pki/app", "pki/openbao_tls"} {
		path, err := store.safePath(filepath.FromSlash(relative))
		if err != nil {
			return environmentCreateBootstrapBlocker("canonical original PKI directory cannot be inspected")
		}
		entries, err := os.ReadDir(path)
		if hash := publications[relative+"/issuer.json"]; hash != "" {
			expected := map[string]string{"issuer.json": hash}
			if relative == "pki/app" && routingSHA != "" {
				expected["routing.json"] = routingSHA
			}
			if err != nil || len(entries) != len(expected) {
				return environmentCreateBootstrapBlocker("published managed domain contains unverified or extra identity material: domain=" + strings.TrimPrefix(relative, "pki/"))
			}
			for _, entry := range entries {
				fileSHA := expected[entry.Name()]
				if fileSHA == "" || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					return environmentCreateBootstrapBlocker("published managed domain contains unverified or extra identity material: domain=" + strings.TrimPrefix(relative, "pki/"))
				}
				if err := verifyCreateBootstrapPublishedFile(store, relative+"/"+entry.Name(), fileSHA); err != nil {
					return err
				}
			}
			continue
		}
		if len(verifiedRoots) > 0 && relative == "pki/custody" {
			if err != nil || len(entries) != len(verifiedCustody) {
				return environmentCreateBootstrapBlocker("unexpected original custody material")
			}
			for _, entry := range entries {
				if !verifiedCustody[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
					return environmentCreateBootstrapBlocker("unverified custody material exists")
				}
			}
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(entries) > 0 {
			return environmentCreateBootstrapBlocker("retained PKI identity material exists or cannot be inspected; qualify its original restore before fresh creation")
		}
	}
	return nil
}

func verifyCreateBootstrapPublishedFile(store secretStore, relative, hash string) error {
	path, err := store.safePath(relative)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return environmentCreateBootstrapBlocker("canonical managed publication ancestors cannot be verified")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || environmentPreflightBytesSHA256(raw) != hash {
		return environmentCreateBootstrapBlocker("published managed issuer file differs from its verified original native lineage")
	}
	return nil
}

// Keep the request field order identical to the owning registry's CreateRequest:
// the exact digest is checked against its real server-returned operation.
type environmentCreateIssuerRequest struct {
	ServerDNSNames   []string `json:"server_dns_names,omitempty"`
	ServiceClientIDs []string `json:"service_client_ids,omitempty"`
	Environment      string   `json:"environment"`
	Domain           string   `json:"trust_domain"`
	Kind             string   `json:"kind"`
	ParentID         string   `json:"parent_issuer_id,omitempty"`
}
type environmentCreateIssuerOperation struct {
	ID       string `json:"operation_id"`
	IssuerID string `json:"issuer_id"`
	Digest   string `json:"request_sha256"`
	Status   string `json:"status"`
}
type environmentCreatePKIClient struct {
	Origin                             *url.URL
	HTTP                               *http.Client
	Environment                        string
	OperatorID                         string
	Execution                          *environmentCreateExecution
	ReservationRecoveryReview          string
	RejectedOpenBaoIntermediateRequest *environmentCreateIssuerRequest
	mutationScope                      string
	token                              string
	authenticatedAt                    time.Time
	operatorCredentials                map[string]string
}

type environmentCreatePKIHTTPFailure struct {
	Status int
	Cause  error
}

func (e *environmentCreatePKIHTTPFailure) Error() string { return e.Cause.Error() }
func (e *environmentCreatePKIHTTPFailure) Unwrap() error { return e.Cause }

func newEnvironmentCreatePKIClient(origin string, client *http.Client, environment, operatorID string) (*environmentCreatePKIClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()) || environment != "dev" && environment != "staging" {
		return nil, environmentCreateBootstrapBlocker("initial ceremony requires the selected HTTPS Account Manager or its own native loopback tunnel")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 || copy.Timeout > time.Minute {
		copy.Timeout = 30 * time.Second
	}
	return &environmentCreatePKIClient{Origin: u, HTTP: &copy, Environment: environment, OperatorID: operatorID}, nil
}

func (c *environmentCreatePKIClient) call(ctx context.Context, method, path, key string, body any, output any) error {
	if c == nil || c.Origin == nil || c.HTTP == nil || !strings.HasPrefix(path, "/v1/") || strings.ContainsAny(path, "?#") {
		return environmentCreateBootstrapBlocker("invalid fixed initial ceremony request")
	}
	// Product stages can wait longer than one access JWT. Refresh before the
	// next request using the same operator, never by retrying an uncertain POST.
	if path != "/v1/auth/login" && c.token != "" && c.operatorCredentials != nil {
		if err := refreshEnvironmentCreateAuthentication(ctx, c, c.operatorCredentials); err != nil {
			return err
		}
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil || len(raw) > 256<<10 {
			return environmentCreateBootstrapBlocker("initial ceremony request exceeds its bound")
		}
	}
	u := *c.Origin
	u.Path = path
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(raw))
	if err != nil {
		return environmentCreateBootstrapBlocker("initial ceremony request cannot be prepared")
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	measured := beginDeploymentQuery(ctx, "pki-http")
	response, err := c.HTTP.Do(request)
	var queryErr error = err
	if err == nil && response.StatusCode >= 400 {
		queryErr = errors.New("HTTP failure")
	}
	measured(queryErr)
	if err != nil {
		return environmentCreateBootstrapBlocker("authenticated initial ceremony request failed or has an uncertain outcome; retain its original journal")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return &environmentCreatePKIHTTPFailure{response.StatusCode, environmentCreateBootstrapBlocker(fmt.Sprintf("authenticated initial ceremony was rejected with HTTP %d; inspect its original registered operation before resuming", response.StatusCode))}
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(result) > 1<<20 {
		return environmentCreateBootstrapBlocker("initial ceremony response is unavailable or exceeds its bound")
	}
	if output != nil && json.Unmarshal(result, output) != nil {
		return environmentCreateBootstrapBlocker("initial ceremony returned invalid public metadata")
	}
	return ctx.Err()
}

func (c *environmentCreatePKIClient) Authenticate(ctx context.Context, email, password string) (string, error) {
	if email == "" || password == "" || c.token != "" {
		return "", environmentCreateBootstrapBlocker("one genuine initial operator password login is required")
	}
	var result struct {
		User struct {
			ID, Email  string
			DisabledAt *time.Time `json:"disabled_at"`
			Pending    bool       `json:"signup_pending_verification"`
		} `json:"user"`
		Tokens struct {
			Access string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := c.call(ctx, http.MethodPost, "/v1/auth/login", "", map[string]string{"email": email, "password": password}, &result); err != nil {
		return "", err
	}
	if !environmentBootstrapOwnerUUID.MatchString(result.User.ID) || result.User.ID == "00000000-0000-0000-0000-000000000000" || !strings.EqualFold(result.User.Email, strings.TrimSpace(email)) || result.User.DisabledAt != nil || result.User.Pending || result.Tokens.Access == "" || c.OperatorID != "" && result.User.ID != c.OperatorID {
		return "", environmentCreateBootstrapBlocker("authenticated user does not match the original configured operator; fresh bootstrap cannot change that identity")
	}
	c.token = result.Tokens.Access
	c.OperatorID = result.User.ID
	c.authenticatedAt = time.Now().UTC()
	return result.User.ID, nil
}

func refreshEnvironmentCreateAuthentication(ctx context.Context, c *environmentCreatePKIClient, operator map[string]string) error {
	if c == nil || c.token == "" {
		return environmentCreateBootstrapBlocker("original authenticated operator session is required")
	}
	if time.Since(c.authenticatedAt) < 3*time.Minute {
		return nil
	}
	fresh, err := newEnvironmentCreatePKIClient(c.Origin.String(), c.HTTP, c.Environment, c.OperatorID)
	if err != nil {
		return err
	}
	if _, err := fresh.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return err
	}
	c.token, c.authenticatedAt = fresh.token, fresh.authenticatedAt
	return nil
}

func (c *environmentCreatePKIClient) ReserveIssuer(ctx context.Context, key string, request environmentCreateIssuerRequest) (environmentCreateIssuerOperation, error) {
	var operation environmentCreateIssuerOperation
	if c.token == "" || request.Environment != c.Environment || key == "" || len(key) > 200 || request.Kind != "root" && request.Kind != "intermediate" || !slices.Contains(managedPKIDomains, request.Domain) || request.Kind == "root" && request.ParentID != "" || request.Kind == "intermediate" && !environmentBootstrapOwnerUUID.MatchString(request.ParentID) {
		return operation, environmentCreateBootstrapBlocker("issuer reservation differs from the exact authenticated initial policy")
	}
	journalKey := "pki-reserve-" + request.Domain + "-" + request.Kind
	originalKey := journalKey
	if old := c.RejectedOpenBaoIntermediateRequest; old != nil && request.Domain == "openbao_tls" && request.Kind == "intermediate" {
		if c.Execution == nil || !environmentPreflightDigest.MatchString(c.ReservationRecoveryReview) {
			return operation, environmentCreateBootstrapBlocker("qualified original intermediate rejection required")
		}
		names, err := correctedOpenBaoDNSPolicy(old.ServerDNSNames)
		if err != nil {
			return operation, err
		}
		corrected := *old
		corrected.ServerDNSNames = names
		if deploymentplan.Digest(corrected) != deploymentplan.Digest(request) {
			return operation, environmentCreateBootstrapBlocker("corrected intermediate request differs")
		}
		matched := false
		oldIntent, err := environmentCreateIssuerIntentSHA(*old)
		if err != nil {
			return operation, err
		}
		for _, v := range c.Execution.Journal.Entries {
			if v.Key == originalKey && v.State == "pending" && len(v.Receipt) == 0 && v.IntentSHA256 == oldIntent {
				matched = true
			}
		}
		if !matched {
			return operation, environmentCreateBootstrapBlocker("original rejected request has no exact no-write checkpoint")
		}
		journalKey += "-recovery-" + c.ReservationRecoveryReview
	}
	if c.ReservationRecoveryReview != "" && request.Domain == "service" && request.Kind == "root" {
		if c.Execution == nil || !environmentPreflightDigest.MatchString(c.ReservationRecoveryReview) {
			return operation, environmentCreateBootstrapBlocker("exact qualified no-write reservation recovery required")
		}
		raw, _ := json.Marshal(request)
		sha, _ := environmentCreatePlanSHA256(raw)
		matched := false
		for _, entry := range c.Execution.Journal.Entries {
			if entry.Key == originalKey && entry.State == "pending" && len(entry.Receipt) == 0 && entry.IntentSHA256 == sha {
				matched = true
			}
		}
		if !matched {
			return operation, environmentCreateBootstrapBlocker("original failed reservation intent differs; no replay allowed")
		}
		journalKey += "-recovery-" + c.ReservationRecoveryReview
	}
	if err := c.beginMutation(journalKey, request); err != nil {
		return operation, err
	}
	if err := c.call(ctx, http.MethodPost, "/v1/platform/pki/operations", key, request, &operation); err != nil {
		return operation, err
	}
	if !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) || operation.Digest != deploymentplan.Digest(request) || operation.Status != "requested" {
		return operation, environmentCreateBootstrapBlocker("registry issuer reservation does not match its independently qualified request digest")
	}
	if err := c.completeMutation(journalKey, operation); err != nil {
		return operation, err
	}
	if journalKey != originalKey {
		if err := c.completeMutation(originalKey, operation); err != nil {
			return operation, err
		}
		c.ReservationRecoveryReview = ""
		c.RejectedOpenBaoIntermediateRequest = nil
	}
	return operation, nil
}

func (c *environmentCreatePKIClient) AuthorizeIssuer(ctx context.Context, operation environmentCreateIssuerOperation) error {
	if c.token == "" || !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentPreflightDigest.MatchString(operation.Digest) {
		return environmentCreateBootstrapBlocker("exact authenticated issuer authorization is unavailable")
	}
	key := "pki-authorize-" + operation.ID
	if err := c.beginMutation(key, operation); err != nil {
		return err
	}
	if err := c.call(ctx, http.MethodPost, "/v1/platform/pki/operations/"+operation.ID+"/authorize", "", map[string]string{"request_sha256": operation.Digest}, nil); err != nil {
		return err
	}
	actual, err := c.ReadIssuer(ctx, operation.IssuerID)
	if err != nil || actual["status"] != "approved" {
		return environmentCreateBootstrapBlocker("authorized issuer has no original approved registry readback")
	}
	return c.completeMutation(key, actual)
}

func (c *environmentCreatePKIClient) beginMutation(key string, intent any) error {
	if c.Execution == nil {
		return nil
	} // Isolated protocol tests do not have a deployment owner.
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return c.Execution.BeginEntry(c.mutationKey(key), "bootstrap", raw, time.Now().UTC())
}

func (c *environmentCreatePKIClient) completeMutation(key string, receipt any) error {
	if c.Execution == nil {
		return nil
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := c.Execution.RecordReceipt(c.mutationKey(key), raw); err != nil {
		return err
	}
	return c.Execution.CompleteEntry(c.mutationKey(key), time.Now().UTC())
}

func (c *environmentCreatePKIClient) mutationKey(key string) string {
	if c.mutationScope != "" {
		return "product-identities-" + c.mutationScope + "-" + key
	}
	return key
}

func (c *environmentCreatePKIClient) ReadIssuer(ctx context.Context, id string) (map[string]any, error) {
	var issuer map[string]any
	if !environmentBootstrapOwnerUUID.MatchString(id) {
		return nil, environmentCreateBootstrapBlocker("exact registered issuer UUID required")
	}
	if err := c.call(ctx, http.MethodGet, "/v1/platform/pki/issuers/"+id, "", nil, &issuer); err != nil {
		return nil, err
	}
	if issuer["issuer_id"] != id || issuer["environment"] != c.Environment {
		return nil, environmentCreateBootstrapBlocker("registry issuer metadata belongs to another identity or environment")
	}
	return issuer, nil
}

func (c *environmentCreatePKIClient) ProvisionIssuer(ctx context.Context, operation environmentCreateIssuerOperation, csr string) (map[string]any, error) {
	var issuer map[string]any
	if !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) {
		return nil, environmentCreateBootstrapBlocker("original registered issuer operation required")
	}
	key := "pki-provision-" + operation.ID
	if err := c.beginMutation(key, deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "csr_sha256": environmentPreflightBytesSHA256([]byte(csr))}); err != nil {
		return nil, err
	}
	if err := c.call(ctx, http.MethodPost, "/v1/platform/pki/operations/"+operation.ID+"/provision", "", map[string]string{"csr_pem": csr}, &issuer); err != nil {
		return nil, err
	}
	if issuer["issuer_id"] != operation.IssuerID || issuer["environment"] != c.Environment || issuer["status"] != "provisioning" {
		return nil, environmentCreateBootstrapBlocker("provisioning result changed the registered issuer owner")
	}
	if err := c.completeMutation(key, issuer); err != nil {
		return nil, err
	}
	return issuer, nil
}

func (c *environmentCreatePKIClient) ImportIssuer(ctx context.Context, operation environmentCreateIssuerOperation, certificate string) error {
	if !environmentBootstrapOwnerUUID.MatchString(operation.ID) || certificate == "" {
		return environmentCreateBootstrapBlocker("exact original issuer operation and signed certificate required")
	}
	key := "pki-import-" + operation.ID
	if err := c.beginMutation(key, deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "certificate_sha256": environmentPreflightBytesSHA256([]byte(certificate))}); err != nil {
		return err
	}
	if err := c.call(ctx, http.MethodPost, "/v1/platform/pki/operations/"+operation.ID+"/import", "", map[string]string{"certificate_pem": certificate}, nil); err != nil {
		return err
	}
	actual, err := c.ReadIssuer(ctx, operation.IssuerID)
	if err != nil || actual["status"] != "ready" || actual["certificate_pem"] != certificate {
		return environmentCreateBootstrapBlocker("issuer import has no exact native ready-certificate readback")
	}
	return c.completeMutation(key, actual)
}

// The native controller verifies real bundle receipts. The deployment adapter
// never authors acknowledgments or treats ready metadata as activation.
func (c *environmentCreatePKIClient) ActivateIssuer(ctx context.Context, operation environmentCreateIssuerOperation) error {
	if !environmentBootstrapOwnerUUID.MatchString(operation.ID) {
		return environmentCreateBootstrapBlocker("original registered issuer activation operation required")
	}
	key := "pki-activate-" + operation.ID
	if err := c.beginMutation(key, operation); err != nil {
		return err
	}
	if err := c.call(ctx, http.MethodPost, "/v1/platform/pki/operations/"+operation.ID+"/activate", "", nil, nil); err != nil {
		return err
	}
	actual, err := c.ReadIssuer(ctx, operation.IssuerID)
	if err != nil || actual["status"] != "active" {
		return environmentCreateBootstrapBlocker("issuer activation has no original active registry readback")
	}
	return c.completeMutation(key, actual)
}

func renderEnvironmentCreatePKIPhase(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, phase string, args ...string) ([]deploymentplan.Definition, error) {
	if phase != "migrate" && phase != "grants" && phase != "bootstrap" && phase != "service-bootstrap" {
		return nil, environmentCreateBootstrapBlocker("an implemented exact managed bootstrap phase is required")
	}
	path := filepath.Join(cfg.Workspace, "repos", "rtk_video_cloud", "deploy", "pki", "render-staging-controller.py")
	raw, err := readBoundedEnvironmentFile(path, false)
	if err != nil || environmentPreflightBytesSHA256(raw) != p.Templates["deploy/pki/render-staging-controller.py"] {
		return nil, environmentCreateBootstrapBlocker("qualified concrete bootstrap renderer changed")
	}
	command := exec.CommandContext(ctx, "python3", append([]string{path, "--environment", p.Environment, "--image", p.Image, "--phase", phase}, args...)...)
	raw, err = command.Output()
	if err != nil || len(raw) > 4<<20 {
		return nil, environmentCreateBootstrapBlocker("concrete managed bootstrap phase could not be rendered")
	}
	var object deploymentplan.Object
	if json.Unmarshal(raw, &object) != nil {
		return nil, environmentCreateBootstrapBlocker("concrete bootstrap renderer produced invalid resources")
	}
	objects := []deploymentplan.Object{object}
	if object["kind"] == "List" {
		objects = nil
		for _, value := range certIssuerObjectList(object["items"]) {
			objects = append(objects, deploymentplan.Map(value))
		}
	}
	var definitions []deploymentplan.Definition
	for _, object := range objects {
		meta := deploymentplan.Map(object["metadata"])
		if meta["namespace"] != p.Stack+"-video-cloud" {
			return nil, environmentCreateBootstrapBlocker("bootstrap renderer escaped the selected namespace")
		}
		if prefix := deploymentplan.String(meta["generateName"]); prefix != "" {
			meta["name"] = strings.TrimSuffix(prefix, "-") + "-" + deploymentplan.Digest(struct {
				Phase string
				Args  []string
			}{phase, args})[:12]
			delete(meta, "generateName")
		}
		labels := deploymentplan.Map(meta["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			meta["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = p.Stack
		labels["app.kubernetes.io/managed-by"] = "rtk-cloud"
		resourcePhase := "bootstrap"
		if phase == "service-bootstrap" {
			// This Job needs its own ServiceAccount and claim before it can
			// create a Pod. Sorting all three as bootstrap would run the Job first.
			resourcePhase = environmentResourcePhase(deploymentplan.String(object["kind"]))
			if object["kind"] == "PersistentVolumeClaim" {
				deploymentplan.Map(object["spec"])["storageClassName"] = firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain")
			}
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: resourcePhase})
	}
	if len(definitions) == 0 {
		return nil, environmentCreateBootstrapBlocker("bootstrap renderer omitted required resources")
	}
	return definitions, nil
}

// Count native allocations from the actual catalog storage manifests and the
// declared fresh managed state claims. It does not read or require a future PV.
func countEnvironmentCreateWorkloadAllocations(cfg deploymentConfig, release environmentRelease, p environmentCreateBootstrapPlan) (int, error) {
	values := appendMap(appendMap(cfg.Values, cfg.AdapterValues), cfg.AdapterResolved)
	values = appendMap(values, deploymentLegacyLKEValues(values, cfg.Environment))
	values["CLOUD_ENV_NAME"] = cfg.Environment
	for role, image := range release.Images {
		values[role] = image.Reference
	}
	restore := installAllCredentialEnvironment(values)
	defer restore()
	claims := map[string]bool{}
	for _, name := range p.Claims {
		suffix := "video-cloud"
		if name == "account-manager-service-state" {
			suffix = "account-manager"
		}
		if name == "openbao-pki-identity" {
			suffix = "secrets"
		}
		claims[p.Stack+"-"+suffix+"/"+name] = true
	}
	var manifests []string
	manifests = append(manifests, lkePostgresStatefulSetManifest(values), lkeFleetValkeyStatefulSetManifest(values), lkeMQTTStatefulSetManifest(values))
	for _, workload := range k8sWorkloads(values) {
		if lkeSQLitePVCEnabled(values, workload.Key) {
			manifests = append(manifests, lkeSQLitePVCManifest(values, workload))
		}
	}
	if lkeLoggerRetentionStorageEnabled(values) {
		manifests = append(manifests, lkeLokiPVCManifest(values))
	}
	if lkeGrafanaPersistenceEnabled(values) {
		manifests = append(manifests, lkeGrafanaPVCManifest(values))
	}
	claims[p.Stack+"-platform/redis-data"] = true
	claims[p.Stack+"-video-cloud/video-cloud-prometheus-data"] = true
	// The maintained standard OpenBao file profile has independently persistent
	// data/audit state; Raft is qualified by its separate production profile.
	claims[p.Stack+"-secrets/data-openbao-0"] = true
	claims[p.Stack+"-secrets/audit-openbao-0"] = true
	add := func(object deploymentplan.Object) error {
		meta := deploymentplan.Map(object["metadata"])
		namespace := deploymentplan.String(meta["namespace"])
		if object["kind"] == "PersistentVolumeClaim" {
			claims[namespace+"/"+deploymentplan.String(meta["name"])] = true
		}
		if object["kind"] == "StatefulSet" {
			spec := deploymentplan.Map(object["spec"])
			replicas := 1
			if value, ok := spec["replicas"].(float64); ok {
				replicas = int(value)
				if value != float64(replicas) || replicas < 0 || replicas > 1000 {
					return environmentCreateBootstrapBlocker("StatefulSet storage allocation count is invalid")
				}
			}
			for _, template := range certIssuerObjectList(spec["volumeClaimTemplates"]) {
				name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(template)["metadata"])["name"])
				for ordinal := 0; ordinal < replicas; ordinal++ {
					claims[fmt.Sprintf("%s/%s-%s-%d", namespace, name, deploymentplan.String(meta["name"]), ordinal)] = true
				}
			}
		}
		return nil
	}
	for _, manifest := range manifests {
		decoder := yaml.NewDecoder(strings.NewReader(manifest))
		for {
			var object deploymentplan.Object
			err := decoder.Decode(&object)
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, environmentCreateBootstrapBlocker("maintained storage catalog could not be rendered")
			}
			raw, err := json.Marshal(object)
			if err != nil || json.Unmarshal(raw, &object) != nil {
				return 0, environmentCreateBootstrapBlocker("storage catalog has invalid API shape")
			}
			if err := add(object); err != nil {
				return 0, err
			}
		}
	}
	if cfg.Values["SUPPORT_TICKETS_ENABLED"] == "true" {
		store, err := newSecretStore("", cfg.Environment)
		if err != nil {
			return 0, err
		}
		values, chart, err := readEnvironmentZammadInputs(cfg)
		if err != nil {
			return 0, err
		}
		definitions, err := renderEnvironmentZammad(context.Background(), cfg, store, release, "create", values, chart)
		if err != nil {
			return 0, err
		}
		for _, definition := range definitions {
			if err := add(definition.Object); err != nil {
				return 0, err
			}
		}
	}
	return len(claims), nil
}

func runEnvironmentCreatePKICeremony(ctx context.Context, store secretStore, image string, args ...string) error {
	if !immutableEnvironmentImage.MatchString(image) || !filepath.IsAbs(store.Root) || strings.ContainsAny(store.Root, ",\r\n") {
		return environmentCreateBootstrapBlocker("the exact ceremony image or canonical private mount is unavailable")
	}
	for _, name := range []string{store.ConfigRoot, store.Root} {
		info, err := os.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return environmentCreateBootstrapBlocker("canonical encrypted custody requires real private directories")
		}
	}
	commandArgs := []string{"run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--mount", "type=bind,src=" + store.Root + ",dst=/store", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--entrypoint", "/app/pkiceremony", image}
	command := exec.CommandContext(ctx, "docker", append(commandArgs, args...)...)
	if command.Run() != nil {
		return environmentCreateBootstrapBlocker("native encrypted custody ceremony failed; its original private output is retained and never overwritten")
	}
	return ctx.Err()
}

func writeEnvironmentCreateBootstrapJSON(store secretStore, relative string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 256<<10 {
		return "", environmentCreateBootstrapBlocker("bounded public ceremony metadata cannot be encoded")
	}
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil {
		return "", environmentCreateBootstrapBlocker("canonical public ceremony path is unavailable")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "create"), filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return "", environmentCreateBootstrapBlocker("private ceremony directory cannot be prepared")
		}
	}
	if err := store.write(relative, raw, false); err != nil {
		return "", environmentCreateBootstrapBlocker("an original ceremony record exists or cannot be saved exclusively; preserve its lineage")
	}
	return "/store/" + filepath.ToSlash(relative), nil
}

// Allocate a new custody key only after an authentic operator request has been
// accepted and separately authorized by the registry. Every output is exclusive.
func executeEnvironmentCreateRootCeremony(ctx context.Context, client *environmentCreatePKIClient, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, executionSHA string, domain string) (environmentCreateIssuerOperation, map[string]any, error) {
	var operation environmentCreateIssuerOperation
	if !environmentPreflightDigest.MatchString(executionSHA) || domain == "device" || !slices.Contains(managedPKIDomains, domain) {
		return operation, nil, environmentCreateBootstrapBlocker("exact parent execution and supported offline Root domain required")
	}
	request := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: domain, Kind: "root"}
	operation, err := client.ReserveIssuer(ctx, "create-"+executionSHA+"-"+domain+"-root", request)
	if err != nil {
		return operation, nil, err
	}
	if err := client.AuthorizeIssuer(ctx, operation); err != nil {
		return operation, nil, err
	}
	issuer, err := client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return operation, nil, err
	}
	if issuer["status"] != "approved" || issuer["trust_domain"] != domain || issuer["signer_provider"] != "offline" || issuer["kind"] != "root" || issuer["csr_pem"] != nil && issuer["csr_pem"] != "" {
		return operation, nil, environmentCreateBootstrapBlocker("authenticated Root reservation is not the approved original initial request")
	}
	base := filepath.Join("deployment", "create", executionSHA, "bootstrap", domain)
	reservationPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "reservation.json"), issuer)
	if err != nil {
		return operation, nil, err
	}
	passRelative := filepath.Join("pki", "custody", domain+"-root-v1-key-passphrase")
	var password [32]byte
	if _, err := rand.Read(password[:]); err != nil {
		return operation, nil, environmentCreateBootstrapBlocker("new encrypted Root custody passphrase could not be allocated")
	}
	if err := store.write(passRelative, []byte(hex.EncodeToString(password[:])), false); err != nil {
		return operation, nil, environmentCreateBootstrapBlocker("original encrypted Root custody passphrase exists or cannot be saved; it is never replaced")
	}
	rootRelative := filepath.Join("pki", "custody", "roots", operation.IssuerID)
	for _, directory := range []string{filepath.Join(store.Root, "pki"), filepath.Join(store.Root, "pki", "custody"), filepath.Join(store.Root, "pki", "custody", "roots")} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return operation, nil, environmentCreateBootstrapBlocker("canonical Root custody directories are unavailable")
		}
	}
	if err := runEnvironmentCreatePKICeremony(ctx, store, p.Image, "generate", "--issuer", reservationPath, "--passphrase-file", "/store/"+filepath.ToSlash(passRelative), "--out", "/store/"+filepath.ToSlash(rootRelative), "--expected-request-sha256", operation.Digest); err != nil {
		return operation, nil, err
	}
	csrPath, err := privateDeploymentStatePath(store.Environment, filepath.Join(rootRelative, "csr.pem"))
	if err != nil {
		return operation, nil, err
	}
	csrPEM, err := readBoundedEnvironmentFile(csrPath, true)
	block, rest := pem.Decode(csrPEM)
	if err != nil || block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return operation, nil, environmentCreateBootstrapBlocker("original generated Root CSR is unavailable")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return operation, nil, environmentCreateBootstrapBlocker("original Root CSR signature is invalid")
	}
	csrHash := sha256.Sum256(csr.Raw)
	issuer, err = client.ProvisionIssuer(ctx, operation, string(csrPEM))
	if err != nil {
		return operation, nil, err
	}
	if issuer["csr_pem"] != string(csrPEM) {
		return operation, nil, environmentCreateBootstrapBlocker("registry provisioning changed the original custody CSR")
	}
	issuerPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "provisioning.json"), issuer)
	if err != nil {
		return operation, nil, err
	}
	signedRelative := filepath.Join(rootRelative, "signed")
	if err := runEnvironmentCreatePKICeremony(ctx, store, p.Image, "sign", "--issuer", issuerPath, "--key", "/store/"+filepath.ToSlash(filepath.Join(rootRelative, "ca-key.encrypted.pem")), "--passphrase-file", "/store/"+filepath.ToSlash(passRelative), "--out", "/store/"+filepath.ToSlash(signedRelative), "--expected-request-sha256", operation.Digest, "--expected-csr-sha256", hex.EncodeToString(csrHash[:])); err != nil {
		return operation, nil, err
	}
	certificatePath, err := privateDeploymentStatePath(store.Environment, filepath.Join(signedRelative, "certificate.pem"))
	if err != nil {
		return operation, nil, err
	}
	certificate, err := readBoundedEnvironmentFile(certificatePath, true)
	if err != nil {
		return operation, nil, environmentCreateBootstrapBlocker("native signed Root certificate cannot be read")
	}
	if err := client.ImportIssuer(ctx, operation, string(certificate)); err != nil {
		return operation, nil, err
	}
	issuer, err = client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return operation, nil, err
	}
	if issuer["status"] != "ready" || issuer["certificate_pem"] != string(certificate) || !environmentPreflightDigest.MatchString(deploymentplan.String(issuer["certificate_fingerprint_sha256"])) {
		return operation, nil, environmentCreateBootstrapBlocker("imported Root differs from its native signed custody certificate")
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "ready.json"), issuer); err != nil {
		return operation, nil, err
	}
	certBlock, rest := pem.Decode(certificate)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return operation, nil, environmentCreateBootstrapBlocker("native custody certificate has an invalid public shape")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || cert.CheckSignatureFrom(cert) != nil {
		return operation, nil, environmentCreateBootstrapBlocker("native custody Root is not independently self-signed")
	}
	csrPublic, csrErr := x509.MarshalPKIXPublicKey(csr.PublicKey)
	certPublic, certErr := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if csrErr != nil || certErr != nil || !bytes.Equal(csrPublic, certPublic) {
		return operation, nil, environmentCreateBootstrapBlocker("native custody Root public key differs from its original CSR")
	}
	if err := store.write(filepath.Join(rootRelative, "certificate.pem"), certificate, false); err != nil {
		return operation, nil, environmentCreateBootstrapBlocker("original custody Root certificate cannot be published exclusively")
	}
	signer, err := store.read(filepath.Join("operator", "env", "PKI_OPERATOR_SIGNER_REF"))
	if err != nil || signer == "" {
		return operation, nil, environmentCreateBootstrapBlocker("original configured signer reference is unavailable")
	}
	filePaths := map[string]string{"key": filepath.ToSlash(filepath.Join(rootRelative, "ca-key.encrypted.pem")), "certificate": filepath.ToSlash(filepath.Join(rootRelative, "certificate.pem")), "csr": filepath.ToSlash(filepath.Join(rootRelative, "csr.pem")), "passphrase": filepath.ToSlash(passRelative)}
	fileHashes := map[string]string{}
	for key, relative := range filePaths {
		path, err := privateDeploymentStatePath(store.Environment, filepath.FromSlash(relative))
		if err != nil {
			return operation, nil, err
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			return operation, nil, environmentCreateBootstrapBlocker("original custody file checksum cannot be verified")
		}
		fileHashes[key] = environmentPreflightBytesSHA256(raw)
	}
	manifest := map[string]any{"schema": "rtk-root-custody/v1", "environment": cfg.Environment, "trust_domain": domain, "root_issuer_id": operation.IssuerID, "certificate_fingerprint_sha256": issuer["certificate_fingerprint_sha256"], "operator_user_id": client.OperatorID, "signer_reference": signer, "key_format": "encrypted-pkcs8-pem", "paths": filePaths, "file_sha256": fileHashes, "created_at": time.Now().UTC(), "verified_at": time.Now().UTC(), "ceremony_operation_id": operation.ID}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(rootRelative, "manifest.json"), manifest); err != nil {
		return operation, nil, err
	}
	return operation, issuer, nil
}

type environmentCreateOpenBaoIssuerPolicies struct {
	Environment               string `json:"environment"`
	IssuerID                  string `json:"issuer_id"`
	Mount                     string `json:"mount"`
	ControllerPolicy          string `json:"controller_policy"`
	SignerPolicy              string `json:"signer_policy"`
	ServiceClientSignerPolicy string `json:"service_client_signer_policy,omitempty"`
	RecoveryPolicy            string `json:"recovery_policy"`
}

// All policy text is emitted by the owning native registry from its approved
// immutable issuer record. No caller supplies ACL text or a broad mount prefix.
func configureEnvironmentCreateIssuerPolicies(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, issuer map[string]any) (environmentCreateOpenBaoIssuerPolicies, error) {
	var policy environmentCreateOpenBaoIssuerPolicies
	id := deploymentplan.String(issuer["issuer_id"])
	if runtime.Execution == nil || !environmentBootstrapOwnerUUID.MatchString(id) || issuer["environment"] != cfg.Environment || issuer["signer_provider"] != "openbao" || issuer["status"] != "approved" {
		return policy, environmentCreateBootstrapBlocker("approved native online issuer and original create execution are required for exact ACL installation")
	}
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", id)
	if err != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.Environment != cfg.Environment || policy.IssuerID != id || policy.Mount != issuer["signer_reference"] || policy.ControllerPolicy == "" || policy.SignerPolicy == "" || policy.RecoveryPolicy == "" {
		return policy, environmentCreateBootstrapBlocker("the real approved issuer's exact OpenBao ACL policy cannot be rendered")
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil || len(bytes.TrimSpace(token)) == 0 || bytes.ContainsAny(bytes.TrimSpace(token), "\r\n") {
		return policy, environmentCreateBootstrapBlocker("the original selected OpenBao bootstrap credential cannot be read")
	}
	entries := []struct{ suffix, text string }{{"controller", policy.ControllerPolicy}, {"signer", policy.SignerPolicy}, {"recovery", policy.RecoveryPolicy}}
	if policy.ServiceClientSignerPolicy != "" {
		entries = append(entries, struct{ suffix, text string }{"service-client", policy.ServiceClientSignerPolicy})
	}
	for _, entry := range entries {
		name := "create-" + id + "-" + entry.suffix
		intent := deploymentplan.Object{"environment": cfg.Environment, "issuer_id": id, "mount": policy.Mount, "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(entry.text))}
		body, _ := json.Marshal(intent)
		reuse := false
		closeRecorded := false
		if runtime.ProductIdentityScope != "" {
			for _, saved := range runtime.Execution.Journal.Entries {
				if saved.Key != "openbao-policy-"+name {
					continue
				}
				var receipt deploymentplan.Object
				if reuse || saved.State != "completed" && saved.State != "allocated" || saved.State == "allocated" && !saved.CompletedAt.IsZero() || saved.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(saved.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
					return policy, environmentCreateBootstrapBlocker("product issuer policy has an uncertain or changed original outcome")
				}
				reuse = true
				closeRecorded = saved.State == "allocated"
			}
		}
		if !reuse {
			if err := runtime.Execution.BeginEntry("openbao-policy-"+name, "bootstrap", body, time.Now().UTC()); err != nil {
				return policy, err
			}
		}
		script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy write " + name + " - >/dev/null\nbao policy read " + name + "\n"
		if reuse {
			script = "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read " + name + "\n"
		}
		actual, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(append(bytes.TrimSpace(token), '\n'), []byte(entry.text)...), script)
		if err != nil || strings.TrimSpace(string(actual)) != strings.TrimSpace(entry.text) {
			return policy, environmentCreateBootstrapBlocker("new exact issuer ACL differs from its native registry policy readback")
		}
		if reuse {
			if closeRecorded {
				if err := runtime.Execution.Check(ctx); err != nil {
					return policy, err
				}
				if err := runtime.Execution.CompleteEntry("openbao-policy-"+name, time.Now().UTC()); err != nil {
					return policy, err
				}
			}
			continue
		}
		proof, _ := json.Marshal(intent)
		if err := runtime.Execution.RecordReceipt("openbao-policy-"+name, proof); err != nil {
			return policy, err
		}
		if err := runtime.Execution.CompleteEntry("openbao-policy-"+name, time.Now().UTC()); err != nil {
			return policy, err
		}
	}
	// Each approved issuer has its own policy. The controller role is updated
	// with the cumulative qualified set, so earlier lifecycle ACLs remain bound.
	var controllerPolicies []string
	for _, entry := range runtime.Execution.Journal.Entries {
		if strings.HasPrefix(entry.Key, "openbao-policy-create-") && strings.HasSuffix(entry.Key, "-controller") && entry.State == "completed" {
			controllerPolicies = append(controllerPolicies, strings.TrimPrefix(entry.Key, "openbao-policy-"))
		}
	}
	slices.Sort(controllerPolicies)
	if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, controllerPolicies); err != nil {
		return policy, err
	}
	return policy, nil
}

func configureEnvironmentCreateOpenBaoRole(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, name string, accounts, policies []string) error {
	if runtime.Execution == nil || !secretEnvironmentPattern.MatchString(name) || len(accounts) == 0 || len(policies) == 0 {
		return environmentCreateBootstrapBlocker("exact native selected Kubernetes auth role is required")
	}
	for _, value := range append(slices.Clone(accounts), policies...) {
		if !secretEnvironmentPattern.MatchString(value) {
			return environmentCreateBootstrapBlocker("exact role binding contains malformed identity")
		}
	}
	namespace := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	intent := deploymentplan.Object{"role": name, "namespace": namespace, "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	key := "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12]
	body, _ := json.Marshal(intent)
	reuse := false
	closeRecorded := false
	resumePending := false
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key == key {
			if reuse {
				return environmentCreateBootstrapBlocker("duplicate native role receipt is ambiguous")
			}
			if runtime.ProductIdentityScope != "" && entry.State == "pending" {
				if entry.Phase != "bootstrap" || entry.StartedAt.IsZero() || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 || entry.IntentSHA256 != deploymentplan.Digest(intent) {
					return environmentCreateBootstrapBlocker("product role pending intent changed")
				}
				reuse, closeRecorded, resumePending = true, true, true
				continue
			}
			var receipt map[string]any
			sha, err := environmentCreatePlanSHA256(body)
			if err != nil || entry.State != "completed" && entry.State != "allocated" || entry.State == "completed" && entry.CompletedAt.IsZero() || entry.State == "allocated" && !entry.CompletedAt.IsZero() || entry.IntentSHA256 != sha || json.Unmarshal(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
				return environmentCreateBootstrapBlocker("existing native role intent is incomplete or differs; automatic replay is prohibited")
			}
			reuse = true
			closeRecorded = entry.State == "allocated"
		}
	}
	if !reuse {
		if err := runtime.Execution.BeginEntry(key, "bootstrap", body, time.Now().UTC()); err != nil {
			return err
		}
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original OpenBao auth role owner credential is unavailable")
	}
	if resumePending {
		write, er := reconcileProductSessionRenewalRole(ctx, cfg, runtime, key, intent)
		if er != nil {
			return er
		}
		if write {
			reuse = false
		}
	}
	script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao write auth/kubernetes/role/" + name + " bound_service_account_names=" + strings.Join(accounts, ",") + " bound_service_account_namespaces=" + namespace + " audience=openbao token_policies=" + strings.Join(policies, ",") + " token_ttl=15m token_max_ttl=15m token_no_default_policy=true >/dev/null\nbao read -format=json auth/kubernetes/role/" + name + "\n"
	if reuse {
		script = "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/" + name + "\n"
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), script)
	var observed struct {
		Data struct {
			Accounts   []string `json:"bound_service_account_names"`
			Namespaces []string `json:"bound_service_account_namespaces"`
			Policies   []string `json:"token_policies"`
			Audience   string   `json:"audience"`
			NoDefault  bool     `json:"token_no_default_policy"`
			TTL        int      `json:"token_ttl"`
			MaxTTL     int      `json:"token_max_ttl"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &observed) != nil {
		return environmentCreateBootstrapBlocker("exact new Kubernetes auth role cannot be read back")
	}
	slices.Sort(observed.Data.Accounts)
	slices.Sort(observed.Data.Policies)
	wantAccounts := slices.Clone(accounts)
	wantPolicies := slices.Clone(policies)
	slices.Sort(wantAccounts)
	slices.Sort(wantPolicies)
	if !slices.Equal(observed.Data.Accounts, wantAccounts) || !slices.Equal(observed.Data.Namespaces, []string{namespace}) || !slices.Equal(observed.Data.Policies, wantPolicies) || observed.Data.Audience != "openbao" || !observed.Data.NoDefault || observed.Data.TTL != 900 || observed.Data.MaxTTL != 900 {
		return environmentCreateBootstrapBlocker("actual Kubernetes auth role differs from qualified exclusive binding")
	}
	if reuse {
		if closeRecorded {
			if err := runtime.Execution.Check(ctx); err != nil {
				return err
			}
			if resumePending {
				if err := runtime.Execution.RecordReceipt(key, body); err != nil {
					return err
				}
			}
			return runtime.Execution.CompleteEntry(key, time.Now().UTC())
		}
		return runtime.Execution.Check(ctx)
	}
	proof, _ := json.Marshal(intent)
	if err := runtime.Execution.RecordReceipt(key, proof); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

func executeEnvironmentCreateIntermediateCeremony(ctx context.Context, client *environmentCreatePKIClient, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, root map[string]any, policy environmentCreateIssuerPolicy) (environmentCreateIssuerOperation, map[string]any, error) {
	var operation environmentCreateIssuerOperation
	rootID := deploymentplan.String(root["issuer_id"])
	rootFP := deploymentplan.String(root["certificate_fingerprint_sha256"])
	if root["environment"] != cfg.Environment || root["trust_domain"] != policy.Domain || root["kind"] != "root" || root["status"] != "active" || !environmentPreflightDigest.MatchString(rootFP) || policy.Domain == "device" {
		return operation, nil, environmentCreateBootstrapBlocker("original active offline parent Root and matching configured online intermediate policy required")
	}
	request := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: policy.Domain, Kind: "intermediate", ParentID: rootID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs}
	reservationKey := "create-" + runtime.ExecutionSHA256 + "-" + policy.Domain + "-intermediate"
	if runtime.ProductIdentityScope != "" {
		if !environmentPreflightDigest.MatchString(runtime.ProductIdentityScope) || client.mutationScope != runtime.ProductIdentityScope {
			return operation, nil, environmentCreateBootstrapBlocker("successor ceremony requires its exact qualified product lifecycle scope")
		}
		reservationKey += "-products-" + runtime.ProductIdentityScope
	}
	operation, err := client.ReserveIssuer(ctx, reservationKey, request)
	if err != nil {
		return operation, nil, err
	}
	if err := client.AuthorizeIssuer(ctx, operation); err != nil {
		return operation, nil, err
	}
	issuer, err := client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return operation, nil, err
	}
	if issuer["status"] != "approved" || issuer["parent_issuer_id"] != rootID || issuer["signer_provider"] != "openbao" {
		return operation, nil, environmentCreateBootstrapBlocker("authorized online intermediate does not preserve its exact original parent")
	}
	if _, err := configureEnvironmentCreateIssuerPolicies(ctx, cfg, runtime, issuer); err != nil {
		return operation, nil, err
	}
	issuer, err = client.ProvisionIssuer(ctx, operation, "")
	if err != nil {
		return operation, nil, err
	}
	csrPEM := []byte(deploymentplan.String(issuer["csr_pem"]))
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return operation, nil, environmentCreateBootstrapBlocker("native online intermediate did not produce its original registered CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return operation, nil, environmentCreateBootstrapBlocker("registered online intermediate CSR is invalid")
	}
	csrHash := sha256.Sum256(csr.Raw)
	base := environmentCreateCeremonyDirectory(runtime, policy.Domain)
	issuerPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "intermediate-provisioning.json"), issuer)
	if err != nil {
		return operation, nil, err
	}
	parentPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "active-parent.json"), root)
	if err != nil {
		return operation, nil, err
	}
	signedRelative := filepath.Join(base, "intermediate-signed")
	if err := runEnvironmentCreatePKICeremony(ctx, store, p.Image, "sign", "--issuer", issuerPath, "--parent", parentPath, "--key", "/store/pki/custody/roots/"+rootID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/"+policy.Domain+"-root-v1-key-passphrase", "--expected-request-sha256", operation.Digest, "--expected-parent-sha256", rootFP, "--expected-csr-sha256", hex.EncodeToString(csrHash[:]), "--out", "/store/"+filepath.ToSlash(signedRelative)); err != nil {
		return operation, nil, err
	}
	path, err := privateDeploymentStatePath(cfg.Environment, filepath.Join(signedRelative, "certificate.pem"))
	if err != nil {
		return operation, nil, err
	}
	certificate, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return operation, nil, environmentCreateBootstrapBlocker("native intermediate certificate is unavailable")
	}
	if err := client.ImportIssuer(ctx, operation, string(certificate)); err != nil {
		return operation, nil, err
	}
	issuer, err = client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return operation, nil, err
	}
	if issuer["status"] != "ready" || issuer["certificate_pem"] != string(certificate) || issuer["parent_issuer_id"] != rootID {
		return operation, nil, environmentCreateBootstrapBlocker("ready intermediate differs from original root custody signing and online mount")
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "intermediate-ready.json"), issuer); err != nil {
		return operation, nil, err
	}
	return operation, issuer, nil
}

func executeEnvironmentCreateRootCRL(ctx context.Context, client *environmentCreatePKIClient, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, issuer map[string]any) (string, error) {
	domain := deploymentplan.String(issuer["trust_domain"])
	id := deploymentplan.String(issuer["issuer_id"])
	fp := deploymentplan.String(issuer["certificate_fingerprint_sha256"])
	if issuer["environment"] != cfg.Environment || issuer["kind"] != "root" || issuer["signer_provider"] != "offline" || !environmentBootstrapOwnerUUID.MatchString(id) || !environmentPreflightDigest.MatchString(fp) {
		return "", environmentCreateBootstrapBlocker("initial offline Root CRL requires its exact original authenticated authority")
	}
	type request struct {
		IssuerID    string    `json:"issuer_id"`
		Fingerprint string    `json:"issuer_fingerprint_sha256"`
		Number      string    `json:"crl_number"`
		ThisUpdate  time.Time `json:"this_update"`
		NextUpdate  time.Time `json:"next_update"`
		Entries     []any     `json:"revocations"`
	}
	now := time.Now().UTC()
	manifest := request{id, fp, "1", now.Add(-time.Minute), now.Add(24 * time.Hour), []any{}}
	base := filepath.Join("deployment", "create", runtime.ExecutionSHA256, "bootstrap", domain)
	manifestPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "initial-root-crl-request.json"), manifest)
	if err != nil {
		return "", err
	}
	issuerPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(base, "crl-authority.json"), issuer)
	if err != nil {
		return "", err
	}
	output := filepath.Join(base, "root-crl")
	if err := runEnvironmentCreatePKICeremony(ctx, store, p.Image, "crl", "--issuer", issuerPath, "--crl-request", manifestPath, "--key", "/store/pki/custody/roots/"+id+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/"+domain+"-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(manifest), "--out", "/store/"+filepath.ToSlash(output)); err != nil {
		return "", err
	}
	path, err := privateDeploymentStatePath(cfg.Environment, filepath.Join(output, "revocations.pem"))
	if err != nil {
		return "", err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return "", environmentCreateBootstrapBlocker("native initial signed Root CRL cannot be read")
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 {
		return "", environmentCreateBootstrapBlocker("native Root CRL shape is invalid")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || len(crl.RevokedCertificateEntries) != 0 {
		return "", environmentCreateBootstrapBlocker("initial Root CRL must be a genuine empty first publication")
	}
	var observed map[string]any
	if err := client.call(ctx, http.MethodPost, "/v1/platform/pki/issuers/"+id+"/crl", "", map[string]string{"crl_pem": string(raw)}, &observed); err != nil {
		return "", err
	}
	if observed["issuer_id"] != id || observed["crl_sha256"] != environmentPreflightBytesSHA256(block.Bytes) {
		return "", environmentCreateBootstrapBlocker("registry Root CRL receipt differs from actual signed first publication")
	}
	return filepath.Join(output, "revocations.pem"), nil
}

// Only fixed read queries are used by the ceremony adapter. PostgreSQL also
// enforces read-only transactions for this native inspection process. It never
// inserts acknowledgments, authorizations, approvals or identity receipts.
func readEnvironmentCreateRegistryJSON(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, query string) ([]byte, error) {
	if runtime.Execution == nil || runtime.Backend.Store.Environment != cfg.Environment || !strings.HasPrefix(query, "SELECT ") || strings.ContainsAny(query, ";\r\n") {
		return nil, environmentCreateBootstrapBlocker("exact selected read-only registry inspection is required")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return nil, err
	}
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on", "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d video_cloud -v ON_ERROR_STOP=1 -At -c "$1"`, "registry-read", query)
	if err != nil || len(raw) > 256<<10 {
		return nil, environmentCreateBootstrapBlocker("native read-only selected registry evidence cannot be inspected")
	}
	return bytes.TrimSpace(raw), nil
}

func waitEnvironmentCreateIssuerAcks(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, issuer environmentCreateRegisteredIssuer, consumers []string) error {
	if issuer.Environment != cfg.Environment || !environmentBootstrapOwnerUUID.MatchString(issuer.ID) || !environmentPreflightDigest.MatchString(issuer.BundleVersion) || len(consumers) == 0 {
		return environmentCreateBootstrapBlocker("actual registered issuer and exact required bundle consumers are needed")
	}
	for _, consumer := range consumers {
		if !environmentCreateConsumerID.MatchString(consumer) {
			return environmentCreateBootstrapBlocker("bundle receipt consumer policy is malformed")
		}
	}
	query := "SELECT COALESCE(json_agg(consumer_id ORDER BY consumer_id),'[]'::json) FROM pki_bundle_acknowledgments WHERE issuer_id='" + issuer.ID + "' AND bundle_version='" + issuer.BundleVersion + "'"
	for {
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		if err != nil {
			return err
		}
		var actual []string
		if json.Unmarshal(raw, &actual) != nil {
			return environmentCreateBootstrapBlocker("native bundle receipt inventory is malformed")
		}
		complete := true
		for _, consumer := range consumers {
			if !slices.Contains(actual, consumer) {
				complete = false
				break
			}
		}
		if complete {
			return nil
		}
		select {
		case <-ctx.Done():
			return environmentCreateBootstrapBlocker("deployment execution timeout ended before every required native bundle receipt arrived; preserve the original partial ceremony")
		case <-time.After(5 * time.Second):
		}
	}
}

func activateEnvironmentCreateIssuer(ctx context.Context, client *environmentCreatePKIClient, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, operation environmentCreateIssuerOperation, issuer map[string]any, policy environmentCreateIssuerPolicy) (map[string]any, error) {
	rootFP := deploymentplan.String(issuer["certificate_fingerprint_sha256"])
	if policy.Domain != issuer["trust_domain"] || issuer["environment"] != cfg.Environment || issuer["status"] != "ready" || operation.IssuerID != issuer["issuer_id"] {
		return nil, environmentCreateBootstrapBlocker("original ready issuer and exact activation policy required")
	}
	if issuer["kind"] != "root" {
		chain := []byte(deploymentplan.String(issuer["chain_pem"]))
		var last *x509.Certificate
		for len(bytes.TrimSpace(chain)) > 0 {
			block, rest := pem.Decode(chain)
			if block == nil || block.Type != "CERTIFICATE" {
				return nil, environmentCreateBootstrapBlocker("original ready issuer chain cannot be independently decoded")
			}
			var err error
			last, err = x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
			chain = rest
		}
		if last == nil {
			return nil, environmentCreateBootstrapBlocker("ready issuer chain is empty")
		}
		rootFP = environmentPreflightBytesSHA256(last.Raw)
	}
	registered, err := environmentCreateRegisteredIssuerFromMap(issuer, rootFP)
	if err != nil {
		return nil, err
	}
	if err := waitEnvironmentCreateIssuerAcks(ctx, cfg, runtime, registered, policy.BundleConsumers); err != nil {
		return nil, err
	}
	if err := client.ActivateIssuer(ctx, operation); err != nil {
		return nil, err
	}
	active, err := client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return nil, err
	}
	if active["status"] != "active" || active["certificate_pem"] != issuer["certificate_pem"] || active["chain_pem"] != issuer["chain_pem"] || active["trust_bundle_version"] != issuer["trust_bundle_version"] {
		return nil, environmentCreateBootstrapBlocker("native activation changed the original ready issuer identity")
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(environmentCreateCeremonyDirectory(runtime, policy.Domain), deploymentplan.String(issuer["kind"])+"-active.json"), active); err != nil {
		return nil, err
	}
	return active, nil
}

func environmentCreateCeremonyDirectory(runtime environmentCreateBootstrapRuntime, domain string) string {
	base := filepath.Join("deployment", "create", runtime.ExecutionSHA256, "bootstrap")
	if runtime.ProductIdentityScope != "" {
		base = filepath.Join(base, "product-identities", runtime.ProductIdentityScope)
	}
	return filepath.Join(base, domain)
}
