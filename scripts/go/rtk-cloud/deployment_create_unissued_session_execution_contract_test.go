package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the historical serial Job-first producer, using the maintained
// original bodies. The current producer orders its PVC and ServiceAccount first.
// The fixture selects that old boundary before any first-session publication;
// it never removes an accepted claim, native issuance or credential owner.
func newHistoricalUnissuedSessionExecutionContract(t *testing.T) *acceptedCoreExecutionContract {
	t.Helper()
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooksForSession(t, false))
	hooks.BeforeServiceSession = func(f *acceptedCoreExecutionContract, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, manifest []byte) {
		p := f.p.Plan.Bootstrap
		f.backend = environmentKubernetesBackend{Store: f.store, KubeconfigContent: []byte("clusters:\n- cluster:\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte(runtime.TransportCAPEM)) + "\n")}
		runtime.Execution, runtime.ExecutionSHA256, runtime.Backend = f.e, f.e.Journal.PlanSHA256, f.backend
		runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
		f.runtime, f.service = runtime, service
		f.protocol = map[string]any{"objects": historicalNativeInventory(f.native.objects), "operator": f.operator["PKI_OPERATOR_USER_ID"], "email": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], "password": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"], "issuers": map[string]any{service.Root.ID: service.Root.Document, service.Intermediate.ID: service.Intermediate.Document}, "policy": environmentCreateOpenBaoIssuerPolicies{Environment: f.cfg.Environment, IssuerID: service.Intermediate.ID, ServiceClientSignerPolicy: "path \"pki/isolated/sign/service-client\" { capabilities = [\"update\"] }\n"}, "role": map[string]any{}, "custody": map[string]string{}, "historical_sessions": map[string]managedSessionState{}, "image_manifests": hooks.ImageManifests}
		f.installProtocol(t, manifest)
		installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
		installHistoricalUnissuedSessionCountProtocol(t, f)
		active, err := renderEnvironmentCreateFirstTrustDefinitions(t.Context(), f.cfg, p, runtime, service.Root, service.Intermediate)
		if err != nil {
			t.Fatal(err)
		}
		f.acceptedHistoricalStage(t, "service-active-listeners", active)
		for _, id := range []string{"PersistentVolumeClaim/" + p.Stack + "-video-cloud/pki-service-bootstrap-state", "ServiceAccount/" + p.Stack + "-video-cloud/pki-service-bootstrap"} {
			if f.native.objects[id] != nil {
				t.Fatal("pre-issuance legacy prerequisite was already produced", id)
			}
		}
		subjects, err := environmentCreateBootstrapSubjects(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		stopped := errors.New("historical first Job reached its native deadline without a Pod")
		current, err := createEnvironmentServiceSession(t.Context(), f.cfg, p, f.store, runtime, service, subjects, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
			if stage != "service-session" {
				t.Fatal("unissued session advanced beyond its original stopped Job", stage)
			}
			failed := ""
			for i := range defs {
				defs[i].Dependencies = nil
				defs[i].Phase = "configuration"
				if defs[i].Object["kind"] == "Job" {
					defs[i].Phase, failed = "bootstrap", deploymentplan.ID(defs[i].Object)
				}
			}
			plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/service-session", defs, failed)
			if len(plan.Resources) != 3 || journal.Failed != failed || len(journal.Completed) != 0 || len(journal.Owners) != 1 {
				t.Fatal("actual unissued first-session interruption differs")
			}
			// The external controller observes the deadline with no scheduled Pod.
			// A failed Pod counter would be a different, unsupported checkpoint.
			f.native.objects[failed]["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}, "uncountedTerminatedPods": deploymentplan.Object{}}
			f.protocol["objects"] = historicalNativeInventory(f.native.objects)
			f.publish(t)
			return stopped
		})
		if !errors.Is(err, stopped) {
			t.Fatal("actual first session producer did not stop before issuance", err)
		}
		f.service, f.session = current, current.BootstrapSessionID
		if len(f.protocol["historical_sessions"].(map[string]managedSessionState)) != 0 || len(f.protocol["custody"].(map[string]string)) != 0 {
			t.Fatal("unstarted native Job invented an issuance or custody outcome")
		}
		path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-observed.json")
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("unissued session acquired an observed completion", err)
		}
	}
	return newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
}

