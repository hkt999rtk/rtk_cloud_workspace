package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCompleteCompilerUsesMaintainedCatalogAndFencesNativeSnapshot(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release, bindings := maintainedDeploymentSnapshotFixture(t, environment)
			settings, _, _ := completeEnvironmentRenderFixture(environment)
			settings["MQTT_FOUNDATION_REGISTRATION_ENABLED"], settings["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
			profile := "standard"
			if environment == "prod" {
				profile = "production"
			}
			architecture := "DEPLOYMENT_RUNTIME=kubernetes\nNODE_CLASS_LABEL_KEY=rtk.io/node-class\nDEFAULT_WORKLOAD_NODE_CLASS=general\nDEPLOYMENT_PROTECTION_PROFILE=" + profile + "\nDEPLOYMENT_POSTGRES_BACKUP_ENABLED=true\nREDIS_DATA_STORAGE=5Gi\nPROMETHEUS_DATA_STORAGE=20Gi\n"
			for key, value := range settings {
				if key == "MQTT_FOUNDATION_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT" || environmentAuthArchitectureKey(key) || environmentControllerPolicySetting(key) || key == "OPENBAO_STORAGE_BACKEND" || key == "OPENBAO_DATA_STORAGE" || key == "OPENBAO_AUDIT_STORAGE" || key == "OPENBAO_POD_MANAGEMENT_POLICY" || key == "OPENBAO_UPDATE_STRATEGY" {
					architecture += key + "=" + value + "\n"
				}
			}
			writeTestFile(t, filepath.Join(cfg.Workspace, "cloud_deploy", "architectures", "kubernetes", "architecture.env"), architecture)
			if environment == "prod" {
				path := filepath.Join(cfg.Workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env")
				body := readTestFile(t, path)
				writeTestFile(t, path, strings.ReplaceAll(body, "OPENBAO_MIN_REPLICAS=1", "OPENBAO_MIN_REPLICAS=3"))
			}
			cfg, err := resolveDeploymentConfig(cfg.Workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			attachEnvironmentAppFixture(&bindings)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			raw, _ := json.Marshal(bindings)
			if err := store.write("deployment/identity-bindings.json", raw, true); err != nil {
				t.Fatal(err)
			}
			protection, err := readPostgresBackupDeployment(filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), environment)
			if err != nil {
				t.Fatal(err)
			}
			protection.Worker.Source.PostgresImage = release.Images["LKE_POSTGRES_IMAGE"].Reference
			protection.RunnerImage = "ghcr.io/hkt999rtk/rtk_cloud_workspace/postgres-backup@sha256:" + strings.Repeat("d", 64)
			scheduled := false
			protection.ScheduleEnabled = &scheduled
			release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"] = environmentReleaseImage{Reference: protection.RunnerImage, SourceRevision: release.WorkspaceRevision}
			raw, _ = json.Marshal(protection)
			writeTestFile(t, filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), string(raw))
			if err := store.write("runtime/postgres-backup", []byte("fixture-backup-password"), true); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"RTK_POSTGRES_BACKUP_ACCESS_KEY_ID", "RTK_POSTGRES_BACKUP_SECRET_ACCESS_KEY"} {
				if err := store.write("operator/env/"+key, []byte("fixture-archive"), true); err != nil {
					t.Fatal(err)
				}
			}
			// Original Account Manager state is a retained dependency, not a template.
			secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "account-manager-runtime", "namespace": bindings.Stack + "-account-manager", "uid": "original-runtime", "resourceVersion": "1", "labels": map[string]any{"rtk.realtek.com/stack": bindings.Stack}}, "data": map[string]any{"DATABASE_URL": base64.StdEncoding.EncodeToString([]byte("postgres://runtime@postgresql." + bindings.Stack + "-platform.svc.cluster.local/rtk_account_manager"))}}
			for key := range deploymentplan.Map(secret["data"]) {
				if strings.HasPrefix(key, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_") {
					delete(deploymentplan.Map(secret["data"]), key)
				}
			}
			snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
			if err != nil {
				t.Fatal(err)
			}
			var definitions []deploymentplan.Definition
			err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
				var err error
				definitions, err = renderEnvironmentResources(values, bindings, release)
				if err == nil {
					protection, pe := environmentPostgresProtectionFromSnapshot(snapshot)
					if pe != nil {
						return pe
					}
					definitions, err = renderEnvironmentPostgresProtection(definitions, protection, release)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			declared := map[string]bool{}
			for _, d := range definitions {
				declared[deploymentplan.ID(d.Object)] = true
			}
			dependencies := map[string]deploymentplan.Object{deploymentplan.ID(secret): secret}
			for _, d := range definitions {
				for _, ref := range environmentResourceReferences(d.Object) {
					if !declared[ref.ID] && dependencies[ref.ID] == nil {
						dependencies[ref.ID] = deploymentplan.Object{"apiVersion": "v1", "kind": ref.Kind, "metadata": map[string]any{"namespace": ref.Namespace, "name": ref.Name, "uid": "fixture-" + ref.Name, "resourceVersion": "1", "labels": map[string]any{"rtk.realtek.com/stack": bindings.Stack}}}
					}
				}
			}
			// Give managed controllers actual native owners, so a successful full
			// compiler test also exercises lifecycle preservation during upgrade.
			for _, d := range definitions {
				name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
				appRoute := d.Object["kind"] == "Ingress" && name == bindings.AppIngress.Name
				appService := d.Object["kind"] == "Service" && name == "video-cloud-api-app-pki"
				if !appRoute && !appService && (environmentManagedComponents[name] == "" && name != "openbao-raft" && name != "ingress-nginx-controller" && d.Object["kind"] != "ValidatingWebhookConfiguration" || d.Object["kind"] != "Deployment" && d.Object["kind"] != "StatefulSet" && d.Object["kind"] != "ConfigMap" && d.Object["kind"] != "ValidatingWebhookConfiguration") {
					continue
				}
				object := deploymentplan.Clone(d.Object)
				if appService {
					deploymentplan.Map(object["spec"])["clusterIP"] = "10.96.0.8"
				}
				id := deploymentplan.ID(object)
				metadata := deploymentplan.Map(object["metadata"])
				metadata["uid"], metadata["resourceVersion"] = "original-managed-"+name, "1"
				bindings.Owners[id] = deploymentplan.String(metadata["uid"])
				dependencies[id] = object
			}
			raw, _ = json.Marshal(bindings)
			if err := store.write("deployment/identity-bindings.json", raw, true); err != nil {
				t.Fatal(err)
			}
			// External durable claims must have their original native volume binding.
			// An empty Secret-like placeholder would correctly fail storage qualification.
			volumes := map[string]deploymentplan.Object{}
			for _, object := range dependencies {
				if object["kind"] != "PersistentVolumeClaim" {
					continue
				}
				metadata := deploymentplan.Map(object["metadata"])
				volume := "fixture-" + deploymentplan.String(metadata["namespace"]) + "-" + deploymentplan.String(metadata["name"])
				object["spec"] = map[string]any{"volumeName": volume, "storageClassName": "fixture", "accessModes": []any{"ReadWriteOnce"}}
				object["status"] = map[string]any{"phase": "Bound"}
				pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": map[string]any{"name": volume, "namespace": "", "uid": volume, "resourceVersion": "1"}, "spec": map[string]any{"claimRef": map[string]any{"name": metadata["name"], "namespace": metadata["namespace"], "uid": metadata["uid"]}}, "status": map[string]any{"phase": "Bound"}}
				volumes[deploymentplan.ID(pv)] = pv
			}
			for id, object := range volumes {
				dependencies[id] = object
			}
			directory := t.TempDir()
			raw, _ = json.Marshal(dependencies)
			writeTestFile(t, filepath.Join(directory, "dependencies.json"), string(raw))
			namespaces := []any{}
			for _, d := range definitions {
				if d.Object["kind"] == "Namespace" {
					namespace := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": deploymentplan.Map(d.Object["metadata"])["name"], "uid": "namespace-" + deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "resourceVersion": "1"}}
					namespaces = append(namespaces, namespace)
					dependencies[deploymentplan.ID(namespace)] = namespace
				}
			}
			raw, _ = json.Marshal(dependencies)
			writeTestFile(t, filepath.Join(directory, "dependencies.json"), string(raw))
			raw, _ = json.Marshal(map[string]any{"items": namespaces})
			writeTestFile(t, filepath.Join(directory, "namespaces.json"), string(raw))

			writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
case "$*" in
 *'--dry-run=server'*) python3 -c 'import json,os,sys; body=json.load(sys.stdin); f=open(os.environ["RTK_COMPILE_ADMISSION"],"a"); f.write(json.dumps(body)+"\n"); f.close();
