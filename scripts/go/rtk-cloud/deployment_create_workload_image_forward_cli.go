package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This prepares an explicit environment selection and immutable child. It does
// not deploy: the ordinary complete preflight and create runner remain owners.
func runCreateWorkloadImageForward(args []string) error {
	fs := flag.NewFlagSet("prepare-create-runtime-images", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	parent := fs.String("parent-plan-sha256", "", "original create generation")
	image := fs.String("image", "", "reviewed immutable API package for the selected PKI roles")
	role := fs.String("role", "", "one explicit PKI runtime role; default selects Controller and Management")
	source := fs.String("source-revision", "", "exact package source")
	runID := fs.Int64("run-id", 0, "exact successful formal Release")
	attempt := fs.Int("run-attempt", 0, "exact successful Release attempt")
	manifest := fs.String("manifest-sha256", "", "exact publication manifest digest")
	confirm := fs.String("confirm", "", "selected stack")
	apply := fs.Bool("apply", false, "apply the reviewed environment selection; no workload writes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	roles, roleErr := createRuntimeImageUpdateRoles(*role)
	if roleErr != nil {
		return roleErr
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || !environmentPreflightDigest.MatchString(*parent) || !immutableEnvironmentImage.MatchString(*image) || !strings.HasPrefix(*image, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@") || !deploymentUpgradeSHA.MatchString(*source) || *runID <= 0 || *attempt <= 0 || !environmentPreflightDigest.MatchString(*manifest) || *confirm != "video-cloud-"+*environment {
		return createProviderRecoveryBlock("select the exact original generation, reviewed immutable PKI package, formal publication and stack")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *environment, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	e, original, err := loadCreateRecoveryExecutionOwner(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	return runEnvironmentProgress(ctx, os.Stdout, "deployment.create.runtime-images.prepare", func(ctx context.Context) error {
		if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
			return createProviderRecoveryBlock("this continuation requires the selected OpenBao identity backend")
		}
		command := environmentCIImageCommand(cfg)
		selected := createVersionImage{Reference: *image, SourceRevision: *source, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: *runID, Attempt: *attempt, ManifestSHA: *manifest}}
		published, err := verifyCreateVersionPublication(ctx, selected, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", command)
		if err != nil {
			return err
		}
		if _, err = command(ctx, "docker", "pull", "--platform", "linux/amd64", *image); err != nil {
			return err
		}
		if err = verifyCreateRuntimeImageCapabilities(ctx, *image, command); err != nil {
			return err
		}
		images := map[string]environmentReleaseImage{}
		for _, role := range roles {
			images[role] = published
		}
		if images["LKE_PKI_MANAGEMENT_IMAGE"].Reference != "" && operator["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] == "managed-public-pem-v1" {
			if err = verifyCreateRuntimeManagementCRLCapabilities(ctx, published.Reference, command); err != nil {
				return err
			}
		}
		// Authenticate every old receipt, original operator input and publication.
		// A proven candidate supplies only the public-export capability that its
		// old package may be missing; ordinary validation uses no such scope.
		repairCtx := context.WithValue(ctx, createRuntimeImageCapabilityRepairKey{}, images)
		if _, _, err = verifyCreateEnvironmentOperator(repairCtx, cfg, store, e, operator, &original.Plan.Plan); err != nil {
			return err
		}
		if completedFreshRuntimeImageSource(ctx, store, e) == nil {
			kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
			if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
				return createProviderRecoveryBlock("original Kubernetes identity differs")
			}
			native, err := newEnvironmentKubernetesBackendForConfig(string(kube), cfg)
			if err != nil {
				return err
			}
			_, err = prepareCreateCompletedRuntimeImageSelection(ctx, cfg, store, e, operator, published, roles, *apply, native, os.Stdout)
			return err
		}
		existing, err := readOriginalCreateWorkloadImageForward(store, e)
		if err != nil {
			return err
		}
		if existing != nil {
			if deploymentplan.Digest(images) != deploymentplan.Digest(existing.Images) {
				return createProviderRecoveryBlock("original runtime image continuation already selects another exact package")
			}
			current, err := readEnvironmentImageSelection(store)
			if err != nil {
				return err
			}
			matches := true
			for role, image := range images {
				matches = matches && deploymentplan.Digest(current.Images[role]) == deploymentplan.Digest(image)
			}
			if !matches {
				// Configuration is the final atomic commit. Resume a crash between
				// private receipt/artifact publication and that commit using the
				// exact stored selection, never reconstruct another target.
				var receipt createVersionReceipt
				receiptPath := filepath.Join(store.Root, "deployment/version-updates", "runtime-"+deploymentplan.Digest(existing)+".json")
				if readExecutionJSON(receiptPath, &receipt) != nil || receipt.Previous != operator[environmentImagesKey] {
					return createProviderRecoveryBlock("prepared runtime selection differs from original configuration")
				}
				raw, err := json.MarshalIndent(receipt.Selection, "", "  ")
				if err != nil {
					return err
				}
				candidate := appendMap(operator, map[string]string{environmentImagesKey: string(raw)})
				_, roles, err := validateCreateRuntimeVersionReceipt(cfg.Environment, candidate, mustProviderRecoveryJSON(receipt))
				if err != nil || deploymentplan.Digest(roles) != deploymentplan.Digest(images) {
					return createProviderRecoveryBlock("prepared runtime receipt differs from reviewed packages")
				}
				if !*apply {
					fmt.Printf("REVIEW environment=%s successor_sha256=%s prepared=true applied=false cloud_writes=0\n", cfg.Environment, existing.Plan.SHA256())
					return e.Check(ctx)
				}
				lock, err := lockEnvironmentPreflightPublication(store)
				if err != nil {
					return err
				}
				defer lock.Close()
				_, fresh, err := readEnvironmentCreateOperator(cfg)
				if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(operator) {
					return createProviderRecoveryBlock("environment settings changed before prepared image commit")
				}
				if err = store.write(filepath.Join("operator/env", environmentImagesKey), raw, true); err != nil {
					return err
				}
			}

			fmt.Printf("PASS environment=%s operation=prepare-create-runtime-images applied=true successor_sha256=%s cloud_writes=0\n", cfg.Environment, existing.Plan.SHA256())
			return e.Check(ctx)
		}
		p, err := readEnvironmentCreateStageInput(store, e, "workload")
		if err != nil || p == nil {
			return createProviderRecoveryBlock("original workload input unavailable")
		}
		j, exists, err := readEnvironmentCreateStageProgress(store, e, "workload", p)
		if err != nil || !exists {
			return createProviderRecoveryBlock("original known workload writes unavailable")
		}
		kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
			return createProviderRecoveryBlock("original Kubernetes identity differs")
		}
		native, err := newEnvironmentKubernetesBackendForConfig(string(kube), cfg)
		if err != nil {
			return err
		}
		live, err := native.Inventory(ctx, p.Stack)
		if err != nil {
			return err
		}
		observed := map[string]deploymentplan.Object{}
		for _, o := range live {
			observed[deploymentplan.ID(o)] = o
		}
		for i, r := range p.Resources {
			if observed[r.ID] == nil || i >= len(j.Completed) {
				observed[r.ID], err = native.Read(ctx, r.ID)
				if err != nil {
					return err
				}
			}
		}
		forward, err := buildCreateWorkloadImageForward(p, j, images, observed, createWorkloadImageForwardStage)
		if err != nil {
			return err
		}
		// Repeat the ordinary native dry-run on this exact successor before publishing
		// configuration. No object is changed by this preview.
		if err = forward.Admit(ctx, native); err != nil {
			return err
		}
		q := createWorkloadImageForward{Schema: createWorkloadImageForwardSchema, Parent: e.Journal.PlanSHA256, OriginalPlan: p.SHA256(), OriginalJournal: deploymentplan.Digest(j), Images: images, Plan: forward}
		fmt.Printf("REVIEW environment=%s source_sha256=%s source_writes=%d/%d successor_sha256=%s qualification_sha256=%s roles=PKI_CONTROLLER_IMAGE,PKI_MANAGEMENT_IMAGE image=%s cloud_writes=0\n", cfg.Environment, p.SHA256(), len(j.Completed), len(p.Resources), forward.SHA256(), deploymentplan.Digest(q), *image)
		if !*apply {
			return e.Check(ctx)
		}
		lock, err := lockEnvironmentPreflightPublication(store)
		if err != nil {
			return err
		}
		defer lock.Close()
		_, fresh, err := readEnvironmentCreateOperator(cfg)
		if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(operator) {
			return createProviderRecoveryBlock("environment settings changed during runtime image review")
		}
		current, err := parseEnvironmentImageSelection(cfg.Environment, operator[environmentImagesKey])
		if err != nil {
			return err
		}
		if current.Images == nil {
			current.Images = map[string]environmentReleaseImage{}
		}
		receipt := createVersionReceipt{Previous: operator[environmentImagesKey], Selection: createVersionSelection{Environment: cfg.Environment, Images: map[string]json.RawMessage{}}}
		for _, role := range roles {
			previous := current.Images[role].Reference
			if previous == "" {
				previous = operator[strings.TrimPrefix(role, "LKE_")]
			}
			current.Images[role] = published
			receipt.Differences = append(receipt.Differences, createVersionDifference{Role: role, Current: previous, Latest: mustProviderRecoveryJSON(published), RunURL: fmt.Sprintf("https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/%d", *runID)})
		}
		for role, image := range current.Images {
			receipt.Selection.Images[role] = mustProviderRecoveryJSON(image)
		}
		selectedRaw, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			return err
		}
		if _, _, err = validateCreateRuntimeVersionReceipt(cfg.Environment, appendMap(operator, map[string]string{environmentImagesKey: string(selectedRaw)}), mustProviderRecoveryJSON(receipt)); err != nil {
			return err
		}
		unchanged, exists, err := p.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(unchanged) != q.OriginalJournal || e.Check(ctx) != nil {
			return createProviderRecoveryBlock("original workload changed before configuration publication")
		}
		receiptPath := filepath.Join("deployment/version-updates", "runtime-"+deploymentplan.Digest(q)+".json")
		if err = writeCreateWorkloadImageArtifact(store, receiptPath, receipt); err != nil {
			return err
		}
		relative, err := filepath.Rel(store.Root, workloadImageForwardPath(e))
		if err != nil {
			return err
		}
		if err = writeCreateWorkloadImageArtifact(store, relative, q); err != nil {
			return err
		}
		if err = store.write(filepath.Join("operator/env", environmentImagesKey), selectedRaw, true); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "PASS environment=%s operation=prepare-create-runtime-images applied=true successor_sha256=%s cloud_writes=0\n", cfg.Environment, forward.SHA256())
		return e.Check(ctx)
	})
}