func installHistoricalUnissuedSessionCountProtocol(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	if parent == "" {
		t.Fatal("original native protocol missing")
	}
	query := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c 'SELECT count(*) FROM pki_deployment_bootstrap_sessions'"
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys
parent=%q
expected=%q
a=sys.argv[1:]
if a and a[-1]==expected:
 if '--namespace' not in a or a[a.index('--namespace')+1]!=%q or 'exec' not in a or a[a.index('exec')+1]!='postgresql-0' or '--container' not in a or a[a.index('--container')+1]!='postgres':sys.exit('wrong native session-count owner')
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
 print(len(d['historical_sessions']));sys.exit()
os.execv(parent,[parent]+a)
`, parent, query, f.p.Plan.Stack+"-platform")
	path := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHistoricalUnissuedSessionWholeSourceAndNativeRefusals(t *testing.T) {
	f := newHistoricalUnissuedSessionExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, false)
	if err != nil {
		original, journal, sourceErr := originalPendingServicePlan(f.store, f.e, "kubernetes/service-session", 3)
		if sourceErr == nil {
			for _, resource := range original.Resources {
				actual, readErr := f.backend.Read(t.Context(), resource.ID)
				t.Logf("native source kind=%v exists=%t sameOwner=%t sameBody=%t neverRan=%t readError=%v", resource.Desired["kind"], actual != nil, deploymentplan.Map(actual["metadata"])["uid"] == journal.Owners[resource.ID], deploymentplan.Matches(resource.Desired, actual), serviceSessionJobNeverRan(deploymentplan.Map(actual["status"])), readErr)
				if resource.Desired["kind"] == "Job" {
					raw, readErr := f.backend.command(t.Context(), nil, "--namespace", f.p.Plan.Stack+"-video-cloud", "get", "pods", "-l", "job-name="+deploymentplan.String(deploymentplan.Map(actual["metadata"])["name"]), "-o", "json")
					var pods struct {
						Items []json.RawMessage `json:"items"`
					}
					decodeErr := json.Unmarshal(raw, &pods)
					t.Logf("native original Job pods=%d readError=%v decodeError=%v", len(pods.Items), readErr, decodeErr)
				}
			}
		}
		trust, trustJournal, trustErr := acceptedCreateStage(f.store, f.e, "kubernetes/service-active-listeners")
		if trustErr == nil {
			for _, resource := range trust.Resources {
				actual, readErr := f.backend.Read(t.Context(), resource.ID)
				if readErr != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != trustJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
					t.Logf("native active trust differs id=%s exists=%t sameOwner=%t sameBody=%t readError=%v", resource.ID, actual != nil, deploymentplan.Map(actual["metadata"])["uid"] == trustJournal.Owners[resource.ID], deploymentplan.Matches(resource.Desired, actual), readErr)
				}
			}
		}
		query := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c 'SELECT count(*) FROM pki_deployment_bootstrap_sessions'"
		raw, readErr := f.backend.command(t.Context(), nil, "--namespace", f.p.Plan.Stack+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", query)
		t.Logf("native session count=%q readError=%v", strings.TrimSpace(string(raw)), readErr)
		t.Fatal("genuine unissued original session source rejected", err)
	}
	if r.Renewal || r.Intent.SessionID != f.session || r.Candidate == nil || len(r.Candidate.Resources) != 3 || p.Materials["original-service-session-plan"] != r.Original.SHA256() || p.Materials["original-service-session-forward-plan"] != r.Candidate.SHA256() {
		t.Fatal("unissued source lost original intent or exact candidate pins")
	}
	for i, resource := range r.Candidate.Resources {
		if resource.Action != "create" || i < 2 && resource.Desired["kind"] == "Job" || i == 2 && (resource.Desired["kind"] != "Job" || resource.ID == r.Journal.Failed || mqttFoundationEnv(mqttFoundationPrimary(resource.Desired), "PKI_BOOTSTRAP_SESSION_ID") != f.session) {
			t.Fatal("forward did not order absent prerequisites before a distinct same-intent Job")
		}
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("source qualification executed a native write")
	}
	original := f.protocol["objects"].(map[string]deploymentplan.Object)
	for _, mode := range []string{"foreign-job-owner", "pod-executed", "pod-observed", "prerequisite-appeared", "native-session-appeared"} {
		t.Run(mode, func(t *testing.T) {
			objects := make(map[string]deploymentplan.Object, len(original))
			for id, object := range original {
				objects[id] = object
			}
			objects[r.Journal.Failed] = deploymentplan.Clone(objects[r.Journal.Failed])
			f.protocol["objects"] = objects
			f.protocol["historical_sessions"] = map[string]managedSessionState{}
			t.Cleanup(func() {
				f.protocol["objects"] = original
				f.protocol["historical_sessions"] = map[string]managedSessionState{}
				f.publish(t)
			})
			want := "original Service session Job has started, changed, or acquired unqualified state"
			switch mode {
			case "foreign-job-owner":
				deploymentplan.Map(objects[r.Journal.Failed]["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
			case "pod-executed":
				deploymentplan.Map(objects[r.Journal.Failed]["status"])["succeeded"] = float64(1)
				want = "original unstarted Job must reach native DeadlineExceeded"
			case "pod-observed":
				job := deploymentplan.Map(objects[r.Journal.Failed]["metadata"])
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "unexpected-original-job-pod", "namespace": job["namespace"], "uid": "99999999-1111-4111-8111-111111111111", "labels": deploymentplan.Object{"job-name": job["name"]}}, "status": deploymentplan.Object{"phase": "Pending"}}
				objects[deploymentplan.ID(pod)] = pod
			case "prerequisite-appeared":
				for _, resource := range r.Original.Resources {
					if resource.Desired["kind"] == "PersistentVolumeClaim" {
						objects[resource.ID] = deploymentplan.Clone(resource.Desired)
						deploymentplan.Map(objects[resource.ID]["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
					}
				}
			case "native-session-appeared":
				f.protocol["historical_sessions"] = map[string]managedSessionState{"foreign": {Session: "foreign"}}
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if _, err := loadServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, false); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("unissued original source did not reach exact native refusal", mode, err)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused unissued source changed native writes")
			}
		})
	}
	var intent createServiceSessionIntent
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-intent.json"), &intent); err != nil || intent.SessionID != f.session {
		t.Fatal("original preallocated intent changed", err)
	}
}
