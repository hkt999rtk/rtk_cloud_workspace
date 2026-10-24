package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native engine uses a 32-hex attempt suffix, including failed attempts.
// Keep the original and access fixture leases realistic for the final reader.
type workloadForwardRecordedLease struct {
	deploymentplan.ExecutionLease
	owner deploymentplan.LeaseOwner
}

func (l workloadForwardRecordedLease) Owner() deploymentplan.LeaseOwner { return l.owner }
func (b *plannedEnvelopeWaitBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	lease, err := b.normalTailE2EBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	owner := lease.Owner()
	owner.Holder = p.SHA256() + fmt.Sprintf(":%032x", b.leases)
	return workloadForwardRecordedLease{lease, owner}, nil
}

type workloadForwardReaderBackend struct {
	*plannedEnvelopeWaitBackend
	stopID      string
	interrupted bool
}

func (b *workloadForwardReaderBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.plannedEnvelopeWaitBackend.Wait(ctx, r); err != nil {
		return err
	}
	if r.ID == b.stopID && !b.interrupted {
		b.interrupted = true
		return errors.New("isolated known response before completion")
	}
	return nil
}

func assertWorkloadForwardReaderPrefix(t *testing.T, f *normalPostgresAccessFixture) {
	t.Helper()
	e := f.h.Execution
	original, j, err := readCreateNormalTailWorkloadOriginal(f.store, e, f.workload)
	if err != nil || original == nil || original.SHA256() != f.workload.SHA256() || j.Accepted {
		t.Fatal("literal original prefix changed", err)
	}
	admission, err := readCreateNormalTailProgramAdmission(f.store, e)
	if err != nil {
		t.Fatal(err)
	}
	at := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/workload" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("logical original workload missing")
	}
	outer := e.Journal.Entries[at]
	following := e.Journal.Entries[at+1:]
	count, children, err := validateCreateNormalTailNestedWorkloadStages(f.store, e, original, j, outer, following, admission)
	if err != nil {
		t.Fatal("genuine producer prefix refused", err)
	}
	intent, err := environmentCreateKubernetesStageIntent(original)
	if err != nil {
		t.Fatal(err)
	}
	reader := productStageReader{entries: append([]environmentCreateJournalEntry{outer}, following...), admission: admission}
	complete := reader.takeNested("kubernetes/workload", "workload", jsonRawObject(intent), nil, count)
	if reader.err != nil || reader.n != len(following)+1 || complete != (outer.State == "completed") {
		t.Fatal("logical workload lost exact nested chronology", reader.err)
	}
	children = append(children, original)
	for _, lease := range e.Journal.KubernetesLeases {
		found := false
		for _, p := range children {
			if p != nil && managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256()) {
				found = true
			}
		}
		if !found {
			t.Fatal("genuine nested child lease lost its Plan")
		}
	}
}

func TestCreateNormalTailWorkloadForwardReaderGenuineProducerPrefixes(t *testing.T) {
	f := newNormalWorkloadForwardFixture(t)
	e := f.h.Execution
	oldPath := filepath.Join(f.store.Root, "deployment/rollback", f.workload.SHA256(), "journal.json")
	before, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	c := &compiledEnvironmentDeployment{Store: f.store, Plan: f.workload, Configuration: f.cfg, Release: f.artifact.Program.Release, Bindings: f.artifact.Program.Bindings}
	c, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
		objects := make([]deploymentplan.Object, 0, len(f.b.objects))
		for _, o := range f.b.objects {
			objects = append(objects, deploymentplan.Clone(o))
		}
		return objects, nil
	})
	if err != nil || c.Plan.SHA256() == f.workload.SHA256() {
		t.Fatal("real forward preparation failed", err)
	}
	assertWorkloadForwardReaderPrefix(t, f)
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	stop := errors.New("isolated admission interruption")
	backend := &workloadForwardReaderBackend{plannedEnvelopeWaitBackend: f.b}
	for _, r := range c.Plan.Resources {
		if r.Action != "retain" {
			backend.stopID = r.ID
			break
		}
	}
	if backend.stopID == "" {
		t.Fatal("no genuine remaining write")
	}
	capture := func(context.Context) error { return nil }
	run := func(qualify, accept func(context.Context, *deploymentplan.Plan) error, capture func(context.Context) error) error {
		return executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, backend, qualify, check, accept, io.Discard, capture)
	}
	if err := run(func(context.Context, *deploymentplan.Plan) error { return stop }, check, capture); err == nil {
		t.Fatal("admission interruption missing")
	}
	assertWorkloadForwardReaderPrefix(t, f)
	if err := run(check, check, capture); err == nil || !backend.interrupted {
		t.Fatal("known write interruption missing", err)
	}
	assertWorkloadForwardReaderPrefix(t, f)
	writes := f.b.writes[backend.stopID]
	if writes != 1 {
		t.Fatal("not a genuine native response")
	}
	if err := run(check, func(context.Context, *deploymentplan.Plan) error { return stop }, capture); err == nil {
		t.Fatal("acceptance interruption missing")
	}
	assertWorkloadForwardReaderPrefix(t, f)
	if err := run(check, check, func(context.Context) error { return stop }); err == nil {
		t.Fatal("capture interruption missing")
	}
	assertWorkloadForwardReaderPrefix(t, f)
	if err := run(check, check, capture); err != nil {
		t.Fatal("real producer closure failed", err)
	}
	assertWorkloadForwardReaderPrefix(t, f)
	selected, j, err := readCreateNormalTailWorkload(f.store, e, f.workload)
	if err != nil || selected.SHA256() != c.Plan.SHA256() || !j.Accepted {
		t.Fatal("effective accepted child not selected", err)
	}
	if f.b.writes[backend.stopID] != writes || f.b.writes["StatefulSet/"+e.Journal.Stack+"-platform/postgresql"] != 1 {
		t.Fatal("known forward or original Postgres write repeated")
	}
	after, err := os.ReadFile(oldPath)
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("real producer changed original journal", err)
	}
	if err := executeCreateNormalTailSequence(t.Context(), e, []func(context.Context) error{
		func(context.Context) error {
			_, actual, err := readCreateNormalTailWorkload(f.store, e, f.workload)
			if err != nil {
				return err
			}
			if !actual.Accepted {
				return errors.New("real accepted forward missing")
			}
			return nil
		},
	}); err != nil {
		t.Fatal("genuine logical completion/archive failed", err)
	}
	if e.Journal.CompletedAt.IsZero() {
		t.Fatal("original owner was not closed")
	}
	t.Run("IndependentPythonAllocationProof", func(t *testing.T) { checkWorkloadForwardPython(t, f) })
}

