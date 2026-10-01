package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// This scoped cutover changes only the device mTLS ingress, its OTA bridge
// Service, and a pod-scoped ingress policy. It does not redeploy the core API.
func runDeploymentOTADeviceEdge(args []string) error {
	return runDeploymentOTADeviceEdgeWithCredentials(args, func(environment string) (func(), error) {
		_, restore, err := configureProvisionSecretStore(environment)
		return restore, err
	})
}

func runDeploymentOTADeviceEdgeWithCredentials(args []string, credentials func(string) (func(), error)) error {
	fs := flag.NewFlagSet("deployment ota-device-edge", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "selected stack name for mutation")
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
	if err := requireTargetedOTAEnvironment(cfg); err != nil {
		return err
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
		return errors.New("resolved OTA edge runtime does not match the selected environment")
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stdout, "OTA device edge plan: environment=%s stack=%s ingress=video-cloud-staging-device-mtls path=/v1/device/ota/\n", cfg.Environment, env["CLOUD_STACK_NAME"])
		fmt.Fprintln(os.Stdout, "Apply requires a ready independent OTA endpoint, a stopped legacy registrar, existing device mTLS ingress, and --confirm STACK.")
		return nil
	}
	if *confirm != env["CLOUD_STACK_NAME"] {
		return errors.New("--confirm must match the selected stack")
	}
	restore, err := credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if !lkeOTAServiceRegistrationEnabled(env) || !lkeOTAServiceEdgeEnabled(env) {
		return errors.New("OTA service registration and device edge flags must both be true")
	}
	if lkeOTARegistrarRegistrationEnabled(env) || lkeOTACoreCutoverEnabled(env) {
		return errors.New("OTA device edge rollout requires old registrar and core cutover flags disabled")
	}
	if !lkeOTAEntitlementsRequired(env) {
		return errors.New("OTA device edge requires strict Product entitlement checks")
	}
	if err := lkeRequireStoppedOTARegistrar(env); err != nil {
		return err
	}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		return err
	}
	ingress, err := kubectlResourceJSON(lkeIngressNamespace(env), "ingress", "video-cloud-staging-device-mtls")
	if err != nil {
		return fmt.Errorf("inspect existing device mTLS ingress: %w", err)
	}
	patch, already, err := otaDeviceEdgePatch(env, ingress)
	if err != nil {
		return err
	}
	route := lkePublicHTTPSRoute{
		Host: otaDeviceHost(env), Path: "/v1/device/ota/",
		Namespace: lkeNamespaceName(env, "video-cloud"), Service: otaServiceWorkloadName,
		ServicePort: 18084, TargetPort: 18084,
	}
	if err := kubectlApply(lkeOTADeviceEdgeNetworkPolicyManifest(env)); err != nil {
		return err
	}
	for _, manifest := range lkePublicHTTPSBridgeServiceManifests(env, []lkePublicHTTPSRoute{route}) {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	if !already {
		if err := runKubectl("-n", lkeIngressNamespace(env), "patch", "ingress", "video-cloud-staging-device-mtls", "--type=json", "-p", patch); err != nil {
			return fmt.Errorf("add OTA path to device mTLS ingress: %w", err)
		}
	}
	return lkeRequireActiveOTADeviceEdgeRoute(env)
}

