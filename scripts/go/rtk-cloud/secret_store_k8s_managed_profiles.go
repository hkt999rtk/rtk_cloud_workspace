package main

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FixedDeviceRootTrust is the Controller's non-production activation policy.
// It does not disable the native consumers' signed Device CRL/Root protocols.
func verifyManagedDeviceTrustConsumers(environment, kubeconfig, namespace string, deployments liveDeploymentList, settings map[string]map[string]string, now time.Time, runtimes ...*deploymentCheckRuntime) error {
	c, api, broker := settings["pki-controller/pki-controller"], settings["video-cloud-api-pki/app"], settings["mqtt-pki/pkibroker"]
	if (environment != "dev" && environment != "staging") || c["PKI_ENVIRONMENT"] != environment || c["PKI_FIXED_DEVICE_ROOT_TRUST"] != "true" || c["PKI_DEV_FIXED_DEVICE_ROOT_TRUST"] != "" || !livePKIIssuerIDPattern.MatchString(c["PKI_DEVICE_ROOT_ID"]) || !environmentPreflightDigest.MatchString(c["PKI_DEVICE_ROOT_SHA256"]) {
		return errors.New("managed Device trust requires the matching non-production Controller and original Root pin")
	}
	if api["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"] != "true" || api["VIDEO_CLOUD_AUTH_MTLS_REQUIRED"] != "true" || api["VIDEO_CLOUD_AUTH_DISABLE_ACL"] == "true" || api["VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS"] == "true" || api["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"] != "true" || api["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID"] != c["PKI_DEVICE_ROOT_ID"] || !filepath.IsAbs(api["VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR"]) || !filepath.IsAbs(api["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"]) || api["VIDEO_CLOUD_AUTH_CRL_MANIFEST"] == "" || api["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"] == "" {
		return errors.New("managed Device API requires direct mTLS, registry enforcement, the pinned Root and signed automatic CRLs")
	}
	if broker["PKI_ENVIRONMENT"] != environment || broker["PKI_BROKER_REQUIRE_CRLS"] != "true" || broker["PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED"] != "true" || broker["PKI_BROKER_DEVICE_ROOT_ID"] != c["PKI_DEVICE_ROOT_ID"] || !filepath.IsAbs(broker["PKI_BROKER_DEVICE_AUTOMATIC_STATE_DIR"]) || broker["PKI_BROKER_DEVICE_CRL_MANIFEST"] == "" || broker["PKI_BROKER_APP_PKI_ENABLED"] != "true" || broker["PKI_BROKER_APP_BUNDLE_ACK_ENABLED"] != "true" || broker["PKI_BROKER_APP_CRL_MANIFEST"] == "" {
		return errors.New("managed broker requires Device/App registry enforcement, signed CRLs and native bundle receipts")
	}
	for _, target := range []struct{ name, container, state string }{{"video-cloud-api-pki", "app", api["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"]}, {"mqtt-pki", "pkibroker", broker["PKI_BROKER_DEVICE_ROOT_STATE"]}} {
		found := false
		for _, d := range deployments.Items {
			if d.Metadata.Name != target.name {
				continue
			}
			if found || !livePKICRLStateOnPVC(d, target.container, target.state) {
				return errors.New("managed Device trust state must have one original private PVC owner")
			}
			found = true
		}
		if !found {
			return errors.New("managed Device trust consumer is missing")
		}
		if err := verifyMountedDeviceRoot(kubeconfig, namespace, deployments, target.name, target.container, "/run/pki-device/roots.pem", c["PKI_DEVICE_ROOT_SHA256"], now, runtimes...); err != nil {
			return err
		}
		if err := verifyMountedPKICRLManifestsAt(kubeconfig, namespace, environment, deployments, target.name, now, runtimes...); err != nil {
			return err
		}
	}
	return nil
}

// These are the same optional verified origins accepted by pkimanagement's
// ServerHTTPTrust. A lone old CRL setting is still an invalid partial profile.
func verifyManagedAccountOrigins(d liveDeployment) error {
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name != "pkimanagement" {
			continue
		}
		settings := map[string]string{}
		for _, e := range c.Env {
			if strings.HasPrefix(e.Name, "PKI_MANAGEMENT_") {
				if _, exists := settings[e.Name]; exists {
					return errors.New("Account Manager managed transport settings are duplicated")
				}
				if e.ValueFrom != nil {
					for _, prefix := range []string{"PKI_MANAGEMENT_CONTROLLER", "PKI_MANAGEMENT_ISSUER"} {
						if e.Name == prefix+"_SERVER_CRL_MANIFEST" || e.Name == prefix+"_PKI_CONTROLLER_URL" || e.Name == prefix+"_MANAGEMENT_CA" {
							return fmt.Errorf("%s must be an explicit managed origin setting", e.Name)
						}
					}
					continue
				}
				settings[e.Name] = e.Value
			}
		}
		for _, prefix := range []string{"PKI_MANAGEMENT_CONTROLLER", "PKI_MANAGEMENT_ISSUER"} {
			if settings[prefix+"_SERVER_CRL_MANIFEST"] == "" && settings[prefix+"_PKI_CONTROLLER_URL"] == "" && settings[prefix+"_MANAGEMENT_CA"] == "" {
				continue
			}
			origin, err := url.Parse(settings[prefix+"_URL"])
			management, managementErr := url.Parse(settings[prefix+"_PKI_CONTROLLER_URL"])
			if err != nil || managementErr != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || management.Scheme != "https" || management.Hostname() == "" || management.User != nil || management.RawQuery != "" || management.Fragment != "" || management.Path != "" || settings[prefix+"_SERVER_PKI_NAME"] != origin.Hostname() || !environmentPreflightDigest.MatchString(settings[prefix+"_SERVER_PKI_ROOT_SHA256"]) || !filepath.IsAbs(settings[prefix+"_TLS_CA"]) || !filepath.IsAbs(settings[prefix+"_MANAGEMENT_CA"]) || !filepath.IsAbs(settings[prefix+"_SERVER_CRL_MANIFEST"]) || !livePKICRLStateOnPVC(d, "pkimanagement", settings["PKI_MANAGEMENT_IDENTITY_STATE"]) {
				return fmt.Errorf("%s_SERVER_CRL_MANIFEST requires the complete managed identity and independently pinned HTTPS origin", prefix)
			}
		}
	}
	return nil
}

