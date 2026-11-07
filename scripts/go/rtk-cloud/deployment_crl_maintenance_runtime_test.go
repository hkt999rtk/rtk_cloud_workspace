package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

type maintenanceTestLease struct{}

func (maintenanceTestLease) Check(ctx context.Context) error { return ctx.Err() }
func (maintenanceTestLease) Release(context.Context) error   { return nil }
func (maintenanceTestLease) Owner() deploymentplan.LeaseOwner {
	return deploymentplan.LeaseOwner{UID: "owned"}
}

type maintenanceRuntimeFixture struct {
	r                           *maintenanceRuntime
	item                        crlMaintenanceIssuer
	pod, controller, secret, cm deploymentplan.Object
	latest, installed           createRootCRLRecord
	public                      string
	identity                    []byte
	ack                         bool
	installs, patches           int
}

func maintenanceRuntimeTestFixture(t *testing.T) *maintenanceRuntimeFixture {
	t.Helper()
	item, key, now := maintenanceFixture(t, "staging", time.Now().UTC().Truncate(time.Second).Add(72*time.Hour))
	f := &maintenanceRuntimeFixture{item: item, latest: item.Snapshot.Prior, installed: item.Snapshot.Prior, public: item.Snapshot.Prior.PEM, ack: true}
	ca, _ := parseFirstCertificatePEM([]byte(item.Issuer.Certificate))
	leaf := &x509.Certificate{SerialNumber: big.NewInt(91), Subject: pkix.Name{CommonName: "service:actual-native-consumer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f.identity, _ = json.Marshal(map[string]any{"subject": "service:actual-native-consumer", "current": map[string]string{"certificate_chain_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + item.Issuer.Certificate}})
	c := deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "TEST_SERVER_CRL_MANIFEST", "value": "/trust/crls.json"}, deploymentplan.Object{"name": "TEST_CLIENT_IDENTITY_STATE", "value": "/state/client.json"}, deploymentplan.Object{"name": "TEST_CLIENT_CRL", "value": "/public/client.crl"}}, "volumeMounts": []any{deploymentplan.Object{"name": "trust", "mountPath": "/trust", "readOnly": true}, deploymentplan.Object{"name": "state", "mountPath": "/state"}, deploymentplan.Object{"name": "public", "mountPath": "/public", "readOnly": true}}}
	f.pod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "display-name-unrelated-abc", "namespace": "video-cloud-staging-video-cloud", "uid": "pod-owned", "labels": deploymentplan.Object{"app": "display-name-unrelated"}}, "spec": deploymentplan.Object{"containers": []any{c}, "volumes": []any{deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "trust"}}, deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-state"}}, deploymentplan.Object{"name": "public", "secret": deploymentplan.Object{"secretName": "original-public"}}}}, "status": deploymentplan.Object{"phase": "Running", "containerStatuses": []any{deploymentplan.Object{"name": "app", "ready": true}}}}
	f.controller = deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "env": []any{deploymentplan.Object{"name": "PKI_REQUIRED_CONSUMERS_SERVICE", "value": "actual-native-consumer"}}}}}}}}
	manifest, _ := json.Marshal([]any{map[string]any{"issuer": item.Issuer.Document, "state_path": "/state/root-crl.json"}})
	f.cm = deploymentplan.Object{"data": deploymentplan.Object{"crls.json": string(manifest)}}
	f.secret = deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "secret-original", "resourceVersion": "7"}, "data": deploymentplan.Object{"client.crl": base64.StdEncoding.EncodeToString([]byte(f.public)), "tls.key": "never-touch"}}
	f.r = &maintenanceRuntime{cfg: deploymentConfig{Environment: "staging", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}}, lease: maintenanceTestLease{}}
	f.r.readOverride = func(_ context.Context, id string) (deploymentplan.Object, error) {
		switch {
		case strings.HasPrefix(id, "Pod/"):
			return f.pod, nil
		case strings.HasPrefix(id, "ConfigMap/"):
			return f.cm, nil
		case strings.HasPrefix(id, "Secret/"):
			return f.secret, nil
		case strings.HasSuffix(id, "/pki-controller"):
			return f.controller, nil
		}
		return nil, errors.New("unexpected resource " + id)
	}
	f.r.commandOverride = func(_ context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "get pods"):
			return json.Marshal(map[string]any{"items": []any{f.pod}})
		case strings.Contains(joined, "json_agg(document"):
			return json.Marshal([]any{item.Issuer.Document})
		case strings.Contains(joined, "json_build_object('prior'"):
			return json.Marshal(createRootCRLNativeSnapshot{Prior: f.latest, Bindings: item.Snapshot.Bindings})
		case strings.Contains(joined, "SELECT EXISTS"):
			if strings.Contains(joined, "pki_service_client_issuances") && (!strings.Contains(joined, "status='succeeded'") || !strings.Contains(joined, "fingerprint='")) {
				t.Fatal("consumer read must match the native succeeded issuance and DER fingerprint")
			}
			if strings.Contains(joined, "pki_crl_acknowledgments") && !f.ack {
				return []byte("f"), nil
			}
			return []byte("t"), nil
		case strings.HasSuffix(joined, "cat /state/client.json"):
			return f.identity, nil
		case strings.HasSuffix(joined, "cat /state/root-crl.json"):
			return json.Marshal(map[string]any{"issuer_fingerprint": item.Issuer.RootFingerprint, "crl": f.installed})
		case strings.HasSuffix(joined, "cat /public/client.crl"):
			return []byte(f.public), nil
		case strings.Contains(joined, "/app/pkitrust apply-crl"):
			lines := strings.Split(string(input), "\n")
			if len(lines) != 3 {
				t.Fatal("native input not exactly issuer+record")
			}
			raw, e := base64.StdEncoding.DecodeString(lines[1])
			if e != nil || json.Unmarshal(raw, &f.installed) != nil {
				t.Fatal("invalid signed input")
			}
			f.installs++
			return nil, nil
		case strings.Contains(joined, "patch secret original-public"):
			var patch []map[string]any
			if json.Unmarshal([]byte(args[len(args)-1]), &patch) != nil || len(patch) != 3 || patch[0]["value"] != "secret-original" || patch[1]["value"] != "7" || patch[2]["path"] != "/data/client.crl" {
				t.Fatal("public patch lacks exact-owner or single-key guard")
			}
			body, e := base64.StdEncoding.DecodeString(patch[2]["value"].(string))
			if e != nil {
				t.Fatal(e)
			}
			f.public = string(body)
			f.patches++
			return nil, nil
		}
		return nil, errors.New("unexpected native command")
	}
	return f
}

