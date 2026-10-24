package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductSessionRenewalDirectoryProfilePreservesHistoricalSourceAndJob(t *testing.T) {
	f, old, original := productRenewalJobFixture(t)
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("CertificateDirectoryProfile")) {
		t.Fatal("zero source gained a serialized field")
	}
	var decoded productSessionRenewalSource
	if err := decodeEnvironmentJSON(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(decoded)
	if !bytes.Equal(raw, again) {
		t.Fatal("historical source bytes changed on typed read")
	}
	a, err := productSessionRenewalJobDefinitions(f.cfg, f.l, old, original)
	if err != nil {
		t.Fatal(err)
	}
	historical := deploymentplan.Digest(a)
	next := old
	next.CertificateDirectoryProfile = productSessionRenewalSGIDDirectoryProfile
	b, err := productSessionRenewalJobDefinitions(f.cfg, f.l, next, original)
	if err != nil {
		t.Fatal(err)
	}
	nextRaw, _ := json.Marshal(next)
	if !bytes.Contains(nextRaw, []byte(`"CertificateDirectoryProfile":"private-directory-sgid-v1"`)) {
		t.Fatal("new source selector absent")
	}
	pa, pb := environmentPKIPod(a[0].Object), environmentPKIPod(b[0].Object)
	ca := deploymentplan.Map(certIssuerObjectList(pa["initContainers"])[1])["command"]
	cb := deploymentplan.Map(certIssuerObjectList(pb["initContainers"])[1])["command"]
	if strings.Contains(ca.([]any)[2].(string), "2700") || !strings.Contains(cb.([]any)[2].(string), "'2700 10001 10001'") {
		t.Fatal("historical/new recipes not versioned")
	}
	// The explicit source selector changes only the source-derived Job name and
	// the renewed certificate directory guard. All identity inputs remain exact.
	deploymentplan.Map(b[0].Object["metadata"])["name"] = deploymentplan.Map(a[0].Object["metadata"])["name"]
	deploymentplan.Map(certIssuerObjectList(pb["initContainers"])[1])["command"] = ca
	if deploymentplan.Digest(b) != historical {
		t.Fatal("new selector changed another native Job field")
	}
	after, err := productSessionRenewalJobDefinitions(f.cfg, f.l, old, original)
	if err != nil || deploymentplan.Digest(after) != historical {
		t.Fatal("new derivation mutated historical recipe", err)
	}
}

func TestProductSessionRenewalDirectoryProfileRejectsUnknownSelectors(t *testing.T) {
	f, old, original := productRenewalJobFixture(t)
	for _, profile := range []string{"unknown", "private-directory-sgid-v2", " private-directory-sgid-v1"} {
		t.Run(profile, func(t *testing.T) {
			source := old
			source.CertificateDirectoryProfile = profile
			if _, err := productSessionRenewalJobDefinitions(f.cfg, f.l, source, original); err == nil {
				t.Fatal("unknown Job selector accepted")
			}
			if err := validateProductSessionRenewalSource(f.store, nil, f.l, source); err == nil || !strings.Contains(err.Error(), "certificate directory recipe profile") {
				t.Fatal("durable source did not reject unknown selector", err)
			}
		})
	}
	old.Generation = 2
	old.CertificateDirectoryProfile = productSessionRenewalSGIDDirectoryProfile
	if _, err := productSessionRenewalJobDefinitions(f.cfg, f.l, old, original); err == nil {
		t.Fatal("selector admitted on historical second-generation recipe")
	}
	if err := validateProductSessionRenewalSource(f.store, nil, f.l, old); err == nil || !strings.Contains(err.Error(), "certificate directory recipe profile") {
		t.Fatal("generation2 selector rejected too late", err)
	}
}

