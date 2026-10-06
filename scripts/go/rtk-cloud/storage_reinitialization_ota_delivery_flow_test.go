package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type otaDeliveryReinitializeFixture struct {
	*reinitializeFixture
	storageWrites         int
	driftPod, driftSecret string
}

func newOTADeliveryReinitializeFixture(t *testing.T, selectedURL, liveURL string) *otaDeliveryReinitializeFixture {
	t.Helper()
	f := &otaDeliveryReinitializeFixture{reinitializeFixture: newReinitializeFixture(t)}
	t.Setenv("STORAGE_KUBECTL_FALLBACK", "")
	f.cfg.Storage.RuntimeMedia.Bucket = "rtk-cloud-dev-ota-firmware-us-sea"
	f.cfg.Storage.OTAFirmware = f.cfg.Storage.RuntimeMedia
	f.cfg.Storage.OTAMode = "dedicated"
	f.cfg.Values["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
	f.cfg.Values["LKE_OTA_CORE_CUTOVER_ENABLED"] = "true"
	f.cfg.Values["LKE_OTA_SERVICE_EDGE_ENABLED"] = "true"
	f.cfg.Values["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"] = "false"
	f.cfg.Values["VIDEO_CLOUD_DOMAIN"] = "video.example.test"
	f.cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = selectedURL
	if err := os.WriteFile(f.candidate, []byte("RTK_STORAGE_CANDIDATE_ENVIRONMENT=dev\nLINODE_OTA_OBJ_ACCESS_KEY_ID=candidate-access\nLINODE_OTA_OBJ_SECRET_ACCESS_KEY=candidate-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"LINODE_OTA_OBJ_ACCESS_KEY_ID": "active-old", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "active-secret"} {
		if err := f.store.write("operator/env/"+name, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	baseTransport := f.checker.client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	f.checker.client = &http.Client{Transport: rolloutRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			f.storageWrites++
		}
		response, err := baseTransport.RoundTrip(r)
		if err == nil && strings.HasPrefix(r.URL.Path, "/object-storage/buckets") {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			body = bytes.ReplaceAll(body, []byte(`"endpoint_type":"E1"`), []byte(`"endpoint_type":"E3"`))
			response.Body = io.NopCloser(bytes.NewReader(body))
			response.ContentLength = int64(len(body))
		}
		return response, err
	})}
	if err := os.Remove(storageCutoverMockFile(f.mockRoot, "Deployment", "stack-video-cloud", "api")); err != nil {
		t.Fatal(err)
	}
	deployment := storageCutoverFixture("Deployment", otaServiceWorkloadName)
	container := storageCutoverMap(storageCutoverGet(deployment, "/spec/template/spec/containers").([]any)[0])
	container["name"] = "otaservice"
	container["command"] = []any{"/app/otaservice"}
	container["image"] = "existing-ota-image@sha256:fixture"
	container["env"] = append(container["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_ENABLED", "value": "true"})
	container["env"] = append(container["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_BASE_URL", "value": liveURL}, map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME", "value": "__token__"})
	if liveURL == "" {
		// The existing direct Deployment and Pod omit both CDN settings.
		direct := []any{}
		for _, entry := range container["env"].([]any) {
			if item := storageCutoverMap(entry); item["name"] != "VIDEO_CLOUD_OTA_CDN_BASE_URL" && item["name"] != "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME" {
				direct = append(direct, entry)
			}
		}
		container["env"] = direct
	}
	if liveURL != "" {
		container["env"] = append(container["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "ota-cdn-runtime", "key": "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"}}})
	}
	storageCutoverMockWrite(t, f.mockRoot, deployment)
	core := storageCutoverFixture("Deployment", "video-cloud-api")
	coreContainer := storageCutoverMap(storageCutoverGet(core, "/spec/template/spec/containers").([]any)[0])
	coreContainer["env"] = append(coreContainer["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"})
	storageCutoverMockWrite(t, f.mockRoot, core)
	coreBridge := lkePublicHTTPSBridgeServiceName(f.cfg.Values, lkePublicHTTPSRoute{Namespace: "stack-video-cloud", Service: "video-cloud-api"})
	otaBridge := lkePublicHTTPSBridgeServiceName(f.cfg.Values, lkePublicHTTPSRoute{Namespace: "stack-video-cloud", Service: otaServiceWorkloadName})
	paths := []any{
		map[string]any{"path": "/v1/device/ota/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": otaBridge, "port": map[string]any{"number": 18084}}}},
		map[string]any{"path": "/v1/device/ota/internal/artifact/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": coreBridge, "port": map[string]any{"number": 80}}}},
	}
	storageCutoverMockWrite(t, f.mockRoot, map[string]any{"kind": "Ingress", "metadata": map[string]any{"name": "video-cloud-staging-device-mtls", "namespace": "stack-ingress", "uid": "device-ingress-uid", "generation": 2, "annotations": map[string]any{"nginx.ingress.kubernetes.io/auth-tls-verify-client": "on", "nginx.ingress.kubernetes.io/auth-tls-secret": "stack-ingress/device-client-ca"}}, "spec": map[string]any{"rules": []any{map[string]any{"host": "device.video.example.test", "http": map[string]any{"paths": paths}}}}})
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "ota-running", "namespace": "stack-video-cloud", "uid": "ota-pod", "ownerReferences": []any{map[string]any{"uid": otaServiceWorkloadName + "-uid", "controller": true}}}, "spec": storageCutoverClone(storageCutoverGet(deployment, "/spec/template/spec")), "status": map[string]any{"phase": "Running"}}
	storageCutoverMockWrite(t, f.mockRoot, pod)
	rolledPod := storageCutoverClone(pod).(map[string]any)
	rolledContainer := storageCutoverMap(storageCutoverGet(rolledPod, "/spec/containers").([]any)[0])
	for _, entry := range rolledContainer["env"].([]any) {
		item := storageCutoverMap(entry)
		switch item["name"] {
		case "VIDEO_CLOUD_BLOB_BUCKET":
			item["value"] = f.cfg.Storage.OTAFirmware.Bucket
		case "VIDEO_CLOUD_BLOB_ENDPOINT":
			item["value"] = f.checker.linodeAPIRoot
		case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY":
			item["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": "rtk-storage-ota", "key": item["name"]}}
		}
	}
	write := func(name string, object map[string]any) string {
		body, _ := json.Marshal(object)
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	podAfter := write("after.json", rolledPod)
	drifted := storageCutoverClone(rolledPod).(map[string]any)
	for _, entry := range storageCutoverMap(storageCutoverGet(drifted, "/spec/containers").([]any)[0])["env"].([]any) {
		if item := storageCutoverMap(entry); item["name"] == "VIDEO_CLOUD_OTA_CDN_BASE_URL" {
			item["value"] = "https://changed.example.test"
		}
	}
	f.driftPod = write("drifted.json", drifted)
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "ota-cdn-runtime", "namespace": "stack-video-cloud"}, "data": map[string]any{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("ab", 32)))}}
	storageCutoverMockWrite(t, f.mockRoot, secret)
	storageCutoverMap(secret["data"])["VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"] = base64.StdEncoding.EncodeToString([]byte("bad"))
	f.driftSecret = write("bad-secret.json", secret)
	storageCutoverMockWrite(t, f.mockRoot, map[string]any{"kind": "Service", "metadata": map[string]any{"name": otaServiceWorkloadName, "namespace": "stack-video-cloud"}, "spec": map[string]any{"type": "ClusterIP", "selector": map[string]any{"app.kubernetes.io/name": otaServiceWorkloadName}, "ports": []any{map[string]any{"port": 18084, "targetPort": "http"}}}})
	underlyingKubectl := os.Getenv("RTK_CLOUD_KUBECTL")
	wrapper := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(wrapper, []byte(`#!/bin/sh
case " $* " in
  *" exec postgresql-0 "*) printf 'query\n' >> "$OTA_TEST_LEGACY_QUERIES"; printf '%s\n' "$OTA_TEST_LEGACY_DB_JSON"; exit 0 ;;
  *" rollout status deployment/video-cloud-otaservice "*) if [ ! -e "$OTA_TEST_ROLLED" ]; then cp "$OTA_TEST_POD_AFTER" "$OTA_TEST_POD"; touch "$OTA_TEST_ROLLED"; fi ;;
  *" get endpointslices "*)
    if [ -n "$OTA_TEST_DRIFT_POD" ]; then cp "$OTA_TEST_DRIFT_POD" "$OTA_TEST_POD"; fi
    if [ -n "$OTA_TEST_DRIFT_SECRET" ]; then cp "$OTA_TEST_DRIFT_SECRET" "$OTA_TEST_SECRET"; fi
    if [ -n "$OTA_TEST_DRIFT_CORE" ]; then cp "$OTA_TEST_DRIFT_CORE" "$OTA_TEST_CORE"; fi
    if [ -n "$OTA_TEST_DRIFT_INGRESS" ]; then cp "$OTA_TEST_DRIFT_INGRESS" "$OTA_TEST_INGRESS"; fi
    if [ -n "$OTA_TEST_REMOVE_PROBE" ]; then rm "$OTA_TEST_REMOVE_PROBE"; fi
    printf '{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}}]}]}\n'; exit 0 ;;
esac
exec "$OTA_TEST_KUBECTL" "$@"
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTA_TEST_KUBECTL", underlyingKubectl)
	t.Setenv("OTA_TEST_ROLLED", filepath.Join(t.TempDir(), "rolled"))
	t.Setenv("OTA_TEST_POD_AFTER", podAfter)
	t.Setenv("OTA_TEST_POD", storageCutoverMockFile(f.mockRoot, "Pod", "stack-video-cloud", "ota-running"))
	t.Setenv("OTA_TEST_SECRET", storageCutoverMockFile(f.mockRoot, "Secret", "stack-video-cloud", "ota-cdn-runtime"))
	t.Setenv("OTA_TEST_CORE", storageCutoverMockFile(f.mockRoot, "Deployment", "stack-video-cloud", "video-cloud-api"))
	t.Setenv("OTA_TEST_INGRESS", storageCutoverMockFile(f.mockRoot, "Ingress", "stack-ingress", "video-cloud-staging-device-mtls"))
	t.Setenv("OTA_TEST_DRIFT_POD", "")
	t.Setenv("OTA_TEST_DRIFT_SECRET", "")
	t.Setenv("OTA_TEST_DRIFT_CORE", "")
	t.Setenv("OTA_TEST_DRIFT_INGRESS", "")
	t.Setenv("OTA_TEST_REMOVE_PROBE", "")
	t.Setenv("OTA_TEST_LEGACY_QUERIES", filepath.Join(t.TempDir(), "queries"))
	setOTADeliveryLegacyDrain(t, 47*time.Hour)
	t.Setenv("RTK_CLOUD_KUBECTL", wrapper)
	return f
}

func setOTADeliveryLegacyDrain(t *testing.T, quiet time.Duration) {
	t.Helper()
	checked := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	last := checked.Add(-quiet)
	body, err := json.Marshal(otaLegacyDrainSnapshot{CheckedAt: checked, LegacyReleases: 1, LastActivityAt: &last})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTA_TEST_LEGACY_DB_JSON", string(body))
}

func otaDeliveryLegacyQueries(t *testing.T) int {
	t.Helper()
	body, err := os.ReadFile(os.Getenv("OTA_TEST_LEGACY_QUERIES"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(body), "query\n")
}

func otaDeliveryRemoveArtifactRoute(t *testing.T, source, target string) {
	t.Helper()
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var ingress map[string]any
	if err := json.Unmarshal(body, &ingress); err != nil {
		t.Fatal(err)
	}
	rule := storageCutoverMap(storageCutoverGet(ingress, "/spec/rules").([]any)[0])
	paths := storageCutoverMap(rule["http"])["paths"].([]any)
	storageCutoverMap(rule["http"])["paths"] = paths[:1]
	body, err = json.Marshal(ingress)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeOTADeliveryReinitializeMetrics(t *testing.T, f *otaDeliveryReinitializeFixture, undercount bool) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Minute)
	endpoint := f.checker.linodeAPIRoot
	host := strings.TrimPrefix(endpoint, "http://")
	relative := filepath.Join("artifacts", "ota-metrics", "qualification.json")
	if err := os.MkdirAll(filepath.Join(f.cfg.RuntimeRoot, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.cfg.RuntimeRoot, relative)), 0700); err != nil {
		t.Fatal(err)
	}
	archive := otaMetricsMatrixFixture(t, f.cfg.Storage.OTAFirmware.Bucket+"."+host, host, now.Add(-5*time.Minute), 2, 4096)
	if err := os.WriteFile(filepath.Join(f.cfg.RuntimeRoot, relative), archive, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	receipt := attachOTAMetricsProbeFixture(t, f.cfg.RuntimeRoot, otaMetricsQualification{Source: "akamai_cloud_pulse", Environment: "dev", Bucket: f.cfg.Storage.OTAFirmware.Bucket, BucketHostname: f.cfg.Storage.OTAFirmware.Bucket + "." + host, Region: "us-sea", Endpoint: endpoint, WindowStart: now.Add(-10 * time.Minute).Format(time.RFC3339), WindowEnd: now.Add(-2 * time.Minute).Format(time.RFC3339), ExportedAt: now.Add(-time.Minute).Format(time.RFC3339), RecordedBy: "test-operator", ExportFile: relative, ExportSHA256: hex.EncodeToString(sum[:]), GETMetric: "obj_requests_get", GETRequests: 2, DownloadedBytesMetric: "obj_bytes_downloaded", DownloadedBytes: 4096})
	if undercount {
		start, _ := time.Parse(time.RFC3339, receipt.WindowStart)
		end, _ := time.Parse(time.RFC3339, receipt.WindowEnd)
		body := writeOTAMetricsProbeFixture(t, filepath.Join(f.cfg.RuntimeRoot, receipt.ProbeFile), otaMetricsProbeFixture("dev", receipt.Bucket, receipt.Region, endpoint, start, end, 2, 4097))
		proofSum := sha256.Sum256(body)
		receipt.ProbeSHA256 = hex.EncodeToString(proofSum[:])
	}
	writeOTAMetricsReceipt(t, f.cfg.RuntimeRoot, receipt)
}

func assertOTADeliveryNotPromoted(t *testing.T, f *otaDeliveryReinitializeFixture) {
	t.Helper()
	active, err := f.store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	if active["LINODE_OTA_OBJ_ACCESS_KEY_ID"] != "active-old" {
		t.Fatal("unverified OTA credentials were promoted")
	}
	if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover-ota.json")); !os.IsNotExist(err) {
		t.Fatal("failed OTA reinitialization created an activation receipt")
	}
}

func TestReinitializeOTADeliveryFailsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, selected, live, want string
		undercount                 bool
	}{
		{"selected CDN live direct", "https://cdn.example.test", "", "literal matching", false},
		{"selected direct live CDN", "", "https://cdn.example.test", "literal matching", false},
		{"direct missing proof", "", "", "metrics qualification receipt", false},
		{"direct probe undercount", "", "", "verified probe", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOTADeliveryReinitializeFixture(t, tc.selected, tc.live)
			if tc.undercount {
				writeOTADeliveryReinitializeMetrics(t, f, true)
			}
			err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unverified delivery accepted: %v", err)
			}
			if f.storageWrites != 0 {
				t.Fatal("delivery refusal happened after provider/S3 writes")
			}
			if _, err := f.store.read(storageCutoverJournalName("ota")); !os.IsNotExist(err) {
				t.Fatal("delivery refusal created a journal")
			}
			commands, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
			if strings.Contains(string(commands), " patch ") || strings.Contains(string(commands), "create ") {
				t.Fatal("delivery refusal mutated Kubernetes")
			}
			assertOTADeliveryNotPromoted(t, f)
		})
	}
}

func TestReinitializeOTADeliveryQualifiedActivation(t *testing.T) {
	for _, url := range []string{"", "https://cdn.example.test"} {
		t.Run(firstNonEmpty(url, "direct"), func(t *testing.T) {
			f := newOTADeliveryReinitializeFixture(t, url, url)
			if url == "" {
				writeOTADeliveryReinitializeMetrics(t, f, false)
			}
			captureStdout(t, func() {
				if err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", true); err != nil {
					t.Fatal(err)
				}
			})
			if f.storageWrites != 0 {
				t.Fatal("planning wrote to storage")
			}
			if err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", false); err != nil {
				t.Fatal(err)
			}
			if otaDeliveryLegacyQueries(t) != 0 {
				t.Fatal("completed storage-only repair queried recent legacy work")
			}
			active, err := f.store.readOperator()
			if err != nil || active["LINODE_OTA_OBJ_ACCESS_KEY_ID"] != "candidate-access" {
				t.Fatal("qualified activation did not promote credentials", err)
			}
			if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover-ota.json")); err != nil {
				t.Fatal(err)
			}
			deployment, err := storageCutoverRead("Deployment", "stack-video-cloud", otaServiceWorkloadName)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range storageCutoverGet(deployment, "/spec/template/spec/containers").([]any)[0].(map[string]any)["env"].([]any) {
				if item := storageCutoverMap(entry); item["name"] == "VIDEO_CLOUD_OTA_CDN_BASE_URL" && storageCutoverString(item["value"]) != url {
					t.Fatal("reinitialization changed delivery mode")
				}
			}
		})
	}
}

func TestReinitializeOTADeliveryUsesSelectedAdapterIntent(t *testing.T) {
	f := newOTADeliveryReinitializeFixture(t, "", "")
	f.cfg.AdapterValues = map[string]string{}
	for _, key := range []string{"LKE_OTA_SERVICE_REGISTRATION_ENABLED", "LKE_OTA_CORE_CUTOVER_ENABLED", "LKE_OTA_SERVICE_EDGE_ENABLED", "LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"} {
		f.cfg.AdapterValues[key] = f.cfg.Values[key]
		delete(f.cfg.Values, key)
	}
	writeOTADeliveryReinitializeMetrics(t, f, false)
	captureStdout(t, func() {
		if err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", true); err != nil {
			t.Fatal(err)
		}
	})
	if f.storageWrites != 0 || otaDeliveryLegacyQueries(t) != 0 {
		t.Fatal("selected completed adapter handoff triggered storage writes or a legacy drain")
	}
}

func TestReinitializeOTALegacyDrainFallback(t *testing.T) {
	for _, quiet := range []time.Duration{49 * time.Hour, 47 * time.Hour} {
		t.Run(quiet.String(), func(t *testing.T) {
			f := newOTADeliveryReinitializeFixture(t, "", "")
			writeOTADeliveryReinitializeMetrics(t, f, false)
			f.cfg.Values["LKE_OTA_SERVICE_EDGE_ENABLED"] = "false"
			path := os.Getenv("OTA_TEST_INGRESS")
			otaDeliveryRemoveArtifactRoute(t, path, path)
			setOTADeliveryLegacyDrain(t, quiet)
			err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", false)
			if quiet < otaLegacyReportLateness {
				if err == nil || !strings.Contains(err.Error(), "48h report-lateness") {
					t.Fatalf("recent legacy work accepted by fallback: %v", err)
				}
				if f.storageWrites != 0 {
					t.Fatal("legacy drain refusal occurred after provider writes")
				}
				if _, err := f.store.read(storageCutoverJournalName("ota")); !os.IsNotExist(err) {
					t.Fatal("legacy drain refusal created a journal")
				}
				assertOTADeliveryNotPromoted(t, f)
			} else {
				if err != nil {
					t.Fatalf("drained pre-handoff dedicated OTA could not be repaired: %v", err)
				}
				active, err := f.store.readOperator()
				if err != nil || active["LINODE_OTA_OBJ_ACCESS_KEY_ID"] != "candidate-access" {
					t.Fatal("drained fallback did not promote credentials", err)
				}
				if otaDeliveryLegacyQueries(t) < 3 {
					t.Fatal("full legacy drain was not repeated before journal and promotion")
				}
			}
			if otaDeliveryLegacyQueries(t) == 0 {
				t.Fatal("incomplete handoff bypassed the original drain")
			}
		})
	}
}

func TestReinitializeOTABoundaryDriftStopsPromotion(t *testing.T) {
	for _, completed := range []bool{true, false} {
		name := "completed exemption"
		if !completed {
			name = "legacy drain fallback"
		}
		t.Run(name, func(t *testing.T) {
			f := newOTADeliveryReinitializeFixture(t, "", "")
			writeOTADeliveryReinitializeMetrics(t, f, false)
			setOTADeliveryLegacyDrain(t, 49*time.Hour)
			ingressPath := os.Getenv("OTA_TEST_INGRESS")
			drift := filepath.Join(t.TempDir(), "boundary-drift.json")
			if completed {
				// Losing the artifact bridge cannot change an established exemption
				// into a fallback, even when the original drain would pass.
				otaDeliveryRemoveArtifactRoute(t, ingressPath, drift)
			} else {
				f.cfg.Values["LKE_OTA_SERVICE_EDGE_ENABLED"] = "false"
				original, err := os.ReadFile(ingressPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(drift, original, 0600); err != nil {
					t.Fatal(err)
				}
				// A pre-handoff repair must also stop if the device route changes
				// during its new-Ready wait.
				otaDeliveryRemoveArtifactRoute(t, ingressPath, ingressPath)
			}
			t.Setenv("OTA_TEST_DRIFT_INGRESS", drift)
			err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", false)
			if err == nil || !strings.Contains(err.Error(), "changed during storage-only reinitialization") {
				t.Fatalf("boundary drift after readiness accepted: %v", err)
			}
			assertOTADeliveryNotPromoted(t, f)
			body, err := f.store.read(storageCutoverJournalName("ota"))
			var journal storageCutoverJournal
			if err != nil || json.Unmarshal([]byte(body), &journal) != nil || journal.Status != "failed" {
				t.Fatal("late boundary refusal did not retain a failure journal")
			}
			queries := otaDeliveryLegacyQueries(t)
			if completed && queries != 0 {
				t.Fatal("established exemption drift silently fell back to a passing drain")
			}
			if !completed && queries < 2 {
				t.Fatal("pre-handoff repair did not retain the initial and pre-journal drain")
			}
		})
	}
}

func TestReinitializeOTADeliveryRecheckedBeforePromotion(t *testing.T) {
	for _, tc := range []struct{ name, want string }{{"Pod drift", "literal matching"}, {"Secret drift", "invalid token key"}, {"direct proof removed", "probe archive"}} {
		t.Run(tc.name, func(t *testing.T) {
			url := "https://cdn.example.test"
			if tc.name == "direct proof removed" {
				url = ""
			}
			f := newOTADeliveryReinitializeFixture(t, url, url)
			if tc.name == "Pod drift" {
				t.Setenv("OTA_TEST_DRIFT_POD", f.driftPod)
			} else if tc.name == "Secret drift" {
				t.Setenv("OTA_TEST_DRIFT_SECRET", f.driftSecret)
			} else {
				writeOTADeliveryReinitializeMetrics(t, f, false)
				t.Setenv("OTA_TEST_REMOVE_PROBE", filepath.Join(f.cfg.RuntimeRoot, "artifacts", "ota-metrics", "sample-probe.json"))
			}
			err := f.checker.reinitializeStorage(f.cfg, "ota", f.source, f.candidate, "old", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("delivery drift after readiness accepted: %v", err)
			}
			assertOTADeliveryNotPromoted(t, f)
			body, err := f.store.read(storageCutoverJournalName("ota"))
			var journal storageCutoverJournal
			if err != nil || json.Unmarshal([]byte(body), &journal) != nil || journal.Status != "failed" {
				t.Fatal("late delivery refusal did not retain a failure journal")
			}
		})
	}
}
