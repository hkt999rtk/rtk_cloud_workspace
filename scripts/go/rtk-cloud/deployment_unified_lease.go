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

// Qualification observes occupancy and exercises create admission without
// acquiring a lease. A prior attempt remains owned even after its timeout.
func checkEnvironmentExecutionLease(ctx context.Context, b environmentExecutionAPI, plan *deploymentplan.Plan, privateRoot string) error {
	if plan == nil || plan.Stack == "" || plan.Environment == "" || !filepath.IsAbs(privateRoot) {
		return errors.New("selected plan and absolute private environment root required")
	}
	if _, err := os.Lstat(filepath.Join(privateRoot, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Cause: "local execution fence exists or cannot be inspected", Next: "inspect the recorded owner and private journal; reconcile actual outcomes before releasing the interrupted execution fence"}
	}
	id := "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution"
	current, err := b.Read(ctx, id)
	if err != nil || current != nil {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Resource: id, Cause: "cluster execution lease is occupied or cannot be read", Next: "restore lease read access or reconcile the existing owner's actual outcomes and private journal; elapsed time does not authorize deleting or stealing the lease"}
	}
	body := environmentExecutionLeaseBody(plan, plan.SHA256()+":preflight")
	admitted, err := b.Write(ctx, "create", body, true)
	if err != nil || admitted == nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(admitted)) {
		return &deploymentplan.Blocker{ID: "deployment.execution.lease", Resource: id, Cause: "exact execution lease create admission failed or changed the owner", Next: "repair lease create permission, namespace or admission policy and repeat full preflight; qualification never persists a lease"}
	}
	return nil
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
		return nil, errors.New("cluster execution lease acquisition could not be confirmed; inspect the selected lease before retrying")
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
