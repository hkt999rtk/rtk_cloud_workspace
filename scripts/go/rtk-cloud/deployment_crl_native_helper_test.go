package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCRLMaintenanceHelperManifestRestrictsCredentials(t *testing.T) {
	cfg := deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}
	controller := []byte(`{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"ghcr-pull"}],"volumes":[{"name":"private-root"}],"containers":[{"name":"pki-controller","env":[{"name":"PKI_DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"original-database","key":"url"}}},{"name":"PKI_ENVIRONMENT","value":"staging"},{"name":"UNRELATED_SECRET","value":"do-not-copy"}]}]}}}}`)
	raw, err := crlMaintenanceHelperManifest(cfg, "ghcr.io/test/api@sha256:"+strings.Repeat("a", 64), controller)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, unwanted := range []string{"do-not-copy", "private-root", "hostPath", "serviceAccountName"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("copied unrelated capability %s", unwanted)
		}
	}
	if !strings.Contains(text, "original-database") || !strings.Contains(text, `"automountServiceAccountToken":false`) {
		t.Fatalf("missing original DB reference or token restriction: %s", text)
	}
}

func TestCRLMaintenanceHelperManifestRejectsWrongEnvironment(t *testing.T) {
	cfg := deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}
	for _, controller := range []string{
		`{"spec":{"template":{"spec":{"containers":[{"name":"pki-controller","env":[{"name":"PKI_DATABASE_URL","value":"secret"},{"name":"PKI_ENVIRONMENT","value":"dev"}]}]}}}}`,
		`{"spec":{"template":{"spec":{"containers":[{"name":"pki-controller","env":[{"name":"PKI_ENVIRONMENT","value":"staging"}]}]}}}}`,
	} {
		if _, err := crlMaintenanceHelperManifest(cfg, "ghcr.io/test/api@sha256:"+strings.Repeat("a", 64), []byte(controller)); err == nil {
			t.Fatal("accepted wrong scope or missing DB binding")
		}
	}
}

func TestCRLMaintenanceHelperInputValidation(t *testing.T) {
	for _, name := range []string{"../key", "/key", "a/b", "a;touch x", "."} {
		if validCRLMaintenanceHelperFile(name) {
			t.Fatalf("unsafe file %q", name)
		}
	}
	if !validCRLMaintenanceHelperFile("signed-crl.pem") {
		t.Fatal("valid public input rejected")
	}
}

