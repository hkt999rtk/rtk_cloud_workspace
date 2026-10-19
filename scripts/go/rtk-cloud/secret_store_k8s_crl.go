package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type livePKICRLManifestEntry struct {
	Issuer struct {
		ID          string `json:"issuer_id"`
		Environment string `json:"environment"`
		Domain      string `json:"trust_domain"`
		Fingerprint string `json:"certificate_fingerprint_sha256"`
		Certificate string `json:"certificate_pem"`
		Status      string `json:"status"`
	} `json:"issuer"`
	StatePath string `json:"state_path"`
}

// The supported CRL consumers are deliberately enumerated. Other root/CRL
// settings still need their own deployment contract before secrets verify can
// accept them.
func verifyMountedPKICRLManifests(kubeconfig, namespace, environment string, deployments liveDeploymentList, target string, runtimes ...*deploymentCheckRuntime) error {
	return verifyMountedPKICRLManifestsAt(kubeconfig, namespace, environment, deployments, target, time.Now().UTC(), runtimes...)
}

func logIngesterCRLRuntimeRequired(deployments liveDeploymentList) bool {
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, setting := range container.Env {
				if deployment.Metadata.Name == "video-cloud-logingester" && container.Name == "app" {
					switch setting.Name {
					case "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST":
						// An empty or partial installed profile is still required;
						// deleting one manifest cannot turn acceptance off.
						return true
					}
				}
				if deployment.Metadata.Name == "pki-controller" && (setting.Name == "PKI_REQUIRED_CONSUMERS_SERVICE" || setting.Name == "PKI_REQUIRED_CONSUMERS_MQTT") {
					for _, consumer := range strings.Split(setting.Value, ",") {
						if strings.TrimSpace(consumer) == "video-cloud-logingester" {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func verifyMountedPKICRLManifestsAt(kubeconfig, namespace, environment string, deployments liveDeploymentList, target string, validUntil time.Time, runtimes ...*deploymentCheckRuntime) error {
	if target == "video-cloud-logingester" {
		for _, runtime := range runtimes {
			if runtime != nil && runtime.preflightPlan != nil {
				// Candidate qualification validates the original public inputs.
				// The updated consumer must install and acknowledge its durable
				// CRL cache before post-deploy acceptance, not before rollout.
				return nil
			}
		}
		if !logIngesterCRLRuntimeRequired(deployments) {
			return nil // Unmanaged workloads receive no new identity requirement.
		}
	}
	specs := map[string][]struct{ container, setting, domain string }{
		"account-manager": {{"pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", "service"}, {"pkimanagement", "PKI_MANAGEMENT_CONTROLLER_SERVER_CRL_MANIFEST", "service"}, {"pkimanagement", "PKI_MANAGEMENT_ISSUER_SERVER_CRL_MANIFEST", "service"}},
		"certissuer": {
			{"certissuer", "CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST", "service"},
			{"certissuer", "OPENBAO_SERVER_CRL_MANIFEST", "openbao_tls"},
		},
		"video-cloud-logingester": {
			{"app", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "mqtt"},
			{"app", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST", "service"},
		},
		"video-cloud-api-pki": {{"app", "VIDEO_CLOUD_AUTH_CRL_MANIFEST", "device"}},
		"mqtt-pki":            {{"pkibroker", "PKI_BROKER_DEVICE_CRL_MANIFEST", "device"}, {"pkibroker", "PKI_BROKER_APP_CRL_MANIFEST", "app"}},
	}
	// Dev already uses the reviewed original consumers. An installed or required
	// managed LogIngester profile must remain complete in every environment.
	if environment == "dev" || target == "video-cloud-logingester" {
		foundDeployment := false
		for _, deployment := range deployments.Items {
			if deployment.Metadata.Name != target {
				continue
			}
			foundDeployment = true
			for _, spec := range specs[target] {
				// Outbound managed origins are optional on the historical listener
				// profile; a selected origin is validated as a complete transport.
				if target == "account-manager" && spec.setting != "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST" {
					continue
				}
				foundContainer, settingCount := false, 0
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name != spec.container {
						continue
					}
					foundContainer = true
					for _, setting := range container.Env {
						if setting.Name != spec.setting {
							continue
						}
						settingCount++
						if setting.Value == "" && setting.ValueFrom == nil {
							return fmt.Errorf("%s CRL manifest path is missing", spec.setting)
						}
					}
				}
				if !foundContainer {
					return fmt.Errorf("%s CRL consumer container is missing", spec.setting)
				}
				if settingCount != 1 {
					return fmt.Errorf("%s requires exactly one CRL manifest setting", spec.setting)
				}
			}
		}
		if !foundDeployment {
			return fmt.Errorf("%s CRL consumer deployment is missing", target)
		}
	}
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name != target {
			continue
		}
		for _, spec := range specs[target] {
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name != spec.container {
					continue
				}
				for _, setting := range container.Env {
					if setting.Name != spec.setting || (setting.Value == "" && setting.ValueFrom == nil) {
						continue
					}
					if setting.ValueFrom != nil {
						return fmt.Errorf("%s CRL manifest path must be explicit", spec.setting)
					}
					configMap, key, err := mountedPKICRLConfigMap(deployment, spec.container, setting.Value)
					if err != nil {
						return fmt.Errorf("%s: %w", spec.setting, err)
					}
					out, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", namespace, "get", "configmap", configMap, "-o", "json")
					if err != nil {
						return secretCheckFailure(err, fmt.Sprintf("%s ConfigMap is unavailable", spec.setting))
					}
					var source struct {
						Data map[string]string `json:"data"`
					}
					if json.Unmarshal(out, &source) != nil {
						return fmt.Errorf("%s ConfigMap metadata is invalid", spec.setting)
					}
					if err := validateMountedPKICRLManifest(source.Data[key], environment, spec.domain, deployment, spec.container); err != nil {
						return fmt.Errorf("%s: %w", spec.setting, err)
					}
					var entries []livePKICRLManifestEntry
					if err := json.Unmarshal([]byte(source.Data[key]), &entries); err != nil {
						return fmt.Errorf("%s manifest cannot be read", spec.setting)
					}
					if spec.domain == "service" {
						bundleSetting := strings.TrimSuffix(spec.setting, "_SERVER_CRL_MANIFEST") + "_BUNDLE_MANIFEST"
						if err := verifyMountedServiceCRLCoverage(kubeconfig, namespace, deployment, spec.container, bundleSetting, entries, runtimes...); err != nil {
							return fmt.Errorf("%s: %w", spec.setting, err)
						}
					}
					for _, entry := range entries {
						if err := verifyLivePKICRLEvidence(kubeconfig, namespace, environment, target, spec.container, entry, validUntil, runtimes...); err != nil {
							return fmt.Errorf("%s issuer %s: %w", spec.setting, entry.Issuer.ID, err)
						}
					}
				}
			}
		}
	}
	if target == "certissuer" && logIngesterCRLRuntimeRequired(deployments) {
		// This existing entry point receives the complete Video Cloud inventory.
		// Check an enabled LogIngester after the existing consumers; no new
		// workload or credential is created to obtain runtime evidence.
		for _, deployment := range deployments.Items {
			if deployment.Metadata.Name == "video-cloud-logingester" && deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
				return nil
			}
		}
		return verifyMountedPKICRLManifestsAt(kubeconfig, namespace, environment, deployments, "video-cloud-logingester", validUntil, runtimes...)
	}
	return nil
}

func mountedPKICRLConfigMap(deployment liveDeployment, containerName, path string) (string, string, error) {
	return mountedPKIConfigMapFile(deployment, containerName, path, "", "CRL manifest")
}

func mountedPKIConfigMapFile(deployment liveDeployment, containerName, path, filename, label string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || (filename != "" && filepath.Base(path) != filename) || (filename == "" && !strings.HasSuffix(filepath.Base(path), ".json")) {
		return "", "", fmt.Errorf("%s path must be an absolute JSON manifest file", label)
	}
	var sourceName, sourceKey string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if mount.SubPathExpr != "" {
				continue
			}
			relative := strings.TrimPrefix(path, mount.MountPath+"/")
			if mount.SubPath != "" {
				if path != mount.MountPath || filepath.Clean(mount.SubPath) != mount.SubPath || filepath.IsAbs(mount.SubPath) {
					continue
				}
				relative = mount.SubPath
			} else if !strings.HasPrefix(path, mount.MountPath+"/") || strings.HasPrefix(relative, "../") {
				continue
			}
			if !mount.ReadOnly {
				return "", "", fmt.Errorf("%s mount is not read-only", label)
			}
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == mount.Name && volume.ConfigMap.Name != "" {
					key := relative
					if len(volume.ConfigMap.Items) > 0 {
						key = ""
						for _, item := range volume.ConfigMap.Items {
							if item.Path == relative {
								if key != "" {
									return "", "", fmt.Errorf("%s ConfigMap key is ambiguous", label)
								}
								key = item.Key
							}
						}
					}
					if key == "" || strings.ContainsAny(key, "/\\") {
						return "", "", fmt.Errorf("%s ConfigMap key is unavailable", label)
					}
					if sourceName != "" {
						return "", "", fmt.Errorf("%s ConfigMap mount is ambiguous", label)
					}
					sourceName, sourceKey = volume.ConfigMap.Name, key
				}
			}
			if sourceName == "" {
				return "", "", fmt.Errorf("%s mount is not backed by a ConfigMap", label)
			}
		}
	}
	if sourceName != "" {
		return sourceName, sourceKey, nil
	}
	return "", "", fmt.Errorf("%s has no matching read-only ConfigMap mount", label)
}

