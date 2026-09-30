package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testIngressRoot(t *testing.T, id string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: id},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	raw, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})), hex.EncodeToString(digest[:])
}

func TestPinnedDeviceIngressRootRequiresExactEnvironmentPin(t *testing.T) {
	root, fingerprint := testIngressRoot(t, "device-root-id")
	paths := provisionPaths{EnvRoot: t.TempDir()}
	file := pinnedDeviceIngressRootPath(paths)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PKI_DEVICE_ROOT_ID": "device-root-id", "PKI_DEVICE_ROOT_SHA256": fingerprint}
	if got, err := loadPinnedDeviceIngressRoot(paths, env); err != nil || got != root {
		t.Fatalf("valid pinned Root = %q, %v", got, err)
	}
	env["PKI_DEVICE_ROOT_SHA256"] = strings.Repeat("0", 64)
	if _, err := loadPinnedDeviceIngressRoot(paths, env); err == nil {
		t.Fatal("different Root fingerprint was accepted")
	}
	env["PKI_DEVICE_ROOT_SHA256"] = fingerprint
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPinnedDeviceIngressRoot(paths, env); err == nil {
		t.Fatal("missing environment Root file was accepted")
	}
	if err := os.WriteFile(file, []byte(root+root), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPinnedDeviceIngressRoot(paths, env); err == nil {
		t.Fatal("multiple Root certificates were accepted")
	}
	env["PKI_DEVICE_ROOT_SHA256"] = ""
	if _, err := loadPinnedDeviceIngressRoot(paths, env); err == nil {
		t.Fatal("incomplete environment pin was accepted")
	}
	env["PKI_DEVICE_ROOT_ID"] = ""
	if got, err := loadPinnedDeviceIngressRoot(paths, env); err != nil || got != "" {
		t.Fatalf("unconfigured Root = %q, %v", got, err)
	}
}

func TestPinnedDeviceIngressRootUsesSelectedOperatorPin(t *testing.T) {
	root, fingerprint := testIngressRoot(t, "operator-root")
	paths := provisionPaths{EnvRoot: t.TempDir()}
	file := pinnedDeviceIngressRootPath(paths)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	old := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = paths.EnvRoot
	t.Cleanup(func() { activeSecretEnvironmentRoot = old })
	t.Setenv("PKI_DEVICE_ROOT_ID", "operator-root")
	t.Setenv("PKI_DEVICE_ROOT_SHA256", fingerprint)
	staleRuntime := map[string]string{
		"PKI_DEVICE_ROOT_ID":     "stale-root",
		"PKI_DEVICE_ROOT_SHA256": strings.Repeat("0", 64),
	}
	if got, err := loadPinnedDeviceIngressRoot(paths, staleRuntime); err != nil || got != root {
		t.Fatalf("operator pin did not win over stale runtime: %q, %v", got, err)
	}
	if got := lkeDeviceMTLSVerifyDepth(staleRuntime); got != "3" {
		t.Fatalf("ingress depth with operator pin = %q", got)
	}
	t.Setenv("PKI_DEVICE_ROOT_SHA256", strings.Repeat("0", 64))
	if _, err := loadPinnedDeviceIngressRoot(paths, staleRuntime); err == nil {
		t.Fatal("stale runtime pin incorrectly overrode mismatched operator pin")
	}
}

func TestDeviceRootIngressPatchPreservesLegacyCAsAndScope(t *testing.T) {
	legacy, _ := testIngressRoot(t, "legacy-root")
	root, fingerprint := testIngressRoot(t, "product-root")
	secret := map[string]any{
		"metadata": map[string]any{"resourceVersion": "17"},
		"type":     "Opaque", "data": map[string]any{"ca.crt": base64.StdEncoding.EncodeToString([]byte(legacy))},
	}
	patch, already, err := deviceRootIngressSecretPatch(secret, root)
	if err != nil || already {
		t.Fatalf("additive CA patch = %q, %t, %v", patch, already, err)
	}
	var operations []map[string]any
	if err := json.Unmarshal([]byte(patch), &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 || operations[0]["op"] != "test" || operations[1]["path"] != "/data/ca.crt" || operations[2]["op"] != "replace" {
		t.Fatalf("unsafe CA operations: %+v", operations)
	}
	newEncoded := operations[2]["value"].(string)
	newBundle, err := base64.StdEncoding.DecodeString(newEncoded)
	if err != nil || !strings.HasPrefix(string(newBundle), legacy) || !strings.Contains(string(newBundle), root) {
		t.Fatal("CA patch did not preserve the legacy Root before adding Product Root")
	}
	secret["data"].(map[string]any)["ca.crt"] = newEncoded
	if got, done, err := deviceRootIngressSecretPatch(secret, root); err != nil || !done || got != "" {
		t.Fatalf("idempotent CA patch = %q, %t, %v", got, done, err)
	}
	if got, err := pinnedDeviceRootFingerprint(root); err != nil || got != fingerprint {
		t.Fatalf("Root fingerprint = %q, %v", got, err)
	}
	secret["data"].(map[string]any)["ca.crt"] = base64.StdEncoding.EncodeToString([]byte("garbage"))
	if _, _, err := deviceRootIngressSecretPatch(secret, root); err == nil {
		t.Fatal("invalid live CA bundle was accepted")
	}
}

func TestDeviceRootIngressDepthPatchAndRendererAgree(t *testing.T) {
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	env := otaEdgeTestEnv()
	env["PKI_DEVICE_ROOT_ID"] = "device-root-id"
	env["PKI_DEVICE_ROOT_SHA256"] = strings.Repeat("a", 64)
	ingress := otaEdgeIngressFixture(env)
	patch, already, err := deviceRootIngressDepthPatch(env, ingress)
	if err != nil || already || !strings.Contains(patch, `"value":"3"`) {
		t.Fatalf("depth patch = %q, %t, %v", patch, already, err)
	}
	ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-depth"] = "3"
	if got, done, err := deviceRootIngressDepthPatch(env, ingress); err != nil || !done || got != "" {
		t.Fatalf("idempotent depth patch = %q, %t, %v", got, done, err)
	}
	if _, _, err := otaDeviceEdgePatch(env, ingress); err != nil {
		t.Fatalf("OTA edge refused pinned Product Root depth: %v", err)
	}
	if !strings.Contains(lkeDeviceMTLSIngressAnnotations(env), `auth-tls-verify-depth: "3"`) {
		t.Fatal("future full deployment would restore the old depth")
	}
	ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-secret"] = "other/ca"
	if _, _, err := deviceRootIngressDepthPatch(env, ingress); err == nil {
		t.Fatal("unexpected ingress CA Secret was accepted")
	}
}

func TestDeviceRootIngressPatchRejectsTamperedResources(t *testing.T) {
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	env := otaEdgeTestEnv()
	root, _ := testIngressRoot(t, "product-root")
	legacy, _ := testIngressRoot(t, "legacy-root")
	for _, scenario := range []struct {
		name string
		data string
		root string
	}{
		{"invalid base64", "not base64", root},
		{"invalid trust bundle", base64.StdEncoding.EncodeToString([]byte("not a certificate")), root},
		{"invalid pinned Root", base64.StdEncoding.EncodeToString([]byte(legacy)), "not a certificate"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			secret := map[string]any{
				"metadata": map[string]any{"resourceVersion": "12"}, "type": "Opaque",
				"data": map[string]any{"ca.crt": scenario.data},
			}
			if _, _, err := deviceRootIngressSecretPatch(secret, scenario.root); err == nil {
				t.Fatal("tampered CA Secret or Root accepted")
			}
		})
	}
	for _, scenario := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong ingress class", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["ingressClassName"] = "other"
		}},
		{"wrong device host", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["host"] = "other.example.test"
		}},
		{"client identity not forwarded", func(ingress map[string]any) {
			ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/configuration-snippet"] = ""
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ingress := otaEdgeIngressFixture(env)
			scenario.change(ingress)
			if _, _, err := deviceRootIngressDepthPatch(env, ingress); err == nil {
				t.Fatal("tampered device ingress accepted")
			}
		})
	}
}

