package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func certIssuerDesiredMaterialTestPaths(t *testing.T) provisionPaths {
	t.Helper()
	old := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = filepath.Join(t.TempDir(), "operator")
	t.Cleanup(func() { activeSecretEnvironmentRoot = old })
	return provisionPaths{EnvRoot: t.TempDir()}
}

func certIssuerDesiredMaterialTestCommand(t *testing.T, material lkeCertIssuerMaterial, calls *[]string) certIssuerIngressCommand {
	t.Helper()
	deployment := certIssuerIngressObject{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "certissuer",
				"env": []any{
					map[string]any{"name": "CERT_ISSUER_SERVER_CERT", "value": "/certissuer/server.crt"},
					map[string]any{"name": "CERT_ISSUER_CLIENT_CA", "value": "/certissuer/service-ca.crt"},
				},
				"volumeMounts": []any{map[string]any{"name": "tls", "mountPath": "/certissuer"}},
			}},
			"volumes": []any{map[string]any{"name": "tls", "secret": map[string]any{"secretName": "certissuer-runtime"}}},
		}}},
	}
	return func(input io.Reader, args ...string) ([]byte, error) {
		if input != nil {
			t.Fatal("desired-material preflight supplied mutation input")
		}
		call := strings.Join(args, " ")
		*calls = append(*calls, call)
		switch {
		case strings.Contains(call, "get deployment certissuer --ignore-not-found=true -o json"):
			return json.Marshal(deployment)
		case strings.Contains(call, "get secret certissuer-runtime -o jsonpath={.data.server\\.crt}"):
			return []byte(base64.StdEncoding.EncodeToString([]byte(material.ServerCert))), nil
		case strings.Contains(call, "get secret certissuer-runtime -o jsonpath={.data.service-ca\\.crt}"):
			return []byte(base64.StdEncoding.EncodeToString([]byte(material.ServiceCA))), nil
		default:
			t.Fatalf("preflight accessed a resource beyond selected public certificates: %s", call)
			return nil, errors.New("unexpected read")
		}
	}
}

func certIssuerDesiredMaterialSnapshot(t *testing.T, paths provisionPaths) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := sensitiveEnvironmentPath(paths, "certissuer")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(raw)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

