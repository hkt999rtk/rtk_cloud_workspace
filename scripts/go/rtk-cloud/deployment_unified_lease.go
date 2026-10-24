package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentExecutionAPI interface {
	Read(context.Context, string) (deploymentplan.Object, error)
	Write(context.Context, string, deploymentplan.Object, bool) (deploymentplan.Object, error)
	DeleteExecutionLease(context.Context, string, string, string) error
}

type environmentExecutionLease struct {
	backend         environmentExecutionAPI
	id, uid, holder string
}

func (l *environmentExecutionLease) Owner() deploymentplan.LeaseOwner {
	return deploymentplan.LeaseOwner{ResourceID: l.id, UID: l.uid, Holder: l.holder}
}

func (b environmentKubernetesBackend) AcquireExecution(ctx context.Context, plan *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	return acquireEnvironmentExecutionLease(ctx, b, plan)
}

// Observe only the selected environment's local execution fence. Interrupted
// owners remain fenced until their original cleanup outcome is verified.
func checkLocalExecutionFence(privateRoot string) error {
	if !filepath.IsAbs(privateRoot) {
		return errors.New("absolute selected environment store required")
	}
	if _, err := os.Lstat(filepath.Join(privateRoot, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "local execution fence exists or cannot be inspected", Next: "inspect the recorded owner and private journal; reconcile actual outcomes before releasing the interrupted execution fence"}
	}
	return nil
}

// A startup guard reads occupancy only. Full qualification still repeats this
// check and exercises admission after the complete target plan is compiled.
func checkEnvironmentExecutionOccupancy(ctx context.Context, b environmentExecutionAPI, plan *deploymentplan.Plan, privateRoot string) error {
	if plan == nil || plan.Stack == "" || plan.Environment == "" || !filepath.IsAbs(privateRoot) {
		return errors.New("selected plan and absolute private environment root required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handoff, ok := createNormalTailHandoffFromContext(ctx); ok {
		if handoff.Execution.Store.Root != privateRoot || plan.CreateParentSHA256 != handoff.Execution.Journal.PlanSHA256 || plan.CreateHandoffSHA256 != deploymentplan.Digest(handoff.Pins) {
			return environmentCreateBlocker("deployment.execution.lease", "continuation does not belong to its original held parent")
		}
		if err := handoff.Execution.Check(ctx); err != nil {
			return err
		}
	} else if err := checkLocalExecutionFence(privateRoot); err != nil {
		return err
	}
	id := "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution"
	current, err := b.Read(ctx, id)
	if err != nil || current != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Resource: id, Cause: "cluster execution lease is occupied or cannot be read", Next: "restore lease read access or reconcile the existing owner's actual outcomes and private journal; elapsed time does not authorize deleting or stealing the lease"}
	}
	return ctx.Err()
}

// Full qualification also exercises create admission without acquiring a lease.
func checkEnvironmentExecutionLease(ctx context.Context, b environmentExecutionAPI, plan *deploymentplan.Plan, privateRoot string) error {
	if err := checkEnvironmentExecutionOccupancy(ctx, b, plan, privateRoot); err != nil {
		return err
	}
	body := environmentExecutionLeaseBody(plan, plan.SHA256()+":preflight")
	admitted, err := b.Write(ctx, "create", body, true)
	if err != nil || admitted == nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(admitted)) {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Resource: deploymentplan.ID(body), Cause: "exact execution lease create admission failed or changed the owner", Next: "repair lease create permission, namespace or admission policy and repeat full preflight; qualification never persists a lease"}
	}
	return nil
}

func checkEnvironmentExecutionAvailable(ctx context.Context, cfg deploymentConfig, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected environment store cannot be resolved", Next: "restore the selected canonical environment store before repeating preflight"}
	}
	// A local interrupted owner needs no Kubernetes access to identify. Stop
	// before loading credentials, preparing storage or resolving CI images.
	if err := checkLocalExecutionFence(store.Root); err != nil {
		if _, inspectErr := inspectCreateRecoveryCheckpoint(cfg, store); inspectErr == nil || operation == "create" {
			return createCheckpointPreflightHint(cfg, store)
		}
		return err
	}
	if operation == "create" {
		return checkEnvironmentCreateTarget(ctx, cfg, store)
	}
	if operation != "upgrade" {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "an explicit supported deployment operation is required", Next: "select create or upgrade for the intended target before repeating preflight"}
	}
	kubeconfig, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("kube", "kubeconfig.yaml"))
	if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected kubeconfig path or private permissions cannot be verified for execution lease checks", Next: "verify the original canonical kubeconfig and real private 0700 directories with a regular 0600 file; local input errors do not establish that the cluster is absent"}
	}
	if _, err := os.Lstat(kubeconfig); errors.Is(err, os.ErrNotExist) {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected environment kubeconfig is missing; the current preflight path requires an existing Kubernetes target", Next: "verify the selected cluster and operation; restore the original canonical kubeconfig only if that cluster still exists; new or removed environments require explicit --operation create qualification; never revive archived cluster identity"}
	} else if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected kubeconfig cannot be inspected for execution lease checks", Next: "verify access to the selected canonical environment store and its original kubeconfig; missing or unreadable local state does not establish that the cluster is absent"}
	}
	raw, err := readBoundedEnvironmentFile(kubeconfig, true)
	if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected kubeconfig exists but cannot be read as a regular private 0600 file", Next: "verify the original canonical kubeconfig, private file permissions and read access before inspecting execution ownership; do not substitute another environment or archived cluster identity"}
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "selected Kubernetes identity cannot be verified for execution lease inspection", Next: "verify the original kubeconfig's embedded identity and selected environment before repeating preflight; unreadable or invalid identity does not establish that the cluster is absent"}
	}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"]}}
	return checkEnvironmentExecutionOccupancy(ctx, backend, plan, store.Root)
}

