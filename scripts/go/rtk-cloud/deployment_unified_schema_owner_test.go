package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type schemaOwnerFixture struct {
	cfg                     deploymentConfig
	store                   secretStore
	release                 environmentRelease
	plan                    *deploymentplan.Plan
	objects                 map[string]deploymentplan.Object
	job                     deploymentplan.Definition
	state, missing, jobPath string
}

func newSchemaOwnerFixture(t *testing.T, environment string) schemaOwnerFixture {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repos", "rtk_billing")
	writeTestFile(t, filepath.Join(repo, "migrations", "001_original.sql"), "-- first")
	writeTestFile(t, filepath.Join(repo, "migrations", "002_required.sql"), "-- second")
	migrationSourceGit(t, repo, "init", "-q")
	migrationSourceGit(t, repo, "add", ".")
	migrationSourceGit(t, repo, "commit", "-qm", "exact published owner migrations")
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:" + strings.Repeat("a", 64), SourceRevision: migrationSourceGit(t, repo, "rev-parse", "HEAD")}
	stack := "video-cloud-" + environment
	f := schemaOwnerFixture{cfg: deploymentConfig{Environment: environment, Workspace: workspace, Values: map[string]string{"CLOUD_STACK_NAME": stack, "LKE_IMAGE_PULL_SECRET_NAME": "selected-pull"}}, store: store, release: environmentRelease{Environment: environment, Stack: stack, Images: map[string]environmentReleaseImage{"LKE_BILLING_IMAGE": image}}, objects: map[string]deploymentplan.Object{}}
	postgres := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "postgresql", "namespace": stack + "-platform", "uid": "11111111-1111-4111-8111-111111111111", "resourceVersion": "1"}, "spec": deploymentplan.Object{"replicas": float64(1)}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "postgresql-0", "namespace": stack + "-platform", "uid": "22222222-2222-4222-8222-222222222222", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "uid": deploymentplan.Map(postgres["metadata"])["uid"], "controller": true}}}}
	for _, v := range []deploymentplan.Object{postgres, pod} {
		f.objects[deploymentplan.ID(v)] = v
	}
	app := func(name string) deploymentplan.Definition {
		return deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": stack + "-billing"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "image": image.Reference}}}}}}}
	}
	defs := []deploymentplan.Definition{{Object: postgres, Phase: "workload", Retain: true}, {Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": deploymentplan.Object{"name": "allow-postgres-clients", "namespace": stack + "-platform"}}, Phase: "configuration"}, app("billing"), app("billing-payment-worker")}
	var err error
	defs, err = bindEnvironmentInitialSchemaOwner(defs, environment, stack, "create", f.release, "selected-pull")
	if err != nil {
		t.Fatal(err)
	}
	f.job = defs[len(defs)-1]
	f.plan, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest(f.release), BindingsSHA256: deploymentplan.Digest("owners")}, defs, []deploymentplan.Object{postgres}, map[string]string{deploymentplan.ID(postgres): deploymentplan.String(deploymentplan.Map(postgres["metadata"])["uid"])})
	if err != nil {
		t.Fatal(err)
	}
	f.state, f.missing, f.jobPath = filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "job")
	f.setState(t, false, 0)
	writeTestFile(t, f.missing, "[]")
	writeTestFile(t, f.jobPath, "null")
	t.Setenv("RTK_SCHEMA_STATE", f.state)
	t.Setenv("RTK_SCHEMA_MISSING", f.missing)
	t.Setenv("RTK_SCHEMA_JOB", f.jobPath)
	deploymentRuntimeTestKubectl(t, `case "$*" in
 *"get Job "*) cat "$RTK_SCHEMA_JOB" ;;
 *"json_build_object('database'"*) cat "$RTK_SCHEMA_STATE" ;;
 *"json_agg(required.version)"*) cat "$RTK_SCHEMA_MISSING" ;;
 *"wait --for=condition=complete"*) exit 0 ;;
 *) exit 94 ;;
esac`)
	t.Setenv("PATH", filepath.Dir(os.Getenv("RTK_CLOUD_KUBECTL"))+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f schemaOwnerFixture) setState(t *testing.T, history bool, objects int) {
	t.Helper()
	raw, _ := json.Marshal(environmentSchemaState{Database: "rtk_billing", Schema: "public", History: history, Objects: objects})
	writeTestFile(t, f.state, string(raw))
}
func (f schemaOwnerFixture) read(ctx context.Context, id string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(f.objects[id]), ctx.Err()
}

