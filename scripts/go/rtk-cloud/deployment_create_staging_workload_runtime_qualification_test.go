package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func stagingRuntimeQualificationResources() []deploymentplan.Resource {
	stack := "video-cloud-staging"
	deployment := func(c deploymentplan.Object, volumes ...any) deploymentplan.Object {
		return deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}, "volumes": volumes}}}
	}
	objects := []deploymentplan.Object{
		stagingQualificationObject("PersistentVolumeClaim", stack+"-logger", "cloud-logger-billing-inbox", "", deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}),
		stagingQualificationObject("Deployment", stack+"-logger", "cloud-logger", "", deployment(deploymentplan.Object{"name": "app", "args": []any{"-addr", ":18090", "-billing-inbox", "/var/lib/rtk-cloud-logger/billing-inbox.db"}, "volumeMounts": []any{deploymentplan.Object{"name": "billing-inbox", "mountPath": "/var/lib/rtk-cloud-logger"}}}, deploymentplan.Object{"name": "billing-inbox", "persistentVolumeClaim": deploymentplan.Object{"claimName": "cloud-logger-billing-inbox"}})),
		stagingQualificationObject("Deployment", stack+"-video-cloud", "video-cloud-api", "", deployment(deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": "staging"}}})),
		stagingQualificationObject("Ingress", stack+"-ingress", "public", "", deploymentplan.Object{}),
	}
	var resources []deploymentplan.Resource
	for i, o := range objects {
		phase := "workload"
		if i == 0 {
			phase = "storage"
		}
		if i == 3 {
			phase = "routing"
		}
		resources = append(resources, deploymentplan.Resource{ID: deploymentplan.ID(o), Action: "create", Phase: phase, Desired: o})
	}
	return resources
}

func stagingRuntimeQualificationFixture(t *testing.T) *stagingWorkloadQualificationFixture {
	t.Helper()
	f := newStagingWorkloadQualificationFixture(t, stagingRuntimeQualificationResources())
	for i, r := range f.child.Resources {
		if r.Phase == "routing" {
			continue
		}
		f.journal.Completed = append(f.journal.Completed, r.ID)
		f.journal.Owners[r.ID] = []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
	}
	f.journal.Failed = f.child.Resources[2].ID
	f.saveProgress(t, f.child, f.journal)
	return f
}

func (f *stagingWorkloadQualificationFixture) acceptDefinitions(t *testing.T, stage string, defs []deploymentplan.Definition) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	p := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: f.child.Inputs}
	p.BindingsSHA256 = deploymentplan.Digest(stage)
	j := deploymentplan.Journal{Owners: map[string]string{}, Accepted: true, AcceptanceStarted: true}
	for _, d := range defs {
		id := deploymentplan.ID(d.Object)
		uid := f.journal.Owners[id]
		if uid == "" {
			uid = "44444444-4444-4444-8444-444444444444"
		}
		action := "update"
		if d.Retain {
			action = "retain"
		}
		p.Resources = append(p.Resources, deploymentplan.Resource{ID: id, UID: uid, Action: action, Phase: d.Phase, Desired: d.Object})
		j.Completed = append(j.Completed, id)
		j.Owners[id] = uid
	}
	j.PlanSHA256 = p.SHA256()
	f.persistStage(t, stage, p)
	f.saveProgress(t, p, j)
	if err := f.e.RecordReceipt("kubernetes/"+stage, mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": p.SHA256(), "owners": j.Owners})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/"+stage, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return p, j
}