// Fresh creation has no Kubernetes Lease to inspect. Confirm the selected
// provider target without writing infrastructure, kubeconfig or bootstrap state;
// the direct existing-target compiler cannot represent future Kubernetes owners.
func checkEnvironmentCreateTarget(ctx context.Context, cfg deploymentConfig, store secretStore) error {
	if cfg.Adapter != "lke" || cfg.Environment != store.Environment || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return &deploymentplan.Blocker{ID: "deployment.create.target", Cause: "fresh target discovery requires the selected maintained LKE environment and exact stack", Next: "verify the selected environment and implemented provider adapter before create qualification"}
	}
	tokenPath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("operator", "env", "LINODE_TOKEN"))
	var token []byte
	if err == nil {
		token, err = readBoundedEnvironmentFile(tokenPath, true)
	}
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return &deploymentplan.Blocker{ID: "deployment.create.target", Cause: "selected environment provider credentials cannot be read for fresh target discovery", Next: "restore the selected canonical operator credentials and private permissions; missing kubeconfig or credentials do not prove that the cluster is absent"}
	}
	input := appendMap(cfg.Values, cfg.AdapterResolved)
	values := appendMap(input, deploymentLegacyLKEValues(input, cfg.Environment))
	values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"] = cfg.Environment, cfg.Values["CLOUD_STACK_NAME"]
	// The shared discovery helper honors explicit selectors. Bind those to
	// maintained environment inputs, never ambient selectors for another target.
	restore := installAllCredentialEnvironment(map[string]string{"LKE_CLUSTER_ID": values["LKE_CLUSTER_ID"], "LKE_CLUSTER_LABEL": values["LKE_CLUSTER_LABEL"]})
	defer restore()
	cluster, err := discoverDeploymentPreflightLKECreateTarget(ctx, strings.TrimSpace(string(token)), provisionPaths{EnvRoot: cfg.RuntimeRoot}, values)
	if errors.Is(err, errLKEMissingCluster) {
		return &deploymentplan.Blocker{ID: "deployment.bootstrap.provider-stage", Cause: "selected LKE cluster absence is confirmed; this existing-target compiler cannot compile a fresh staged plan", Next: "use the stage-aware deployment plan or preflight entry with explicit --operation create; retired provision routes are not a fallback"}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return blocker
		}
		return &deploymentplan.Blocker{ID: "deployment.create.target", Cause: "selected cluster absence cannot be verified from provider access, target state and complete inventory", Next: "verify the selected cluster ID or label, canonical provider state, read permission and complete pagination; an inaccessible or mismatched recorded ID cannot become an absent target"}
	}
	return &deploymentplan.Blocker{ID: "deployment.create.target", Resource: strconv.Itoa(cluster.ID), Cause: "selected provider cluster already exists; fresh create does not qualify an existing or replacement cluster", Next: "use upgrade for the matched existing environment, or complete its explicit reviewed bootstrap procedure; operation selection never replaces a cluster or regenerates identity"}
}

func (b environmentKubernetesBackend) CheckExecutionPrerequisites(ctx context.Context, plan *deploymentplan.Plan, privateRoot string) error {
	if err := checkEnvironmentExecutionLease(ctx, b, plan, privateRoot); err != nil {
		return err
	}
	raw, err := b.command(ctx, nil, "auth", "can-i", "delete", "leases.coordination.k8s.io/rtk-deployment-execution", "--namespace", plan.Stack+"-video-cloud")
	if err != nil || strings.TrimSpace(string(raw)) != "yes" {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "execution lease release permission is unavailable", Next: "grant the selected deployment identity lease delete permission in the environment execution namespace and repeat full preflight"}
	}
	return nil
}

