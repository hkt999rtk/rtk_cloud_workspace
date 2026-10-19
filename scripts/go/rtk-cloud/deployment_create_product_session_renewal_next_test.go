package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProductSessionRenewalNextExpiredCompleteACKUsesNativeFinalization(t *testing.T) {
	f, _, _ := productRenewalJobFixture(t)
	subjects := []string{"service:mqtt", "service:ota", "service:shadow", "service:video-storage", "service:webrtc"}
	slices.Sort(subjects)
	now := time.Now().UTC()
	ack := productSessionInstallations{Session: f.service.BootstrapSessionID, Issuer: f.service.Intermediate.ID, Root: f.service.Root.RootFingerprint, Subjects: subjects, Installed: slices.Clone(subjects), Expires: now.Add(-time.Minute)}
	for _, status := range []string{"sealed", "expired"} {
		t.Run(status, func(t *testing.T) {
			v := ack
			v.Status = status
			if complete, err := productInstallationsComplete(v, f.service, subjects, now); err != nil || !complete {
				t.Fatal("genuine complete roster cannot reach supported native finalizer", err)
			}
			v.Installed = v.Installed[:4]
			if _, err := productInstallationsComplete(v, f.service, subjects, now); err == nil {
				t.Fatal("incomplete expired session accepted")
			}
		})
	}
	ack.Status = "active"
	if _, err := productInstallationsComplete(ack, f.service, subjects, now); err == nil {
		t.Fatal("active expired session cannot be natively sealed")
	}
	ack.Status = "expired"
	ack.Issuer = "foreign"
	if _, err := productInstallationsComplete(ack, f.service, subjects, now); err == nil {
		t.Fatal("foreign completed history accepted")
	}
}

func TestProductSessionRenewalNextJobRetainsNativeKeyAndPlacement(t *testing.T) {
	f, first, original := productRenewalJobFixture(t)
	one, err := productSessionRenewalJobDefinitions(f.cfg, f.l, first, original)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Generation = 2
	second.PreviousRenewalSHA256 = deploymentplan.Digest("immutable first completed proof")
	second.RuntimeSequence = 3
	second.OriginalSession.Session = first.SessionID
	second.SessionID, second.DeploymentID, second.CertificatePath = productSessionRenewalIdentity(second.ParentSHA256, second.ScopeSHA256, first.SessionID)
	a, b := deploymentplan.Digest(original), deploymentplan.Digest(one)
	base := deploymentplan.Clone(original)
	baseC := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(base)["containers"])[0])
	baseC["env"] = mergeEnvironmentNamedFields(baseC["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": first.SessionID}))
	two, err := productSessionRenewalJobDefinitions(f.cfg, f.l, second, base)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(original) != a || deploymentplan.Digest(one) != b || second.SessionID == first.SessionID || second.CertificatePath == first.CertificatePath {
		t.Fatal("earlier recipe or identity changed")
	}
	oldPod, newPod := environmentPKIPod(one[0].Object), environmentPKIPod(two[0].Object)
	oldC, newC := deploymentplan.Map(certIssuerObjectList(oldPod["containers"])[0]), deploymentplan.Map(certIssuerObjectList(newPod["containers"])[0])
	for _, k := range []string{"PKI_BOOTSTRAP_KEY_PATH", "PKI_BOOTSTRAP_CSR_PATH"} {
		if mqttFoundationEnv(newC, k) != mqttFoundationEnv(oldC, k) {
			t.Fatal("changed private input", k)
		}
	}
	for _, k := range []string{"affinity", "serviceAccountName", "securityContext", "volumes"} {
		if deploymentplan.Digest(newPod[k]) != deploymentplan.Digest(oldPod[k]) {
			t.Fatal("changed retained custody or trust", k)
		}
	}
	if mqttFoundationEnv(newC, "PKI_BOOTSTRAP_SESSION_ID") != second.SessionID || mqttFoundationEnv(newC, "PKI_BOOTSTRAP_CERT_PATH") != second.CertificatePath || mqttFoundationEnv(newC, "PKI_BOOTSTRAP_EXPIRES_AT") != "" {
		t.Fatal("not a distinct native bounded session")
	}
	for _, bad := range []string{"previous-session", "key-path", "claim"} {
		t.Run(bad, func(t *testing.T) {
			o := deploymentplan.Clone(base)
			p := environmentPKIPod(o)
			c := deploymentplan.Map(certIssuerObjectList(p["containers"])[0])
			switch bad {
			case "previous-session":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": first.OriginalSession.Session}))
			case "key-path":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/foreign/key"}))
			case "claim":
				v, _ := environmentPKINamedFields(p["volumes"])
				deploymentplan.Map(v["bootstrap-state"]["persistentVolumeClaim"])["claimName"] = "foreign"
			}
			if _, err := productSessionRenewalJobDefinitions(f.cfg, f.l, second, o); err == nil {
				t.Fatal("foreign predecessor accepted")
			}
		})
	}
	legacy, _ := json.Marshal(first)
	if bytes.Contains(legacy, []byte("Generation")) || bytes.Contains(legacy, []byte("PreviousRenewalSHA256")) || bytes.Contains(legacy, []byte("RuntimeSequence")) {
		t.Fatal("legacy source bytes gained fields")
	}
}

