package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
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
					Name string `json:"name"`
					Env  []struct {
						Name      string `json:"name"`
						Value     string `json:"value"`
						ValueFrom any    `json:"valueFrom"`
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
					ConfigMap struct {
						Name string `json:"name"`
					} `json:"configMap"`
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

type livePendingServiceClientIssuance struct {
	Caller    string    `json:"caller"`
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
}

type liveDeploymentBootstrapReport struct {
	Sessions               []liveDeploymentBootstrapSession   `json:"sessions"`
	PendingIssuances       []livePendingServiceClientIssuance `json:"pending_issuances"`
	PendingCount           int                                `json:"pending_count"`
	ActiveCallerIndex      bool                               `json:"active_caller_index"`
	LegacyCallerConstraint bool                               `json:"legacy_caller_constraint"`
}

// verifySecretStoreK8SRuntime is a read-only live check. It validates every
// certificate/key pair stored in a Secret owned by the selected stack and makes
// PKI-backed workload readiness part of secret verification. The latter catches
// identities held on PVCs whose authorization can only be proven by the running
// service. It deliberately has no CRL or OCSP dependency.
func verifySecretStoreK8SRuntime(store secretStore, now time.Time) error {
	if err := verifySelectedStackMetadata(store); err != nil {
		return err
	}
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
		if err := verifyLiveAccountManagerEnvironment(store.Environment, secret.Metadata.Namespace, secret.Metadata.Name, secret.Data); err != nil {
			failures = append(failures, err.Error())
		}
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
	serviceClientController, serviceClientRegistryConfigured := false, false
	deploymentRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployments", "-o", "json").Output()
	if err != nil {
		failures = append(failures, "cannot read live Video Cloud deployment status")
	} else {
		var deployments liveDeploymentList
		if json.Unmarshal(deploymentRaw, &deployments) != nil {
			failures = append(failures, "live Video Cloud deployment metadata is invalid")
		} else {
			serviceClientController, serviceClientRegistryConfigured = serviceClientRegistryInputs(deployments)
			if err := verifyCertIssuerBootstrapConfiguration(kubeconfig, namespace, deployments); err != nil {
				failures = append(failures, err.Error())
			}
			if err := verifyAutomaticDeviceTrustConsumers(store.Environment, kubeconfig, namespace, deployments, now); err != nil {
				failures = append(failures, err.Error())
			}
			if err := verifyLiveRootPolicyReferences(kubeconfig, stack+"-platform", deployments); err != nil {
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
			if serviceClientController {
				if serviceClientRegistryConfigured {
					if err := verifyLiveServiceClientRegistry(kubeconfig, namespace, deployments); err != nil {
						failures = append(failures, err.Error())
					}
				} else if err := verifyUnadoptedServiceClientRegistry(kubeconfig, stack+"-platform", now); err != nil {
					failures = append(failures, err.Error())
				}
			}
		}
	}
	accountNamespace := stack + "-account-manager"
	accountRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", accountNamespace, "get", "deployments", "-o", "json").Output()
	if err != nil {
		failures = append(failures, "cannot read live Account Manager deployment status")
	} else {
		var deployments liveDeploymentList
		if json.Unmarshal(accountRaw, &deployments) != nil {
			failures = append(failures, "live Account Manager deployment metadata is invalid")
		} else if err := verifyAccountManagerPKIConfiguration(deployments); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if serviceClientController && serviceClientRegistryConfigured {
		if err := verifyLiveDeploymentBootstrapSessions(kubeconfig, stack+"-platform", now); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return fmt.Errorf("live Kubernetes secret validation failed: %s", strings.Join(failures, "; "))
}

// Product CA creation waits for receipts from these workloads. If either
// automatic trust consumer is disabled, a new Product remains pending forever.
func verifyAutomaticDeviceTrustConsumers(environment, kubeconfig, namespace string, deployments liveDeploymentList, now time.Time) error {
	settings := map[string]map[string]string{}
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			key := deployment.Metadata.Name + "/" + container.Name
			settings[key] = map[string]string{}
			for _, env := range container.Env {
				settings[key][env.Name] = strings.TrimSpace(env.Value)
			}
		}
	}
	controller := settings["pki-controller/pki-controller"]
	if controller == nil {
		return nil
	}
	if controller["PKI_DEV_FIXED_DEVICE_ROOT_TRUST"] != "" {
		return verifyDevFixedDeviceRootTrust(environment, kubeconfig, namespace, deployments, settings, now)
	}
	consumers := firstNonEmpty(controller["PKI_REQUIRED_BUNDLE_CONSUMERS_DEVICE"], controller["PKI_REQUIRED_CONSUMERS_DEVICE"])
	var missing []string
	for _, consumer := range strings.Split(consumers, ",") {
		switch strings.TrimSpace(consumer) {
		case "video-cloud-api":
			api := settings["video-cloud-api-pki/app"]
			if api["VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR"] == "" || api["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"] != "true" {
				missing = append(missing, "video-cloud-api-pki automatic Device trust and Product CRL enforcement")
			}
		case "pkibroker":
			broker := settings["mqtt-pki/pkibroker"]
			if broker["PKI_BROKER_DEVICE_AUTOMATIC_STATE_DIR"] == "" || broker["PKI_BROKER_REQUIRE_CRLS"] != "true" || broker["PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED"] != "true" {
				missing = append(missing, "mqtt-pki/pkibroker automatic Device trust and CRL enforcement")
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Product PKI cannot activate new issuers: required bundle consumers are not configured: %s", strings.Join(missing, "; "))
	}
	return nil
}

// The dev shortcut is valid only when both consumers use the same fixed Root
// as the controller and registry status enforcement remains enabled.
func verifyDevFixedDeviceRootTrust(environment, kubeconfig, namespace string, deployments liveDeploymentList, settings map[string]map[string]string, now time.Time) error {
	controller := settings["pki-controller/pki-controller"]
	if environment != "dev" || controller["PKI_ENVIRONMENT"] != "dev" || controller["PKI_DEV_FIXED_DEVICE_ROOT_TRUST"] != "true" || controller["PKI_DEVICE_ROOT_ID"] == "" || len(controller["PKI_DEVICE_ROOT_SHA256"]) != 64 {
		return errors.New("dev fixed Device Root trust requires the dev controller and its Root ID/fingerprint pin")
	}
	api := settings["video-cloud-api-pki/app"]
	if api["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"] != "true" || api["VIDEO_CLOUD_AUTH_MTLS_REQUIRED"] != "true" || api["VIDEO_CLOUD_AUTH_DISABLE_ACL"] == "true" || api["VIDEO_CLOUD_AUTH_TRUSTED_CERT_HEADERS"] == "true" || api["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"] != "false" || api["VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR"] != "" || api["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"] != "" || api["VIDEO_CLOUD_AUTH_DEVICE_CA_CRL"] != "" || api["VIDEO_CLOUD_AUTH_DEVICE_CA_OCSP_URL"] != "" || api["VIDEO_CLOUD_AUTH_DEVICE_REVOCATION"] != "" || api["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"] == "" {
		return errors.New("dev fixed Device Root trust requires API direct mTLS, Product PKI, static Root and registry enforcement without CRL consumers")
	}
	broker := settings["mqtt-pki/pkibroker"]
	if broker["PKI_ENVIRONMENT"] != "dev" || broker["PKI_BROKER_REQUIRE_CRLS"] != "false" || broker["PKI_BROKER_DEVICE_AUTOMATIC_STATE_DIR"] != "" || broker["PKI_BROKER_DEVICE_CRL_MANIFEST"] != "" || broker["PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED"] == "true" {
		return errors.New("dev fixed Device Root trust requires broker registry enforcement without dynamic Device trust or CRLs")
	}
	for _, target := range []struct{ deployment, container, path string }{
		{"video-cloud-api-pki", "app", api["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"]},
		{"mqtt-pki", "pkibroker", "/run/pki-device/roots.pem"},
	} {
		if err := verifyMountedDeviceRoot(kubeconfig, namespace, deployments, target.deployment, target.container, target.path, controller["PKI_DEVICE_ROOT_SHA256"], now); err != nil {
			return err
		}
	}
	return nil
}

func verifyMountedDeviceRoot(kubeconfig, namespace string, deployments liveDeploymentList, deploymentName, containerName, path, fingerprint string, now time.Time) error {
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name != deploymentName {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != containerName {
				continue
			}
			for _, mount := range container.VolumeMounts {
				if !strings.HasPrefix(path, mount.MountPath+"/") {
					continue
				}
				key := strings.TrimPrefix(path, mount.MountPath+"/")
				for _, volume := range deployment.Spec.Template.Spec.Volumes {
					if volume.Name == mount.Name && volume.ConfigMap.Name != "" {
						raw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "configmap", volume.ConfigMap.Name, "-o", "json").Output()
						if err != nil {
							return fmt.Errorf("%s fixed Device Root ConfigMap is unreadable", deploymentName)
						}
						var config struct {
							Data map[string]string `json:"data"`
						}
						if json.Unmarshal(raw, &config) != nil {
							return fmt.Errorf("%s fixed Device Root ConfigMap is invalid", deploymentName)
						}
						block, rest := pem.Decode([]byte(config.Data[key]))
						if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
							return fmt.Errorf("%s must pin exactly one Device Root", deploymentName)
						}
						cert, err := x509.ParseCertificate(block.Bytes)
						if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
							return fmt.Errorf("%s fixed Device Root is invalid or expired", deploymentName)
						}
						digest := sha256.Sum256(cert.Raw)
						if hex.EncodeToString(digest[:]) != fingerprint {
							return fmt.Errorf("%s fixed Device Root differs from controller pin", deploymentName)
						}
						return nil
					}
				}
			}
		}
	}
	return fmt.Errorf("%s fixed Device Root is not mounted at %s", deploymentName, path)
}