func TestCRLMaintenanceNativeDiscoveryInstallationAndRealACK(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	inventory, err := f.r.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Consumers) != 1 || inventory.Consumers[0].ConsumerID != "actual-native-consumer" || len(inventory.Public) != 1 {
		t.Fatalf("incomplete actual consumer discovery: %+v", inventory)
	}
	records := map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest}
	if err := f.r.converge(context.Background(), inventory, records); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 || f.patches != 0 {
		t.Fatalf("healthy native convergence: installs=%d patches=%d", f.installs, f.patches)
	}
	f.ack = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := f.r.converge(ctx, inventory, records); err == nil || !strings.Contains(err.Error(), "ACK") {
		t.Fatalf("missing actual ACK accepted: %v", err)
	}
	deploymentplan.Map(f.pod["metadata"])["uid"] = "replacement"
	if err := f.r.install(context.Background(), inventory.Consumers[0], f.item.Issuer, f.latest); err == nil {
		t.Fatal("foreign Pod accepted")
	}
}

func TestCRLMaintenancePublicProjectionGuardsAndIdempotence(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	inventory, err := f.r.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := inventory.Public[0]
	record := f.latest
	record.PEM = "replacement public bytes"
	records := map[string]createRootCRLRecord{f.item.Issuer.ID: record}
	if err := f.r.publishPublicCRLs(context.Background(), []maintenancePublicCRL{original, original}, records); err != nil {
		t.Fatal(err)
	}
	if f.patches != 1 || deploymentplan.Map(f.secret["data"])["tls.key"] != "never-touch" {
		t.Fatal("public CRL patch mutated private field or duplicated")
	}
	original.Original = record.PEM
	if err := f.r.publishPublicCRLs(context.Background(), []maintenancePublicCRL{original}, records); err != nil || f.patches != 1 {
		t.Fatal("same projection not idempotent")
	}
	if err := f.r.verifyPublicCRLs(context.Background(), []maintenancePublicCRL{original}, records); err != nil {
		t.Fatal(err)
	}
	f.public = "old projection"
	if err := f.r.verifyPublicCRLs(context.Background(), []maintenancePublicCRL{original}, records); err == nil {
		t.Fatal("stale mounted projection accepted")
	}
	if _, err := maintenanceBundleIssuers([]byte("invalid"), map[string]environmentCreateRegisteredIssuer{}); err == nil {
		t.Fatal("invalid bundle accepted")
	}
}