if os.environ.get("RTK_COMPILE_MUTATE_ADMISSION")=="1" and body["kind"]=="Deployment": body["spec"]["template"]["spec"]["containers"][0]["image"]="unapproved:latest"
print(json.dumps(body))';;
 *' exec '*) printf '{"system_identifier":"123456789","sealed":true}';;
 *'get namespace '*) cat "$RTK_COMPILE_NAMESPACES";;
 *'get deployment,'*) python3 -c 'import json,sys; args=sys.argv[1:]; ns=args[args.index("--namespace")+1]; values=json.load(open(sys.argv[-1])); print(json.dumps({"items":[o for o in values.values() if o["metadata"].get("namespace","")==ns]}))' "$@" "$RTK_COMPILE_DEPENDENCIES";;
 *'get clusterrole,'*) python3 -c 'import json,sys; values=json.load(open(sys.argv[-1])); print(json.dumps({"items":[o for o in values.values() if o["kind"]=="ValidatingWebhookConfiguration"]}))' "$RTK_COMPILE_DEPENDENCIES" ;;
 *' get '*) python3 -c 'import json,sys; args=sys.argv[1:]; i=args.index("get"); ns=args[args.index("--namespace")+1] if "--namespace" in args else ""; obj=json.load(open(sys.argv[-1])).get(args[i+1]+"/"+ns+"/"+args[i+2]); print(json.dumps(obj) if obj else "",end="")' "$@" "$RTK_COMPILE_DEPENDENCIES";;
 *) exit 1;;
