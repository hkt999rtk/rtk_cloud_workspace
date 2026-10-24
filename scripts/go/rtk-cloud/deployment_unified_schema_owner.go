package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentBillingSchemaRevision = "rtk.realtek.com/schema-source-revision"

func environmentSchemaBlocker(source, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.schema", Resource: "Database/rtk_" + strings.ReplaceAll(source, "-", "_"), Cause: cause, Next: "retain the original database; qualify its exact selected-image owner migration and verify all committed migration versions before starting dependent applications; never reset data or claim a future migration has completed"}
}

func environmentReleaseSchemaVersions(ctx context.Context, cfg deploymentConfig, source lkeServiceImageSource, image environmentReleaseImage) ([]string, error) {
	if !deploymentUpgradeSHA.MatchString(image.SourceRevision) {
		return nil, environmentSchemaBlocker(source.Key, "candidate migration source revision is unavailable")
	}
	raw, err := deploymentUpgradeCommand(ctx, "git", "-C", filepath.Join(cfg.Workspace, source.RepoPath), "ls-tree", "-z", image.SourceRevision+":migrations")
	if err != nil {
		return nil, environmentSchemaBlocker(source.Key, "candidate committed migration inventory cannot be read")
	}
	var versions []string
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		metadata, name, found := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) != 3 || name == "" {
			return nil, environmentSchemaBlocker(source.Key, "candidate committed migration inventory is invalid")
		}
		if fields[1] == "blob" && strings.HasSuffix(name, ".sql") {
			versions = append(versions, name)
		}
	}
	if len(versions) == 0 {
		return nil, environmentSchemaBlocker(source.Key, "candidate committed migration inventory is empty")
	}
	sort.Strings(versions)
	return versions, ctx.Err()
}

// This is an initial database owner, not a general upgrade migration. Its
// content-bound Job and every consumer live in the same standard child plan.
func environmentBillingSchemaJob(environment, stack string, image environmentReleaseImage, pullNames ...string) deploymentplan.Definition {
	pull := "ghcr-pull"
	if len(pullNames) == 1 && pullNames[0] != "" {
		pull = pullNames[0]
	}
	name := "billing-migrate-" + deploymentplan.Digest(struct{ Reference, Revision string }{image.Reference, image.SourceRevision})[:16]
	labels := deploymentplan.Object{"app.kubernetes.io/name": name, "app.kubernetes.io/part-of": "rtk-cloud", "app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/provider": "lke", "rtk.realtek.com/stack": stack}
	return deploymentplan.Definition{Phase: "migration", Dependencies: []string{"StatefulSet/" + stack + "-platform/postgresql", "NetworkPolicy/" + stack + "-platform/allow-postgres-clients"}, Object: deploymentplan.Object{
		"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": stack + "-billing", "labels": labels, "annotations": deploymentplan.Object{environmentBillingSchemaRevision: image.SourceRevision}},
		"spec": deploymentplan.Object{"backoffLimit": float64(6), "activeDeadlineSeconds": float64(600), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{
			"restartPolicy": "Never", "automountServiceAccountToken": false,
			"imagePullSecrets": []any{deploymentplan.Object{"name": pull}},
			"containers": []any{deploymentplan.Object{"name": "migrate", "image": image.Reference, "command": []any{"/rtk-billing-migrate"}, "env": []any{
				deploymentplan.Object{"name": "DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "billing-runtime", "key": "DATABASE_URL"}}},
				deploymentplan.Object{"name": "MIGRATIONS_DIR", "value": "/migrations"},
			}, "securityContext": deploymentplan.Object{"runAsNonRoot": true, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}}},
		}}},
	}}
}

