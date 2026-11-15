package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The formal release readers must consume synthetic publications bound to the
// very same frozen parent and operator inputs as the issuer fixture.
func TestUnifiedRepairPublicationsUseFrozenCoreParent(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	sourceWorkspace := mustWorkspaceRoot(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sourceWorkspace, "repos", "rtk_video_cloud"), filepath.Join(workspace, "repos", "rtk_video_cloud")); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal("isolated environment configuration", err)
	}
	refs := map[string]string{
		"LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64),
		"LKE_MQTT_PKI_IMAGE":    "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:" + strings.Repeat("2", 64),
		"LKE_OPENBAO_PKI_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("3", 64),
	}
	hooks := coreExecutionContractHooks{
		ConfigureOperator: func(operator map[string]string) { operator["GHCR_PULL_TOKEN"] = "isolated-fake-gh-only" },
		Configure: func(selected *deploymentConfig, plan *environmentCreatePlan, bootstrap *environmentCreateBootstrapPlan) {
			*selected = resolved
			for role, ref := range refs {
				image := plan.Release.Images[role]
				image.Reference = ref
				plan.Release.Images[role] = image
				bootstrap.Images[role] = ref
			}
			bootstrap.Image = refs["LKE_VIDEO_CLOUD_IMAGE"]
		},
		BeforeServiceSession: func(*acceptedCoreExecutionContract, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony, []byte) {
		},
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, false, false, hooks)
	reread, err := resolveDeploymentConfig(workspace, "dev", filepath.Join(workspace, "cloud_env", "dev"))
	if err != nil || deploymentplan.Digest(reread) != f.p.Plan.ConfigurationSHA256 {
		t.Fatal("CLI environment does not reproduce original selected configuration", err)
	}
	first, second := installUnifiedRepairPublications(t, f.cfg, f.store, f.e, f.p.Plan)
	if len(first) != 3 || len(second) != 1 {
		t.Fatal("both exact repair publications were not bound to the original parent")
	}
}

// Reuse the real isolated foundation/init producers before resolving repair
// publications. Later unified recovery must use this very same parent.
func TestUnifiedHistoricalFoundationAndPublicationsShareParent(t *testing.T) {
	hooks := historicalOriginalFoundationHooks(t)
	configure := hooks.Configure
	hooks.ConfigureOperator = func(operator map[string]string) { operator["GHCR_PULL_TOKEN"] = "isolated-fake-gh-only" }
	hooks.Configure = func(cfg *deploymentConfig, plan *environmentCreatePlan, bootstrap *environmentCreateBootstrapPlan) {
		configure(cfg, plan, bootstrap)
		mqtt := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:" + strings.Repeat("2", 64)
		image := plan.Release.Images["LKE_MQTT_PKI_IMAGE"]
		image.Reference = mqtt
		plan.Release.Images["LKE_MQTT_PKI_IMAGE"] = image
		bootstrap.Images["LKE_MQTT_PKI_IMAGE"] = mqtt
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
	if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, "kubernetes/foundation"); err != nil {
		t.Fatal("original initialized foundation unavailable", err)
	}
	// Move the accepted historical owners into the one isolated native API
	// used by the subsequent continuation. The original parent remains fixed.
	originalJournal := deploymentplan.Digest(f.e.Journal)
	native, _, _ := unifiedCoreNativeBackend(t, f)
	if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.e, native, "kubernetes/foundation"); err != nil || deploymentplan.Digest(f.e.Journal) != originalJournal {
		t.Fatal("same-parent native foundation unavailable", err)
	}
	first, second := installUnifiedRepairPublications(t, f.cfg, f.store, f.e, f.p.Plan)
	if len(first) != 3 || len(second) != 1 {
		t.Fatal("same-parent historical repair publications unavailable")
	}
}

// Build the original session and its first same-key successor using their
// native producer before introducing the later unified continuation.
func TestUnifiedOriginalSessionProducersAndPublicationsShareParent(t *testing.T) {
	t.Setenv(historicalWallClockExpirySkipEnv, "0")
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	hooks.OriginalServiceSessionLifecycle = true
	configure := hooks.Configure
	hooks.ConfigureOperator = func(operator map[string]string) { operator["GHCR_PULL_TOKEN"] = "isolated-fake-gh-only" }
	hooks.Configure = func(cfg *deploymentConfig, plan *environmentCreatePlan, bootstrap *environmentCreateBootstrapPlan) {
		configure(cfg, plan, bootstrap)
		mqtt := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:" + strings.Repeat("2", 64)
		image := plan.Release.Images["LKE_MQTT_PKI_IMAGE"]
		image.Reference = mqtt
		plan.Release.Images["LKE_MQTT_PKI_IMAGE"] = image
		bootstrap.Images["LKE_MQTT_PKI_IMAGE"] = mqtt
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, true, true, false, hooks, historicalCompleteDomainPolicy(t))
	before := deploymentplan.Digest(f.e.Journal)
	native, _, _ := unifiedCoreNativeBackend(t, f)
	if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.e, native, "kubernetes/foundation"); err != nil {
		t.Fatal("original initialized foundation unavailable", err)
	}
	state, _, err := readManagedSessionState(t.Context(), f.cfg, environmentCreateBootstrapRuntime{Execution: f.e, Backend: native}, f.session)
	if err != nil || state.Session != f.session || state.Expires.After(time.Now().UTC()) || deploymentplan.Digest(f.e.Journal) != before {
		t.Fatal("original same-key successor did not expire under its native producer", err)
	}
	first, second := installUnifiedRepairPublications(t, f.cfg, f.store, f.e, f.p.Plan)
	if len(first) != 3 || len(second) != 1 {
		t.Fatal("same-parent historical repair publications unavailable")
	}
}
