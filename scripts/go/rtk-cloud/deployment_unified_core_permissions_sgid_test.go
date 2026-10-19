package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCoreNativeSGIDRecipePreservesFrozenLegacyAndRejectsAlteredCommand(t *testing.T) {
	definitions, _ := nativeCoreAliasFixture(t)
	if err := deriveEnvironmentCoreMQTTTransport(nil, definitions); err != nil {
		t.Fatal(err)
	}
	if err := applyCoreIdentityPreparation(definitions, identityPermissionAlpine); err != nil {
		t.Fatal(err)
	}
	object := definitions[0].Object
	init := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["initContainers"])[0])
	if !strings.Contains(coreNativeIdentityPreparation, "700:10001|2700:10001") || strings.Contains(previousCoreNativeIdentityPreparation, "2700") {
		t.Fatal("expected exact versioned directory-only recipe")
	}
	for _, recipe := range []string{coreNativeIdentityPreparation, previousCoreNativeIdentityPreparation} {
		init["command"] = []any{"/bin/sh", "-ec", recipe}
		before := deploymentplan.Digest(object)
		if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err != nil {
			t.Fatal(err)
		}
		if deploymentplan.Digest(object) != before {
			t.Fatal("validation rewrote frozen recipe")
		}
	}
	init["command"] = []any{"/bin/sh", "-ec", strings.Replace(coreNativeIdentityPreparation, "2700:10001", "3700:10001", 1)}
	if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err == nil {
		t.Fatal("unreviewed special mode admitted")
	}
	init["command"] = []any{"/bin/sh", "-ec", previousCoreNativeIdentityPreparation}
	init["image"] = "foreign:image"
	if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err == nil {
		t.Fatal("legacy recipe adopted a foreign initializer")
	}
}

func TestCoreNativeSGIDPinnedLinuxDirectoryAndPrivateFileGuards(t *testing.T) {
	image := os.Getenv("RTK_CORE_PERMISSION_IMAGE")
	if image == "" {
		t.Skip("explicit pinned Linux image required")
	}
	if image != "docker.io/library/alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc" {
		t.Fatal("exact permission image required")
	}
	script := `set -eu
umask 077
cat > /work/guard.sh <<'GUARD'
` + coreNativeIdentityPreparation + `
GUARD
reset() {
 rm -rf /identity-volume/private /work/outside /identity-volume/hard
 mkdir -p /identity-volume/private/crl
 printf synthetic-identity > /identity-volume/private/client.json
 printf synthetic-crl > /identity-volume/private/crl/issuer.json
 chown -R 10001:10001 /identity-volume/private
 chmod 0700 /identity-volume/private /identity-volume/private/crl
 chmod 0600 /identity-volume/private/client.json /identity-volume/private/crl/issuer.json
 chmod 2775 /identity-volume
 chown 0:10001 /identity-volume
}
files() {
 sha256sum /identity-volume/private/client.json /identity-volume/private/crl/issuer.json
 stat -c '%a %u %g %h %i' /identity-volume/private/client.json /identity-volume/private/crl/issuer.json
}
for private_mode in 700 2700;do
 for crl_mode in 700 2700;do
  reset;chmod "$private_mode" /identity-volume/private;chmod "$crl_mode" /identity-volume/private/crl
  before=$(files)
  /bin/sh -ec "$(cat /work/guard.sh)" guard /identity-volume/private/client.json
  test "$(files)" = "$before"
  test "$(stat -c '%a %u %g' /identity-volume)" = '2775 0 10001'
  test "$(stat -c '%a %u %g' /identity-volume/private)" = "$private_mode 10001 10001"
  test "$(stat -c '%a %u %g' /identity-volume/private/crl)" = '700 10001 10001'
  printf 'PASS modes-%s-%s\n' "$private_mode" "$crl_mode"
 done
done
for bad in private1700 private3700 private750 private2770 crl1700 crl3700 crl750 crl2770 private-owner crl-owner private-link crl-link state-mode state-owner state-link state-hardlink cache-mode cache-owner cache-link cache-hardlink;do
 reset
 case "$bad" in
 private1700|private3700|private750|private2770)chmod "${bad#private}" /identity-volume/private;;
 crl1700|crl3700|crl750|crl2770)chmod "${bad#crl}" /identity-volume/private/crl;;
 private-owner)chown 0 /identity-volume/private;;crl-owner)chown 0 /identity-volume/private/crl;;
 private-link)mv /identity-volume/private /work/outside;ln -s /work/outside /identity-volume/private;;
 crl-link)mv /identity-volume/private/crl /work/outside;ln -s /work/outside /identity-volume/private/crl;;
 state-mode)chmod 0644 /identity-volume/private/client.json;;state-owner)chown 0 /identity-volume/private/client.json;;
 state-link)mv /identity-volume/private/client.json /work/outside;ln -s /work/outside /identity-volume/private/client.json;;
 state-hardlink)ln /identity-volume/private/client.json /identity-volume/hard;;
 cache-mode)chmod 0644 /identity-volume/private/crl/issuer.json;;cache-owner)chown 0 /identity-volume/private/crl/issuer.json;;
 cache-link)mv /identity-volume/private/crl/issuer.json /work/outside;ln -s /work/outside /identity-volume/private/crl/issuer.json;;
 cache-hardlink)ln /identity-volume/private/crl/issuer.json /identity-volume/hard;;
 esac
 before=$(files)
 if /bin/sh -ec "$(cat /work/guard.sh)" guard /identity-volume/private/client.json >/dev/null 2>&1;then echo FAIL;exit 1;fi
 test "$(files)" = "$before"
 test "$(stat -c '%a %u %g' /identity-volume)" = '2775 0 10001'
 printf 'PASS rejected-%s\n' "$bad"
done
`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	args := []string{"run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--user", "0:0", "--tmpfs", "/identity-volume:rw,mode=0700", "--tmpfs", "/work:rw,mode=0700", "--entrypoint", "/bin/sh", image, "-ec", script}
	raw, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("pinned Linux permission guards failed: %v: %s", err, raw)
	}
	if bytes.Count(raw, []byte("PASS ")) != 24 || strings.Contains(string(raw), "FAIL") {
		t.Fatal("missing Linux guard cases", string(raw))
	}
	t.Log("24 real Linux cases: exact700/2700 directory combinations; rejected other special/permissive modes, foreign owners, symlinks and hardlinks; private bytes/metadata and volume root preserved")
}