func TestSchemaOwnerQualificationDistinguishesInitialAndExistingDatabases(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"initial", "applied", "applied-unstarted-job", "upgrade-empty", "no-producer", "nonempty-no-history", "missing-version", "wrong-job", "missing-edge", "existing-api", "existing-cronjob", "unrecorded-job", "database-owner-drift", "query-pod-drift", "null-evidence", "foreign-schema"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newSchemaOwnerFixture(t, environment)
				switch mode {
				case "applied":
					f.setState(t, true, 9)
					jobID := deploymentplan.ID(f.job.Object)
					f.plan.Resources = slices.DeleteFunc(f.plan.Resources, func(r deploymentplan.Resource) bool { return r.ID == jobID })
					for i := range f.plan.Resources {
						f.plan.Resources[i].Dependencies = slices.DeleteFunc(f.plan.Resources[i].Dependencies, func(id string) bool { return id == jobID })
					}
					f.plan.Operation = "upgrade"
				case "applied-unstarted-job":
					f.setState(t, true, 9)
				case "upgrade-empty":
					f.plan.Operation = "upgrade"
				case "no-producer":
					f.plan.Resources = slices.DeleteFunc(f.plan.Resources, func(r deploymentplan.Resource) bool { return r.Desired["kind"] == "Job" })
				case "nonempty-no-history":
					f.setState(t, false, 1)
				case "missing-version":
					f.setState(t, true, 9)
					writeTestFile(t, f.missing, `["002_required.sql"]`)
				case "wrong-job":
					for i := range f.plan.Resources {
						if f.plan.Resources[i].Desired["kind"] == "Job" {
							deploymentplan.Map(certIssuerObjectList(environmentZammadPod(f.plan.Resources[i].Desired)["containers"])[0])["command"] = []any{"/rtk-billing"}
						}
					}
				case "missing-edge":
					for i := range f.plan.Resources {
						if f.plan.Resources[i].Desired["kind"] == "Deployment" {
							f.plan.Resources[i].Dependencies = nil
						}
					}
				case "existing-api":
					id := "Deployment/" + f.plan.Stack + "-billing/billing"
					f.objects[id] = deploymentplan.Object{"kind": "Deployment"}
				case "existing-cronjob":
					cron := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": deploymentplan.Object{"name": "billing-batch", "namespace": f.plan.Stack + "-billing"}, "spec": deploymentplan.Object{"jobTemplate": deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"image": f.release.Images["LKE_BILLING_IMAGE"].Reference}}}}}}}}
					f.plan.Resources = append(f.plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(cron), Desired: cron, Action: "create", Phase: "workload", Dependencies: []string{deploymentplan.ID(f.job.Object)}})
					f.objects[deploymentplan.ID(cron)] = cron
				case "unrecorded-job":
					f.objects[deploymentplan.ID(f.job.Object)] = deploymentplan.Clone(f.job.Object)
				case "database-owner-drift":
					deploymentplan.Map(f.objects["StatefulSet/"+f.plan.Stack+"-platform/postgresql"]["metadata"])["uid"] = "foreign"
				case "query-pod-drift":
					deploymentplan.Map(f.objects["Pod/"+f.plan.Stack+"-platform/postgresql-0"]["metadata"])["ownerReferences"] = []any{}
				case "null-evidence":
					writeTestFile(t, f.state, `{"database":"rtk_billing","schema":"public","history":null,"objects":null}`)
				case "foreign-schema":
					writeTestFile(t, f.state, `{"database":"rtk_billing","schema":"other","history":false,"objects":0}`)
				}
				err := verifyEnvironmentReleaseSchemasForPlan(t.Context(), f.cfg, f.store, f.release, f.plan, f.read)
				pass := mode == "initial" || mode == "applied"
				if (err == nil) != pass {
					t.Fatalf("mode=%s error=%v", mode, err)
				}
				if err != nil {
					var b *deploymentplan.Blocker
					if !errors.As(environmentSchemaQualificationError(err), &b) || b.ID != "deployment.compatibility.schema" || b.Resource == "" {
						t.Fatal("safe owner diagnostic lost", err)
					}
				}
			})
		}
	}
}