func bindEnvironmentInitialSchemaOwner(definitions []deploymentplan.Definition, environment, stack, operation string, release environmentRelease, pullName string, owners ...map[string]string) ([]deploymentplan.Definition, error) {
	if operation != "create" {
		return definitions, nil
	}
	if len(owners) == 1 && owners[0]["Deployment/"+stack+"-billing/billing"] != "" {
		return definitions, nil
	}
	image, present := release.Images["LKE_BILLING_IMAGE"]
	if !present { // Historical/minimal catalogs without Billing keep their scope.
		return definitions, nil
	}
	source, err := deploymentUpgradeImageSource(image.Reference)
	if err != nil || source.Key != "billing" || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || stack != "video-cloud-"+environment {
		return nil, environmentSchemaBlocker("billing", "initial schema owner requires the exact selected Billing image and source revision")
	}
	job := environmentBillingSchemaJob(environment, stack, image, pullName)
	id := deploymentplan.ID(job.Object)
	for i := range definitions {
		d := &definitions[i]
		if deploymentplan.ID(d.Object) == id {
			return nil, environmentSchemaBlocker("billing", "initial schema owner is duplicated")
		}
		if deploymentplan.ID(d.Object) == "StatefulSet/"+stack+"-platform/postgresql" {
			d.Phase = "configuration" // Its genuine readiness precedes the owner Job.
		}
		if !environmentSchemaApplication(d.Object) {
			continue
		}
		for _, item := range certIssuerObjectList(environmentZammadPod(d.Object)["containers"]) {
			container := deploymentplan.Map(item)
			if container["image"] != image.Reference {
				continue
			}
			d.Dependencies = append(d.Dependencies, id)
			// The declared owner is the only schema writer; API/simulator runtime
			// cannot race it or repeat schema changes after the Job has completed.
			if command := certIssuerObjectList(container["command"]); len(command) == 0 || deploymentplan.String(command[0]) == "/rtk-billing" || deploymentplan.String(command[0]) == "/rtk-billing-payment-simulator" {
				setEnvironmentContainerEnv(container, "BILLING_DB_MIGRATE_ON_STARTUP", "false")
			}
		}
	}
	return append(definitions, job), nil
}

func setEnvironmentContainerEnv(container deploymentplan.Object, name, value string) {
	var env []any
	for _, item := range certIssuerObjectList(container["env"]) {
		if deploymentplan.Map(item)["name"] != name {
			env = append(env, item)
		}
	}
	container["env"] = append(env, deploymentplan.Object{"name": name, "value": value})
}

type environmentUnappliedSchemaError struct{ error }

func (e *environmentUnappliedSchemaError) Unwrap() error { return e.error }

type environmentSchemaState struct {
	Database string `json:"database"`
	Schema   string `json:"schema"`
	History  bool   `json:"history"`
	Objects  int    `json:"objects"`
}

func readEnvironmentSchemaState(store secretStore, source string) (environmentSchemaState, error) {
	var state environmentSchemaState
	query := "BEGIN TRANSACTION READ ONLY; SET LOCAL statement_timeout='10s'; SELECT json_build_object('database',current_database(),'schema',current_schema(),'history',to_regclass('public.schema_migrations') IS NOT NULL,'objects',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema')+(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema')+(SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema')+(SELECT count(*) FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname NOT IN ('public','information_schema'))+(SELECT count(*) FROM pg_extension WHERE extname<>'plpgsql')); COMMIT;"
	raw, err := environmentSchemaSQL(store, source, query)
	if err != nil {
		return state, environmentSchemaBlocker(source, secretCheckFailure(err, "original database schema catalog cannot be read").Error())
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["database"] == nil || fields["schema"] == nil || fields["history"] == nil || fields["objects"] == nil || strings.TrimSpace(string(fields["history"])) == "null" || strings.TrimSpace(string(fields["objects"])) == "null" || json.Unmarshal(raw, &state) != nil || state.Database != "rtk_"+strings.ReplaceAll(source, "-", "_") || state.Schema != "public" || state.Objects < 0 || state.History && state.Objects == 0 {
		return state, environmentSchemaBlocker(source, "original database schema catalog response is invalid")
	}
	return state, nil
}

func environmentSchemaSQL(store secretStore, source, query string) ([]byte, error) {
	return secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "-n", "video-cloud-"+store.Environment+"-platform", "exec", "postgresql-0", "--", "psql", "-U", "postgres", "-d", "rtk_"+strings.ReplaceAll(source, "-", "_"), "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", query)
}

func verifyEnvironmentSchemaVersions(store secretStore, source string, versions []string) error {
	var literals []string
	for _, version := range versions {
		literals = append(literals, "('"+strings.ReplaceAll(version, "'", "''")+"')")
	}
	query := "BEGIN TRANSACTION READ ONLY; SET LOCAL statement_timeout='10s'; SELECT COALESCE(json_agg(required.version),'[]'::json) FROM (VALUES " + strings.Join(literals, ",") + ") AS required(version) WHERE NOT EXISTS (SELECT 1 FROM public.schema_migrations applied WHERE applied.version=required.version); COMMIT;"
	raw, err := environmentSchemaSQL(store, source, query)
	if err != nil {
		return environmentSchemaBlocker(source, secretCheckFailure(err, "original migration history cannot be read").Error())
	}
	var missing []string
	if json.Unmarshal(raw, &missing) != nil || missing == nil {
		return environmentSchemaBlocker(source, "original migration history response is invalid")
	}
	if len(missing) != 0 {
		return &environmentUnappliedSchemaError{environmentSchemaBlocker(source, fmt.Sprintf("selected image requires %d unapplied owner migration(s)", len(missing)))}
	}
	return nil
}

