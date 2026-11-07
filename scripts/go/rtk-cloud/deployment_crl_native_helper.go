package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
	"strings"
	"time"
)

// This short-lived helper uses the original controller's database reference.
// Offline signing keys stay on the operator host; no service mTLS is required.
type crlMaintenanceHelper struct {
	kubeconfig, namespace, name, uid string
}

func crlMaintenanceHelperManifest(cfg deploymentConfig, image string, controller []byte) ([]byte, error) {
	if cfg.Environment != "dev" && cfg.Environment != "staging" && cfg.Environment != "prod" {
		return nil, errors.New("unsupported CRL maintenance environment")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`).MatchString(image) {
		return nil, errors.New("CRL helper requires a digest-pinned image")
	}
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string           `json:"name"`
						Env  []map[string]any `json:"env"`
					} `json:"containers"`
					ImagePullSecrets []map[string]string `json:"imagePullSecrets"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(controller, &deployment); err != nil {
		return nil, errors.New("cannot decode original controller deployment")
	}
	var database map[string]any
	environmentMatches := false
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name != "pki-controller" {
			continue
		}
		for _, env := range c.Env {
			switch env["name"] {
			case "PKI_DATABASE_URL":
				database = env
			case "PKI_ENVIRONMENT":
				environmentMatches = env["value"] == cfg.Environment
			}
		}
	}
	if !environmentMatches || database == nil {
		return nil, errors.New("original controller environment or database binding is unavailable")
	}
	ref, ok := database["valueFrom"].(map[string]any)
	if !ok || ref["secretKeyRef"] == nil {
		return nil, errors.New("original controller database must use its existing Secret reference")
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if stack == "" {
		return nil, errors.New("selected environment stack is missing")
	}
	return json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"generateName": "crl-maintenance-", "namespace": stack + "-video-cloud", "labels": map[string]string{"app.kubernetes.io/name": "crl-maintenance", "rtk.cloud/environment": cfg.Environment}},
		"spec": map[string]any{
			"restartPolicy": "Never", "activeDeadlineSeconds": 1800, "terminationGracePeriodSeconds": 1, "automountServiceAccountToken": false,
			"imagePullSecrets": deployment.Spec.Template.Spec.ImagePullSecrets,
			"securityContext":  map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
			"volumes":          []any{map[string]any{"name": "work", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Mi"}}},
			"containers": []any{map[string]any{
				"name": "maintenance", "image": image, "imagePullPolicy": "IfNotPresent", "command": []string{"/bin/sh", "-ec", "umask 077; mkdir /work/input; sleep 1800"},
				"env":             []any{database, map[string]string{"name": "PKI_ENVIRONMENT", "value": cfg.Environment}},
				"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
				"volumeMounts":    []any{map[string]any{"name": "work", "mountPath": "/work"}},
				"readinessProbe":  map[string]any{"exec": map[string]any{"command": []string{"/bin/sh", "-ec", "test -d /work/input && test -x /app/pkicontroller"}}, "initialDelaySeconds": 1, "periodSeconds": 2},
			}},
		},
	})
}

func crlMaintenanceKubectl(ctx context.Context, kubeconfig string, input []byte, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bounded, "kubectl", append([]string{"--kubeconfig", kubeconfig, "--request-timeout=30s"}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Native errors may include a DSN. Do not echo untrusted command output.
		return nil, fmt.Errorf("CRL maintenance Kubernetes command failed: %w", err)
	}
	return stdout.Bytes(), nil
}

func newCRLMaintenanceHelper(ctx context.Context, cfg deploymentConfig, kubeconfig, image string) (*crlMaintenanceHelper, error) {
	return newCRLMaintenanceHelperWithManifest(ctx, cfg, kubeconfig, image, crlMaintenanceHelperManifest)
}

