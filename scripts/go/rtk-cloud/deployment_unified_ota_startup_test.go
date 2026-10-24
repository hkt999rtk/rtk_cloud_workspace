package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func otaStorageFixture(environment string) (deploymentConfig, environmentOTAStorageObservation) {
	cfg := deploymentConfig{Environment: environment, Values: map[string]string{"OTA_SERVICE_REGISTRATION_ENABLED": "true"}, Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Bucket: "isolated-ota-" + environment, Region: "us-lax", Prefix: "environments/video-cloud-" + environment}}}
	o := environmentOTAStorageObservation{Version: 1, Environment: environment, Provider: "linode", Bucket: cfg.Storage.OTAFirmware.Bucket, Region: cfg.Storage.OTAFirmware.Region, Prefix: cfg.Storage.OTAFirmware.Prefix, Endpoint: "https://isolated-ota.example.invalid", EndpointType: "E3"}
	return cfg, o
}

func otaStartupFixture(t *testing.T, environment string) ([]deploymentplan.Definition, deploymentplan.Object) {
	t.Helper()
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "CLOUD_ENV_NAME": environment, "VIDEO_CLOUD_OTA_STORAGE_MODE": "dedicated", "VIDEO_CLOUD_OTA_BLOB_BUCKET": "isolated-ota-" + environment, "VIDEO_CLOUD_OTA_BLOB_REGION": "us-lax", "VIDEO_CLOUD_OTA_BLOB_PREFIX": "environments/video-cloud-" + environment, "VIDEO_CLOUD_OTA_BLOB_ENDPOINT": "https://isolated-ota.example.invalid"}
	var object deploymentplan.Object
	if err := yaml.Unmarshal([]byte(lkeOTAServiceDeploymentManifest(env)), &object); err != nil {
		t.Fatal(err)
	}
	object = deploymentplan.Clone(object)
	definitions := []deploymentplan.Definition{{Object: object}}
	for _, role := range [][2]string{{"account-manager", "account-manager"}, {"billing", "billing"}} {
		ports := []any{deploymentplan.Object{"name": "http", "port": float64(80), "targetPort": float64(8080)}}
		if role[0] == "account-manager" {
			ports = append(ports, deploymentplan.Object{"name": "service-reg", "port": float64(8443), "targetPort": "service-reg"})
		}
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"name": role[1], "namespace": "video-cloud-" + environment + "-" + role[0]}, "spec": deploymentplan.Object{"ports": ports}}})
	}
	definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"name": "postgresql", "namespace": "video-cloud-" + environment + "-platform"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "postgres", "port": float64(5432), "targetPort": float64(5432)}}}}})
	if err := deriveEnvironmentOTAStartup(definitions); err != nil {
		t.Fatal(err)
	}
	return definitions, object
}

