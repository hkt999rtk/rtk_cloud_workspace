package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"syscall"
	"time"
)

func runCRLMaintenance(args []string) error {
	return runCRLMaintenanceCLI(context.Background(), args, os.Stdout, os.Stderr)
}

func maintenanceImage(ctx context.Context, cfg deploymentConfig, selected, sourceRevision string) (string, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return "", err
	}
	return maintenanceImageWithCommand(ctx, cfg, store, selected, sourceRevision, environmentCIImageCommand(cfg))
}

func maintenanceImageWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, selected, sourceRevision string, run environmentReleaseCommand) (string, error) {
	revision := sourceRevision
	if revision == "" {
		raw, err := run(ctx, "git", "-C", filepath.Join(cfg.Workspace, "repos/rtk_video_cloud"), "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		revision = strings.TrimSpace(string(raw))
	}
	if !deploymentUpgradeSHA.MatchString(revision) {
		return "", errors.New("native helper source revision unavailable")
	}

	cache := maintenanceImageEvidencePath(selected, revision)
	var cached crlMaintenanceImageEvidence
	if err := maintenanceRead(store, cache, &cached); err == nil {
		if cached.Environment != cfg.Environment || cached.Revision != revision || selected != "" && cached.Image != selected {
			return "", errors.New("cached helper provenance belongs to another environment, source or digest")
		}
		image, err := selectEnvironmentPublishedImage(cached.Manifest, "hkt999rtk/rtk_video_cloud", cached.RunID, cached.Attempt, revision, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", false)
		if err != nil || image.Reference != cached.Image {
			return "", errors.New("cached helper provenance is invalid")
		}
		if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
			return "", err
		}
		return image.Reference, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("original helper provenance cannot be read")
	}
	evidence, id, attempt, err := latestEnvironmentPublication(ctx, "hkt999rtk/rtk_video_cloud", "release.yml", revision, false, run)
	if err != nil {
		return "", err
	}
	image, err := selectEnvironmentPublishedImage(evidence, "hkt999rtk/rtk_video_cloud", id, attempt, revision, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", false)
	if err != nil {
		return "", err
	}
	if selected != "" && selected != image.Reference {
		return "", errors.New("selected helper image differs from exact-source formal CI publication")
	}
	if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
		return "", err
	}

	proof := crlMaintenanceImageEvidence{Environment: cfg.Environment, Image: image.Reference, Revision: revision, RunID: id, Attempt: attempt, Manifest: evidence}
	for _, relative := range []string{maintenanceImageEvidencePath(image.Reference, revision), maintenanceImageEvidencePath("", revision)} {
		if err := maintenanceWrite(store, relative, proof); err != nil {
			return "", err
		}
	}
	return image.Reference, nil
}

type crlMaintenanceImageEvidence struct {
	Environment string          `json:"environment"`
	Image       string          `json:"image"`
	Revision    string          `json:"source_revision"`
	RunID       int64           `json:"run_id"`
	Attempt     int             `json:"run_attempt"`
	Manifest    json.RawMessage `json:"verified_ci_manifest"`
}

func maintenanceImageEvidencePath(image, revision string) string {
	if image != "" {
		return crlMaintenanceBase + "/helpers/" + environmentPreflightBytesSHA256([]byte(image)) + "/publication.json"
	}
	return crlMaintenanceBase + "/helpers/sources/" + revision + ".json"
}

func runCRLMaintenanceCLI(parent context.Context, args []string, out, diagnostics io.Writer) (resultErr error) {
	fs := flag.NewFlagSet("deployment renew-crls", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	environment := fs.String("environment", "", "explicit dev, staging or prod")
	workspace := fs.String("workspace", "", "workspace checkout")
	execute := fs.Bool("execute", false, "renew due original offline Roots and converge actual consumers")
	recoverExpired := fs.Bool("recover", false, "allow recovery of expired prior CRLs through native Kubernetes transport")
	confirm := fs.String("confirm", "", "exact selected stack required for execution")
	image := fs.String("image", "", "optional exact formal CI helper image digest")
	helperRevision := fs.String("helper-revision", "", "pinned native helper source commit, independent of checkout updates")
	timeout := fs.Duration("timeout", 15*time.Minute, "bounded operation deadline (maximum 30m)")
	fs.Bool("status", false, "read-only current inventory and renewal status (default)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*environment != "dev" && *environment != "staging" && *environment != "prod") {
		return errors.New("--environment dev|staging|prod is required; positional arguments are not accepted")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("--timeout must be positive and at most 30m")
	}
	if !*execute && *confirm != "" {
		return errors.New("--confirm requires --execute")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if *execute && *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return errors.New("--execute requires --confirm with the exact selected stack")
	}
	store, err := newSecretStore("", *environment)
	if err != nil {
		return err
	}
	path, err := maintenancePath(store, "kube/kubeconfig.yaml")
	if err != nil {
		return err
	}
	kube, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return err
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), *environment)
	if err != nil {
		return err
	}
	backend.Store = store
	backend.RolloutTimeout = *timeout
	signals, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, *timeout)
	defer cancel()
	r := &maintenanceRuntime{cfg: cfg, store: store, backend: backend, kubeconfig: path}
	// Register host-lock release first so it remains held through cleanup and
	// the final persisted/emitted status, including every early-return path.
	var hostLock *os.File
	defer func() {
		if hostLock != nil {
			_ = hostLock.Close()
		}
	}()
	var finalStatus *crlMaintenanceStatus
	var previousStatus crlMaintenanceStatus
	_ = maintenanceRead(store, crlMaintenanceBase+"/status.json", &previousStatus)
	defer func() {
		if finalStatus == nil {
			return
		}
		if resultErr != nil {
			finalStatus.Completed = false
			finalStatus.Failure = resultErr.Error()
			finalStatus.LastSuccess = previousStatus.LastSuccess
		}
		if *execute {
			if e := maintenanceWrite(store, crlMaintenanceBase+"/status.json", finalStatus); resultErr == nil && e != nil {
				resultErr = e
			}
		}
		if e := json.NewEncoder(out).Encode(finalStatus); resultErr == nil && e != nil {
			resultErr = e
		}
	}()

	if *execute {
		// All normal deployments share this Kubernetes lease. The host flock also
		// prevents scheduler/manual races before any local signing files are written.
		directory := filepath.Join(store.Root, crlMaintenanceBase)
		if _, err := maintenancePath(store, crlMaintenanceBase+"/running.lock"); err != nil {
			return err
		}
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
		_, statErr := os.Lstat(filepath.Join(directory, "running.lock"))
		lock, err := deploymentplan.LockExecutionOwner(directory, errors.Is(statErr, os.ErrNotExist))
		if err != nil {
			return err
		}
		hostLock = lock
		plan := rootCRLLeasePlan(cfg, environmentPreflightBytesSHA256(backend.KubeconfigContent))
		if err := checkEnvironmentExecutionOccupancy(ctx, backend, plan, store.Root); err != nil {
			return err
		}
		r.lease, err = backend.AcquireExecution(ctx, plan)
		if err != nil {
			return err
		}
		frozen, removeFrozen, frozenErr := backend.privateKubeconfig()
		if frozenErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = r.lease.Release(cleanup)
			return frozenErr
		}
		r.kubeconfig = frozen
		defer removeFrozen()
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if r.helper != nil {
				if e := r.helper.Close(cleanup); resultErr == nil && e != nil {
					resultErr = e
				}
			}
			if e := r.lease.Release(cleanup); resultErr == nil && e != nil {
				resultErr = e
			}
		}()

	}
	r.ensureImage = func(ctx context.Context) (string, error) { return maintenanceImage(ctx, cfg, *image, *helperRevision) }
	options := crlMaintenanceOptions{Environment: *environment, Execute: *execute, Recover: *recoverExpired, ClusterSHA: environmentPreflightBytesSHA256(backend.KubeconfigContent), Now: time.Now}
	deps := crlMaintenanceDependencies{Observe: r.observe, Prepare: r.prepare, Sign: func(ctx context.Context, item crlMaintenanceIssuer, request createRootCRLRequest, candidate string) (createRootCRLRecord, error) {
		if err := r.lease.Check(ctx); err != nil {
			return createRootCRLRecord{}, err
		}
		if r.image == "" {
			image, err := r.ensureImage(ctx)
			if err != nil {
				return createRootCRLRecord{}, err
			}
			r.image = image
		}
		return signMaintenanceCRL(ctx, store, r.image, item, request, candidate)
	}, Import: r.publish, Converge: r.converge}
	status, err := executeCRLMaintenance(ctx, store, options, deps)
	finalStatus = &status
	if err != nil {
		fmt.Fprintln(diagnostics, "CRL maintenance incomplete; preserve canonical evidence and original custody")
	}
	return err
}