func TestCertIssuerDesiredMaterialPreflight(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		edit  func(*testing.T, provisionPaths, map[string]string, lkeCertIssuerMaterial)
		cause string
	}{
		{name: "matching-persisted-state"},
		{name: "missing-all-persisted-state", edit: func(t *testing.T, paths provisionPaths, _ map[string]string, _ lkeCertIssuerMaterial) {
			if err := os.RemoveAll(sensitiveEnvironmentPath(paths, "certissuer")); err != nil {
				t.Fatal(err)
			}
		}, cause: "missing or invalid persisted certificate"},
		{name: "missing-client-private-key", edit: func(t *testing.T, paths provisionPaths, _ map[string]string, _ lkeCertIssuerMaterial) {
			if err := os.Remove(sensitiveEnvironmentPath(paths, "certissuer", "client.key")); err != nil {
				t.Fatal(err)
			}
		}, cause: "client.key: missing or invalid persisted private key"},
		{name: "stale-public-SAN", edit: func(t *testing.T, paths provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			oldEnv := appendMap(env, map[string]string{"VIDEO_CLOUD_CERTISSUER_DOMAIN": ""})
			material, err := newLKECertIssuerMaterial(oldEnv)
			if err != nil {
				t.Fatal(err)
			}
			if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), material); err != nil {
				t.Fatal(err)
			}
		}, cause: "omits a resolved public or internal DNS name"},
		{name: "stale-internal-SAN", edit: func(t *testing.T, paths provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			otherEnv := appendMap(env, map[string]string{"CLOUD_STACK_NAME": "other-stack"})
			material, err := newLKECertIssuerMaterial(otherEnv)
			if err != nil {
				t.Fatal(err)
			}
			if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), material); err != nil {
				t.Fatal(err)
			}
		}, cause: "omits a resolved public or internal DNS name"},
		{name: "different-desired-algorithm", edit: func(_ *testing.T, _ provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			env["CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM"] = "ed25519"
		}, cause: "key algorithm differs from desired configuration"},
		{name: "live-good-local-key-mismatch", edit: func(t *testing.T, paths provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			other, err := newLKECertIssuerMaterial(env)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeSensitiveFile(sensitiveEnvironmentPath(paths, "certissuer", "server.key"), other.ServerKey); err != nil {
				t.Fatal(err)
			}
		}, cause: "certificate and private key do not match"},
		{name: "live-good-local-identity-differs", edit: func(t *testing.T, paths provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			other, err := newLKECertIssuerMaterial(env)
			if err != nil {
				t.Fatal(err)
			}
			if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), other); err != nil {
				t.Fatal(err)
			}
		}, cause: "persisted serving identity differs"},
		{name: "local-CA-does-not-sign-local-leaf", edit: func(t *testing.T, paths provisionPaths, env map[string]string, _ lkeCertIssuerMaterial) {
			other, err := newLKECertIssuerMaterial(env)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sensitiveEnvironmentPath(paths, "certissuer", "service-ca.crt"), []byte(other.ServiceCA), 0o600); err != nil {
				t.Fatal(err)
			}
		}, cause: "does not verify against its persisted CA"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			paths := certIssuerDesiredMaterialTestPaths(t)
			env := certIssuerIngressTestEnv()
			material, err := newLKECertIssuerMaterial(env)
			if err != nil {
				t.Fatal(err)
			}
			if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), material); err != nil {
				t.Fatal(err)
			}
			if scenario.edit != nil {
				scenario.edit(t, paths, env, material)
			}
			before := certIssuerDesiredMaterialSnapshot(t, paths)
			calls := []string{}
			err = lkeRequireCertIssuerDesiredMaterialWithCommand(context.Background(), paths, env, certIssuerDesiredMaterialTestCommand(t, material, &calls))
			if scenario.cause == "" && err != nil {
				t.Fatal(err)
			}
			if scenario.cause != "" && (err == nil || !strings.Contains(err.Error(), scenario.cause)) {
				t.Fatalf("got %v; want safe error containing %q", err, scenario.cause)
			}
			if err != nil && (strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), material.ServerKey)) {
				t.Fatal("preflight disclosed private material")
			}
			if after := certIssuerDesiredMaterialSnapshot(t, paths); !reflect.DeepEqual(before, after) {
				t.Fatal("read-only qualification generated, deleted, or replaced persisted material")
			}
			if scenario.cause == "" && len(calls) != 3 {
				t.Fatalf("expected fresh workload and two selected public reads, got %v", calls)
			}
		})
	}
}

func TestCertIssuerDesiredMaterialPreflightRejectsDifferentLiveCA(t *testing.T) {
	paths := certIssuerDesiredMaterialTestPaths(t)
	env := certIssuerIngressTestEnv()
	material, err := newLKECertIssuerMaterial(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), material); err != nil {
		t.Fatal(err)
	}
	other, err := newLKECertIssuerMaterial(env)
	if err != nil {
		t.Fatal(err)
	}
	live := material
	live.ServiceCA = other.ServiceCA
	calls := []string{}
	err = lkeRequireCertIssuerDesiredMaterialWithCommand(context.Background(), paths, env, certIssuerDesiredMaterialTestCommand(t, live, &calls))
	if err == nil || !strings.Contains(err.Error(), "persisted CA differs") {
		t.Fatalf("got %v; wanted live root mismatch to block full rendering", err)
	}
}

func TestCertIssuerDesiredMaterialPreflightAllowsAbsentBootstrapWithoutWriting(t *testing.T) {
	paths := certIssuerDesiredMaterialTestPaths(t)
	calls := 0
	command := func(_ io.Reader, args ...string) ([]byte, error) {
		calls++
		if !strings.Contains(strings.Join(args, " "), "get deployment certissuer --ignore-not-found=true -o json") {
			t.Fatalf("unexpected bootstrap read: %v", args)
		}
		return nil, nil
	}
	if err := lkeRequireCertIssuerDesiredMaterialWithCommand(context.Background(), paths, certIssuerIngressTestEnv(), command); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected fresh deployment discovery, got %d commands", calls)
	}
	if _, err := os.Stat(sensitiveEnvironmentPath(paths, "certissuer")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bootstrap qualification created local TLS state")
	}
}