func TestOTAStartupCandidatesRejectLostStorageAndUnsafeInputsWithoutLiveWorkload(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "empty-bucket", "empty-region", "empty-endpoint", "http-endpoint", "credential-url", "missing-DSN", "wrong-DB", "foreign-DB-host", "wrong-AM-URL", "missing-AM-token", "inline-AWS-secret", "wrong-identity-path", "bad-lateness", "enable-MQTT", "enable-clip", "initialize-schema"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				defs, object := otaStartupFixture(t, environment)
				c := otaStartupContainer(object)
				key, value := "", ""
				switch mode {
				case "empty-bucket":
					key = "VIDEO_CLOUD_BLOB_BUCKET"
				case "empty-region":
					key = "VIDEO_CLOUD_BLOB_REGION"
				case "empty-endpoint":
					key = "VIDEO_CLOUD_BLOB_ENDPOINT"
				case "http-endpoint":
					key, value = "VIDEO_CLOUD_BLOB_ENDPOINT", "http://invalid.example"
				case "credential-url":
					key, value = "VIDEO_CLOUD_BLOB_ENDPOINT", "https://private-canary:private-canary@invalid.example"
				case "missing-DSN":
					key = "VIDEO_CLOUD_DB_DSN"
				case "wrong-DB":
					key, value = "VIDEO_CLOUD_DB_DSN", strings.Replace(mqttFoundationEnv(c, "VIDEO_CLOUD_DB_DSN"), "/video_cloud?", "/foreign?", 1)
				case "foreign-DB-host":
					key, value = "VIDEO_CLOUD_DB_DSN", strings.Replace(mqttFoundationEnv(c, "VIDEO_CLOUD_DB_DSN"), "postgresql.video-cloud-"+environment+"-platform.svc.cluster.local", "foreign.example", 1)
				case "wrong-AM-URL":
					key, value = "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL", "http://foreign.example:80"
				case "missing-AM-token":
					key = "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"
				case "inline-AWS-secret":
					key, value = "AWS_SECRET_ACCESS_KEY", "private-canary"
				case "wrong-identity-path":
					key, value = "VIDEO_CLOUD_OTA_SERVICE_CLIENT_CERT", "/foreign/private-canary"
				case "bad-lateness":
					key, value = "VIDEO_CLOUD_OTA_REPORT_LATENESS", "49h"
				case "enable-MQTT":
					key, value = "VIDEO_CLOUD_MQTT_ENABLED", "true"
				case "enable-clip":
					key, value = "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "true"
				case "initialize-schema":
					key, value = "VIDEO_CLOUD_DB_ENSURE_SCHEMA", "true"
				}
				if key != "" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": key, "value": value}})
				}
				err := validateEnvironmentOTAStartup(defs)
				if (err == nil) != (mode == "valid") {
					t.Fatal("candidate startup prerequisite decision wrong", mode, err)
				}
				if err != nil {
					var b *deploymentplan.Blocker
					if !errors.As(err, &b) || b.ID != "deployment.configuration.ota-startup" || b.Resource != deploymentplan.ID(object) || strings.Contains(err.Error(), "private-canary") {
						t.Fatal("unsafe/misclassified OTA blocker", err)
					}
				}
			})
		}
	}
}

func TestOTAStartupChecksOriginalCredentialOwnersAndKeysWithoutPodReads(t *testing.T) {
	for _, mode := range []string{"valid", "replacement-owner", "missing-key", "changed-reference", "canonical-storage-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			defs, ota := otaStartupFixture(t, "dev")
			ns := "video-cloud-dev-video-cloud"
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Operation: "upgrade"}}
			for _, d := range defs {
				r := deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Action: "retain", UID: "original-role", Desired: d.Object, Baseline: deploymentplan.Clone(d.Object)}
				if d.Object == nil {
					t.Fatal("missing fixture")
				}
				if d.Object["kind"] == "Deployment" {
					r.Action = "update"
				}
				plan.Resources = append(plan.Resources, r)
			}
			actual := map[string]deploymentplan.Object{}
			settings, _ := environmentPKINamedFields(otaStartupContainer(ota)["env"])
			for _, key := range otaStartupCredentialSettings {
				ref := deploymentplan.Map(deploymentplan.Map(settings[key]["valueFrom"])["secretKeyRef"])
				id := "Secret/" + ns + "/" + deploymentplan.String(ref["name"])
				if actual[id] == nil {
					actual[id] = deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": ref["name"], "namespace": ns, "uid": "original-secret"}, "data": deploymentplan.Object{}}
				}
				deploymentplan.Map(actual[id]["data"])[deploymentplan.String(ref["key"])] = base64.StdEncoding.EncodeToString([]byte("isolated"))
			}
			for _, key := range []string{"client.crt", "client.key", "server-ca.crt"} {
				file, _ := coreMQTTFile(ota, otaStartupContainer(ota), "platform-service-identity", key)
				id := "Secret/" + ns + "/" + file.reference
				if actual[id] == nil {
					actual[id] = deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": file.reference, "namespace": ns, "uid": "original-secret"}, "data": deploymentplan.Object{}}
				}
				deploymentplan.Map(actual[id]["data"])[key] = base64.StdEncoding.EncodeToString([]byte("isolated"))
			}
			for id, source := range actual {
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: "original-secret", Desired: deploymentplan.Projection(source), Baseline: deploymentplan.Projection(source)})
			}
			operator := map[string]string{"LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated"}
			if mode == "canonical-storage-mismatch" {
				operator["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"] = "private-canary"
			}
			if mode == "changed-reference" {
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(settings["AWS_SECRET_ACCESS_KEY"]["valueFrom"])["secretKeyRef"]))["name"] = "changed"
			}
			read := func(_ context.Context, id string) (deploymentplan.Object, error) {
				if !strings.HasPrefix(id, "Secret/") {
					t.Fatal("startup guard depended on running workload", id)
				}
				source := deploymentplan.Clone(actual[id])
				if source == nil {
					return nil, nil
				}
				if mode == "replacement-owner" {
					deploymentplan.Map(source["metadata"])["uid"] = "changed"
				}
				if mode == "missing-key" {
					source["data"] = deploymentplan.Object{}
				}
				return source, nil
			}
			err := checkEnvironmentOTAStartup(context.Background(), plan, read, operator)
			if (err == nil) != (mode == "valid") {
				t.Fatal("original startup source guard decision wrong", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-canary") {
				t.Fatal("private credential leaked", err)
			}
		})
	}
}

