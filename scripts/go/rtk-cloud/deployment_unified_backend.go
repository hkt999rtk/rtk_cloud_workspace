package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentKubernetesBackend struct {
	Store             secretStore
	KubeconfigContent []byte
}

// Kubernetes inventories can be larger than one resource, but subprocess output
// must not grow without a bound while private material is being captured.
type environmentAPIOutput struct{ buffer bytes.Buffer }

func (b *environmentAPIOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 32<<20 {
		return 0, errors.New("deployment.access: Kubernetes response exceeds the inventory bound")
	}
	return b.buffer.Write(p)
}

// Freeze the single selected API/authentication identity. External credential
// files, token refresh commands and alternate contexts would introduce inputs
// that were not hashed with the approved plan.
func newEnvironmentKubernetesBackend(raw, environment string) (environmentKubernetesBackend, error) {
	if !secretEnvironmentPattern.MatchString(environment) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: explicit selected environment required")
	}
	var config struct {
		Version  string `yaml:"apiVersion"`
		Kind     string `yaml:"kind"`
		Current  string `yaml:"current-context"`
		Clusters []struct {
			Name    string         `yaml:"name"`
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string         `yaml:"name"`
			User map[string]any `yaml:"user"`
		} `yaml:"users"`
		Contexts []struct {
			Name    string         `yaml:"name"`
			Context map[string]any `yaml:"context"`
		} `yaml:"contexts"`
	}
	if len(raw) == 0 || len(raw) > 4<<20 || yaml.Unmarshal([]byte(raw), &config) != nil || config.Version != "v1" || config.Kind != "Config" || config.Current == "" {
		return environmentKubernetesBackend{}, errors.New("deployment.access: a bounded selected Kubernetes context is required")
	}
	var clusterName, userName string
	var selectedContext, cluster, user map[string]any
	for _, entry := range config.Contexts {
		if entry.Name == config.Current {
			if selectedContext != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes context")
			}
			selectedContext = entry.Context
			clusterName = deploymentplan.String(entry.Context["cluster"])
			userName = deploymentplan.String(entry.Context["user"])
		}
	}
	for _, entry := range config.Clusters {
		if entry.Name == clusterName {
			if cluster != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes cluster")
			}
			cluster = entry.Cluster
		}
	}
	for _, entry := range config.Users {
		if entry.Name == userName {
			if user != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes authentication identity")
			}
			user = entry.User
		}
	}
	endpoint, err := url.Parse(deploymentplan.String(cluster["server"]))
	if selectedContext == nil || clusterName == "" || userName == "" || cluster == nil || user == nil || err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || deploymentplan.String(cluster["certificate-authority-data"]) == "" {
		return environmentKubernetesBackend{}, errors.New("deployment.access: selected HTTPS cluster and embedded CA/authentication identity are required")
	}
	for field := range cluster {
		if !keySet("server", "certificate-authority-data", "tls-server-name")[field] {
			return environmentKubernetesBackend{}, errors.New("deployment.access: external or insecure Kubernetes transport inputs cannot be frozen")
		}
	}
	// Provider kubeconfigs can serialize an unused impersonation map. An empty
	// map adds no authentication input; nonempty impersonation stays unsupported.
	if extra, ok := user["as-user-extra"].(map[string]any); ok && len(extra) == 0 {
		delete(user, "as-user-extra")
	}
	for field := range user {
		if !keySet("token", "client-certificate-data", "client-key-data")[field] {
			return environmentKubernetesBackend{}, &deploymentplan.Blocker{ID: "deployment.access.kubeconfig", Cause: "selected kubeconfig contains unsupported authentication or impersonation fields", Next: "use embedded token or client certificate/key credentials; external credential files, refresh commands, authentication providers and nonempty impersonation settings cannot be frozen for this plan"}
		}
	}
	token := deploymentplan.String(user["token"])
	cert, key := deploymentplan.String(user["client-certificate-data"]), deploymentplan.String(user["client-key-data"])
	if (token == "") == (cert == "" || key == "") || ((cert == "") != (key == "")) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: one complete embedded Kubernetes authentication method is required")
	}
	if err := validateEnvironmentKubernetesCertificates(deploymentplan.String(cluster["certificate-authority-data"]), cert, key, time.Now()); err != nil {
		return environmentKubernetesBackend{}, err
	}
	if token != "" && (strings.TrimSpace(token) != token || strings.ContainsAny(token, "\x00\r\n\t ")) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: embedded Kubernetes token is malformed")
	}
	frozen := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": config.Current, "contexts": []any{map[string]any{"name": config.Current, "context": selectedContext}}, "clusters": []any{map[string]any{"name": clusterName, "cluster": cluster}}, "users": []any{map[string]any{"name": userName, "user": user}}}
	encoded, err := yaml.Marshal(frozen)
	if err != nil {
		return environmentKubernetesBackend{}, errors.New("deployment.access: cannot freeze selected Kubernetes identity")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return environmentKubernetesBackend{}, errors.New("deployment.access: selected environment storage unavailable")
	}
	return environmentKubernetesBackend{Store: store, KubeconfigContent: encoded}, nil
}

