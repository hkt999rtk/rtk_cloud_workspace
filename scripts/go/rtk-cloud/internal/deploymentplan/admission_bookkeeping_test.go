package deploymentplan

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type bookkeepingAdmissionBackend struct {
	memoryBackend
	mode string
}

func (b *bookkeepingAdmissionBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if !dry || err != nil {
		return actual, err
	}
	actual = Clone(actual)
	metadata := Map(actual["metadata"])
	Map(metadata["annotations"])["kubectl.kubernetes.io/last-applied-configuration"] = "credential=refreshed-client-bookkeeping"
	if actual["kind"] == "Namespace" {
		switch b.mode {
		case "namespace-spec":
			Map(actual["spec"])["finalizers"] = []any{"unreviewed-finalizer"}
		case "namespace-label":
			Map(metadata["labels"])["pod-security.kubernetes.io/enforce"] = "privileged"
		case "namespace-annotation":
			Map(metadata["annotations"])["policy.example/identity"] = "credential=unapproved-security-policy"
		case "namespace-uid":
			metadata["uid"] = "replacement-owner"
		}
	} else {
		pod := Map(Map(Map(actual["spec"])["template"])["spec"])
		container := Map(pod["containers"].([]any)[0])
		switch b.mode {
		case "workload-spec":
			Map(actual["spec"])["replicas"] = float64(99)
		case "image":
			container["image"] = "registry.example/unreviewed:latest"
		case "env":
			Map(container["env"].([]any)[0])["value"] = "credential=unapproved-runtime-value"
		case "workload-label":
			Map(metadata["labels"])["rtk.realtek.com/stack"] = "different-owner"
		case "workload-annotation":
			Map(metadata["annotations"])["policy.example/identity"] = "credential=unapproved-security-policy"
		case "workload-uid":
			metadata["uid"] = "replacement-owner"
		}
	}
	return actual, nil
}

func TestAdmissionAllowsOnlyKubectlBookkeepingRefreshForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"refresh", "refresh-only-annotation", "namespace-spec", "namespace-label", "namespace-annotation", "namespace-uid", "workload-spec", "image", "env", "workload-label", "workload-annotation", "workload-uid"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, workload, liveWorkload := fixture(t, environment, "Deployment")
				Map(workload.Object["spec"])["replicas"] = float64(2)
				pod := Map(Map(Map(workload.Object["spec"])["template"])["spec"])
				Map(pod["containers"].([]any)[0])["env"] = []any{Object{"name": "RUNTIME_MODE", "value": "qualified"}}
				annotations := Object{"kubectl.kubernetes.io/last-applied-configuration": "original-client-bookkeeping", "policy.example/identity": "qualified-policy"}
				if mode == "refresh-only-annotation" {
					delete(annotations, "policy.example/identity")
				}
				Map(liveWorkload["metadata"])["annotations"] = Clone(annotations)
				namespace := Definition{Phase: "namespace", Object: Object{"apiVersion": "v1", "kind": "Namespace", "metadata": Object{"name": in.Stack + "-account-manager"}}}
				liveNamespace := Clone(namespace.Object)
				Map(liveNamespace["metadata"])["uid"] = "original-namespace-owner"
				Map(liveNamespace["metadata"])["resourceVersion"] = "4"
				Map(liveNamespace["metadata"])["labels"] = Object{"rtk.realtek.com/stack": in.Stack, "pod-security.kubernetes.io/enforce": "restricted"}
				Map(liveNamespace["metadata"])["annotations"] = Clone(annotations)
				liveNamespace["spec"] = Object{"finalizers": []any{"kubernetes"}}
				plan, err := Compile(in, []Definition{namespace, workload}, []Object{liveNamespace, liveWorkload})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Resources) != 2 || plan.Resources[0].Action != "update" || plan.Resources[1].Action != "update" {
					t.Fatal("fixture did not exercise exact existing-resource admission", plan.Resources)
				}
				body, err := plan.Resources[0].Body(liveNamespace)
				if err != nil {
					t.Fatal(err)
				}
				admitted := Clone(body)
				Map(Map(admitted["metadata"])["annotations"])["kubectl.kubernetes.io/last-applied-configuration"] = "refreshed-client-bookkeeping"
				bodyDigest, admittedDigest := Digest(body), Digest(admitted)
				if !matchesAdmission(body, admitted) || Digest(body) != bodyDigest || Digest(admitted) != admittedDigest {
					t.Fatal("admission normalization did not compare independent projections without mutating either body")
				}
				backend := &bookkeepingAdmissionBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(liveNamespace): liveNamespace, ID(liveWorkload): liveWorkload}}, mode: mode}
				digest := plan.SHA256()
				liveDigest := Digest(backend.objects)
				allowed := strings.HasPrefix(mode, "refresh")
				err = plan.Admit(context.Background(), backend)
				if (err == nil) != allowed {
					t.Fatal("admission bookkeeping normalization accepted configuration drift or rejected client metadata", err)
				}
				if plan.SHA256() != digest || Digest(backend.objects) != liveDigest || len(backend.written) != 0 {
					t.Fatal("admission normalization mutated plan, live fixture or persisted resources")
				}
				for _, body := range backend.dry {
					if Map(Map(body["metadata"])["annotations"])["kubectl.kubernetes.io/last-applied-configuration"] != "original-client-bookkeeping" {
						t.Fatal("admission normalization changed the exact execution request body")
					}
				}
				if err != nil {
					var blocker *Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.admission" || strings.Contains(err.Error(), "credential=") {
						t.Fatal("admission drift lost its stable private-safe blocker", err)
					}
				}
				qualified := func(context.Context, *Plan) error { return nil }
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				err = plan.Execute(context.Background(), backend, digest, root, qualified, qualified, qualified)
				if (err == nil) != allowed || plan.SHA256() != digest {
					t.Fatal("execution admission differs from preflight normalization or mutated the plan", err)
				}
				if !allowed && len(backend.written) != 0 {
					t.Fatal("unapproved namespace or consumer configuration persisted")
				}
				if allowed && len(backend.written) != 2 {
					t.Fatal("qualified unchanged resource bodies did not complete execution")
				}
				// Bookkeeping is ignored only in admission; later external changes
				// still invalidate the originally compiled baseline/owner fence.
				changed := Clone(liveNamespace)
				Map(Map(changed["metadata"])["annotations"])["kubectl.kubernetes.io/last-applied-configuration"] = "concurrent-client-bookkeeping"
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("admission normalization weakened the baseline annotation drift fence")
				}
				changed = Clone(liveNamespace)
				Map(changed["metadata"])["uid"] = "replacement-owner"
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("admission normalization weakened the baseline UID fence")
				}
			})
		}
	}
}