// Adopting a successor bundle without its CRL leaves newly renewed Service
// clients unable to authenticate even when every older CRL is current.
func verifyMountedServiceCRLCoverage(kubeconfig, namespace string, deployment liveDeployment, containerName, settingName string, entries []livePKICRLManifestEntry, runtimes ...*deploymentCheckRuntime) error {
	count, path, indirect := 0, "", false
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			for _, setting := range container.Env {
				if setting.Name == settingName {
					count++
					path, indirect = setting.Value, setting.ValueFrom != nil
				}
			}
		}
	}
	if count == 0 {
		return nil // Legacy listeners without bundle adoption retain their checks.
	}
	if count != 1 || indirect || path == "" {
		return fmt.Errorf("%s requires exactly one explicit bundle manifest path", settingName)
	}
	name, key, err := mountedPKIConfigMapFile(deployment, containerName, path, "", "Service bundle manifest")
	if err != nil {
		return err
	}
	raw, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", namespace, "get", "configmap", name, "-o", "json")
	if err != nil {
		return secretCheckFailure(err, "Service bundle ConfigMap is unavailable")
	}
	var source struct {
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return fmt.Errorf("Service bundle ConfigMap metadata is invalid")
	}
	return validateServiceCRLCoverage(source.Data[key], entries)
}