func validateEnvironmentKubernetesCertificates(ca, clientCertificate, clientKey string, now time.Time) error {
	raw, err := base64.StdEncoding.Strict().DecodeString(ca)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("deployment.access: embedded Kubernetes CA bundle is malformed")
	}
	count := 0
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("deployment.access: embedded Kubernetes CA bundle has unexpected content")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return errors.New("deployment.access: embedded Kubernetes CA is invalid or outside its validity period")
		}
		count++
		raw = rest
	}
	if count == 0 {
		return errors.New("deployment.access: embedded Kubernetes CA bundle is empty")
	}
	if clientCertificate == "" && clientKey == "" {
		return nil
	}
	certBytes, certErr := base64.StdEncoding.Strict().DecodeString(clientCertificate)
	keyBytes, keyErr := base64.StdEncoding.Strict().DecodeString(clientKey)
	if certErr != nil || keyErr != nil || len(certBytes) > 1<<20 || len(keyBytes) > 1<<20 {
		return errors.New("deployment.access: embedded Kubernetes client identity is malformed")
	}
	pair, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("deployment.access: embedded Kubernetes client certificate and key do not match")
	}
	for _, raw := range pair.Certificate {
		certificate, err := x509.ParseCertificate(raw)
		if err != nil || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return errors.New("deployment.access: embedded Kubernetes client chain is outside its validity period")
		}
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if leaf.IsCA || leaf.Subject.CommonName == "" {
		return errors.New("deployment.access: embedded Kubernetes client leaf lacks an authenticated subject")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		allowed := false
		for _, usage := range leaf.ExtKeyUsage {
			allowed = allowed || usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny
		}
		if !allowed {
			return errors.New("deployment.access: embedded Kubernetes client leaf is not permitted for client authentication")
		}
	}
	return nil
}

func (b environmentKubernetesBackend) privateKubeconfig() (string, func(), error) {
	if len(b.KubeconfigContent) == 0 {
		return "", nil, errors.New("deployment.access: frozen Kubernetes identity is absent")
	}
	store := b.Store
	if !filepath.IsAbs(store.Root) || !secretEnvironmentPattern.MatchString(store.Environment) {
		return "", nil, errors.New("deployment.access: frozen environment store unavailable")
	}
	temporaryRoot := filepath.Join(store.Root, "deployment", "tmp")
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), temporaryRoot} {
		if err := ensurePrivateDirectory(path); err != nil {
			return "", nil, errors.New("deployment.access: private environment temporary directory unavailable")
		}
	}
	directory, err := os.MkdirTemp(temporaryRoot, "kubernetes-api-*")
	if err != nil {
		return "", nil, errors.New("deployment.access: cannot prepare private API identity")
	}
	cleanup := func() { os.RemoveAll(directory) }
	path := filepath.Join(directory, "kubeconfig.yaml")
	if err := os.WriteFile(path, b.KubeconfigContent, 0600); err != nil {
		cleanup()
		return "", nil, errors.New("deployment.access: cannot prepare private API identity")
	}
	return path, cleanup, nil
}

// Admission errors can contain Secret values. Only sanitized operation and
// resource identities cross this boundary; private payloads stay in memory.
func (b environmentKubernetesBackend) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path, cleanup, err := b.privateKubeconfig()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	argv := append([]string{"--kubeconfig", path, "--request-timeout=90s"}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", argv...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(input)
	var stdout environmentAPIOutput
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("selected Kubernetes API operation failed; inspect sanitized admission events with environment-local access")
	}
	return stdout.buffer.Bytes(), nil
}

func environmentResourceArguments(id string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, errors.New("invalid Kubernetes resource identity")
	}
	args := []string{parts[0], parts[2]}
	if parts[1] != "" {
		args = append(args, "--namespace", parts[1])
	}
	return args, nil
}