func TestStagingRuntimeQualificationFinishesOriginalParentsAfterAcceptedChildren(t *testing.T) {
	for _, mode := range []string{"pending-parents", "allocation-receipt-recorded", "runtime-receipt-recorded"} {
		t.Run(mode, func(t *testing.T) {
			f := stagingRuntimeQualificationFixture(t)
			initial, steady, err := stagingWorkloadRuntimeDefinitions(f.child, f.journal)
			if err != nil {
				t.Fatal(err)
			}
			initStage := stagingLoggerInitPrefix + f.child.SHA256()[:12]
			runtimeStage := stagingWorkloadRuntimePrefix + f.child.SHA256()[:12]
			f.acceptDefinitions(t, initStage, initial)
			accepted, progress := f.acceptDefinitions(t, runtimeStage, steady)
			if mode != "pending-parents" {
				stage, original := f.stage, f.child
				if mode == "runtime-receipt-recorded" {
					stage, original = "workload", f.original
				}
				receipt := stagingWorkloadAllocationReceipt{accepted.SHA256(), progress.Owners, "kubernetes/" + runtimeStage, original.SHA256()}
				if err := f.e.RecordReceipt("kubernetes/"+stage, mustProviderRecoveryJSON(receipt)); err != nil {
					t.Fatal(err)
				}
			}
			originalBytes, err := os.ReadFile(environmentCreateStageInputPath(f.e, "workload"))
			if err != nil {
				t.Fatal(err)
			}
			priorBytes, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", f.child.SHA256(), "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			backend := stagingQualificationBackend(t, f.store, map[string]deploymentplan.Object{})
			if err := applyStagingWorkloadRuntimeForward(t.Context(), f.cfg, f.store, &f.parent, f.e, f.stage, f.original, f.child, f.journal, backend); err != nil {
				t.Fatal("accepted children could not complete original parents", err)
			}
			for _, stage := range []string{"workload", f.stage} {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stage)
				if entry == nil || entry.State != "completed" {
					t.Fatalf("parent %s not completed", stage)
				}
			}
			selected, err := freshCreateWorkloadStage(f.store, f.e)
			if err != nil || selected != runtimeStage {
				t.Fatalf("resumed wrong workload stage %q: %v", selected, err)
			}
			checkpoint, j, err := readStagingWorkloadAllocationCheckpoint(f.store, f.e)
			if err != nil || checkpoint.SHA256() != f.child.SHA256() || deploymentplan.Digest(j) != deploymentplan.Digest(f.journal) {
				t.Fatal("runtime supersession lost exact original checkpoint", err)
			}
			afterOriginal, _ := os.ReadFile(environmentCreateStageInputPath(f.e, "workload"))
			afterPrior, _ := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", f.child.SHA256(), "journal.json"))
			if string(afterOriginal) != string(originalBytes) || string(afterPrior) != string(priorBytes) {
				t.Fatal("runtime recovery rewrote historical inputs or known writes")
			}
			before := deploymentplan.Digest(f.e.Journal)
			if err := applyStagingWorkloadRuntimeForward(t.Context(), f.cfg, f.store, &f.parent, f.e, f.stage, f.original, f.child, f.journal, backend); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(f.e.Journal) != before {
				t.Fatal("completed runtime continuation rewrote parents")
			}
		})
	}
}