func createRuntimeImageUpdateRoles(role string) ([]string, error) {
	if role == "" {
		return []string{"LKE_PKI_CONTROLLER_IMAGE", "LKE_PKI_MANAGEMENT_IMAGE"}, nil
	}
	if role != "LKE_PKI_CONTROLLER_IMAGE" && role != "LKE_PKI_MANAGEMENT_IMAGE" {
		return nil, createProviderRecoveryBlock("runtime image update role must be Controller or Management")
	}
	return []string{role}, nil
}

// The explicit updater commits configuration only. The complete ordinary GO
// and create runner own the new selection-bound native child, independently of
// the immutable artifact that completed the original failed workload.
func prepareCreateCompletedRuntimeImageSelection(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, operator map[string]string, published environmentReleaseImage, roles []string, apply bool, backend deploymentplan.Backend, out io.Writer) (bool, error) {
	if len(roles) == 0 || len(roles) > 2 || published.Publication == nil {
		return true, createProviderRecoveryBlock("runtime role selection lacks its exact reviewed publication and roles")
	}
	if err := completedFreshRuntimeImageSource(ctx, store, e); err != nil {
		return false, err
	}
	current, err := parseEnvironmentImageSelection(cfg.Environment, operator[environmentImagesKey])
	if err != nil {
		return true, err
	}
	if current.Images == nil {
		current.Images = map[string]environmentReleaseImage{}
	}
	receipt := createVersionReceipt{Previous: operator[environmentImagesKey], Selection: createVersionSelection{Environment: cfg.Environment, Images: map[string]json.RawMessage{}}}
	for _, role := range roles {
		if _, err := createRuntimeImageUpdateRoles(role); err != nil || role == "" {
			return true, createProviderRecoveryBlock("runtime image update has an invalid explicit role")
		}
		if deploymentplan.Digest(current.Images[role]) == deploymentplan.Digest(published) {
			continue
		}
		previous := current.Images[role].Reference
		if previous == "" {
			previous = operator[strings.TrimPrefix(role, "LKE_")]
		}
		current.Images[role] = published
		receipt.Differences = append(receipt.Differences, createVersionDifference{Role: role, Current: previous, Latest: mustProviderRecoveryJSON(published), RunURL: fmt.Sprintf("https://github.com/hkt999rtk/rtk_video_cloud/actions/runs/%d", published.Publication.RunID)})
	}
	if len(receipt.Differences) == 0 {
		fmt.Fprintf(out, "PASS environment=%s operation=prepare-create-runtime-images selected=true cloud_writes=0\n", cfg.Environment)
		return true, e.Check(ctx)
	}
	runtimeImages := map[string]environmentReleaseImage{}
	for role, image := range current.Images {
		receipt.Selection.Images[role] = mustProviderRecoveryJSON(image)
		if role == "LKE_PKI_CONTROLLER_IMAGE" || role == "LKE_PKI_MANAGEMENT_IMAGE" {
			runtimeImages[role] = image
		}
	}
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return true, err
	}
	candidate := appendMap(operator, map[string]string{environmentImagesKey: string(raw)})
	if _, _, err = validateCreateRuntimeVersionReceipt(cfg.Environment, candidate, mustProviderRecoveryJSON(receipt)); err != nil {
		return true, err
	}
	q, stage, err := selectedCreateWorkloadImageForward(ctx, store, e, backend, runtimeImages)
	if err != nil {
		return true, err
	}
	if err = q.Plan.AdmitResumable(ctx, backend, store.Root); err != nil {
		return true, err
	}
	fmt.Fprintf(out, "REVIEW environment=%s stage=%s successor_sha256=%s cloud_writes=0\n", cfg.Environment, stage, q.Plan.SHA256())
	if !apply {
		return true, e.Check(ctx)
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return true, err
	}
	defer lock.Close()
	_, fresh, err := readEnvironmentCreateOperator(cfg)
	if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(operator) {
		return true, createProviderRecoveryBlock("environment settings changed before runtime role selection commit")
	}
	if err = e.Check(ctx); err != nil {
		return true, err
	}
	relative := filepath.Join("deployment/version-updates", "runtime-"+deploymentplan.Digest(receipt)+".json")
	if err = writeCreateWorkloadImageArtifact(store, relative, receipt); err != nil {
		return true, err
	}
	if err = store.write(filepath.Join("operator/env", environmentImagesKey), raw, true); err != nil {
		return true, err
	}
	if e.versionState != nil {
		e.versionState.cache = nil
	}
	fmt.Fprintf(out, "PASS environment=%s operation=prepare-create-runtime-images applied=true cloud_writes=0\n", cfg.Environment)
	return true, e.Check(ctx)
}
