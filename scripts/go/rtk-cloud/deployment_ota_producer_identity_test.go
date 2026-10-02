package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestOTAProducerOwnerIdentityIsIndependentAndReused(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	api, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	apiPath, _ := deploymentServiceIdentityPath("service:ota", "")
	before, _ := f.store.read(apiPath)
	job, err := ensureDeploymentServiceIdentityOwner(f.store, f.cfg.Stack, "service:ota", otaProducerSealOwner, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Subject != api.Subject || job.Owner != otaProducerSealOwner || job.RequestID == api.RequestID || job.PrivateKey == api.PrivateKey || job.Fingerprint == api.Fingerprint {
		t.Fatal("Job reused API owner identity or request reference")
	}
	path, _ := deploymentServiceIdentityPath(job.Subject, job.Owner)
	saved, _ := f.store.read(path)
	replay, err := ensureDeploymentServiceIdentityOwner(f.store, f.cfg.Stack, "service:ota", otaProducerSealOwner, nil, nil, "")
	after, _ := f.store.read(apiPath)
	savedAfter, _ := f.store.read(path)
	if err != nil || replay != job || len(f.requests) != 2 || before != after || saved != savedAfter {
		t.Fatal("completed owner identity changed or reenrolled")
	}
	if _, err := deploymentServiceIdentityPath("service:shadow", otaProducerSealOwner); err == nil {
		t.Fatal("unknown owner/subject binding accepted")
	}
}

func otaProducerSecretFixture(t *testing.T, record, current deploymentServiceIdentity) map[string]any {
	t.Helper()
	state, _ := json.Marshal(map[string]any{"version": 1, "subject": "service:ota", "current": map[string]any{"private_key_pem": current.PrivateKey, "certificate_chain_pem": current.CertificateChain, "installed_at": time.Now().UTC()}})
	return map[string]any{"metadata": map[string]any{"uid": "original-uid", "annotations": map[string]any{"rtk.realtek.com/identity-owner": record.Owner, "rtk.realtek.com/environment": record.Environment, "rtk.realtek.com/stack": record.Stack, "rtk.realtek.com/service-root-sha256": record.RootSHA256}}, "data": map[string]any{"identity.json": base64.StdEncoding.EncodeToString(state)}}
}

func TestOTAProducerStateInstallerPreservesCurrentCredentialAndPinsUID(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	initial, err := ensureDeploymentServiceIdentityOwner(f.store, f.cfg.Stack, "service:ota", otaProducerSealOwner, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	initial.RuntimeSecretUID = "original-uid"
	initialPath, _ := deploymentServiceIdentityPath(initial.Subject, initial.Owner)
	if err := writeDeploymentServiceIdentity(f.store, initialPath, initial); err != nil {
		t.Fatal(err)
	}
	// A second issuer response supplies a valid same-subject replacement key.
	current, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	secret := otaProducerSecretFixture(t, initial, current)
	dir := t.TempDir()
	secretPath, logPath := filepath.Join(dir, "secret.json"), filepath.Join(dir, "calls.jsonl")
	payload, _ := json.Marshal(secret)
	if err := os.WriteFile(secretPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
with open(os.environ['OTA_IDENTITY_TEST_LOG'],'a') as f: f.write(json.dumps(args)+'\n')
if 'get' not in args or 'secret' not in args: sys.exit(9)
with open(os.environ['OTA_IDENTITY_TEST_SECRET']) as f: sys.stdout.write(f.read())
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("OTA_IDENTITY_TEST_SECRET", secretPath)
	t.Setenv("OTA_IDENTITY_TEST_LOG", logPath)
	env := map[string]string{"CLOUD_STACK_NAME": f.cfg.Stack, "CLOUD_ENV_NAME": "dev"}
	if err := installOTAProducerIdentityState(f.store, env, initial); err != nil {
		t.Fatal(err)
	}
	path, _ := deploymentServiceIdentityPath(initial.Subject, initial.Owner)
	raw, _ := f.store.read(path)
	var pinned deploymentServiceIdentity
	_ = json.Unmarshal([]byte(raw), &pinned)
	if pinned.RuntimeSecretUID != "original-uid" || pinned.PrivateKey != initial.PrivateKey {
		t.Fatal("operator provenance changed or UID was not saved")
	}
	if err := installOTAProducerIdentityState(f.store, env, pinned); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(secretPath)
	if string(actual) != string(payload) {
		t.Fatal("current credential was overwritten")
	}
	secret["metadata"].(map[string]any)["uid"] = "replacement-uid"
	replaced, _ := json.Marshal(secret)
	_ = os.WriteFile(secretPath, replaced, 0600)
	if err := installOTAProducerIdentityState(f.store, env, pinned); err == nil {
		t.Fatal("replacement Secret UID accepted")
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), `"create"`) || strings.Contains(string(calls), `"apply"`) || strings.Contains(string(calls), `"patch"`) {
		t.Fatal("existing owner state was mutated")
	}
}

func TestOTAProducerInitialStateRecoversUnknownCreateWithoutAnotherIdentity(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	record, err := ensureDeploymentServiceIdentityOwner(f.store, f.cfg.Stack, "service:ota", otaProducerSealOwner, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	statePath, logPath := filepath.Join(dir, "cluster-secret.json"), filepath.Join(dir, "calls.jsonl")
	bin := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env python3
import base64,json,os,sys
args=sys.argv[1:]
with open(os.environ['OTA_IDENTITY_CREATE_LOG'],'a') as f: f.write(json.dumps(args)+'\n')
path=os.environ['OTA_IDENTITY_CREATE_STATE']
if 'get' in args:
    if os.path.exists(path):
        with open(path) as f: sys.stdout.write(f.read())
elif 'create' in args:
    if os.path.exists(path): sys.exit(9)
    obj=json.load(sys.stdin)
    obj['metadata']['uid']='created-uid'
    obj['data']={k:base64.b64encode(v.encode()).decode() for k,v in obj.pop('stringData').items()}
    with open(path,'w') as f: json.dump(obj,f)
    sys.exit(3)
else: sys.exit(8)
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("OTA_IDENTITY_CREATE_LOG", logPath)
	t.Setenv("OTA_IDENTITY_CREATE_STATE", statePath)
	env := map[string]string{"CLOUD_STACK_NAME": f.cfg.Stack, "CLOUD_ENV_NAME": "dev"}
	if err := installOTAProducerIdentityState(f.store, env, record); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal("uncertain creation was claimed successful")
	}
	path, _ := deploymentServiceIdentityPath(record.Subject, record.Owner)
	raw, _ := f.store.read(path)
	var pending deploymentServiceIdentity
	_ = json.Unmarshal([]byte(raw), &pending)
	if pending.RuntimeSecretUID != "" || pending.PrivateKey != record.PrivateKey {
		t.Fatal("uncertain creation lost initial owner record")
	}
	if err := installOTAProducerIdentityState(f.store, env, record); err != nil {
		t.Fatal(err)
	}
	raw, _ = f.store.read(path)
	_ = json.Unmarshal([]byte(raw), &pending)
	if pending.RuntimeSecretUID != "created-uid" || len(f.requests) != 1 {
		t.Fatal("retry generated a replacement identity or did not persist UID")
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Count(string(calls), `"create"`) != 1 {
		t.Fatal("unknown create was blindly repeated")
	}
}

func TestOTAProducerMaintenanceManifestHasOnlyIdentityCredentials(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ENV_NAME": "staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video@sha256:" + strings.Repeat("a", 64), "LKE_OTA_PRODUCER_SEAL_FIRST_MONTH": "2026-11"}
	manifest, err := lkeOTAProducerSealJobManifest(env, "", "operator-identity", true)
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &job); err != nil {
		t.Fatal(err)
	}
	if job["kind"] != "Job" {
		t.Fatal("maintenance unexpectedly schedules a recurring job")
	}
	for _, forbidden := range []string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN", "VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "platform-service-identity", "persistentVolumeClaim"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("maintenance includes unrelated credential/storage %s", forbidden)
		}
	}
	for _, want := range []string{"--maintain-identity", "VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256", "/var/lib/ota-period-identity/state/identity.json", "kubernetes-secret", "ota-producer-period-seal-identity", "serviceAccountName: ota-producer-period-seal", "automountServiceAccountToken: false"} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("maintenance lacks %s", want)
		}
	}
	cron := lkeOTAProducerSealCronJobManifest(env)
	if !strings.Contains(cron, "suspend: true") || !strings.Contains(cron, `"--first-month", "2026-11"`) || strings.Contains(cron, "http://account-manager.") {
		t.Fatal("new schedule can close an unqualified month or use HTTP inventory")
	}
	rbac := lkeOTAProducerIdentityRBACManifest(env)
	if !strings.Contains(rbac, `resourceNames: ["ota-producer-period-seal-identity"]`) || strings.Contains(rbac, `"create"`) || strings.Contains(rbac, `"list"`) || strings.Contains(rbac, `"update"`) {
		t.Fatal("Job receives broad Secret rights")
	}
}

func TestOTAProducerSealMonthRequiresFullLatenessWindow(t *testing.T) {
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, elapsed := range []time.Duration{0, 24 * time.Hour, 48*time.Hour - time.Nanosecond} {
		if err := validateOTAProducerSealMonth("2026-11", end.Add(elapsed)); err == nil {
			t.Fatal("early source close accepted")
		}
	}
	if err := validateOTAProducerSealMonth("2026-11", end.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAProducerSealMonth("2026-1", end); err == nil {
		t.Fatal("malformed month accepted")
	}
}
