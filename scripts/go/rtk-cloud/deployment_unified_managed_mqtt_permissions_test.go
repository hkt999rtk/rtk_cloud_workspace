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

func TestManagedMQTTConsumerPermissionsInSelectedLinuxImage(t *testing.T) {
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
	defs := managedMQTTConsumersFixture(t)
	if err := bindEnvironmentManagedMQTTConsumers(defs); err != nil {
		t.Fatal(err)
	}
	workers := 0
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] != "mqttfoundation" && c["name"] != "shadowworker" {
				continue
			}
			workers++
			pod := environmentPKIPod(d.Object)
			security := deploymentplan.Map(pod["securityContext"])
			if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["fsGroup"] != nil || deploymentplan.Digest(security["supplementalGroups"]) != deploymentplan.Digest([]any{float64(0)}) {
				t.Fatal("selected worker permission profile changed")
			}
			volumes, _ := environmentPKINamedFields(pod["volumes"])
			mounts, _ := environmentPKINamedFields(c["volumeMounts"])
			if deploymentplan.Map(volumes["mqtt-bundle-state"]["persistentVolumeClaim"])["claimName"] != managedMQTTConsumerClaim(d.Object, c) || mounts["mqtt-bundle-state"]["mountPath"] != "/var/lib/mqtt-trust" || mounts["mqtt-bundle-state"]["readOnly"] == true {
				t.Fatal("selected worker does not use its own writable cache claim")
			}
		}
	}
	if workers != 2 {
		t.Fatal("both maintained MQTT product workers must be exercised")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		raw, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated selected-image worker permissions: %v; %s", err, raw)
		}
		return raw
	}
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	state, identity := "rtk-mqtt-state-"+suffix, "rtk-mqtt-identity-"+suffix
	run("volume", "create", state)
	run("volume", "create", identity)
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_ = exec.CommandContext(clean, docker, "volume", "rm", "-f", state, identity).Run()
	})
	base := []string{"run", "--rm", "--platform", "linux/amd64", "--pull=never", "--network", "none", "--read-only", "--security-opt", "no-new-privileges", "--mount", "type=volume,source=" + state + ",target=/var/lib/mqtt-trust", "--mount", "type=volume,source=" + identity + ",target=/etc/video_cloud/platform-service"}
	root := append(append([]string(nil), base...), "--user", "0:0", "--entrypoint", "/bin/sh", image, "-ec")
	run(append(root, `set -eu
umask 077
mkdir -p /var/lib/mqtt-trust/private /etc/video_cloud/platform-service/..payload
printf 'synthetic-original-key' > /var/lib/mqtt-trust/private/client.key
printf 'synthetic-original-csr' > /var/lib/mqtt-trust/private/client.csr
chown -R 10001:10001 /var/lib/mqtt-trust
chmod 0700 /var/lib/mqtt-trust /var/lib/mqtt-trust/private
chmod 0600 /var/lib/mqtt-trust/private/client.key /var/lib/mqtt-trust/private/client.csr
printf 'synthetic-product-certificate' > /etc/video_cloud/platform-service/..payload/client.crt
printf 'synthetic-product-private-key' > /etc/video_cloud/platform-service/..payload/client.key
chown -R 0:0 /etc/video_cloud/platform-service
chmod 0755 /etc/video_cloud/platform-service /etc/video_cloud/platform-service/..payload
chmod 0440 /etc/video_cloud/platform-service/..payload/client.crt /etc/video_cloud/platform-service/..payload/client.key
ln -s ..payload /etc/video_cloud/platform-service/..data
ln -s ..data/client.crt /etc/video_cloud/platform-service/client.crt
ln -s ..data/client.key /etc/video_cloud/platform-service/client.key
cd /var/lib/mqtt-trust
sha256sum private/client.key private/client.csr > private/original.sha256
chmod 0600 private/original.sha256
chown 10001:10001 private/original.sha256`)...)
	userBase := append([]string(nil), base...)
	for i, arg := range userBase {
		if strings.HasSuffix(arg, "target=/etc/video_cloud/platform-service") {
			userBase[i] += ",readonly"
		}
	}
	user := append(append([]string(nil), userBase...), "--user", "10001:10001", "--cap-drop", "ALL", "--entrypoint", "/bin/sh", image, "-ec")
	run(append(user, `set -eu
if cat /etc/video_cloud/platform-service/client.key >/dev/null 2>&1; then exit 91; fi`)...)
	user = append(userBase, "--user", "10001:10001", "--group-add", "0", "--cap-drop", "ALL", "--entrypoint", "/bin/sh", image, "-ec")
	raw := run(append(user, `set -eu
umask 077
test "$(id -u):$(id -g)" = '10001:10001'
test "$(stat -Lc '%a %u %g' /etc/video_cloud/platform-service/client.key)" = '440 0 0'
test "$(cat /etc/video_cloud/platform-service/client.key)" = 'synthetic-product-private-key'
test "$(cat /etc/video_cloud/platform-service/client.crt)" = 'synthetic-product-certificate'
cd /var/lib/mqtt-trust
test "$(stat -c '%a %u %g' .)" = '700 10001 10001'
for role in mqtt shadow; do
  printf 'synthetic-persistent-crl-state' > "$role.json"
  : > "$role.json.lock"
  test "$(stat -c '%a %u %g' "$role.json")" = '600 10001 10001'
  test "$(stat -c '%a %u %g' "$role.json.lock")" = '600 10001 10001'
done
sha256sum -c private/original.sha256 >/dev/null
test "$(stat -c '%a %u %g' private/client.key)" = '600 10001 10001'
test "$(stat -c '%a %u %g' private/client.csr)" = '600 10001 10001'
printf 'selected Linux image: supplemental root group reads projected identity; own cache writable; private key/CSR hashes and 0600 unchanged\n'`)...)
	if !strings.Contains(string(raw), "private key/CSR hashes and 0600 unchanged") {
		t.Fatal("container permission assertions did not execute")
	}
	t.Log(strings.TrimSpace(string(raw)))
}
