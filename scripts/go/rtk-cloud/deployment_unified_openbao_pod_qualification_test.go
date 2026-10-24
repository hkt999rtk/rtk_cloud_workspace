package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoPodQualificationFixture(t *testing.T, environment string) (*deploymentplan.Plan, []deploymentplan.Definition, []deploymentplan.Object) {
	t.Helper()
	env, release, original := existingFileOpenBaoFixture(environment, true)
	bindings, err := observeEnvironmentBindings(environment, release, original)
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderEnvironmentOpenBao(env, bindings.Pods[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = applyEnvironmentPodBindings(want, bindings, release); err != nil {
		t.Fatal(err)
	}
	if err = bindOpenBaoAllocations([]deploymentplan.Definition{{Object: want}}, original); err != nil {
		t.Fatal(err)
	}
	controller := deploymentplan.Clone(want)
	meta := deploymentplan.Map(controller["metadata"])
	meta["uid"], meta["resourceVersion"], meta["generation"] = "original-controller", "2", float64(2)
	meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": env["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}
	controller["status"] = deploymentplan.Object{"observedGeneration": float64(2), "updateRevision": "target-revision"}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": meta["namespace"], "uid": "original-pod", "resourceVersion": "4", "labels": deploymentplan.Object{"controller-revision-hash": "old-revision"}, "ownerReferences": []any{deploymentplan.Object{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "original-controller"}}}, "spec": deploymentplan.Clone(environmentPKIPod(want)), "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}
	defs := []deploymentplan.Definition{{Object: want, Phase: "workload"}}
	live := []deploymentplan.Object{controller, pod}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: env["CLOUD_STACK_NAME"], Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("configuration"), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Resources) != 1 || p.Resources[0].Action != "retain" {
		t.Fatal("fixture did not reproduce ordinary implicit retain")
	}
	return p, defs, live
}

func TestOpenBaoPodQualificationSelectsExplicitLifecycleBeforeApplication(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		for _, mode := range []string{"old-revision", "old-executable", "current", "current-defaults", "current-not-ready", "current-image-drift", "current-init-drift", "explicit-retain", "missing-pod", "foreign-pod", "pod-no-rv", "pod-terminating", "missing-revision", "unobserved", "controller-drift", "duplicate-pod"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				p, defs, live := openBaoPodQualificationFixture(t, environment)
				controller, pod := live[0], live[1]
				meta := deploymentplan.Map(pod["metadata"])
				labels := deploymentplan.Map(meta["labels"])
				if strings.HasPrefix(mode, "current") {
					labels["controller-revision-hash"] = "target-revision"
				}
				switch mode {
				case "old-executable", "current-image-drift":
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "old-reviewed-image"
				case "current-init-drift":
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"])[0])["image"] = "unexpected-private-value"
				case "current-not-ready":
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"])[0])["status"] = "False"
				case "current-defaults":
					for _, group := range []string{"containers", "initContainers"} {
						for _, raw := range certIssuerObjectList(deploymentplan.Map(pod["spec"])[group]) {
							for _, mount := range certIssuerObjectList(deploymentplan.Map(raw)["volumeMounts"]) {
								if deploymentplan.Map(mount)["readOnly"] == false {
									delete(deploymentplan.Map(mount), "readOnly")
								}
							}
						}
					}
				case "explicit-retain":
					defs[0].Retain = true
				case "missing-pod":
					live = live[:1]
				case "foreign-pod":
					deploymentplan.Map(certIssuerObjectList(meta["ownerReferences"])[0])["uid"] = "foreign-controller"
				case "pod-no-rv":
					delete(meta, "resourceVersion")
				case "pod-terminating":
					meta["deletionTimestamp"] = "2026-10-19T00:00:00Z"
				case "missing-revision":
					delete(labels, "controller-revision-hash")
				case "unobserved":
					deploymentplan.Map(controller["status"])["observedGeneration"] = float64(1)
				case "controller-drift":
					deploymentplan.Map(controller["metadata"])["uid"] = "foreign-controller"
				case "duplicate-pod":
					live = append(live, deploymentplan.Clone(pod))
				}
				before := deploymentplan.Digest(p)
				err := qualifyEnvironmentOnDeleteOpenBaoPlan(p, defs, live)
				update := mode == "old-revision" || mode == "old-executable"
				retained := mode == "current" || mode == "current-defaults" || mode == "current-not-ready"
				if (err == nil) != (update || retained) {
					t.Fatal("qualification decision", err)
				}
				if err != nil && (deploymentplan.Digest(p) != before || strings.Contains(err.Error(), "unexpected-private-value")) {
					t.Fatal("failure changed plan or exposed runtime values", err)
				}
				if retained && deploymentplan.Digest(p) != before {
					t.Fatal("current Pod changed retained intent")
				}
				if update {
					if p.Resources[0].Action != "update" {
						t.Fatal("old revision was not an explicit update")
					}
					p.Resources[0].Action = "retain"
					if deploymentplan.Digest(p) != before {
						t.Fatal("qualification changed desired, baseline, owner or dependencies")
					}
					p.Resources[0].Action = "update"
					permissions, err := environmentPlanAccessRequirements(p)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, permission := range permissions {
						found = found || permission.Resource == "pods" && permission.Name == "openbao-0" && permission.Verb == "delete"
					}
					if !found {
						t.Fatal("qualified restart omitted existing delete admission")
					}
				}
			})
		}
	}
}

