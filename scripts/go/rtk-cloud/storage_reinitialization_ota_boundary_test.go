package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type otaBoundaryTestFixture struct {
	root    string
	items   []map[string]any
	env     map[string]string
	core    map[string]any
	ingress map[string]any
}

func newOTABoundaryTestFixture(t *testing.T) *otaBoundaryTestFixture {
	t.Helper()
	root := installStorageCutoverMock(t)
	t.Setenv("STORAGE_KUBECTL_FALLBACK", "")
	for _, key := range []string{"LKE_OTA_CORE_CUTOVER_ENABLED", "LKE_OTA_SERVICE_EDGE_ENABLED", "LKE_OTA_SERVICE_REGISTRATION_ENABLED", "LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "LKE_NAMESPACE_INGRESS", "LKE_DEVICE_DOMAIN"} {
		t.Setenv(key, "")
	}
	items, env := otaDeliveryTestFixture(false)
	env["LKE_OTA_CORE_CUTOVER_ENABLED"] = "true"
	env["LKE_OTA_SERVICE_EDGE_ENABLED"] = "true"
	env["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
	env["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"] = "false"
	env["VIDEO_CLOUD_DOMAIN"] = "video.example.invalid"
	env["LKE_DEVICE_DOMAIN"] = "device.video.example.invalid"
	for _, object := range items {
		app := otaDeliveryTestContainer(object)
		app["command"] = []any{"/app/otaservice"}
		app["image"] = "existing-ota-image@sha256:fixture"
		app["env"] = append(app["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_ENABLED", "value": "true"})
	}
	storageCutoverMap(items[0]["metadata"])["generation"] = float64(1)
	core := storageCutoverFixture("Deployment", "video-cloud-api")
	storageCutoverMap(core["metadata"])["generation"] = float64(1)
	coreApp := otaDeliveryTestContainer(core)
	coreApp["image"] = "existing-core-image@sha256:fixture"
	coreApp["env"] = []any{map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"}}
	namespace := lkeNamespaceName(env, "video-cloud")
	coreBridge := lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: namespace, Service: "video-cloud-api"})
	otaBridge := lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: namespace, Service: otaServiceWorkloadName})
	path := func(value, service string, port float64) map[string]any {
		return map[string]any{"path": value, "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": service, "port": map[string]any{"number": port}}}}
	}
	ingress := map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{
			"namespace": "stack-ingress", "name": "video-cloud-staging-device-mtls", "uid": "device-ingress-uid", "generation": float64(1), "resourceVersion": "1",
			"labels":      map[string]any{"app.kubernetes.io/name": "device-mtls"},
			"annotations": map[string]any{"nginx.ingress.kubernetes.io/auth-tls-verify-client": "on", "nginx.ingress.kubernetes.io/auth-tls-secret": "stack-ingress/device-ca"},
		},
		"spec": map[string]any{"rules": []any{map[string]any{"host": env["LKE_DEVICE_DOMAIN"], "http": map[string]any{"paths": []any{
			path("/", coreBridge, 80), path("/v1/device/ota/", otaBridge, 18084), path("/v1/device/ota/internal/artifact/", coreBridge, 80),
		}}}}},
	}
	f := &otaBoundaryTestFixture{root: root, items: items, env: env, core: core, ingress: ingress}
	f.write(t)
	return f
}

func (f *otaBoundaryTestFixture) write(t *testing.T) {
	t.Helper()
	storageCutoverMockWrite(t, f.root, f.core)
	storageCutoverMockWrite(t, f.root, f.ingress)
}

func (f *otaBoundaryTestFixture) paths() []any {
	return storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["http"].(map[string]any)["paths"].([]any)
}

func (f *otaBoundaryTestFixture) verify(t *testing.T, boundary *storageReinitializationOTABoundary) error {
	t.Helper()
	return boundary.verify(otaDeliveryTestBody(t, f.items), f.env)
}

func TestStorageReinitializationOTABoundaryAcceptsExistingDedicatedHandoff(t *testing.T) {
	f := newOTABoundaryTestFixture(t)
	var boundary storageReinitializationOTABoundary
	if err := f.verify(t, &boundary); err != nil {
		t.Fatal(err)
	}
	if err := f.verify(t, &boundary); err != nil {
		t.Fatalf("unchanged handoff rejected: %v", err)
	}
}

