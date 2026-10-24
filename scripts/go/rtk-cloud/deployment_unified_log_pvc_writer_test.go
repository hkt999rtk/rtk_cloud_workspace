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

func logPVCWriterFixture(t *testing.T) (deploymentConfig, []deploymentplan.Definition, environmentLogIngesterPVCWriterProfile) {
	t.Helper()
	defs, _ := logMQTTFixture(t, "dev")
	if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": "dev"}, defs); err != nil {
		t.Fatal(err)
	}
	o := defs[0].Object
	pod := environmentPKIPod(o)
	app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	delete(app, "securityContext") // genuine old native image default10001:999
	var claim string
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		if v["name"] == "mqtt-pki-state" {
			claim = deploymentplan.String(deploymentplan.Map(v["persistentVolumeClaim"])["claimName"])
		}
	}
	pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), deploymentplan.Object{"name": "mqtt-bundle-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": claim}})
	app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "mqtt-bundle-state", "mountPath": "/var/lib/mqtt-trust"})
	id := func(n int) string { return fmt.Sprintf("%08d-1111-4111-8111-111111111111", n) }
	deploymentplan.Map(o["metadata"])["uid"] = id(1)
	p := environmentLogIngesterPVCWriterProfile{Schema: environmentLogIngesterPVCWriterSchema, DeploymentUID: id(1), PodName: "original-log-pod", PodUID: id(2), ClaimName: claim, ClaimUID: id(3), PVName: "original-pv", PVUID: id(4), NativeID: "1234", AppUID: 10001, AppGID: 999, RootUID: 0, RootGID: 10001, RootMode: "2775"}
	return deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}, defs, p
}

func TestLogIngesterPVCWriterCorrectionPreservesOriginalIdentityAndVolumes(t *testing.T) {
	cfg, defs, p := logPVCWriterFixture(t)
	before := deploymentplan.Digest(defs)
	got, err := environmentPostCleanupLogIngesterDefinitions(cfg, defs, p)
	if err != nil || len(got) != 1 {
		t.Fatal("finite original-owner correction", err)
	}
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(got[0].Object)["containers"])[0])
	security := deploymentplan.Map(c["securityContext"])
	if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || len(security) != 2 || got[0].Retain {
		t.Fatal("native PVC writer selection")
	}
	restored := deploymentplan.Clone(got[0].Object)
	delete(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(restored)["containers"])[0]), "securityContext")
	if deploymentplan.Digest(restored) != deploymentplan.Digest(defs[0].Object) || deploymentplan.Digest(defs) != before || deploymentplan.Digest(got[0].Dependencies) != deploymentplan.Digest(defs[0].Dependencies) || got[0].Phase != defs[0].Phase {
		t.Fatal("correction changed image, identity, original PVC, trust, rollout or source")
	}
	for _, mode := range []string{"foreign-user", "foreign-group", "foreign-deployment", "foreign-claim", "foreign-root-owner", "foreign-mode", "missing-pv", "duplicate-source"} {
		t.Run(mode, func(t *testing.T) {
			cfg, defs, p := logPVCWriterFixture(t)
			app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			switch mode {
			case "foreign-user":
				app["securityContext"] = deploymentplan.Object{"runAsUser": float64(0)}
			case "foreign-group":
				app["securityContext"] = deploymentplan.Object{"runAsGroup": float64(0)}
			case "foreign-deployment":
				p.DeploymentUID = "99999999-9999-4999-8999-999999999999"
			case "foreign-claim":
				p.ClaimName = "other"
			case "foreign-root-owner":
				p.RootUID = 10001
			case "foreign-mode":
				p.RootMode = "777"
			case "missing-pv":
				p.PVUID = ""
			case "duplicate-source":
				defs = append(defs, defs[0])
			}
			if _, err := environmentPostCleanupLogIngesterDefinitions(cfg, defs, p); err == nil {
				t.Fatal("unqualified native writer correction accepted")
			}
		})
	}
}

// This executes the actual native pkitrust.ApplyCRL implementation in the pinned
// Linux image, switching only the process group on the same root-owned tmpfs.
func TestLogIngesterPVCWriterNativeAtomicCRLRegression(t *testing.T) {
	image := os.Getenv("RTK_PRODUCT_PERMISSION_IMAGE")
	binary := os.Getenv("RTK_LOG_PVC_NATIVE_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("explicit pinned native image and built native test required")
	}
	if image != "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:2ce619e5fe6d110ee3d8461792c8a106cc87bcf6c67190643a716a3a04ae08b6" {
		t.Fatal("selected native image required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	docker := func(t *testing.T, args ...string) []byte {
		t.Helper()
		raw, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated native permission fixture: %v: %s", err, raw)
		}
		return raw
	}
	name := fmt.Sprintf("rtk-log-pvc-%d-%d", os.Getpid(), time.Now().UnixNano())
	docker(t, "run", "-d", "--rm", "--pull=never", "--platform", "linux/amd64", "--network", "none", "--read-only", "--user", "0:0", "--tmpfs", "/fixture:rw,mode=0755", "--tmpfs", "/tool:rw,exec,mode=0755", "--name", name, "--entrypoint", "/bin/sh", image, "-ec", "sleep 120")
	defer exec.Command("docker", "rm", "-f", name).Run()
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	install := exec.CommandContext(ctx, "docker", "exec", "-i", "--user", "0:0", name, "/bin/sh", "-ec", "cat > /tool/native.test; chmod 0555 /tool/native.test")
	install.Stdin = bytes.NewReader(payload)
	if raw, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install isolated test in writable tmpfs: %v: %s", err, raw)
	}
	for _, item := range []struct{ phase, user string }{{"setup", "0:0"}, {"wrong-group", "10001:999"}, {"correct-group", "10001:10001"}, {"verify", "0:0"}} {
		t.Run(item.phase, func(t *testing.T) {
			raw := docker(t, "exec", "--user", item.user, "-e", "RTK_LOG_PVC_NATIVE_PHASE="+item.phase, name, "/tool/native.test", "-test.run=^TestLogIngesterPVCGroupAtomicNativeCRL$", "-test.v", "-test.count=1")
			if !bytes.Contains(raw, []byte("=== RUN   TestLogIngesterPVCGroupAtomicNativeCRL")) || !bytes.Contains(raw, []byte("--- PASS: TestLogIngesterPVCGroupAtomicNativeCRL")) || strings.Contains(string(raw), "--- SKIP:") {
				t.Fatal("native named test did not execute and pass")
			}
		})
	}
}
