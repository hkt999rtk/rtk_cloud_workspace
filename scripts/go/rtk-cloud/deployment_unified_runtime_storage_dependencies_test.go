package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRuntimeDependencyMediaMirrorIsDeclaredInFreshCatalogBeforeNativeOutputs(t *testing.T) {
	cfg, store, release, operator, p := createDependencyContractFixture(t)
	values, chart, err := readEnvironmentZammadInputs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	support, err := renderEnvironmentZammad(context.Background(), cfg, store, release, "create", values, chart)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		t.Fatal(err)
	}
	renderValues := appendMap(cfg.Values, deploymentLegacyLKEValues(cfg.Values, cfg.Environment))
	err = withEnvironmentRenderValues(store, release, environmentDeploymentSnapshot{Secrets: secrets, Operator: operator}, renderValues, func(env map[string]string) error {
		intent, err := environmentCreateDependencyOutputs(cfg, p, env)
		if err != nil {
			return err
		}
		bindings := environmentIdentityBindings{Environment: cfg.Environment, Stack: p.Stack, Owners: map[string]string{}}
		catalog, err := renderEnvironmentResourceCatalog(env, bindings, release, intent, support)
		if err != nil {
			return err
		}
		produced, err := renderEnvironmentRuntimeDependencyDefinitions(env, secrets, operator)
		if err != nil {
			return err
		}
		mediaID := "Secret/" + p.Stack + "-video-cloud/" + environmentVideoStorageObjectStorageSecret
		consumers, producers := 0, 0
		for _, d := range catalog {
			for _, ref := range environmentResourceReferences(d.Object) {
				if ref.ID == mediaID {
					consumers++
					if videoStorageStartupContainer(d.Object) == nil {
						t.Fatal("dedicated media credentials leaked into an unrelated Core consumer")
					}
				}
			}
		}
		for _, d := range produced {
			if deploymentplan.ID(d.Object) == mediaID {
				producers++
				if len(deploymentplan.Map(d.Object["stringData"])) != 2 || deploymentplan.Map(d.Object["metadata"])["uid"] != nil {
					t.Fatal("future mirror invented an owner or shared runtime output")
				}
			}
		}
		if consumers != 1 || producers != 1 {
			t.Fatal("fresh complete catalog lacks the exact producer/consumer edge", consumers, producers)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qualifyEnvironmentCreateDependencyContract(context.Background(), cfg, release, store, operator, p); err != nil {
		t.Fatal("full maintained graph cannot qualify its real producer contracts", err)
	}
}

func TestRuntimeDependencyStorageMirrorsUseDedicatedKeysAndRetainSharedData(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeDependencyFixture(t, environment)
			cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"], cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
			for _, purpose := range []string{"MEDIA", "OTA"} {
				for _, field := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
					snapshot.Operator["LINODE_"+purpose+"_OBJ_"+field] = "isolated-" + purpose + "-" + field
				}
			}
			snapshot.Operator["LINODE_OBJ_ACCESS_KEY_ID"], snapshot.Operator["LINODE_OBJ_SECRET_ACCESS_KEY"] = "wrong-legacy-access", "wrong-legacy-secret"
			var sharedDefs []deploymentplan.Definition
			render := func() ([]deploymentplan.Definition, error) {
				var defs []deploymentplan.Definition
				err := withEnvironmentRenderValues(store, release, snapshot, cfg.Values, func(values map[string]string) error {
					var err error
					sharedDefs, err = decodeEnvironmentCreateManifests(lkeVideoCloudRuntimeSecretManifest(values))
					if err == nil {
						defs, err = renderEnvironmentRuntimeDependencyDefinitions(values, snapshot.Secrets, snapshot.Operator)
					}
					return err
				})
				return defs, err
			}
			defs, err := render()
			if err != nil || len(defs) != 14 {
				t.Fatal("dedicated runtime mirror set missing", len(defs), err)
			}
			inputs := deploymentplan.Inputs{Environment: environment, Stack: release.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(snapshot), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(defs)}
			plan, err := deploymentplan.Compile(inputs, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			sharedPlan, err := deploymentplan.Compile(inputs, sharedDefs, nil)
			if err != nil || len(sharedPlan.Resources) != 1 {
				t.Fatal("maintained shared producer", err)
			}
			shared, err := sharedPlan.Resources[0].Body(nil)
			if err != nil {
				t.Fatal(err)
			}
			sharedID := deploymentplan.ID(shared)
			mediaID := "Secret/" + release.Stack + "-video-cloud/" + environmentVideoStorageObjectStorageSecret
			deploymentplan.Map(shared["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
			data := deploymentplan.Map(shared["data"])
			data["AWS_ACCESS_KEY_ID"] = base64.StdEncoding.EncodeToString([]byte("original-core-access"))
			data["AWS_SECRET_ACCESS_KEY"] = base64.StdEncoding.EncodeToString([]byte("original-core-secret"))
			data["ORIGINAL_BILLING_TOKEN"] = base64.StdEncoding.EncodeToString([]byte("original-billing-token"))
			if len(data) != 17 {
				t.Fatal("fixture must retain the complete original shared dataset", len(data))
			}
			owners := map[string]string{sharedID: deploymentplan.String(deploymentplan.Map(shared["metadata"])["uid"])}
			live := []deploymentplan.Object{shared}
			for i, r := range plan.Resources {
				if r.ID == sharedID {
					t.Fatal("independent media producer claimed the original shared runtime")
				}
				object, err := r.Body(nil)
				if err != nil {
					t.Fatal(err)
				}
				uid := fmt.Sprintf("%08d-2222-4222-8222-222222222222", i+1)
				deploymentplan.Map(object["metadata"])["uid"] = uid
				owners[r.ID], live = uid, append(live, object)
				if r.ID == mediaID {
					fields := deploymentplan.Map(object["data"])
					if len(fields) != 2 || fields["AWS_ACCESS_KEY_ID"] != base64.StdEncoding.EncodeToString([]byte(snapshot.Operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"])) || fields["AWS_SECRET_ACCESS_KEY"] != base64.StdEncoding.EncodeToString([]byte(snapshot.Operator["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"])) {
						t.Fatal("wrong dedicated media role or shared fields")
					}
				}
			}
			before := deploymentplan.Digest(live)
			retained, err := bindEnvironmentRuntimeDependencyOwners(defs, defs, live, owners)
			if err != nil {
				t.Fatal(err)
			}
			for _, namespace := range lkeNamespaces(cfg.Values) {
				retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace.Name}}, Phase: "namespace"})
			}
			retained, err = bindRetainedEnvironmentDependencies(retained, live, environmentIdentityBindings{Stack: release.Stack, Owners: owners})
			if err != nil {
				t.Fatal(err)
			}
			complete, err := deploymentplan.Compile(inputs, retained, live, owners)
			if err != nil {
				t.Fatal("complete dependency target", err)
			}
			seenShared, seenMedia := false, false
			for _, r := range complete.Resources {
				if r.ID == sharedID {
					seenShared = true
					body, err := r.Body(shared)
					if r.Action != "retain" || r.UID != owners[sharedID] || body != nil || err != nil || deploymentplan.Digest(r.Baseline["data"]) != deploymentplan.Digest(data) {
						t.Fatal("compiled target would rewrite original shared17 data/UID", err)
					}
				}
				if r.ID == mediaID {
					seenMedia = true
					if r.Action != "retain" || r.UID != owners[mediaID] {
						t.Fatal("existing dedicated mirror owner changed")
					}
				}
			}
			if !seenShared || !seenMedia || deploymentplan.Digest(live) != before {
				t.Fatal("shared original owner/data lost")
			}
			for _, mode := range []string{"wrong-key", "wrong-uid", "unrecorded", "recorded-missing"} {
				t.Run(mode, func(t *testing.T) {
					changed := make([]deploymentplan.Object, 0, len(live))
					selected := map[string]string{}
					for id, uid := range owners {
						selected[id] = uid
					}
					for _, original := range live {
						object := deploymentplan.Clone(original)
						if deploymentplan.ID(object) == mediaID {
							switch mode {
							case "wrong-key":
								deploymentplan.Map(object["data"])["AWS_ACCESS_KEY_ID"] = base64.StdEncoding.EncodeToString([]byte("foreign-key-canary"))
							case "wrong-uid":
								deploymentplan.Map(object["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
							case "unrecorded":
								delete(selected, mediaID)
							case "recorded-missing":
								continue
							}
						}
						changed = append(changed, object)
					}
					if _, err := bindEnvironmentRuntimeDependencyOwners(defs, defs, changed, selected); err == nil || strings.Contains(err.Error(), "foreign-key-canary") {
						t.Fatal("dedicated mirror drift accepted or leaked", err)
					}
				})
			}
			for _, key := range []string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY"} {
				value := snapshot.Operator[key]
				delete(snapshot.Operator, key)
				_, err := render()
				if err == nil || strings.Contains(err.Error(), "isolated-") {
					t.Fatal("missing dedicated pair used fallback or leaked", err)
				}
				snapshot.Operator[key] = value
			}
		})
	}
}