func newCRLMaintenanceHelperWithManifest(ctx context.Context, cfg deploymentConfig, kubeconfig, image string, build func(deploymentConfig, string, []byte) ([]byte, error)) (*crlMaintenanceHelper, error) {
	h := &crlMaintenanceHelper{kubeconfig: kubeconfig, namespace: cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}
	controller, err := crlMaintenanceKubectl(ctx, kubeconfig, nil, "-n", h.namespace, "get", "deployment", "pki-controller", "-o", "json")
	if err != nil {
		return nil, err
	}
	manifest, err := build(cfg, image, controller)
	if err != nil {
		return nil, err
	}
	raw, err := crlMaintenanceKubectl(ctx, kubeconfig, manifest, "create", "-f", "-", "-o", "json")
	if err != nil {
		return nil, err
	}
	var pod struct{ Metadata struct{ Name, UID string } }
	if err = json.Unmarshal(raw, &pod); err != nil {
		return nil, errors.New("cannot decode created CRL helper identity")
	}
	h.name, h.uid = pod.Metadata.Name, pod.Metadata.UID
	if h.name == "" || h.uid == "" {
		return nil, errors.New("created CRL helper identity missing")
	}
	if _, err = crlMaintenanceKubectl(ctx, kubeconfig, nil, "-n", h.namespace, "wait", "--for=condition=Ready", "pod/"+h.name, "--timeout=120s"); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = h.Close(cleanup)
		return nil, err
	}
	return h, nil
}

