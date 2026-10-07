package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func postgresBackupTestConfig(t *testing.T) postgresBackupDeployment {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	image := "registry.example.test/postgres@sha256:" + strings.Repeat("a", 64)
	return postgresBackupDeployment{Version: 1, Environment: "staging", Stack: "video-cloud-staging", Namespace: "video-cloud-staging-platform", RunnerImage: "registry.example.test/runner@sha256:" + strings.Repeat("b", 64), SourcePod: "postgresql-0", SourceContainer: "postgres", SourcePVC: "data-postgresql-0", ScratchStorage: "60Gi", Worker: postgresbackup.Config{Version: 1, Environment: "staging", Stack: "video-cloud-staging", ClusterID: "main", Directory: "/backup", Source: postgresbackup.Source{Host: "postgresql.video-cloud-staging-platform.svc.cluster.local", Port: 5432, User: "rtk_postgres_backup", PasswordFile: "/run/postgres-backup/source-password", SSLMode: "disable", PostgresImage: image, SystemIdentifier: "123456789", MaxRate: "10M"}, Recipients: []string{identity.Recipient().String()}, Remote: recovery.Remote{Endpoint: "https://backup.example.test", Region: "us-test", Bucket: "private-backup", Prefix: "staging/postgres"}, TimeoutSeconds: 14400, MaxArchiveBytes: 22 << 30, MaxPlaintextBytes: 20 << 30, RetentionDays: 14, MinimumBackups: 14}}
}