func environmentExecutionLeaseBody(plan *deploymentplan.Plan, holder string) deploymentplan.Object {
	return deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"name": "rtk-deployment-execution", "namespace": plan.Stack + "-video-cloud", "labels": map[string]any{"rtk.realtek.com/stack": plan.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}, "annotations": map[string]any{"rtk.realtek.com/plan-sha256": plan.SHA256(), "rtk.realtek.com/environment": plan.Environment}}, "spec": map[string]any{"holderIdentity": holder, "acquireTime": time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")}}
}

func acquireEnvironmentExecutionLease(ctx context.Context, b environmentExecutionAPI, plan *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	if plan == nil || plan.Stack == "" || plan.Environment == "" {
		return nil, errors.New("selected plan required for execution lease")
	}
	namespace, name := plan.Stack+"-video-cloud", "rtk-deployment-execution"
	id := "Lease/" + namespace + "/" + name
	current, err := b.Read(ctx, id)
	if err != nil || current != nil {
		return nil, errors.New("cluster execution lease is occupied or cannot be read; interrupted owners require reviewed recovery")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("execution lease identity unavailable")
	}
	holder := plan.SHA256() + ":" + hex.EncodeToString(nonce)
	body := environmentExecutionLeaseBody(plan, holder)
	admitted, err := b.Write(ctx, "create", body, true)
	if err != nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(admitted)) {
		return nil, errors.New("cluster execution lease admission failed or changed its owner")
	}
	actual, err := b.Write(ctx, "create", body, false)
	if err != nil || actual == nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(actual)) || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) == "" {
		// The API may have persisted creation before its response was lost or
		// cancelled. Inspect only our unpredictable holder with a fresh deadline.
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, readErr := b.Read(cleanup, id)
		if readErr != nil {
			return nil, &deploymentplan.Blocker{ID: "deployment.execution.cleanup", Resource: id, Cause: "failed lease acquisition left an owner that cannot be inspected", Next: "restore API access and verify the original lease owner before repeating preflight; never delete a concurrent owner's lease"}
		}
		if current != nil && deploymentplan.Map(current["spec"])["holderIdentity"] == holder {
			uid := deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"])
			originalUID := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
			if deploymentplan.ID(current) != id || uid == "" || originalUID != "" && originalUID != uid {
				return nil, &deploymentplan.Blocker{ID: "deployment.execution.cleanup", Resource: id, Cause: "failed lease acquisition no longer has a verifiable original owner", Next: "inspect the selected lease and original acquisition outcome; preserve replacement owners before repeating preflight"}
			}
			lease := &environmentExecutionLease{b, id, uid, holder}
			if err := lease.Release(cleanup); err != nil {
				return nil, &deploymentplan.Blocker{ID: "deployment.execution.cleanup", Resource: id, Cause: "the original lease from a failed acquisition could not be released", Next: "restore API access and reconcile the original acquisition outcome before repeating preflight; never delete a concurrent owner's lease"}
			}
		}
		return nil, errors.New("cluster execution lease acquisition could not be confirmed")
	}
	return &environmentExecutionLease{b, id, deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]), holder}, nil
}

func (l *environmentExecutionLease) owned(ctx context.Context) (deploymentplan.Object, error) {
	current, err := l.backend.Read(ctx, l.id)
	if err != nil || current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != l.uid || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(current["spec"])["holderIdentity"] != l.holder {
		return nil, errors.New("exact cluster execution owner cannot be verified")
	}
	return current, nil
}
func (l *environmentExecutionLease) Check(ctx context.Context) error {
	_, err := l.owned(ctx)
	return err
}
func (l *environmentExecutionLease) Release(ctx context.Context) error {
	current, err := l.owned(ctx)
	if err != nil {
		return err
	}
	version := deploymentplan.String(deploymentplan.Map(current["metadata"])["resourceVersion"])
	if version == "" {
		return errors.New("execution lease version unavailable")
	}
	if err := l.backend.DeleteExecutionLease(ctx, l.id, l.uid, version); err != nil {
		return errors.New("exact execution lease release failed")
	}
	actual, err := l.backend.Read(ctx, l.id)
	if err != nil || actual != nil {
		return errors.New("execution lease absence could not be confirmed; concurrent owner must not be removed")
	}
	return nil
}

func (b environmentKubernetesBackend) DeleteExecutionLease(ctx context.Context, id, uid, version string) error {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] != "Lease" || parts[1] == "" || parts[2] != "rtk-deployment-execution" || uid == "" || version == "" {
		return errors.New("exact execution lease identity required")
	}
	path := "/apis/coordination.k8s.io/v1/namespaces/" + url.PathEscape(parts[1]) + "/leases/" + url.PathEscape(parts[2])
	body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": uid, "resourceVersion": version}})
	_, err := b.command(ctx, body, "delete", "--raw="+path, "-f", "-")
	return err
}
