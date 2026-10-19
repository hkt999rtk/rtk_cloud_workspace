package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func unstartedManagedOpenBaoPodFixture() deploymentplan.Object {
	pending := func(name, reason string) any {
		return deploymentplan.Object{"name": name, "ready": false, "restartCount": float64(0), "state": deploymentplan.Object{"waiting": deploymentplan.Object{"reason": reason}}, "lastState": deploymentplan.Object{}}
	}
	return deploymentplan.Object{"status": deploymentplan.Object{"phase": "Pending", "initContainerStatuses": []any{pending("openbao-pki-install", "ImagePullBackOff")}, "containerStatuses": []any{pending("openbao", "PodInitializing"), pending("openbao-pki", "PodInitializing")}}}
}

func TestManagedOpenBaoInterruptedPullRequiresNeverStartedNativeOwners(t *testing.T) {
	for _, mode := range []string{"valid", "err-pull", "running-pod", "installer-started", "installer-completed", "installer-restarted", "foreign-installer", "missing-installer", "provider-started", "provider-restarted", "provider-ready", "missing-provider", "foreign-provider", "previous-execution"} {
		t.Run(mode, func(t *testing.T) {
			pod := unstartedManagedOpenBaoPodFixture()
			status := deploymentplan.Map(pod["status"])
			init := deploymentplan.Map(certIssuerObjectList(status["initContainerStatuses"])[0])
			provider := deploymentplan.Map(certIssuerObjectList(status["containerStatuses"])[0])
			switch mode {
			case "err-pull":
				deploymentplan.Map(deploymentplan.Map(init["state"])["waiting"])["reason"] = "ErrImagePull"
			case "running-pod":
				status["phase"] = "Running"
			case "installer-started":
				init["state"] = deploymentplan.Object{"running": deploymentplan.Object{}}
			case "installer-completed":
				init["state"] = deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}
			case "installer-restarted":
				init["restartCount"] = float64(1)
			case "foreign-installer":
				init["name"] = "foreign"
			case "missing-installer":
				delete(status, "initContainerStatuses")
			case "provider-started":
				provider["containerID"] = "containerd://original-started"
			case "provider-restarted":
				provider["restartCount"] = float64(1)
			case "provider-ready":
				provider["ready"] = true
			case "missing-provider":
				status["containerStatuses"] = []any{provider}
			case "foreign-provider":
				provider["name"] = "foreign"
			case "previous-execution":
				init["lastState"] = deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(1)}}
			}
			err := validateManagedOpenBaoUnstartedPod(pod)
			if (mode == "valid" || mode == "err-pull") != (err == nil) {
				t.Fatal("uncertain native execution admitted", mode, err)
			}
		})
	}
}

func TestNativeOpenBaoExecPreservesOnlyTheAuthenticatedContainerCAPaths(t *testing.T) {
	for _, selected := range []string{"/openbao/transport/ca.crt", "/run/openbao-pki-root/root.pem", "/foreign/root.pem", ""} {
		t.Run(strings.ReplaceAll(selected, "/", "_"), func(t *testing.T) {
			cfg, _, b, _, _ := createOpenBaoNativeFixture(t, "normal")
			fake := filepath.Join(t.TempDir(), "kubectl")
			script := `#!/bin/sh
set -eu
for arg do
 case "$arg" in BAO_CACERT=*) exit 72 ;; esac
 last=$arg
done
exec /bin/sh -ec "$last"
`
			writeTestFile(t, fake, script)
			if err := os.Chmod(fake, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("BAO_CACERT", selected)
			raw, err := environmentCreateOpenBaoExec(context.Background(), cfg, b, nil, "printf 'native-readback'")
			want := selected == "/openbao/transport/ca.crt" || selected == "/run/openbao-pki-root/root.pem"
			if want != (err == nil) || want && string(raw) != "native-readback" {
				t.Fatal("native read replaced or accepted an unqualified CA path", selected, err)
			}
		})
	}
}

func TestManagedOpenBaoInterruptedPodProjectionRetainsOriginalClaims(t *testing.T) {
	for _, mode := range []string{"valid", "api-defaults", "remapped-data", "extra-volume", "changed-image", "changed-empty-env"} {
		t.Run(mode, func(t *testing.T) {
			set := deploymentplan.Object{"kind": "StatefulSet", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "original-trust"}}}, "containers": []any{deploymentplan.Object{"name": "openbao", "image": "immutable-original-image", "env": []any{deploymentplan.Object{"name": "EXPLICIT_EMPTY", "value": ""}}}}}}, "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}}, deploymentplan.Object{"metadata": deploymentplan.Object{"name": "audit"}}}}}
			pod := deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Clone(environmentPKIPod(set))}
			p := deploymentplan.Map(pod["spec"])
			volumes := []any{deploymentplan.Object{"name": "audit", "persistentVolumeClaim": deploymentplan.Object{"claimName": "audit-openbao-0"}}, deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "data-openbao-0"}}, deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "original-trust"}}}
			p["volumes"] = volumes
			container := deploymentplan.Map(certIssuerObjectList(p["containers"])[0])
			env := deploymentplan.Map(certIssuerObjectList(container["env"])[0])
			switch mode {
			case "api-defaults":
				delete(env, "value")
				p["dnsPolicy"] = "ClusterFirst"
			case "remapped-data":
				deploymentplan.Map(deploymentplan.Map(volumes[1])["persistentVolumeClaim"])["claimName"] = "foreign-data"
			case "extra-volume":
				p["volumes"] = append(volumes, deploymentplan.Object{"name": "extra", "emptyDir": deploymentplan.Object{}})
			case "changed-image":
				container["image"] = "unreviewed-image"
			case "changed-empty-env":
				delete(env, "value")
				env["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "foreign", "key": "value"}}
			}
			got := managedOpenBaoPodMatches(set, pod)
			if got != (mode == "valid" || mode == "api-defaults") {
				t.Fatal("Pod projection admitted unrelated or remapped state", mode, got)
			}
		})
	}
}

func TestManagedOpenBaoAcceptedContinuationDoesNotSelectAnotherRepair(t *testing.T) {
	for _, mode := range []string{"none", "pull", "audit", "retained"} {
		t.Run(mode, func(t *testing.T) {
			r := &createServiceSessionRecovery{}
			if mode != "none" {
				partial := &createManagedTransportPartialRecovery{PriorForward: &createManagedTransportInterruptedForward{Previous: &createManagedTransportInterruptedForward{}}}
				r.ManagedTransportRecovery = &createManagedTransportRecovery{Partial: partial}
				if mode == "audit" || mode == "retained" {
					partial.AcceptedForward = &createManagedTransportAcceptedForward{}
				}
				if mode == "retained" {
					partial.PostManaged = &createManagedTransportPostManaged{}
				}
			}
			if managedTransportOpenBaoPullRepair(r) != (mode == "pull") || managedTransportOpenBaoAuditRepair(r) != (mode == "audit") || managedTransportOpenBaoRetained(r) != (mode == "retained") {
				t.Fatal("accepted state selected a replayed pull or audit repair", mode)
			}
			if mode == "retained" {
				if err := verifyManagedTransportRestoredOpenBao(context.Background(), deploymentConfig{}, secretStore{}, environmentCreateBootstrapRuntime{ServiceSessionRecovery: r}); err != nil {
					t.Fatal("retained accepted hosts entered restoration or audit signal", err)
				}
			}
		})
	}
}