func validateServiceCRLCoverage(raw string, entries []livePKICRLManifestEntry) error {
	var bundles []struct {
		ID string `json:"issuer_id"`
	}
	if len(raw) == 0 || len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &bundles) != nil || len(bundles) == 0 || len(bundles) > 128 {
		return fmt.Errorf("Service bundle manifest is invalid")
	}
	covered, seen := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		covered[entry.Issuer.ID] = true
	}
	for _, bundle := range bundles {
		if !livePKIIssuerIDPattern.MatchString(bundle.ID) || seen[bundle.ID] {
			return fmt.Errorf("Service bundle manifest has an invalid or duplicate issuer")
		}
		if !covered[bundle.ID] {
			return fmt.Errorf("Service bundle issuer %s has no CRL manifest entry", bundle.ID)
		}
		seen[bundle.ID] = true
	}
	return nil
}

func validateMountedPKICRLManifest(raw, environment, domain string, deployment liveDeployment, containerName string) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return fmt.Errorf("CRL manifest is missing or oversized")
	}
	var entries []livePKICRLManifestEntry
	if json.Unmarshal([]byte(raw), &entries) != nil || len(entries) == 0 || len(entries) > 128 {
		return fmt.Errorf("CRL manifest is invalid")
	}
	seenIDs, seenPaths := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		issuer := entry.Issuer
		if !livePKIIssuerIDPattern.MatchString(issuer.ID) || issuer.Environment != environment || issuer.Domain != domain || (issuer.Status != "active" && issuer.Status != "retiring") || seenIDs[issuer.ID] || seenPaths[entry.StatePath] || !livePKICRLStateOnPVC(deployment, containerName, entry.StatePath) {
			return fmt.Errorf("CRL manifest has an invalid issuer scope or state path")
		}
		block, _ := pem.Decode([]byte(issuer.Certificate))
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("CRL manifest issuer certificate is invalid")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		fingerprint := sha256.Sum256(block.Bytes)
		if err != nil || !certificate.IsCA || issuer.Fingerprint != hex.EncodeToString(fingerprint[:]) {
			return fmt.Errorf("CRL manifest issuer certificate fingerprint differs")
		}
		seenIDs[issuer.ID], seenPaths[entry.StatePath] = true, true
	}
	return nil
}

