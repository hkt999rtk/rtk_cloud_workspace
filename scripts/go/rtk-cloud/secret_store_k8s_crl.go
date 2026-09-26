package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os/exec"
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
func verifyMountedPKICRLManifests(kubeconfig, namespace, environment string, deployments liveDeploymentList, target string) error {
	specs := map[string][]struct{ container, setting, domain string }{
		"account-manager": {{"pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", "service"}},
		"certissuer": {
			{"certissuer", "CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST", "service"},
			{"certissuer", "OPENBAO_SERVER_CRL_MANIFEST", "openbao_tls"},
		},
	}
	// Dev already uses all three reviewed CRL consumers. A later Deployment
	// replacement must not silently remove one and make secrets verify pass.
	if environment == "dev" {
		foundDeployment := false
		for _, deployment := range deployments.Items {
			if deployment.Metadata.Name != target {
				continue
			}
			foundDeployment = true
			for _, spec := range specs[target] {
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
					out, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "configmap", configMap, "-o", "json").Output()
					if err != nil {
						return fmt.Errorf("%s ConfigMap is unavailable", spec.setting)
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
					for _, entry := range entries {
						if err := verifyLivePKICRLEvidence(kubeconfig, namespace, environment, target, spec.container, entry); err != nil {
							return fmt.Errorf("%s issuer %s: %w", spec.setting, entry.Issuer.ID, err)
						}
					}
				}
			}
		}
	}
	return nil
}

func mountedPKICRLConfigMap(deployment liveDeployment, containerName, path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "crls.json" {
		return "", "", fmt.Errorf("CRL manifest path must be an absolute crls.json file")
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if !strings.HasPrefix(path, mount.MountPath+"/") || strings.TrimPrefix(path, mount.MountPath+"/") != "crls.json" {
				continue
			}
			if !mount.ReadOnly {
				return "", "", fmt.Errorf("CRL manifest mount is not read-only")
			}
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == mount.Name && volume.ConfigMap.Name != "" {
					return volume.ConfigMap.Name, "crls.json", nil
				}
			}
			return "", "", fmt.Errorf("CRL manifest mount is not backed by a ConfigMap")
		}
	}
	return "", "", fmt.Errorf("CRL manifest has no matching read-only ConfigMap mount")
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
func verifyLivePKICRLEvidence(kubeconfig, namespace, environment, target, container string, entry livePKICRLManifestEntry) error {
	stateRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", "deployment/"+target, "-c", container, "--", "cat", entry.StatePath).Output()
	if err != nil || len(stateRaw) == 0 || len(stateRaw) > 1<<20 {
		return fmt.Errorf("current CRL PVC state is unreadable")
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
	digest := sha256.Sum256(crl.Raw)
	if state.CRL.Digest != hex.EncodeToString(digest[:]) || state.CRL.Number != crl.Number.String() || !state.CRL.ThisUpdate.Equal(crl.ThisUpdate) || !state.CRL.NextUpdate.Equal(crl.NextUpdate) {
		return fmt.Errorf("installed CRL metadata differs from signed content")
	}
	if !livePKIIssuerIDPattern.MatchString(entry.Issuer.ID) {
		return fmt.Errorf("CRL issuer ID is invalid")
	}
	platformNamespace := "video-cloud-" + environment + "-platform"
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", platformNamespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json").Output()
	var pods livePodList
	if err != nil || json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return fmt.Errorf("cannot read PostgreSQL Pod for CRL receipt verification")
	}
	consumer := target
	query := fmt.Sprintf(`SELECT json_build_object('digest',digest,'number',number::text,'ack',EXISTS(SELECT 1 FROM pki_crl_acknowledgments a WHERE a.issuer_id=c.issuer_id AND a.digest=c.digest AND a.consumer_id='%s')) FROM pki_crls c WHERE c.issuer_id='%s'::uuid ORDER BY c.number DESC LIMIT 1`, consumer, entry.Issuer.ID)
	row, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", platformNamespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query).Output()
	var latest struct {
		Digest string `json:"digest"`
		Number string `json:"number"`
		Ack    bool   `json:"ack"`
	}
	if err != nil || json.Unmarshal(bytes.TrimSpace(row), &latest) != nil || latest.Digest != state.CRL.Digest || latest.Number != state.CRL.Number || !latest.Ack {
		return fmt.Errorf("installed CRL is not the latest acknowledged registry record")
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
