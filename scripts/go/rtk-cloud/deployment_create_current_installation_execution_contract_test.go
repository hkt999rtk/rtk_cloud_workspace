package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newCurrentInstallationExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	mqtt := newPostServiceMQTTExecutionContract(t)
	f := mqtt.acceptedCoreExecutionContract
	f.stage(t, "kubernetes/mqtt-intermediate-trust", mqtt.definitions)
	renewed := f.service
	renewed.BootstrapSessionID = sessionRenewalUUID(f.e.Journal.PlanSHA256, mqtt.original.SHA256(), f.session)
	renewed.BootstrapCredentialSecret = "service-bootstrap-" + renewed.BootstrapSessionID
	defs, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, renewed)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for _, d := range defs {
		if strings.HasPrefix(deploymentplan.ID(d.Object), "Job/"+f.p.Plan.Stack+"-video-cloud/pki-enroll-video-cloud-api-client-") {
			failed = deploymentplan.ID(d.Object)
		}
	}
	review := deploymentplan.Digest("isolated admitted current installation")
	key := "kubernetes/application-current-installation-renewal-" + review
	original, j := f.interruptedStage(t, key, defs, failed)
	if !knownCurrentInstallationFailure(original, j, f.p.Plan.Stack) {
		t.Fatalf("not original 9-complete/10-owner failure: completed=%d owners=%d failed=%s", len(j.Completed), len(j.Owners), j.Failed)
	}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	return f, &createServiceSessionRecovery{CoreAccepted: true, PostServiceResume: true, InstallationForward: true, InstallationKey: key, InstallationPlan: original, InstallationJournal: j, Ceremony: renewed, Review: review}
}

// This adapter executes the same immutable child plan and durable parent
// transaction used by production. Only Kubernetes effects use an in-memory
// native endpoint; accepted resources and all parent/child leases remain real.
func currentInstallationContractApply(t *testing.T, f *acceptedCoreExecutionContract, candidates map[string]*deploymentplan.Plan) environmentCreateBootstrapApply {
	t.Helper()
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		candidate := candidates[stage]
		if candidate == nil {
			return fmt.Errorf("unexpected installation stage %s", stage)
		}
		var live []deploymentplan.Object
		for _, o := range f.native.objects {
			live = append(live, o)
		}
		fresh, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, live)
		if err != nil {
			return err
		}
		wanted := map[string]string{}
		for _, v := range candidate.Resources {
			wanted[v.ID] = deploymentplan.Digest(v.Desired)
		}
		for _, v := range fresh.Resources {
			if wanted[v.ID] != deploymentplan.Digest(v.Desired) {
				return fmt.Errorf("qualified installation body changed %s", v.ID)
			}
			delete(wanted, v.ID)
		}
		if len(wanted) != 0 {
			return fmt.Errorf("qualified installation resource omitted")
		}
		frozen, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil {
			return err
		}
		if frozen == nil {
			frozen = fresh
		}
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
		accept := func(ctx context.Context, p *deploymentplan.Plan) error {
			for _, v := range p.Resources {
				o, err := f.native.Read(ctx, v.ID)
				if err != nil || o == nil || !deploymentplan.Matches(v.Desired, o) || v.UID != "" && deploymentplan.Map(o["metadata"])["uid"] != v.UID {
					return fmt.Errorf("native original owner not accepted: %s", v.ID)
				}
			}
			return f.e.Check(ctx)
		}
		if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, frozen, f.native, check, check, accept, f.e.Check, io.Discard); err != nil {
			return err
		}
		f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
		f.publish(t)
		return nil
	}
}