func (b environmentKubernetesBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	args, err := environmentResourceArguments(id)
	if err != nil {
		return nil, err
	}
	args = append([]string{"get"}, args...)
	args = append(args, "--ignore-not-found=true", "-o", "json")
	raw, err := b.command(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var object deploymentplan.Object
	if json.Unmarshal(raw, &object) != nil || deploymentplan.ID(object) != id {
		return nil, errors.New("Kubernetes resource identity does not match the selected owner")
	}
	return object, nil
}

func (b environmentKubernetesBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action == "retire" {
		return b.retire(ctx, body, dry)
	}
	verb := "replace"
	if action == "create" {
		verb = "create"
	} else if action != "update" {
		return nil, errors.New("unsupported Kubernetes mutation")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("cannot encode selected Kubernetes resource")
	}
	args := []string{verb, "-f", "-", "-o", "json"}
	if dry {
		args = append(args, "--dry-run=server")
	}
	raw, err = b.command(ctx, raw, args...)
	if err != nil {
		return nil, err
	}
	var object deploymentplan.Object
	if json.Unmarshal(raw, &object) != nil || deploymentplan.ID(object) != deploymentplan.ID(body) {
		return nil, errors.New("Kubernetes mutation returned an unexpected resource")
	}
	return object, nil
}

func (b environmentKubernetesBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	parts := strings.Split(resource.ID, "/")
	args := []string{"--namespace", parts[1]}
	if resource.Action == "retire" {
		args = append(args, "wait", "--for=delete", parts[0]+"/"+parts[2], "--timeout=90s")
		_, err := b.command(ctx, nil, args...)
		return err
	}
	if environmentOnDeleteUpdate(resource) {
		return b.waitOnDeleteOpenBao(ctx, resource)
	}
	switch parts[0] {
	case "Deployment", "StatefulSet", "DaemonSet":
		args = append(args, "rollout", "status", parts[0]+"/"+parts[2], "--timeout=90s")
	case "Job":
		args = append(args, "wait", "--for=condition=complete", "Job/"+parts[2], "--timeout=90s")
	case "PersistentVolumeClaim":
		args = append(args, "wait", "--for=jsonpath={.status.phase}=Bound", "PersistentVolumeClaim/"+parts[2], "--timeout=90s")
	default:
		return nil
	}
	_, err := b.command(ctx, nil, args...)
	return err
}

func (b environmentKubernetesBackend) Inventory(ctx context.Context, stack string) ([]deploymentplan.Object, error) {
	raw, err := b.command(ctx, nil, "get", "namespace", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("cannot decode namespace inventory")
	}
	var resources []deploymentplan.Object
	for _, namespace := range list.Items {
		name := deploymentplan.String(deploymentplan.Map(namespace["metadata"])["name"])
		if !strings.HasPrefix(name, stack+"-") {
			continue
		}
		resources = append(resources, namespace)
		raw, err = b.command(ctx, nil, "get", environmentNamespacedInventoryKinds, "--namespace", name, "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("deployment.inventory: cannot enumerate resources in %s", name)
		}
		list.Items = nil
		if json.Unmarshal(raw, &list) != nil {
			return nil, errors.New("cannot decode complete environment inventory")
		}
		resources = append(resources, list.Items...)
	}
	raw, err = b.command(ctx, nil, "get", environmentClusterInventoryKinds, "-l", "rtk.realtek.com/stack="+stack, "-o", "json")
	if err != nil {
		return nil, err
	}
	list.Items = nil
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("cannot decode cluster ownership inventory")
	}
	return append(resources, list.Items...), nil
}

func (b environmentKubernetesBackend) retire(ctx context.Context, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	meta := deploymentplan.Map(body["metadata"])
	uid, rv := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	kind := deploymentplan.String(body["kind"])
	api, plural := "", ""
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		api = "apis/apps/v1"
		plural = strings.ToLower(kind) + "s"
	case "Job", "CronJob":
		api = "apis/batch/v1"
		plural = strings.ToLower(kind) + "s"
	case "Pod", "Service", "ServiceAccount":
		api = "api/v1"
		plural = strings.ToLower(kind) + "s"
	case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		api = "apis/rbac.authorization.k8s.io/v1"
		plural = strings.ToLower(kind) + "s"
	case "NetworkPolicy":
		api = "apis/networking.k8s.io/v1"
		plural = "networkpolicies"
	case "Ingress":
		api = "apis/networking.k8s.io/v1"
		plural = "ingresses"
	case "IngressClass":
		api = "apis/networking.k8s.io/v1"
		plural = "ingressclasses"
	case "ValidatingWebhookConfiguration":
		api = "apis/admissionregistration.k8s.io/v1"
		plural = "validatingwebhookconfigurations"
	case "PodDisruptionBudget":
		api = "apis/policy/v1"
		plural = "poddisruptionbudgets"
	case "HorizontalPodAutoscaler":
		api = "apis/autoscaling/v2"
		plural = "horizontalpodautoscalers"
	default:
		return nil, errors.New("resource needs its separate durable-data or PKI retirement lifecycle")
	}
	if uid == "" || rv == "" {
		return nil, errors.New("retirement requires exact UID and current resource version")
	}
	path := "/" + api + "/"
	if namespace := deploymentplan.String(meta["namespace"]); namespace != "" {
		path += "namespaces/" + url.PathEscape(namespace) + "/"
	}
	path += plural + "/" + url.PathEscape(deploymentplan.String(meta["name"]))
	if dry {
		path += "?dryRun=All"
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "preconditions": map[string]string{"uid": uid, "resourceVersion": rv}})
	if _, err := b.command(ctx, raw, "delete", "--raw="+path, "-f", "-"); err != nil {
		return nil, err
	}
	return body, nil
}
