package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type onlineRuntimeTestLease struct{ checks, failAt int }

func (l *onlineRuntimeTestLease) Owner() deploymentplan.LeaseOwner {
	return deploymentplan.LeaseOwner{UID: "online-test-owner"}
}
func (l *onlineRuntimeTestLease) Check(ctx context.Context) error {
	l.checks++
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.failAt > 0 && l.checks >= l.failAt {
		return errors.New("original lease lost")
	}
	return nil
}
func (l *onlineRuntimeTestLease) Release(context.Context) error { return nil }

type onlineRuntimeTestFixture struct {
	t                                             *testing.T
	dir                                           string
	runtime                                       maintenanceRuntime
	item                                          crlMaintenanceIssuer
	successor                                     createRootCRLRecord
	inventory                                     crlMaintenanceInventory
	records                                       map[string]createRootCRLRecord
	lease                                         *onlineRuntimeTestLease
	snapshotCalls, installCalls                   int
	snapshotFailureAt                             int
	registryMismatch, installFailure, podReplaced bool
}

// The process boundary stands in for Kubernetes and its native helper. Real
// adapter code builds the helper, checks its UID, orders dry-run before execute,
// verifies signed readback, writes private evidence and packages installation.
func newOnlineRuntimeTestFixture(t *testing.T) *onlineRuntimeTestFixture {
	t.Helper()
	item, key, now := maintenanceFixture(t, "staging", time.Now().UTC().Truncate(time.Second).Add(-time.Hour))
	item.Issuer.Kind = "intermediate"
	item.Issuer.Reference = "pki-issuers/service/" + item.Issuer.ID + "/v1"
	item.Issuer.Document = map[string]any{"id": item.Issuer.ID, "environment": "staging", "trust_domain": "service", "kind": "intermediate", "signer_provider": "openbao", "certificate_fingerprint_sha256": item.Issuer.RootFingerprint, "certificate_pem": item.Issuer.Certificate}
	successor := maintenanceSign(t, item.Issuer, key, createRootCRLRequest{IssuerID: item.Issuer.ID, Fingerprint: item.Issuer.RootFingerprint, Number: "5", ThisUpdate: now, NextUpdate: now.Add(24 * time.Hour), Entries: []createRootCRLRevocation{{Serial: "ab", RevokedAt: now.Add(-20 * 24 * time.Hour), Reason: 1}, {Serial: "cd", RevokedAt: now.Add(-24 * time.Hour)}}})
	dir := t.TempDir()
	store, err := newSecretStore(filepath.Join(dir, "isolated-config"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	f := &onlineRuntimeTestFixture{t: t, dir: dir, item: item, successor: successor, lease: &onlineRuntimeTestLease{}, records: map[string]createRootCRLRecord{item.Issuer.ID: item.Snapshot.Prior}}
	consumer := crlMaintenanceConsumer{Namespace: "video-cloud-staging-video-cloud", Pod: "original-consumer", PodUID: "original-consumer-uid", Container: "api", StatePath: "/original/state/service-crl.json", IssuerID: item.Issuer.ID, ConsumerID: "api"}
	f.inventory = crlMaintenanceInventory{Issuers: []crlMaintenanceIssuer{item}, Consumers: []crlMaintenanceConsumer{consumer}}
	f.runtime = maintenanceRuntime{cfg: deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}, store: store, kubeconfig: filepath.Join(dir, "frozen-kubeconfig"), image: "ghcr.io/test/api@sha256:" + strings.Repeat("a", 64), lease: f.lease, trustBinary: []byte("synthetic pinned installer")}
	f.writeJSON("result.json", map[string]any{"status": "imported", "environment": "staging", "crl": successor})
	// Only original public CA and audience-scoped token references enter the
	// helper manifest. No real credentials or environment files are used.
	controller := `{"spec":{"template":{"spec":{"serviceAccountName":"original-controller","containers":[{"name":"pki-controller","env":[{"name":"PKI_DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"original-db","key":"url"}}},{"name":"PKI_ENVIRONMENT","value":"staging"},{"name":"OPENBAO_AUTH_METHOD","value":"kubernetes"},{"name":"OPENBAO_ADDR","value":"https://original-openbao:8200"},{"name":"OPENBAO_CACERT","value":"/original-ca/roots.pem"},{"name":"OPENBAO_KUBERNETES_ROLE","value":"original-role"},{"name":"OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE","value":"/original-auth/token"},{"name":"OPENBAO_SERVER_PKI_ROOT_SHA256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"volumeMounts":[{"name":"ca","mountPath":"/original-ca","readOnly":true},{"name":"auth","mountPath":"/original-auth","readOnly":true}]}],"volumes":[{"name":"ca","configMap":{"name":"original-ca"}},{"name":"auth","projected":{"sources":[{"serviceAccountToken":{"audience":"openbao","path":"token","expirationSeconds":3600}}]}}]}}}}`
	f.write("controller.json", []byte(controller))
	script := `#!/bin/sh
set -eu
case "$*" in
 *'get deployment pki-controller'*) cat "$CRL_ONLINE_TEST_DIR/controller.json" ;;
 *'create -f -'*)
   printf 'create\n' >> "$CRL_ONLINE_TEST_DIR/events"
   cat > "$CRL_ONLINE_TEST_DIR/manifest.json"
   printf '%s\n' '{"metadata":{"name":"online-helper-original","uid":"online-helper-uid"}}' ;;
 *'get pod online-helper-original'*) printf '%s\n' '{"metadata":{"uid":"online-helper-uid"}}' ;;
 *'wait --for=condition=Ready'*) ;;
 *'recovery-renew-online-crl'*)
   if [ -f "$CRL_ONLINE_TEST_DIR/mode" ]; then mode=$(cat "$CRL_ONLINE_TEST_DIR/mode"); else mode=ok; fi
   case "$*" in
    *'--confirm staging'*)
      printf 'execute\n' >> "$CRL_ONLINE_TEST_DIR/events"
      printf '%s\n' "$@" > "$CRL_ONLINE_TEST_DIR/execute-args"
      if [ "$mode" = execute-failure ]; then exit 21; fi
      : > "$CRL_ONLINE_TEST_DIR/published"
      if [ "$mode" = ambiguous ]; then exit 22; fi
      cat "$CRL_ONLINE_TEST_DIR/result.json" ;;
    *)
      printf 'dry\n' >> "$CRL_ONLINE_TEST_DIR/events"
      printf '%s\n' "$@" > "$CRL_ONLINE_TEST_DIR/dry-args"
      if [ "$mode" = dry-failure ]; then exit 20; fi
      printf '%s\n' '{"status":"qualified"}' ;;
   esac ;;
 *'delete --raw'*)
   printf 'cleanup\n' >> "$CRL_ONLINE_TEST_DIR/events"
   cat > "$CRL_ONLINE_TEST_DIR/delete.json"
   if [ -f "$CRL_ONLINE_TEST_DIR/cleanup-failure" ]; then exit 23; fi ;;
 *) exit 24 ;;
esac
`
	f.write("kubectl", []byte(script))
	if err := os.Chmod(filepath.Join(dir, "kubectl"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CRL_ONLINE_TEST_DIR", dir)
	f.runtime.commandOverride = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(args) > 1 && args[len(args)-2] == "crl-maintenance-read" {
			f.snapshotCalls++
			f.event("snapshot")
			if f.snapshotFailureAt == f.snapshotCalls {
				return nil, errors.New("registry unavailable")
			}
			if !strings.Contains(args[len(args)-1], "issuer_id='"+item.Issuer.ID+"'") {
				t.Fatal("snapshot lost issuer pin")
			}
			record := item.Snapshot.Prior
			if _, err := os.Stat(filepath.Join(dir, "published")); err == nil && !f.registryMismatch {
				record = f.successor
			}
			return json.Marshal(createRootCRLNativeSnapshot{Prior: record})
		}
		if len(args) > 1 && args[len(args)-2] == "crl-writer-access" && args[len(args)-1] == consumer.StatePath {
			return []byte("10001 10001 700 10001 10001 yes 10001\n"), nil
		}
		if len(args) > 1 && args[len(args)-2] == "cat" && args[len(args)-1] == consumer.StatePath {
			return nil, errors.New("isolated current state absent")
		}
		if len(args) > 2 && args[len(args)-3] == "crl-maintenance" {
			prefix := []string{"--namespace", consumer.Namespace, "exec", "-i", "pod/" + consumer.Pod, "--container", consumer.Container, "--", "/bin/sh", "-ec"}
			if len(args) != 14 || !reflect.DeepEqual(args[:10], prefix) || !strings.Contains(args[10], `"$directory/pkitrust" apply-crl`) {
				t.Fatalf("installation changed original native consumer: %v", args)
			}
			f.event("install")
			f.installCalls++
			if args[len(args)-2] != consumer.StatePath {
				t.Fatal("installation changed original durable path")
			}
			lines := strings.Split(strings.TrimSpace(string(input)), "\n")
			if len(lines) != 3 {
				t.Fatal("native install input framing changed")
			}
			binary, err := base64.StdEncoding.DecodeString(lines[0])
			if err != nil || string(binary) != "synthetic pinned installer" || args[len(args)-1] != environmentPreflightBytesSHA256(binary) {
				t.Fatal("installer bytes lost their pinned digest")
			}
			issuerRaw, err := base64.StdEncoding.DecodeString(lines[1])
			if err != nil {
				t.Fatal(err)
			}
			var issuerDoc map[string]any
			if err := json.Unmarshal(issuerRaw, &issuerDoc); err != nil {
				t.Fatal(err)
			}
			if issuerDoc["environment"] != "staging" || issuerDoc["id"] != item.Issuer.ID {
				t.Fatal("installation lost issuer/environment pin")
			}
			recordRaw, err := base64.StdEncoding.DecodeString(lines[2])
			if err != nil {
				t.Fatal(err)
			}
			var record createRootCRLRecord
			if err := json.Unmarshal(recordRaw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Digest != f.successor.Digest || record.PEM != f.successor.PEM {
				t.Fatal("installed different signed CRL")
			}
			if f.installFailure {
				return nil, errors.New("native installer unavailable")
			}
			return nil, nil
		}
		t.Fatalf("unexpected native command: %v", args)
		return nil, errors.New("unexpected command")
	}
	f.runtime.readOverride = func(_ context.Context, id string) (deploymentplan.Object, error) {
		if id != "Pod/"+consumer.Namespace+"/"+consumer.Pod {
			t.Fatalf("wrong consumer owner read: %s", id)
		}
		uid := consumer.PodUID
		if f.podReplaced {
			uid = "replacement-uid"
		}
		return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": uid}}, nil
	}
	return f
}

func (f *onlineRuntimeTestFixture) write(name string, data []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *onlineRuntimeTestFixture) writeJSON(name string, value any) {
	f.t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(name, raw)
}
func (f *onlineRuntimeTestFixture) event(event string) {
	f.t.Helper()
	file, err := os.OpenFile(filepath.Join(f.dir, "events"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := file.WriteString(event + "\n"); err != nil {
		f.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		f.t.Fatal(err)
	}
}
func (f *onlineRuntimeTestFixture) events() []string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "events"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Fields(string(raw))
}
func (f *onlineRuntimeTestFixture) run() error {
	return f.runtime.recoverOnline(context.Background(), f.inventory, f.records)
}
func (f *onlineRuntimeTestFixture) evidencePath() string {
	return filepath.Join(f.runtime.store.Root, crlMaintenanceBase, "online", f.item.Issuer.ID, f.item.Snapshot.Prior.Digest+".json")
}

func TestCRLMaintenanceOnlineRuntimeGuardsPublicationBeforeInstallation(t *testing.T) {
	f := newOnlineRuntimeTestFixture(t)
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "create", "dry", "execute", "snapshot", "install", "cleanup"}
	if !reflect.DeepEqual(f.events(), want) {
		t.Fatalf("unsafe execution order: %v", f.events())
	}
	expected := []string{"recovery-renew-online-crl", "staging", f.item.Issuer.ID, f.item.Issuer.RootFingerprint, f.item.Issuer.Reference, f.item.Snapshot.Prior.Digest}
	for _, phase := range []string{"dry", "execute"} {
		raw, err := os.ReadFile(filepath.Join(f.dir, phase+"-args"))
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSpace(string(raw)), "\n")
		start := -1
		for i, arg := range args {
			if arg == "recovery-renew-online-crl" {
				start = i
				break
			}
		}
		want := append([]string(nil), expected...)
		if phase == "execute" {
			want = append(want, "--confirm", "staging")
		}
		if start < 0 || !reflect.DeepEqual(args[start:], want) {
			t.Fatalf("%s lost original authority: %v", phase, args)
		}
		if args[0] != "--kubeconfig" || args[1] != f.runtime.kubeconfig {
			t.Fatal("helper changed frozen kubeconfig")
		}
	}
	if f.records[f.item.Issuer.ID].Digest != f.successor.Digest {
		t.Fatal("accepted successor missing from convergence records")
	}
	raw, err := os.ReadFile(f.evidencePath())
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]json.RawMessage
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence["prior"]) == 0 || len(evidence["result"]) == 0 || len(evidence["published_at"]) == 0 {
		t.Fatalf("incomplete publication evidence: %s", raw)
	}
	info, err := os.Stat(f.evidencePath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("publication evidence is not private", err)
	}
	var deletion struct{ Preconditions struct{ UID string } }
	raw, err = os.ReadFile(filepath.Join(f.dir, "delete.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &deletion); err != nil || deletion.Preconditions.UID != "online-helper-uid" {
		t.Fatal("helper cleanup lost UID precondition")
	}
}

func TestCRLMaintenanceOnlineRuntimeUsesWorkerSuccessorWithoutRotation(t *testing.T) {
	f := newOnlineRuntimeTestFixture(t)
	f.write("published", nil)
	f.runtime.image = ""
	f.runtime.ensureImage = func(context.Context) (string, error) {
		t.Fatal("healthy native worker required a helper image")
		return "", nil
	}
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events(), []string{"snapshot", "install"}) {
		t.Fatalf("unnecessary online mutation: %v", f.events())
	}
	if f.records[f.item.Issuer.ID].Digest != f.successor.Digest {
		t.Fatal("stale initial inventory won over native worker")
	}
	if _, err := os.Stat(f.evidencePath()); !os.IsNotExist(err) {
		t.Fatal("healthy worker fabricated recovery evidence")
	}
}