func verifyLiveAccountManagerEnvironment(environment, namespace, name string, data map[string]string) error {
	if namespace != "video-cloud-"+environment+"-account-manager" || name != "account-manager-runtime" {
		return nil
	}
	encoded, ok := data["ACCOUNT_MANAGER_ENV"]
	if !ok {
		return errors.New("Account Manager runtime Secret is missing ACCOUNT_MANAGER_ENV")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || strings.TrimSpace(string(raw)) != environment {
		return fmt.Errorf("Account Manager runtime Secret ACCOUNT_MANAGER_ENV does not match selected %s environment", environment)
	}
	return nil
}

func verifySelectedStackMetadata(store secretStore) error {
	path := filepath.Join(store.Root, "env", "stack.env")
	values, err := readEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("selected environment stack metadata cannot be read")
	}
	expected := "video-cloud-" + store.Environment
	if values["CLOUD_ENV_NAME"] != store.Environment || values["CLOUD_STACK_NAME"] != expected {
		return fmt.Errorf("selected %s environment metadata points to CLOUD_ENV_NAME=%q CLOUD_STACK_NAME=%q; run sync-env for the selected environment before E2E or deployment", store.Environment, values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"])
	}
	for _, key := range []string{"CERTIFICATE_APP_CSR_KEY_ALGORITHMS", "CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS"} {
		if _, err := deploymentCertificateAlgorithms(key, values[key]); err != nil {
			return fmt.Errorf("selected %s environment has invalid %s: %w", store.Environment, key, err)
		}
	}
	return nil
}

// Dynamic root consumers fetch a policy before serving. A deployment can have
// valid certificates and Secrets yet loop on controller HTTP 503 when its root
// ID has no policy row (for example after a dev database rebuild).
func verifyLiveRootPolicyReferences(kubeconfig, platformNamespace string, deployments liveDeploymentList) error {
	fields := [][2]string{
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_SERVICE_ROOT_ID", "VIDEO_CLOUD_ACCOUNT_MANAGER_SERVICE_ROOT_STATE"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVICE_ROOT_ID", "VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVICE_ROOT_STATE"},
		{"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVICE_ROOT_ID", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVICE_ROOT_STATE"},
		{"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_RENEWAL_SERVICE_ROOT_ID", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_RENEWAL_SERVICE_ROOT_STATE"},
		{"VIDEO_CLOUD_MQTT_ROOT_ID", "VIDEO_CLOUD_MQTT_ROOT_STATE"},
		{"VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"},
		{"VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE"},
		{"PKI_BROKER_DEVICE_ROOT_ID", "PKI_BROKER_DEVICE_ROOT_STATE"},
		{"PKI_BROKER_APP_ROOT_ID", "PKI_BROKER_APP_ROOT_STATE"},
		{"FACTORY_ENROLL_CERT_ISSUER_SERVICE_ROOT_ID", "FACTORY_ENROLL_CERT_ISSUER_SERVICE_ROOT_STATE"},
		{"FACTORY_ENROLL_ACCOUNT_MANAGER_SERVICE_ROOT_ID", "FACTORY_ENROLL_ACCOUNT_MANAGER_SERVICE_ROOT_STATE"},
		{"PKI_TURN_APP_ROOT_ID", "PKI_TURN_APP_ROOT_STATE"},
	}
	references := map[string][]string{}
	canonicalUUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			settings := map[string]string{}
			for _, env := range container.Env {
				settings[env.Name] = strings.TrimSpace(env.Value)
			}
			for _, field := range fields {
				id, state := settings[field[0]], settings[field[1]]
				if id == "" && state == "" {
					continue
				}
				label := deployment.Metadata.Name + "/" + container.Name + " " + field[0]
				if id == "" || state == "" || !canonicalUUID.MatchString(id) {
					return fmt.Errorf("dynamic root policy reference is incomplete: %s", label)
				}
				references[id] = append(references[id], label)
			}
		}
	}
	if len(references) == 0 {
		return nil
	}
	ids := make([]string, 0, len(references))
	for id := range references {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", platformNamespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read PostgreSQL Pod metadata for root policy verification")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("PostgreSQL Pod is unavailable for root policy verification")
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + id + "'"
	}
	query := "SELECT COALESCE(json_agg(issuer_id ORDER BY issuer_id),'[]'::json) FROM (SELECT DISTINCT issuer_id::text AS issuer_id FROM public.pki_root_distrust WHERE issuer_id::text IN (" + strings.Join(quoted, ",") + ")) policies"
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", platformNamespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query).CombinedOutput()
	var present []string
	if commandErr != nil || json.Unmarshal([]byte(strings.TrimSpace(string(output))), &present) != nil {
		return errors.New("dynamic root policy verification did not return valid database metadata")
	}
	found := map[string]bool{}
	for _, id := range present {
		found[id] = true
	}
	var missing []string
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, strings.Join(references[id], ", ")+" references root "+id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("dynamic root policy is missing from the PKI registry: %s", strings.Join(missing, "; "))
	}
	return nil
}