esac
`)
			if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_COMPILE_NAMESPACES", filepath.Join(directory, "namespaces.json"))
			t.Setenv("RTK_COMPILE_DEPENDENCIES", filepath.Join(directory, "dependencies.json"))
			admissionPath := filepath.Join(directory, "admission.jsonl")
			t.Setenv("RTK_COMPILE_ADMISSION", admissionPath)
			t.Setenv("RTK_COMPILE_MUTATE_ADMISSION", "0")
			if err := os.Remove(filepath.Join(store.Root, "deployment", "identity-bindings.json")); err != nil {
				t.Fatal(err)
			}
			fixtureBackend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, environment)
			if err != nil {
				t.Fatal(err)
			}
			fixtureLive, err := fixtureBackend.Inventory(context.Background(), bindings.Stack)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observeEnvironmentBindings(environment, release, fixtureLive); err != nil {
				t.Fatal("fixture observations", err)
			}
			compiled, err := compileEnvironmentDeploymentWithImages(context.Background(), cfg, "upgrade", release, nil)
			if err != nil {
				t.Fatal("complete compiler failed", err)
			}
			planned := map[string]deploymentplan.Resource{}
			for _, resource := range compiled.Plan.Resources {
				planned[resource.ID] = resource
			}
			for id := range declared {
				if _, present := planned[id]; !present {
					t.Fatal("maintained component omitted from the complete plan", id)
				}
			}
			for _, resource := range compiled.Plan.Resources {
				if resource.Action == "create" && (resource.UID != "" || compiled.Bindings.Owners[resource.ID] != "") {
					t.Fatal("preflight invented an owner UID for a new resource", resource.ID)
				}
			}
			appServiceID := "Service/" + bindings.Stack + "-video-cloud/video-cloud-api-app-pki"
			serviceResource := planned[appServiceID]
			serviceBody, serviceErr := serviceResource.Body(dependencies[appServiceID])
			if serviceErr != nil || deploymentplan.Map(serviceBody["spec"])["clusterIP"] != "10.96.0.8" {
				t.Fatal("App Service lost its original cluster IP", serviceErr)
			}
			approved := compiled.Plan.SHA256()
			if err := compiled.Plan.Admit(context.Background(), compiled.Backend); err != nil {
				t.Fatal("native admission did not receive the compiled target", err)
			}
			admitted := map[string]bool{}
			for _, line := range strings.Split(strings.TrimSpace(readTestFile(t, admissionPath)), "\n") {
				var object deploymentplan.Object
				if err := json.Unmarshal([]byte(line), &object); err != nil {
					t.Fatal(err)
				}
				id := deploymentplan.ID(object)
				resource, exists := planned[id]
				body, err := resource.Body(dependencies[id])
				if !exists || admitted[id] || err != nil || body == nil || deploymentplan.Digest(body) != deploymentplan.Digest(object) {
					t.Fatal("native admission received a different or duplicate body", id, err)
				}
				admitted[id] = true
			}
			for id, resource := range planned {
				if admitted[id] != (resource.Action != "retain") {
					t.Fatal("native admission skipped a target or wrote a retained lifecycle", id)
				}
			}
			t.Setenv("RTK_COMPILE_MUTATE_ADMISSION", "1")
			var blocker *deploymentplan.Blocker
			if err := compiled.Plan.Admit(context.Background(), compiled.Backend); !errors.As(err, &blocker) || blocker.ID != "deployment.admission" {
				t.Fatal("native admission accepted an unapproved image", err)
			}
			t.Setenv("RTK_COMPILE_MUTATE_ADMISSION", "0")
			if err := store.write("deployment/managed-upgrade.json", []byte("retired invalid schema-1 plan"), true); err != nil {
				t.Fatal(err)
			}
			repeated, err := compileEnvironmentDeploymentWithImages(context.Background(), cfg, "upgrade", release, nil)
			if err != nil || repeated.Plan.SHA256() != approved {
				t.Fatal("retired private plan changed the common compiler", err)
			}
			if err := compiled.Fence(context.Background(), compiled.Plan); err != nil {
				t.Fatal(err)
			}
			if compiled.Plan.SHA256() != approved {
				t.Fatal("fence mutated plan")
			}
			// Foreign observed identity is rejected before admission or execution,
			// even when its owner UID is unchanged.
			originalID := "Deployment/" + bindings.Stack + "-video-cloud/certissuer"
			original := dependencies[originalID]
			pod := environmentPKIPod(original)
			consumer := deploymentplan.Map(pod["containers"].([]any)[0])
			consumer["env"] = append(certIssuerObjectList(consumer["env"]), deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": "private-pin-canary"})
			raw, _ = json.Marshal(dependencies)
			writeTestFile(t, filepath.Join(directory, "dependencies.json"), string(raw))
			admittedBefore := readTestFile(t, admissionPath)
			if _, err := compileEnvironmentDeploymentWithImages(context.Background(), cfg, "upgrade", release, nil); !errors.As(err, &blocker) || blocker.ID != "deployment.pki.observation" || strings.Contains(err.Error(), "private-pin-canary") {
				t.Fatal("complete compiler ignored original PKI bindings", err)
			}
			if readTestFile(t, admissionPath) != admittedBefore {
				t.Fatal("PKI transition blocker ran after resource admission")
			}
			if err := store.write("runtime/postgres", []byte("concurrent-change"), true); err != nil {
				t.Fatal(err)
			}
			if err := compiled.Fence(context.Background(), compiled.Plan); err == nil {
				t.Fatal("compiler/fence ignored credential drift")
			}
		})
	}
}
