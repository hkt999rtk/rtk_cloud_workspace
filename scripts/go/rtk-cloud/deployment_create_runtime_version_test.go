package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
)

func TestCreateRuntimeVersionReceiptPreservesOtherRolesAndLegacyInputs(t *testing.T) {
	old := map[string]string{"PKI_CONTROLLER_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), "PKI_MANAGEMENT_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("b", 64), "OTHER": "unchanged"}
	prior := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("c", 64)}}}
	raw, _ := json.Marshal(prior)
	old[environmentImagesKey] = string(raw)
	next := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{}}
	for k, v := range prior.Images {
		next.Images[k] = v
	}
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("d", 64), SourceRevision: strings.Repeat("e", 40), Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 1, ManifestSHA: strings.Repeat("f", 64)}}
	r := createVersionReceipt{Previous: string(raw)}
	for _, role := range []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"} {
		next.Images[role] = image
		latest, _ := json.Marshal(image)
		r.Differences = append(r.Differences, createVersionDifference{Role: role, Current: old[strings.TrimPrefix(role, "LKE_")], Latest: latest, RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"})
	}
	raw, _ = json.Marshal(next)
	current := appendMap(old, map[string]string{environmentImagesKey: string(raw)})
	r.Selection = createVersionSelection{Environment: "dev", Images: map[string]json.RawMessage{}}
	for k, v := range next.Images {
		r.Selection.Images[k] = mustProviderRecoveryJSON(v)
	}
	before := deploymentplan.Digest(current)
	restored, images, err := validateCreateRuntimeVersionReceipt("dev", current, mustProviderRecoveryJSON(r))
	if err != nil || deploymentplan.Digest(restored) != deploymentplan.Digest(old) || len(images) != 2 {
		t.Fatal("exact role selection failed", err)
	}
	if deploymentplan.Digest(current) != before {
		t.Fatal("verification changed environment selection")
	}
	for _, mode := range []string{"other-role", "wrong-old", "duplicate-role", "missing-publication", "wrong-environment"} {
		t.Run(mode, func(t *testing.T) {
			var changed createVersionReceipt
			_ = json.Unmarshal(mustProviderRecoveryJSON(r), &changed)
			op := appendMap(current, nil)
			switch mode {
			case "other-role":
				changed.Selection.Images["LKE_OPENBAO_PKI_IMAGE"] = mustProviderRecoveryJSON(image)
				op[environmentImagesKey] = string(mustProviderRecoveryJSON(changed.Selection))
			case "wrong-old":
				changed.Differences[0].Current = "foreign"
			case "duplicate-role":
				changed.Differences[1].Role = changed.Differences[0].Role
			case "missing-publication":
				broken := image
				broken.Publication = nil
				changed.Selection.Images[changed.Differences[0].Role] = mustProviderRecoveryJSON(broken)
				changed.Differences[0].Latest = mustProviderRecoveryJSON(broken)
				op[environmentImagesKey] = string(mustProviderRecoveryJSON(changed.Selection))
			case "wrong-environment":
				changed.Selection.Environment = "staging"
				op[environmentImagesKey] = string(mustProviderRecoveryJSON(changed.Selection))
			}
			if _, _, err := validateCreateRuntimeVersionReceipt("dev", op, mustProviderRecoveryJSON(changed)); err == nil {
				t.Fatal("unreviewed transition accepted")
			}
		})
	}
}