func TestStagingRuntimeQualificationRejectsChangedSuccessorAndUnknownPredecessor(t *testing.T) {
	for _, mode := range []string{"replaced-owner", "wrong-original", "wrong-receipt-owners", "unknown-write", "original-written"} {
		t.Run(mode, func(t *testing.T) {
			f := stagingRuntimeQualificationFixture(t)
			_, steady, err := stagingWorkloadRuntimeDefinitions(f.child, f.journal)
			if err != nil {
				t.Fatal(err)
			}
			runtimeStage := stagingWorkloadRuntimePrefix + f.child.SHA256()[:12]
			accepted, progress := f.acceptDefinitions(t, runtimeStage, steady)
			receipt := stagingWorkloadAllocationReceipt{accepted.SHA256(), progress.Owners, "kubernetes/" + runtimeStage, f.child.SHA256()}
			switch mode {
			case "replaced-owner":
				progress.Owners[f.child.Resources[0].ID] = "99999999-9999-4999-8999-999999999999"
				f.saveProgress(t, accepted, progress)
			case "wrong-original":
				receipt.Original = deploymentplan.Digest("another workload")
			case "wrong-receipt-owners":
				receipt.Owners = map[string]string{}
			case "unknown-write":
				f.journal.InFlight = f.child.Resources[3].ID
				f.saveProgress(t, f.child, f.journal)
			case "original-written":
				j := deploymentplan.Journal{PlanSHA256: f.original.SHA256(), Owners: map[string]string{}}
				f.saveProgress(t, f.original, j)
			}
			if err := f.e.RecordReceipt("kubernetes/"+f.stage, mustProviderRecoveryJSON(receipt)); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("kubernetes/"+f.stage, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if mode == "original-written" {
				receipt.Original = f.original.SHA256()
				if err := f.e.RecordReceipt("kubernetes/workload", mustProviderRecoveryJSON(receipt)); err != nil {
					t.Fatal(err)
				}
				if _, err := freshCreateWorkloadStage(f.store, f.e); err == nil {
					t.Fatal("written predecessor accepted as unwritten")
				}
			} else if err := verifyStagingWorkloadSupersession(f.store, f.e, f.stage, f.child); err == nil {
				t.Fatal("changed successor or unresolved predecessor accepted")
			}
		})
	}
}

func TestStagingLoggerInitializationQualificationIsOneTimeAndKeepsExistingInbox(t *testing.T) {
	for _, mode := range []string{"empty", "lost-found", "existing-inbox", "unknown-file", "interrupted-initialization", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			if err := os.Mkdir(data, 0700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "args")
			executable := filepath.Join(dir, "logger")
			writeTestFile(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RTK_LOGGER_ARGS_TEST\"\n")
			if err := os.Chmod(executable, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_LOGGER_ARGS_TEST", log)
			switch mode {
			case "lost-found":
				if err := os.Mkdir(filepath.Join(data, "lost+found"), 0700); err != nil {
					t.Fatal(err)
				}
			case "existing-inbox":
				writeTestFile(t, filepath.Join(data, "billing-inbox.db"), "original inbox bytes")
			case "unknown-file":
				writeTestFile(t, filepath.Join(data, "unrecognized"), "do not erase")
			case "interrupted-initialization":
				if err := os.Mkdir(filepath.Join(data, ".rtk-first-initialization"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(data, "unknown-link")); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(stagingLoggerInitializeInbox, "/var/lib/rtk-cloud-logger", data)
			script = strings.ReplaceAll(script, "/usr/local/bin/rtk-cloud-logger", executable)
			run := func() error {
				return exec.CommandContext(t.Context(), "/bin/sh", "-ec", script, "rtk-cloud-logger", "-billing-inbox", filepath.Join(data, "billing-inbox.db")).Run()
			}
			err := run()
			allowed := mode == "empty" || mode == "lost-found" || mode == "existing-inbox"
			if allowed && err != nil || !allowed && err == nil {
				t.Fatalf("initializer outcome for %s: %v", mode, err)
			}
			args, readErr := os.ReadFile(log)
			if !allowed {
				if !os.IsNotExist(readErr) {
					t.Fatal("unsafe directory launched Logger")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			initialized := strings.Contains(string(args), "-initialize-billing-inbox")
			if initialized != (mode != "existing-inbox") {
				t.Fatal("initializer flag did not respect durable inbox")
			}
			if mode == "existing-inbox" {
				raw, _ := os.ReadFile(filepath.Join(data, "billing-inbox.db"))
				if string(raw) != "original inbox bytes" {
					t.Fatal("existing inbox was changed")
				}
			} else {
				marker, err := os.Stat(filepath.Join(data, ".rtk-first-initialization"))
				if err != nil || marker.Mode().Perm() != 0700 {
					t.Fatal("private first-initialization marker missing")
				}
				if run() == nil {
					t.Fatal("missing database after first launch allowed second initialization")
				}
			}
		})
	}
}