// The Service bundle is the current admission path. An older sidecar with
// optional CRL/root consumers enabled can be Ready while its local signing
// socket still returns 503, so inspect that configuration before rollout.
func verifyAccountManagerPKIConfiguration(deployments liveDeploymentList) error {
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name != "account-manager" {
			continue
		}
		var appSocket, managementSocket string
		var unsupported []string
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if container.Name == "app" && env.Name == "APP_CERT_ISSUER_SOCKET" {
					appSocket = env.Value
				}
				if container.Name != "pkimanagement" {
					continue
				}
				if env.Name == "PKI_MANAGEMENT_SOCKET" {
					managementSocket = env.Value
				}
				accountClient := strings.HasPrefix(env.Name, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_")
				serviceOrigin := strings.HasPrefix(env.Name, "PKI_MANAGEMENT_ISSUER_") || strings.HasPrefix(env.Name, "PKI_MANAGEMENT_CONTROLLER_")
				if (accountClient || serviceOrigin) && (strings.Contains(env.Name, "_CRL_MANIFEST") || strings.Contains(env.Name, "_SERVICE_ROOT_") || serviceOrigin && (strings.HasSuffix(env.Name, "_PKI_CONTROLLER_URL") || strings.HasSuffix(env.Name, "_MANAGEMENT_CA"))) && strings.TrimSpace(env.Value) != "" {
					unsupported = append(unsupported, env.Name)
				}
			}
		}
		if len(unsupported) > 0 {
			sort.Strings(unsupported)
			return fmt.Errorf("Account Manager sidecar enables CRL/root-consumer settings before that deployment contract exists: %s", strings.Join(unsupported, ","))
		}
		if appSocket != "" && (managementSocket == "" || appSocket != managementSocket) {
			return errors.New("Account Manager app and PKI sidecar use different managed socket paths")
		}
	}
	return nil
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
	sessionID := strings.TrimSpace(settings["PKI_BOOTSTRAP_SESSION_ID"])
	if subject != caller || settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA"] == "" || settings["CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT"] == "" || settings["CERT_ISSUER_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY"] == "" || sessionID == "" {
		return errors.New("certissuer bootstrap caller requires matching subject, CA, client cert/key and session id")
	}
	if strings.TrimSpace(settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID"]) != sessionID {
		return errors.New("certissuer bootstrap handler and managed client must use the same session id")
	}
	renewal, err := url.Parse(strings.TrimSpace(settings["CERT_ISSUER_HOST_RENEWAL_URL"]))
	if err != nil || renewal.Scheme != "https" || !isLoopbackHost(renewal.Hostname()) {
		return errors.New("certissuer first-trust renewal URL must use loopback so Pod readiness cannot block self-enrollment")
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	address := net.ParseIP(strings.TrimSpace(host))
	return address != nil && address.IsLoopback()
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
// starting while an earlier deployment bootstrap owns its caller or a Service
// client issuance still awaits reconciliation. It reports only non-secret metadata.
func verifyLiveDeploymentBootstrapSessions(kubeconfig, namespace string, now time.Time) error {
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read PostgreSQL Pod metadata for deployment bootstrap verification")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("PostgreSQL Pod is unavailable for deployment bootstrap verification")
	}
	query := `SELECT json_build_object('sessions',COALESCE((SELECT json_agg(json_build_object('caller',caller,'deployment_id',deployment_id,'expires_at',expires_at) ORDER BY created_at) FROM public.pki_deployment_bootstrap_sessions WHERE status='active'),'[]'::json),'pending_issuances',COALESCE((SELECT json_agg(json_build_object('caller',caller,'request_id',request_id,'created_at',created_at) ORDER BY created_at) FROM (SELECT caller,request_id,created_at FROM public.pki_service_client_issuances WHERE status='issuing' ORDER BY created_at LIMIT 20) pending),'[]'::json),'pending_count',(SELECT count(*) FROM public.pki_service_client_issuances WHERE status='issuing'),'active_caller_index',EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='pki_deployment_bootstrap_sessions' AND indexname='pki_deployment_bootstrap_sessions_active_caller_idx' AND indexdef LIKE '%WHERE (status = ''active''::text)%'),'legacy_caller_constraint',EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='public.pki_deployment_bootstrap_sessions'::regclass AND conname='pki_deployment_bootstrap_sessions_environment_caller_key'))`
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query).CombinedOutput()
	var report liveDeploymentBootstrapReport
	if commandErr != nil || json.Unmarshal([]byte(strings.TrimSpace(string(output))), &report) != nil {
		return errors.New("deployment bootstrap session verification did not return valid metadata")
	}
	if !report.ActiveCallerIndex || report.LegacyCallerConstraint {
		return errors.New("deployment bootstrap caller schema is outdated; active-only caller ownership migration is required before signing")
	}
	if len(report.Sessions) == 0 && report.PendingCount == 0 {
		return nil
	}
	issues := make([]string, 0, len(report.Sessions)+len(report.PendingIssuances))
	for _, session := range report.Sessions {
		state := "active"
		if !now.Before(session.ExpiresAt) {
			state = "expired but still active"
		}
		issues = append(issues, fmt.Sprintf("caller=%s deployment=%s state=%s", session.Caller, session.DeploymentID, state))
	}
	for _, pending := range report.PendingIssuances {
		issues = append(issues, fmt.Sprintf("pending Service client issuance caller=%s request=%s created=%s", pending.Caller, pending.RequestID, pending.CreatedAt.UTC().Format(time.RFC3339)))
	}
	if report.PendingCount > len(report.PendingIssuances) {
		issues = append(issues, fmt.Sprintf("%d additional pending Service client issuances", report.PendingCount-len(report.PendingIssuances)))
	}
	return fmt.Errorf("deployment signing state requires reconciliation before another signing attempt: %s", strings.Join(issues, ", "))
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