func TestCRLMaintenanceHelperTransportAndUIDCleanup(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CRL_HELPER_TEST_LOG"
case "$*" in
 *'get deployment pki-controller'*) printf '%s\n' '{"spec":{"template":{"spec":{"containers":[{"name":"pki-controller","env":[{"name":"PKI_DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"original","key":"url"}}},{"name":"PKI_ENVIRONMENT","value":"staging"}]}]}}}}' ;;
 *'get pod crl-maintenance-original'*) printf '%s\n' '{"metadata":{"uid":"original-pod-uid"}}' ;;
 *'create -f -'*) cat > "$CRL_HELPER_TEST_DIR/manifest.json"; printf '%s\n' '{"metadata":{"name":"crl-maintenance-original","uid":"original-pod-uid"}}' ;;
 *'write-public-input'*) cat > "$CRL_HELPER_TEST_DIR/upload" ;;
 *'recovery-export-root-crl'*) printf '%s\n' '{"schema":"pki-root-crl-recovery/v1"}' ;;
 *'delete --raw'*) cat > "$CRL_HELPER_TEST_DIR/delete.json" ;;
 *'wait --for=condition=Ready'*) ;;
 *) exit 10 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CRL_HELPER_TEST_LOG", filepath.Join(dir, "commands"))
	t.Setenv("CRL_HELPER_TEST_DIR", dir)
	cfg := deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}
	h, err := newCRLMaintenanceHelper(context.Background(), cfg, "/selected/kubeconfig", "ghcr.io/test/api@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.Run(context.Background(), []string{"recovery-export-root-crl", "staging", "issuer", "root-pin"}, map[string][]byte{"public.json": []byte("public-input")})
	if err != nil || !strings.Contains(string(result), "pki-root-crl-recovery/v1") {
		t.Fatalf("run %s %v", result, err)
	}
	if _, err := h.Run(context.Background(), []string{"serve"}, nil); err == nil {
		t.Fatal("allowed unbounded controller startup")
	}
	h.uid = "replacement-mismatch"
	if _, err := h.Run(context.Background(), []string{"recovery-export-root-crl"}, nil); err == nil {
		t.Fatal("accepted replaced helper")
	}
	h.uid = "original-pod-uid"
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "delete.json"))
	if err != nil {
		t.Fatal(err)
	}
	var deletion struct{ Preconditions struct{ UID string } }
	if err := json.Unmarshal(raw, &deletion); err != nil || deletion.Preconditions.UID != "original-pod-uid" {
		t.Fatalf("cleanup not pinned to created pod: %s %v", raw, err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	uploaded, _ := os.ReadFile(filepath.Join(dir, "upload"))
	if string(uploaded) != "public-input" {
		t.Fatal("input changed")
	}
}

func TestCRLMaintenanceOnlineHelperKeepsOriginalScopedAuth(t *testing.T) {
	cfg := deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}
	env := []any{map[string]any{"name": "PKI_DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "original-db", "key": "url"}}}}
	for k, v := range map[string]string{"PKI_ENVIRONMENT": "staging", "OPENBAO_AUTH_METHOD": "kubernetes", "OPENBAO_ADDR": "https://original-openbao:8200", "OPENBAO_CACERT": "/original-ca/roots.pem", "OPENBAO_KUBERNETES_ROLE": "original-role", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": "/original-auth/token", "OPENBAO_SERVER_PKI_ROOT_SHA256": strings.Repeat("b", 64), "UNRELATED_SECRET": "never-copy"} {
		env = append(env, map[string]string{"name": k, "value": v})
	}
	spec := map[string]any{"serviceAccountName": "original-controller", "containers": []any{map[string]any{"name": "pki-controller", "env": env, "volumeMounts": []any{map[string]any{"name": "ca", "mountPath": "/original-ca", "readOnly": true}, map[string]any{"name": "auth", "mountPath": "/original-auth", "readOnly": true}, map[string]string{"name": "identity", "mountPath": "/private"}}}}, "volumes": []any{map[string]any{"name": "ca", "configMap": map[string]string{"name": "original-ca"}}, map[string]any{"name": "auth", "projected": map[string]any{"sources": []any{map[string]any{"serviceAccountToken": map[string]any{"audience": "openbao", "path": "token", "expirationSeconds": 3600}}}}}, map[string]any{"name": "identity", "persistentVolumeClaim": map[string]string{"claimName": "do-not-mount"}}}}
	original := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}
	raw, _ := json.Marshal(original)
	image := "ghcr.io/test/api@sha256:" + strings.Repeat("a", 64)
	result, err := crlMaintenanceOnlineHelperManifest(cfg, image, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"original-controller", "original-ca", "original-role", `"audience":"openbao"`, `"automountServiceAccountToken":false`} {
		if !strings.Contains(string(result), want) {
			t.Fatalf("missing original binding %s", want)
		}
	}
	for _, bad := range []string{"never-copy", "do-not-mount", "hostPath"} {
		if strings.Contains(string(result), bad) {
			t.Fatalf("copied unrelated authority %s", bad)
		}
	}
	for _, mutate := range []func(map[string]any){
		func(s map[string]any) { s["volumes"] = []any{} },
		func(s map[string]any) {
			s["volumes"] = []any{map[string]any{"name": "ca", "secret": map[string]string{"secretName": "replacement"}}}
		},
	} {
		var altered map[string]any
		_ = json.Unmarshal(raw, &altered)
		s := altered["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		mutate(s)
		input, _ := json.Marshal(altered)
		if _, err := crlMaintenanceOnlineHelperManifest(cfg, image, input); err == nil {
			t.Fatal("accepted changed/missing online trust projection")
		}
	}
}