func otaDeviceHost(env map[string]string) string {
	return firstNonEmpty(lkeEnvValue(env, "LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+env["VIDEO_CLOUD_DOMAIN"])
}

// The JSON patch tests resourceVersion and host so a concurrent ingress
// update cannot be silently replaced by this narrow route addition.
func otaDeviceEdgePatch(env map[string]string, ingress map[string]any) (string, bool, error) {
	metadata, ok := ingress["metadata"].(map[string]any)
	if !ok {
		return "", false, errors.New("device ingress has no metadata")
	}
	version, ok := metadata["resourceVersion"].(string)
	if !ok || version == "" {
		return "", false, errors.New("device ingress lacks resourceVersion")
	}
	annotations, ok := metadata["annotations"].(map[string]any)
	if !ok ||
		annotations["nginx.ingress.kubernetes.io/auth-tls-verify-client"] != "on" ||
		annotations["nginx.ingress.kubernetes.io/auth-tls-verify-depth"] != lkeDeviceMTLSVerifyDepth(env) ||
		annotations["nginx.ingress.kubernetes.io/auth-tls-secret"] != lkeIngressNamespace(env)+"/"+lkeDeviceMTLSAppCASecretName(env) {
		return "", false, errors.New("device ingress does not pin the expected mTLS app CA")
	}
	snippet, _ := annotations["nginx.ingress.kubernetes.io/configuration-snippet"].(string)
	if !strings.Contains(snippet, "proxy_set_header X-Client-Verify $ssl_client_verify;") ||
		!strings.Contains(snippet, "proxy_set_header X-Client-Cert $ssl_client_escaped_cert;") {
		return "", false, errors.New("device ingress does not forward the verified client certificate")
	}
	spec, ok := ingress["spec"].(map[string]any)
	if !ok || spec["ingressClassName"] != "nginx" {
		return "", false, errors.New("device ingress does not use the expected nginx class")
	}
	rules, ok := spec["rules"].([]any)
	if !ok || len(rules) != 1 {
		return "", false, errors.New("device ingress must contain one reviewed host rule")
	}
	rule, ok := rules[0].(map[string]any)
	if !ok || rule["host"] != otaDeviceHost(env) {
		return "", false, errors.New("device ingress host differs from the selected environment")
	}
	httpRule, ok := rule["http"].(map[string]any)
	if !ok {
		return "", false, errors.New("device ingress has no HTTP paths")
	}
	paths, ok := httpRule["paths"].([]any)
	if !ok || len(paths) == 0 {
		return "", false, errors.New("device ingress has no existing core route")
	}
	rootFound := false
	already := false
	bridge := lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: lkeNamespaceName(env, "video-cloud"), Service: otaServiceWorkloadName})
	for _, item := range paths {
		path, ok := item.(map[string]any)
		if !ok {
			return "", false, errors.New("device ingress contains an invalid path")
		}
		backend, _ := path["backend"].(map[string]any)
		service, _ := backend["service"].(map[string]any)
		port, _ := service["port"].(map[string]any)
		if path["path"] == "/" && path["pathType"] == "Prefix" &&
			service["name"] == lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: lkeNamespaceName(env, "video-cloud"), Service: "video-cloud-api"}) &&
			port["number"] == float64(80) {
			rootFound = true
		}
		if path["path"] == "/v1/device/ota/" {
			if already || path["pathType"] != "Prefix" || service["name"] != bridge || port["number"] != float64(18084) {
				return "", false, errors.New("device ingress OTA path is already owned by another backend")
			}
			already = true
		}
	}
	if !rootFound {
		return "", false, errors.New("device ingress lost its existing core route")
	}
	if already {
		return "", true, nil
	}
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": "/spec/rules/0/host", "value": otaDeviceHost(env)},
		{"op": "add", "path": "/spec/rules/0/http/paths/-", "value": map[string]any{
			"path": "/v1/device/ota/", "pathType": "Prefix",
			"backend": map[string]any{"service": map[string]any{
				"name": bridge, "port": map[string]any{"number": 18084},
			}},
		}},
	}
	encoded, err := json.Marshal(operations)
	if err != nil {
		return "", false, err
	}
	return string(encoded), false, nil
}

func lkeOTADeviceEdgeNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf("apiVersion: networking.k8s.io/v1\n"+
		"kind: NetworkPolicy\n"+
		"metadata:\n  name: allow-public-otaservice\n  namespace: %s\n"+
		"spec:\n  podSelector:\n    matchLabels:\n      app.kubernetes.io/name: %s\n"+
		"  policyTypes: [Ingress]\n  ingress:\n    - from:\n"+
		"        - namespaceSelector:\n            matchLabels:\n              kubernetes.io/metadata.name: %s\n"+
		"      ports:\n        - protocol: TCP\n          port: 18084\n",
		lkeNamespaceName(env, "video-cloud"), otaServiceWorkloadName, lkeIngressNamespace(env))
}