func TestOTAStorageProjectionChangesOnlyOTAAndBindsAllocationInputs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-ota-projection")
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			before, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			cfg, observation := otaStorageFixture(environment)
			projected := appendMap(env, nil)
			if err := projectOTAStorageRenderInputs(cfg, observation, projected); err != nil {
				t.Fatal(err)
			}
			after, err := renderEnvironmentResources(projected, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(before) != deploymentplan.Digest(after) {
				t.Fatal("OTA-only storage projection changed an unrelated working workload")
			}
			snapshot := environmentDeploymentSnapshot{OTAStorage: observation}
			hash := snapshot.Digest(cfg)
			snapshot.OTAStorage.Endpoint = "https://changed.example.invalid"
			if hash == snapshot.Digest(cfg) {
				t.Fatal("provider endpoint not included in plan input fence")
			}
			for _, mode := range []string{"bucket", "region", "prefix", "environment"} {
				changed := observation
				switch mode {
				case "bucket":
					changed.Bucket = "foreign"
				case "region":
					changed.Region = "foreign"
				case "prefix":
					changed.Prefix = "foreign"
				case "environment":
					changed.Environment = "foreign"
				}
				if validateOTAStorageObservation(cfg, changed) == nil {
					t.Fatal("allocation switch silently accepted", mode)
				}
			}
			observation.EndpointType = "E1"
			if validateOTAStorageObservation(cfg, observation) == nil {
				t.Fatal("non-CDN direct download accepted E1")
			}
			cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = "https://private-cdn.example.invalid"
			if err := validateOTAStorageObservation(cfg, observation); err != nil {
				t.Fatal("existing private-CDN profile unnecessarily requires E3", err)
			}
		})
	}
}