func verifyEnvironmentReleaseSchemasForPlan(ctx context.Context, cfg deploymentConfig, store secretStore, release environmentRelease, plan *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	images := make([]string, 0, len(release.Images))
	for _, image := range release.Images {
		if strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") && !strings.Contains(image.Reference, "/rtk_cloud_workspace/postgres-backup@") {
			images = append(images, image.Reference)
		}
	}
	sort.Strings(images)
	checked := map[string]bool{}
	for _, reference := range images {
		source, err := deploymentUpgradeImageSource(reference)
		if err != nil {
			return environmentSchemaBlocker("selected-release", "candidate package has no maintained source/publication mapping")
		}
		if checked[source.Key] || source.Key != "account-manager" && source.Key != "billing" {
			continue
		}
		if len(checked) == 0 && plan != nil {
			if err := verifyEnvironmentSchemaDatabaseTarget(ctx, plan, read); err != nil {
				return err
			}
		}
		checked[source.Key] = true
		image := release.Images[source.EnvKey]
		versions, err := environmentReleaseSchemaVersions(ctx, cfg, source, image)
		if err != nil {
			return err
		}
		state, err := readEnvironmentSchemaState(store, source.Key)
		if err != nil {
			return err
		}
		if !state.History {
			if source.Key != "billing" || state.Objects != 0 {
				return environmentSchemaBlocker(source.Key, "original database has no migration history; an existing or nonempty schema cannot be initialized implicitly")
			}
			if err := verifyEnvironmentInitialBillingOwner(ctx, cfg, store, release, plan, read, false); err != nil {
				return err
			}
			continue // A qualified empty input, never evidence of applied versions.
		}
		if source.Key == "billing" && plan != nil {
			// Once schema work started, only the literal original child may
			// finish it. A newly rendered Job cannot initialize existing data.
			want := environmentBillingSchemaJob(plan.Environment, plan.Stack, image, cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"])
			for _, resource := range plan.Resources {
				if resource.ID != deploymentplan.ID(want.Object) {
					continue
				}
				journal, exists, readErr := plan.ReadExecution(store.Root)
				if readErr != nil || !exists || read == nil || journal.Owners[resource.ID] == "" || deploymentplan.Digest(deploymentplan.Projection(resource.Desired)) != deploymentplan.Digest(deploymentplan.Projection(want.Object)) {
					return environmentSchemaBlocker("billing", "nonempty schema cannot authorize a new or unrecorded initial migration Job")
				}
				current, readErr := read(ctx, resource.ID)
				if readErr != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, current) {
					return environmentSchemaBlocker("billing", "original schema Job write owner or body changed during recovery")
				}
			}
		}
		if err := verifyEnvironmentSchemaVersions(store, source.Key, versions); err != nil {
			var unapplied *environmentUnappliedSchemaError
			if !errors.As(err, &unapplied) || source.Key != "billing" || verifyEnvironmentInitialBillingOwner(ctx, cfg, store, release, plan, read, true) != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func verifyEnvironmentSchemaDatabaseTarget(ctx context.Context, plan *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error)) error {
	fail := func() error {
		return environmentSchemaBlocker("selected-release", "original PostgreSQL controller and query Pod ownership cannot be verified")
	}
	if plan == nil || read == nil {
		return fail()
	}
	id := "StatefulSet/" + plan.Stack + "-platform/postgresql"
	uid := ""
	for _, resource := range plan.Resources {
		if resource.ID == id && resource.Action != "retire" {
			uid = resource.UID
		}
	}
	if uid == "" {
		return fail()
	}
	controller, err := read(ctx, id)
	if err != nil || controller == nil || deploymentplan.Map(controller["metadata"])["uid"] != uid || deploymentplan.Map(controller["metadata"])["deletionTimestamp"] != nil {
		return fail()
	}
	pod, err := read(ctx, "Pod/"+plan.Stack+"-platform/postgresql-0")
	if err != nil || pod == nil || !identityPermissionOwnedBy(deploymentplan.Map(pod["metadata"]), "StatefulSet", uid) || deploymentplan.Map(pod["metadata"])["uid"] == "" || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil {
		return fail()
	}
	return ctx.Err()
}

func verifyEnvironmentInitialBillingOwner(ctx context.Context, cfg deploymentConfig, store secretStore, release environmentRelease, plan *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error), requireRecorded bool) error {
	fail := func(cause string) error { return environmentSchemaBlocker("billing", cause) }
	if plan == nil || plan.Operation != "create" || plan.Environment != store.Environment || plan.Stack != "video-cloud-"+store.Environment || read == nil {
		return fail("Billing database is empty and has no applied history; this plan does not declare its initial owner migration")
	}
	want := environmentBillingSchemaJob(plan.Environment, plan.Stack, release.Images["LKE_BILLING_IMAGE"], cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"])
	id := deploymentplan.ID(want.Object)
	var job *deploymentplan.Resource
	for i := range plan.Resources {
		r := &plan.Resources[i]
		if r.ID == id {
			job = r
		}
		if environmentSchemaApplication(r.Desired) && environmentSchemaUsesBilling(r.Desired) && r.UID != "" {
			return fail("an existing Billing application owner requires applied schema before any update")
		}
	}
	if job == nil || job.Phase != want.Phase || job.Action != "create" || job.UID != "" || deploymentplan.Digest(deploymentplan.Projection(job.Desired)) != deploymentplan.Digest(deploymentplan.Projection(want.Object)) {
		return fail("empty Billing database lacks its exact selected-image initial migration Job")
	}
	for _, dependency := range want.Dependencies {
		if !slices.Contains(job.Dependencies, dependency) {
			return fail("initial migration Job lacks an explicit database or network-policy prerequisite")
		}
	}
	for _, r := range plan.Resources {
		if !environmentSchemaApplication(r.Desired) {
			continue
		}
		for _, item := range certIssuerObjectList(environmentZammadPod(r.Desired)["containers"]) {
			if deploymentplan.Map(item)["image"] == release.Images["LKE_BILLING_IMAGE"].Reference && !slices.Contains(r.Dependencies, id) {
				return fail("Billing application lacks its explicit schema-owner dependency")
			}
		}
	}
	consumers := map[string]string{}
	for _, name := range []string{"billing", "billing-payment-worker", "billing-settlement-collector", "payment-simulator"} {
		consumers["Deployment/"+plan.Stack+"-billing/"+name] = "declared"
	}
	for _, r := range plan.Resources {
		if environmentSchemaApplication(r.Desired) && environmentSchemaUsesBilling(r.Desired) {
			consumers[r.ID] = "declared"
		}
	}
	for _, consumer := range sortedMapKeys(consumers) {
		current, err := read(ctx, consumer)
		if err != nil || current != nil {
			return fail("empty Billing initialization requires absence of every original Billing application owner")
		}
	}

	current, err := read(ctx, id)
	if err != nil {
		return fail("initial migration Job ownership cannot be read")
	}
	if current == nil && requireRecorded {
		return fail("nonempty schema cannot authorize a new migration Job")
	}
	if current != nil {
		journal, exists, err := plan.ReadExecution(store.Root)
		if err != nil || !exists || journal.Owners[id] == "" || deploymentplan.Map(current["metadata"])["uid"] != journal.Owners[id] || !deploymentplan.Matches(job.Desired, current) {
			return fail("an existing migration Job lacks its original child write receipt; it cannot be adopted or replayed")
		}
	}
	return ctx.Err()
}

// Native completion alone is insufficient: read back the exact committed
// inventory before the executor can advance to its dependent API or worker.
func (b environmentKubernetesBackend) verifySchemaOwnerJob(ctx context.Context, resource deploymentplan.Resource) error {
	revision := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(resource.Desired["metadata"])["annotations"])[environmentBillingSchemaRevision])
	if revision == "" {
		return nil
	}
	pod := environmentZammadPod(resource.Desired)
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 1 || !filepath.IsAbs(b.SchemaWorkspace) {
		return environmentSchemaBlocker("billing", "completed schema Job lacks its frozen source/readback boundary")
	}
	image := environmentReleaseImage{Reference: deploymentplan.String(deploymentplan.Map(containers[0])["image"]), SourceRevision: revision}
	pulls := certIssuerObjectList(pod["imagePullSecrets"])
	if len(pulls) != 1 {
		return environmentSchemaBlocker("billing", "completed schema Job lacks its frozen pull identity")
	}
	want := environmentBillingSchemaJob(b.Store.Environment, "video-cloud-"+b.Store.Environment, image, deploymentplan.String(deploymentplan.Map(pulls[0])["name"]))
	if resource.ID != deploymentplan.ID(want.Object) || deploymentplan.Digest(deploymentplan.Projection(resource.Desired)) != deploymentplan.Digest(deploymentplan.Projection(want.Object)) {
		return environmentSchemaBlocker("billing", "completed schema Job differs from the exact maintained owner recipe")
	}
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || resource.UID == "" || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
		return environmentSchemaBlocker("billing", "completed schema Job no longer matches its genuine original write UID and body")
	}
	complete := false
	for _, item := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
		condition := deploymentplan.Map(item)
		if condition["type"] == "Complete" && condition["status"] == "True" {
			complete = true
		}
		if (condition["type"] == "Failed" || condition["type"] == "FailureTarget") && condition["status"] == "True" {
			return environmentSchemaBlocker("billing", "original migration Job reached native terminal failure")
		}
	}
	if !complete {
		return environmentSchemaBlocker("billing", "original migration Job has no native completion evidence")
	}
	source, err := deploymentUpgradeImageSource(image.Reference)
	if err != nil || source.Key != "billing" {
		return environmentSchemaBlocker("billing", "completed schema Job image has no Billing owner mapping")
	}
	versions, err := environmentReleaseSchemaVersions(ctx, deploymentConfig{Workspace: b.SchemaWorkspace}, source, image)
	if err != nil {
		return err
	}
	store := b.Store
	store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, store.Environment, b)
	return verifyEnvironmentSchemaVersions(store, "billing", versions)
}