func TestCRLMaintenancePolicyAndCLIRejectAmbiguousScope(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	p := environmentPKIPod(f.controller)
	c := deploymentplan.Map(certIssuerObjectList(p["containers"])[0])
	c["env"] = []any{deploymentplan.Object{"name": "PKI_REQUIRED_CONSUMERS", "value": "actual-native-consumer"}}
	if _, err := maintenanceRequiredConsumers(f.controller, []crlMaintenanceIssuer{f.item}); err != nil {
		t.Fatal(err)
	}
	c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "PKI_REQUIRED_CONSUMERS_SERVICE", "value": "other"})
	if _, err := maintenanceRequiredConsumers(f.controller, []crlMaintenanceIssuer{f.item}); err == nil {
		t.Fatal("mixed policy accepted")
	}
	c["env"] = []any{}
	if _, err := maintenanceRequiredConsumers(f.controller, []crlMaintenanceIssuer{f.item}); err == nil {
		t.Fatal("missing policy accepted")
	}
	for _, args := range [][]string{{}, {"--environment", "other"}, {"--environment", "prod", "--timeout", "0s"}, {"--environment", "staging", "--confirm", "video-cloud-staging"}, {"--environment", "dev", "unexpected"}} {
		if err := runCRLMaintenanceCLI(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	if err := runCRLMaintenanceCLI(context.Background(), []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestCRLMaintenancePrivateEvidenceNeverFollowsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, _ := newSecretStore(root, "staging")
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.Root, "deployment")); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceWrite(store, crlMaintenanceBase+"/status.json", map[string]bool{"changed": true}); err == nil {
		t.Fatal("followed environment symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "crl-maintenance/status.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside canonical store")
	}
}

func TestCRLMaintenanceExpiredRestartNeverTouchesStatefulOwner(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	inventory, err := f.r.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	meta := deploymentplan.Map(f.pod["metadata"])
	meta["ownerReferences"] = []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": "original-openbao", "controller": true}}
	calls := 0
	f.r.commandOverride = func(context.Context, []byte, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("stateful mutation prohibited")
	}
	if err := f.r.restartExpiredConsumers(context.Background(), inventory, map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("restarted a stateful trust provider")
	}
	meta["ownerReferences"] = []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "original-rs", "uid": "rs-uid", "controller": true}}
	originalRead := f.r.readOverride
	f.r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
		if strings.HasPrefix(id, "ReplicaSet/") {
			return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "replacement-rs"}}, nil
		}
		return originalRead(ctx, id)
	}
	if err := f.r.restartExpiredConsumers(context.Background(), inventory, map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest}); err == nil {
		t.Fatal("restarted a changed workload owner")
	}
	if calls != 0 {
		t.Fatal("issued mutation after owner mismatch")
	}
}

func TestCRLMaintenanceStaticClientCertificateUsesNativeReceipt(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	var state struct {
		Current struct {
			Chain string `json:"certificate_chain_pem"`
		} `json:"current"`
	}
	if json.Unmarshal(f.identity, &state) != nil {
		t.Fatal("fixture")
	}
	original := f.r.commandOverride
	f.r.commandOverride = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(strings.Join(args, " "), "cat /original/client.crt") {
			return []byte(state.Current.Chain), nil
		}
		return original(ctx, input, args...)
	}
	c := deploymentplan.Object{"env": []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_CERT", "value": "/original/client.crt"}, deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_KEY", "value": "/original/client.key"}}}
	id, err := f.r.consumerID(context.Background(), "ns", "pod", "mqttfoundation", c)
	if err != nil || id != "actual-native-consumer" {
		t.Fatalf("actual static identity %q %v", id, err)
	}
	f.r.commandOverride = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(strings.Join(args, " "), "cat /original/client.crt") {
			return []byte(state.Current.Chain), nil
		}
		return []byte("f"), nil
	}
	if _, err := f.r.consumerID(context.Background(), "ns", "pod", "mqttfoundation", c); err == nil {
		t.Fatal("unregistered static identity accepted")
	}
}

func TestCRLMaintenancePendingDeploymentRestartPreservesConfigurationAndRetries(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	inventory, err := f.r.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(f.pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "original-rs", "uid": "rs-uid", "controller": true}}
	rs := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "rs-uid", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "original-service", "uid": "deployment-uid", "controller": true}}}}
	originalSpec := deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": "original-sealed-session"}}, "volumeMounts": []any{deploymentplan.Object{"name": "original-identity", "mountPath": "/state"}}}}, "volumes": []any{deploymentplan.Object{"name": "original-identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-claim"}}}}
	owner := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "deployment-uid", "resourceVersion": "8"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{"annotations": deploymentplan.Object{"keep": "original"}}, "spec": originalSpec}}}
	originalDigest := deploymentplan.Digest(originalSpec)
	readOriginal := f.r.readOverride
	f.r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
		if strings.HasPrefix(id, "ReplicaSet/") {
			return rs, nil
		}
		if strings.HasSuffix(id, "/original-service") {
			return owner, nil
		}
		return readOriginal(ctx, id)
	}
	patches, waits := 0, 0
	f.r.commandOverride = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "patch deployment original-service") {
			var patch []map[string]any
			if json.Unmarshal([]byte(args[len(args)-1]), &patch) != nil || len(patch) != 3 || patch[0]["value"] != "deployment-uid" || patch[1]["value"] != "8" || patch[2]["path"] != "/spec/template/metadata/annotations" {
				t.Fatal("unguarded or broad restart patch")
			}
			annotations := patch[2]["value"].(map[string]any)
			if annotations["keep"] != "original" {
				t.Fatal("lost existing annotation")
			}
			deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(owner["spec"])["template"])["metadata"])["annotations"] = annotations
			patches++
			return nil, nil
		}
		if strings.Contains(joined, "rollout status deployment/original-service") {
			waits++
			return nil, nil
		}
		return nil, errors.New("unexpected recovery mutation")
	}
	records := map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest}
	for n := 0; n < 2; n++ {
		if err := f.r.restartExpiredConsumers(context.Background(), inventory, records); err != nil {
			t.Fatal(err)
		}
	}
	if patches != 1 || waits != 2 || deploymentplan.Digest(originalSpec) != originalDigest {
		t.Fatalf("restart replay mutated configuration: patches=%d waits=%d", patches, waits)
	}
}