func TestCreateRuntimeVersionComposesWithOriginalOpenBaoPublication(t *testing.T) {
	cfg, store, priorExecution, _, _ := providerExecutionContractFixture(t)
	f := newCreateOpenBaoACKImageFixture(t)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	apiRepo := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:"
	baoRepo := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:"
	before := map[string]string{"UNRELATED": "preserved", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": "managed-public-pem-v1", "PKI_CONTROLLER_IMAGE": apiRepo + strings.Repeat("1", 64), "PKI_MANAGEMENT_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("2", 64)}
	bao := environmentReleaseImage{Reference: baoRepo + strings.Repeat("d", 64), SourceRevision: f.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}}
	first := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": bao}}
	priorRaw := string(mustProviderRecoveryJSON(first))
	firstReceipt := createVersionReceipt{Selection: createVersionSelection{Environment: "dev", Images: map[string]json.RawMessage{"LKE_OPENBAO_PKI_IMAGE": mustProviderRecoveryJSON(bao)}}, Differences: []createVersionDifference{{Role: "LKE_OPENBAO_PKI_IMAGE", Current: baoRepo + strings.Repeat("a", 64), Latest: mustProviderRecoveryJSON(bao), RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"}}}
	if err := store.write("deployment/version-updates/openbao.json", mustProviderRecoveryJSON(firstReceipt), false); err != nil {
		t.Fatal(err)
	}
	image := environmentReleaseImage{Reference: apiRepo + strings.Repeat("c", 64), SourceRevision: f.selection.Revision, Publication: bao.Publication}
	next := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": bao}}
	second := createVersionReceipt{Previous: priorRaw, Selection: createVersionSelection{Environment: "dev", Images: map[string]json.RawMessage{}}}
	for _, role := range []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"} {
		next.Images[role] = image
		second.Differences = append(second.Differences, createVersionDifference{Role: role, Current: before[strings.TrimPrefix(role, "LKE_")], Latest: mustProviderRecoveryJSON(image), RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"})
	}
	for role, image := range next.Images {
		second.Selection.Images[role] = mustProviderRecoveryJSON(image)
	}
	if err := store.write("deployment/version-updates/runtime.json", mustProviderRecoveryJSON(second), false); err != nil {
		t.Fatal(err)
	}
	// A previous attempt can leave the identical unapplied receipt before
	// writing its large plan. Byte-identical authority is one transition.
	if err := store.write("deployment/version-updates/runtime-copy.json", mustProviderRecoveryJSON(second), false); err != nil {
		t.Fatal(err)
	}

	current := appendMap(before, map[string]string{environmentImagesKey: string(mustProviderRecoveryJSON(next))})
	parent := environmentCreatePlan{Environment: store.Environment, Stack: priorExecution.Journal.Stack, ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: deploymentplan.Digest(before), Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: baoRepo + strings.Repeat("a", 64)}}}}
	e, _ := providerExecutionContractFork(t, priorExecution, parent, "isolated-runtime-image-update")
	e.originalPlanSHA = deploymentplan.Digest(parent)
	native := f.command(t)
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			if slices.Contains(args, "/app/pkimanagement") {
				features := []string{}
				if slices.Contains(args, apiRepo+strings.Repeat("f", 64)) {
					features = []string{"public-registration-crl-bundle-v1"}
				}
				return mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "pkimanagement", "features": features}), nil
			}
			return mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "serviceidentity-bootstrap", "features": serviceIdentityBootstrapCapabilities("openbao")}), nil
		}
		return native(ctx, name, args...)
	}
	repairCtx := context.WithValue(t.Context(), createRuntimeImageCapabilityRepairKey{}, map[string]environmentReleaseImage{"LKE_PKI_MANAGEMENT_IMAGE": {Reference: apiRepo + strings.Repeat("f", 64)}})
	restored, root, err := verifyCreateEnvironmentOperator(repairCtx, cfg, store, e, current, &parent, command)
	if err != nil || deploymentplan.Digest(restored) != parent.OperatorSHA256 || root.Reference != bao.Reference || len(e.versionState.cache.RuntimeImages) != 2 {
		t.Fatal("two exact formal role changes did not compose with original OpenBao", err)
	}
	if _, _, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, command); err == nil {
		t.Fatal("ordinary current-package validation reused a candidate-only public CRL capability admission")
	}
	calls := f.calls
	if _, _, err = verifyCreateEnvironmentOperator(repairCtx, cfg, store, e, current, &parent, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("verified unchanged operation repeated publication lookup")
		return nil, nil
	}); err != nil || f.calls != calls {
		t.Fatal("same-operation image evidence not reused", err)
	}

	// The next official updater changes only Management. Controller must keep
	// its earlier selected package while both receipts restore the same original
	// operator and OpenBao handoff.
	newer := newCreateOpenBaoACKImageFixture(t)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	newer.selection.Revision = strings.Repeat("e", 40)
	newer.manifest["source_commit"] = newer.selection.Revision
	newer.manifest["run_id"] = "78"
	newer.manifest["digest"] = "sha256:" + strings.Repeat("f", 64)
	newer.runInfo["id"] = 78
	newer.runInfo["head_sha"] = newer.selection.Revision
	newer.write(t)
	management := environmentReleaseImage{Reference: apiRepo + strings.Repeat("f", 64), SourceRevision: newer.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 78, Attempt: 2, ManifestSHA: newer.selection.ManifestSHA}}
	third := createVersionReceipt{Previous: current[environmentImagesKey], Selection: createVersionSelection{Environment: "dev", Images: map[string]json.RawMessage{}}, Differences: []createVersionDifference{{Role: "LKE_PKI_MANAGEMENT_IMAGE", Current: image.Reference, Latest: mustProviderRecoveryJSON(management), RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/78"}}}
	for role, selected := range next.Images {
		third.Selection.Images[role] = mustProviderRecoveryJSON(selected)
	}
	third.Selection.Images["LKE_PKI_MANAGEMENT_IMAGE"] = mustProviderRecoveryJSON(management)
	if err := store.write("deployment/version-updates/runtime-management.json", mustProviderRecoveryJSON(third), false); err != nil {
		t.Fatal(err)
	}
	current = appendMap(current, map[string]string{environmentImagesKey: string(mustProviderRecoveryJSON(third.Selection))})
	currentSHA := deploymentplan.Digest(current)
	storeBefore := createOpenBaoACKStoreSnapshot(t, store)
	archive := publicationZIP(t, "image-manifest.json", newer.publication)
	olderCommand := command
	command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) == 2 && args[0] == "api" {
			switch args[1] {
			case "repos/hkt999rtk/rtk_video_cloud/actions/runs/78":
				return json.Marshal(newer.runInfo)
			case "repos/hkt999rtk/rtk_video_cloud/actions/runs/78/artifacts?per_page=100":
				return json.Marshal(map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 100, "name": "image-manifest", "size_in_bytes": len(archive), "expired": false}}})
			case "repos/hkt999rtk/rtk_video_cloud/actions/artifacts/100/zip":
				return archive, nil
			}
		}
		return olderCommand(ctx, name, args...)
	}
	restored, root, err = verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, command)
	if err != nil {
		t.Fatal("second Management-only runtime update did not compose with original operator and OpenBao", err)
	}
	if deploymentplan.Digest(restored) != parent.OperatorSHA256 || root.Reference != bao.Reference || len(e.versionState.cache.RuntimeImages) != 2 || e.versionState.cache.RuntimeImages["LKE_PKI_CONTROLLER_IMAGE"].Reference != image.Reference || e.versionState.cache.RuntimeImages["LKE_PKI_MANAGEMENT_IMAGE"].Reference != management.Reference {
		t.Fatal("runtime receipt chain changed original settings or the current authoritative role selections")
	}
	if deploymentplan.Digest(current) != currentSHA || !reflect.DeepEqual(storeBefore, createOpenBaoACKStoreSnapshot(t, store)) {
		t.Fatal("runtime receipt-chain verification changed current settings or the environment store")
	}

	for _, mode := range []string{"missing-link", "foreign-environment", "non-runtime-role", "wrong-old", "other-role-drift", "ambiguous-intermediate", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			var earlier, earlierCopy, last createVersionReceipt
			_ = json.Unmarshal(mustProviderRecoveryJSON(second), &earlier)
			_ = json.Unmarshal(mustProviderRecoveryJSON(second), &earlierCopy)
			_ = json.Unmarshal(mustProviderRecoveryJSON(third), &last)
			switch mode {
			case "missing-link":
				earlier.Selection.Images["LKE_PKI_MANAGEMENT_IMAGE"] = mustProviderRecoveryJSON(management)
				earlierCopy = earlier
			case "foreign-environment":
				earlier.Previous = strings.Replace(earlier.Previous, `"dev"`, `"staging"`, 1)
				earlierCopy = earlier
			case "non-runtime-role":
				earlier.Differences[0].Role = "LKE_CERTISSUER_IMAGE"
				earlierCopy = earlier
			case "wrong-old":
				earlier.Differences[0].Current = apiRepo + strings.Repeat("0", 64)
				earlierCopy = earlier
			case "other-role-drift":
				prior := first
				prior.Images = map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: baoRepo + strings.Repeat("0", 64)}}
				earlier.Previous = string(mustProviderRecoveryJSON(prior))
				earlierCopy = earlier
			case "ambiguous-intermediate":
				prior := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": bao, "LKE_PKI_MANAGEMENT_IMAGE": {Reference: apiRepo + strings.Repeat("0", 64)}}}
				earlierCopy.Previous = string(mustProviderRecoveryJSON(prior))
				earlierCopy.Differences[1].Current = apiRepo + strings.Repeat("0", 64)
			case "cycle":
				earlier.Previous = current[environmentImagesKey]
				earlier.Differences = []createVersionDifference{{Role: "LKE_PKI_MANAGEMENT_IMAGE", Current: management.Reference, Latest: mustProviderRecoveryJSON(image), RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"}}
				earlierCopy = earlier
			}
			for name, receipt := range map[string]createVersionReceipt{"runtime.json": earlier, "runtime-copy.json": earlierCopy, "runtime-management.json": last} {
				if err := store.write("deployment/version-updates/"+name, mustProviderRecoveryJSON(receipt), true); err != nil {
					t.Fatal(err)
				}
			}
			e.versionState.cache = nil
			_, _, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, command)
			if err == nil {
				t.Fatal("invalid runtime receipt chain accepted")
			}
			if mode == "cycle" && !strings.Contains(err.Error(), "cycle") || mode == "ambiguous-intermediate" && !strings.Contains(err.Error(), "ambiguous") {
				t.Fatal("invalid chain did not fail at its exact link", err)
			}
			for name, receipt := range map[string]createVersionReceipt{"runtime.json": second, "runtime-copy.json": second, "runtime-management.json": third} {
				if err := store.write("deployment/version-updates/"+name, mustProviderRecoveryJSON(receipt), true); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	changed := appendMap(current, map[string]string{"UNRELATED": "foreign"})
	if _, _, err = verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, changed, &parent, command); err == nil {
		t.Fatal("formal image transition permitted unrelated setting drift")
	}
}