func TestCRLMaintenanceOnlineRuntimeRetriesAmbiguousPublicationAndFailedInstall(t *testing.T) {
	for _, failure := range []string{"ambiguous", "install"} {
		t.Run(failure, func(t *testing.T) {
			f := newOnlineRuntimeTestFixture(t)
			if failure == "ambiguous" {
				f.write("mode", []byte("ambiguous"))
			} else {
				f.installFailure = true
			}
			if err := f.run(); err == nil {
				t.Fatal("interrupted recovery reported success")
			}
			first := f.events()
			if first[len(first)-1] != "cleanup" {
				t.Fatalf("helper leaked on failure: %v", first)
			}
			before := strings.Count(strings.Join(first, " "), "execute")
			f.installFailure = false
			if err := f.run(); err != nil {
				t.Fatal(err)
			}
			after := f.events()
			if strings.Count(strings.Join(after, " "), "execute") != before {
				t.Fatal("retry rotated an already published successor")
			}
			if after[len(after)-1] != "install" || f.records[f.item.Issuer.ID].Digest != f.successor.Digest {
				t.Fatal("retry did not install genuine registry successor")
			}
		})
	}
}

func TestCRLMaintenanceOnlineRuntimeRejectsFailedOrUntrustedReadback(t *testing.T) {
	cases := []struct {
		name   string
		change func(*onlineRuntimeTestFixture)
		want   string
	}{
		{"snapshot", func(f *onlineRuntimeTestFixture) { f.snapshotFailureAt = 1 }, "registry read failed"},
		{"dry-run", func(f *onlineRuntimeTestFixture) { f.write("mode", []byte("dry-failure")) }, "qualification"},
		{"execution", func(f *onlineRuntimeTestFixture) { f.write("mode", []byte("execute-failure")) }, "recovery"},
		{"malformed-result", func(f *onlineRuntimeTestFixture) { f.write("result.json", []byte("not JSON")) }, "readback differs"},
		{"wrong-environment", func(f *onlineRuntimeTestFixture) {
			f.writeJSON("result.json", map[string]any{"status": "imported", "environment": "prod", "crl": f.successor})
		}, "readback differs"},
		{"wrong-status", func(f *onlineRuntimeTestFixture) {
			f.writeJSON("result.json", map[string]any{"status": "qualified", "environment": "staging", "crl": f.successor})
		}, "readback differs"},
		{"wrong-issuer", func(f *onlineRuntimeTestFixture) {
			record := f.successor
			record.IssuerID = "22222222-2222-4222-8222-222222222222"
			f.writeJSON("result.json", map[string]any{"status": "imported", "environment": "staging", "crl": record})
		}, "readback differs"},
		{"invalid-signature", func(f *onlineRuntimeTestFixture) {
			record := f.successor
			record.PEM = "not a signed CRL"
			f.writeJSON("result.json", map[string]any{"status": "imported", "environment": "staging", "crl": record})
		}, "invalid"},
		{"registry-readback", func(f *onlineRuntimeTestFixture) { f.snapshotFailureAt = 2 }, "registry read failed"},
		{"registry-diverged", func(f *onlineRuntimeTestFixture) { f.registryMismatch = true }, "registry differs"},
		{"consumer-replaced", func(f *onlineRuntimeTestFixture) { f.podReplaced = true }, "consumer Pod changed"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newOnlineRuntimeTestFixture(t)
			tt.change(f)
			err := f.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
			if f.installCalls != 0 {
				t.Fatal("unqualified state reached consumer installation")
			}
			events := f.events()
			if tt.name != "snapshot" && events[len(events)-1] != "cleanup" {
				t.Fatalf("failed operation leaked helper: %v", events)
			}
			if tt.name == "dry-run" && strings.Contains(strings.Join(events, " "), "execute") {
				t.Fatal("failed dry-run reached mutation")
			}
		})
	}
}

