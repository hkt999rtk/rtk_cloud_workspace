package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Establish maintained synthetic inputs before the original admission. Native
// execution can re-resolve these files through the production configuration
// fence. This creates neither another environment store nor another execution.
func joinedMaintainedDeploymentConfig(t *testing.T, selected deploymentConfig, bootstrap environmentCreateBootstrapPlan) deploymentConfig {
	t.Helper()
	workspace := writeDeploymentFixture(t, selected.Environment, selected.Adapter)
	read := func(path string) map[string]string {
		t.Helper()
		values, err := readStrictEnv(path)
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	write := func(path string, values map[string]string) {
		t.Helper()
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var body strings.Builder
		for _, key := range keys {
			if strings.ContainsAny(values[key], "\r\n") {
				t.Fatalf("multiline synthetic configuration key %s", key)
			}
			body.WriteString(key + "=" + values[key] + "\n")
		}
		writeTestFile(t, path, body.String())
	}
	architecture := map[string]string{}
	for _, name := range []string{"architecture.env", "capacity.env", "topology.env", "workloads.env"} {
		for key, value := range read(filepath.Join(workspace, "cloud_deploy/architectures/kubernetes", name)) {
			architecture[key] = value
		}
	}
	// Keep the existing native fixture's bounded capacity and fill shared
	// service settings from the maintained architecture, then apply the exact
	// selected neutral inputs. Renderer compatibility aliases are not inputs.
	for _, name := range []string{"architecture.env", "capacity.env", "topology.env", "workloads.env"} {
		for key, value := range read(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy/architectures/kubernetes", name)) {
			if _, exists := architecture[key]; !exists {
				architecture[key] = value
			}
		}
	}
	for key, value := range selected.Values {
		if deploymentArchitectureKeys[key] {
			architecture[key] = value
		}
	}
	architecture["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	write(filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), architecture)
	for _, name := range []string{"capacity.env", "topology.env", "workloads.env"} {
		writeTestFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes", name), "")
	}
	environmentRoot := filepath.Join(workspace, "cloud_env", selected.Environment)
	identityPath := filepath.Join(environmentRoot, "environment.env")
	identity := read(identityPath)
	for key, value := range selected.Values {
		if !deploymentEnvironmentKeys[key] {
			continue
		}
		// The legacy render fixture supplies a derived console hostname for
		// every environment. Only production supports an explicit override.
		if key == "CONSOLE_DOMAIN" && selected.Environment != "prod" {
			continue
		}
		identity[key] = value
	}
	write(identityPath, identity)
	// The region is selected fixture intent, and becomes the adapter's normal
	// location mapping; no provider state or environment facts are fabricated.
	locationKey := "LOCATION_" + strings.ToUpper(strings.ReplaceAll(identity["DEPLOYMENT_LOCATION"], "-", "_"))
	locationsPath := filepath.Join(workspace, "cloud_deploy/adapters/lke/locations.env")
	locations := read(locationsPath)
	locations[locationKey] = selected.Storage.RuntimeMedia.Region
	write(locationsPath, locations)
	storage := map[string]string{
		"RUNTIME_MEDIA_STORAGE_POLICY":           selected.Storage.RuntimeMedia.Policy,
		"RUNTIME_MEDIA_STORAGE_BUCKET":           selected.Storage.RuntimeMedia.Bucket,
		"RUNTIME_MEDIA_STORAGE_PREFIX":           selected.Storage.RuntimeMedia.Prefix,
		"RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED": "false",
		"RUNTIME_OTA_STORAGE_MODE":               selected.Storage.OTAMode,
		"RUNTIME_OTA_STORAGE_POLICY":             selected.Storage.OTAFirmware.Policy,
		"RUNTIME_OTA_STORAGE_BUCKET":             selected.Storage.OTAFirmware.Bucket,
		"RUNTIME_OTA_STORAGE_PREFIX":             selected.Storage.OTAFirmware.Prefix,
	}
	if selected.Storage.RuntimeMediaCutoverRequired {
		storage["RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED"] = "true"
	}
	if selected.Storage.OTAFirmware.Policy == "cross-region" {
		storage["RUNTIME_OTA_STORAGE_REGION"] = selected.Storage.OTAFirmware.Region
	}
	write(filepath.Join(environmentRoot, "storage.env"), storage)
	for relative, want := range bootstrap.Templates {
		if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("invalid synthetic template source")
		}
		raw, err := os.ReadFile(filepath.Join(selected.Workspace, "repos/rtk_video_cloud", relative))
		if err != nil || environmentPreflightBytesSHA256(raw) != want {
			t.Fatal("selected original bootstrap template changed", relative, err)
		}
		target := filepath.Join(workspace, "repos/rtk_video_cloud", relative)
		writeTestFile(t, target, string(raw))
	}
	resolved, err := resolveDeploymentConfig(workspace, selected.Environment, environmentRoot)
	if err != nil {
		t.Fatal("original maintained joined configuration", err)
	}
	again, err := resolveDeploymentConfig(resolved.Workspace, resolved.Environment, resolved.EnvironmentRoot)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(resolved) {
		t.Fatal("joined configuration cannot survive the ordinary native input fence", err)
	}
	before, beforeErr := environmentCreatePlannedServiceSubjects(selected, bootstrap)
	after, afterErr := environmentCreatePlannedServiceSubjects(resolved, bootstrap)
	coreBefore, coreBeforeErr := environmentCreateBootstrapSubjects(selected)
	coreAfter, coreAfterErr := environmentCreateBootstrapSubjects(resolved)
	if beforeErr != nil || afterErr != nil || coreBeforeErr != nil || coreAfterErr != nil || !slices.Equal(before, after) || !slices.Equal(coreBefore, coreAfter) {
		t.Fatal("maintained fixture changed the original service roster")
	}
	for _, pair := range [][2]deploymentStorageTarget{{selected.Storage.RuntimeMedia, resolved.Storage.RuntimeMedia}, {selected.Storage.OTAFirmware, resolved.Storage.OTAFirmware}} {
		if pair[0].Bucket != pair[1].Bucket || pair[0].Region != pair[1].Region || pair[0].Prefix != pair[1].Prefix || pair[0].Policy != pair[1].Policy {
			t.Fatal("maintained fixture changed the selected storage target")
		}
	}
	return resolved
}

func TestJoinedMaintainedConfigurationUsesOrdinaryNativeFence(t *testing.T) {
	scenario, _ := newCurrentCoreForwardTestScenario(t)
	selected := scenario.cfg
	selected.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	selected.Workspace = mustWorkspaceRoot(t)
	selected.Storage.OTAMode = "dedicated"
	selected.Storage.RuntimeMedia = deploymentStorageTarget{Policy: "colocated", Bucket: "rtk-cloud-dev-runtime-us-lax", Region: "us-lax", Prefix: "original/media"}
	selected.Storage.OTAFirmware = deploymentStorageTarget{Policy: "colocated", Bucket: "rtk-cloud-dev-ota-firmware-us-lax", Region: "us-lax", Prefix: "original/ota"}
	currentCoreMaintainedWrapperPolicy(t, &selected)
	scenario.bootstrap.Templates = map[string]string{}
	relative := "deploy/pki/render-staging-controller.py"
	raw, err := os.ReadFile(filepath.Join(selected.Workspace, "repos/rtk_video_cloud", relative))
	if err != nil {
		t.Fatal(err)
	}
	scenario.bootstrap.Templates[relative] = environmentPreflightBytesSHA256(raw)
	cfg := joinedMaintainedDeploymentConfig(t, selected, scenario.bootstrap)
	planned, err := environmentCreatePlannedServiceSubjects(cfg, scenario.bootstrap)
	core, coreErr := environmentCreateBootstrapSubjects(cfg)
	if err != nil || coreErr != nil || len(planned) != 14 || len(core) != 9 || cfg.Values["PKI_STATE_BACKEND"] != "openbao" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" {
		t.Fatal("joined original maintained selection lost its complete scope")
	}
}