func validCRLMaintenanceHelperFile(name string) bool {
	return name != "." && name != ".." && filepath.Base(name) == name && regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`).MatchString(name)
}

func (h *crlMaintenanceHelper) verifyPod(ctx context.Context) error {
	raw, err := crlMaintenanceKubectl(ctx, h.kubeconfig, nil, "-n", h.namespace, "get", "pod", h.name, "-o", "json")
	if err != nil {
		return err
	}
	var pod struct{ Metadata struct{ UID string } }
	if json.Unmarshal(raw, &pod) != nil || pod.Metadata.UID != h.uid || h.uid == "" {
		return errors.New("original CRL helper Pod identity changed")
	}
	return nil
}

func (h *crlMaintenanceHelper) Run(ctx context.Context, args []string, files map[string][]byte) ([]byte, error) {
	if len(args) == 0 || (args[0] != "recovery-import-root-crl" && args[0] != "recovery-export-root-crl" && args[0] != "recovery-renew-online-crl") {
		return nil, errors.New("unsupported CRL helper command")
	}
	names := make([]string, 0, len(files))
	for name, data := range files {
		if !validCRLMaintenanceHelperFile(name) || len(data) > 4<<20 {
			return nil, errors.New("invalid CRL helper public input")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := h.verifyPod(ctx); err != nil {
			return nil, err
		}
		// The path is positional, never interpolated into shell source.
		if _, err := crlMaintenanceKubectl(ctx, h.kubeconfig, files[name], "-n", h.namespace, "exec", "-i", h.name, "-c", "maintenance", "--", "/bin/sh", "-ec", `umask 077; cat > "$1"`, "write-public-input", "/work/"+name); err != nil {
			return nil, err
		}
	}
	if err := h.verifyPod(ctx); err != nil {
		return nil, err
	}
	return crlMaintenanceKubectl(ctx, h.kubeconfig, nil, append([]string{"-n", h.namespace, "exec", h.name, "-c", "maintenance", "--", "/app/pkicontroller"}, args...)...)
}

func (h *crlMaintenanceHelper) Close(ctx context.Context) error {
	if h == nil || h.name == "" || h.uid == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": h.uid}, "gracePeriodSeconds": 0})
	endpoint := "/api/v1/namespaces/" + url.PathEscape(h.namespace) + "/pods/" + url.PathEscape(h.name)
	_, err := crlMaintenanceKubectl(ctx, h.kubeconfig, body, "delete", "--raw", endpoint, "-f", "-")
	if err == nil {
		h.name = ""
		h.uid = ""
	}
	return err
}

// Online recovery uses the controller's existing short-lived Kubernetes role.
// It carries only the original CA projection and explicitly-audienced auth token.
func crlMaintenanceOnlineHelperManifest(cfg deploymentConfig, image string, controller []byte) ([]byte, error) {
	raw, err := crlMaintenanceHelperManifest(cfg, image, controller)
	if err != nil {
		return nil, err
	}
	var original, helper map[string]any
	if json.Unmarshal(controller, &original) != nil || json.Unmarshal(raw, &helper) != nil {
		return nil, errors.New("invalid online recovery helper source")
	}
	spec := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(original["spec"])["template"])["spec"])
	target := deploymentplan.Map(helper["spec"])
	containers := certIssuerObjectList(target["containers"])
	container := deploymentplan.Map(containers[0])
	var source deploymentplan.Object
	for _, c := range certIssuerObjectList(spec["containers"]) {
		v := deploymentplan.Map(c)
		if v["name"] == "pki-controller" {
			source = v
		}
	}
	required := []string{"OPENBAO_ADDR", "OPENBAO_AUTH_METHOD", "OPENBAO_CACERT", "OPENBAO_KUBERNETES_ROLE", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE", "OPENBAO_SERVER_PKI_ROOT_SHA256"}
	values := map[string]string{}
	env := certIssuerObjectList(container["env"])
	for _, name := range required {
		value := mqttFoundationEnv(source, name)
		if value == "" {
			return nil, fmt.Errorf("original controller online recovery setting %s is missing", name)
		}
		values[name] = value
		env = append(env, map[string]any{"name": name, "value": value})
	}
	if values["OPENBAO_AUTH_METHOD"] != "kubernetes" || !strings.HasPrefix(values["OPENBAO_ADDR"], "https://") || !environmentPreflightDigest.MatchString(values["OPENBAO_SERVER_PKI_ROOT_SHA256"]) {
		return nil, errors.New("online recovery requires original pinned HTTPS Kubernetes auth")
	}
	mounts := certIssuerObjectList(container["volumeMounts"])
	volumes := certIssuerObjectList(target["volumes"])
	seen := map[string]bool{}
	for _, setting := range []string{"OPENBAO_CACERT", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE"} {
		path := values[setting]
		found := false
		for _, value := range certIssuerObjectList(source["volumeMounts"]) {
			mount := deploymentplan.Map(value)
			prefix := deploymentplan.String(mount["mountPath"])
			if prefix == "" || !strings.HasPrefix(path, prefix+"/") || mount["subPath"] != nil {
				continue
			}
			name := deploymentplan.String(mount["name"])
			for _, v := range certIssuerObjectList(spec["volumes"]) {
				volume := deploymentplan.Map(v)
				if volume["name"] != name {
					continue
				}
				if setting == "OPENBAO_CACERT" && volume["configMap"] == nil {
					return nil, errors.New("online recovery CA must use original ConfigMap projection")
				}
				if setting == "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE" {
					sources := certIssuerObjectList(deploymentplan.Map(volume["projected"])["sources"])
					if len(sources) != 1 || deploymentplan.Map(deploymentplan.Map(sources[0])["serviceAccountToken"])["audience"] != "openbao" {
						return nil, errors.New("online recovery token must be restricted to original OpenBao audience")
					}
				}
				if !seen[name] {
					mounts = append(mounts, mount)
					volumes = append(volumes, volume)
					seen[name] = true
				}
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("original online recovery mount unavailable for %s", setting)
		}
	}
	sa := deploymentplan.String(spec["serviceAccountName"])
	if sa == "" {
		sa = "default"
	}
	target["serviceAccountName"] = sa
	target["volumes"] = volumes
	container["env"] = env
	container["volumeMounts"] = mounts
	target["containers"] = []any{container}
	helper["spec"] = target
	return json.Marshal(helper)
}

func newCRLMaintenanceOnlineHelper(ctx context.Context, cfg deploymentConfig, kubeconfig, image string) (*crlMaintenanceHelper, error) {
	return newCRLMaintenanceHelperWithManifest(ctx, cfg, kubeconfig, image, crlMaintenanceOnlineHelperManifest)
}
