package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyProjectionAgainstIsolatedKubernetes(t *testing.T) {
	kubeconfig := os.Getenv("RTK_CUSTODY_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("isolated Kubernetes custody fixture not selected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	run := func(input []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfig, "--request-timeout=10s"}, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("isolated Kubernetes %s failed (private output suppressed)", args[0])
		}
		return raw
	}
	var view struct {
		CurrentContext string `json:"current-context"`
		Clusters       []struct {
			Cluster struct {
				Server string `json:"server"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if json.Unmarshal(run(nil, "config", "view", "--minify", "-o", "json"), &view) != nil || len(view.Clusters) != 1 || !strings.HasPrefix(view.CurrentContext, "kind-rtk-custody-") {
		t.Fatal("only the isolated custody context is eligible")
	}
	origin, err := url.Parse(view.Clusters[0].Cluster.Server)
	if err != nil || origin.Scheme != "https" || origin.Hostname() != "127.0.0.1" {
		t.Fatal("fixture API must be HTTPS loopback")
	}
	environment := fmt.Sprintf("custody-%x", time.Now().UnixNano())
	stack := "video-cloud-" + environment
	namespace := stack + "-video-cloud"
	run(nil, "create", "namespace", namespace)
	t.Cleanup(func() {
		if exec.Command("kubectl", "--kubeconfig", kubeconfig, "--request-timeout=10s", "delete", "namespace", namespace, "--wait=false").Run() != nil {
			t.Error("isolated custody namespace cleanup failed")
		}
	})
	values := map[string]string{"CLOUD_STACK_NAME": stack, "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api-state", Namespace: namespace, ServiceAccount: "api"}}}
	state := "/identity/private/client.json"
	container := deploymentplan.Object{"name": "api", "env": environmentCreateEnv(map[string]string{"PKI_CLIENT_IDENTITY_STATE": state}), "volumeMounts": []any{environmentCreateServiceMount("identity", "/identity", false)}}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "api", "namespace": namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", "api-state")}}}}}
	definitions, err := projectEnvironmentCustodyResources(values, environment, binding, "", []deploymentplan.Definition{{Object: object}})
	if err != nil {
		t.Fatal(err)
	}
	var resources []any
	for _, d := range definitions {
		if d.Object["kind"] != "Deployment" {
			resources = append(resources, d.Object)
		}
	}
	raw, err := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "items": resources})
	if err != nil {
		t.Fatal(err)
	}
	apply := func() { run(raw, "apply", "--server-side", "--field-manager=rtk-custody-fixture", "-f", "-") }
	apply()
	prefix := environment + "/" + binding.Generation + "/api-state"
	reader := environmentCustodyReaderName(prefix)
	run(nil, "get", "serviceaccount", reader, "-n", namespace)
	name := environmentCustodyLeaseName(prefix, state)
	run(nil, "patch", "lease", name, "-n", namespace, "--type=merge", "-p", `{"spec":{"holderIdentity":"original-native-writer","leaseDurationSeconds":30}}`)
	apply()
	var lease struct {
		Spec struct {
			Holder string `json:"holderIdentity"`
		} `json:"spec"`
	}
	if json.Unmarshal(run(nil, "get", "lease", name, "-n", namespace, "-o", "json"), &lease) != nil || lease.Spec.Holder != "original-native-writer" {
		t.Fatal("reapplying the pre-created Lease cleared its live owner")
	}
	for _, check := range []struct{ account, verb, resource, want string }{
		{"api", "get", "leases/" + name, "yes"}, {"api", "update", "leases/" + name, "yes"},
		{"api", "get", "leases/foreign-identity", "no"}, {"api", "update", "leases/foreign-identity", "no"}, {"api", "create", "leases", "no"},
		{reader, "get", "leases/" + name, "no"}, {reader, "update", "leases/" + name, "no"},
	} {
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--request-timeout=10s", "auth", "can-i", check.verb, check.resource, "-n", namespace, "--as=system:serviceaccount:"+namespace+":"+check.account)
		answer, err := cmd.Output()
		if strings.TrimSpace(string(answer)) != check.want || (check.want == "yes" && err != nil) {
			t.Fatal("actual custody RBAC differs from its exact original identity scope", check.verb, check.resource)
		}
	}
}
