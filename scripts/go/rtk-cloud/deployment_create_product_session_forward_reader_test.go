package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The shared legacy fake used :attempt-N. Model the real engine's exact
// holder shape here, so independent final readers consume actual lease rules.
type productForwardRecordedLease struct {
	deploymentplan.ExecutionLease
	owner deploymentplan.LeaseOwner
}

func (l productForwardRecordedLease) Owner() deploymentplan.LeaseOwner { return l.owner }
func (b *productForwardBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	lease, err := b.productProtocolBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	owner := lease.Owner()
	owner.Holder = p.SHA256() + fmt.Sprintf(":%032x", b.leases)
	return productForwardRecordedLease{lease, owner}, nil
}

func productForwardReadFixture(t *testing.T) (*productForwardFixture, createNormalTailProgramAdmission) {
	t.Helper()
	f := newProductForwardFixture(t)
	if handled, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); !handled || err != nil {
		t.Fatal(handled, err)
	}
	a, err := readCreateNormalTailProgramAdmission(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	return f, a
}
func productForwardRead(t *testing.T, f *productForwardFixture, a createNormalTailProgramAdmission, j environmentCreateJournal) (*productStageReader, bool) {
	t.Helper()
	view := *f.e
	view.Journal = j
	stage := "product-identities-" + productIdentityScope(f.l)[:12] + "-service-session"
	at := -1
	for i, e := range j.Entries {
		if e.Key == "kubernetes/"+stage {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("missing original session")
	}
	r := &productStageReader{store: f.store, e: &view, l: f.l, entries: j.Entries[at:], admission: a}
	defs, err := productStageDefinitions(t.Context(), f.cfg, f.l, f.service, "service-session")
	if err != nil {
		t.Fatal(err)
	}
	complete := r.kube("service-session", defs, nil)
	return r, complete
}
func TestProductSessionForwardReaderConsumesGenuinePrefixesAndPreservesFailedAllocation(t *testing.T) {
	f, a := productForwardReadFixture(t)
	originalRaw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", f.old.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := append(slices.Clone(f.snapshots), f.e.Journal)
	for i, j := range snapshots {
		r, complete := productForwardRead(t, f, a, j)
		if r.err != nil || r.n != len(r.entries) {
			t.Fatalf("prefix %d consumed %d/%d: %v", i, r.n, len(r.entries), r.err)
		}
		want := r.entries[0].State == "completed"
		if complete != want {
			t.Fatalf("prefix %d completion=%v want=%v", i, complete, want)
		}
		if len(r.children) == 0 || r.children[0].SHA256() != f.old.SHA256() {
			t.Fatal("original allocated owner omitted")
		}
	}
	p, j, _, err := readProductSessionForwardOriginal(f.store, f.e, f.l)
	if err != nil || p.SHA256() != f.old.SHA256() || j.Accepted || j.Failed != p.Resources[2].ID || len(j.Completed) != 2 {
		t.Fatal("failed original changed", err)
	}
	claims, err := managedLatePlannedVolumeClaims(p)
	if err != nil || len(claims) != 1 || j.Owners[p.Resources[0].ID] == "" {
		t.Fatal("original PVC no longer has genuine write proof", err)
	}
	nowRaw, _ := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", f.old.SHA256(), "journal.json"))
	if string(nowRaw) != string(originalRaw) {
		t.Fatal("reader rewrote original failed journal")
	}
}
func TestProductSessionForwardReaderRefusesCompositeAndChronologyDrift(t *testing.T) {
	f, a := productForwardReadFixture(t)
	for _, name := range []string{"source", "original-plan", "original-journal", "forward-stage", "forward-plan", "early-composite", "foreign-suffix", "late-child", "late-outer"} {
		t.Run(name, func(t *testing.T) {
			var j environmentCreateJournal
			if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(f.e.Journal), &j); err != nil {
				t.Fatal(err)
			}
			at := 0
			for i, e := range j.Entries {
				if e.Key == "kubernetes/product-identities-"+productIdentityScope(f.l)[:12]+"-service-session" {
					at = i
					break
				}
			}
			var receipt environmentProductSessionForwardReceipt
			if err := json.Unmarshal(j.Entries[at].Receipt, &receipt); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "source":
				receipt.SourceSHA256 = deploymentplan.Digest("foreign source")
			case "original-plan":
				receipt.OriginalPlanSHA256 = deploymentplan.Digest("foreign original")
			case "original-journal":
				receipt.OriginalFailedJournalSHA256 = deploymentplan.Digest("foreign journal")
			case "forward-stage":
				receipt.ActualForwardStage = "foreign-stage"
			case "forward-plan":
				receipt.ActualForwardPlanSHA256 = deploymentplan.Digest("foreign actual")
			case "early-composite":
				j.Entries = j.Entries[:len(j.Entries)-1]
			case "foreign-suffix":
				j.Entries[at+1].Key = "kubernetes/foreign-custody"
			case "late-child":
				j.Entries[at+1].CompletedAt = a.Deadline.Add(time.Second)
			case "late-outer":
				j.Entries[at].CompletedAt = a.Deadline.Add(time.Second)
			}
			j.Entries[at].Receipt = mustProviderRecoveryJSON(receipt)
			r, _ := productForwardRead(t, f, a, j)
			if r.err == nil {
				t.Fatal("unbound recovery accepted")
			}
		})
	}
}
func TestProductSessionForwardOriginalFailureRemainsHistoricalAtFullAcceptance(t *testing.T) {
	f, a := productForwardReadFixture(t)
	r, _ := productForwardRead(t, f, a, f.e.Journal)
	if r.err != nil {
		t.Fatal(r.err)
	}
	old := deploymentplan.Clone(f.b.objects[f.old.Resources[2].ID])
	pod := deploymentplan.Clone(f.pod)
	owner := certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0]
	deploymentplan.Map(owner)["apiVersion"] = "batch/v1"
	live := []deploymentplan.Object{old, pod}
	empty, healthy, digest := targetWorkloadFixture(t, "dev", "Deployment")
	live = append(live, healthy...)
	if err := verifyEnvironmentTargetWorkloads(t.Context(), empty, live, func(_ context.Context, image string) (map[string]bool, error) {
		if strings.Contains(image, "rtk-video-cloud") {
			t.Fatal("historical failed image is not an active runtime")
		}
		return map[string]bool{digest: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	latest := map[string]deploymentplan.Resource{f.old.Resources[2].ID: f.old.Resources[2]}
	owners := map[string]string{f.old.Resources[2].ID: f.oldJournal.Owners[f.old.Resources[2].ID]}
	if err := verifyAcceptedCreateSnapshot(latest, owners, live); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"running", "foreign-pod-owner", "foreign-job-uid", "changed-job-body"} {
		t.Run(bad, func(t *testing.T) {
			job, p := deploymentplan.Clone(old), deploymentplan.Clone(pod)
			switch bad {
			case "running":
				deploymentplan.Map(p["status"])["phase"] = "Running"
			case "foreign-pod-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "foreign-job-uid":
				deploymentplan.Map(job["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "changed-job-body":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])["image"] = "foreign@sha256:bad"
			}
			objects := []deploymentplan.Object{job, p}
			if bad == "changed-job-body" || bad == "foreign-job-uid" {
				if verifyAcceptedCreateSnapshot(latest, owners, objects) == nil {
					t.Fatal("historical owner/body drift accepted")
				}
			} else if empty.CheckWorkloadInventory(objects) == nil {
				t.Fatal("nonhistorical or foreign failed Pod accepted")
			}
		})
	}
}

func TestProductSessionForwardIndependentFinalAllocationReader(t *testing.T) {
	f, _ := productForwardReadFixture(t)
	reader := os.Getenv("RTK_PRODUCT_FINAL_READER")
	if reader == "" {
		t.Skip("RTK_PRODUCT_FINAL_READER selects the independently verified read-only helper")
	}
	program := filepath.Join(t.TempDir(), "program.json")
	if err := os.WriteFile(program, mustProviderRecoveryJSON(f.program), 0600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "journal.json")
	if err := os.WriteFile(journal, mustProviderRecoveryJSON(f.e.Journal), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import importlib.util,json,pathlib,sys,copy
spec=importlib.util.spec_from_file_location('final_reader',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
m.ROOT=pathlib.Path(sys.argv[2]);m.PARENT=sys.argv[3];m.JOURNAL=sys.argv[4]
j=json.loads(pathlib.Path(sys.argv[5]).read_text());p=json.loads(pathlib.Path(sys.argv[6]).read_text())
stage='product-identities-'+m.sha(m.go_json(p['Products']))[:12]+'-service-session'
# The fixture's isolated temporary ancestor is not an environment directory.
original=m.private_bytes
def isolated(path,expected=None):
 raw=(m.ROOT/path).read_bytes()
 if expected is not None:m.require(m.sha(raw)==expected,'immutable isolated evidence changed')
 return raw
m.private_bytes=isolated
plan,failed,entry,_=m.product_session_forward_allocation(j,p,stage)
m.require(not failed.get('accepted') and len(failed['completed'])==2 and len(plan['resources'])==3,'original failed allocation was fabricated')
for key in ('original_plan_sha256','original_failed_journal_sha256','actual_forward_plan_sha256','source_sha256','actual_forward_stage'):
 changed=copy.deepcopy(j)
 for e in changed['entries']:
  if e.get('key')=='kubernetes/'+stage:e['receipt'][key]='f'*64
 try:m.product_session_forward_allocation(changed,p,stage)
 except (ValueError,FileNotFoundError):pass
 else:raise AssertionError('foreign composite accepted: '+key)
print('genuine composite/original failed allocation +5 crosspin negatives PASS')
`
	relative, err := filepath.Rel(f.store.Root, filepath.Join(f.e.Directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-B", "-c", script, reader, f.store.Root, f.e.Journal.PlanSHA256, relative, journal, program)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent final reader: %v: %s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestProductSessionForwardReaderBindsOriginalNativeSourceAndKeyGuard(t *testing.T) {
	f, a := productForwardReadFixture(t)
	path := filepath.Join(f.store.Root, productSessionForwardPath(f.l))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, raw, 0600) })
	for _, name := range []string{"key", "csr", "session", "claim-owner", "trust-owner", "terminal-pod", "terminal-job-body"} {
		t.Run(name, func(t *testing.T) {
			var source environmentProductSessionForward
			if err := decodeEnvironmentJSON(raw, &source); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "key":
				source.KeySHA256 = deploymentplan.Digest("foreign private key")
			case "csr":
				source.CSRSHA256 = deploymentplan.Digest("foreign CSR")
			case "session":
				source.SessionIntentSHA256 = deploymentplan.Digest("foreign session")
			case "claim-owner":
				source.ClaimUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "trust-owner":
				source.Trust.UID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "terminal-pod":
				source.OriginalPodUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "terminal-job-body":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(source.TerminalJob)["containers"])[0])["image"] = "foreign@sha256:bad"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(source), 0600); err != nil {
				t.Fatal(err)
			}
			// Reseal only the composite's public source digest: the accepted forward
			// recipe still contains its original immutable key/CSR guard.
			var j environmentCreateJournal
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.e.Journal), &j)
			for i := range j.Entries {
				var receipt environmentProductSessionForwardReceipt
				if json.Unmarshal(j.Entries[i].Receipt, &receipt) == nil && receipt.ActualForwardStage != "" {
					receipt.SourceSHA256 = deploymentplan.Digest(source)
					j.Entries[i].Receipt = mustProviderRecoveryJSON(receipt)
				}
			}
			r, _ := productForwardRead(t, f, a, j)
			if r.err == nil {
				t.Fatal("foreign native source accepted")
			}
		})
	}
}

func TestProductSessionForwardWholeProductProtocolAndEveryPrefix(t *testing.T) {
	f := newProductForwardFixture(t, true)
	start := len(f.e.Journal.Entries)
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage == "service-session" && f.old != nil {
			if handled, err := executeProductSessionTrustForward(ctx, f.cfg, f.store, f.runtime, f.l, f.apply); handled || err != nil {
				return err
			}
		}
		err := f.apply(ctx, stage, defs)
		if stage == "service-session" && f.old == nil {
			f.old = f.plans["product-identities-"+productIdentityScope(f.l)[:12]+"-service-session"]
			if f.old != nil {
				f.oldJournal, _, _ = f.old.ReadExecution(f.store.Root)
			}
		}
		if err == nil && strings.HasPrefix(stage, "enroll-") {
			f.issue("service:" + strings.TrimPrefix(stage, "enroll-"))
		}
		return err
	}
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, apply); err == nil || f.old == nil {
		t.Fatal("original real TLS failure was not preserved", err)
	}
	pendingOriginal := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/product-identities-"+productIdentityScope(f.l)[:12]+"-service-session")
	failed := deploymentplan.Digest(f.oldJournal)
	key, csr := f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.csr"]
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, apply); err != nil {
		t.Fatal("whole original product protocol retry", err)
	}
	installed, err := readProductInstalled(f.store, f.l)
	if err != nil || len(installed.Outputs) != len(f.l.Outputs) || f.minted != 1 || f.bootstrap["bootstrap.key"] != key || f.bootstrap["bootstrap.csr"] != csr {
		t.Fatal("same-key native session or products changed", err)
	}
	entries := f.e.Journal.Entries[start:]
	n, children, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, entries)
	if err != nil || n != len(entries) || len(children) != len(f.plans) {
		t.Fatalf("complete producer to reader %d/%d children %d/%d: %v", n, len(entries), len(children), len(f.plans), err)
	}
	for end := 1; end <= len(entries); end++ {
		view := *f.e
		view.Journal = f.e.Journal
		view.Journal.Entries = slices.Clone(f.e.Journal.Entries[:start+end])
		// A nested child was persisted while its original outer entry was
		// still pending. Reuse that genuine saved original state; slicing a
		// later completed composite before its children would invent history.
		forwardKey := "kubernetes/" + productSessionForwardStage(f.l, "forward")
		forwardVisible := createOpenBaoACKCompletionEntry(view.Journal, forwardKey) != nil
		if !forwardVisible {
			for i := range view.Journal.Entries {
				if view.Journal.Entries[i].Key == pendingOriginal.Key {
					view.Journal.Entries[i] = pendingOriginal
				}
			}
		}
		n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, &view, f.l, view.Journal.Entries[start:])
		if err != nil || n != end {
			t.Fatalf("whole persisted prefix %d/%d consumed %d: %v", end, len(entries), n, err)
		}
	}
	f.installed = slices.Clone(f.subjects)
	if err := completeProductIdentitySession(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, installed, nil); err != nil {
		t.Fatal("same native session finalization", err)
	}
	current, exists, err := f.old.ReadExecution(f.store.Root)
	if err != nil || !exists || current.Accepted || deploymentplan.Digest(current) != failed || f.minted != 1 {
		t.Fatal("finalizer rewrote or replayed original failure", err)
	}
	if len(f.bootstrap) != 0 || f.final.SessionID != f.service.BootstrapSessionID || f.status != "sealed" {
		t.Fatal("genuine native cleanup did not complete")
	}
}