func TestProductSessionRenewalDirectoryProfilePinnedLinuxFirstRetry(t *testing.T) {
	image := os.Getenv("RTK_PRODUCT_PERMISSION_IMAGE")
	if image == "" {
		t.Skip("explicit pinned Linux image required")
	}
	if image != "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:2ce619e5fe6d110ee3d8461792c8a106cc87bcf6c67190643a716a3a04ae08b6" {
		t.Fatal("exact native image required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	f, s, original := productRenewalJobFixture(t)
	s.CertificateDirectoryProfile = productSessionRenewalSGIDDirectoryProfile
	defs, err := productSessionRenewalJobDefinitions(f.cfg, f.l, s, original)
	if err != nil {
		t.Fatal(err)
	}
	init := certIssuerObjectList(environmentPKIPod(defs[0].Object)["initContainers"])
	guard := deploymentplan.Map(init[0])["command"].([]any)[2].(string)
	prepare := deploymentplan.Map(init[1])["command"].([]any)[2].(string)
	directory := strings.TrimSuffix(s.CertificatePath, "/bootstrap.crt")
	script := `set -eu
umask 077
cat > /work/guard.sh <<'GUARD'
` + guard + `
GUARD
cat > /work/prepare.sh <<'PREPARE'
` + prepare + `
PREPARE
chown -R 10001:10001 /work
chown 10001:10001 /run/pki-bootstrap
run() { setpriv --reuid=10001 --regid=10001 --clear-groups /bin/sh "$1"; }
reset() {
 rm -rf /run/pki-bootstrap/*
 printf '%s' '` + base64.StdEncoding.EncodeToString([]byte(f.bootstrap["bootstrap.key"])) + `' |base64 -d > /run/pki-bootstrap/bootstrap.key
 printf '%s' '` + base64.StdEncoding.EncodeToString([]byte(f.bootstrap["bootstrap.csr"])) + `' |base64 -d > /run/pki-bootstrap/bootstrap.csr
 chown 10001:10001 /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr
 chmod 0600 /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr
}
files() { sha256sum /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr;stat -c '%a %u %g %h %i' /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr; }
for mode in 700 2700;do
 reset;chmod "$mode" /run/pki-bootstrap
 before=$(files)
 run /work/guard.sh;run /work/prepare.sh;run /work/prepare.sh
 setpriv --reuid=10001 --regid=10001 --clear-groups /bin/sh -ec 'printf synthetic-issued-public-certificate > ` + s.CertificatePath + `;chmod 0600 ` + s.CertificatePath + `'
 public=$(sha256sum '` + s.CertificatePath + `')
 run /work/guard.sh;run /work/prepare.sh
 test "$(files)" = "$before"
 test "$(sha256sum '` + s.CertificatePath + `')" = "$public"
 test "$(stat -c '%a %u %g' '` + directory + `')" = "$mode 10001 10001"
 test "$(stat -c '%a %u %g' /run/pki-bootstrap)" = "$mode 10001 10001"
 printf 'PASS first-native-retry-%s\n' "$mode"
 for bad in 1700 3700 755 owner group symlink;do
  reset
  case "$bad" in
   symlink)ln -s /work '` + directory + `';;
   *)mkdir -m 0700 '` + directory + `';chown 10001:10001 '` + directory + `';
    case "$bad" in owner)chown 0 '` + directory + `';;group)chgrp 0 '` + directory + `';;*)chmod "$bad" '` + directory + `';;esac;;
  esac
  before=$(files)
  if run /work/prepare.sh >/dev/null 2>&1;then echo FAIL;exit 1;fi
  test "$(files)" = "$before"
  printf 'PASS refused-directory-%s-%s\n' "$mode" "$bad"
 done
 for bad in key-public csr-public key-symlink csr-bytes;do
  reset
  case "$bad" in key-public)chmod 0644 /run/pki-bootstrap/bootstrap.key;;csr-public)chmod 0644 /run/pki-bootstrap/bootstrap.csr;;key-symlink)mv /run/pki-bootstrap/bootstrap.key /work/key;ln -s /work/key /run/pki-bootstrap/bootstrap.key;;csr-bytes)printf changed > /run/pki-bootstrap/bootstrap.csr;;esac
  before=$(files)
  if run /work/guard.sh >/dev/null 2>&1;then echo FAIL;exit 1;fi
  test "$(files)" = "$before"
  printf 'PASS refused-private-%s-%s\n' "$mode" "$bad"
 done
done
`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, docker, "run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--user", "0:0", "--tmpfs", "/run/pki-bootstrap:rw,mode=0700", "--tmpfs", "/work:rw,mode=0700", "--entrypoint", "/bin/sh", image, "-ec", script).CombinedOutput()
	if err != nil {
		t.Fatalf("first-renewal Linux retry matrix failed: %v: %s", err, raw)
	}
	if bytes.Count(raw, []byte("PASS ")) != 22 || strings.Contains(string(raw), "FAIL") {
		t.Fatal("missing Linux cases", string(raw))
	}
	t.Log("22 real pinned Linux cases; runAs10001:10001 before/after native certificate output;700/2700 retries preserve root/key/CSR/certificate; invalid modes/owners/groups/links/privatefiles refused")
}