func TestOTAStorageObservationUsesGenuineBoundedReadOnlyProviderMetadata(t *testing.T) {
	cfg, _ := otaStorageFixture("dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/object-storage/buckets" {
			t.Errorf("provider observation mutated or enumerated unrelated resources: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer private-canary" {
			t.Error("original canonical provider credential not used")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "pages": 1, "data": []any{linodeStorageBucket{Label: cfg.Storage.OTAFirmware.Bucket, Region: cfg.Storage.OTAFirmware.Region, S3Endpoint: "isolated-ota.example.invalid", EndpointType: "E3"}}})
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}
	operator := map[string]string{"LINODE_TOKEN": "private-canary", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated-access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated-secret"}
	observation, err := observeOTAStorageForRender(context.Background(), cfg, store, operator, checker)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := privateDeploymentStatePath("dev", environmentOTAObservationPath)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := readOTAStorageObservation(cfg, store)
	if err != nil || loaded != observation {
		t.Fatal("genuine selected allocation did not round-trip", err)
	}
	info, _ := os.Stat(path)
	parent, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || parent.Mode().Perm() != 0700 {
		t.Fatal("canonical metadata permissions incorrect")
	}
	if _, err := observeOTAStorageForRender(context.Background(), cfg, store, operator, checker); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || calls != 2 {
		t.Fatal("stable projection was rewritten or current provider read reused cached evidence")
	}
	if strings.Contains(string(before), "private-canary") || strings.Contains(string(before), "isolated-secret") {
		t.Fatal("observation persisted credential values")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeOTAStorageForRender(ctx, cfg, store, operator, checker); err == nil || calls != 2 {
		t.Fatal("cancelled observation started provider work")
	}
	var record environmentOTAStorageRecord
	_ = json.Unmarshal(before, &record)
	hash := (environmentDeploymentSnapshot{OTAStorage: record.Observation}).Digest(cfg)
	record.ObservedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	changed, _ := json.Marshal(record)
	if err := writePrivateDeploymentState("dev", environmentOTAObservationPath, changed); err != nil {
		t.Fatal(err)
	}
	loaded, err = readOTAStorageObservation(cfg, store)
	if err != nil || hash != (environmentDeploymentSnapshot{OTAStorage: loaded}).Digest(cfg) {
		t.Fatal("observation timestamp perturbed reviewed plan input fence", err)
	}
}

func TestOTAStorageObservationRejectsMalformedAndEscapedCanonicalEvidence(t *testing.T) {
	for _, mode := range []string{"malformed", "hash", "cross-environment", "file-mode", "file-symlink", "parent-symlink", "parent-mode"} {
		t.Run(mode, func(t *testing.T) {
			cfg, o := otaStorageFixture("dev")
			store := makeIsolatedTestSecretStore(t, "dev")
			record := environmentOTAStorageRecord{Observation: o, SHA256: deploymentplan.Digest(o), ObservedAt: time.Now().UTC().Format(time.RFC3339)}
			if mode == "hash" {
				record.SHA256 = "private-canary"
			}
			if mode == "cross-environment" {
				record.Observation.Environment = "prod"
				record.SHA256 = deploymentplan.Digest(record.Observation)
			}
			raw, _ := json.Marshal(record)
			if mode == "malformed" {
				raw = []byte("private-canary")
			}
			if err := writePrivateDeploymentState("dev", environmentOTAObservationPath, raw); err != nil {
				t.Fatal(err)
			}
			path, _ := store.safePath(environmentOTAObservationPath)
			switch mode {
			case "file-mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "file-symlink":
				target := filepath.Join(t.TempDir(), "observation")
				_ = os.WriteFile(target, raw, 0600)
				_ = os.Remove(path)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				parent := filepath.Dir(path)
				outside := filepath.Join(t.TempDir(), "outside")
				_ = os.Mkdir(outside, 0700)
				_ = os.RemoveAll(parent)
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			case "parent-mode":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readOTAStorageObservation(cfg, store); err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("malformed/escaped observation was accepted or leaked", mode, err)
			}
		})
	}
}

func TestOTAStorageAccessUsesOnlySignedPrivacyAndListReads(t *testing.T) {
	cfg, observation := otaStorageFixture("dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Errorf("OTA input check attempted a storage write: %s", r.Method)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("storage read was not signed")
		}
		if _, ok := r.URL.Query()["acl"]; ok {
			_, _ = w.Write([]byte(`<AccessControlPolicy><Owner><ID>isolated-owner</ID></Owner><AccessControlList><Grant><Grantee><ID>isolated-owner</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`))
			return
		}
		if _, ok := r.URL.Query()["policy"]; ok {
			w.WriteHeader(404)
			return
		}
		if r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != cfg.Storage.OTAFirmware.Prefix+"/" {
			t.Error("unexpected native storage read scope")
		}
		_, _ = w.Write([]byte(`<ListBucketResult></ListBucketResult>`))
	}))
	defer server.Close()
	observation.Endpoint = server.URL
	record := environmentOTAStorageRecord{Observation: observation, SHA256: deploymentplan.Digest(observation), ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(record)
	if err := writePrivateDeploymentState("dev", environmentOTAObservationPath, raw); err != nil {
		t.Fatal(err)
	}
	operator := map[string]string{"LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated-access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated-secret"}
	checker := deploymentCredentialChecker{client: server.Client()}
	if err := checkOTAStorageAccess(context.Background(), cfg, store, operator, checker); err != nil || calls != 3 {
		t.Fatal("read-only storage prerequisite failed or produced extra operations", err, calls)
	}
	if _, err := readDeploymentStorageState("dev", "storage-preflight-ota.json"); err == nil {
		t.Fatal("read-only storage prerequisite fabricated a validation receipt")
	}
}
