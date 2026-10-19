package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productOpenBaoTrustFixture(t *testing.T, caLifetime ...time.Duration) (*productProtocolFixture, []deploymentplan.Definition) {
	t.Helper()
	f := (*productProtocolFixture)(nil)
	if len(caLifetime) == 0 {
		f = newProductProtocolFixture(t, true)
	} else {
		f = newProductProtocolFixtureLifetime(t, true, caLifetime[0])
	}
	ns := f.p.Stack + "-video-cloud"
	id := "ConfigMap/" + ns + "/pki-create-openbao-tls-consumer-trust"
	f.l.SourceOwners[id] = "4543596a-cf3b-4ac8-ae15-931c2ee7a106"
	f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-openbao-tls-consumer-trust"}}, Phase: "configuration", Retain: true}, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": f.p.Stack + "-secrets", "name": "openbao"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao-pki", "env": environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_ROOT_SHA256": f.l.Root.RootFingerprint})}}}}}}, Phase: "workload"})
	s := environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, BootstrapSessionID: "11111111-2222-4333-8444-555555555555", BootstrapClaim: "pki-product-bootstrap-" + productIdentityScope(f.l)[:12]}
	defs, err := productStageDefinitions(t.Context(), f.cfg, f.l, s, "service-session")
	if err != nil {
		t.Fatal(err)
	}
	return f, defs
}

func TestProductIdentityManagedOpenBaoTrustKeepsOldRecipeAndFrozenPins(t *testing.T) {
	f, original := productOpenBaoTrustFixture(t)
	originalSHA, scope := deploymentplan.Digest(original), productIdentityScope(f.l)
	fixed, err := bindProductIdentityOpenBaoTrust(f.l, original)
	if err != nil {
		t.Fatal(err)
	}
	currentTrust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": f.l.Bootstrap.Stack + "-video-cloud", "name": "pki-create-openbao-tls-consumer-trust", "uid": f.l.SourceOwners["ConfigMap/"+f.l.Bootstrap.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust"]}, "data": deploymentplan.Object{"roots.pem": f.l.Root.Certificate}}
	if err := validateProductIdentityOpenBaoTrust(f.l, currentTrust, time.Now().UTC()); err != nil {
		t.Fatal("frozen current listener root rejected", err)
	}
	for _, mode := range []string{"uid", "foreign-valid-root", "expired"} {
		t.Run("live-"+mode, func(t *testing.T) {
			object, now := deploymentplan.Clone(currentTrust), time.Now().UTC()
			switch mode {
			case "uid":
				deploymentplan.Map(object["metadata"])["uid"] = "11111111-2222-4333-8444-555555555555"
			case "foreign-valid-root":
				deploymentplan.Map(object["data"])["roots.pem"] = f.runtime.TransportCAPEM
			case "expired":
				cert, _ := parseFirstCertificatePEM([]byte(f.l.Root.Certificate))
				now = cert.NotAfter.Add(time.Second)
			}
			if err := validateProductIdentityOpenBaoTrust(f.l, object, now); err == nil {
				t.Fatal("unbound current trust accepted")
			}
		})
	}
	for i, d := range fixed {
		if d.Object["kind"] != "Job" {
			continue
		}
		volumes, _ := environmentPKINamedFields(environmentPKIPod(d.Object)["volumes"])
		projection := deploymentplan.Map(volumes["openbao-ca"]["configMap"])
		if projection["name"] != "pki-create-openbao-tls-consumer-trust" || deploymentplan.Digest(projection["items"]) != deploymentplan.Digest([]any{deploymentplan.Object{"key": "roots.pem", "path": "ca.crt"}}) {
			t.Fatal("wrong managed projection")
		}
		oldVolumes, _ := environmentPKINamedFields(environmentPKIPod(original[i].Object)["volumes"])
		volumes["openbao-ca"]["configMap"] = oldVolumes["openbao-ca"]["configMap"]
	}
	if deploymentplan.Digest(fixed) != originalSHA || deploymentplan.Digest(original) != originalSHA || productIdentityScope(f.l) != scope {
		t.Fatal("shared adapter changed identity, original recipe, paths or scope")
	}
	var future createNormalTailProductLifecycle
	_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.l), &future)
	future.OpenBaoTrustProfile = productIdentityManagedOpenBaoTrustProfile
	session := environmentCreateServiceCeremony{Root: future.Root, Intermediate: f.next, BootstrapSessionID: "11111111-2222-4333-8444-555555555555", BootstrapClaim: "pki-product-bootstrap-" + productIdentityScope(&future)[:12]}
	futureDefinitions, err := productStageDefinitions(t.Context(), f.cfg, &future, session, "service-session")
	if err != nil {
		t.Fatal("new managed profile did not render", err)
	}
	futureJobs := 0
	for _, d := range futureDefinitions {
		if d.Object["kind"] == "Job" {
			futureJobs++
			volumes, _ := environmentPKINamedFields(environmentPKIPod(d.Object)["volumes"])
			projection := deploymentplan.Map(volumes["openbao-ca"]["configMap"])
			if projection["name"] != "pki-create-openbao-tls-consumer-trust" || deploymentplan.Digest(projection["items"]) != deploymentplan.Digest([]any{deploymentplan.Object{"key": "roots.pem", "path": "ca.crt"}}) {
				t.Fatal("new program did not select current managed trust")
			}
		}
	}
	if futureJobs != 1 || productIdentityScope(f.l) != scope {
		t.Fatal("new profile changed original scope")
	}
	encoded, _ := json.Marshal(f.l)
	if strings.Contains(string(encoded), "OpenBaoTrustProfile") {
		t.Fatal("old descriptor wire bytes gained a zero profile")
	}
	for _, mode := range []string{"owner", "root", "endpoint", "path", "mount", "source", "profile"} {
		t.Run(mode, func(t *testing.T) {
			var l createNormalTailProductLifecycle
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.l), &l)
			var defs []deploymentplan.Definition
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(original), &defs)
			for _, d := range defs {
				if d.Object["kind"] != "Job" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				v, _ := environmentPKINamedFields(pod["volumes"])
				m, _ := environmentPKINamedFields(c["volumeMounts"])
				switch mode {
				case "endpoint":
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"OPENBAO_ADDR": "https://foreign.invalid:8200"}))
				case "path":
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"OPENBAO_CACERT": "/foreign/ca.crt"}))
				case "mount":
					m["openbao-ca"]["readOnly"] = false
				case "source":
					deploymentplan.Map(v["openbao-ca"]["configMap"])["name"] = "foreign"
				}
			}
			switch mode {
			case "owner":
				delete(l.SourceOwners, "ConfigMap/"+l.Bootstrap.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust")
			case "root":
				for _, d := range l.SourceDefinitions {
					if d.Object["kind"] == "StatefulSet" {
						c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
						c["env"] = environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_ROOT_SHA256": "malformed"})
					}
				}
			case "profile":
				l.OpenBaoTrustProfile = "foreign"
			}
			if mode == "profile" {
				if _, err := bindProductIdentityOpenBaoTrust(&l, defs); err == nil {
					t.Fatal("unknown adapter profile accepted")
				}
				s := environmentCreateServiceCeremony{Root: l.Root, Intermediate: f.next, BootstrapSessionID: "11111111-2222-4333-8444-555555555555", BootstrapClaim: "pki-product-bootstrap-" + productIdentityScope(&l)[:12]}
				if _, err := productStageDefinitions(t.Context(), f.cfg, &l, s, "service-session"); err == nil {
					t.Fatal("unknown profile accepted")
				}
				return
			}
			if _, err := bindProductIdentityOpenBaoTrust(&l, defs); err == nil {
				t.Fatal("unbound trust rewrite accepted")
			}
		})
	}
}

