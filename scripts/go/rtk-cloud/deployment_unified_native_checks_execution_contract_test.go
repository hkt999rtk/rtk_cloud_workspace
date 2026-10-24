package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A configuration-only upgrade still needs real current capacity, permissions,
// storage and lease admission checks. Only the kubectl protocol is modeled;
// these tests call the default preflight and execution wiring unchanged.
func TestUnifiedNativeChecksExecutionContractRechecksCurrentPrerequisites(t *testing.T) {
	for _, phase := range []string{"preflight", "execution"} {
		for _, mode := range []string{"original", "node-unready", "permission-denied", "lease-occupied", "lease-admission-drift", "cancelled"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				c := fenceDiagnosticFixture(t)
				// This bounded plan changes no service image or database schema.
				c.Release.Images = map[string]environmentReleaseImage{"LKE_POSTGRES_IMAGE": c.Release.Images["LKE_POSTGRES_IMAGE"]}
				c.Plan.ReleaseSHA256 = deploymentplan.Digest(c.Release)
				plan, err := deploymentplan.Compile(c.Plan.Inputs, []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "selected-public-policy", "namespace": c.Plan.Stack + "-video-cloud"}, "data": deploymentplan.Object{"policy": "original reviewed configuration"}}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				plan.StorageSHA256 = c.Plan.StorageSHA256
				c.Plan = plan
				var nodes []deploymentplan.Object
				for _, class := range []string{"general", "broker", "database"} {
					intent := c.Configuration.Capacity.NodeClasses[class]
					for i := 0; i < intent.EffectiveCount; i++ {
						nodes = append(nodes, deploymentplan.Object{"apiVersion": "v1", "kind": "Node", "metadata": deploymentplan.Object{"name": fmt.Sprintf("%s-%d", class, i), "uid": fmt.Sprintf("original-%s-%d", class, i), "labels": deploymentplan.Object{c.Configuration.Values["NODE_CLASS_LABEL_KEY"]: class, "kubernetes.io/arch": "amd64", "kubernetes.io/os": "linux"}}, "spec": deploymentplan.Object{}, "status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "allocatable": deploymentplan.Object{"cpu": "128", "memory": "256Gi"}}})
					}
				}
				if len(nodes) == 0 {
					t.Fatal("fixture omitted the maintained capacity plan")
				}
				if mode == "node-unready" {
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(nodes[0]["status"])["conditions"])[0])["status"] = "False"
				}
				bin := t.TempDir()
				state := filepath.Join(bin, "nodes.json")
				calls := filepath.Join(bin, "calls")
				if err := os.WriteFile(state, mustProviderRecoveryJSON(deploymentplan.Object{"items": nodes}), 0600); err != nil {
					t.Fatal(err)
				}
				script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
with open(os.environ['RTK_NATIVE_CHECK_CALLS'],'a') as log: log.write(json.dumps(args)+'\n')
mode=os.environ['RTK_NATIVE_CHECK_MODE']
if 'create' in args:
    body=json.load(sys.stdin)
    if body.get('kind')=='SelfSubjectAccessReview':
        print(json.dumps({'status':{'allowed':mode!='permission-denied','denied':mode=='permission-denied'}}))
    elif '--dry-run=server' in args:
        if body.get('kind')=='Lease' and mode=='lease-admission-drift': body['spec']['holderIdentity']='foreign'
        print(json.dumps(body))
    else:
        sys.stderr.write('unexpected persistent native write'); sys.exit(2)
elif 'get' in args:
    pos=args.index('get'); kind=args[pos+1]
    if kind=='nodes': print(open(os.environ['RTK_NATIVE_CHECK_NODES']).read())
    elif kind.lower()=='lease':
        if mode=='lease-occupied': print(json.dumps({'apiVersion':'coordination.k8s.io/v1','kind':'Lease','metadata':{'name':'rtk-deployment-execution','uid':'foreign-original'},'spec':{'holderIdentity':'foreign'}}))
    elif len(args)>pos+2 and not args[pos+2].startswith('-'): pass
    else: print('{"items":[]}')
elif 'auth' in args and 'can-i' in args: print('yes')
else:
    sys.stderr.write('unexpected native protocol'); sys.exit(2)
`
				if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
				t.Setenv("RTK_NATIVE_CHECK_CALLS", calls)
				t.Setenv("RTK_NATIVE_CHECK_MODE", mode)
				t.Setenv("RTK_NATIVE_CHECK_NODES", state)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				checks := defaultUnifiedDeploymentDependencies().Checks(c)
				if phase == "execution" {
					checks = executionEnvironmentChecks(c)
				}
				before := c.Plan.SHA256()
				evidence, err := checks["deployment.execution"](ctx, c.Plan)
				if (err == nil) != (mode == "original") {
					t.Fatal("default current-prerequisite wiring lost a required check", err)
				}
				if mode == "original" && evidence.SHA256 != before {
					t.Fatal("current prerequisites were not bound to the selected immutable plan")
				}
				if mode == "cancelled" {
					if _, err := os.Lstat(calls); !os.IsNotExist(err) {
						t.Fatal("cancelled prerequisite check reached a native protocol", err)
					}
				}
				if mode != "original" && mode != "cancelled" {
					var blocker *deploymentplan.Blocker
					want := map[string]string{"node-unready": "deployment.execution.node-capacity", "permission-denied": "deployment.execution.permissions", "lease-occupied": "deployment.execution.lease", "lease-admission-drift": "deployment.execution.lease"}[mode]
					if !errors.As(err, &blocker) || blocker.ID != want {
						t.Fatal("current prerequisite failed at the wrong boundary", err)
					}
				}
				if c.Plan.SHA256() != before {
					t.Fatal("current checks rerendered or changed the qualified plan")
				}
				if mode == "original" {
					raw, err := os.ReadFile(calls)
					if err != nil || !strings.Contains(string(raw), "nodes") || !strings.Contains(string(raw), "selfsubjectaccessreviews") || !strings.Contains(string(raw), "--dry-run=server") || !strings.Contains(string(raw), "can-i") {
						t.Fatal("current checks omitted native capacity, RBAC or lease admission", err)
					}
				}
			})
		}
	}
}

// These default gates still fence all selected input files for an upgrade that
// has no service executable or schema changes.
func TestUnifiedNativeChecksExecutionContractFencesConfigurationOnlyUpgrade(t *testing.T) {
	for _, gate := range []string{"deployment.target", "deployment.compatibility"} {
		t.Run(gate, func(t *testing.T) {
			c := fenceDiagnosticFixture(t)
			c.Release.Images = map[string]environmentReleaseImage{"LKE_POSTGRES_IMAGE": c.Release.Images["LKE_POSTGRES_IMAGE"]}
			c.Plan.ReleaseSHA256 = deploymentplan.Digest(c.Release)
			checks := defaultUnifiedDeploymentDependencies().Checks(c)
			evidence, err := checks[gate](t.Context(), c.Plan)
			if err != nil || evidence.SHA256 != c.Plan.SHA256() {
				t.Fatal("configuration-only default gate requires an unrelated producer", err)
			}
			before := c.Plan.SHA256()
			if err := c.Store.write("runtime/postgres", []byte("changed-original-runtime-input"), true); err != nil {
				t.Fatal(err)
			}
			if _, err := checks[gate](t.Context(), c.Plan); err == nil {
				t.Fatal("default gate reused stale selected configuration evidence")
			}
			if c.Plan.SHA256() != before {
				t.Fatal("failed gate changed the original target")
			}
		})
	}
}