// Enabling public CRL export must select a Management package that advertises
// that native function; unrelated roles and profiles keep their existing gate.
func TestCreateRuntimeVersionPublicCRLCapabilityFollowsSelectedManagementProfile(t *testing.T) {
	for _, tc := range []struct {
		name, profile, service, schema string
		roles                          []string
		features                       []string
		commandFails, wantErr          bool
		replacement                    bool
		wantCalls                      int
	}{
		{name: "enabled", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, service: "pkimanagement", schema: "rtk-cloud.deployment-capabilities/v1", features: []string{"public-registration-crl-bundle-v1"}, wantCalls: 1},
		{name: "enabled-missing-feature", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, service: "pkimanagement", schema: "rtk-cloud.deployment-capabilities/v1", wantErr: true, wantCalls: 1},
		{name: "enabled-wrong-service", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, service: "serviceidentity-bootstrap", schema: "rtk-cloud.deployment-capabilities/v1", features: []string{"public-registration-crl-bundle-v1"}, wantErr: true, wantCalls: 1},
		{name: "enabled-wrong-schema", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, service: "pkimanagement", schema: "other/v1", features: []string{"public-registration-crl-bundle-v1"}, wantErr: true, wantCalls: 1},
		{name: "enabled-command-failure", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, commandFails: true, wantErr: true, wantCalls: 1},
		{name: "controller-only", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_CONTROLLER_IMAGE"}},
		{name: "profile-disabled", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}},
		{name: "other-profile", profile: "other", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}},
		{name: "repair-old-package", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_MANAGEMENT_IMAGE"}, service: "pkimanagement", schema: "rtk-cloud.deployment-capabilities/v1", features: []string{"public-registration-crl-bundle-v1"}, replacement: true, wantCalls: 1},
		{name: "same-package-two-roles", profile: "managed-public-pem-v1", roles: []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"}, service: "pkimanagement", schema: "rtk-cloud.deployment-capabilities/v1", features: []string{"public-registration-crl-bundle-v1"}, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCreateOpenBaoACKImageFixture(t)
			api := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:"
			image := environmentReleaseImage{Reference: api + strings.Repeat("c", 64), SourceRevision: f.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}}
			current := map[string]string{"PKI_CONTROLLER_IMAGE": api + strings.Repeat("1", 64), "PKI_MANAGEMENT_IMAGE": api + strings.Repeat("2", 64), "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": tc.profile}
			receipt := createVersionReceipt{Selection: createVersionSelection{Environment: "dev", Images: map[string]json.RawMessage{}}}
			for _, role := range tc.roles {
				receipt.Selection.Images[role] = mustProviderRecoveryJSON(image)
				receipt.Differences = append(receipt.Differences, createVersionDifference{Role: role, Current: current[strings.TrimPrefix(role, "LKE_")], Latest: mustProviderRecoveryJSON(image), RunURL: "https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/77"})
			}
			current[environmentImagesKey] = string(mustProviderRecoveryJSON(receipt.Selection))
			if err := f.store.write("deployment/version-updates/runtime.json", mustProviderRecoveryJSON(receipt), false); err != nil {
				t.Fatal(err)
			}
			calls := 0
			native := f.command(t)
			command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "docker" {
					return native(ctx, name, args...)
				}
				entrypoint := "/app/serviceidentity-bootstrap"
				if slices.Contains(args, "/app/pkimanagement") {
					entrypoint = "/app/pkimanagement"
				}
				capabilityImage := image.Reference
				if tc.replacement && entrypoint == "/app/pkimanagement" {
					capabilityImage = api + strings.Repeat("f", 64)
				}
				want := []string{"run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", entrypoint, capabilityImage, "--deployment-capabilities"}
				if !reflect.DeepEqual(args, want) {
					t.Fatal("runtime capability check changed the selected digest or isolation arguments")
				}
				if entrypoint == "/app/pkimanagement" {
					calls++
					if tc.commandFails {
						return nil, errors.New("native capability unavailable")
					}
					return mustProviderRecoveryJSON(map[string]any{"schema": tc.schema, "service": tc.service, "features": tc.features}), nil
				}
				return mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "serviceidentity-bootstrap", "features": serviceIdentityBootstrapCapabilities("openbao")}), nil
			}
			ctx := t.Context()
			if tc.replacement {
				ctx = context.WithValue(ctx, createRuntimeImageCapabilityRepairKey{}, map[string]environmentReleaseImage{"LKE_PKI_MANAGEMENT_IMAGE": {Reference: api + strings.Repeat("f", 64)}})
			}
			_, images, err := selectedCreateRuntimeVersionUpdate(ctx, f.cfg, f.store, current, command)
			if (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("Management public CRL capability gate: error=%v calls=%d, want error=%t calls=%d", err, calls, tc.wantErr, tc.wantCalls)
			}
			if err == nil && len(images) != len(tc.roles) {
				t.Fatal("capability verification changed selected runtime roles")
			}
		})
	}
}