func TestOpenBaoPodQualificationNormalProgramPreviewUsesRestartAdmission(t *testing.T) {
	for _, mode := range []string{"old-revision", "current", "missing-pod", "explicit-retain"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, artifact := normalTailProgramFixture(t)
			_, definitions, live := openBaoPodQualificationFixture(t, "dev")
			if mode == "current" {
				deploymentplan.Map(deploymentplan.Map(live[1]["metadata"])["labels"])["controller-revision-hash"] = "target-revision"
			}
			if mode == "missing-pod" {
				live = live[:1]
			}
			if mode == "explicit-retain" {
				definitions[0].Retain = true
			}
			artifact.Program.Definitions = append(artifact.Program.Definitions, definitions...)
			artifact.Program.Products.SourceDefinitions = artifact.Program.Definitions
			artifact.Program.Bindings.Owners[deploymentplan.ID(live[0])] = "original-controller"
			before := deploymentplan.Digest(artifact.Program)
			plan, err := createNormalTailProgramAdmissionPreview(cfg, &artifact.Program, live)
			if (err == nil) != (mode == "old-revision" || mode == "current") {
				t.Fatal("program preview decision", err)
			}
			if deploymentplan.Digest(artifact.Program) != before {
				t.Fatal("preview changed admitted source intent")
			}
			if err != nil {
				return
			}
			found := false
			for _, resource := range plan.Resources {
				if resource.ID != deploymentplan.ID(live[0]) {
					continue
				}
				found = true
				want := "retain"
				if mode == "old-revision" {
					want = "update"
				}
				if resource.Action != want {
					t.Fatal("pre-admission Pod lifecycle was omitted", resource.Action)
				}
			}
			if !found {
				t.Fatal("preview omitted the original OpenBao controller")
			}
		})
	}
}

type openBaoPodQualificationLease struct{ plan string }

func (l *openBaoPodQualificationLease) Owner() deploymentplan.LeaseOwner {
	return deploymentplan.LeaseOwner{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-2222-4333-8444-555555555555", Holder: l.plan + ":" + strings.Repeat("a", 32)}
}
func (l *openBaoPodQualificationLease) Check(ctx context.Context) error { return ctx.Err() }
func (l *openBaoPodQualificationLease) Release(context.Context) error   { return nil }

type openBaoPodQualificationBackend struct {
	native        environmentKubernetesBackend
	writes, stops int
}

func (b *openBaoPodQualificationBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	return b.native.Read(ctx, id)
}
func (b *openBaoPodQualificationBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		b.writes++
	}
	return b.native.Write(ctx, action, body, dry)
}
func (b *openBaoPodQualificationBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	if b.stops > 0 {
		b.stops--
		return errors.New("isolated known-controller-response interruption")
	}
	return b.native.Wait(ctx, resource)
}
func (b *openBaoPodQualificationBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	return &openBaoPodQualificationLease{plan: p.SHA256()}, ctx.Err()
}