func TestDeviceRootIngressRendererKeepsLegacyTrust(t *testing.T) {
	root, _ := testIngressRoot(t, "product-root")
	legacyRoot, _ := testIngressRoot(t, "legacy-root")
	legacyDevice, _ := testIngressRoot(t, "legacy-device")
	legacyApp, _ := testIngressRoot(t, "legacy-app")
	paths := provisionPaths{EnvRoot: t.TempDir()}
	if err := writeLKEDeviceClientCABundle(paths, legacyRoot, legacyDevice, legacyApp, root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(paths.EnvRoot, "state", "pki", "device-client-ca-bundle.pem"))
	if err != nil {
		t.Fatal(err)
	}
	certs, err := x509CertificatesFromPEM(raw)
	if err != nil || len(certs) != 4 ||
		!strings.HasPrefix(string(raw), legacyRoot) || !strings.Contains(string(raw), root) {
		t.Fatalf("full deployment lost legacy or Product Root trust: count=%d err=%v", len(certs), err)
	}
}

type ingressCommandFixture struct {
	args          []string
	ops           deviceRootIngressOps
	patches       []string
	secret        map[string]any
	ingress       map[string]any
	routeChecks   int
	failPatchKind string
	skipReadback  bool
}

func newIngressCommandFixture(t *testing.T) *ingressCommandFixture {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	configRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	old := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = ""
	t.Cleanup(func() { activeSecretEnvironmentRoot = old })
	store, err := newSecretStore(configRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(store.Root, "env", "stack.env"), strings.Join([]string{
		"CLOUD_ENV_NAME=dev", "CLOUD_PROVIDER=lke", "CLOUD_STACK_NAME=video-cloud-dev",
		"CLOUD_DNS_ROOT_DOMAIN=example.test", "CLOUD_REGION=us-sea", "",
	}, "\n"))
	root, fingerprint := testIngressRoot(t, "product-root")
	for name, value := range map[string]string{
		"operator/env/PKI_DEVICE_ROOT_ID":     "product-root",
		"operator/env/PKI_DEVICE_ROOT_SHA256": fingerprint,
		"pki/devices/device-root.crt":         root,
	} {
		if err := store.write(name, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	envRoot, err := loadLKEImageEnv(workspace, store.Root)
	if err != nil {
		t.Fatal(err)
	}
	env := appendMap(envRoot.Values, cfg.Values)
	env = appendMap(env, cfg.AdapterValues)
	env = appendMap(env, cfg.AdapterResolved)
	env["PKI_DEVICE_ROOT_ID"] = "product-root"
	env["PKI_DEVICE_ROOT_SHA256"] = fingerprint
	ingress := otaEdgeIngressFixture(env)
	baseEnv := appendMap(env, map[string]string{"PKI_DEVICE_ROOT_ID": "", "PKI_DEVICE_ROOT_SHA256": ""})
	routePatch, _, err := otaDeviceEdgePatch(baseEnv, ingress)
	if err != nil {
		t.Fatal(err)
	}
	var routeOps []map[string]any
	if err := json.Unmarshal([]byte(routePatch), &routeOps); err != nil {
		t.Fatal(err)
	}
	ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] =
		append(otaEdgePaths(ingress), routeOps[2]["value"])
	legacy, _ := testIngressRoot(t, "legacy-root")
	fixture := &ingressCommandFixture{
		args: []string{"--workspace", workspace, "--environment", "dev"},
		secret: map[string]any{
			"metadata": map[string]any{"resourceVersion": "1234"},
			"type":     "Opaque", "data": map[string]any{"ca.crt": base64.StdEncoding.EncodeToString([]byte(legacy))},
		},
		ingress: ingress,
	}
	fixture.ops = deviceRootIngressOps{
		credentials: func(environment string) (func(), error) {
			if environment != "dev" {
				return nil, fmt.Errorf("unexpected credential environment %s", environment)
			}
			return func() {}, nil
		},
		requireRoute: func(selected map[string]string) error {
			fixture.routeChecks++
			if selected["CLOUD_STACK_NAME"] != "video-cloud-dev" || len(otaEdgePaths(fixture.ingress)) != 2 {
				return errors.New("OTA route not active")
			}
			return nil
		},
		get: func(namespace, kind, name string) (map[string]any, error) {
			if namespace != "video-cloud-dev-ingress" {
				return nil, fmt.Errorf("unexpected namespace %s", namespace)
			}
			var source map[string]any
			switch {
			case kind == "secret" && name == "video-cloud-dev-app-client-ca":
				source = fixture.secret
			case kind == "ingress" && name == "video-cloud-staging-device-mtls":
				source = fixture.ingress
			default:
				return nil, fmt.Errorf("unexpected resource %s/%s", kind, name)
			}
			raw, err := json.Marshal(source)
			if err != nil {
				return nil, err
			}
			var copy map[string]any
			err = json.Unmarshal(raw, &copy)
			return copy, err
		},
		patch: func(namespace, kind, name, patch string) error {
			if namespace != "video-cloud-dev-ingress" || fixture.failPatchKind == kind {
				return fmt.Errorf("refused %s patch", kind)
			}
			var operations []map[string]any
			if err := json.Unmarshal([]byte(patch), &operations); err != nil {
				return err
			}
			if len(operations) != 3 || operations[0]["op"] != "test" || operations[2]["op"] != "replace" {
				return errors.New("unguarded or broad patch")
			}
			fixture.patches = append(fixture.patches, kind)
			if fixture.skipReadback {
				return nil
			}
			switch kind {
			case "secret":
				fixture.secret["data"].(map[string]any)["ca.crt"] = operations[2]["value"]
				fixture.secret["metadata"].(map[string]any)["resourceVersion"] = "1235"
			case "ingress":
				fixture.ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-depth"] = operations[2]["value"]
				fixture.ingress["metadata"].(map[string]any)["resourceVersion"] = "1235"
			default:
				return fmt.Errorf("unexpected patch kind %s", kind)
			}
			return nil
		},
	}
	return fixture
}

func TestDeviceRootIngressCommandPreflightApplyAndIdempotence(t *testing.T) {
	f := newIngressCommandFixture(t)
	if err := runDeploymentWithOperations(append([]string{"device-root-ingress-trust"}, f.args...), deploymentOperations{}); err != nil {
		t.Fatalf("deployment dispatcher plan: %v", err)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(f.args, f.ops); err != nil || f.routeChecks != 0 {
		t.Fatalf("plan consulted live resources: %v, checks=%d", err, f.routeChecks)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--read-only"), f.ops); err == nil || !strings.Contains(err.Error(), "CA_ready=false depth_ready=false") || len(f.patches) != 0 {
		t.Fatalf("read-only preflight = %v, patches=%v", err, f.patches)
	}
	applyArgs := append(f.args, "--confirm", "video-cloud-dev")
	if err := runDeploymentDeviceRootIngressTrustWithOps(applyArgs, f.ops); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.patches, ",") != "secret,ingress" || f.routeChecks != 3 {
		t.Fatalf("repair sequence = %v, route checks=%d", f.patches, f.routeChecks)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--read-only"), f.ops); err != nil {
		t.Fatalf("postflight did not see repair: %v", err)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(applyArgs, f.ops); err != nil || len(f.patches) != 2 {
		t.Fatalf("idempotent repair = %v, patches=%v", err, f.patches)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--confirm", "wrong"), f.ops); err == nil {
		t.Fatal("wrong stack confirmation accepted")
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--read-only", "--confirm", "video-cloud-dev"), f.ops); err == nil {
		t.Fatal("read-only request accepted mutation confirmation")
	}
}

func TestDeviceRootIngressCommandStopsBeforeUnsafeSecondPatch(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		configure func(*ingressCommandFixture)
		want      string
	}{
		{"route missing", func(f *ingressCommandFixture) {
			f.ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = otaEdgePaths(f.ingress)[:1]
		}, "OTA route not active"},
		{"secret patch rejected", func(f *ingressCommandFixture) { f.failPatchKind = "secret" }, "add pinned Device Root"},
		{"secret readback missing", func(f *ingressCommandFixture) { f.skipReadback = true }, "CA read-back is incomplete"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newIngressCommandFixture(t)
			scenario.configure(f)
			err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--confirm", "video-cloud-dev"), f.ops)
			if err == nil || !strings.Contains(err.Error(), scenario.want) || strings.Contains(strings.Join(f.patches, ","), "ingress") {
				t.Fatalf("unsafe second patch proceeded: err=%v patches=%v", err, f.patches)
			}
		})
	}
}

