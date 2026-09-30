package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// This dev repair adds one pinned public Device Root to the existing ingress
// client-CA bundle and allows the Product device chain's three CA hops.
func runDeploymentDeviceRootIngressTrust(args []string) error {
	return runDeploymentDeviceRootIngressTrustWithOps(args, deviceRootIngressOps{
		credentials: func(environment string) (func(), error) {
			_, restore, err := configureProvisionSecretStore(environment)
			return restore, err
		},
		requireRoute: lkeRequireActiveOTADeviceEdgeRoute,
		get:          kubectlResourceJSON,
		patch: func(namespace, kind, name, patch string) error {
			return runKubectl("-n", namespace, "patch", kind, name, "--type=json", "-p", patch)
		},
	})
}

type deviceRootIngressOps struct {
	credentials  func(string) (func(), error)
	requireRoute func(map[string]string) error
	get          func(string, string, string) (map[string]any, error)
	patch        func(string, string, string, string) error
}

func runDeploymentDeviceRootIngressTrustWithOps(args []string, ops deviceRootIngressOps) error {
	fs := flag.NewFlagSet("deployment device-root-ingress-trust", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "selected stack name for mutation")
	readOnly := fs.Bool("read-only", false, "verify the live client CA and depth without changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" {
		return errors.New("--environment is required and positional arguments are not accepted")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" || cfg.Environment != "dev" {
		return errors.New("targeted Device Root ingress update requires the existing dev LKE stack")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	envRoot, err := loadLKEImageEnv(cfg.Workspace, store.Root)
	if err != nil {
		return err
	}
	env := appendMap(envRoot.Values, cfg.Values)
	env = appendMap(env, cfg.AdapterValues)
	env = appendMap(env, cfg.AdapterResolved)
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("resolved Device Root runtime does not match the selected environment")
	}
	operatorValues, err := store.readOperator()
	if err != nil {
		return err
	}
	for _, key := range []string{"PKI_DEVICE_ROOT_ID", "PKI_DEVICE_ROOT_SHA256"} {
		env[key] = operatorValues[key]
	}
	root, err := loadPinnedDeviceIngressRoot(provisionPaths{EnvRoot: store.Root}, env)
	if err != nil {
		return err
	}
	if root == "" {
		return errors.New("selected environment has no pinned Device Root")
	}
	if *confirm == "" && !*readOnly {
		fmt.Fprintf(os.Stdout, "Device Root ingress trust plan: environment=%s stack=%s Root=%s depth=3 (additive public CA only)\n", cfg.Environment, env["CLOUD_STACK_NAME"], env["PKI_DEVICE_ROOT_SHA256"])
		fmt.Fprintln(os.Stdout, "Apply requires the existing device mTLS ingress, unchanged legacy CAs, and --confirm STACK.")
		return nil
	}
	if (*readOnly && *confirm != "") || (!*readOnly && *confirm != env["CLOUD_STACK_NAME"]) {
		return errors.New("--confirm must match the selected stack")
	}
	restore, err := ops.credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if err := ops.requireRoute(env); err != nil {
		return err
	}
	namespace := lkeIngressNamespace(env)
	secretName := lkeDeviceMTLSAppCASecretName(env)
	secret, err := ops.get(namespace, "secret", secretName)
	if err != nil {
		return err
	}
	patch, already, err := deviceRootIngressSecretPatch(secret, root)
	if err != nil {
		return err
	}
	ingressName := "video-cloud-staging-device-mtls"
	ingress, err := ops.get(namespace, "ingress", ingressName)
	if err != nil {
		return err
	}
	depthPatch, depthReady, err := deviceRootIngressDepthPatch(env, ingress)
	if err != nil {
		return err
	}
	if *readOnly {
		if !already || !depthReady {
			return fmt.Errorf("device ingress lacks pinned Product Device Root trust: CA_ready=%t depth_ready=%t", already, depthReady)
		}
		fmt.Fprintf(os.Stdout, "PASS device ingress Root=%s depth=3\n", env["PKI_DEVICE_ROOT_SHA256"])
		return nil
	}
	if !already {
		if err := ops.patch(namespace, "secret", secretName, patch); err != nil {
			return fmt.Errorf("add pinned Device Root to ingress CA bundle: %w", err)
		}
	}
	secret, err = ops.get(namespace, "secret", secretName)
	if err != nil {
		return err
	}
	if _, complete, err := deviceRootIngressSecretPatch(secret, root); err != nil || !complete {
		return fmt.Errorf("Device Root ingress CA read-back is incomplete: %v", err)
	}
	if !depthReady {
		if err := ops.patch(namespace, "ingress", ingressName, depthPatch); err != nil {
			return fmt.Errorf("set Product device ingress verification depth: %w", err)
		}
	}
	ingress, err = ops.get(namespace, "ingress", ingressName)
	if err != nil {
		return err
	}
	if _, complete, err := deviceRootIngressDepthPatch(env, ingress); err != nil || !complete {
		return fmt.Errorf("Device Root ingress depth read-back is incomplete: %v", err)
	}
	return ops.requireRoute(env)
}

func deviceRootIngressSecretPatch(secret map[string]any, root string) (string, bool, error) {
	meta, _ := secret["metadata"].(map[string]any)
	version, _ := meta["resourceVersion"].(string)
	data, _ := secret["data"].(map[string]any)
	encoded, _ := data["ca.crt"].(string)
	if version == "" || encoded == "" || secret["type"] != "Opaque" {
		return "", false, errors.New("device ingress CA Secret has unexpected metadata or data")
	}
	current, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false, errors.New("device ingress CA Secret is not valid base64")
	}
	want, err := x509CertificatesFromPEM([]byte(root))
	if err != nil || len(want) != 1 {
		return "", false, errors.New("pinned Device Root is not one PEM certificate")
	}
	before, err := x509CertificatesFromPEM(current)
	if err != nil || len(before) == 0 {
		return "", false, errors.New("existing ingress CA bundle is invalid")
	}
	rootDigest := sha256.Sum256(want[0].Raw)
	for _, cert := range before {
		fingerprint := sha256.Sum256(cert.Raw)
		if fingerprint == rootDigest {
			return "", true, nil
		}
	}
	after := append([]byte(strings.TrimSpace(string(current))+"\n"), []byte(root)...)
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": "/data/ca.crt", "value": encoded},
		{"op": "replace", "path": "/data/ca.crt", "value": base64.StdEncoding.EncodeToString(after)},
	}
	raw, err := json.Marshal(operations)
	return string(raw), false, err
}

