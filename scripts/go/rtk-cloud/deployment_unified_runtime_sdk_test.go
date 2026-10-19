package main

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func runtimeSDKFixture(t *testing.T, environment string) (deploymentConfig, secretStore, environmentRelease, environmentDeploymentSnapshot) {
	t.Helper()
	cfg, store, release, snapshot := runtimeDependencyFixture(t, environment)
	snapshot.Operator = appendMap(snapshot.Operator, map[string]string{
		"SDK_DOWNLOADS_ENABLED": "true", "SDK_ARTIFACT_BUCKET": "selected-sdk", "SDK_ARTIFACT_ENDPOINT": "https://objects.example.test",
		"SDK_ARTIFACT_ACCESS_KEY_ID": "selected-read-key", "SDK_ARTIFACT_SECRET_ACCESS_KEY": "private-selected-sdk-canary",
	})
	return cfg, store, release, snapshot
}

func runtimeSDKConsumer(t *testing.T, values map[string]string) deploymentplan.Object {
	t.Helper()
	for _, workload := range lkeWorkloads(values) {
		if workload.Key == "frontend" {
			definitions, err := decodeEnvironmentCreateManifests(lkeDeploymentManifest(values, workload, nil))
			if err != nil || len(definitions) != 1 {
				t.Fatal("maintained frontend cannot render", err)
			}
			return definitions[0].Object
		}
	}
	t.Fatal("maintained frontend is absent")
	return nil
}

func TestRuntimeDependencySDKUsesOneFrozenIntentForProducerAndConsumer(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeSDKFixture(t, environment)
			before := snapshot.Digest(cfg)
			for _, key := range environmentFrontendSDKRenderKeys() {
				t.Setenv(key, "ambient-private-canary")
			}
			t.Setenv("SDK_DOWNLOADS_ENABLED", "false")
			// Even a secret-shaped config override is not an alternative source
			// for the canonical operator credential pair.
			cfg.Values["SDK_ARTIFACT_SECRET_ACCESS_KEY"] = "unselected-config-canary"
			before = snapshot.Digest(cfg)
			err := withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
				if values["SDK_DOWNLOADS_ENABLED"] != "true" || os.Getenv("SDK_ARTIFACT_SECRET_ACCESS_KEY") != snapshot.Operator["SDK_ARTIFACT_SECRET_ACCESS_KEY"] {
					t.Fatal("SDK selected intent or credentials were replaced by ambient/config values")
				}
				produced, err := renderEnvironmentRuntimeDependencyDefinitions(values, snapshot.Secrets, snapshot.Operator)
				if err != nil {
					return err
				}
				id := "Secret/" + release.Stack + "-frontend/frontend-sdk-downloads"
				var sdk deploymentplan.Definition
				count := 0
				for _, d := range produced {
					if deploymentplan.ID(d.Object) == id {
						sdk, count = d, count+1
					}
				}
				if count != 1 || sdk.Object["type"] != "Opaque" {
					t.Fatal("enabled SDK lacks one canonical producer", count)
				}
				data := deploymentplan.Map(sdk.Object["stringData"])
				if len(data) != 8 || data["SDK_ARTIFACT_BUCKET"] != "selected-sdk" || data["SDK_ARTIFACT_SECRET_ACCESS_KEY"] != snapshot.Operator["SDK_ARTIFACT_SECRET_ACCESS_KEY"] || data["SDK_ARTIFACT_REGION"] != "us-sea" || data["SDK_LATEST_OBJECT_KEY"] != "sdk/latest.json" || data["PRO2_EXAMPLES_PREFIX"] != "pro2-examples/"+environment+"/" {
					t.Fatal("SDK mirror differs from the selected exact eight-key contract")
				}
				consumer := runtimeSDKConsumer(t, values)
				found := 0
				for _, ref := range environmentResourceReferences(consumer) {
					if ref.ID == id {
						found++
					}
				}
				if found != 1 {
					t.Fatal("maintained frontend and SDK producer selected different intent", found)
				}
				uid := "11111111-1111-4111-8111-111111111111"
				actual := deploymentplan.Clone(sdk.Object)
				actual["data"] = deploymentplan.Object{}
				for key, value := range data {
					deploymentplan.Map(actual["data"])[key] = base64.StdEncoding.EncodeToString([]byte(value.(string)))
				}
				delete(actual, "stringData")
				deploymentplan.Map(actual["metadata"])["uid"], deploymentplan.Map(actual["metadata"])["resourceVersion"] = uid, "1"
				bound, err := bindEnvironmentRuntimeDependencyOwners([]deploymentplan.Definition{sdk}, []deploymentplan.Definition{sdk}, []deploymentplan.Object{actual}, map[string]string{id: uid})
				if err != nil || !bound[0].Retain {
					t.Fatal("genuine SDK owner was not retained", err)
				}
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: release.Stack, Operation: "scoped", ConfigurationSHA256: before, ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bound)}, bound, []deploymentplan.Object{actual}, map[string]string{id: uid})
				if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "retain" || plan.Resources[0].UID != uid {
					t.Fatal("SDK mirror planned a replacement", err)
				}
				body, err := plan.Resources[0].Body(actual)
				if err != nil || body != nil {
					t.Fatal("retained SDK owner generated a write", err)
				}
				return nil
			})
			if err != nil || snapshot.Digest(cfg) != before || os.Getenv("SDK_DOWNLOADS_ENABLED") != "false" || os.Getenv("SDK_ARTIFACT_SECRET_ACCESS_KEY") != "ambient-private-canary" {
				t.Fatal("bounded SDK rendering changed inputs or process state", err)
			}
		})
	}
}