func TestStorageReinitializationOTABoundaryRequiresSelectedHandoff(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"core-disabled", "LKE_OTA_CORE_CUTOVER_ENABLED", "false"},
		{"core-missing", "LKE_OTA_CORE_CUTOVER_ENABLED", ""},
		{"edge-disabled", "LKE_OTA_SERVICE_EDGE_ENABLED", "false"},
		{"registration-disabled", "LKE_OTA_SERVICE_REGISTRATION_ENABLED", "false"},
		{"legacy-registrar-enabled", "LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			f.env[tc.key] = tc.value
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("incomplete selected handoff accepted")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryRequiresDedicatedProcess(t *testing.T) {
	for _, workload := range []struct {
		name  string
		index int
	}{{"deployment", 0}, {"pod", 2}} {
		for _, mutation := range []string{"wrong-command", "missing-command", "missing-registration", "disabled-registration", "indirect-registration", "duplicate-registration", "yes-registration", "on-registration", "whitespace-registration", "missing-image", "mismatched-image"} {
			t.Run(workload.name+"/"+mutation, func(t *testing.T) {
				f := newOTABoundaryTestFixture(t)
				object := f.items[workload.index]
				app := otaDeliveryTestContainer(object)
				switch mutation {
				case "wrong-command":
					app["command"] = []any{"/app/api"}
				case "missing-command":
					delete(app, "command")
				case "missing-registration":
					otaDeliveryTestRemoveEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")
				case "disabled-registration":
					otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")["value"] = "false"
				case "yes-registration":
					otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")["value"] = "yes"
				case "on-registration":
					otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")["value"] = "on"
				case "whitespace-registration":
					otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")["value"] = " true "
				case "indirect-registration":
					entry := otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")
					delete(entry, "value")
					entry["valueFrom"] = map[string]any{"configMapKeyRef": map[string]any{"name": "runtime", "key": "VIDEO_CLOUD_OTA_SERVICE_ENABLED"}}
				case "duplicate-registration":
					app["env"] = append(app["env"].([]any), storageCutoverClone(otaDeliveryTestEnv(object, "VIDEO_CLOUD_OTA_SERVICE_ENABLED")))
				case "missing-image":
					delete(app, "image")
				case "mismatched-image":
					app["image"] = "another-ota-image@sha256:fixture"
				}
				var boundary storageReinitializationOTABoundary
				if err := f.verify(t, &boundary); err == nil {
					t.Fatal("unknown or changed OTA process accepted")
				}
			})
		}
	}
}

func TestStorageReinitializationOTABoundaryRequiresLiteralObservedCoreHandoff(t *testing.T) {
	for _, mutation := range []string{"missing-core", "missing-flag", "false-flag", "indirect-flag", "duplicate-flag", "env-from", "missing-app", "duplicate-app"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			app := otaDeliveryTestContainer(f.core)
			entry := otaDeliveryTestEnv(f.core, "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED")
			switch mutation {
			case "missing-core":
				if err := os.Remove(storageCutoverMockFile(f.root, "Deployment", "stack-video-cloud", "video-cloud-api")); err != nil {
					t.Fatal(err)
				}
			case "missing-flag":
				otaDeliveryTestRemoveEnv(f.core, "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED")
			case "false-flag":
				entry["value"] = "false"
			case "indirect-flag":
				delete(entry, "value")
				entry["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": "core-runtime", "key": "cutover"}}
			case "duplicate-flag":
				app["env"] = append(app["env"].([]any), storageCutoverClone(entry))
			case "env-from":
				app["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "runtime"}}}
			case "missing-app":
				app["name"] = "sidecar"
			case "duplicate-app":
				pod := storageCutoverMap(storageCutoverGet(f.core, "/spec/template/spec"))
				pod["containers"] = append(pod["containers"].([]any), storageCutoverClone(app))
			}
			if mutation != "missing-core" {
				f.write(t)
			}
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("unproven core handoff accepted")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryRequiresExistingArtifactEdge(t *testing.T) {
	for _, mutation := range []string{"missing-ingress", "missing-artifact-route", "wrong-artifact-backend", "wrong-artifact-port", "wrong-artifact-path-type", "wrong-ota-backend", "wrong-host", "missing-mtls", "malformed-paths"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			paths := f.paths()
			artifact := storageCutoverMap(paths[2])
			switch mutation {
			case "missing-ingress":
				if err := os.Remove(storageCutoverMockFile(f.root, "Ingress", "stack-ingress", "video-cloud-staging-device-mtls")); err != nil {
					t.Fatal(err)
				}
			case "missing-artifact-route":
				storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = paths[:2]
			case "wrong-artifact-backend":
				storageCutoverMap(storageCutoverGet(artifact, "/backend/service"))["name"] = "wrong-backend"
			case "wrong-artifact-port":
				storageCutoverMap(storageCutoverGet(artifact, "/backend/service/port"))["number"] = float64(18084)
			case "wrong-artifact-path-type":
				artifact["pathType"] = "Exact"
			case "wrong-ota-backend":
				storageCutoverMap(storageCutoverGet(storageCutoverMap(paths[1]), "/backend/service"))["name"] = "wrong-backend"
			case "wrong-host":
				storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["host"] = "other.example.invalid"
			case "missing-mtls":
				delete(storageCutoverMap(storageCutoverGet(f.ingress, "/metadata/annotations")), "nginx.ingress.kubernetes.io/auth-tls-verify-client")
			case "malformed-paths":
				storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = "unknown"
			}
			if mutation != "missing-ingress" {
				f.write(t)
			}
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("unproven or incorrect device/artifact route accepted")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryRejectsSnapshotDrift(t *testing.T) {
	for _, mutation := range []string{"core-uid", "core-generation", "core-spec", "ingress-uid", "ingress-path", "ingress-annotation", "ingress-label", "ota-uid", "ota-image", "ota-args"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "core-uid":
				storageCutoverMap(f.core["metadata"])["uid"] = "replacement-core"
			case "core-generation":
				storageCutoverMap(f.core["metadata"])["generation"] = float64(2)
			case "core-spec":
				otaDeliveryTestContainer(f.core)["image"] = "updated-core-image"
			case "ingress-uid":
				storageCutoverMap(f.ingress["metadata"])["uid"] = "replacement-ingress"
			case "ingress-path":
				storageCutoverMap(f.paths()[0])["path"] = "/changed/"
			case "ingress-annotation":
				storageCutoverMap(storageCutoverGet(f.ingress, "/metadata/annotations"))["nginx.ingress.kubernetes.io/auth-tls-secret"] = "stack-ingress/other-ca"
			case "ingress-label":
				storageCutoverMap(storageCutoverGet(f.ingress, "/metadata/labels"))["app.kubernetes.io/name"] = "changed-label"
			case "ota-uid":
				storageCutoverMap(f.items[0]["metadata"])["uid"] = "replacement-ota"
			case "ota-image":
				for _, i := range []int{0, 2} {
					otaDeliveryTestContainer(f.items[i])["image"] = "updated-ota-image"
				}
			case "ota-args":
				for _, i := range []int{0, 2} {
					otaDeliveryTestContainer(f.items[i])["args"] = []any{"--changed"}
				}
			}
			f.write(t)
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("handoff or existing OTA process changed without blocking repair")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryAllowsStatusAndPodReplacement(t *testing.T) {
	f := newOTABoundaryTestFixture(t)
	var boundary storageReinitializationOTABoundary
	if err := f.verify(t, &boundary); err != nil {
		t.Fatal(err)
	}
	for _, object := range []map[string]any{f.core, f.ingress, f.items[0], f.items[2]} {
		storageCutoverMap(object["metadata"])["resourceVersion"] = "new-status-version"
		object["status"] = map[string]any{"observedGeneration": float64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False"}}}
	}
	storageCutoverMap(f.items[2]["metadata"])["uid"] = "replacement-owned-pod"
	storageCutoverMap(f.items[2]["status"])["phase"] = "Running"
	for _, i := range []int{0, 2} {
		otaDeliveryTestEnv(f.items[i], "VIDEO_CLOUD_BLOB_BUCKET")["value"] = "qualified-replacement-bucket"
	}
	f.write(t)
	if err := f.verify(t, &boundary); err != nil {
		t.Fatalf("normal status, owned Pod replacement, or storage repair invalidated handoff: %v", err)
	}
}

func TestStorageReinitializationOTABoundaryDoesNotRequireOldOTAReady(t *testing.T) {
	f := newOTABoundaryTestFixture(t)
	f.items[2]["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "False"}}, "containerStatuses": []any{map[string]any{"name": "otaservice", "ready": false}}}
	var boundary storageReinitializationOTABoundary
	if err := f.verify(t, &boundary); err != nil {
		t.Fatalf("broken old OTA readiness blocked storage repair: %v", err)
	}
	commands, err := os.ReadFile(filepath.Join(f.root, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "get endpointslices") || strings.Contains(string(commands), "get service "+otaServiceWorkloadName) || strings.Contains(string(commands), "rollout status deployment/"+otaServiceWorkloadName) {
		t.Fatal("boundary verification required old OTA service readiness")
	}
	if !strings.Contains(string(commands), "rollout status deployment/video-cloud-api") {
		t.Fatal("boundary verification skipped observed core rollout")
	}
}

func otaBoundaryTestDrain(t *testing.T, quietFor time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	last := now.Add(-quietFor)
	body, err := json.Marshal(otaLegacyDrainSnapshot{CheckedAt: now, LegacyReleases: 1, LastActivityAt: &last})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	log := filepath.Join(root, "legacy-sql")
	underlying := os.Getenv("RTK_CLOUD_KUBECTL")
	path := filepath.Join(root, "kubectl")
	if err := os.WriteFile(path, []byte(`#!/bin/sh
case " $* " in
  *" exec postgresql-0 "*)
    printf '%s\n' "$*" >> "$OTA_BOUNDARY_TEST_SQL_LOG"
    printf '%s\n' "$OTA_BOUNDARY_TEST_DRAIN_JSON"
    exit 0 ;;
esac
exec "$OTA_BOUNDARY_TEST_KUBECTL" "$@"
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTA_BOUNDARY_TEST_SQL_LOG", log)
	t.Setenv("OTA_BOUNDARY_TEST_DRAIN_JSON", string(body))
	t.Setenv("OTA_BOUNDARY_TEST_KUBECTL", underlying)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	return log
}

func otaBoundaryTestSQL(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestStorageReinitializationOTABoundaryIncompleteHandoffRequiresFullDrain(t *testing.T) {
	for _, quietFor := range []time.Duration{49 * time.Hour, 47 * time.Hour} {
		t.Run(quietFor.String(), func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			f.env["LKE_OTA_CORE_CUTOVER_ENABLED"] = "false"
			f.env["LKE_OTA_SERVICE_EDGE_ENABLED"] = "false"
			storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = f.paths()[:2]
			f.write(t)
			log := otaBoundaryTestDrain(t, quietFor)
			var boundary storageReinitializationOTABoundary
			for i := 0; i < 2; i++ {
				err := f.verify(t, &boundary)
				if quietFor < 48*time.Hour {
					if err == nil || !strings.Contains(err.Error(), "48h") {
						t.Fatalf("recent legacy activity accepted: %v", err)
					}
				} else if err != nil {
					t.Fatalf("full drain failed to permit existing pre-handoff repair: %v", err)
				}
			}
			queries := otaBoundaryTestSQL(t, log)
			if strings.Count(queries, " exec postgresql-0 ") != 2 || !strings.Contains(queries, "SELECT json_build_object") || !strings.Contains(queries, "psql -X -A -t") {
				t.Fatal("incomplete handoff did not repeat the existing read-only database drain")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryCompletedHandoffDoesNotDrainAgain(t *testing.T) {
	f := newOTABoundaryTestFixture(t)
	log := otaBoundaryTestDrain(t, 47*time.Hour)
	var boundary storageReinitializationOTABoundary
	for i := 0; i < 2; i++ {
		if err := f.verify(t, &boundary); err != nil {
			t.Fatalf("unchanged completed handoff required another legacy drain: %v", err)
		}
	}
	if otaBoundaryTestSQL(t, log) != "" {
		t.Fatal("completed storage-only handoff queried legacy drain")
	}
}

func TestStorageReinitializationOTABoundaryExemptionDriftCannotFallBackToDrain(t *testing.T) {
	for _, mutation := range []string{"artifact-route-removed", "selected-edge-disabled"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			log := otaBoundaryTestDrain(t, 49*time.Hour)
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err != nil {
				t.Fatal(err)
			}
			if mutation == "artifact-route-removed" {
				storageCutoverGet(f.ingress, "/spec/rules").([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = f.paths()[:2]
				f.write(t)
			} else {
				f.env["LKE_OTA_SERVICE_EDGE_ENABLED"] = "false"
			}
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("established exemption changed into a full-drain repair")
			}
			if otaBoundaryTestSQL(t, log) != "" {
				t.Fatal("exemption boundary drift was allowed to fall back to a passing drain")
			}
		})
	}
}

func TestStorageReinitializationOTABoundaryFullDrainModeCannotChange(t *testing.T) {
	for _, mutation := range []string{"selected-handoff-enabled", "core-uid-changed"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOTABoundaryTestFixture(t)
			f.env["LKE_OTA_CORE_CUTOVER_ENABLED"] = "false"
			f.env["LKE_OTA_SERVICE_EDGE_ENABLED"] = "false"
			log := otaBoundaryTestDrain(t, 49*time.Hour)
			var boundary storageReinitializationOTABoundary
			if err := f.verify(t, &boundary); err != nil {
				t.Fatal(err)
			}
			before := otaBoundaryTestSQL(t, log)
			if before == "" {
				t.Fatal("initial repair did not use full drain")
			}
			if mutation == "selected-handoff-enabled" {
				f.env["LKE_OTA_CORE_CUTOVER_ENABLED"] = "true"
				f.env["LKE_OTA_SERVICE_EDGE_ENABLED"] = "true"
			} else {
				storageCutoverMap(f.core["metadata"])["uid"] = "replacement-core"
				f.write(t)
			}
			if err := f.verify(t, &boundary); err == nil {
				t.Fatal("full-drain repair changed mode or boundary mid-operation")
			}
			if after := otaBoundaryTestSQL(t, log); after != before {
				t.Fatal("boundary drift was evaluated through another passing drain")
			}
		})
	}
}
