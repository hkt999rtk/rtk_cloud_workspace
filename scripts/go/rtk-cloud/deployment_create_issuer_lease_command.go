package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type issuerLeaseRepairReview struct {
	Parent, ParentJournal, Child, ChildJournal, Kubeconfig, Checker string
	Patch                                                           issuerLeasePatchPlan
}

type issuerLeaseRepairQualification struct {
	Review                  issuerLeaseRepairReview
	QualifiedAt, ValidUntil time.Time
}

func runEnvironmentCreateIssuerLeaseRepair(args []string) error {
	flags := flag.NewFlagSet("recover-create-issuer-lease", flag.ContinueOnError)
	env := flags.String("environment", "", "selected dev environment")
	parent := flags.String("parent-plan-sha256", "", "original create recovery plan")
	execute := flags.Bool("execute", false, "apply only the reviewed Lease label repair")
	reviewSHA := flags.String("review-sha256", "", "exact repair qualification digest")
	confirm := flags.String("confirm", "", "exact stack for execution")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) ||
		*execute && (!environmentPreflightDigest.MatchString(*reviewSHA) || *confirm != "video-cloud-dev") ||
		!*execute && (*confirm != "" || *reviewSHA != "") {
		return createProviderRecoveryBlock("select the original dev parent and provide exact stack and review digest only for execution")
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-issuer-lease"}, args...)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(root, *env, "")
	if err != nil {
		return err
	}
	store, err := newSecretStore("", *env)
	if err != nil {
		return err
	}
	e, original, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil || deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("original admitted operator inputs differ")
	}
	last := e.Journal.Entries[len(e.Journal.Entries)-1]
	if last.Key != "kubernetes/service-issuer-server-enrollment" || last.State != "pending" || len(last.Receipt) != 0 {
		return createProviderRecoveryBlock("only the original pending CertIssuer server enrollment is eligible for this repair")
	}
	child, childJournal, err := originalCreateChildAtIntent(store, e, last)
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	kubeSHA := environmentPreflightBytesSHA256(raw)
	if err != nil || kubeSHA != original.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original admitted Kubernetes identity differs")
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), *env)
	if err != nil {
		return err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	repair := issuerLeaseEnrollmentRecovery{plan: child, journal: childJournal}
	review := issuerLeaseRepairReview{Parent: *parent, ParentJournal: deploymentplan.Digest(e.Journal),
		Child: child.SHA256(), ChildJournal: deploymentplan.Digest(childJournal), Kubeconfig: kubeSHA, Checker: checker}
	base := filepath.Join("deployment", "create", "issuer-lease-repair", *parent)
	now := time.Now().UTC()
	if !*execute {
		review.Patch, err = repair.prepareLabelPatch(ctx, backend)
		if err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		sha := deploymentplan.Digest(review)
		directory := filepath.Join(base, sha)
		if _, err := prepareEnvironmentCreateDirectories(store, directory); err != nil {
			return err
		}
		result := issuerLeaseRepairQualification{Review: review, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
		if err := store.write(filepath.Join(directory, "qualification.json"), mustProviderRecoveryJSON(result), true); err != nil {
			return err
		}
		fmt.Printf("GO environment=dev scope=certissuer-lease-label-repair review-sha256=%s deployment-completed=false\n", sha)
		return nil
	}
	var evidence issuerLeaseRepairQualification
	directory := filepath.Join(base, *reviewSHA)
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, directory, "qualification.json"), &evidence) != nil ||
		deploymentplan.Digest(evidence.Review) != *reviewSHA || evidence.QualifiedAt.After(now) || !evidence.ValidUntil.Equal(evidence.QualifiedAt.Add(time.Hour)) || !now.Before(evidence.ValidUntil) {
		return createProviderRecoveryBlock("fresh exact Lease label repair qualification is required")
	}
	review.Patch = evidence.Review.Patch
	if deploymentplan.Digest(review) != *reviewSHA || review.Patch.Resource != childJournal.Failed {
		return createProviderRecoveryBlock("original child, ownership, cluster or checker changed since Lease repair qualification")
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(directory, "original-child-journal.json"), mustProviderRecoveryJSON(childJournal)); err != nil {
		return err
	}
	var completed struct {
		Review string `json:"review_sha256"`
		Lease  string `json:"lease_sha256"`
	}
	completedPath := filepath.Join(store.Root, directory, "completed.json")
	if _, err := os.Lstat(completedPath); errors.Is(err, os.ErrNotExist) {
		patch, err := repair.prepareLabelPatch(ctx, backend)
		if err != nil {
			return err
		}
		if deploymentplan.Digest(patch) != deploymentplan.Digest(review.Patch) {
			return createProviderRecoveryBlock("current exact Lease dry-run differs from the reviewed correction")
		}
		if err := executeIssuerLeaseRepair(ctx, store, e, backend, child, review, directory); err != nil {
			return err
		}
	} else {
		if err != nil || readEnvironmentPreflightJSON(completedPath, &completed) != nil || completed.Review != *reviewSHA || completed.Lease != review.Patch.AfterSHA {
			return createProviderRecoveryBlock("retained Lease repair receipt cannot be verified")
		}
		actual, err := backend.Read(ctx, review.Patch.Resource)
		if err != nil || deploymentplan.Digest(deploymentplan.Projection(actual)) != completed.Lease {
			return createProviderRecoveryBlock("original corrected Lease differs from its retained receipt")
		}
	}
	if err := acceptIssuerLeaseEnrollment(ctx, cfg, store, e, backend, child, original.Plan.Plan.OperatorSHA256); err != nil {
		return err
	}
	fmt.Printf("PASS environment=dev scope=certissuer-lease-label-repair-and-enrollment-acceptance review-sha256=%s deployment-completed=false\n", *reviewSHA)
	return nil
}