func TestOpenBaoPodQualificationOrdinaryEngineConvergesAndResumesSamePlan(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "known-controller-response"}[interrupted], func(t *testing.T) {
			p, defs, live := openBaoPodQualificationFixture(t, "dev")
			old := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(live[1]["spec"])["containers"])[0])
			old["image"] = "old-reviewed-image"
			if err := qualifyEnvironmentOnDeleteOpenBaoPlan(p, defs, live); err != nil {
				t.Fatal(err)
			}
			planSHA := p.SHA256()
			directory := t.TempDir()
			for name, body := range map[string]deploymentplan.Object{"controller.json": live[0], "pod.json": live[1]} {
				raw, _ := json.Marshal(body)
				writeTestFile(t, filepath.Join(directory, name), string(raw))
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			stub := "#!" + python + "\n" + `import sys,json,os
from pathlib import Path
root=Path(os.environ['RTK_OPENBAO_QUALIFICATION_ROOT']);args=sys.argv[4:]
controller=json.loads((root/'controller.json').read_text())
if args[0]=='get':
 if args[1]=='StatefulSet': print(json.dumps(controller))
 elif args[1]=='Pod':
  pod=json.loads((root/'pod.json').read_text())
  if (root/'deleted').exists():
   pod['metadata']['uid']='replacement-pod';pod['metadata']['resourceVersion']='5';pod['metadata']['labels']['controller-revision-hash']='target-revision';pod['spec']=controller['spec']['template']['spec']
  print(json.dumps(pod))
 else: sys.exit(2)
elif args[0]=='replace':
 body=json.load(sys.stdin);assert body['metadata']['uid']==controller['metadata']['uid']
 if '--dry-run=server' not in args:
  body['status']=controller['status'];body['metadata']['resourceVersion']='3';(root/'controller.json').write_text(json.dumps(body))
 print(json.dumps(body))
elif args[0]=='delete':
 body=json.load(sys.stdin);assert body['preconditions']=={'uid':'original-pod','resourceVersion':'4'}
 assert '/api/v1/namespaces/video-cloud-dev-secrets/pods/openbao-0' in args[1]
 if 'dryRun=All' in args[1]: (root/'dry-run').touch()
 else:
  assert not (root/'deleted').exists();(root/'deleted').touch()
 print('{}')
else: sys.exit(2)
`
			writeTestFile(t, filepath.Join(directory, "kubectl"), stub)
			if err := os.Chmod(filepath.Join(directory, "kubectl"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_OPENBAO_QUALIFICATION_ROOT", directory)
			raw, _ := json.Marshal(frozenKubernetesFixture(t))
			native, err := newEnvironmentKubernetesBackend(string(raw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			native.RolloutTimeout = 5 * time.Second
			prepared := 0
			native.OpenBaoReplacementReady = func(ctx context.Context, controller, pod deploymentplan.Object) error {
				prepared++
				if deploymentplan.Map(pod["metadata"])["uid"] != "replacement-pod" {
					return errors.New("replacement callback received original Pod")
				}
				return validateOpenBaoPodOwner(controller, pod)
			}
			if err := native.checkOpenBaoRestartAdmission(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(directory, "deleted")); !os.IsNotExist(err) {
				t.Fatal("qualification mutated original Pod")
			}
			b := &openBaoPodQualificationBackend{native: native}
			if interrupted {
				b.stops = 1
			}
			private := t.TempDir()
			if err := os.Chmod(private, 0700); err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return ctx.Err() }
			accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
			run := func() error { return p.ExecuteResumable(t.Context(), b, planSHA, private, check, check, accept) }
			if interrupted {
				if err := run(); err == nil {
					t.Fatal("missing known-response interruption")
				}
				j, exists, err := p.ReadExecution(private)
				if err != nil || !exists || j.Accepted || len(j.Completed) != 1 || len(j.Owners) != 1 || j.InFlight != "" {
					t.Fatal("untruthful interrupted prefix", j, err)
				}
			}
			if err := run(); err != nil {
				j, exists, readErr := p.ReadExecution(private)
				t.Fatal("execution", err, "writes", b.writes, "journal", j, "exists", exists, "read", readErr)
			}
			j, exists, err := p.ReadExecution(private)
			wantPrepared := 1
			if interrupted {
				// Both recorded-phase checks validate the same replacement on resume.
				wantPrepared = 2
			}
			if err != nil || !exists || !j.Accepted || b.writes != 1 || prepared != wantPrepared || p.SHA256() != planSHA {
				t.Fatal("ordinary controlled convergence or retry changed", j, err, b.writes, prepared)
			}
			if _, err := os.Stat(filepath.Join(directory, "deleted")); err != nil {
				t.Fatal("controlled update did not replace the old Pod", err)
			}
		})
	}
}