func TestCRLMaintenanceOnlineRuntimeRequiresLeaseAndCIQualification(t *testing.T) {
	cases := []struct {
		name   string
		change func(*onlineRuntimeTestFixture)
		want   string
	}{
		{"no-owner", func(f *onlineRuntimeTestFixture) { f.runtime.lease = nil }, "ownership"},
		{"lost-owner", func(f *onlineRuntimeTestFixture) { f.lease.failAt = 1 }, "lease lost"},
		{"lost-before-execute", func(f *onlineRuntimeTestFixture) { f.lease.failAt = 2 }, "lease lost"},
		{"missing-image-resolver", func(f *onlineRuntimeTestFixture) { f.runtime.image = "" }, "resolver missing"},
		{"failed-image-qualification", func(f *onlineRuntimeTestFixture) {
			f.runtime.image = ""
			f.runtime.ensureImage = func(context.Context) (string, error) { return "", errors.New("CI provenance mismatch") }
		}, "CI provenance mismatch"},
		{"wrong-environment", func(f *onlineRuntimeTestFixture) { f.runtime.cfg.Environment = "prod" }, "environment differs"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newOnlineRuntimeTestFixture(t)
			tt.change(f)
			err := f.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
			if f.installCalls != 0 || strings.Contains(strings.Join(f.events(), " "), "execute") {
				t.Fatal("lost authorization reached mutation")
			}
		})
	}
}

func TestCRLMaintenanceOnlineRuntimeCleanupFailureIsNotSuccess(t *testing.T) {
	f := newOnlineRuntimeTestFixture(t)
	f.write("cleanup-failure", nil)
	if err := f.run(); err == nil {
		t.Fatal("failed helper cleanup reported success")
	}
	if f.installCalls != 1 || f.records[f.item.Issuer.ID].Digest != f.successor.Digest {
		t.Fatal("cleanup failure discarded actual successful publication")
	}
}

func TestCRLMaintenanceOnlineRuntimeLeavesOfflineSignerUntouched(t *testing.T) {
	f := newOnlineRuntimeTestFixture(t)
	f.inventory.Issuers[0].Issuer.Document["signer_provider"] = "offline"
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if len(f.events()) != 0 {
		t.Fatal("online adapter touched offline signing scope")
	}
}