func acceptIssuerLeaseEnrollment(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, child *deploymentplan.Plan, operatorSHA string) error {
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store, stagePlanSHA256: child.SHA256()}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := backend.CheckParentExecution(ctx, p, store.Root); err != nil {
			return err
		}
		_, current, err := readEnvironmentCreateOperator(cfg)
		fresh, configErr := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
		if err != nil || configErr != nil || deploymentplan.Digest(fresh) != p.ConfigurationSHA256 || deploymentplan.Digest(current) != operatorSHA {
			return createProviderRecoveryBlock("original enrollment acceptance inputs changed")
		}
		return nil
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := fence(ctx, p); err != nil {
			return err
		}
		objects, err := native.TargetWorkloadInventory(ctx, p)
		if err != nil {
			return err
		}
		return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		})
	}
	return executeEnvironmentCreateStagePlan(ctx, store, e, "service-issuer-server-enrollment", child, backend, fence, fence, accept, func(ctx context.Context) error {
		return captureEnvironmentCreateVolumeOwners(ctx, cfg, native, e)
	}, os.Stdout)
}

func executeIssuerLeaseRepair(ctx context.Context, store secretStore, e *environmentCreateExecution, backend environmentKubernetesBackend, child *deploymentplan.Plan, review issuerLeaseRepairReview, directory string) (resultErr error) {
	if err := e.Check(ctx); err != nil {
		return err
	}
	lock, err := backend.AcquireExecution(ctx, child)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := deploymentCleanupContext()
		defer cancel()
		resultErr = errors.Join(resultErr, lock.Release(cleanup))
	}()
	if err := e.Check(ctx); err != nil {
		return err
	}
	if err := lock.Check(ctx); err != nil {
		return err
	}
	return review.Patch.apply(ctx, backend.Read, func(ctx context.Context) error {
		return store.write(filepath.Join(directory, "intent.json"), mustProviderRecoveryJSON(deploymentplan.Object{
			"review_sha256": deploymentplan.Digest(review), "execution_lease": lock.Owner(), "admitted_at": time.Now().UTC(),
		}), false)
	}, func(ctx context.Context) (deploymentplan.Object, error) {
		if err := e.Check(ctx); err != nil {
			return nil, err
		}
		if err := lock.Check(ctx); err != nil {
			return nil, err
		}
		return review.Patch.call(ctx, backend, false)
	}, func(ctx context.Context) error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := lock.Check(ctx); err != nil {
			return err
		}
		return store.write(filepath.Join(directory, "completed.json"), mustProviderRecoveryJSON(deploymentplan.Object{
			"review_sha256": deploymentplan.Digest(review), "completed_at": time.Now().UTC(), "lease_sha256": review.Patch.AfterSHA,
		}), false)
	})
}

func (p issuerLeasePatchPlan) apply(ctx context.Context, read func(context.Context, string) (deploymentplan.Object, error), intent func(context.Context) error, patch func(context.Context) (deploymentplan.Object, error), complete func(context.Context) error) error {
	before, err := read(ctx, p.Resource)
	if err != nil || before == nil || deploymentplan.Digest(before) != p.BeforeSHA {
		return createProviderRecoveryBlock("original Lease changed since exact repair qualification")
	}
	if err := intent(ctx); err != nil {
		return err
	}
	observed, err := patch(ctx)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(deploymentplan.Projection(observed)) != p.AfterSHA {
		return createProviderRecoveryBlock("Lease label repair response differs from the approved result")
	}
	observed, err = read(ctx, p.Resource)
	if err != nil || deploymentplan.Digest(deploymentplan.Projection(observed)) != p.AfterSHA {
		return createProviderRecoveryBlock("Lease label repair readback differs from the approved result")
	}
	return complete(ctx)
}