// The managed Service client registry is adopted explicitly. An older
// controller with none of these inputs still needs a database check for
// stranded issuers or signing requests, but it cannot run the new inventory.
func serviceClientRegistryInputs(deployments liveDeploymentList) (controllerPresent, configured bool) {
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name != "pki-controller" {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, item := range container.Env {
				switch item.Name {
				case "RTK_DEPLOYMENT_SERVICE_ISSUER_ID", "PKI_SERVICE_CLIENT_ROOT_SHA256", "PKI_REQUIRED_CONSUMERS_SERVICE":
					configured = configured || strings.TrimSpace(item.Value) != "" || item.ValueFrom != nil
				}
			}
		}
		return true, configured
	}
	return false, false
}

func verifyUnadoptedServiceClientRegistry(kubeconfig, namespace string, now time.Time) error {
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=postgresql", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read PostgreSQL Pod metadata for Service client registry verification")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("PostgreSQL Pod is unavailable for Service client registry verification")
	}
	query := `SELECT json_build_object('bootstrap_table',to_regclass('public.pki_deployment_bootstrap_sessions') IS NOT NULL,'service_issuers',(SELECT count(*) FROM public.pki_issuers WHERE domain='service'),'pending_issuances',(SELECT count(*) FROM public.pki_service_client_issuances WHERE status='issuing'))`
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", pods.Items[0].Metadata.Name, "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-c", query).CombinedOutput()
	var state struct {
		BootstrapTable   bool `json:"bootstrap_table"`
		ServiceIssuers   int  `json:"service_issuers"`
		PendingIssuances int  `json:"pending_issuances"`
	}
	if commandErr != nil || json.Unmarshal([]byte(strings.TrimSpace(string(output))), &state) != nil {
		return errors.New("unadopted Service client registry verification did not return valid database metadata")
	}
	if state.ServiceIssuers != 0 || state.PendingIssuances != 0 {
		return fmt.Errorf("Service client registry is not configured but has issuers=%d pending_issuances=%d", state.ServiceIssuers, state.PendingIssuances)
	}
	if state.BootstrapTable {
		return verifyLiveDeploymentBootstrapSessions(kubeconfig, namespace, now)
	}
	return nil
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
	// The inventory command takes the active Service intermediate, not the
	// Service Root used by the optional dynamic root consumer.
	issuerID := settings["RTK_DEPLOYMENT_SERVICE_ISSUER_ID"]
	rootSHA := settings["PKI_SERVICE_CLIENT_ROOT_SHA256"]
	consumers := settings["PKI_REQUIRED_CONSUMERS_SERVICE"]
	if issuerID == "" || rootSHA == "" || consumers == "" {
		return errors.New("pki-controller Service client registry issuer settings are incomplete")
	}
	podsRaw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=pki-controller", "-o", "json").Output()
	if err != nil {
		return errors.New("cannot read pki-controller Pod metadata")
	}
	var pods livePodList
	if json.Unmarshal(podsRaw, &pods) != nil || len(pods.Items) == 0 || pods.Items[0].Metadata.Name == "" {
		return errors.New("pki-controller Pod is unavailable for Service client registry verification")
	}
	output, commandErr := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "exec", pods.Items[0].Metadata.Name, "--", "/app/pkicontroller", "recovery-inventory-service-client", issuerID, rootSHA, consumers).CombinedOutput()
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
	if report.Status == "service-client-registry-inventory-incomplete" {
		return errors.New("pki-controller Service client registry inventory did not complete; check controller database grants and query errors")
	}
	if commandErr != nil || report.Status != "service-client-registry-inventory-checked" || report.Issuances == 0 || report.PendingIssuances != 0 || report.InvalidRecords != 0 || report.UnpublishedRevocations != 0 || report.MissingAcknowledgments != 0 {
		return fmt.Errorf("pki-controller Service client registry is incomplete: issuances=%d pending=%d invalid=%d unpublished_revocations=%d missing_acknowledgments=%d", report.Issuances, report.PendingIssuances, report.InvalidRecords, report.UnpublishedRevocations, report.MissingAcknowledgments)
	}
	return nil
}
