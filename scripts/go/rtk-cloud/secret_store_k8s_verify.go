package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type liveSecretList struct {
	Items []struct {
		Metadata struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	} `json:"items"`
}

type liveDeployment struct {
	Metadata struct {
		Name       string `json:"name"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Env []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
					VolumeMounts []struct {
						Name      string `json:"name"`
						MountPath string `json:"mountPath"`
					} `json:"volumeMounts"`
				} `json:"containers"`
				Volumes []struct {
					Name   string `json:"name"`
					Secret struct {
						SecretName string `json:"secretName"`
					} `json:"secret"`
					PersistentVolumeClaim any `json:"persistentVolumeClaim"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

type liveDeploymentList struct {
	Items []liveDeployment `json:"items"`
}

type livePodList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"items"`
}

type liveDeploymentBootstrapSession struct {
	Caller       string    `json:"caller"`
	DeploymentID string    `json:"deployment_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type liveDeploymentBootstrapReport struct {
	Sessions               []liveDeploymentBootstrapSession `json:"sessions"`
	ActiveCallerIndex      bool                             `json:"active_caller_index"`
	LegacyCallerConstraint bool                             `json:"legacy_caller_constraint"`
}

// verifySecretStoreK8SRuntime is a read-only live check. It validates every
// certificate/key pair stored in a Secret owned by the selected stack and makes
// PKI-backed workload readiness part of secret verification. The latter catches
// identities held on PVCs whose authorization can only be proven by the running
// service. It deliberately has no CRL or OCSP dependency.
func verifySecretStoreK8SRuntime(store secretStore, now time.Time) error {
	kubeconfig := store.KubeconfigPath()
	if _, err := os.Stat(kubeconfig); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	stack := "video-cloud-" + store.Environment
	secretRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "get", "secrets", "--all-namespaces", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read live Kubernetes Secret metadata")
	}
	var secrets liveSecretList
	if json.Unmarshal(secretRaw, &secrets) != nil {
		return errors.New("live Kubernetes Secret metadata is invalid")
	}
	var failures []string
	for _, secret := range secrets.Items {
		if secret.Metadata.Namespace != stack && !strings.HasPrefix(secret.Metadata.Namespace, stack+"-") {
			continue
		}
		label := secret.Metadata.Namespace + "/" + secret.Metadata.Name
		for _, pair := range [][2]string{{"tls.crt", "tls.key"}, {"client.crt", "client.key"}, {"cert.pem", "key.pem"}} {
			certEncoded, certOK := secret.Data[pair[0]]
			keyEncoded, keyOK := secret.Data[pair[1]]
			if !certOK && !keyOK {
				continue
			}
			if !certOK || !keyOK {
				failures = append(failures, fmt.Sprintf("%s has incomplete %s/%s pair", label, pair[0], pair[1]))
				continue
			}
			certPEM, certErr := base64.StdEncoding.DecodeString(certEncoded)
			keyPEM, keyErr := base64.StdEncoding.DecodeString(keyEncoded)
			identity, pairErr := tls.X509KeyPair(certPEM, keyPEM)
			if certErr != nil || keyErr != nil || pairErr != nil || len(identity.Certificate) == 0 {
				failures = append(failures, fmt.Sprintf("%s has an invalid or mismatched %s/%s pair", label, pair[0], pair[1]))
				continue
			}
			leaf, parseErr := x509.ParseCertificate(identity.Certificate[0])
			if parseErr != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
				failures = append(failures, fmt.Sprintf("%s %s is not currently valid", label, pair[0]))
			}
		}
	}

	namespace := stack + "-video-cloud"
	deploymentRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployments", "-o", "json").Output()
	if err != nil {
		failures = append(failures, "cannot read live Video Cloud deployment status")
	} else {
		var deployments liveDeploymentList
		if json.Unmarshal(deploymentRaw, &deployments) != nil {
			failures = append(failures, "live Video Cloud deployment metadata is invalid")
		} else {
			if err := verifyCertIssuerBootstrapConfiguration(kubeconfig, namespace, deployments); err != nil {
				failures = append(failures, err.Error())
			}
			if err := verifyCertIssuerStaticServingChain(namespace, secrets, deployments, now); err != nil {
				failures = append(failures, err.Error())
			}
			for _, deployment := range deployments.Items {
				if !deploymentUsesPKI(deployment) {
					continue
				}
				desired := int32(1)
				if deployment.Spec.Replicas != nil {
					desired = *deployment.Spec.Replicas
				}
				if desired == 0 {
					continue
				}
				if deployment.Status.ObservedGeneration < deployment.Metadata.Generation || deployment.Status.UpdatedReplicas != desired || deployment.Status.AvailableReplicas != desired || deployment.Status.UnavailableReplicas != 0 {
					failures = append(failures, fmt.Sprintf("%s/%s PKI-backed identity is not proven by an available current workload", namespace, deployment.Metadata.Name))
				}
			}
			if err := verifyLiveServiceClientRegistry(kubeconfig, namespace, deployments); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	if err := verifyLiveDeploymentBootstrapSessions(kubeconfig, stack+"-platform", now); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return fmt.Errorf("live Kubernetes secret validation failed: %s", strings.Join(failures, "; "))
}

func verifyCertIssuerBootstrapConfiguration(kubeconfig, namespace string, deployments liveDeploymentList) error {
	var certissuer *liveDeployment
	for i := range deployments.Items {
		if deployments.Items[i].Metadata.Name == "certissuer" {
			certissuer = &deployments.Items[i]
			break
		}
	}
	if certissuer == nil {
		return nil
	}
	settings := map[string]string{}
	for _, container := range certissuer.Spec.Template.Spec.Containers {
		for _, item := range container.Env {
			settings[item.Name] = item.Value
		}
	}
	unsupported := []string{
		"CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST",
		"OPENBAO_SERVER_CRL_MANIFEST",
		"CERT_ISSUER_SERVICE_CLIENT_SERVICE_ROOT_STATE",
		"CERT_ISSUER_SERVICE_CLIENT_SERVICE_ROOTS",
		"CERT_ISSUER_SERVICE_CLIENT_SERVICE_ROOT_ID",
		"OPENBAO_SERVER_ROOT_STATE",
		"OPENBAO_SERVER_ROOTS",
		"OPENBAO_SERVER_ROOT_ID",
	}
	var enabled []string
	for _, name := range unsupported {
		if strings.TrimSpace(settings[name]) != "" {
			enabled = append(enabled, name)
		}
	}
	if len(enabled) > 0 {
		return fmt.Errorf("certissuer enables CRL/root-consumer settings before that deployment contract exists: %s", strings.Join(enabled, ","))
	}
	caller := strings.TrimSpace(settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER"])
	if caller == "" {
		return nil
	}
	subject := strings.TrimSpace(settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECT"])
	if subject != caller || settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA"] == "" || settings["CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT"] == "" || settings["CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY"] == "" || settings["PKI_BOOTSTRAP_SESSION_ID"] == "" {
		return errors.New("certissuer bootstrap caller requires matching subject, CA, client cert/key and session id")
	}
	if pattern := strings.TrimSpace(settings["CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN"]); pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return errors.New("certissuer service-client provisioner pattern is invalid")
		}
	}
	policyRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "networkpolicies", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read NetworkPolicy metadata for certissuer bootstrap verification")
	}
	var policies struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Ingress []struct {
					From []struct {
						PodSelector struct {
							MatchLabels map[string]string `json:"matchLabels"`
						} `json:"podSelector"`
					} `json:"from"`
				} `json:"ingress"`
			} `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(policyRaw, &policies) != nil {
		return errors.New("NetworkPolicy metadata for certissuer bootstrap is invalid")
	}
	for _, policy := range policies.Items {
		for _, ingress := range policy.Spec.Ingress {
			for _, from := range ingress.From {
				if from.PodSelector.MatchLabels["rtk.realtek.com/pki-bootstrap"] == "true" {
					return nil
				}
			}
		}
	}
	return errors.New("NetworkPolicy does not admit the labeled certissuer bootstrap Job")
}

// verifyCertIssuerStaticServingChain catches a bootstrap dead end before a
// rollout starts. A CertIssuer that has not switched to managed host identity
// must ship the issuer chain for its static TLS leaf in the mounted Secret.
func verifyCertIssuerStaticServingChain(namespace string, secrets liveSecretList, deployments liveDeploymentList, now time.Time) error {
	var certissuer *liveDeployment
	for i := range deployments.Items {
		if deployments.Items[i].Metadata.Name == "certissuer" {
			certissuer = &deployments.Items[i]
			break
		}
	}
	if certissuer == nil {
		return nil
	}
	settings := map[string]string{}
	var mounts map[string]string
	for _, container := range certissuer.Spec.Template.Spec.Containers {
		for _, item := range container.Env {
			settings[item.Name] = strings.TrimSpace(item.Value)
		}
		if settings["CERT_ISSUER_SERVER_CERT"] != "" {
			mounts = map[string]string{}
			for _, mount := range container.VolumeMounts {
				mounts[mount.Name] = filepath.Clean(mount.MountPath)
			}
		}
	}
	if settings["CERT_ISSUER_HOST_IDENTITY_STATE"] != "" {
		return nil
	}
	certPath := filepath.Clean(settings["CERT_ISSUER_SERVER_CERT"])
	if certPath == "." || len(mounts) == 0 {
		return nil
	}
	volumeName, mountPath := "", ""
	for name, candidate := range mounts {
		if (certPath == candidate || strings.HasPrefix(certPath, candidate+string(os.PathSeparator))) && len(candidate) > len(mountPath) {
			volumeName, mountPath = name, candidate
		}
	}
	if volumeName == "" {
		return fmt.Errorf("certissuer static serving certificate %s is not backed by a mounted Secret", certPath)
	}
	secretName := ""
	for _, volume := range certissuer.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName {
			secretName = volume.Secret.SecretName
			break
		}
	}
	if secretName == "" {
		return fmt.Errorf("certissuer static serving certificate %s is not backed by a mounted Secret", certPath)
	}
	var data map[string]string
	for _, secret := range secrets.Items {
		if secret.Metadata.Namespace == namespace && secret.Metadata.Name == secretName {
			data = secret.Data
			break
		}
	}
	key := strings.TrimPrefix(strings.TrimPrefix(certPath, mountPath), string(os.PathSeparator))
	leafPEM, err := base64.StdEncoding.DecodeString(data[key])
	if err != nil || len(leafPEM) == 0 {
		return fmt.Errorf("certissuer static serving certificate %s/%s is missing or invalid", secretName, key)
	}
	chain, err := parsePEMCertificates(leafPEM)
	if err != nil || len(chain) == 0 {
		return fmt.Errorf("certissuer static serving certificate %s/%s is missing or invalid", secretName, key)
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	addIssuer := func(cert *x509.Certificate) {
		if cert == nil || !cert.IsCA || cert.Equal(chain[0]) {
			return
		}
		if cert.CheckSignatureFrom(cert) == nil {
			roots.AddCert(cert)
		} else {
			intermediates.AddCert(cert)
		}
	}
	for _, cert := range chain[1:] {
		addIssuer(cert)
	}
	for dataKey, encoded := range data {
		if dataKey == key {
			continue
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			continue
		}
		certificates, parseErr := parsePEMCertificates(raw)
		if parseErr != nil {
			continue
		}
		for _, cert := range certificates {
			addIssuer(cert)
		}
	}
	if _, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("certissuer static serving certificate %s/%s has no usable issuer chain in its mounted Secret", secretName, key)
	}
	return nil
}

func parsePEMCertificates(raw []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	for len(raw) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, certificate)
	}
	if len(certificates) == 0 {
		return nil, errors.New("no certificates")
	}
	return certificates, nil
}

// verifyLiveDeploymentBootstrapSessions prevents a second signing attempt from
// starting while an earlier deployment bootstrap still owns its caller. This
// is intentionally read-only and reports only non-secret session metadata.
func verifyLiveDeploymentBootstrapSessions(kubeconfig, namespace string, now time.Time) error {
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read PostgreSQL Pod metadata for deployment bootstrap verification")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("PostgreSQL Pod is unavailable for deployment bootstrap verification")
	}
	query := `SELECT json_build_object('sessions',COALESCE((SELECT json_agg(json_build_object('caller',caller,'deployment_id',deployment_id,'expires_at',expires_at) ORDER BY created_at) FROM public.pki_deployment_bootstrap_sessions WHERE status='active'),'[]'::json),'active_caller_index',EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='pki_deployment_bootstrap_sessions' AND indexname='pki_deployment_bootstrap_sessions_active_caller_idx' AND indexdef LIKE '%WHERE (status = ''active''::text)%'),'legacy_caller_constraint',EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='public.pki_deployment_bootstrap_sessions'::regclass AND conname='pki_deployment_bootstrap_sessions_environment_caller_key'))`
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query).CombinedOutput()
	var report liveDeploymentBootstrapReport
	if commandErr != nil || json.Unmarshal([]byte(strings.TrimSpace(string(output))), &report) != nil {
		return errors.New("deployment bootstrap session verification did not return valid metadata")
	}
	if !report.ActiveCallerIndex || report.LegacyCallerConstraint {
		return errors.New("deployment bootstrap caller schema is outdated; active-only caller ownership migration is required before signing")
	}
	if len(report.Sessions) == 0 {
		return nil
	}
	issues := make([]string, 0, len(report.Sessions))
	for _, session := range report.Sessions {
		state := "active"
		if !now.Before(session.ExpiresAt) {
			state = "expired but still active"
		}
		issues = append(issues, fmt.Sprintf("caller=%s deployment=%s state=%s", session.Caller, session.DeploymentID, state))
	}
	return fmt.Errorf("deployment bootstrap session already owns a caller; seal or expire it before signing: %s", strings.Join(issues, ", "))
}

func deploymentUsesPKI(deployment liveDeployment) bool {
	if strings.Contains(deployment.Metadata.Name, "pki") || deployment.Metadata.Name == "certissuer" || deployment.Metadata.Name == "factoryenroll" {
		return true
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, item := range container.Env {
			if strings.Contains(item.Name, "PKI") || strings.Contains(item.Name, "_TLS_") {
				return true
			}
		}
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if strings.Contains(volume.Name, "pki") {
			return true
		}
	}
	return false
}

func verifyLiveServiceClientRegistry(kubeconfig, namespace string, deployments liveDeploymentList) error {
	var controller *liveDeployment
	for i := range deployments.Items {
		if deployments.Items[i].Metadata.Name == "pki-controller" {
			controller = &deployments.Items[i]
			break
		}
	}
	if controller == nil {
		return nil
	}
	settings := map[string]string{}
	for _, container := range controller.Spec.Template.Spec.Containers {
		for _, item := range container.Env {
			settings[item.Name] = item.Value
		}
	}
	rootID := settings["PKI_SERVICE_CLIENT_SERVICE_ROOT_ID"]
	rootSHA := settings["PKI_SERVICE_CLIENT_ROOT_SHA256"]
	consumers := settings["PKI_REQUIRED_CONSUMERS_SERVICE"]
	if rootID == "" || rootSHA == "" || consumers == "" {
		return errors.New("pki-controller Service client registry settings are incomplete")
	}
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=pki-controller", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read pki-controller Pod metadata")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("pki-controller Pod is unavailable for Service client registry verification")
	}
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", pods.Items[0].Metadata.Name, "--", "/app/pkicontroller", "recovery-inventory-service-client", rootID, rootSHA, consumers).CombinedOutput()
	start, end := strings.IndexByte(string(output), '{'), strings.LastIndexByte(string(output), '}')
	var report struct {
		Status                 string `json:"status"`
		Issuances              int    `json:"issuances"`
		PendingIssuances       int    `json:"pending_issuances"`
		InvalidRecords         int    `json:"invalid_records"`
		UnpublishedRevocations int    `json:"unpublished_revocations"`
		MissingAcknowledgments int    `json:"missing_acknowledgments"`
	}
	if start < 0 || end < start || json.Unmarshal(output[start:end+1], &report) != nil {
		return errors.New("pki-controller Service client registry verification did not return a valid report")
	}
	if commandErr != nil || report.Status != "service-client-registry-inventory-checked" || report.Issuances == 0 || report.PendingIssuances != 0 || report.InvalidRecords != 0 || report.UnpublishedRevocations != 0 || report.MissingAcknowledgments != 0 {
		return fmt.Errorf("pki-controller Service client registry is incomplete: issuances=%d pending=%d invalid=%d unpublished_revocations=%d missing_acknowledgments=%d", report.Issuances, report.PendingIssuances, report.InvalidRecords, report.UnpublishedRevocations, report.MissingAcknowledgments)
	}
	return nil
}