func TestSchemaOwnerNativeCompletionRequiresExactVersionsAndOriginalUID(t *testing.T) {
	for _, mode := range []string{"complete", "missing-version", "job-owner-drift", "missing-completion", "wrong-workspace", "wrong-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newSchemaOwnerFixture(t, "dev")
			f.setState(t, true, 9)
			actual := deploymentplan.Clone(f.job.Object)
			deploymentplan.Map(actual["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			actual["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
			r := deploymentplan.Resource{ID: deploymentplan.ID(f.job.Object), Desired: f.job.Object, Action: "create", Phase: "migration", UID: deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])}
			backend := environmentKubernetesBackend{Store: f.store, SchemaWorkspace: f.cfg.Workspace, RolloutTimeout: time.Second, KubeconfigContent: []byte("isolated-only")}
			switch mode {
			case "missing-version":
				writeTestFile(t, f.missing, `["002_required.sql"]`)
			case "job-owner-drift":
				deploymentplan.Map(actual["metadata"])["uid"] = "foreign"
			case "missing-completion":
				delete(actual, "status")
			case "wrong-workspace":
				backend.SchemaWorkspace = t.TempDir()
			case "wrong-source":
				deploymentplan.Map(deploymentplan.Map(r.Desired["metadata"])["annotations"])[environmentBillingSchemaRevision] = strings.Repeat("f", 40)
			}
			raw, _ := json.Marshal(actual)
			writeTestFile(t, f.jobPath, string(raw))
			if err := backend.Wait(t.Context(), r); (err == nil) != (mode == "complete") {
				t.Fatal("native Job readback", err)
			}
		})
	}
}

func TestSchemaOwnerDiagnosticKeepsSafeServiceAndRedactsUnknownToolOutput(t *testing.T) {
	safe := environmentSchemaBlocker("billing", "original database has no migration history")
	if environmentSchemaQualificationError(safe) != safe {
		t.Fatal("typed safe owner cause was replaced")
	}
	private := "postgres://private-user:private-password@private-host/private-database"
	if strings.Contains(environmentSchemaQualificationError(errors.New(private)).Error(), "private-") {
		t.Fatal("unknown tool output leaked")
	}
	cancel, stop := context.WithCancel(t.Context())
	stop()
	f := newSchemaOwnerFixture(t, "dev")
	if !errors.Is(verifyEnvironmentReleaseSchemasForPlan(cancel, f.cfg, f.store, f.release, f.plan, f.read), context.Canceled) {
		t.Fatal("schema read ignored caller cancellation")
	}
}

func TestSchemaOwnerRecipeUsesSeparatePodAndSelectedPullIdentity(t *testing.T) {
	f := newSchemaOwnerFixture(t, "dev")
	pod := environmentZammadPod(f.job.Object)
	if deploymentplan.Map(deploymentplan.Map(f.job.Object["spec"])["template"])["metadata"] == nil || deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(f.job.Object["spec"])["template"])["metadata"])["labels"] == nil {
		t.Fatal("Job lacks native labels")
	}
	labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(f.job.Object["spec"])["template"])["metadata"])["labels"])
	if labels["app.kubernetes.io/name"] == "billing" || labels["app.kubernetes.io/name"] == "billing-payment-worker" {
		t.Fatal("migration Pod is selected by application Service")
	}
	if deploymentplan.Map(certIssuerObjectList(pod["imagePullSecrets"])[0])["name"] != "selected-pull" {
		t.Fatal("selected pull identity was replaced")
	}
	for _, r := range f.plan.Resources {
		if r.Desired["kind"] == "Deployment" && !slices.Contains(r.Dependencies, deploymentplan.ID(f.job.Object)) {
			t.Fatal("application may start before schema owner")
		}
	}
	_, err := os.Stat(filepath.Join(f.store.Root, "deployment", "rollback"))
	if !os.IsNotExist(err) {
		t.Fatal("read-only schema qualification wrote an execution journal")
	}
}