func x509CertificatesFromPEM(raw []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	for len(strings.TrimSpace(string(raw))) != 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("invalid certificate PEM block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return nil, errors.New("ingress trust bundle contains a non-CA certificate")
		}
		certificates = append(certificates, cert)
		raw = rest
	}
	return certificates, nil
}

func deviceRootIngressDepthPatch(env map[string]string, ingress map[string]any) (string, bool, error) {
	meta, _ := ingress["metadata"].(map[string]any)
	version, _ := meta["resourceVersion"].(string)
	annotations, _ := meta["annotations"].(map[string]any)
	depth, _ := annotations["nginx.ingress.kubernetes.io/auth-tls-verify-depth"].(string)
	if version == "" || annotations["nginx.ingress.kubernetes.io/auth-tls-verify-client"] != "on" ||
		annotations["nginx.ingress.kubernetes.io/auth-tls-secret"] != lkeIngressNamespace(env)+"/"+lkeDeviceMTLSAppCASecretName(env) ||
		(depth != "2" && depth != "3") {
		return "", false, errors.New("device ingress mTLS policy differs from the reviewed policy")
	}
	spec, _ := ingress["spec"].(map[string]any)
	rules, _ := spec["rules"].([]any)
	if spec["ingressClassName"] != "nginx" || len(rules) != 1 {
		return "", false, errors.New("device ingress class or host layout changed")
	}
	rule, _ := rules[0].(map[string]any)
	if rule["host"] != otaDeviceHost(env) {
		return "", false, errors.New("device ingress host differs from selected environment")
	}
	snippet, _ := annotations["nginx.ingress.kubernetes.io/configuration-snippet"].(string)
	if !strings.Contains(snippet, "proxy_set_header X-Client-Verify $ssl_client_verify;") ||
		!strings.Contains(snippet, "proxy_set_header X-Client-Cert $ssl_client_escaped_cert;") {
		return "", false, errors.New("device ingress does not forward the verified client certificate")
	}
	if depth == "3" {
		return "", true, nil
	}
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": "/metadata/annotations/nginx.ingress.kubernetes.io~1auth-tls-verify-depth", "value": "2"},
		{"op": "replace", "path": "/metadata/annotations/nginx.ingress.kubernetes.io~1auth-tls-verify-depth", "value": "3"},
	}
	raw, err := json.Marshal(operations)
	return string(raw), false, err
}

func pinnedDeviceRootFingerprint(root string) (string, error) {
	certificates, err := x509CertificatesFromPEM([]byte(root))
	if err != nil || len(certificates) != 1 {
		return "", errors.New("invalid pinned Device Root")
	}
	digest := sha256.Sum256(certificates[0].Raw)
	return hex.EncodeToString(digest[:]), nil
}