func checkWorkloadForwardPython(t *testing.T, f *normalPostgresAccessFixture) {
	t.Helper()
	helper := os.Getenv("RTK_PRODUCT_FINAL_READER")
	if helper == "" {
		t.Skip("RTK_PRODUCT_FINAL_READER must select the independent read-only verifier")
	}
	e := f.h.Execution
	admission, err := readCreateNormalTailProgramAdmission(f.store, e)
	if err != nil {
		t.Fatal(err)
	}
	journalPath, err := filepath.Rel(f.store.Root, filepath.Join(e.Directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"root": f.store.Root, "parent": e.Journal.PlanSHA256, "owner": deploymentplan.Digest(e.owner), "journal_path": journalPath, "journal": e.Journal, "program": f.artifact.Program, "admission": admission})
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys,json,pathlib,importlib.util,copy
spec=importlib.util.spec_from_file_location('independent_final',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
x=json.load(sys.stdin);m.ROOT=pathlib.Path(x['root']);m.PARENT=x['parent'];m.OWNER_SHA=x['owner'];m.JOURNAL=x['journal_path']
# The fixture's program/admission are already authenticated by the production Go
# reader above. Keep this test scoped to the independently parsed workload proof.
m.recorded_program=lambda journal:(None,x['program'],x['admission'])
journal=x['journal'];sources=m.workload_allocation_sources(journal)
assert len(sources)==2 and sources[0][1]['accepted'] is False and sources[1][1]['accepted'] is True
# Verify the exact original+forward allocation sources without inventing native
# product stage evidence absent from this deliberately small workload fixture.
workload=copy.deepcopy(journal);workload['entries']=[e for e in workload['entries'] if e['key']!='normal-tail/program']
m.planned_volume_claims(workload)
for mode in ('source-header','missing-forward-lease','wrong-composite','owner-replacement','foreign-desired','missing-policy'):
    original_read=m.read_json;original_private=m.private_bytes
    def alter_read(path,expected=None):
        value=original_read(path,expected)
        if path.endswith('normal-tail-workload-forward.json'):
            if mode=='source-header':value['OwnerSHA256']='0'*64
            elif mode=='owner-replacement':value['Target']['Plan']['resources'][0]['uid']='ffffffff-ffff-4fff-8fff-ffffffffffff'
            elif mode=='foreign-desired':value['Target']['Plan']['resources'][0]['desired']['metadata']['labels']['foreign']='x'
            elif mode=='missing-policy':value['Target']['Plan']['resources'].pop()
        return value
    m.read_json=alter_read
    bad=copy.deepcopy(journal)
    if mode=='missing-forward-lease':bad['kubernetes_leases']=[e for e in bad['kubernetes_leases'] if not e['holder'].startswith(m.sha(m.go_json(sources[1][0]))+':')]
    if mode=='wrong-composite':next(e for e in bad['entries'] if e['key']=='kubernetes/workload')['receipt']['forward_journal_sha256']='0'*64
    try:m.workload_allocation_sources(bad)
    except (ValueError,KeyError):pass
    else:raise AssertionError('independent verifier accepted '+mode)
    m.read_json=original_read;m.private_bytes=original_private
print('genuine forward/old failed source/allocation proof + six substitutions rejected')
`
	cmd := exec.Command("python3", "-B", "-c", script, helper)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	cmd.Stdin = bytes.NewReader(payload)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent allocation verifier: %v\n%s", err, output)
	}
}
