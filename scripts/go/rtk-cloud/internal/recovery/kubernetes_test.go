package recovery

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

type fakeCluster struct {
	t          *testing.T
	config     Config
	journal    string
	replicas   map[string]int
	events     []string
	failCheck  bool
	extraKind  string
	failCreate bool
}

func newFake(t *testing.T) (*Engine, *fakeCluster) {
	c := fixture(t)
	f := &fakeCluster{t: t, config: c, replicas: map[string]int{"api": 2, "postgres": 1, "openbao": 1}}
	e := &Engine{Config: c, SecretRoot: privateTemp(t), Kubeconfig: "/test/kubeconfig", Exec: f.exec}
	e.Publish = func(context.Context, Config, string, string) error { return nil }
	return e, f
}
func (f *fakeCluster) exec(_ context.Context, argv []string, in io.Reader, out io.Writer) error {
	line := strings.Join(argv, " ")
	f.events = append(f.events, line)
	emit := func(v any) error { return json.NewEncoder(out).Encode(v) }
	if argv[0] == "/operator/check" {
		if f.failCheck {
			return errors.New("check failed")
		}
		return nil
	}
	if strings.Contains(line, "-- SELECT datname") {
		return errors.New("unexpected query")
	}
	if strings.Contains(line, "SELECT datname") {
		_, err := io.WriteString(out, "accounts\n")
		return err
	}
	if strings.Contains(line, "SELECT count(*) FROM pg_tablespace") {
		_, err := io.WriteString(out, "0\n")
		return err
	}
	if strings.Contains(line, "SELECT rolname FROM pg_roles") {
		_, err := io.WriteString(out, "postgres\n")
		return err
	}
	if strings.Contains(line, "-- pg_dumpall ") {
		_, err := io.WriteString(out, "-- PostgreSQL database cluster dump\nCREATE ROLE postgres;\n")
		return err
	}
	if strings.Contains(line, "-- pg_dump ") {
		_, err := io.WriteString(out, "PGDMP fixture")
		return err
	}
	if strings.Contains(line, "-- pg_restore ") {
		_, err := io.Copy(io.Discard, in)
		return err
	}
	if strings.Contains(line, "-- psql ") {
		if in != nil {
			_, err := io.Copy(io.Discard, in)
			return err
		}
		return nil
	}
	if strings.Contains(line, "-- tar -cpf -") {
		tw := tar.NewWriter(out)
		if err := tw.WriteHeader(&tar.Header{Name: "core", Mode: 0600, Size: 4, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte("data")); err != nil {
			return err
		}
		return tw.Close()
	}
	if strings.Contains(line, "-- find /backup ") {
		return nil
	}
	if strings.Contains(line, "-- tar -xpf -") {
		_, err := io.Copy(io.Discard, in)
		return err
	}
	if strings.Contains(line, " wait --for=condition=Ready pod/") || strings.Contains(line, " delete pod ") {
		return nil
	}
	if strings.Contains(line, " get namespace ") {
		return emit(map[string]any{"metadata": map[string]string{"uid": "namespace-uid"}})
	}
	if strings.Contains(line, " get configmap ") {
		if f.journal == "" {
			return errors.New("not found")
		}
		return emit(map[string]any{"data": map[string]string{"journal": f.journal}})
	}
	if strings.Contains(line, " create -f -") || strings.Contains(line, " apply -f -") {
		if f.failCreate {
			return errors.New("lock exists")
		}
		var object struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := json.NewDecoder(in).Decode(&object); err != nil {
			return err
		}
		if object.Kind == "ConfigMap" {
			f.journal = object.Data["journal"]
		}
		return nil
	}
	if strings.Contains(line, " get deployments,statefulsets,") {
		items := []any{}
		for _, w := range f.config.Workloads {
			kind := "Deployment"
			if w.Kind == "statefulset" {
				kind = "StatefulSet"
			}
			items = append(items, map[string]any{"kind": kind, "metadata": map[string]string{"name": w.Name}, "spec": map[string]int{"replicas": f.replicas[w.Name]}})
		}
		if f.extraKind != "" {
			items = append(items, map[string]any{"kind": f.extraKind, "metadata": map[string]string{"name": "unknown"}})
		}
		return emit(map[string]any{"items": items})
	}
	if strings.Contains(line, " get pods ") || strings.Contains(line, " get replicasets ") {
		return emit(map[string]any{"items": []any{}})
	}
	for _, w := range f.config.Workloads {
		if strings.Contains(line, " scale "+w.Kind+"/"+w.Name) {
			for _, a := range argv {
				if a == "--replicas=0" {
					f.replicas[w.Name] = 0
				}
				if a == "--replicas=1" {
					f.replicas[w.Name] = 1
				}
				if a == "--replicas=2" {
					f.replicas[w.Name] = 2
				}
			}
			return nil
		}
		if strings.Contains(line, " get "+w.Kind+" "+w.Name+" ") {
			return emit(map[string]any{"spec": map[string]any{"replicas": f.replicas[w.Name], "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]string{"name": "main", "image": "fixture@sha256:" + strings.Repeat("a", 64)}}}}}})
		}
	}
	if strings.Contains(line, " delete configmap ") {
		f.journal = ""
		return nil
	}
	f.t.Fatalf("unexpected fake Kubernetes call: %s", line)
	return nil
}

func TestRestorePublishesSafetyBackupBeforeAnyOverwrite(t *testing.T) {
	e, f := newFake(t)
	ctx := context.Background()
	identity, _ := age.GenerateX25519Identity()
	e.Config.Recipients = []string{identity.Recipient().String()}
	f.config = e.Config
	key := filepath.Join(privateTemp(t), "identity")
	writeTest(t, key, []byte(identity.String()+"\n"))
	if err := os.Mkdir(filepath.Join(e.SecretRoot, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(e.SecretRoot, "runtime", "credential"), []byte("original"))
	e.Publish = func(context.Context, Config, string, string) error { return nil }
	id, err := e.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.Resume(ctx, ""); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(e.SecretRoot, "runtime", "credential"), []byte("current-target"))
	e.Publish = func(context.Context, Config, string, string) error { return errors.New("remote unavailable") }
	f.events = nil
	if _, err = e.Restore(ctx, filepath.Join(e.Config.Directory, id+".age"), key); err == nil {
		t.Fatal("restore ignored failed safety publication")
	}
	for _, event := range f.events {
		if strings.Contains(event, "-- find /backup") || strings.Contains(event, "-- pg_restore -U") {
			t.Fatal("dataset mutated before safety backup")
		}
	}
	b, _ := os.ReadFile(filepath.Join(e.SecretRoot, "runtime", "credential"))
	if string(b) != "current-target" {
		t.Fatal("secret overwritten before safety backup")
	}
	if err = e.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.Resume(ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.Publish = func(_ context.Context, _ Config, safetyID string, _ string) error {
		f.events = append(f.events, "published "+safetyID)
		return nil
	}
	if _, err = e.Restore(ctx, filepath.Join(e.Config.Directory, id+".age"), key); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(e.SecretRoot, "runtime", "credential"))
	if string(b) != "original" {
		t.Fatal("matched credential not restored")
	}
	j, err := e.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if j.Phase != "restored" || j.SafetyBackupID == "" || j.BackupID != id {
		t.Fatal("restore journal lost backup identities")
	}
	if e.Resume(ctx, "") == nil {
		t.Fatal("restore resumed without verify/reconciliation")
	}
}

func TestMaintenancePersistsBeforeScaleAndRestoresReplicas(t *testing.T) {
	e, f := newFake(t)
	ctx := context.Background()
	j, err := e.Begin(ctx, "backup", "operation-1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Phase != "paused" || f.replicas["api"] != 0 || f.replicas["openbao"] != 0 || f.replicas["postgres"] != 1 {
		t.Fatal("wrong maintenance scope")
	}
	created, scaled := -1, -1
	for i, event := range f.events {
		if created < 0 && strings.Contains(event, "create -f -") {
			created = i
		}
		if scaled < 0 && strings.Contains(event, " scale ") {
			scaled = i
		}
	}
	if created < 0 || created > scaled {
		t.Fatal("mutation before persistent lock")
	}
	if _, err = os.Stat(e.journalPath()); err != nil {
		t.Fatal(err)
	}
	j.Phase = "captured"
	if err = e.save(ctx, &j, false); err != nil {
		t.Fatal(err)
	}
	if err = e.Resume(ctx, ""); err == nil {
		t.Fatal("resume without verify")
	}
	if err = e.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.Resume(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if f.replicas["api"] != 2 || f.replicas["openbao"] != 1 {
		t.Fatal("original replicas not restored")
	}
	if _, err = os.Stat(e.journalPath()); !os.IsNotExist(err) {
		t.Fatal("completed local lock survived")
	}
}

func TestFailuresRetainLockAndRestoreRequiresReconciliation(t *testing.T) {
	e, f := newFake(t)
	ctx := context.Background()
	j, err := e.Begin(ctx, "restore", "operation-2")
	if err != nil {
		t.Fatal(err)
	}
	j.Phase = "restoring"
	j.BackupID = "source-backup"
	if err = e.save(ctx, &j, false); err != nil {
		t.Fatal(err)
	}
	if e.Abort(ctx) == nil || e.Verify(ctx) == nil {
		t.Fatal("incomplete restore could release maintenance")
	}
	j.Phase = "restored"
	if err = e.save(ctx, &j, false); err != nil {
		t.Fatal(err)
	}
	f.failCheck = true
	if e.Verify(ctx) == nil {
		t.Fatal("failed check passed")
	}
	if f.journal == "" {
		t.Fatal("lock cleared on failure")
	}
	f.failCheck = false
	if err = e.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if e.Resume(ctx, "") == nil {
		t.Fatal("restore resumed without reconciliation")
	}
	evidence := filepath.Join(privateTemp(t), "approved.json")
	if err = WriteJSON(evidence, Reconciliation{Environment: e.Config.Environment, Stack: e.Config.Stack, OperationID: j.ID, BackupID: j.BackupID, ApprovedBy: "test-operator", PaymentAndEmail: true, PKIAndRevocation: true, ObjectsAndAudit: true}); err != nil {
		t.Fatal(err)
	}
	if err = e.Resume(ctx, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownInventoryAndLockConflictFailBeforeScale(t *testing.T) {
	for _, kind := range []string{"HorizontalPodAutoscaler", "DaemonSet", "CronJob", "Job", "PersistentVolumeClaim"} {
		t.Run(kind, func(t *testing.T) {
			e, f := newFake(t)
			f.extraKind = kind
			if _, err := e.Begin(context.Background(), "backup", "operation"); err == nil {
				t.Fatal("unknown resource accepted")
			}
			for _, event := range f.events {
				if strings.Contains(event, " scale ") {
					t.Fatal("mutation on failed preflight")
				}
			}
		})
	}
	e, f := newFake(t)
	f.failCreate = true
	if _, err := e.Begin(context.Background(), "backup", "operation"); err == nil {
		t.Fatal("lock conflict accepted")
	}
	for _, event := range f.events {
		if strings.Contains(event, " scale ") {
			t.Fatal("mutation without lock")
		}
	}
}

func TestJournalConfigurationMismatch(t *testing.T) {
	e, _ := newFake(t)
	ctx := context.Background()
	if _, err := e.Begin(ctx, "backup", "operation"); err != nil {
		t.Fatal(err)
	}
	e.Config.Stack = "different"
	if _, err := e.Load(ctx); err == nil {
		t.Fatal("changed configuration accepted during maintenance")
	}
}

func TestBackupLayoutRefusesSelfCaptureAndGit(t *testing.T) {
	e, _ := newFake(t)
	for _, dir := range []string{e.SecretRoot, filepath.Join(e.SecretRoot, "runtime", "backups"), filepath.Dir(e.SecretRoot)} {
		e.Config.Directory = dir
		if e.ValidateLayout() == nil {
			t.Fatal("overlapping backup/SecretStore trees accepted")
		}
	}
	repo := privateTemp(t)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	e.Config.Directory = filepath.Join(repo, "backups")
	if e.ValidateLayout() == nil {
		t.Fatal("archive inside Git accepted")
	}
}