func TestDeviceRootIngressCommandRecoversAfterDepthPatchFailure(t *testing.T) {
	f := newIngressCommandFixture(t)
	f.failPatchKind = "ingress"
	args := append(f.args, "--confirm", "video-cloud-dev")
	if err := runDeploymentDeviceRootIngressTrustWithOps(args, f.ops); err == nil || !strings.Contains(err.Error(), "verification depth") {
		t.Fatalf("ingress patch failure was ignored: %v", err)
	}
	if strings.Join(f.patches, ",") != "secret" ||
		f.ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-depth"] != "2" {
		t.Fatalf("failed depth update changed unsafe state: %v", f.patches)
	}
	f.failPatchKind = ""
	if err := runDeploymentDeviceRootIngressTrustWithOps(args, f.ops); err != nil || strings.Join(f.patches, ",") != "secret,ingress" {
		t.Fatalf("retry did not finish only the missing ingress update: %v, patches=%v", err, f.patches)
	}
}

func TestDeviceRootIngressCommandRequiresDepthReadback(t *testing.T) {
	f := newIngressCommandFixture(t)
	originalPatch := f.ops.patch
	f.ops.patch = func(namespace, kind, name, patch string) error {
		if kind == "ingress" {
			return nil // Simulate an API response with no observed policy update.
		}
		return originalPatch(namespace, kind, name, patch)
	}
	err := runDeploymentDeviceRootIngressTrustWithOps(append(f.args, "--confirm", "video-cloud-dev"), f.ops)
	if err == nil || !strings.Contains(err.Error(), "depth read-back is incomplete") {
		t.Fatalf("unobserved ingress policy accepted: %v", err)
	}
}