func TestCertIssuerDesiredMaterialPreflightCancellationStartsNoRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := lkeRequireCertIssuerDesiredMaterialWithCommand(ctx, provisionPaths{}, certIssuerIngressTestEnv(), func(io.Reader, ...string) ([]byte, error) {
		t.Fatal("cancelled qualification started a Kubernetes read")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; wanted cancellation", err)
	}
}

func TestCertIssuerInitialMaterialQualifiesPersistedStateBeforeAbsentWorkloadBootstrap(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		persist bool
		edit    func(*testing.T, provisionPaths, map[string]string)
		cause   string
	}{
		{name: "all-absent"},
		{name: "matching-persisted-state", persist: true},
		{name: "partial-persisted-state", persist: true, edit: func(t *testing.T, paths provisionPaths, _ map[string]string) {
			if err := os.Remove(sensitiveEnvironmentPath(paths, "certissuer", "factory.key")); err != nil {
				t.Fatal(err)
			}
		}, cause: "factory.key: missing or invalid persisted private key"},
		{name: "persisted-old-public-SAN", persist: true, edit: func(_ *testing.T, _ provisionPaths, env map[string]string) {
			env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "new-public.example.test"
		}, cause: "omits a resolved public or internal DNS name"},
		{name: "persisted-old-algorithm", persist: true, edit: func(_ *testing.T, _ provisionPaths, env map[string]string) {
			env["CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM"] = "ed25519"
		}, cause: "key algorithm differs from desired configuration"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			paths := certIssuerDesiredMaterialTestPaths(t)
			env := certIssuerIngressTestEnv()
			if scenario.persist {
				material, err := newLKECertIssuerMaterial(env)
				if err != nil {
					t.Fatal(err)
				}
				if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(paths, "certissuer"), material); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.edit != nil {
				scenario.edit(t, paths, env)
			}
			before := certIssuerDesiredMaterialSnapshot(t, paths)
			for _, check := range []func() error{
				func() error { return lkeRequireCertIssuerInitialMaterial(paths, env) },
				func() error {
					return lkeRequireCertIssuerDesiredMaterialWithCommand(context.Background(), paths, env, func(io.Reader, ...string) ([]byte, error) { return nil, nil })
				},
			} {
				err := check()
				if scenario.cause == "" && err != nil {
					t.Fatal(err)
				}
				if scenario.cause != "" && (err == nil || !strings.Contains(err.Error(), scenario.cause)) {
					t.Fatalf("got %v; expected persisted bootstrap blocker %q", err, scenario.cause)
				}
			}
			if !reflect.DeepEqual(before, certIssuerDesiredMaterialSnapshot(t, paths)) {
				t.Fatal("initial qualification mutated persisted material")
			}
		})
	}
}

func TestCertIssuerDesiredMaterialRequiresRendererCanonicalState(t *testing.T) {
	paths := certIssuerDesiredMaterialTestPaths(t)
	env := certIssuerIngressTestEnv()
	material, err := newLKECertIssuerMaterial(env)
	if err != nil {
		t.Fatal(err)
	}
	// A legacy runtime directory does not supply the canonical renderer state.
	if err := replaceLKECertIssuerMaterial(filepath.Join(paths.EnvRoot, "state", "certissuer"), material); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	err = lkeRequireCertIssuerDesiredMaterialWithCommand(context.Background(), paths, env, certIssuerDesiredMaterialTestCommand(t, material, &calls))
	if err == nil || !strings.Contains(err.Error(), "missing or invalid persisted certificate") {
		t.Fatalf("got %v; expected missing canonical renderer state to block", err)
	}
}
