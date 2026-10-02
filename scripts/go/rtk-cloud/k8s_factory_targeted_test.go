package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTargetedFactoryCanonicalRollout(t *testing.T) {
	previousCanonical, previousRoot, previousState, previousCache := activeCanonicalSecretStore, activeSecretEnvironmentRoot, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache
	activeCanonicalSecretStore, activeSecretEnvironmentRoot, lkeRuntimeSecretStateDir = true, t.TempDir(), t.TempDir()
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() {
		activeCanonicalSecretStore, activeSecretEnvironmentRoot, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache = previousCanonical, previousRoot, previousState, previousCache
	})
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "false")
	t.Setenv("FACTORY_PRODUCTION_JWT_AUDIENCE", "factory-enroll")
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "ghcr.io/example/video-cloud@sha256:"+strings.Repeat("a", 64))
	ca, key, caPEM, _, err := newLKECertificateAuthority("Factory test Service CA", "p256")
	if err != nil {
		t.Fatal(err)
	}
	cert, private, err := newLKESignedCertificate(ca, key, "factoryenroll", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, "p256")
	if err != nil {
		t.Fatal(err)
	}
	paths := provisionPaths{EnvRoot: t.TempDir()}
	dir := sensitiveEnvironmentPath(paths, "certissuer")
	local := map[string]string{"factory.crt": cert, "factory.key": private, "service-ca.crt": caPEM}
	for name, value := range local {
		writeTestFile(t, filepath.Join(dir, name), value)
		if err := os.Chmod(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{"factory-enroll-auth": "preserved-auth", "factory-production-jwt": "preserved-jwt", "factory-admission": "preserved-admission", "postgres": "preserved-password", "fleet-read-token": "preserved-fleet-read-token"}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(lkeRuntimeSecretStateDir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	secret := func(uid string, data map[string]string) map[string]any {
		encoded := map[string]any{}
		for key, value := range data {
			encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		return map[string]any{"metadata": map[string]any{"uid": uid}, "data": encoded}
	}
	client := secret("client-original", map[string]string{"client.crt": cert, "client.key": private, "ca.crt": caPEM, "unrelated": "preserved-client-extra"})
	runtime := secret("runtime-original", map[string]string{
		"FACTORY_ENROLL_AUTH_KEY": values["factory-enroll-auth"], "FACTORY_ENROLL_PRODUCTION_JWT_SECRET": values["factory-production-jwt"],
		"FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE": "factory-enroll", "FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN": values["factory-admission"],
		"FACTORY_ENROLL_RECOVERY_TOKEN": "", "POSTGRES_PASSWORD": values["postgres"], "unrelated": "preserved-runtime-extra",
	})
	setJSON := func(name string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, string(body))
	}
	fakeKubectl(t)
	baseKubectl := os.Getenv("RTK_CLOUD_KUBECTL")
	t.Setenv("FACTORY_BASE_KUBECTL", baseKubectl)
	bin, calls, marker := t.TempDir(), filepath.Join(t.TempDir(), "calls"), filepath.Join(t.TempDir(), "rolled")
	kubectl := filepath.Join(bin, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *"config current-context"*) printf fixture-context ;;
  *"get deployment factoryenroll"*)
    if [ "${FACTORY_MANAGED:-}" = true ]; then
      printf '{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"FACTORY_ENROLL_IDENTITY_STATE","value":"managed"}]}]}}}}'
    else printf '{}' ; fi ;;
  *"get deployment"*) printf '{}' ;;
  *"get secret factoryenroll-certissuer-client"*) printf '%s' "$FACTORY_CLIENT_JSON" ;;
  *"get secret factoryenroll-runtime"*)
    if [ -f "$FACTORY_ROLLED" ] && [ "${FACTORY_CHANGED_AFTER:-}" = true ]; then printf '%s' "$FACTORY_CHANGED_JSON";
    else printf '%s' "$FACTORY_RUNTIME_JSON"; fi ;;
  *"apply -f -"*) cat >> "$FACTORY_CALLS" ;;
  *"rollout status deployment/factoryenroll"*)
    printf '\nrollout factoryenroll\n' >> "$FACTORY_CALLS"
    if [ "${FACTORY_ROLLOUT_FAIL:-}" = true ]; then exit 1; fi
    touch "$FACTORY_ROLLED" ;;
  *) exec "$FACTORY_BASE_KUBECTL" "$@" ;;