func TestRuntimeDependencySDKDisabledAndAbsentIntentCannotUseAmbientEnablement(t *testing.T) {
	for _, mode := range []string{"absent", "operator-disabled", "config-disabled", "config-enabled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeSDKFixture(t, "dev")
			switch mode {
			case "absent":
				for _, key := range environmentFrontendSDKRenderKeys() {
					delete(snapshot.Operator, key)
				}
			case "operator-disabled":
				snapshot.Operator["SDK_DOWNLOADS_ENABLED"] = "false"
			case "config-disabled":
				cfg.Values["SDK_DOWNLOADS_ENABLED"] = "false"
			case "config-enabled":
				cfg.Values["SDK_DOWNLOADS_ENABLED"], snapshot.Operator["SDK_DOWNLOADS_ENABLED"] = "true", "false"
			}
			t.Setenv("SDK_DOWNLOADS_ENABLED", "true")
			err := withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
				produced, err := renderEnvironmentRuntimeDependencyDefinitions(values, snapshot.Secrets, snapshot.Operator)
				if err != nil {
					return err
				}
				enabled := mode == "config-enabled"
				count := 0
				for _, d := range produced {
					if strings.HasSuffix(deploymentplan.ID(d.Object), "/frontend-sdk-downloads") {
						count++
					}
				}
				consumer := runtimeSDKConsumer(t, values)
				referenced := false
				for _, ref := range environmentResourceReferences(consumer) {
					referenced = referenced || strings.HasSuffix(ref.ID, "/frontend-sdk-downloads")
				}
				if (count == 1) != enabled || referenced != enabled {
					t.Fatal("SDK producer/consumer depends on ambient intent", mode, count, referenced)
				}
				return errors.New("fixture render stop")
			})
			if err == nil || os.Getenv("SDK_DOWNLOADS_ENABLED") != "true" {
				t.Fatal("error return did not restore the bounded environment", err)
			}
		})
	}
}

func TestRuntimeDependencySDKRejectsMissingInputsAndForeignOwners(t *testing.T) {
	for _, mode := range []string{"missing-all", "invalid-flag", "foreign-prefix", "unrecorded", "replaced", "missing-recorded", "changed-key", "extra-key", "changed-type"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeSDKFixture(t, "dev")
			switch mode {
			case "missing-all":
				for _, key := range []string{"SDK_ARTIFACT_BUCKET", "SDK_ARTIFACT_ENDPOINT", "SDK_ARTIFACT_ACCESS_KEY_ID", "SDK_ARTIFACT_SECRET_ACCESS_KEY", "GHCR_PULL_TOKEN"} {
					delete(snapshot.Operator, key)
					t.Setenv(key, "ambient-private-canary")
				}
			case "invalid-flag":
				snapshot.Operator["SDK_DOWNLOADS_ENABLED"] = "invalid-private-canary"
			case "foreign-prefix":
				snapshot.Operator["PRO2_EXAMPLES_PREFIX"] = "pro2-examples/staging/"
			}
			definitions, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
			if mode == "missing-all" {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || len(strings.Split(blocker.Resource, ",")) != 5 || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("missing SDK inputs were not aggregated/redacted", err)
				}
				return
			}
			if mode == "invalid-flag" || mode == "foreign-prefix" {
				if err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("invalid SDK intent passed or leaked", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var sdk deploymentplan.Definition
			for _, d := range definitions {
				if strings.HasSuffix(deploymentplan.ID(d.Object), "/frontend-sdk-downloads") {
					sdk = d
				}
			}
			actual := deploymentplan.Clone(sdk.Object)
			actual["data"] = deploymentplan.Object{}
			for key, value := range deploymentplan.Map(actual["stringData"]) {
				deploymentplan.Map(actual["data"])[key] = base64.StdEncoding.EncodeToString([]byte(value.(string)))
			}
			delete(actual, "stringData")
			id, uid := deploymentplan.ID(actual), "11111111-1111-4111-8111-111111111111"
			deploymentplan.Map(actual["metadata"])["uid"] = uid
			owners, live := map[string]string{id: uid}, []deploymentplan.Object{actual}
			switch mode {
			case "unrecorded":
				owners = nil
			case "replaced":
				deploymentplan.Map(actual["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
			case "missing-recorded":
				live = nil
			case "changed-key":
				deploymentplan.Map(actual["data"])["SDK_ARTIFACT_SECRET_ACCESS_KEY"] = base64.StdEncoding.EncodeToString([]byte("private-drift-canary"))
			case "extra-key":
				deploymentplan.Map(actual["data"])["UNQUALIFIED_ENV_OVERRIDE"] = base64.StdEncoding.EncodeToString([]byte("private-drift-canary"))
			case "changed-type":
				actual["type"] = "kubernetes.io/tls"
			}
			if _, err := bindEnvironmentRuntimeDependencyOwners([]deploymentplan.Definition{sdk}, []deploymentplan.Definition{sdk}, live, owners); err == nil || strings.Contains(err.Error(), "private-drift-canary") {
				t.Fatal("SDK ownership/credential replacement passed or leaked", err)
			}
		})
	}
}