func environmentSchemaApplication(object deploymentplan.Object) bool {
	switch object["kind"] {
	case "Deployment", "StatefulSet", "DaemonSet", "CronJob":
		return true
	}
	return false
}

func environmentSchemaUsesBilling(object deploymentplan.Object) bool {
	for _, item := range certIssuerObjectList(environmentZammadPod(object)["containers"]) {
		source, err := deploymentUpgradeImageSource(deploymentplan.String(deploymentplan.Map(item)["image"]))
		if err == nil && source.Key == "billing" {
			return true
		}
	}
	return false
}

func (b environmentCreateKubernetesBackend) beforeSchemaOwnerWrite(ctx context.Context, body deploymentplan.Object) error {
	if deploymentplan.Map(deploymentplan.Map(body["metadata"])["annotations"])[environmentBillingSchemaRevision] == nil {
		return nil
	}
	var plan deploymentplan.Plan
	path := filepath.Join(b.store.Root, "deployment", "rollback", b.stagePlanSHA256, "plan.json")
	if readEnvironmentPreflightJSON(path, &plan) != nil || plan.SHA256() != b.stagePlanSHA256 {
		return environmentSchemaBlocker("billing", "initial schema mutation lacks its original immutable child plan")
	}
	if err := b.CheckParentExecution(ctx, &plan, b.store.Root); err != nil {
		return err
	}
	containers := certIssuerObjectList(environmentZammadPod(body)["containers"])
	if len(containers) != 1 {
		return environmentSchemaBlocker("billing", "initial schema owner executable is ambiguous")
	}
	image := environmentReleaseImage{Reference: deploymentplan.String(deploymentplan.Map(containers[0])["image"]), SourceRevision: deploymentplan.String(deploymentplan.Map(deploymentplan.Map(body["metadata"])["annotations"])[environmentBillingSchemaRevision])}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_BILLING_IMAGE": image}}
	if err := verifyEnvironmentSchemaDatabaseTarget(ctx, &plan, b.environmentKubernetesBackend.Read); err != nil {
		return err
	}
	store := b.store
	store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, store.Environment, b.environmentKubernetesBackend)
	state, err := readEnvironmentSchemaState(store, "billing")
	if err != nil {
		return err
	}
	if state.History || state.Objects != 0 {
		return environmentSchemaBlocker("billing", "initial migration write no longer has a genuinely empty original database")
	}
	if err := verifyEnvironmentInitialBillingOwner(ctx, b.cfg, store, release, &plan, b.environmentKubernetesBackend.Read, false); err != nil {
		return err
	}
	return b.execution.Check(ctx)
}

// Preserve checker-owned service/stage diagnostics, never arbitrary tool output.
func environmentSchemaQualificationError(err error) error {
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) && blocker.ID == "deployment.compatibility.schema" {
		return err
	}
	return environmentSchemaBlocker("selected-release", secretCheckFailure(err, "selected release schema prerequisites cannot be verified").Error())
}
