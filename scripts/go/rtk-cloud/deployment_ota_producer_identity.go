package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	otaProducerSealOwner      = "ota-producer-period-seal"
	otaProducerIdentitySecret = "ota-producer-period-seal-identity"
)

type otaProducerIdentityState struct {
	Version  int      `json:"version"`
	Subject  string   `json:"subject"`
	Domain   string   `json:"domain,omitempty"`
	DNSNames []string `json:"dns_names,omitempty"`
	Current  *struct {
		PrivateKey  string    `json:"private_key_pem"`
		Chain       string    `json:"certificate_chain_pem"`
		InstalledAt time.Time `json:"installed_at"`
	} `json:"current"`
	Pending json.RawMessage `json:"pending,omitempty"`
}

// The operator installs this owner once. Normal deploys never apply the state
// Secret, so an older local initial certificate cannot replace a renewed key.
func installOTAProducerIdentityState(store secretStore, env map[string]string, record deploymentServiceIdentity) error {
	if record.Owner != otaProducerSealOwner || record.Subject != "service:ota" || record.Environment != store.Environment || record.Stack != env["CLOUD_STACK_NAME"] {
		return errors.New("OTA monthly identity owner differs from selected environment")
	}
	cfg, root, err := readDeploymentServiceIssuer(store, record.Stack)
	if err != nil {
		return err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	live, err := lkeGetOptionalSecret(namespace, otaProducerIdentitySecret, "--kubeconfig", store.KubeconfigPath())
	if err != nil {
		return err
	}
	if live == nil {
		if record.RuntimeSecretUID != "" {
			return errors.New("recorded OTA identity Secret is missing; explicit recovery required")
		}
		if err := validateDeploymentServiceIdentity(record, root); err != nil {
			return err
		}
		accountCA, err := store.read(cfg.RegistrationServerCAFile)
		if err != nil {
			return err
		}
		issuerCA, err := store.read(cfg.ServerCAFile)
		if err != nil {
			return err
		}
		state, err := json.Marshal(map[string]any{"version": 1, "subject": record.Subject, "current": map[string]any{"private_key_pem": record.PrivateKey, "certificate_chain_pem": record.CertificateChain, "installed_at": time.Now().UTC()}})
		if err != nil {
			return err
		}
		secret := newK8SSecretObject(namespace, otaProducerIdentitySecret, map[string]string{"identity.json": string(state), "account-manager-ca.crt": accountCA, "certissuer-ca.crt": issuerCA})
		secret["metadata"].(map[string]any)["annotations"] = map[string]string{"rtk.realtek.com/identity-owner": record.Owner, "rtk.realtek.com/environment": record.Environment, "rtk.realtek.com/stack": record.Stack, "rtk.realtek.com/service-root-sha256": record.RootSHA256}
		payload, _ := json.Marshal(secret)
		if _, err := kubectlCombinedOutput(bytes.NewReader(payload), "create", "-f", "-", "--kubeconfig", store.KubeconfigPath()); err != nil {
			return errors.New("OTA monthly identity creation outcome is unknown; retain local record and inspect before retry")
		}
		live, err = lkeGetOptionalSecret(namespace, otaProducerIdentitySecret, "--kubeconfig", store.KubeconfigPath())
		if err != nil || live == nil {
			return errors.New("OTA monthly identity creation cannot be confirmed; local record retained")
		}
	}
	uid, err := validateOTAProducerIdentitySecret(live, record, root, false)
	if err != nil {
		return err
	}
	if record.RuntimeSecretUID == "" {
		record.RuntimeSecretUID = uid
		path, _ := deploymentServiceIdentityPath(record.Subject, record.Owner)
		if err := writeDeploymentServiceIdentity(store, path, record); err != nil {
			return err
		}
	}
	return nil
}

func validateOTAProducerIdentitySecret(secret map[string]any, record deploymentServiceIdentity, root string, allowPending bool) (string, error) {
	metadata, _ := secret["metadata"].(map[string]any)
	uid, _ := metadata["uid"].(string)
	annotations, _ := metadata["annotations"].(map[string]any)
	if uid == "" || (record.RuntimeSecretUID != "" && uid != record.RuntimeSecretUID) ||
		annotations["rtk.realtek.com/identity-owner"] != record.Owner || annotations["rtk.realtek.com/environment"] != record.Environment ||
		annotations["rtk.realtek.com/stack"] != record.Stack || annotations["rtk.realtek.com/service-root-sha256"] != record.RootSHA256 {
		return "", errors.New("OTA monthly identity Secret scope or UID changed; explicit reconciliation required")
	}
	raw, err := kubernetesSecretBytes(secret, "identity.json")
	var state otaProducerIdentityState
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Subject != "service:ota" || state.Domain != "" || len(state.DNSNames) != 0 || state.Current == nil || state.Current.InstalledAt.IsZero() {
		return "", errors.New("OTA monthly identity owner state is incomplete")
	}
	if !allowPending && len(state.Pending) > 0 && string(state.Pending) != "null" {
		return "", errors.New("OTA monthly identity issuance is pending; complete operator maintenance before sealing")
	}
	current := record
	current.CertificateChain, current.PrivateKey, current.Fingerprint = state.Current.Chain, state.Current.PrivateKey, ""
	if err := validateDeploymentServiceIdentity(current, root); err != nil {
		return "", fmt.Errorf("OTA monthly current identity is invalid: %w", err)
	}
	if record.RuntimeSecretUID == "" {
		chain, _ := pemCertificates([]byte(current.CertificateChain))
		if certificateSHA256(chain[0]) != record.Fingerprint {
			return "", errors.New("unrecorded OTA owner Secret does not contain the expected initial identity; reconcile before adoption")
		}
	}
	return uid, nil
}

func deploymentTrustRootPin(raw string) (string, error) {
	certs, err := pemCertificates([]byte(raw))
	if err != nil {
		return "", err
	}
	pin := ""
	for _, cert := range certs {
		if cert.IsCA && cert.CheckSignatureFrom(cert) == nil {
			if pin != "" {
				return "", errors.New("monthly Job trust requires one independent root")
			}
			pin = certificateSHA256(cert)
		}
	}
	if pin == "" {
		return "", errors.New("monthly Job trust root is missing")
	}
	return pin, nil
}

func lkeRequireOTAProducerSealIdentity(env map[string]string, allowPending bool) error {
	store, err := deploymentIdentityStore(env)
	if err != nil {
		return err
	}
	cfg, root, err := readDeploymentServiceIssuer(store, env["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	path, _ := deploymentServiceIdentityPath("service:ota", otaProducerSealOwner)
	raw, err := store.read(path)
	var record deploymentServiceIdentity
	if err != nil || json.Unmarshal([]byte(raw), &record) != nil || record.Version != 1 || record.Owner != otaProducerSealOwner || record.Subject != "service:ota" || record.Environment != store.Environment || record.Stack != env["CLOUD_STACK_NAME"] || record.RootSHA256 != cfg.RootSHA256 || record.RuntimeSecretUID == "" {
		return errors.New("OTA monthly Job requires its own recorded and installed environment identity")
	}
	secret, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", otaProducerIdentitySecret)
	if err != nil {
		return errors.New("OTA monthly identity Secret is unavailable")
	}
	uid, err := validateOTAProducerIdentitySecret(secret, record, root, allowPending)
	if err != nil {
		return err
	}
	accountCA, err := kubernetesSecretBytes(secret, "account-manager-ca.crt")
	if err != nil {
		return errors.New("OTA monthly Account Manager trust is unavailable")
	}
	canonicalAccountCA, err := store.read(cfg.RegistrationServerCAFile)
	if err != nil || !bytes.Equal(bytes.TrimSpace(accountCA), bytes.TrimSpace([]byte(canonicalAccountCA))) {
		return errors.New("OTA monthly Account Manager trust differs from selected operator configuration")
	}
	accountPin, err := deploymentTrustRootPin(string(accountCA))
	if err != nil {
		return err
	}
	issuerCA, err := kubernetesSecretBytes(secret, "certissuer-ca.crt")
	if err != nil {
		return errors.New("OTA monthly renewal trust is unavailable")
	}
	canonicalIssuerCA, err := store.read(cfg.ServerCAFile)
	if err != nil || !bytes.Equal(bytes.TrimSpace(issuerCA), bytes.TrimSpace([]byte(canonicalIssuerCA))) {
		return errors.New("OTA monthly renewal trust differs from selected operator configuration")
	}
	issuerPin, err := deploymentTrustRootPin(string(issuerCA))
	if err != nil {
		return err
	}
	// The API's registration key is not an acceptable substitute for this owner.
	api, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", otaRegistrarIdentitySecretName)
	if err != nil {
		return errors.New("OTA API identity cannot be compared with the monthly owner")
	}
	apiCert, err := kubernetesSecretBytes(api, "client.crt")
	var current otaProducerIdentityState
	state, _ := kubernetesSecretBytes(secret, "identity.json")
	_ = json.Unmarshal(state, &current)
	if err != nil {
		return errors.New("OTA API certificate cannot be compared with the monthly owner")
	}
	apiChain, apiErr := pemCertificates(apiCert)
	jobChain, jobErr := pemCertificates([]byte(current.Current.Chain))
	if apiErr != nil || jobErr != nil {
		return errors.New("OTA owner certificates cannot be compared")
	}
	apiPublic, apiErr := x509.MarshalPKIXPublicKey(apiChain[0].PublicKey)
	jobPublic, jobErr := x509.MarshalPKIXPublicKey(jobChain[0].PublicKey)
	if apiErr != nil || jobErr != nil || bytes.Equal(apiPublic, jobPublic) {
		return errors.New("OTA monthly Job must not share the OTA API private key")
	}
	env["LKE_OTA_PRODUCER_IDENTITY_SECRET_UID"] = uid
	env["LKE_OTA_PRODUCER_IDENTITY_ROOT_SHA256"] = record.RootSHA256
	env["LKE_OTA_PRODUCER_ACCOUNT_MANAGER_ROOT_SHA256"] = accountPin
	env["LKE_OTA_PRODUCER_RENEWAL_ROOT_SHA256"] = issuerPin
	// The operator origin may be a local port-forward. A Pod must use the
	// private issuer Service while retaining the independently recorded CA/SNI.
	env["LKE_OTA_PRODUCER_RENEWAL_URL"] = "https://certissuer." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:9443"
	env["LKE_OTA_PRODUCER_RENEWAL_SERVER_NAME"] = cfg.ServerName
	return nil
}
