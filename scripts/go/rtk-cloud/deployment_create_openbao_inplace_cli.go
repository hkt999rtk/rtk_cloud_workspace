package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

// This backend accepts only the StatefulSet template. OnDelete deliberately
// leaves the original provider Pod running; it never deletes or unseals it.
type openBaoTemplateBackend struct {
	environmentCreateKubernetesBackend
}

func (b openBaoTemplateBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		o, err := b.Read(ctx, r.ID)
		if err != nil {
			return err
		}
		if o == nil || deploymentplan.Map(o["metadata"])["uid"] != r.UID || !deploymentplan.Matches(r.Desired, o) {
			return createProviderRecoveryBlock("original OpenBao template changed")
		}
		m, s := deploymentplan.Map(o["metadata"]), deploymentplan.Map(o["status"])
		if m["generation"] != nil && s["observedGeneration"] == m["generation"] && deploymentplan.String(s["updateRevision"]) != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func openBaoInPlaceQualificationPath(parent, review string) string {
	return filepath.Join("deployment/create", parent, "openbao-inplace", review+".json")
}
func openBaoInPlaceSource(store secretStore, e *environmentCreateExecution, q openBaoInPlaceQualification) (*deploymentplan.Plan, deploymentplan.Object, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Object, error) {
		return nil, nil, createProviderRecoveryBlock("original OpenBao image source changed")
	}
	p, j, err := acceptedCreateStage(store, e, "kubernetes/"+q.SourceStage)
	if err != nil || p == nil || p.SHA256() != q.SourcePlanSHA || deploymentplan.Digest(j) != q.SourceJournalSHA {
		return bad()
	}
	id := "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
	var o deploymentplan.Object
	for _, r := range p.Resources {
		if r.ID == id && r.Action != "retire" {
			if o != nil {
				return bad()
			}
			o = deploymentplan.Clone(r.Desired)
		}
	}
	if o == nil || j.Owners[id] == "" {
		return bad()
	}
	deploymentplan.Map(o["metadata"])["uid"] = j.Owners[id]
	return p, o, nil
}
func openBaoInPlaceTemplatePlan(source *deploymentplan.Plan, original, live deploymentplan.Object, image, stage string) (*deploymentplan.Plan, error) {
	target := deploymentplan.Clone(original)
	p := environmentPKIPod(target)
	host := managedNamedField(p["containers"], "openbao-pki")
	init := managedNamedField(p["initContainers"], "openbao-pki-install")
	if host == nil || init == nil {
		return nil, createProviderRecoveryBlock("original OpenBao host template unavailable")
	}
	host["image"], init["image"] = image, image
	defs := []deploymentplan.Definition{{Phase: "workload", Object: target}}
	in := source.Inputs
	in.Operation = "scoped"
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	return deploymentplan.Compile(in, defs, []deploymentplan.Object{live})
}
func openBaoInPlacePatch(ctx context.Context, b environmentKubernetesBackend, namespace, kind, name string, patch []openBaoImagePatch, dry bool) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	args := []string{"--namespace", namespace, "patch", kind, name, "--type=json", "--patch=" + string(raw), "-o", "json"}
	if dry {
		args = append(args, "--dry-run=server")
	}
	_, err = b.command(ctx, nil, args...)
	return err
}
func openBaoInPlaceStatuses(pod deploymentplan.Object) (string, string, error) {
	bad := func() (string, string, error) {
		return "", "", createProviderRecoveryBlock("original OpenBao containers are not running and ready")
	}
	if pod == nil || !staleIdentityPodReady(pod, true) {
		return bad()
	}
	status := deploymentplan.Map(pod["status"])
	main := managedNamedField(status["containerStatuses"], "openbao")
	host := managedNamedField(status["containerStatuses"], "openbao-pki")
	if main == nil || host == nil || main["ready"] != true || host["ready"] != true || deploymentplan.String(main["containerID"]) == "" || deploymentplan.String(host["containerID"]) == "" {
		return bad()
	}
	return deploymentplan.String(main["containerID"]), deploymentplan.String(host["containerID"]), nil
}

