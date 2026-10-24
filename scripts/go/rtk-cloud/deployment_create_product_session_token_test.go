package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productSessionTokenFixture(t *testing.T) ([]deploymentplan.Definition, *environmentProductSessionForward) {
	t.Helper()
	f := newProductForwardFixture(t)
	proof := &environmentProductSessionForward{OriginalPlanSHA256: f.old.SHA256(), KeySHA256: environmentPreflightBytesSHA256([]byte("original-key")), CSRSHA256: environmentPreflightBytesSHA256([]byte("original-csr"))}
	definitions, err := productSessionForwardDefinitions(f.l, f.old, proof)
	if err != nil {
		t.Fatal(err)
	}
	return definitions, proof
}

func TestProductSessionForwardTokenProjectionPreservesOriginalPrivateCustody(t *testing.T) {
	original, _ := productSessionTokenFixture(t)
	before := deploymentplan.Digest(original)
	fixed, err := bindProductSessionForwardReadableToken(original)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(fixed[0].Object)
	volumes, _ := environmentPKINamedFields(pod["volumes"])
	projected := deploymentplan.Map(volumes["openbao-auth"]["projected"])
	if projected["defaultMode"] != float64(0444) || deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
		t.Fatal("projected token still depends on private-volume fsGroup")
	}
	projected["defaultMode"] = float64(0440)
	if deploymentplan.Digest(fixed) != before || deploymentplan.Digest(original) != before {
		t.Fatal("token projection changed original identities, paths, mounts, command or frozen legacy recipe")
	}
	for _, mode := range []string{"uid", "gid", "fsGroup", "audience", "ttl", "token-path", "extra-source", "mode", "env", "mount", "writable"} {
		t.Run(mode, func(t *testing.T) {
			var defs []deploymentplan.Definition
			if decodeEnvironmentJSON(mustProviderRecoveryJSON(original), &defs) != nil {
				t.Fatal("fixture clone")
			}
			pod := environmentPKIPod(defs[0].Object)
			c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			volumes, _ := environmentPKINamedFields(pod["volumes"])
			p := deploymentplan.Map(volumes["openbao-auth"]["projected"])
			source := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(p["sources"])[0])["serviceAccountToken"])
			mounts, _ := environmentPKINamedFields(c["volumeMounts"])
			switch mode {
			case "uid":
				deploymentplan.Map(pod["securityContext"])["runAsUser"] = float64(0)
			case "gid":
				deploymentplan.Map(pod["securityContext"])["runAsGroup"] = float64(0)
			case "fsGroup":
				deploymentplan.Map(pod["securityContext"])["fsGroup"] = float64(10001)
			case "audience":
				source["audience"] = "foreign"
			case "ttl":
				source["expirationSeconds"] = float64(7200)
			case "token-path":
				source["path"] = "elsewhere"
			case "extra-source":
				p["sources"] = append(certIssuerObjectList(p["sources"]), deploymentplan.Object{"secret": deploymentplan.Object{"name": "foreign"}})
			case "mode":
				p["defaultMode"] = float64(0600)
			case "env":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": "/foreign/token"}))
			case "mount":
				mounts["openbao-auth"]["mountPath"] = "/foreign"
			case "writable":
				mounts["openbao-auth"]["readOnly"] = false
			}
			if _, err := bindProductSessionForwardReadableToken(defs); err == nil {
				t.Fatal("unqualified token projection accepted")
			}
		})
	}
}

func TestProductSessionForwardTokenPermissionsInSelectedLinuxImage(t *testing.T) {
	image := os.Getenv("RTK_PRODUCT_PERMISSION_IMAGE")
	if image == "" {
		t.Skip("selected immutable native image not configured")
	}
	if !immutableEnvironmentImage.MatchString(image) {
		t.Fatal("selected native image must be digest pinned")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("selected Linux Docker test runtime is unavailable")
	}
	definitions, proof := productSessionTokenFixture(t)
	fixed, err := bindProductSessionForwardReadableToken(definitions)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(fixed[0].Object)
	volumes, _ := environmentPKINamedFields(pod["volumes"])
	selectedMode := deploymentplan.Map(volumes["openbao-auth"]["projected"])["defaultMode"].(float64)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		raw, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated selected-image permission boundary: %v; %s", err, raw)
		}
		return raw
	}
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	state, auth := "rtk-product-state-"+suffix, "rtk-product-auth-"+suffix
	run("volume", "create", state)
	run("volume", "create", auth)
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_ = exec.CommandContext(clean, docker, "volume", "rm", "-f", state, auth).Run()
	})
	base := []string{"run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--mount", "type=volume,source=" + state + ",target=/run/pki-bootstrap", "--mount", "type=volume,source=" + auth + ",target=/run/openbao-auth"}
	root := append(append([]string(nil), base...), "--user", "0:0", "--entrypoint", "/bin/sh", image, "-ec")
	setup := `set -eu
mkdir -p /run/openbao-auth/..payload
chown 0:0 /run/openbao-auth /run/openbao-auth/..payload
chmod 0755 /run/openbao-auth /run/openbao-auth/..payload
printf 'synthetic-projected-token' > /run/openbao-auth/..payload/token
chown 0:0 /run/openbao-auth/..payload/token
chmod 0440 /run/openbao-auth/..payload/token
ln -s ..payload /run/openbao-auth/..data
ln -s ..data/token /run/openbao-auth/token
printf 'original-key' > /run/pki-bootstrap/bootstrap.key
printf 'original-csr' > /run/pki-bootstrap/bootstrap.csr
chown 10001:10001 /run/pki-bootstrap /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr
chmod 0700 /run/pki-bootstrap
chmod 0600 /run/pki-bootstrap/bootstrap.key /run/pki-bootstrap/bootstrap.csr`
	run(append(root, setup)...)
	userBase := append([]string(nil), base...)
	for i, arg := range userBase {
		if strings.HasSuffix(arg, "target=/run/openbao-auth") {
			userBase[i] += ",readonly"
		}
	}
	user := append(userBase, "--user", "10001:10001", "--cap-drop", "ALL", "--env", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE=/run/openbao-auth/token", "--entrypoint", "/bin/sh", image, "-ec")
	legacy := `set -eu
test "$(id -u):$(id -g)" = '10001:10001'
test "$(stat -c '%a %u %g' /run/openbao-auth/token)" = '777 0 0'
test "$(stat -Lc '%a %u %g' /run/openbao-auth/token)" = '440 0 0'
if cat "$OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE" >/dev/null 2>&1; then exit 91; fi
` + productSessionForwardKeyGuard(proof)
	run(append(user, legacy)...)
	run(append(root, fmt.Sprintf("chmod %04o /run/openbao-auth/..payload/token", int(selectedMode)))...)
	fixedRead := `set -eu
test "$(id -u):$(id -g)" = '10001:10001'
test "$(stat -Lc '%a %u %g' /run/openbao-auth/token)" = '444 0 0'
test "$(cat "$OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE")" = 'synthetic-projected-token'
` + productSessionForwardKeyGuard(proof) + `
test ! -e /run/pki-bootstrap/bootstrap.crt
printf 'selected Linux image: token readable; original private key/CSR modes and hashes unchanged\n'`
	raw := run(append(user, fixedRead)...)
	if !strings.Contains(string(raw), "original private key/CSR modes and hashes unchanged") {
		t.Fatal("container permission assertions did not execute")
	}
	t.Log(strings.TrimSpace(string(raw)))
}