type schemaOwnerLifecycleBackend struct {
	*normalTailE2EBackend
	f                     schemaOwnerFixture
	native                environmentCreateKubernetesBackend
	failure               string
	interrupted           bool
	postgresPath, podPath string
}

func (b *schemaOwnerLifecycleBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry && body["kind"] == "Job" {
		if err := b.native.beforeSchemaOwnerWrite(ctx, body); err != nil {
			return nil, err
		}
	}
	actual, err := b.normalTailE2EBackend.Write(ctx, action, body, dry)
	if err == nil && !dry && body["kind"] == "Job" {
		raw, _ := json.Marshal(actual)
		writeErr := os.WriteFile(b.f.jobPath, raw, 0600)
		if writeErr != nil {
			return nil, writeErr
		}
		if b.failure == "unknown-job-write" && !b.interrupted {
			b.interrupted = true
			return nil, errors.New("native Job response unknown")
		}
	}
	return actual, err
}

func (b *schemaOwnerLifecycleBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Desired["kind"] != "Job" {
		return b.normalTailE2EBackend.Wait(ctx, r)
	}
	if !b.interrupted && (b.failure == "job-written" || b.failure == "job-partial-schema") {
		b.interrupted = true
		if b.failure == "job-partial-schema" {
			raw, _ := json.Marshal(environmentSchemaState{Database: "rtk_billing", Schema: "public", History: true, Objects: 3})
			_ = os.WriteFile(b.f.state, raw, 0600)
			_ = os.WriteFile(b.f.missing, []byte(`["002_required.sql"]`), 0600)
		}
		return errors.New("interrupted original Job readiness")
	}
	actual := b.objects[r.ID]
	actual["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	raw, _ := json.Marshal(actual)
	if err := os.WriteFile(b.f.jobPath, raw, 0600); err != nil {
		return err
	}
	raw, _ = json.Marshal(environmentSchemaState{Database: "rtk_billing", Schema: "public", History: true, Objects: 9})
	if err := os.WriteFile(b.f.state, raw, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(b.f.missing, []byte(`[]`), 0600); err != nil {
		return err
	}
	// This is the production parent wait adapter, including its original child
	// write UID and explicit cfg workspace; only kubectl/SQL are isolated.
	if err := b.native.Wait(ctx, r); err != nil {
		return err
	}
	if b.failure == "after-job-complete" && !b.interrupted {
		b.interrupted = true
		return errors.New("interrupted after native completion/readback")
	}
	return nil
}

func TestSchemaOwnerOriginalChildRecoveryNeverRecreatesMigration(t *testing.T) {
	for _, mode := range []string{"complete", "before-admission", "job-written", "job-partial-schema", "after-job-complete", "application-readiness", "acceptance", "unknown-job-write"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, a := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			f := newSchemaOwnerFixture(t, "dev")
			f.store = store
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
			h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"isolated-verified-bootstrap": deploymentplan.Digest("original-terminal")}}
			f.plan.CreateParentSHA256 = e.Journal.PlanSHA256
			f.plan.CreateHandoffSHA256 = deploymentplan.Digest(h.Pins)
			native := environmentCreateKubernetesBackend{environmentKubernetesBackend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-only"), RolloutTimeout: time.Second, SchemaWorkspace: "/deliberately-wrong-unselected-workspace"}, cfg: f.cfg, store: store, execution: e, stagePlanSHA256: f.plan.SHA256()}
			b := &schemaOwnerLifecycleBackend{normalTailE2EBackend: &normalTailE2EBackend{e: e, objects: f.objects, writes: map[string]int{}}, f: f, native: native, failure: mode, postgresPath: filepath.Join(t.TempDir(), "postgres"), podPath: filepath.Join(t.TempDir(), "pod")}
			for id, path := range map[string]string{"StatefulSet/" + f.plan.Stack + "-platform/postgresql": b.postgresPath, "Pod/" + f.plan.Stack + "-platform/postgresql-0": b.podPath} {
				raw, _ := json.Marshal(f.objects[id])
				writeTestFile(t, path, string(raw))
			}
			t.Setenv("RTK_SCHEMA_POSTGRES", b.postgresPath)
			t.Setenv("RTK_SCHEMA_POD", b.podPath)
			deploymentRuntimeTestKubectl(t, `case "$*" in
 *"get StatefulSet postgresql "*) cat "$RTK_SCHEMA_POSTGRES" ;;
 *"get Pod postgresql-0 "*) cat "$RTK_SCHEMA_POD" ;;
 *"get Deployment "*|*"get CronJob "*) exit 0 ;;
 *"get Job "*) if test "$(cat "$RTK_SCHEMA_JOB")" != null; then cat "$RTK_SCHEMA_JOB"; fi ;;
 *"json_build_object('database'"*) cat "$RTK_SCHEMA_STATE" ;;
 *"json_agg(required.version)"*) cat "$RTK_SCHEMA_MISSING" ;;
 *"wait --for=condition=complete"*) exit 0 ;;
 *) exit 95 ;;
esac`)
			t.Setenv("PATH", filepath.Dir(os.Getenv("RTK_CLOUD_KUBECTL"))+string(os.PathListSeparator)+os.Getenv("PATH"))
			beforeAdmit, acceptFailed := false, false
			qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
				if mode == "before-admission" && !beforeAdmit {
					beforeAdmit = true
					return errors.New("interrupted before admission")
				}
				return verifyEnvironmentReleaseSchemasForPlan(ctx, f.cfg, store, f.release, p, b.Read)
			}
			accept := func(context.Context, *deploymentplan.Plan) error {
				if mode == "acceptance" && !acceptFailed {
					acceptFailed = true
					return errors.New("interrupted acceptance")
				}
				return nil
			}
			fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			if mode == "application-readiness" {
				b.normalTailE2EBackend.failure = "after-write-workload"
			}
			run := func() error {
				return executeCreateNormalTailWorkloadBackend(t.Context(), store, h, f.plan, b, qualify, fence, accept, io.Discard, func(context.Context) error { return nil })
			}
			first := run()
			if mode == "complete" {
				if first != nil {
					t.Fatal(first)
				}
			} else if first == nil {
				t.Fatal("required interruption did not execute")
			}
			if mode == "unknown-job-write" {
				writes := deploymentplan.Digest(b.writes)
				if run() == nil || deploymentplan.Digest(b.writes) != writes {
					t.Fatal("unknown native outcome replayed a write")
				}
				return
			}
			if first != nil {
				if err := run(); err != nil {
					t.Fatal("known original child failed to resume", err)
				}
			}
			j, exists, err := f.plan.ReadExecution(store.Root)
			if err != nil || !exists || !j.Accepted || j.Owners[deploymentplan.ID(f.job.Object)] == "" {
				t.Fatal("original successful migration has no genuine child acceptance", err)
			}
			if b.writes[deploymentplan.ID(f.job.Object)] != 1 {
				t.Fatal("original schema migration was recreated", b.writes)
			}
			for _, r := range f.plan.Resources {
				if r.Action != "retain" && b.writes[r.ID] != 1 {
					t.Fatal("normal consumer write replayed", r.ID, b.writes)
				}
			}
			if err := run(); err != nil {
				t.Fatal("completed parent stage did not remain read-only", err)
			}
			if b.writes[deploymentplan.ID(f.job.Object)] != 1 {
				t.Fatal("completed stage replayed migration")
			}
		})
	}
}

