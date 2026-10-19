package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeParallelAdmissionPreservesPlanAndPrivateIdentityCleanup(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			kubeconfig, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			store := makeIsolatedTestSecretStore(t, environment)
			backend, err := newEnvironmentKubernetesBackend(string(kubeconfig), environment)
			if err != nil {
				t.Fatal(err)
			}
			if backend.AdmissionConcurrency() != environmentDeploymentCheckConcurrency || backend.AdmissionConcurrency() != 12 {
				t.Fatal("native admission must use twelve independent checks")
			}
			directory := t.TempDir()
			log := filepath.Join(directory, "calls")
			kubectl := filepath.Join(directory, "kubectl")
			writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
 *' get ConfigMap '*)
   while test "$1" != get; do shift; done
   shift; shift
   printf 'get:%s\n' "$1" >> "$RTK_NATIVE_ADMISSION_LOG"
   cat "$RTK_NATIVE_ADMISSION_OBJECTS/$1.json";;
 *' replace -f - -o json --dry-run=server')
   printf 'dry-run\n' >> "$RTK_NATIVE_ADMISSION_LOG"
   cat;;
 *) exit 2;;
esac
`)
			if err := os.Chmod(kubectl, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_NATIVE_ADMISSION_LOG", log)
			t.Setenv("RTK_NATIVE_ADMISSION_OBJECTS", directory)
			stack := "video-cloud-" + environment
			var definitions []deploymentplan.Definition
			var live []deploymentplan.Object
			for index := 0; index < 24; index++ {
				name := fmt.Sprintf("owned-%02d", index)
				object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": stack + "-platform", "uid": "owner-" + name, "resourceVersion": "4", "labels": map[string]any{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "data": map[string]any{"setting": "original"}}
				raw, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(directory, name+".json"), string(raw))
				wanted := deploymentplan.Projection(object)
				if index%3 != 0 {
					deploymentplan.Map(wanted["data"])["setting"] = "updated"
				}
				definitions = append(definitions, deploymentplan.Definition{Object: wanted, Phase: "configuration", Retain: index%3 == 0})
				live = append(live, object)
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("configuration"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, definitions, live)
			if err != nil {
				t.Fatal(err)
			}
			before := plan.SHA256()
			var out bytes.Buffer
			if err := runEnvironmentProgress(context.Background(), &out, "deployment.execution.admission", func(ctx context.Context) error { return plan.Admit(ctx, backend) }); err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || strings.Count(string(calls), "get:") != 24 || strings.Count(string(calls), "dry-run\n") != 16 || plan.SHA256() != before || !strings.Contains(out.String(), "checked=24/24") || strings.Contains(out.String(), "[PASS]") {
				t.Fatal("parallel native admission skipped fresh reads, changed the plan or claimed qualification")
			}
			for index := 0; index < 24; index++ {
				if strings.Count(string(calls), fmt.Sprintf("get:owned-%02d\n", index)) != 1 {
					t.Fatal("admission did not freshly read each original resource exactly once")
				}
			}
			entries, err := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
			if err != nil || len(entries) != 0 {
				t.Fatal("parallel API calls left private identity material behind")
			}
		})
	}
}