func TestProductIdentityRenderedJobRunsNativeBootstrapWithCurrentOpenBaoCA(t *testing.T) {
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
		t.Skip("isolated PostgreSQL test runtime not configured")
	}
	f, original := productOpenBaoTrustFixture(t)
	managed, err := bindProductIdentityOpenBaoTrust(f.l, original)
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]deploymentplan.Object{}
	for _, row := range []struct {
		name string
		defs []deploymentplan.Definition
	}{{"original", original}, {"managed", managed}} {
		for _, d := range row.defs {
			if d.Object["kind"] == "Job" {
				jobs[row.name] = d.Object
			}
		}
	}
	oldKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := &x509.Certificate{Subject: f.root.Subject, SerialNumber: big.NewInt(707), NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	oldDER, err := x509.CreateCertificate(rand.Reader, oldRoot, oldRoot, oldKey.Public(), oldKey)
	if err != nil {
		t.Fatal(err)
	}
	oldCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldDER}))
	rootKey, _ := x509.MarshalPKCS8PrivateKey(f.rootKey)
	icaKey, _ := x509.MarshalPKCS8PrivateKey(f.icaKey)
	fixture := deploymentplan.Object{"jobs": jobs, "root": f.l.Root.Certificate, "intermediate": f.next.Certificate, "root_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rootKey})), "intermediate_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: icaKey})), "old_ca": oldCA}
	path := filepath.Join(t.TempDir(), "rendered-private-fixture.json")
	if err := os.WriteFile(path, mustProviderRecoveryJSON(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"test"}
	if overlay := os.Getenv("RTK_PRODUCT_BOOTSTRAP_NATIVE_OVERLAY"); overlay != "" {
		args = append(args, "-overlay="+overlay)
	}
	args = append(args, "./cmd/serviceidentity-bootstrap", "-run", "^TestNativeRenderedBootstrapOpenBaoTrustAndSameKeyResume$", "-count=1", "-v", "-timeout=90s")
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = filepath.Join(f.cfg.Workspace, "repos/rtk_video_cloud")
	command.Env = append(os.Environ(), "GOWORK=off", "RTK_PRODUCT_BOOTSTRAP_RENDERED_FIXTURE="+path)
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native rendered-job bootstrap regression failed: %v; %s", err, raw)
	}
	if !strings.Contains(string(raw), "--- PASS: TestNativeRenderedBootstrapOpenBaoTrustAndSameKeyResume") {
		t.Fatal("native regression did not execute")
	}
	t.Log("actual native bootstrap: old CA refused before sign/session, current CA issued once, original key/CSR and session resumed")
}