func TestSchemaOwnerMaintainedFreshCatalogHasRealProducerAndConsumerEdges(t *testing.T) {
	cfg, store, release, operator, bootstrap := createDependencyContractFixture(t)
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		t.Fatal(err)
	}
	values, chart, err := readEnvironmentZammadInputs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	support, err := renderEnvironmentZammad(t.Context(), cfg, store, release, "create", values, chart)
	if err != nil {
		t.Fatal(err)
	}
	var definitions []deploymentplan.Definition
	valuesForCreate := appendMap(appendMap(cfg.Values, cfg.AdapterValues), cfg.AdapterResolved)
	valuesForCreate = appendMap(valuesForCreate, deploymentLegacyLKEValues(valuesForCreate, cfg.Environment))
	valuesForCreate = appendMap(valuesForCreate, cfg.DNSValues)
	valuesForCreate["CLOUD_ENV_NAME"] = cfg.Environment
	err = withEnvironmentRenderValues(store, release, environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}, valuesForCreate, func(env map[string]string) error {
		intent, err := environmentCreateDependencyOutputs(cfg, bootstrap, env)
		if err != nil {
			return err
		}
		definitions, err = renderEnvironmentResourceCatalog(env, environmentIdentityBindings{Environment: cfg.Environment, Stack: release.Stack}, release, intent, support)
		if err != nil {
			return err
		}
		definitions, err = bindEnvironmentInitialSchemaOwner(definitions, cfg.Environment, release.Stack, "create", release, env["LKE_IMAGE_PULL_SECRET_NAME"])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	job := environmentBillingSchemaJob(cfg.Environment, release.Stack, release.Images["LKE_BILLING_IMAGE"], cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"])
	jobID := deploymentplan.ID(job.Object)
	seen, consumers, policy := 0, 0, false
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == jobID {
			seen++
			if d.Phase != "migration" || deploymentplan.Map(d.Object["spec"])["backoffLimit"] != float64(6) {
				t.Fatal("migration lacks bounded native same-Job retries")
			}
		}
		if deploymentplan.ID(d.Object) == "StatefulSet/"+release.Stack+"-platform/postgresql" && d.Phase != "configuration" {
			t.Fatal("database readiness is after migration")
		}
		if environmentSchemaApplication(d.Object) && environmentSchemaUsesBilling(d.Object) {
			consumers++
			if !slices.Contains(d.Dependencies, jobID) {
				t.Fatal("maintained Billing consumer has no schema edge", deploymentplan.ID(d.Object))
			}
		}
		if deploymentplan.ID(d.Object) == "NetworkPolicy/"+release.Stack+"-platform/allow-postgres-clients" {
			raw, _ := json.Marshal(d.Object)
			policy = strings.Contains(string(raw), release.Stack+"-billing") && strings.Contains(string(raw), "5432")
		}
		if d.Object["kind"] == "Service" && deploymentplan.Map(d.Object["metadata"])["namespace"] == release.Stack+"-billing" {
			selector := deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["selector"])
			if len(selector) > 0 && deploymentplan.Matches(deploymentplan.Object{"labels": selector}, deploymentplan.Object{"labels": deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(job.Object["spec"])["template"])["metadata"])["labels"]}) {
				t.Fatal("business Service selects migration Pod")
			}
		}
	}
	if seen != 1 || consumers < 2 || !policy {
		t.Fatal("maintained full catalog lost initial owner or native network prerequisites", seen, consumers, policy)
	}
	// This exact fresh adapter validates future producer edges without creating
	// a database, kubeconfig, native Job, or invented migration history.
	if _, err := qualifyEnvironmentCreateDependencyContract(t.Context(), cfg, release, store, operator, bootstrap); err != nil {
		t.Fatal("shared fresh contract", err)
	}
	if _, err := os.Lstat(store.KubeconfigPath()); !os.IsNotExist(err) {
		t.Fatal("fresh qualification created native database access material")
	}
}