esac
`)
	if err := os.Chmod(kubectl, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("FACTORY_CALLS", calls)
	t.Setenv("FACTORY_ROLLED", marker)
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev", "ACCOUNT_MANAGER_DOMAIN": "account.example.test", "FACTORY_ENROLL_PUBLIC_ENABLED": "true"}
	for _, tc := range []struct{ name, failure, want string }{
		{"preserved canonical credentials", "", ""},
		{"managed identity rejected", "managed", "managed identity owner"},
		{"different client bytes rejected", "client", "differs from canonical identity"},
		{"different JWT rejected", "jwt", "FACTORY_ENROLL_PRODUCTION_JWT_SECRET differs from canonical settings"},
		{"missing issuer Secret rejected", "missing-client", "existing issuer client Secret"},
		{"missing runtime Secret rejected", "missing-runtime", "existing runtime Secret"},
		{"missing local bundle does not bootstrap", "missing-local", "incomplete"},
		{"changed Secret during rollout rejected", "after", "changed during rollout"},
		{"failed rollout rejected", "rollout", "exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(marker)
			if err := os.WriteFile(calls, nil, 0600); err != nil {
				t.Fatal(err)
			}
			setJSON("FACTORY_CLIENT_JSON", client)
			setJSON("FACTORY_RUNTIME_JSON", runtime)
			setJSON("FACTORY_CHANGED_JSON", secret("replacement", map[string]string{"changed": "value"}))
			t.Setenv("FACTORY_MANAGED", "false")
			t.Setenv("FACTORY_CHANGED_AFTER", "false")
			t.Setenv("FACTORY_ROLLOUT_FAIL", "false")
			switch tc.failure {
			case "managed":
				t.Setenv("FACTORY_MANAGED", "true")
			case "client":
				setJSON("FACTORY_CLIENT_JSON", secret("client-original", map[string]string{"client.crt": cert + "\n", "client.key": private, "ca.crt": caPEM}))
			case "jwt":
				data := map[string]any{}
				for key, value := range runtime["data"].(map[string]any) {
					data[key] = value
				}
				data["FACTORY_ENROLL_PRODUCTION_JWT_SECRET"] = base64.StdEncoding.EncodeToString([]byte("different"))
				setJSON("FACTORY_RUNTIME_JSON", map[string]any{"metadata": runtime["metadata"], "data": data})
			case "missing-client":
				t.Setenv("FACTORY_CLIENT_JSON", "")
			case "missing-runtime":
				t.Setenv("FACTORY_RUNTIME_JSON", "")
			case "missing-local":
				if err := os.Remove(filepath.Join(dir, "factory.key")); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(filepath.Join(dir, "factory.key"), []byte(private), 0600); err != nil {
						t.Fatal(err)
					}
				})
			case "after":
				t.Setenv("FACTORY_CHANGED_AFTER", "true")
			case "rollout":
				t.Setenv("FACTORY_ROLLOUT_FAIL", "true")
			}
			var err error
			if tc.failure == "" {
				err = lkeApplyTargetedRuntimeDependencies(paths, env, provisionOptions{workloads: []string{"video-cloud"}})
			} else {
				err = lkeApplyTargetedFactoryEnroll(paths, env)
			}
			if (err != nil) != (tc.want != "") || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("unexpected Factory result: %v", err)
			}
			body := readTestFile(t, calls)
			if tc.want == "" {
				for _, required := range []string{"name: video-cloud-runtime", "kind: Service", "kind: Deployment", "name: factoryenroll", "rollout factoryenroll", `value: "dev"`} {
					if !strings.Contains(body, required) {
						t.Fatalf("missing normal Factory operation: %s", required)
					}
				}
			} else if tc.failure != "after" && tc.failure != "rollout" && body != "" {
				t.Fatal("Factory mutation preceded preservation guard")
			}
			if strings.Contains(body, "kind: Secret\nmetadata:\n  name: factoryenroll-runtime") || strings.Contains(body, "kind: Secret\nmetadata:\n  name: factoryenroll-certissuer-client") || strings.Contains(body, "name: certissuer\n") || strings.Contains(body, "name: openbao") {
				t.Fatal("targeted Factory touched unrelated resources or rewrote credentials")
			}
			if tc.failure != "missing-local" {
				for name, value := range local {
					if readTestFile(t, filepath.Join(dir, name)) != value {
						t.Fatal("Factory local identity changed")
					}
				}
			}
			for name, value := range values {
				if readTestFile(t, filepath.Join(lkeRuntimeSecretStateDir, name)) != value {
					t.Fatal("Factory runtime credential changed")
				}
			}
		})
	}
}
