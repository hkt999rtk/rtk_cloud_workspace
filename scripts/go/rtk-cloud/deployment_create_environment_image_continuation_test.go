package main

import (
	"context"
	"encoding/json"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCreateEnvironmentOpenBaoVersionReceiptPreservesOriginalOperator(t *testing.T) {
	oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	newRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)
	other := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("c", 64)
	image := map[string]any{"reference": newRef, "source_revision": strings.Repeat("d", 40), "publication": map[string]any{"repository": "hkt999rtk/rtk_video_cloud", "run_id": float64(77), "run_attempt": float64(2), "manifest_sha256": strings.Repeat("e", 64)}}
	previous := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": map[string]any{"reference": oldRef}, "LKE_PKI_CONTROLLER_IMAGE": map[string]any{"reference": other}}}
	next := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": image, "LKE_PKI_CONTROLLER_IMAGE": map[string]any{"reference": other}}}
	prior, _ := json.Marshal(previous)
	current, _ := json.Marshal(next)
	receipt := map[string]any{"previous_selection": string(prior), "selection": next, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": image, "run_url": "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"}}}
	raw, _ := json.Marshal(receipt)
	before := map[string]string{"DEPLOYMENT_IMAGES": string(prior), "UNRELATED": "unchanged"}
	after := map[string]string{"DEPLOYMENT_IMAGES": string(current), "UNRELATED": "unchanged"}
	selected, err := validateCreateOpenBaoVersionReceipt("dev", after, deploymentplan.Digest(before), oldRef, raw)
	if err != nil || selected.Reference != newRef || selected.Publication.RunID != 77 || selected.Publication.Attempt != 2 {
		t.Fatal("valid version transition rejected", err)
	}
	t.Run("original-environment-selection-differs-from-release-base", func(t *testing.T) {
		base := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("f", 64)
		selected, err := validateCreateOpenBaoVersionReceipt("dev", after, deploymentplan.Digest(before), base, raw)
		if err != nil || selected.Reference != newRef {
			t.Fatal("authenticated environment selection was replaced by the historical release base", err)
		}
		var wrong map[string]any
		if err := json.Unmarshal(raw, &wrong); err != nil {
			t.Fatal(err)
		}
		wrong["differences"].([]any)[0].(map[string]any)["current"] = base
		changed, _ := json.Marshal(wrong)
		if _, err := validateCreateOpenBaoVersionReceipt("dev", after, deploymentplan.Digest(before), base, changed); err == nil {
			t.Fatal("receipt naming the release base instead of the original environment selection was accepted")
		}
	})
	for _, mode := range []string{"missing", "wrong-role", "wrong-old", "unrelated-setting", "other-role", "target-drift", "missing-publication", "wrong-environment"} {
		t.Run(mode, func(t *testing.T) {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			op := map[string]string{"DEPLOYMENT_IMAGES": string(current), "UNRELATED": "unchanged"}
			diff := body["differences"].([]any)[0].(map[string]any)
			selection := body["selection"].(map[string]any)
			switch mode {
			case "missing":
				body = nil
			case "wrong-role":
				diff["role"] = "LKE_MQTT_PKI_IMAGE"
			case "wrong-old":
				diff["current"] = "foreign"
			case "unrelated-setting":
				op["UNRELATED"] = "changed"
			case "other-role":
				selection["images"].(map[string]any)["LKE_PKI_CONTROLLER_IMAGE"] = map[string]any{"reference": "foreign"}
				v, _ := json.Marshal(selection)
				op["DEPLOYMENT_IMAGES"] = string(v)
			case "target-drift":
				op["DEPLOYMENT_IMAGES"] = string(prior)
			case "missing-publication":
				delete(diff["latest"].(map[string]any), "publication")
			case "wrong-environment":
				selection["environment"] = "staging"
				v, _ := json.Marshal(selection)
				op["DEPLOYMENT_IMAGES"] = string(v)
			}
			b, _ := json.Marshal(body)
			if _, err := validateCreateOpenBaoVersionReceipt("dev", op, deploymentplan.Digest(before), oldRef, b); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestCreateEnvironmentOpenBaoVersionReceiptReconstructsAbsentSelection(t *testing.T) {
	oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	newRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)
	image := map[string]any{"reference": newRef, "source_revision": strings.Repeat("d", 40), "publication": map[string]any{"repository": "hkt999rtk/rtk_video_cloud", "run_id": float64(77), "run_attempt": float64(2), "manifest_sha256": strings.Repeat("e", 64)}}
	selected := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": image}}
	currentRaw, _ := json.Marshal(selected)
	receipt := map[string]any{"previous_selection": "", "selection": selected, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": image, "run_url": "https://github.com/example"}}}
	raw, _ := json.Marshal(receipt)
	current := map[string]string{"DEPLOYMENT_IMAGES": string(currentRaw), "UNRELATED": "kept"}
	for _, before := range []map[string]string{{"UNRELATED": "kept"}, {"DEPLOYMENT_IMAGES": "", "UNRELATED": "kept"}} {
		if _, err := validateCreateOpenBaoVersionReceipt("dev", current, deploymentplan.Digest(before), oldRef, raw); err != nil {
			t.Fatal("failed to authenticate original presence via operator hash", err)
		}
	}
}

func TestCreateEnvironmentOpenBaoVersionVerifiesExactPublication(t *testing.T) {
	f := newCreateOpenBaoACKImageFixture(t)
	target := createVersionImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("d", 64), SourceRevision: f.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}}
	image, err := verifyCreateOpenBaoVersionPublication(t.Context(), target, f.command(t))
	if err != nil || image.Reference != target.Reference {
		t.Fatal("formal publication rejected", err)
	}
	for _, mode := range []string{"wrong-digest", "wrong-manifest", "wrong-attempt", "wrong-revision", "wrong-workflow"} {
		t.Run(mode, func(t *testing.T) {
			candidate := target
			publication := *target.Publication
			candidate.Publication = &publication
			switch mode {
			case "wrong-digest":
				candidate.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("f", 64)
			case "wrong-manifest":
				candidate.Publication.ManifestSHA = strings.Repeat("f", 64)
			case "wrong-attempt":
				candidate.Publication.Attempt = 1
			case "wrong-revision":
				candidate.SourceRevision = strings.Repeat("f", 40)
			case "wrong-workflow":
				f.runInfo["path"] = ".github/workflows/ci.yml"
				defer func() { f.runInfo["path"] = ".github/workflows/release.yml" }()
			}
			if _, err := verifyCreateOpenBaoVersionPublication(t.Context(), candidate, f.command(t)); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestResolveCreateOpenBaoEnvironmentUpdateRequiresUniqueExactReceipt(t *testing.T) {
	f := newCreateOpenBaoACKImageFixture(t)
	oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	target := createVersionImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("d", 64), SourceRevision: f.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}}
	old := map[string]string{"UNRELATED": "kept"}
	selected := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": target}}
	selectedRaw, _ := json.Marshal(selected)
	current := map[string]string{"UNRELATED": "kept", "DEPLOYMENT_IMAGES": string(selectedRaw)}
	body := map[string]any{"previous_selection": "", "selection": selected, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": target, "run_url": "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"}}}
	raw, _ := json.Marshal(body)
	image, _, _, err := resolveCreateOpenBaoEnvironmentUpdate(t.Context(), f.cfg, f.store, current, deploymentplan.Digest(old), oldRef, f.command(t))
	if err == nil || image.Reference != "" {
		t.Fatal("accepted missing receipt")
	}
	if err = f.store.write("deployment/version-updates/fixture.json", raw, false); err != nil {
		t.Fatal(err)
	}
	image, selectionSHA, manifestSHA, err := resolveCreateOpenBaoEnvironmentUpdate(t.Context(), f.cfg, f.store, current, deploymentplan.Digest(old), oldRef, f.command(t))
	if err != nil || image.Reference != target.Reference || selectionSHA != environmentPreflightBytesSHA256(selectedRaw) || manifestSHA != target.Publication.ManifestSHA {
		t.Fatal("exact current setting not accepted", err)
	}
	if err = f.store.write("deployment/version-updates/duplicate.json", raw, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = resolveCreateOpenBaoEnvironmentUpdate(t.Context(), f.cfg, f.store, current, deploymentplan.Digest(old), oldRef, f.command(t)); err == nil {
		t.Fatal("ambiguous receipts accepted")
	}
}

func TestVerifyCreateEnvironmentOperatorCachesExactSelectedRole(t *testing.T) {
	cfg, store, oldExecution, _, _ := providerExecutionContractFixture(t)
	var err error
	f := newCreateOpenBaoACKImageFixture(t)
	// The publication fixture has its own isolated root; restore the original
	// execution root before verifying its canonical fence.
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	target := createVersionImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("d", 64), SourceRevision: f.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}}
	previous := map[string]string{"UNRELATED": "keep"}
	chosen := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": target}}
	raw, _ := json.Marshal(chosen)
	current := map[string]string{"UNRELATED": "keep", "DEPLOYMENT_IMAGES": string(raw)}
	receipt := map[string]any{"previous_selection": "", "selection": chosen, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": target, "run_url": "https://github.com/example"}}}
	body, _ := json.Marshal(receipt)
	if err = store.write("deployment/version-updates/test.json", body, false); err != nil {
		t.Fatal(err)
	}
	plan := &environmentCreatePlan{Environment: "dev", Stack: oldExecution.Journal.Stack, ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: deploymentplan.Digest(previous), Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: oldRef}}}}
	e, _ := providerExecutionContractFork(t, oldExecution, *plan, "isolated-version-update")
	e.originalPlanSHA = deploymentplan.Digest(*plan)
	e.versionState = &createOperatorVersionState{}
	for _, boundary := range []string{"run", "inventory", "archive"} {
		t.Run("remote-failure-"+boundary, func(t *testing.T) {
			originalCommand := f.command(t)
			failed := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				endpoint := args[len(args)-1]
				if boundary == "run" && strings.HasSuffix(endpoint, "/runs/77") || boundary == "inventory" && strings.Contains(endpoint, "/artifacts?per_page=") || boundary == "archive" && strings.HasSuffix(endpoint, "/zip") {
					return nil, context.DeadlineExceeded
				}
				return originalCommand(ctx, name, args...)
			}
			_, _, readErr := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, plan, failed)
			if !errors.Is(readErr, context.DeadlineExceeded) || strings.Contains(readErr.Error(), "settings changed") || e.versionState.cache != nil {
				t.Fatal("remote publication failure became setting drift or successful evidence", readErr)
			}
		})
	}
	before, image, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, plan, f.command(t))
	if err != nil || deploymentplan.Digest(before) != plan.OperatorSHA256 || image.Reference != target.Reference {
		t.Fatal("selected environment continuation rejected", err)
	}
	calls := f.calls
	if _, _, err = verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, plan, f.command(t)); err != nil || f.calls != calls {
		t.Fatal("same fenced setting repeated CI checks", err)
	}
	changed := map[string]string{"UNRELATED": "changed", "DEPLOYMENT_IMAGES": string(raw)}
	if _, _, err = verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, changed, plan, f.command(t)); err == nil {
		t.Fatal("cached proof allowed unrelated setting drift")
	}
	altered := *plan
	altered.OperatorSHA256 = deploymentplan.Digest(changed)
	if _, _, err = verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &altered, f.command(t)); err == nil {
		t.Fatal("unbound original plan accepted")
	}
}