func TestCurrentInstallationExecutionContractQualifiesOnlyUnfinishedJobsAndOwnerQuiesce(t *testing.T) {
	f, r := newCurrentInstallationExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	if err := prepareCurrentInstallationForward(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal(err)
	}
	if len(r.QuiesceCandidate.Resources) != 2 || len(r.InstallationCandidate.Resources) != 3 {
		t.Fatal("repair did not preserve exact unfinished cohort")
	}
	for _, v := range r.QuiesceCandidate.Resources {
		if v.Action != "update" || deploymentplan.Map(v.Desired["spec"])["replicas"] != float64(0) {
			t.Fatal("original consumer not exclusively quiesced", v.ID, v.Action)
		}
	}
	for _, v := range r.InstallationCandidate.Resources {
		if v.Action != "create" || currentInstallationContains(r.InstallationJournal.Completed, v.ID) || v.ID == r.InstallationJournal.Failed {
			t.Fatal("repair replayed finished/refused original enrollment", v.ID)
		}
	}
	if p.Materials["accepted-service-core-current-installation-forward-plan"] != r.InstallationCandidate.SHA256() || p.Materials["accepted-service-core-current-owner-quiesce-plan"] != r.QuiesceCandidate.SHA256() {
		t.Fatal("qualification did not bind exact candidate")
	}
	used := *r
	if err := prepareUsedInstallationQuiesce(context.Background(), f.cfg, f.e, f.backend, &p, &used); err != nil {
		t.Fatal("used-session quiesce could not retain original MQTT owners", err)
	}
	if len(used.QuiesceCandidate.Resources) != 2 {
		t.Fatal("used-session quiesce lost owners")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestCurrentInstallationExecutionContractRejectsChangedOwnerAndUnwrittenJob(t *testing.T) {
	for _, mode := range []string{"consumer-owner", "refused-job-owner", "unwritten-job-exists", "forward-job-exists"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newCurrentInstallationExecutionContract(t)
			objects := f.protocol["objects"].(map[string]deploymentplan.Object)
			switch mode {
			case "consumer-owner":
				deploymentplan.Map(objects["Deployment/"+f.p.Plan.Stack+"-video-cloud/video-cloud-api"]["metadata"])["uid"] = "other-owner"
			case "refused-job-owner":
				deploymentplan.Map(objects[r.InstallationJournal.Failed]["metadata"])["uid"] = "other-job"
			case "unwritten-job-exists":
				for _, v := range r.InstallationPlan.Resources {
					if v.Desired["kind"] == "Job" && r.InstallationJournal.Owners[v.ID] == "" {
						o := deploymentplan.Clone(v.Desired)
						deploymentplan.Map(o["metadata"])["uid"] = "unrecorded-job"
						objects[v.ID] = o
						break
					}
				}
			case "forward-job-exists":
				for _, v := range r.InstallationPlan.Resources {
					if v.ID == r.InstallationJournal.Failed {
						o := deploymentplan.Clone(v.Desired)
						m := deploymentplan.Map(o["metadata"])
						m["name"] = deploymentplan.String(m["name"]) + "-forward"
						m["uid"] = "unrecorded-forward"
						objects[deploymentplan.ID(o)] = o
					}
				}
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareCurrentInstallationForward(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err == nil {
				t.Fatal("changed installation boundary accepted", mode)
			}
			if p.Materials["accepted-service-core-current-installation-forward-plan"] != "" {
				t.Fatal("changed source bound executable installation")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestCurrentInstallationExecutionContractWaitsForIdentityReleaseAndResumesWithoutReplay(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupt), func(t *testing.T) {
			f, r := newCurrentInstallationExecutionContract(t)
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareCurrentInstallationForward(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
				t.Fatal(err)
			}
			originalWrites := deploymentplan.Digest(f.native.writes)
			original := map[string]string{}
			for id, o := range f.native.objects {
				original[id] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
			}
			originalKubectl := os.Getenv("RTK_CLOUD_KUBECTL")
			wrapper := filepath.Join(t.TempDir(), "kubectl")
			script := `#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:]
if 'wait' not in args:os.execv(os.environ['RTK_INSTALLATION_NATIVE'],[os.environ['RTK_INSTALLATION_NATIVE']]+args)
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as f:d=json.load(f)
if '--for=delete' not in args or 'pod' not in args or '--selector' not in args:sys.exit('unexpected wait protocol')
selector=args[args.index('--selector')+1];pairs=[part.split('=',1) for part in selector.split(',')]
ns=args[args.index('--namespace')+1]
matched=[o for o in d['objects'].values() if o['kind']=='Deployment' and o['metadata'].get('namespace')==ns and all(o['spec'].get('template',{}).get('metadata',{}).get('labels',{}).get(key)==value for key,value in pairs)]
if len(matched)!=1 or matched[0]['spec'].get('replicas')!=0:sys.exit('original workload has not stopped')
if d.get('terminating_identity_pod',False):sys.exit('original Pod still terminating')
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_INSTALLATION_NATIVE", originalKubectl)
			t.Setenv("RTK_CLOUD_KUBECTL", wrapper)
			t.Setenv("PATH", filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
			f.protocol["terminating_identity_pod"] = interrupt
			f.publish(t)
			apply := currentInstallationContractApply(t, f, map[string]*deploymentplan.Plan{"current-owner-quiesce-" + r.Review: r.QuiesceCandidate, "current-installation-lock-forward-" + r.Review: r.InstallationCandidate})
			runtime := f.runtime
			runtime.ServiceSessionRecovery = r
			runtime.Backend = f.backend
			runtime.Backend.RolloutTimeout = time.Minute
			err := executeCurrentInstallationForward(context.Background(), f.cfg, p.Plan.Bootstrap, f.store, runtime, apply)
			if interrupt {
				if err == nil {
					t.Fatal("enrollment ran while old identity Pod was terminating")
				}
				for _, v := range r.InstallationCandidate.Resources {
					if f.native.objects[v.ID] != nil {
						t.Fatal("unfinished enrollment ran before identity release")
					}
				}
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, r.InstallationKey)
				if entry == nil || entry.State != "pending" {
					t.Fatal("unfinished original installation was prematurely completed")
				}
				f.protocol["terminating_identity_pod"] = false
				f.publish(t)
				err = executeCurrentInstallationForward(context.Background(), f.cfg, p.Plan.Bootstrap, f.store, runtime, apply)
			}
			if err != nil {
				t.Fatal("original installation forward did not finish", err)
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, r.InstallationKey)
			var receipt map[string]string
			if entry == nil || entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["original_failed_plan_sha256"] != r.InstallationPlan.SHA256() || receipt["renewed_session_id"] != r.Ceremony.BootstrapSessionID {
				t.Fatal("original failed installation lost qualified completion")
			}
			for _, v := range r.InstallationCandidate.Resources {
				if f.native.writes[v.ID] != 1 {
					t.Fatal("forward replayed enrollment", v.ID)
				}
			}
			for id, uid := range original {
				if deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != uid {
					t.Fatal("repair replaced existing owner", id)
				}
			}
			if originalWrites == deploymentplan.Digest(f.native.writes) {
				t.Fatal("execution never applied repair")
			}
		})
	}
}
