package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func productAttemptFixture(t *testing.T) (*productForwardFixture, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	f := newProductForwardFixture(t)
	f.failure = "before-service-session-trust-forward"
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
		t.Fatal("expected stop before first legacy forward")
	}
	source, err := readProductSessionForward(f.store, f.e, f.l)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	defs, err := productSessionForwardDefinitions(f.l, f.old, source)
	if err != nil {
		t.Fatal(err)
	}
	f.tokenFailure = true
	if err = f.apply(t.Context(), "service-session-trust-forward", defs); err == nil {
		t.Fatal("expected original terminal token failure")
	}
	p := f.plans[productSessionForwardStage(f.l, "forward")]
	j, exists, err := p.ReadExecution(f.store.Root)
	if err != nil || !exists || !productSessionAttemptFailed(p, j) {
		t.Fatal("not genuine failed token child", err)
	}
	return f, p, j
}
func TestProductSessionAttemptsPreserveFailedChildrenAndCloseOriginalOwner(t *testing.T) {
	f, p, j := productAttemptFixture(t)
	old := deploymentplan.Digest(f.oldJournal)
	failed := deploymentplan.Digest(j)
	key, csr := f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.csr"]
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	a, err := readCreateNormalTailProgramAdmission(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	r, done := productForwardRead(t, f, a, f.e.Journal)
	if r.err != nil || !done || r.n != len(r.entries) || len(r.children) != 5 {
		t.Fatalf("full linked parser done=%v %d/%d children=%d: %v", done, r.n, len(r.entries), len(r.children), r.err)
	}
	current, _, _ := p.ReadExecution(f.store.Root)
	original, _, _ := f.old.ReadExecution(f.store.Root)
	if deploymentplan.Digest(current) != failed || current.Accepted || deploymentplan.Digest(original) != old || original.Accepted || f.minted != 1 || f.bootstrap["bootstrap.key"] != key || f.bootstrap["bootstrap.csr"] != csr {
		t.Fatal("failed history or original custody changed")
	}
	source, _ := readProductSessionForward(f.store, f.e, f.l)
	attempt, err := readProductSessionAttempt(f.store, f.e, f.l, source, productSessionForwardStage(f.l, "forward"), p, j)
	if err != nil || attempt == nil {
		t.Fatal("closed known failure source", err)
	}
	for _, entry := range f.e.Journal.Entries {
		if entry.State != "completed" {
			t.Fatal("parent cannot close", entry.Key, entry.State)
		}
	}
	// A retry after mint must consume the accepted winner, not re-run no-mint,
	// custody, failed child, native signing, or any failed Job write.
	before := f.noMintReads
	writes := deploymentplan.Digest(f.b.writes)
	if _, err = executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal("completed retry", err)
	}
	if f.noMintReads != before || deploymentplan.Digest(f.b.writes) != writes || f.minted != 1 {
		t.Fatal("completed chain replayed native effects")
	}
	// Unknown or forged parent supersession can never become closure proof.
	for _, bad := range []string{"winner", "failed", "early"} {
		t.Run(bad, func(t *testing.T) {
			var view environmentCreateJournal
			_ = json.Unmarshal(mustProviderRecoveryJSON(f.e.Journal), &view)
			for i, entry := range view.Entries {
				if entry.Key == "kubernetes/"+productSessionForwardStage(f.l, "forward") {
					var receipt environmentProductSessionSuperseded
					_ = json.Unmarshal(entry.Receipt, &receipt)
					switch bad {
					case "winner":
						receipt.WinnerPlanSHA256 = deploymentplan.Digest("foreign")
					case "failed":
						receipt.FailedJournalSHA256 = deploymentplan.Digest("foreign")
					case "early":
						view.Entries[i].CompletedAt = entry.StartedAt
					}
					view.Entries[i].Receipt = mustProviderRecoveryJSON(receipt)
				}
			}
			rr, _ := productForwardRead(t, f, a, view)
			if rr.err == nil {
				t.Fatal("forged supersession accepted")
			}
		})
	}
	if err = f.e.CompleteAndReleaseContext(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal("original owner closure", err)
	}
	if _, err = os.Stat(filepath.Join(f.store.Root, "deployment/create/owner.json")); !os.IsNotExist(err) {
		t.Fatal("original owner not archived", err)
	}
}
func TestProductSessionAttemptsUnknownWriteNeverDuplicates(t *testing.T) {
	f, p, _ := productAttemptFixture(t)
	stage := productSessionAttemptStage(f.l, p)
	f.failure = "unknown-" + strings.TrimPrefix(stage, "product-identities-"+productIdentityScope(f.l)[:12]+"-")
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
		t.Fatal("expected unknown write")
	}
	writes := deploymentplan.Digest(f.b.writes)
	for range 2 {
		if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
			t.Fatal("unknown write replayed")
		}
	}
	if deploymentplan.Digest(f.b.writes) != writes || f.minted != 0 {
		t.Fatal("unknown native outcome caused repeated write/mint")
	}
}
func TestProductSessionAttemptsNewForwardUsesReadableTokenWithoutFailure(t *testing.T) {
	f := newProductForwardFixture(t)
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	p := f.plans[productSessionForwardStage(f.l, "forward")]
	volumes := certIssuerObjectList(environmentPKIPod(p.Resources[0].Desired)["volumes"])
	for _, v := range volumes {
		if deploymentplan.Map(v)["name"] == "openbao-auth" && deploymentplan.Map(deploymentplan.Map(v)["projected"])["defaultMode"] != float64(292) {
			t.Fatal("new initial forward retained unreadable token")
		}
	}
	if f.minted != 1 {
		t.Fatal("new forward was not direct")
	}
}
func TestProductSessionAttemptsBothFailuresRemainHistoricalAtFullAcceptance(t *testing.T) {
	f, p, j := productAttemptFixture(t)
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	old, token := deploymentplan.Clone(f.b.objects[f.old.Resources[2].ID]), deploymentplan.Clone(f.b.objects[p.Resources[0].ID])
	pod, tokenPod := deploymentplan.Clone(f.pod), deploymentplan.Clone(f.tokenPod)
	live := []deploymentplan.Object{old, pod, token, tokenPod}
	empty, healthy, digest := targetWorkloadFixture(t, "dev", "Deployment")
	live = append(live, healthy...)
	if err := verifyEnvironmentTargetWorkloads(t.Context(), empty, live, func(context.Context, string) (map[string]bool, error) { return map[string]bool{digest: true}, nil }); err != nil {
		t.Fatal(err)
	}
	latest := map[string]deploymentplan.Resource{f.old.Resources[2].ID: f.old.Resources[2], p.Resources[0].ID: p.Resources[0]}
	owners := map[string]string{f.old.Resources[2].ID: f.oldJournal.Owners[f.old.Resources[2].ID], p.Resources[0].ID: j.Owners[p.Resources[0].ID]}
	if err := verifyAcceptedCreateSnapshot(latest, owners, live); err != nil {
		t.Fatal(err)
	}

	// The current winner remains a planned successful Job, never another
	// historical failure exemption. Verify its native exit and immutable images.
	winner := f.plans[productSessionAttemptStage(f.l, p)]
	wj, _, err := winner.ReadExecution(f.store.Root)
	if err != nil {
		t.Fatal(err)
	}
	winnerJob := deploymentplan.Clone(f.b.objects[winner.Resources[0].ID])
	wm := deploymentplan.Map(winnerJob["metadata"])
	winnerResource := winner.Resources[0]
	winnerResource.UID = wj.Owners[winnerResource.ID]
	empty.Resources = append(empty.Resources, winnerResource)
	winnerPod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": deploymentplan.String(wm["name"]) + "-complete", "namespace": wm["namespace"], "uid": "adadadad-adad-4dad-8dad-adadadadadad", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "name": wm["name"], "uid": wm["uid"], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(winnerJob)), "status": deploymentplan.Object{"phase": "Succeeded"}}
	for _, field := range []string{"containers", "initContainers"} {
		var statuses []any
		for _, raw := range certIssuerObjectList(environmentPKIPod(winnerJob)[field]) {
			c := deploymentplan.Map(raw)
			statuses = append(statuses, deploymentplan.Object{"name": c["name"], "containerID": "containerd://" + deploymentplan.String(c["name"]), "imageID": "docker-pullable://" + deploymentplan.String(c["image"]), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}})
		}
		deploymentplan.Map(winnerPod["status"])[map[string]string{"containers": "containerStatuses", "initContainers": "initContainerStatuses"}[field]] = statuses
	}
	live = append(live, winnerJob, winnerPod)
	resolver := func(_ context.Context, image string) (map[string]bool, error) {
		_, imageDigest, _ := strings.Cut(image, "@")
		return map[string]bool{imageDigest: true}, nil
	}
	if err := verifyEnvironmentTargetWorkloads(t.Context(), empty, live, resolver); err != nil {
		t.Fatal("accepted winner health", err)
	}
	winnerJob["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
	if verifyEnvironmentTargetWorkloads(t.Context(), empty, live, resolver) == nil {
		t.Fatal("current winner failure was ignored as history")
	}
	// Failed history is excluded only from readiness, never from owner/body proof.
	deploymentplan.Map(token["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if verifyAcceptedCreateSnapshot(latest, owners, live) == nil {
		t.Fatal("failed history UID drift ignored")
	}
}

func TestProductSessionAttemptsWholeProtocolFinalizationAndEveryPrefix(t *testing.T) {
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
	f.failure = "before-service-session-trust-forward"
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
		t.Fatal("expected legacy stop")
	}
	source, err := readProductSessionForward(f.store, f.e, f.l)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := productSessionForwardDefinitions(f.l, f.old, source)
	if err != nil {
		t.Fatal(err)
	}
	f.tokenFailure = true
	if err = f.apply(t.Context(), "service-session-trust-forward", legacy); err == nil {
		t.Fatal("expected original token failure")
	}
	failedForward := f.plans[productSessionForwardStage(f.l, "forward")]
	failedForwardJournal, _, err := failedForward.ReadExecution(f.store.Root)
	if err != nil {
		t.Fatal(err)
	}
	tokenFailedDigest := deploymentplan.Digest(failedForwardJournal)

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
		forwardKey := "kubernetes/" + productSessionAttemptStage(f.l, failedForward)
		forwardVisible := createOpenBaoACKCompletionEntry(view.Journal, forwardKey) != nil
		if !forwardVisible {
			for i := range view.Journal.Entries {
				if view.Journal.Entries[i].Key == pendingOriginal.Key {
					view.Journal.Entries[i] = pendingOriginal
				}
				if view.Journal.Entries[i].Key == "kubernetes/"+productSessionForwardStage(f.l, "forward") {
					view.Journal.Entries[i].State = "pending"
					view.Journal.Entries[i].Receipt = nil
					view.Journal.Entries[i].CompletedAt = time.Time{}
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
	currentForward, _, err := failedForward.ReadExecution(f.store.Root)
	if err != nil || currentForward.Accepted || deploymentplan.Digest(currentForward) != tokenFailedDigest {
		t.Fatal("token failure overwritten", err)
	}
	if len(f.bootstrap) != 0 || f.final.SessionID != f.service.BootstrapSessionID || f.status != "sealed" {
		t.Fatal("genuine native cleanup did not complete")
	}
	// The ordinary original closure accepts truthful supersession outcomes while
	// each failed child remains failed; it does not need a replacement owner.
	if err := f.e.CompleteAndReleaseContext(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal("whole protocol original closure", err)
	}
}

func TestProductSessionAttemptsIndependentFinalAllocationReader(t *testing.T) {
	f, _, _ := productAttemptFixture(t)
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
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
