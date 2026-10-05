package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentCertIssuerReadinessUsesDerivedEnvironmentAndDoesNotMutate(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	store, err := newSecretStore("", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.Root, "operator", "env"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.KubeconfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.KubeconfigPath(), []byte("fixture-kubeconfig"), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := fakeKubectl(t)
	material := fakeKubectlCertIssuerServing(t)
	if err := replaceLKECertIssuerMaterial(filepath.Join(store.Root, "pki", "certissuer"), material); err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Adapter: "lke", Environment: "staging", RuntimeRoot: t.TempDir(), Workspace: t.TempDir(), Values: map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}}
	var output bytes.Buffer
	if err := deploymentCertIssuerIngressReadiness(context.Background(), cfg, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "owned legacy route migration are deployable") {
		t.Fatalf("derived public route was not checked: %s", output.String())
	}
	if raw, _ := os.ReadFile(logPath); len(raw) != 0 {
		t.Fatalf("read-only planning changed resources: %s", raw)
	}
	if _, err := os.Stat(certIssuerIngressJournalDir(provisionPaths{EnvRoot: cfg.RuntimeRoot})); !os.IsNotExist(err) {
		t.Fatalf("read-only planning wrote a journal: %v", err)
	}
}

func TestDeploymentCertIssuerReadinessNewEnvironmentAndInvalidPolicy(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	cfg := deploymentConfig{Adapter: "lke", Environment: "staging", RuntimeRoot: t.TempDir(), Values: map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}}
	var output bytes.Buffer
	if err := deploymentCertIssuerIngressReadinessWithClusterState(context.Background(), cfg, &output, true); err != nil || !strings.Contains(output.String(), "new deployment") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
	if err := deploymentCertIssuerIngressReadiness(context.Background(), cfg, &output); err == nil || !strings.Contains(err.Error(), "kubeconfig is missing") {
		t.Fatalf("missing local access was mistaken for a new cluster: %v", err)
	}
	cfg.Values = map[string]string{"CLOUD_DNS_ROOT_DOMAIN": "invalid/host"}
	if err := deploymentCertIssuerIngressReadiness(context.Background(), cfg, &output); err == nil {
		t.Fatal("malformed desired hostname passed planning")
	}
}

func TestLKEDeployOnlyConvergesCertIssuerRoutingAndRepeatPreservesIngress(t *testing.T) {
	logPath := fakeKubectl(t)
	fakeHelm(t)
	ctx := provisionContext{Env: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-staging.realtekconnect.com"}, Paths: provisionPaths{EnvRoot: t.TempDir()}, Opts: provisionOptions{mode: provisionMode{deploy: true}}}
	if !lkeCertIssuerRoutingSelected(ctx) {
		t.Fatal("deploy-only update omitted routing")
	}
	for range 2 {
		if err := lkeDeployCertIssuerPublicIngress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	raw := readTestFile(t, logPath)
	if strings.Count(raw, "ARGS create -f - -o json") != 1 || strings.Contains(raw, "ARGS replace") {
		t.Fatalf("repeated deployment changed the converged ingress:\n%s", raw)
	}
	if strings.Contains(raw, "kind: Ingress\nmetadata:\n  name: video-cloud-staging-public") {
		t.Fatal("deploy-only update reconciled unrelated public ingress")
	}
	if !strings.Contains(raw, "name: allow-certissuer-public-mtls") || strings.Contains(raw, "name: default-deny-ingress") || strings.Contains(raw, "name: allow-postgres-clients") {
		t.Fatalf("deploy-only routing must grant only its own public upstream access: %s", raw)
	}
}

func TestLKEFullDeploymentRefusesManagedPKIBeforeResourceMutation(t *testing.T) {
	logPath := fakeKubectl(t)
	var deployment certIssuerIngressObject
	if err := json.Unmarshal([]byte(os.Getenv("FAKE_CERTISSUER_DEPLOYMENT_JSON")), &deployment); err != nil {
		t.Fatal(err)
	}
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	container := certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])
	container["env"] = append(certIssuerObjectList(container["env"]), map[string]any{"name": "CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED", "value": "true"})
	raw, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CERTISSUER_DEPLOYMENT_JSON", string(raw))
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-staging.realtekconnect.com"}
	if err := lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "would overwrite managed PKI") {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw, _ := os.ReadFile(logPath); len(raw) != 0 {
		t.Fatalf("managed guard ran after mutation: %s", raw)
	}
}