type openBaoInPlacePrepared struct {
	cfg                 deploymentConfig
	store               secretStore
	execution           *environmentCreateExecution
	image               environmentReleaseImage
	selection, manifest string
	native              environmentKubernetesBackend
	backend             environmentCreateKubernetesBackend
	cleanup             func()
}

func prepareCreateOpenBaoInPlace(ctx context.Context, environment string) (*openBaoInPlacePrepared, error) {
	workspace, err := workspaceRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := resolveDeploymentConfig(workspace, environment, "")
	if err != nil {
		return nil, err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return nil, err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return nil, err
	}
	e, original, err := loadActiveTransportExecution(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			_ = e.Close()
		}
	}()
	if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(checkpoint.Journal) || deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 {
		return nil, createProviderRecoveryBlock("original environment or execution owner changed")
	}
	_, image, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &original.Plan.Plan)
	if err != nil {
		return nil, err
	}
	selection, manifest, ok := createOperatorVersionEvidence(e, operator)
	if !ok || image.Reference == original.Plan.Plan.Release.Images["LKE_OPENBAO_PKI_IMAGE"].Reference {
		return nil, createProviderRecoveryBlock("environment has no authenticated new OpenBao host image selection")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return nil, createProviderRecoveryBlock("original Kubernetes binding changed")
	}
	native, err := newEnvironmentKubernetesBackend(string(kube), environment)
	if err != nil {
		return nil, err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return nil, err
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	ready = true
	return &openBaoInPlacePrepared{cfg: cfg, store: store, execution: e, image: image, selection: selection, manifest: manifest, native: native, backend: backend, cleanup: func() { _ = e.Close() }}, nil
}

func runCreateOpenBaoInPlace(args []string) error {
	return runCreateOpenBaoInPlaceWith(args, prepareCreateOpenBaoInPlace)
}