func TestDeviceRootIngressCommandRejectsInvalidInputAndLiveDrift(t *testing.T) {
	f := newIngressCommandFixture(t)
	for _, args := range [][]string{
		{"--environment", "dev", "--unknown"},
		{"--workspace", f.args[1]},
		append(append([]string{}, f.args...), "unexpected"),
		append(append([]string{}, f.args...), "--confirm", "wrong-stack"),
	} {
		if err := runDeploymentDeviceRootIngressTrustWithOps(args, f.ops); err == nil {
			t.Fatalf("invalid command input accepted: %v", args)
		}
	}
	applyArgs := append(f.args, "--confirm", "video-cloud-dev")
	originalCredentials := f.ops.credentials
	f.ops.credentials = func(string) (func(), error) { return nil, errors.New("operator credentials unavailable") }
	if err := runDeploymentDeviceRootIngressTrustWithOps(applyArgs, f.ops); err == nil || !strings.Contains(err.Error(), "operator credentials unavailable") {
		t.Fatalf("missing credentials accepted: %v", err)
	}
	f.ops.credentials = originalCredentials
	originalGet := f.ops.get
	f.ops.get = func(namespace, kind, name string) (map[string]any, error) {
		if kind == "secret" {
			return nil, errors.New("CA Secret unavailable")
		}
		return originalGet(namespace, kind, name)
	}
	if err := runDeploymentDeviceRootIngressTrustWithOps(applyArgs, f.ops); err == nil || !strings.Contains(err.Error(), "CA Secret unavailable") {
		t.Fatalf("missing CA Secret accepted: %v", err)
	}
	f.ops.get = originalGet
	f.ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-client"] = "off"
	if err := runDeploymentDeviceRootIngressTrustWithOps(applyArgs, f.ops); err == nil || len(f.patches) != 0 {
		t.Fatalf("disabled client mTLS was patched: %v, patches=%v", err, f.patches)
	}
}