// Current native listeners publish their registered CA roster in a mounted
// public bundle, rather than the former deployment-only issuer environment
// variable. Read that roster and its exact registered documents; never guess a
// replacement issuer from an unscoped database search or private identity file.
func readMountedServiceRegistryIssuers(kubeconfig, namespace string, d liveDeployment, settings map[string]string, validUntil time.Time, runtimes ...*deploymentCheckRuntime) ([]string, error) {
	name, key, err := mountedPKIConfigMapFile(d, "pki-controller", settings["PKI_SERVICE_CLIENT_BUNDLE_MANIFEST"], "", "Service bundle manifest")
	if err != nil {
		return nil, err
	}
	raw, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", namespace, "get", "configmap", name, "-o", "json")
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &cm) != nil {
		return nil, secretCheckFailure(err, "Controller Service bundle metadata is unavailable")
	}
	var refs []struct {
		ID      string `json:"issuer_id"`
		Version string `json:"trust_bundle_version"`
	}
	manifest := cm.Data[key]
	if len(manifest) == 0 || len(manifest) > 1<<20 || json.Unmarshal([]byte(manifest), &refs) != nil || len(refs) < 2 || len(refs) > 128 {
		return nil, errors.New("Controller Service bundle roster is invalid")
	}
	versions, quoted := map[string]string{}, []string{}
	for _, ref := range refs {
		if !livePKIIssuerIDPattern.MatchString(ref.ID) || !environmentPreflightDigest.MatchString(ref.Version) || versions[ref.ID] != "" {
			return nil, errors.New("Controller Service bundle roster has invalid or duplicate issuer pins")
		}
		versions[ref.ID] = ref.Version
		quoted = append(quoted, "'"+ref.ID+"'::uuid")
	}
	platform := strings.TrimSuffix(namespace, "-video-cloud") + "-platform"
	podsRaw, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", platform, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json")
	var pods livePodList
	if err != nil || json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return nil, secretCheckFailure(err, "Controller Service issuer registry Pod is unavailable")
	}
	query := "SELECT COALESCE(json_agg(document),'[]'::json) FROM public.pki_issuers WHERE id IN (" + strings.Join(quoted, ",") + ")"
	row, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", platform, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query)
	if err != nil {
		return nil, secretCheckFailure(err, "Controller Service issuer registry cannot be read")
	}
	return validateMountedServiceRegistryIssuers(row, versions, strings.TrimSuffix(strings.TrimPrefix(namespace, "video-cloud-"), "-video-cloud"), settings["PKI_SERVICE_CLIENT_ROOT_SHA256"], validUntil)
}

func validateMountedServiceRegistryIssuers(raw []byte, versions map[string]string, environment, rootPin string, now time.Time) ([]string, error) {
	var documents []map[string]any
	if len(raw) == 0 || len(raw) > 4<<20 || json.Unmarshal(raw, &documents) != nil || len(documents) != len(versions) || !environmentPreflightDigest.MatchString(rootPin) {
		return nil, errors.New("Controller Service issuer registry roster is incomplete")
	}
	issuers := map[string]environmentCreateRegisteredIssuer{}
	certs := map[string]*x509.Certificate{}
	rootID := ""
	for _, doc := range documents {
		i, err := environmentCreateRegisteredIssuerFromMap(doc, rootPin)
		chain, certErr := parsePEMCertificates([]byte(i.ChainPEM))
		if err != nil || certErr != nil || len(chain) == 0 || i.Environment != environment || i.Domain != "service" || (i.Status != "active" && i.Status != "retiring") || versions[i.ID] != i.BundleVersion || issuers[i.ID].ID != "" || !chain[0].IsCA || time.Now().UTC().Before(chain[0].NotBefore) || !now.Before(chain[0].NotAfter) {
			return nil, errors.New("Controller Service issuer registry differs from its mounted version, scope or validity")
		}
		certificate, certErr := parsePEMCertificates([]byte(i.Certificate))
		if certErr != nil || len(certificate) != 1 || !certificate[0].Equal(chain[0]) {
			return nil, errors.New("Controller Service issuer certificate differs from its bundle")
		}
		issuers[i.ID], certs[i.ID] = i, chain[0]
		if i.Kind == "root" {
			if rootID != "" || i.RootFingerprint != rootPin || environmentPreflightBytesSHA256(chain[0].Raw) != rootPin || chain[0].CheckSignatureFrom(chain[0]) != nil {
				return nil, errors.New("Controller Service Root differs from its independent pin")
			}
			rootID = i.ID
		}
	}
	if rootID == "" {
		return nil, errors.New("Controller Service bundle has no pinned Root")
	}
	var ids []string
	for id, i := range issuers {
		if id == rootID {
			continue
		}
		if i.Kind != "intermediate" || i.Document["parent_issuer_id"] != rootID || certs[id].CheckSignatureFrom(certs[rootID]) != nil {
			return nil, errors.New("Controller Service intermediate does not descend from the pinned Root")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("Controller Service bundle has no registered intermediate")
	}
	sort.Strings(ids)
	return ids, nil
}