func runCreateOpenBaoInPlaceWith(args []string, prepare func(context.Context, string) (*openBaoInPlacePrepared, error)) (result error) {
	fs := flag.NewFlagSet("recover-create-openbao-inplace", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	execute := fs.Bool("execute", false, "apply the qualified original host image")
	review := fs.String("plan-sha256", "", "qualified image handoff")
	timeout := fs.Duration("timeout", 10*time.Minute, "operation timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || *timeout <= 0 || (*execute && !environmentPreflightDigest.MatchString(*review)) || (!*execute && *review != "") {
		return createProviderRecoveryBlock("select environment and the qualified plan for execution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	prepared, err := prepare(ctx, *env)
	if err != nil {
		return err
	}
	if prepared == nil || prepared.execution == nil || prepared.cleanup == nil {
		return createProviderRecoveryBlock("original OpenBao execution preparation is unavailable")
	}
	defer prepared.cleanup()
	cfg, store, e, image := prepared.cfg, prepared.store, prepared.execution, prepared.image
	selection, manifest, native, backend := prepared.selection, prepared.manifest, prepared.native, prepared.backend
	ns := e.Journal.Stack + "-secrets"
	id := "StatefulSet/" + ns + "/openbao"
	var q openBaoInPlaceQualification
	if !*execute {
		sources, err := loadServiceSessionCoreSources(ctx, store, e, backend, []string{id})
		if err != nil || len(sources) != 1 {
			return createProviderRecoveryBlock("original OpenBao StatefulSet source unavailable")
		}
		source := sources[0]
		var recorded deploymentplan.Object
		for _, r := range source.Plan.Resources {
			if r.ID == id {
				recorded = deploymentplan.Clone(r.Desired)
			}
		}
		if recorded == nil || source.Journal.Owners[id] == "" {
			return createProviderRecoveryBlock("original OpenBao owner unavailable")
		}
		deploymentplan.Map(recorded["metadata"])["uid"] = source.Journal.Owners[id]
		set, err := native.Read(ctx, id)
		if err != nil {
			return err
		}
		pod, err := native.Read(ctx, "Pod/"+ns+"/openbao-0")
		if err != nil {
			return err
		}
		tp, pp, err := compileOpenBaoInPlaceImagePatches(recorded, set, pod, image.Reference)
		if err != nil || len(tp) == 0 || len(pp) == 0 || !openBaoInPlaceUsesPVC(pod) {
			return createProviderRecoveryBlock("original OpenBao in-place initial target unavailable")
		}
		main, host, err := openBaoInPlaceStatuses(pod)
		if err != nil {
			return err
		}
		stage := "openbao-inplace-template-" + deploymentplan.Digest(image.Reference)[:12]
		plan, err := openBaoInPlaceTemplatePlan(source.Plan, recorded, set, image.Reference, stage)
		if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "update" {
			return createProviderRecoveryBlock("OpenBao template candidate unavailable")
		}
		if err = plan.Admit(ctx, backend); err != nil {
			return err
		}
		if err = openBaoInPlacePatch(ctx, native, ns, "pod", "openbao-0", pp, true); err != nil {
			return err
		}
		q = openBaoInPlaceQualification{Environment: *env, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), SourceStage: source.Stage, SourcePlanSHA: source.Plan.SHA256(), SourceJournalSHA: deploymentplan.Digest(source.Journal), TargetImage: image.Reference, SelectionSHA: selection, ManifestSHA: manifest, PodUID: deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), ProviderContainerID: main, OldHostContainerID: host, TemplateStage: stage, TemplatePlanSHA: plan.SHA256(), ParentJournal: e.Journal}
		sha := deploymentplan.Digest(q)
		path := openBaoInPlaceQualificationPath(q.ParentSHA, sha)
		if _, err = os.Lstat(filepath.Join(store.Root, path)); os.IsNotExist(err) {
			if err = store.write(path, mustProviderRecoveryJSON(q), false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			var saved openBaoInPlaceQualification
			if readExecutionJSON(filepath.Join(store.Root, path), &saved) != nil || deploymentplan.Digest(saved) != sha {
				return createProviderRecoveryBlock("saved OpenBao qualification differs")
			}
		}
		fmt.Fprintf(os.Stdout, "plan-sha256: %s\nGO environment=%s scope=original-openbao-host-image completed=false\n", sha, *env)
		return nil
	}
	if readExecutionJSON(filepath.Join(store.Root, openBaoInPlaceQualificationPath(e.Journal.PlanSHA256, *review)), &q) != nil || deploymentplan.Digest(q) != *review || q.ParentSHA != e.Journal.PlanSHA256 || q.ParentOwnerSHA != deploymentplan.Digest(e.owner) || q.Environment != *env || q.TargetImage != image.Reference || q.SelectionSHA != selection || q.ManifestSHA != manifest || !partialSessionJournalExtends(q.ParentJournal, e.Journal) {
		return createProviderRecoveryBlock("OpenBao in-place qualification changed")
	}
	source, recorded, err := openBaoInPlaceSource(store, e, q)
	if err != nil {
		return err
	}
	set, err := native.Read(ctx, id)
	if err != nil {
		return err
	}
	stage := q.TemplateStage
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return err
	}
	if plan == nil {
		plan, err = openBaoInPlaceTemplatePlan(source, recorded, set, q.TargetImage, stage)
		if err != nil {
			return err
		}
	}
	if plan.SHA256() != q.TemplatePlanSHA {
		return createProviderRecoveryBlock("original OpenBao template plan changed")
	}
	key := "openbao-host-inplace-" + *review
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intent := openBaoInPlaceIntent(q)
	intentSHA, _ := environmentCreatePlanSHA256(intent)
	if entry == nil {
		if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(q.ParentJournal) {
			return createProviderRecoveryBlock("original execution changed before image admission")
		}
		if err = e.BeginEntry(key, "openbao-host-inplace", intent, time.Now().UTC()); err != nil {
			return err
		}
	} else if entry.Phase != "openbao-host-inplace" || entry.IntentSHA256 != intentSHA || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return createProviderRecoveryBlock("original OpenBao image handoff intent changed")
	}
	if entry != nil && entry.State == "completed" {
		_, err = verifyOpenBaoInPlaceReady(ctx, cfg, store, e, native)
		return err
	}
	wrapped := openBaoTemplateBackend{backend}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, wrapped, store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return backend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, wrapped) }
	if err = executeEnvironmentCreateStagePlan(ctx, store, e, stage, plan, wrapped, qualify, fence, accept, func(context.Context) error { return nil }, os.Stdout); err != nil {
		return err
	}
	if err = backend.CheckParentExecution(ctx, plan, store.Root); err != nil {
		return err
	}
	lease, err := backend.AcquireExecution(ctx, plan)
	if err != nil || lease == nil {
		return createProviderRecoveryBlock("original OpenBao execution lease unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil && result == nil {
			result = err
		}
	}()
	owner := lease.Owner()
	fenced := func() bool {
		return e.Check(ctx) == nil && lease.Check(ctx) == nil && lease.Owner() == owner && backend.CheckParentExecution(ctx, plan, store.Root) == nil
	}
	if !fenced() || backend.RecordParentExecutionLease(ctx, plan, owner) != nil {
		return createProviderRecoveryBlock("original OpenBao execution fence changed")
	}
	set, err = native.Read(ctx, id)
	if err != nil {
		return err
	}
	pod, err := native.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil {
		return err
	}
	if pod == nil || deploymentplan.Map(pod["metadata"])["uid"] != q.PodUID {
		return createProviderRecoveryBlock("original OpenBao Pod was replaced")
	}
	tp, pp, err := compileOpenBaoInPlaceImagePatches(recorded, set, pod, q.TargetImage)
	if err != nil || len(tp) != 0 {
		return createProviderRecoveryBlock("original OpenBao template or Pod changed")
	}
	main := managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "openbao")
	if main == nil || main["containerID"] != q.ProviderContainerID {
		return createProviderRecoveryBlock("original OpenBao provider restarted")
	}
	if len(pp) > 0 {
		if !fenced() {
			return createProviderRecoveryBlock("original OpenBao execution lease changed")
		}
		if err = openBaoInPlacePatch(ctx, native, ns, "pod", "openbao-0", pp, false); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pod, err = native.Read(ctx, "Pod/"+ns+"/openbao-0")
		if err != nil {
			return err
		}
		if pod == nil || deploymentplan.Map(pod["metadata"])["uid"] != q.PodUID || !fenced() {
			return createProviderRecoveryBlock("original OpenBao Pod or execution fence changed")
		}
		provider := managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "openbao")
		if provider == nil || provider["containerID"] != q.ProviderContainerID {
			return createProviderRecoveryBlock("original OpenBao provider restarted")
		}
		main, host, statusErr := openBaoInPlaceStatuses(pod)
		if statusErr == nil && main == q.ProviderContainerID && host != q.OldHostContainerID {
			set, err = native.Read(ctx, id)
			if err != nil {
				return err
			}
			if _, err = validateOpenBaoInPlaceObservation(q, recorded, set, pod, host); err != nil {
				return err
			}
			if !fenced() {
				return createProviderRecoveryBlock("original OpenBao execution lease changed")
			}
			receipt := openBaoInPlaceReceipt{QualificationSHA256: *review, TemplateStage: stage, TemplatePlanSHA: plan.SHA256(), HostContainerID: host}
			verify := func(ctx context.Context) error {
				_, err := verifyOpenBaoInPlaceReceipt(ctx, cfg, store, e, native, receipt)
				return err
			}
			checkFence := func(context.Context) error {
				if !fenced() {
					return createProviderRecoveryBlock("original OpenBao execution lease changed")
				}
				return nil
			}
			if err = completeOpenBaoInPlaceReceipt(ctx, e, key, receipt, verify, checkFence, time.Now().UTC()); err != nil {
				return err
			}
			if _, err = verifyOpenBaoInPlaceReady(ctx, cfg, store, e, native); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "PASS environment=%s scope=original-openbao-host-image completed=false\n", *env)
			return nil
		}
		if statusErr == nil && main != q.ProviderContainerID {
			return createProviderRecoveryBlock("original OpenBao provider restarted")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