func TestProductSessionRenewalNextInstalledPreservesOriginalEnrollment(t *testing.T) {
	f, s, _ := productRenewalJobFixture(t)
	_, _, binding := cleanupDiagnosticFixture(t, "dev")
	originalSID := f.service.BootstrapSessionID
	old := environmentProductIdentitySession{Schema: productIdentitySessionSchema, ParentSHA256: f.l.ParentSHA256, PlanSHA256: productIdentityScope(f.l), OwnerSHA256: f.l.OwnerSHA256, Ceremony: f.service, Outputs: []environmentProductIdentityReceipt{{ResourceID: f.l.Outputs[0].ID, Subject: f.l.Outputs[0].Subject, SecretUID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", ClaimUID: "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee", CertificateSHA256: strings.Repeat("1", 64)}}, EnrollmentSessionID: originalSID}
	old.Ceremony.BootstrapSessionID = s.SessionID
	old.Ceremony.CleanupBinding = binding
	old.Ceremony.Root.Environment = "dev"
	s.Generation = 2
	s.OriginalSession.Session = old.Ceremony.BootstrapSessionID
	s.SessionID, s.DeploymentID, s.CertificatePath = productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, s.OriginalSession.Session)
	s.SignerPolicySHA256 = binding.PolicySHA256
	var next environmentProductIdentitySession
	raw, _ := json.Marshal(old)
	_ = json.Unmarshal(raw, &next)
	next.Ceremony.BootstrapSessionID = s.SessionID
	next.Ceremony.BootstrapCredentialSecret = "service-bootstrap-" + s.SessionID
	ns := next.Ceremony.BootstrapCredentialNamespace
	next.Ceremony.CredentialUIDs = map[string]string{ns: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
	next.Ceremony.CleanupBinding.SessionID = s.SessionID
	next.Ceremony.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + s.SessionID[:12]
	next.Ceremony.CleanupBinding.SecretUID = next.Ceremony.CredentialUIDs[ns]
	if !productSessionRenewalInstalledMatches(&old, &next, s) {
		t.Fatal("same permanent identities with original enrollment rejected")
	}
	next.EnrollmentSessionID = old.Ceremony.BootstrapSessionID
	if productSessionRenewalInstalledMatches(&old, &next, s) {
		t.Fatal("placement lineage advanced to temporary session")
	}
}

func TestProductSessionRenewalNextCleanupPreservesBothExpiredCertificates(t *testing.T) {
	image := os.Getenv("RTK_PRODUCT_PERMISSION_IMAGE")
	if image == "" {
		t.Skip("explicit pinned Linux image required")
	}
	if !immutableEnvironmentImage.MatchString(image) {
		t.Fatal("immutable image required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	first := &productSessionRenewal{Source: productSessionRenewalSource{OriginalSession: productSessionInstallations{Session: "11111111-1111-4111-8111-111111111111"}, OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte("original-public")), CertificatePath: "/run/pki-bootstrap/renew-first/bootstrap.crt"}}
	next := &productSessionRenewal{Source: productSessionRenewalSource{Generation: 2, OriginalSession: productSessionInstallations{Session: "22222222-2222-4222-8222-222222222222"}, OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte("first-public")), CertificatePath: "/run/pki-bootstrap/renew-second/bootstrap.crt"}}
	proof := productSessionRenewalCleanupProof{RenewedCertificateSHA256: environmentPreflightBytesSHA256([]byte("second-public"))}
	generated := productSessionRenewalNextCleanupPrefix(first, next.Source.OriginalCertificateSHA256) + productSessionRenewalCleanupScript(next, proof)
	f, source, original := productRenewalJobFixture(t)
	source.Generation = 2
	defs, err := productSessionRenewalJobDefinitions(f.cfg, f.l, source, original)
	if err != nil {
		t.Fatal(err)
	}
	prepare := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["initContainers"])[1])["command"].([]any)[2].(string)
	directory := strings.TrimSuffix(source.CertificatePath, "/bootstrap.crt")

	script := `set -eu
umask 077
cat > /work/reconcile.sh <<'SCRIPT'
` + generated + `
SCRIPT
cat > /work/directory.sh <<'DIRECTORY'
` + prepare + `
DIRECTORY
reset() {
 rm -rf /run/pki-bootstrap/*
 mkdir -m 0700 /run/pki-bootstrap/renew-first /run/pki-bootstrap/renew-second
 printf original-public > /run/pki-bootstrap/bootstrap.crt
 printf first-public > /run/pki-bootstrap/renew-first/bootstrap.crt
 printf second-public > /run/pki-bootstrap/renew-second/bootstrap.crt
 printf original-private-key > /run/pki-bootstrap/bootstrap.key
 printf original-signed-csr > /run/pki-bootstrap/bootstrap.csr
 chmod 0600 /run/pki-bootstrap/bootstrap.* /run/pki-bootstrap/renew-*/bootstrap.crt
 sha256sum /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr > /work/private.sha
}
original=/run/pki-bootstrap/expired-11111111-1111-4111-8111-111111111111
previous=/run/pki-bootstrap/expired-22222222-2222-4222-8222-222222222222
for root_mode in 700 2700;do
 chmod "$root_mode" /run/pki-bootstrap
 reset
 /bin/sh /work/directory.sh;/bin/sh /work/directory.sh
 test "$(stat -c '%a %u %g' /run/pki-bootstrap)" = "$root_mode 10001 10001"
 printf 'PASS renewed-directory-%s\n' "$root_mode"
 for invalid in 1700 3700 755 symlink;do
  reset
  if test "$invalid" = symlink;then ln -s /work '` + directory + `';else mkdir -m 0700 '` + directory + `';chmod "$invalid" '` + directory + `';fi
  if /bin/sh /work/directory.sh >/dev/null 2>&1;then echo FAIL;exit 1;fi
  sha256sum -c /work/private.sha >/dev/null;printf 'PASS renewed-directory-refused-%s-%s\n' "$root_mode" "$invalid"
 done
for state in clean original-archived first-linked first-archived canonical-unlinked second-linked;do
 reset
 case "$state" in original-archived|first-linked|first-archived|canonical-unlinked|second-linked)mkdir -m 0700 "$original";ln /run/pki-bootstrap/bootstrap.crt "$original/bootstrap.crt";;esac
 case "$state" in first-linked|first-archived|canonical-unlinked|second-linked)rm /run/pki-bootstrap/bootstrap.crt;ln /run/pki-bootstrap/renew-first/bootstrap.crt /run/pki-bootstrap/bootstrap.crt;;esac
 case "$state" in first-archived|canonical-unlinked|second-linked)mkdir -m 0700 "$previous";ln /run/pki-bootstrap/bootstrap.crt "$previous/bootstrap.crt";;esac
 case "$state" in canonical-unlinked|second-linked)rm /run/pki-bootstrap/bootstrap.crt;;esac
 case "$state" in second-linked)ln /run/pki-bootstrap/renew-second/bootstrap.crt /run/pki-bootstrap/bootstrap.crt;;esac
 /bin/sh /work/reconcile.sh;/bin/sh /work/reconcile.sh
 sha256sum -c /work/private.sha >/dev/null
 test "$(cat "$original/bootstrap.crt")" = original-public
 test "$(cat "$previous/bootstrap.crt")" = first-public
 test "$(cat /run/pki-bootstrap/bootstrap.crt)" = second-public
 test "$(stat -c '%a %u %g' /run/pki-bootstrap/bootstrap.key)" = '600 10001 10001'
 printf 'PASS %s\n' "$state"
done
for bad in original-bytes first-bytes second-bytes original-symlink previous-mode;do
 reset
 case "$bad" in original-bytes)printf foreign > /run/pki-bootstrap/bootstrap.crt;;first-bytes)printf foreign > /run/pki-bootstrap/renew-first/bootstrap.crt;;second-bytes)printf foreign > /run/pki-bootstrap/renew-second/bootstrap.crt;;original-symlink)ln -s /work "$original";;previous-mode)mkdir -m 0755 "$previous";;esac
 if /bin/sh /work/reconcile.sh >/dev/null 2>&1;then echo FAIL;exit 1;fi
 sha256sum -c /work/private.sha >/dev/null;printf 'PASS refused-%s\n' "$bad"
done
 test "$(stat -c '%a %u %g' /run/pki-bootstrap)" = "$root_mode 10001 10001"
done
`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	args := []string{"run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--user", "10001:10001", "--tmpfs", "/run/pki-bootstrap:rw,uid=10001,gid=10001,mode=0700", "--tmpfs", "/work:rw,uid=10001,gid=10001,mode=0700", "--entrypoint", "/bin/sh", image, "-ec", script}
	raw, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("two-generation public certificate reconciliation failed: %v: %s", err, raw)
	}
	if bytes.Count(raw, []byte("PASS ")) != 32 || strings.Contains(string(raw), "FAIL") {
		t.Fatal("missing Linux cases", string(raw))
	}
	t.Log("32 Linux cases passed under literal700 and inherited2700 root; rootmetadata and both original private files0600/hash preserved")
}