var livePKIIssuerIDPattern = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// The public PVC state is checked against the registry's latest signed CRL
// and the exact workload receipt. A valid ConfigMap alone is not acceptance.
func verifyLivePKICRLEvidence(kubeconfig, namespace, environment, target, container string, entry livePKICRLManifestEntry, validUntil time.Time, runtimes ...*deploymentCheckRuntime) error {
	stateRaw, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", namespace, "exec", "deployment/"+target, "-c", container, "--", "cat", entry.StatePath)
	if err != nil || len(stateRaw) == 0 || len(stateRaw) > 1<<20 {
		return secretCheckFailure(err, "current CRL PVC state is unreadable")
	}
	var state struct {
		IssuerFingerprint string `json:"issuer_fingerprint"`
		TerminalIssuerID  string `json:"terminal_issuer_id"`
		CRL               struct {
			IssuerID   string    `json:"issuer_id"`
			Digest     string    `json:"crl_sha256"`
			Number     string    `json:"crl_number"`
			PEM        string    `json:"crl_pem"`
			ThisUpdate time.Time `json:"this_update"`
			NextUpdate time.Time `json:"next_update"`
		} `json:"crl"`
	}
	if json.Unmarshal(stateRaw, &state) != nil || state.TerminalIssuerID != "" || state.IssuerFingerprint != entry.Issuer.Fingerprint || state.CRL.IssuerID != entry.Issuer.ID {
		return fmt.Errorf("current CRL PVC state differs from the pinned issuer")
	}
	certBlock, _ := pem.Decode([]byte(entry.Issuer.Certificate))
	if certBlock == nil {
		return fmt.Errorf("CRL signing certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("CRL signing certificate is invalid")
	}
	crlBlock, rest := pem.Decode([]byte(state.CRL.PEM))
	if crlBlock == nil || crlBlock.Type != "X509 CRL" || len(crlBlock.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("installed CRL PEM is invalid")
	}
	crl, err := x509.ParseRevocationList(crlBlock.Bytes)
	now := time.Now().UTC()
	if err != nil || crl.Number == nil || crl.Number.Sign() <= 0 || crl.CheckSignatureFrom(certificate) != nil || !bytes.Equal(crl.RawIssuer, certificate.RawSubject) || len(certificate.SubjectKeyId) == 0 || !bytes.Equal(crl.AuthorityKeyId, certificate.SubjectKeyId) || crl.ThisUpdate.After(now) || crl.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || !crl.NextUpdate.After(now) || crl.NextUpdate.After(crl.ThisUpdate.Add(7*24*time.Hour)) || crl.NextUpdate.After(certificate.NotAfter) {
		return fmt.Errorf("installed CRL signature or freshness is invalid")
	}
	// Future rollout qualification extends expiration only. Publication time,
	// freshness, registry version and acknowledgments still describe actual now.
	if !crl.NextUpdate.After(validUntil) {
		return fmt.Errorf("installed CRL expires within the checked validity window")
	}
	digest := sha256.Sum256(crl.Raw)
	if state.CRL.Digest != hex.EncodeToString(digest[:]) || state.CRL.Number != crl.Number.String() || !state.CRL.ThisUpdate.Equal(crl.ThisUpdate) || !state.CRL.NextUpdate.Equal(crl.NextUpdate) {
		return fmt.Errorf("installed CRL metadata differs from signed content")
	}
	if !livePKIIssuerIDPattern.MatchString(entry.Issuer.ID) {
		return fmt.Errorf("CRL issuer ID is invalid")
	}
	platformNamespace := "video-cloud-" + environment + "-platform"
	podsRaw, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", platformNamespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json")
	var pods livePodList
	if err != nil || json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return secretCheckFailure(err, "cannot read PostgreSQL Pod for CRL receipt verification")
	}
	consumer := target
	// Receipt membership follows the native enrolled Service subject, not the
	// Kubernetes owner name. Reuse the producer's exact owner/container roster.
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Name == target && owner.Container == container {
			consumer = owner.Role
		}
	}
	query := fmt.Sprintf(`SELECT json_build_object('digest',digest,'number',number::text,'ack',EXISTS(SELECT 1 FROM pki_crl_acknowledgments a WHERE a.issuer_id=c.issuer_id AND a.digest=c.digest AND a.consumer_id='%s')) FROM pki_crls c WHERE c.issuer_id='%s'::uuid ORDER BY c.number DESC LIMIT 1`, consumer, entry.Issuer.ID)
	row, err := secretCheckKubectl(runtimes, false, "--kubeconfig", kubeconfig, "-n", platformNamespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query)
	var latest struct {
		Digest string `json:"digest"`
		Number string `json:"number"`
		Ack    bool   `json:"ack"`
	}
	if err != nil || json.Unmarshal(bytes.TrimSpace(row), &latest) != nil || latest.Digest != state.CRL.Digest || latest.Number != state.CRL.Number || !latest.Ack {
		return secretCheckFailure(err, "installed CRL is not the latest acknowledged registry record")
	}
	return nil
}

func livePKICRLStateOnPVC(deployment liveDeployment, containerName, path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if !strings.HasPrefix(path, mount.MountPath+"/") || mount.ReadOnly {
				continue
			}
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == mount.Name && volume.PersistentVolumeClaim != nil {
					return true
				}
			}
		}
	}
	return false
}
