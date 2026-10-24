package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productRenewalJobFixture(t *testing.T) (*productForwardFixture, productSessionRenewalSource, deploymentplan.Object) {
	t.Helper()
	f := newProductForwardFixture(t)
	s := productSessionRenewalSource{Schema: productSessionRenewalSchema, ParentSHA256: f.l.ParentSHA256, ScopeSHA256: productIdentityScope(f.l), OriginalSession: productSessionInstallations{Session: f.service.BootstrapSessionID}, KeySHA256: environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.key"])), CSRSHA256: environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.csr"]))}
	s.SessionID, s.DeploymentID, s.CertificatePath = productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, s.OriginalSession.Session)
	return f, s, f.old.Resources[2].Desired
}
func TestProductSessionRenewalJobKeepsOriginalKeyCSRAndCurrentTrust(t *testing.T) {
	f, s, original := productRenewalJobFixture(t)
	before := deploymentplan.Digest(original)
	defs, err := productSessionRenewalJobDefinitions(f.cfg, f.l, s, original)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Object["kind"] != "Job" || deploymentplan.Digest(original) != before {
		t.Fatal("original recipe changed")
	}
	pod := environmentPKIPod(defs[0].Object)
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if deploymentplan.Digest(c["command"]) != deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["command"]) {
		t.Fatal("native command changed")
	}
	for k, want := range map[string]string{"PKI_BOOTSTRAP_SESSION_ID": s.SessionID, "PKI_BOOTSTRAP_DEPLOYMENT_ID": s.DeploymentID, "PKI_BOOTSTRAP_CERT_PATH": s.CertificatePath, "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr"} {
		if mqttFoundationEnv(c, k) != want {
			t.Fatal("wrong native renewal input", k)
		}
	}
	if mqttFoundationEnv(c, "PKI_BOOTSTRAP_EXPIRES_AT") != "" || deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
		t.Fatal("renewal resets expiry or retained custody permissions")
	}
	volumes, _ := environmentPKINamedFields(pod["volumes"])
	if deploymentplan.Map(volumes["openbao-auth"]["projected"])["defaultMode"] != float64(0444) || deploymentplan.Map(volumes["bootstrap-state"]["persistentVolumeClaim"])["claimName"] != f.service.BootstrapClaim {
		t.Fatal("token or original claim changed")
	}
	if deploymentplan.Map(volumes["openbao-ca"]["configMap"])["name"] != "pki-create-openbao-tls-consumer-trust" {
		t.Fatal("expired transport CA selected")
	}
	init := certIssuerObjectList(pod["initContainers"])
	if len(init) != 2 || deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(init[0])["volumeMounts"])[0])["readOnly"] != true {
		t.Fatal("original custody guard is not read-only")
	}
	guard := deploymentplan.Map(init[0])["command"].([]any)[2].(string)
	if !strings.Contains(guard, s.KeySHA256) || !strings.Contains(guard, s.CSRSHA256) || strings.Contains(guard, "chmod") {
		t.Fatal("immutable key/CSR proof missing")
	}
	affinity := deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAffinity"])
	required := certIssuerObjectList(affinity["requiredDuringSchedulingIgnoredDuringExecution"])
	if len(required) != 1 || deploymentplan.Map(required[0])["topologyKey"] != "kubernetes.io/hostname" {
		t.Fatal("original RWO custody placement missing")
	}
	for _, bad := range []string{"session", "key-path", "image", "claim", "account"} {
		t.Run(bad, func(t *testing.T) {
			o := deploymentplan.Clone(original)
			p := environmentPKIPod(o)
			cc := deploymentplan.Map(certIssuerObjectList(p["containers"])[0])
			switch bad {
			case "session":
				cc["env"] = mergeEnvironmentNamedFields(cc["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": "99999999-9999-4999-8999-999999999999"}))
			case "key-path":
				cc["env"] = mergeEnvironmentNamedFields(cc["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/new/key"}))
			case "image":
				cc["image"] = "foreign"
			case "claim":
				v, _ := environmentPKINamedFields(p["volumes"])
				deploymentplan.Map(v["bootstrap-state"]["persistentVolumeClaim"])["claimName"] = "foreign"
			case "account":
				p["serviceAccountName"] = "foreign"
			}
			if _, err := productSessionRenewalJobDefinitions(f.cfg, f.l, s, o); err == nil {
				t.Fatal("foreign renewal recipe accepted")
			}
		})
	}
}
func TestProductSessionRenewalCanonicalCertificateScriptInSelectedLinuxImage(t *testing.T) {
	image := os.Getenv("RTK_PRODUCT_PERMISSION_IMAGE")
	if image == "" {
		t.Skip("selected immutable native image not configured")
	}
	if !immutableEnvironmentImage.MatchString(image) {
		t.Fatal("selected native image must be digest pinned")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	expired, renewed := "synthetic-original-public-certificate", "synthetic-renewed-public-certificate"
	r := &productSessionRenewal{Source: productSessionRenewalSource{OriginalSession: productSessionInstallations{Session: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, CertificatePath: "/run/pki-bootstrap/renew-synthetic/bootstrap.crt", OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte(expired))}}
	proof := productSessionRenewalCleanupProof{RenewedCertificateSHA256: environmentPreflightBytesSHA256([]byte(renewed))}
	generated := productSessionRenewalCleanupScript(r, proof)
	guard := productSessionForwardKeyGuard(&environmentProductSessionForward{KeySHA256: environmentPreflightBytesSHA256([]byte("retained-original-private-key")), CSRSHA256: environmentPreflightBytesSHA256([]byte("retained-original-csr"))})
	script := `set -eu
umask 077
cat > /work/cleanup.sh <<'CLEANUP_SCRIPT'
` + generated + `
CLEANUP_SCRIPT
cat > /work/keyguard.sh <<'KEY_GUARD'
` + guard + `
KEY_GUARD
reset_fixture() {
 rm -rf /run/pki-bootstrap/*
 mkdir -m 0700 /run/pki-bootstrap/renew-synthetic
 printf 'synthetic-original-public-certificate' > /run/pki-bootstrap/bootstrap.crt
 printf 'synthetic-renewed-public-certificate' > /run/pki-bootstrap/renew-synthetic/bootstrap.crt
 printf 'retained-original-private-key' > /run/pki-bootstrap/bootstrap.key
 printf 'retained-original-csr' > /run/pki-bootstrap/bootstrap.csr
 chmod 0600 /run/pki-bootstrap/bootstrap.* /run/pki-bootstrap/renew-synthetic/bootstrap.crt
 sha256sum /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr > /work/keys.sha256
}
archive=/run/pki-bootstrap/expired-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb
for prefix in initial mkdir link canonical-unlinked swapped; do
 reset_fixture
 case "$prefix" in
 mkdir) mkdir -m 0700 "$archive" ;;
 link) mkdir -m 0700 "$archive"; ln /run/pki-bootstrap/bootstrap.crt "$archive/bootstrap.crt" ;;
 canonical-unlinked) mkdir -m 0700 "$archive"; ln /run/pki-bootstrap/bootstrap.crt "$archive/bootstrap.crt"; rm /run/pki-bootstrap/bootstrap.crt ;;
 swapped) mkdir -m 0700 "$archive"; ln /run/pki-bootstrap/bootstrap.crt "$archive/bootstrap.crt"; rm /run/pki-bootstrap/bootstrap.crt; ln /run/pki-bootstrap/renew-synthetic/bootstrap.crt /run/pki-bootstrap/bootstrap.crt ;;
 esac
 /bin/sh /work/cleanup.sh
 /bin/sh /work/cleanup.sh
 sha256sum -c /work/keys.sha256 >/dev/null
 test "$(cat "$archive/bootstrap.crt")" = synthetic-original-public-certificate
 test "$(cat /run/pki-bootstrap/bootstrap.crt)" = synthetic-renewed-public-certificate
 test "$(stat -c '%a %u %g' /run/pki-bootstrap/bootstrap.key)" = '600 10001 10001'
 printf 'PASS %s\n' "$prefix"
done
for bad in old-bytes new-bytes archive-mode archive-symlink; do
 reset_fixture
 case "$bad" in
 old-bytes) printf foreign > /run/pki-bootstrap/bootstrap.crt ;;
 new-bytes) printf foreign > /run/pki-bootstrap/renew-synthetic/bootstrap.crt ;;
 archive-mode) mkdir -m 0755 "$archive" ;;
 archive-symlink) ln -s /work "$archive" ;;
 esac
 if /bin/sh /work/cleanup.sh >/dev/null 2>&1; then echo 'FAIL accepted bad public custody'; exit 1; fi
 sha256sum -c /work/keys.sha256 >/dev/null
 printf 'PASS rejected-%s\n' "$bad"
done
reset_fixture
/bin/sh /work/keyguard.sh
printf 'PASS keyguard-valid\n'
for bad in key-mode csr-hash missing-key symlink-key; do
 reset_fixture
 case "$bad" in
 key-mode) chmod 0660 /run/pki-bootstrap/bootstrap.key ;;
 csr-hash) printf foreign > /run/pki-bootstrap/bootstrap.csr ;;
 missing-key) rm /run/pki-bootstrap/bootstrap.key ;;
 symlink-key) rm /run/pki-bootstrap/bootstrap.key; ln -s /run/pki-bootstrap/bootstrap.csr /run/pki-bootstrap/bootstrap.key ;;
 esac
 if /bin/sh /work/keyguard.sh >/dev/null 2>&1; then echo 'FAIL accepted bad native keyguard'; exit 1; fi
 printf 'PASS keyguard-rejected-%s\n' "$bad"
done
`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	args := []string{"run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--user", "10001:10001", "--tmpfs", "/run/pki-bootstrap:rw,uid=10001,gid=10001,mode=0700", "--tmpfs", "/work:rw,uid=10001,gid=10001,mode=0700", "--entrypoint", "/bin/sh", image, "-ec", script}
	raw, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("selected-image certificate cleanup interruption: %v; %s", err, raw)
	}
	if bytes.Count(raw, []byte("PASS ")) != 14 {
		t.Fatal("missing Linux cleanup prefixes", string(raw))
	}
	t.Logf("all 14 Linux certificate archive/retry and native keyguard cases passed; original private key/CSR hashes and0600 preserved")
}
func TestProductSessionRenewalNativeHistoryOnlyExpiresMonotonically(t *testing.T) {
	old := productSessionInstallations{Session: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Issuer: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Root: strings.Repeat("a", 64), Subjects: []string{"service:mqtt", "service:ota", "service:shadow", "service:video-storage", "service:webrtc"}, Installed: []string{"service:mqtt"}, Status: "active", Expires: time.Now().UTC().Add(-time.Hour)}
	s := productSessionRenewalSource{OriginalSession: old, OriginalFingerprint: strings.Repeat("b", 64), OriginalDeploymentID: "original-deployment"}
	n := productSessionRenewalNative{productSessionInstallations: old, Fingerprint: s.OriginalFingerprint, Caller: "service:deployment-bootstrap", DeploymentID: s.OriginalDeploymentID}
	if !productSessionRenewalOldNativeMatches(s, n) {
		t.Fatal("original exact row refused")
	}
	n.Status = "expired"
	if !productSessionRenewalOldNativeMatches(s, n) {
		t.Fatal("native monotonic expiry refused")
	}
	for _, bad := range []string{"fingerprint", "expiry", "subjects", "ack", "status"} {
		t.Run(bad, func(t *testing.T) {
			v := n
			switch bad {
			case "fingerprint":
				v.Fingerprint = strings.Repeat("c", 64)
			case "expiry":
				v.Expires = v.Expires.Add(time.Minute)
			case "subjects":
				v.Subjects = []string{"service:foreign"}
			case "ack":
				v.Installed = []string{"service:shadow"}
			case "status":
				v.Status = "sealed"
			}
			if productSessionRenewalOldNativeMatches(s, v) {
				t.Fatal("foreign original native state accepted")
			}
		})
	}
	if sid, did, path := productSessionRenewalIdentity(strings.Repeat("d", 64), strings.Repeat("e", 64), old.Session); sid == old.Session || !environmentBootstrapOwnerUUID.MatchString(sid) || did == "" || !strings.HasPrefix(path, "/run/pki-bootstrap/renew-") {
		t.Fatal("renewal identity is not distinct and deterministic")
	}
}