func TestPostgresBackupImagePreservesReviewedWorkspaceModule(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(workspace, "scripts", "go", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "replace github.com/hkt999rtk/rtk_cloud_logger => ../../repos/rtk_cloud_logger") {
		t.Fatal("test must track the actual reviewed Logger module replacement")
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "postgres-backup", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	// Download must see both manifests at the unchanged relative replacement
	// path. Compilation must then use that same pinned submodule's source.
	directives := []string{
		"WORKDIR /src/scripts/go\n",
		"COPY scripts/go/go.mod scripts/go/go.sum ./\n",
		"COPY repos/rtk_cloud_logger/go.mod repos/rtk_cloud_logger/go.sum /src/repos/rtk_cloud_logger/\n",
		"RUN go mod download\n",
		"COPY scripts/go/ ./\n",
		"COPY repos/rtk_cloud_logger/ /src/repos/rtk_cloud_logger/\n",
		"RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/rtk-cloud ./rtk-cloud\n",
	}
	validLayout := func(body string) bool {
		for _, directive := range directives {
			index := strings.Index(body, directive)
			if index < 0 {
				return false
			}
			body = body[index+len(directive):]
		}
		return true
	}
	if !validLayout(string(raw)) {
		t.Fatal("backup builder no longer preserves the workspace module layout before download/build")
	}
	for name, broken := range map[string]string{
		"flattened-workdir": strings.Replace(string(raw), directives[0], "WORKDIR /src\n", 1),
		"missing-manifests": strings.Replace(string(raw), directives[2], "", 1),
		"missing-source":    strings.Replace(string(raw), directives[5], "", 1),
		"late-manifests":    strings.Replace(string(raw), directives[2], "", 1) + directives[2],
	} {
		t.Run(name, func(t *testing.T) {
			if validLayout(broken) {
				t.Fatal("regression did not reject an unavailable or unreviewed module dependency")
			}
		})
	}
}

func TestPostgresBackupConfigBoundaries(t *testing.T) {
	base := postgresBackupTestConfig(t)
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*postgresBackupDeployment){
		"wrong namespace":        func(c *postgresBackupDeployment) { c.Namespace = "video-cloud-prod-platform" },
		"source pvc":             func(c *postgresBackupDeployment) { c.SourcePVC = "another" },
		"mutable runner":         func(c *postgresBackupDeployment) { c.RunnerImage = "postgres:16" },
		"worker environment":     func(c *postgresBackupDeployment) { c.Worker.Environment = "prod" },
		"source host":            func(c *postgresBackupDeployment) { c.Worker.Source.Host = "postgresql.prod.svc.cluster.local" },
		"privileged source role": func(c *postgresBackupDeployment) { c.Worker.Source.User = "postgres" },
		"small scratch":          func(c *postgresBackupDeployment) { c.ScratchStorage = "20Gi" },
		"short retention":        func(c *postgresBackupDeployment) { c.Worker.MinimumBackups = 2 },
		"arbitrary directory":    func(c *postgresBackupDeployment) { c.Worker.Directory = "/var/lib/postgresql/data" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if c.validate() == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestPostgresBackupCommandsKeepExplicitEnvironment(t *testing.T) {
	for _, command := range []string{"postgres-backup", "postgres-restore", "postgres-backup-worker"} {
		args := []string{command, "plan", "--environment", "staging", "--config", "backup.json"}
		got, err := normalizeEnvironmentArgs(args)
		if err != nil || !reflect.DeepEqual(args, got) {
			t.Fatalf("%s changed arguments: %#v %v", command, got, err)
		}
		if commands[command].run == nil {
			t.Fatalf("%s not registered", command)
		}
	}
}

func TestPostgresBackupPlanDoesNotCallCluster(t *testing.T) {
	c := postgresBackupTestConfig(t)
	path := filepath.Join(t.TempDir(), "backup.json")
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", "/no-such-kubectl")
	if err := runPostgresBackup([]string{"plan", "--environment", "staging", "--config", path}); err != nil {
		t.Fatal(err)
	}
	if err := runPostgresBackup([]string{"run", "--environment", "staging", "--config", path}); err == nil || !strings.Contains(err.Error(), "confirm-environment") {
		t.Fatalf("mutation must fail before cluster access: %v", err)
	}
	if _, err := readPostgresBackupDeployment(path, "prod"); err == nil {
		t.Fatal("wrong environment accepted")
	}
}

func TestPostgresBackupJobIsolationAndLimits(t *testing.T) {
	c := postgresBackupTestConfig(t)
	spec := postgresBackupJobSpec(c, "run", "")
	b, _ := json.Marshal(spec)
	text := string(b)
	for _, required := range []string{`"activeDeadlineSeconds":14400`, `"backoffLimit":0`, `"cpu":"500m"`, `"memory":"512Mi"`, `"runAsUser":70`, `"claimName":"rtk-postgres-backup-scratch"`, `"serviceAccountName":"rtk-postgres-backup"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %s in %s", required, text)
		}
	}
	for _, forbidden := range []string{"data-postgresql-0", "/var/lib/postgresql/data", "pods/exec", "agekey", "POSTGRES_PASSWORD"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe job value %s", forbidden)
		}
	}
	objects := postgresBackupObjects(c, false)
	var schedule map[string]any
	for _, obj := range objects {
		if obj["kind"] == "CronJob" {
			schedule = obj["spec"].(map[string]any)
		}
		if obj["kind"] == "Role" {
			b, _ := json.Marshal(obj)
			if strings.Contains(string(b), `"secrets"`) || strings.Contains(string(b), `"pods/exec"`) {
				t.Fatal("runner grants excessive Kubernetes permission")
			}
			for _, raw := range obj["rules"].([]any) {
				rule := raw.(map[string]any)
				resources := rule["resources"].([]string)
				if resources[0] == "pods" && (!reflect.DeepEqual(rule["verbs"], []string{"get"}) || !reflect.DeepEqual(rule["resourceNames"], []string{"postgresql-0"})) {
					t.Fatal("source Pod access is not restricted to one read-only target")
				}
			}
		}
	}
	if schedule["suspend"] != true || schedule["concurrencyPolicy"] != "Forbid" || schedule["schedule"] != "0 3 * * *" || schedule["timeZone"] != "Asia/Shanghai" {
		t.Fatalf("unsafe schedule %#v", schedule)
	}
}

func TestPostgresBackupScheduleAndQualificationGates(t *testing.T) {
	c := postgresBackupTestConfig(t)
	c.Schedule = "15 2 * * *"
	c.TimeZone = "UTC"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	c.Schedule = "* * * * *"
	if c.validate() == nil {
		t.Fatal("every-minute schedule accepted")
	}
	c.Schedule = "0 3 * * *"
	c.TimeZone = "Local"
	if c.validate() == nil {
		t.Fatal("host-local zone accepted")
	}
	c.TimeZone = "Asia/Shanghai"
	q := postgresBackupQualification{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, SourcePostgresImage: c.Worker.Source.PostgresImage, RunnerImage: c.RunnerImage, SystemIdentifier: c.Worker.Source.SystemIdentifier, MaxRate: "10M", BackupID: "backup-1", WorkloadID: "staging-fixed-100-rps", ReviewedBy: "operator", MeasuredAt: time.Now().Add(-time.Hour), Baseline: postgresBackupLoadMetrics{DurationSeconds: 300, Requests: 30000, P95MS: 100, P99MS: 200}, DuringBackup: postgresBackupLoadMetrics{DurationSeconds: 300, Requests: 30000, P95MS: 104, P99MS: 209}}
	status := postgresbackup.Status{Latest: &postgresbackup.Completion{Manifest: postgresbackup.Manifest{PostgresImage: c.Worker.Source.PostgresImage}}, LatestDrill: &postgresbackup.Drill{BackupID: "backup-1", PostgresImage: c.Worker.Source.PostgresImage, Success: true}}
	path := filepath.Join(t.TempDir(), "qualification.json")
	write := func(v postgresBackupQualification) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(q)
	if err := postgresBackupValidateQualification(c, path, status); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*postgresBackupQualification){"latency": func(q *postgresBackupQualification) { q.DuringBackup.P95MS = 106 }, "errors": func(q *postgresBackupQualification) { q.DuringBackup.Errors = 1 }, "wrong runner": func(q *postgresBackupQualification) { q.RunnerImage = "different" }, "no requests": func(q *postgresBackupQualification) { q.Baseline.Requests = 0 }, "unmatched load": func(q *postgresBackupQualification) { q.DuringBackup.Requests = 1000 }, "wrong backup": func(q *postgresBackupQualification) { q.BackupID = "unknown" }} {
		t.Run(name, func(t *testing.T) {
			bad := q
			mutate(&bad)
			write(bad)
			if postgresBackupValidateQualification(c, path, status) == nil {
				t.Fatal("unqualified schedule enabling accepted")
			}
		})
	}
}

func TestPostgresBackupNetworkPolicyPreservesApplicationAccess(t *testing.T) {
	c := postgresBackupTestConfig(t)
	for _, missing := range []bool{false, true} {
		m := postgresBackupManager{Config: c, Exec: func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
			from := []any{}
			for _, suffix := range []string{"video-cloud", "account-manager", "billing"} {
				if missing && suffix == "billing" {
					continue
				}
				from = append(from, map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": c.Stack + "-" + suffix}}})
			}
			return json.NewEncoder(out).Encode(map[string]any{"metadata": map[string]any{"labels": map[string]string{"rtk.realtek.com/stack": c.Stack}}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "postgresql"}}, "ingress": []any{map[string]any{"from": from, "ports": []any{map[string]any{"protocol": "TCP", "port": 5432}}}}}})
		}}
		if err := m.validateSourceNetworkPolicy(context.Background()); (err != nil) != missing {
			t.Fatalf("missing=%t result=%v", missing, err)
		}
	}
}

func TestPostgresRestoreChecksRolesDataAndOutbox(t *testing.T) {
	checks := postgresBackupDataChecks()
	seen := map[string]bool{}
	query := func(db, sql string) (string, error) {
		for _, check := range checks {
			if check.Database == db && check.SQL == sql {
				seen[check.Name] = true
				if !strings.HasPrefix(sql, "SELECT ") {
					t.Fatal("non-read-only recovery check")
				}
				return check.Expected, nil
			}
		}
		return "", errors.New("unexpected query")
	}
	if err := postgresBackupCheckRestoredData(query); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 7 {
		t.Fatalf("missing checks %v", seen)
	}
	for _, failed := range checks {
		t.Run(failed.Name, func(t *testing.T) {
			err := postgresBackupCheckRestoredData(func(db, sql string) (string, error) {
				if sql == failed.SQL {
					return "", errors.New("missing table/role or unreadable data")
				}
				return query(db, sql)
			})
			if err == nil {
				t.Fatal("missing role/data accepted")
			}
		})
	}
	if postgresBackupCheckRestoredData(func(string, string) (string, error) { return "", nil }) == nil {
		t.Fatal("empty query result accepted")
	}
}

func TestPostgresBackupHBAIdempotentPreservesOtherRules(t *testing.T) {
	start := "# original\nlocal all all trust\nhost all all 10.0.0.0/8 scram-sha-256\n"
	first, err := postgresBackupHBA(start)
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgresBackupHBA(first)
	if err != nil || first != second {
		t.Fatalf("HBA update not idempotent: %v\n%s\n%s", err, first, second)
	}
	if !strings.HasPrefix(first, start) || strings.Count(first, "host replication rtk_postgres_backup all scram-sha-256") != 1 {
		t.Fatal("existing rules were changed")
	}
	if _, err := postgresBackupHBA(start + "# BEGIN RTK POSTGRES BACKUP\n"); err == nil {
		t.Fatal("broken ownership block accepted")
	}
}

func TestPostgresBackupPreflightChecksActualSource(t *testing.T) {
	for _, test := range []struct {
		name, image, capacity, identity string
		fail                            bool
	}{{"valid", "", "20Gi", "123456789", false}, {"wrong image", "postgres:16", "20Gi", "123456789", true}, {"undersized scratch", "", "40Gi", "123456789", true}, {"wrong database", "", "20Gi", "987654321", true}} {
		t.Run(test.name, func(t *testing.T) {
			c := postgresBackupTestConfig(t)
			image := test.image
			if image == "" {
				image = c.Worker.Source.PostgresImage
			}
			m := postgresBackupManager{Config: c, Exec: func(_ context.Context, args []string, in io.Reader, out io.Writer) error {
				joined := strings.Join(args, " ")
				switch {
				case strings.Contains(joined, "get pod"):
					json.NewEncoder(out).Encode(map[string]any{"spec": map[string]any{"containers": []any{map[string]string{"name": "postgres", "image": "postgres:16-alpine"}}, "volumes": []any{map[string]any{"persistentVolumeClaim": map[string]string{"claimName": c.SourcePVC}}}}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": "postgres", "imageID": "docker-pullable://" + image, "ready": true}}}})
				case strings.Contains(joined, "get pvc"):
					json.NewEncoder(out).Encode(map[string]any{"status": map[string]any{"capacity": map[string]string{"storage": test.capacity}}})
				case strings.Contains(joined, "psql"):
					io.WriteString(out, "16|"+test.identity+"|replica|10|10\n")
				default:
					return errors.New("unexpected command")
				}
				return nil
			}}
			err := m.validateSource(context.Background())
			if (err != nil) != test.fail {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}

func TestPostgresBackupScratchRetainsUnpublishedArchives(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "old.age")
	if err := os.WriteFile(archive, []byte("encrypted"), 0600); err != nil {
		t.Fatal(err)
	}
	if postgresBackupScratchReady(dir) == nil {
		t.Fatal("new capture allowed with pending upload")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal("pending encrypted backup deleted")
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".capture-postgres-old")
	if err := os.Mkdir(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := postgresBackupScratchReady(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale owned temporary capture not removed")
	}
}

func TestPostgresRestoreDrillHasNoSourceCredentialsOrRoute(t *testing.T) {
	c := postgresBackupTestConfig(t)
	ns, err := postgresBackupDrillNamespace(c, "test-1")
	if err != nil {
		t.Fatal(err)
	}
	objects := postgresBackupDrillObjects(c, ns, "backup-123", map[string]string{"identity.agekey": "private-fixture", "ACCESS_KEY_ID": "readonly", "SECRET_ACCESS_KEY": "readonly-secret"})
	for _, obj := range objects {
		if obj["metadata"].(map[string]any)["namespace"] != ns {
			t.Fatal("drill object escaped isolated namespace")
		}
		if obj["kind"] == "Job" {
			b, _ := json.Marshal(obj)
			text := string(b)
			for _, bad := range []string{"source-password", "data-postgresql-0", `"serviceAccountName"`, `"automountServiceAccountToken":true`} {
				if strings.Contains(text, bad) {
					t.Fatalf("drill contains %s", bad)
				}
			}
			if !strings.Contains(text, `"automountServiceAccountToken":false`) {
				t.Fatal("drill token mounted")
			}
		}
	}
	if _, err := postgresBackupDrillNamespace(c, "../../platform"); err == nil {
		t.Fatal("unsafe drill id accepted")
	}
	m := postgresBackupManager{Config: c, Exec: func(_ context.Context, args []string, _ io.Reader, out io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "delete") {
			t.Fatal("deleted unowned namespace")
		}
		io.WriteString(out, `{"metadata":{"uid":"1","labels":{"rtk.realtek.com/purpose":"application"}}}`)
		return nil
	}}
	if m.cleanupDrill(context.Background(), "test-1") == nil {
		t.Fatal("cleanup accepted unrelated namespace")
	}
}
