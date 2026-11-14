package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

// This scoped handoff updates only a public CA bundle, preserves both existing
// authorities during transition, and resumes the recorded native stage.
func runCreateOpenBaoPublicCAHandoff(args []string) error {
	fs := flag.NewFlagSet("recover-create-openbao-ca", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	execute := fs.Bool("execute", false, "apply the validated public CA overlap")
	review := fs.String("plan-sha256", "", "optional reviewed scoped plan digest")
	timeout := fs.Duration("timeout", 10*time.Minute, "operation timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || *timeout <= 0 || (*review != "" && (!*execute || !environmentPreflightDigest.MatchString(*review))) {
		return createProviderRecoveryBlock("select the environment and a positive timeout")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil || !environmentPreflightDigest.MatchString(checkpoint.Owner.PlanSHA256) {
		return createProviderRecoveryBlock("original create generation is unavailable")
	}
	e, original, err := loadActiveTransportExecution(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(checkpoint.Journal) || deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 || deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("original scoped repair settings or owner changed")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original Kubernetes identity changed")
	}
	native, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	b := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	_, _, _, controller, err := staleControllerRecordedSource(ctx, store, e, b)
	if err != nil {
		return err
	}
	c := managedNamedField(environmentPKIPod(controller.Desired)["containers"], "pki-controller")
	pin, name, prefix := mqttFoundationEnv(c, "PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256"), mqttFoundationEnv(c, "PKI_STATE_OPENBAO_SERVER_PKI_NAME"), mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
	if !environmentPreflightDigest.MatchString(pin) || name == "" || !strings.HasPrefix(prefix, *env+"/") {
		return createProviderRecoveryBlock("recorded Controller managed authority is unavailable")
	}
	pod := environmentPKIPod(controller.Desired)
	mount := managedNamedField(c["volumeMounts"], "pki-custody-trust")
	volume := managedNamedField(pod["volumes"], "pki-custody-trust")
	trustName := deploymentplan.String(deploymentplan.Map(volume["configMap"])["name"])
	if mount == nil || trustName == "" || mqttFoundationEnv(c, "PKI_STATE_OPENBAO_TLS_CA") != deploymentplan.String(mount["mountPath"])+"/managed-root.pem" {
		return createProviderRecoveryBlock("original managed custody CA mount differs")
	}
	ns := e.Journal.Stack + "-video-cloud"
	trust, err := native.Read(ctx, "ConfigMap/"+ns+"/"+trustName)
	if err != nil || trust == nil {
		return createProviderRecoveryBlock("recorded managed public authority is unavailable")
	}
	data := deploymentplan.Map(trust["data"])
	managed := deploymentplan.String(data["managed-root.pem"])
	var snapshot environmentCustodyTransportTrust
	pieces := strings.Split(prefix, "/")
	if decodeEnvironmentJSON([]byte(deploymentplan.String(data["transport.json"])), &snapshot) != nil || len(pieces) < 3 || snapshot.Environment != *env || snapshot.Generation != pieces[1] || snapshot.ServerName != name || snapshot.Schema != "rtk.custody.transport/v1" || snapshot.ManagedRootSHA256 != pin || strings.TrimSpace(snapshot.ManagedPEM) != strings.TrimSpace(managed) || !snapshot.ManagedOnly {
		return createProviderRecoveryBlock("managed custody snapshot does not bind the original generation")
	}
	foundation, fj, err := acceptedCreateStage(store, e, "kubernetes/foundation")
	if err != nil {
		return err
	}
	id := "ConfigMap/" + ns + "/pki-openbao-transport-ca"
	var source deploymentplan.Resource
	for _, r := range foundation.Resources {
		if r.ID == id {
			source = r
		}
	}
	uid := fj.Owners[id]
	if source.ID == "" || uid == "" {
		return createProviderRecoveryBlock("original public CA owner is unavailable")
	}
	completedWrites, err := completedInitialTransportSuccessorOriginalResources(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	source.Desired, err = advanceOpenBaoCASource(source.Desired, uid, completedWrites)
	if err != nil {
		return err
	}
	live, err := native.Read(ctx, id)
	if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid {
		return createProviderRecoveryBlock("original public CA owner changed")
	}
	originalData := deploymentplan.Clone(deploymentplan.Map(source.Desired["data"]))
	liveData := deploymentplan.Clone(deploymentplan.Map(live["data"]))
	if originalData == nil || liveData == nil {
		return createProviderRecoveryBlock("original public CA data is unavailable")
	}
	old := deploymentplan.String(originalData["ca.crt"])
	current := deploymentplan.String(liveData["ca.crt"])
	delete(originalData, "ca.crt")
	delete(liveData, "ca.crt")
	if deploymentplan.Digest(originalData) != deploymentplan.Digest(liveData) {
		return createProviderRecoveryBlock("unrelated original public CA data changed")
	}
	bundle, err := openBaoCAOverlap(old, managed, current, pin, time.Now().UTC())
	if err != nil {
		return err
	}
	// Validate the public chain file selected by the original OpenBao listener.
	baoNS := e.Journal.Stack + "-secrets"
	sts, err := native.Read(ctx, "StatefulSet/"+baoNS+"/openbao")
	if err != nil || sts == nil {
		return createProviderRecoveryBlock("original OpenBao listener is unavailable")
	}
	bp := environmentPKIPod(sts)
	fileVolume := managedNamedField(bp["volumes"], "file-config")
	configName := deploymentplan.String(deploymentplan.Map(fileVolume["configMap"])["name"])
	conf, err := native.Read(ctx, "ConfigMap/"+baoNS+"/"+configName)
	if err != nil || conf == nil {
		return createProviderRecoveryBlock("original OpenBao listener configuration is unavailable")
	}
	re := regexp.MustCompile(`(?m)^\s*tls_cert_file\s*=\s*"(/[^"\r\n]+)"\s*$`)
	path := ""
	for _, v := range deploymentplan.Map(conf["data"]) {
		for _, m := range re.FindAllStringSubmatch(deploymentplan.String(v), -1) {
			if path != "" {
				return createProviderRecoveryBlock("OpenBao listener certificate path is ambiguous")
			}
			path = m[1]
		}
	}
	if path == "" || filepath.Clean(path) != path || (filepath.Base(path) != "chain.pem" && filepath.Base(path) != "cert.pem" && filepath.Base(path) != "tls.crt") {
		return createProviderRecoveryBlock("OpenBao listener public certificate path is unavailable")
	}
	chain, err := native.command(ctx, nil, "--namespace", baoNS, "exec", "statefulset/openbao", "--container", "openbao", "--", "cat", path)
	if err != nil {
		return createProviderRecoveryBlock("OpenBao listener public certificate cannot be read")
	}
	if err = validateOpenBaoListenerFile(string(chain), managed, name, time.Now().UTC()); err != nil {
		return err
	}
	def, err := openBaoCAOverlapDefinition(source.Desired, uid, managed, pin, time.Now().UTC())
	if err != nil {
		return err
	}
	if deploymentplan.Map(def.Object["data"])["ca.crt"] != bundle {
		return createProviderRecoveryBlock("public CA transition recipe differs")
	}
	stage := "openbao-public-ca-overlap-" + pin[:12]
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return err
	}
	if plan == nil {
		in := foundation.Inputs
		in.Operation = "scoped"
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, []deploymentplan.Definition{def})
		plan, err = deploymentplan.Compile(in, []deploymentplan.Definition{def}, []deploymentplan.Object{live})
		if err != nil {
			return err
		}
	} else if len(plan.Resources) != 1 || plan.Resources[0].ID != id || plan.Resources[0].UID != uid || !deploymentplan.Matches(def.Object, plan.Resources[0].Desired) {
		return createProviderRecoveryBlock("saved public CA handoff differs from original")
	}
	if *review != "" && plan.SHA256() != *review {
		return createProviderRecoveryBlock("reviewed public CA plan changed")
	}
	if !*execute {
		if err = plan.Admit(ctx, b); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "plan-sha256: %s\nGO environment=%s scope=public-openbao-ca-overlap completed=false\n", plan.SHA256(), *env)
		return nil
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, store.Root) }
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if e.Check(ctx) != nil {
			return createProviderRecoveryBlock("original parent owner changed")
		}
		return b.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
	if err = executeEnvironmentCreateStagePlan(ctx, store, e, stage, plan, b, qualify, fence, accept, func(context.Context) error { return nil }, os.Stdout); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "PASS environment=%s scope=public-openbao-ca-overlap completed=false\n", *env)
	return nil
}