func TestProductSessionRenewalControlsPinDistinctOriginalRBACOwners(t *testing.T) {
	f, source, _ := productRenewalJobFixture(t)
	_, controller, binding := cleanupDiagnosticFixture(t, "dev")
	binding.SessionID = source.SessionID
	binding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + source.SessionID[:12]
	deploymentplan.Map(controller["metadata"])["namespace"] = f.p.Stack + "-video-cloud"
	source.ControlOwners = map[string]string{}
	for i, id := range []string{"Role/" + f.p.Stack + "-video-cloud/bootstrap-cleanup", "RoleBinding/" + f.p.Stack + "-video-cloud/bootstrap-cleanup", "ClusterRole//" + f.p.Stack + "-bootstrap-cleanup", "ClusterRoleBinding//" + f.p.Stack + "-bootstrap-cleanup"} {
		source.ControlOwners[id] = fmt.Sprintf("aaaaaaaa-bbbb-4ccc-8ddd-%012d", i+1)
	}
	for _, d := range f.l.SourceDefinitions {
		if deploymentplan.ID(d.Object) == "Deployment/"+f.p.Stack+"-video-cloud/certissuer" {
			source.ControlSources = append(source.ControlSources, d)
		}
	}
	source.ControlSources = append(source.ControlSources, deploymentplan.Definition{Object: controller, Phase: "workload"})
	r := &productSessionRenewal{Source: source, Installed: environmentProductIdentitySession{Ceremony: f.service}}
	r.Installed.Ceremony.BootstrapSessionID = source.SessionID
	r.Installed.Ceremony.CleanupBinding = binding
	before := deploymentplan.Digest(r.Source)
	defs, err := productSessionRenewalControlDefinitions(f.cfg, f.l, r)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if owner := source.ControlOwners[deploymentplan.ID(d.Object)]; owner != "" {
			if deploymentplan.Map(d.Object["metadata"])["uid"] != owner {
				t.Fatal("original distinct RBAC owner overwritten by aliased metadata", deploymentplan.ID(d.Object))
			}
			seen[deploymentplan.ID(d.Object)] = true
		}
	}
	if len(seen) != 4 || deploymentplan.Digest(r.Source) != before {
		t.Fatal("owner source mutated or RBAC owner omitted")
	}
}
