package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// runIsolatedMigrationJob is deliberately limited to the original pre-issuer
// migration Jobs: one container, no volumes, and no service-account token.
// It does not fabricate Kubernetes status, parent receipts, or ACKs. The caller
// owns the test-labeled Docker network and synthetic Secret inventory.
type isolatedMigrationRuntime struct {
	ContainerID string
	ImageID     string
	ExitCode    int
}

func runIsolatedMigrationJob(ctx context.Context, docker, root, network, owner string, job deploymentplan.Object, selected map[string]bool, secrets map[string]map[string]string, logPath string, observers ...func(isolatedMigrationRuntime) error) (int, error) {
	bad := func() (int, error) {
		return -1, errors.New("isolated migration Job is outside the supported execution envelope")
	}
	if len(observers) > 1 || !filepath.IsAbs(docker) || !filepath.IsAbs(root) || !filepath.IsAbs(logPath) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(network) || owner == "" || job["kind"] != "Job" {
		return bad()
	}
	rel, err := filepath.Rel(root, logPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return bad()
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return bad()
	}
	check := exec.CommandContext(ctx, docker, "network", "inspect", "--format", `{{ index .Labels "rtk.test.owner" }}`, network)
	raw, err := check.Output()
	if err != nil || strings.TrimSpace(string(raw)) != owner {
		return bad()
	}
	spec := deploymentplan.Map(job["spec"])
	pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	allowedPod := map[string]bool{"restartPolicy": true, "automountServiceAccountToken": true, "imagePullSecrets": true, "securityContext": true, "containers": true}
	for k := range pod {
		if !allowedPod[k] {
			return bad()
		}
	}
	containers := certIssuerObjectList(pod["containers"])
	if pod["restartPolicy"] != "Never" || pod["automountServiceAccountToken"] != false || len(containers) != 1 {
		return bad()
	}
	container := deploymentplan.Map(containers[0])
	allowedContainer := map[string]bool{"name": true, "image": true, "imagePullPolicy": true, "command": true, "args": true, "env": true, "envFrom": true, "securityContext": true, "resources": true}
	for k := range container {
		if !allowedContainer[k] {
			return bad()
		}
	}
	image := deploymentplan.String(container["image"])
	if !immutableEnvironmentImage.MatchString(image) || !selected[image] {
		return bad()
	}
	runtimeImage := image
	// Docker's classic store has this index pinned to an existing ARM test
	// container. Use its verified amd64 child without replacing that container
	// or changing the original Kubernetes image selection.
	if image == "docker.io/library/postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685" {
		index, err := os.ReadFile("/private/tmp/rtk-postgres-index-cf78-20261111.json")
		var manifest struct {
			Manifests []struct {
				Digest   string                            `json:"digest"`
				Platform struct{ OS, Architecture string } `json:"platform"`
			} `json:"manifests"`
		}
		if err != nil || environmentPreflightBytesSHA256(index) != "cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685" || json.Unmarshal(index, &manifest) != nil {
			return bad()
		}
		verified := false
		for _, child := range manifest.Manifests {
			if child.Platform.OS == "linux" && child.Platform.Architecture == "amd64" && child.Digest == "sha256:075f7ba66bc9b3ce7d6b8b635208ff61cd7cf1a67d71ec530eec5d7ae0cbe571" {
				verified = true
			}
		}
		if !verified {
			return bad()
		}
		runtimeImage = "docker.io/library/postgres@sha256:075f7ba66bc9b3ce7d6b8b635208ff61cd7cf1a67d71ec530eec5d7ae0cbe571"
	}
	command := certIssuerObjectList(container["command"])
	if len(command) == 0 || deploymentplan.String(command[0]) == "" {
		return bad()
	}
	var program []string
	for _, value := range append(command, certIssuerObjectList(container["args"])...) {
		s, ok := value.(string)
		if !ok || s == "" || strings.ContainsRune(s, '\x00') {
			return bad()
		}
		program = append(program, s)
	}
	values := map[string]string{}
	validName := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	put := func(name, value string) bool {
		if !validName.MatchString(name) || strings.ContainsAny(value, "\n\r\x00") {
			return false
		}
		if _, exists := values[name]; exists {
			return false
		}
		values[name] = value
		return true
	}
	for _, entry := range certIssuerObjectList(container["envFrom"]) {
		item := deploymentplan.Map(entry)
		if len(item) != 1 {
			return bad()
		}
		ref := deploymentplan.Map(item["secretRef"])
		if len(ref) != 1 {
			return bad()
		}
		secret, exists := secrets[deploymentplan.String(ref["name"])]
		if !exists {
			return bad()
		}
		for k, v := range secret {
			if !put(k, v) {
				return bad()
			}
		}
	}
	for _, entry := range certIssuerObjectList(container["env"]) {
		item := deploymentplan.Map(entry)
		name := deploymentplan.String(item["name"])
		value, direct := item["value"].(string)
		if direct && len(item) != 2 {
			return bad()
		}
		if !direct {
			if len(item) != 2 {
				return bad()
			}
			from := deploymentplan.Map(item["valueFrom"])
			ref := deploymentplan.Map(from["secretKeyRef"])
			if len(from) != 1 || len(ref) != 2 {
				return bad()
			}
			secret, exists := secrets[deploymentplan.String(ref["name"])]
			value, direct = secret[deploymentplan.String(ref["key"])]
			if !exists || !direct {
				return bad()
			}
		}
		if !put(name, value) {
			return bad()
		}
	}
	env, err := os.CreateTemp(root, "job-env-")
	if err != nil {
		return -1, err
	}
	defer os.Remove(env.Name())
	if err = env.Chmod(0600); err == nil {
		for _, name := range sortedMapKeys(values) {
			_, err = fmt.Fprintf(env, "%s=%s\n", name, values[name])
			if err != nil {
				break
			}
		}
	}
	if closeErr := env.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return -1, err
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return -1, err
	}
	defer log.Close()
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return -1, err
	}
	cidPath := filepath.Join(root, "job-"+hex.EncodeToString(nonce[:])+".cid")
	defer func() {
		defer os.Remove(cidPath)
		idBytes, readErr := os.ReadFile(cidPath)
		id := strings.TrimSpace(string(idBytes))
		if readErr != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
			return
		}
		label, inspectErr := exec.Command(docker, "inspect", "--format", `{{ index .Config.Labels "rtk.test.owner" }}`, id).Output()
		if inspectErr == nil && strings.TrimSpace(string(label)) == owner {
			_ = exec.Command(docker, "rm", "--force", "--volumes", id).Run()
		}
	}()
	args := []string{"run", "--platform", "linux/amd64", "--pull=never", "--cidfile", cidPath, "--name", "rtk-migration-" + hex.EncodeToString(nonce[:]), "--label", "rtk.test.owner=" + owner, "--network", network, "--env-file", env.Name()}
	podSecurity := deploymentplan.Map(pod["securityContext"])
	for k := range podSecurity {
		if k != "runAsUser" && k != "runAsGroup" && k != "runAsNonRoot" && k != "fsGroup" && k != "seccompProfile" {
			return bad()
		}
	}
	if podSecurity["seccompProfile"] != nil && deploymentplan.Map(podSecurity["seccompProfile"])["type"] != "RuntimeDefault" {
		return bad()
	}
	if user, ok := podSecurity["runAsUser"].(float64); ok {
		group, groupOK := podSecurity["runAsGroup"].(float64)
		if !groupOK || user <= 0 || group <= 0 || user != float64(int64(user)) || group != float64(int64(group)) {
			return bad()
		}
		args = append(args, "--user", fmt.Sprintf("%d:%d", int64(user), int64(group)))
	} else if podSecurity["runAsUser"] != nil || podSecurity["runAsGroup"] != nil || podSecurity["runAsNonRoot"] == true {
		return bad()
	}
	resources := deploymentplan.Map(container["resources"])
	for name, raw := range resources {
		if name != "limits" && name != "requests" {
			return bad()
		}
		for resource, value := range deploymentplan.Map(raw) {
			text := deploymentplan.String(value)
			switch resource {
			case "cpu":
				if !regexp.MustCompile(`^[0-9]+(\.[0-9]+)?m?$`).MatchString(text) {
					return bad()
				}
				amount := strings.TrimSuffix(text, "m")
				number, parseErr := strconv.ParseFloat(amount, 64)
				if parseErr != nil || number <= 0 {
					return bad()
				}
				if strings.HasSuffix(text, "m") {
					number /= 1000
				}
				if name == "limits" {
					args = append(args, "--cpus", strconv.FormatFloat(number, 'f', -1, 64))
				}
			case "memory":
				match := regexp.MustCompile(`^([1-9][0-9]*)(Ki|Mi|Gi)$`).FindStringSubmatch(text)
				if len(match) != 3 {
					return bad()
				}
				number, parseErr := strconv.ParseUint(match[1], 10, 52)
				if parseErr != nil {
					return bad()
				}
				multiplier := map[string]uint64{"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024}[match[2]]
				if number > (^uint64(0))/multiplier {
					return bad()
				}
				if name == "limits" {
					args = append(args, "--memory", strconv.FormatUint(number*multiplier, 10))
				}
			default:
				return bad()
			}
		}
	}
	// Requests affect Kubernetes scheduling, which this isolated process runner
	// does not simulate; the actual CPU and memory limits are enforced above.
	security := deploymentplan.Map(container["securityContext"])
	for k := range security {
		if k != "allowPrivilegeEscalation" && k != "capabilities" && k != "readOnlyRootFilesystem" {
			return bad()
		}
	}
	if security["allowPrivilegeEscalation"] == false {
		args = append(args, "--security-opt", "no-new-privileges")
	} else if security["allowPrivilegeEscalation"] != nil {
		return bad()
	}
	if security["readOnlyRootFilesystem"] == true {
		args = append(args, "--read-only")
	} else if security["readOnlyRootFilesystem"] != nil {
		return bad()
	}
	if caps := deploymentplan.Map(security["capabilities"]); len(caps) > 0 {
		drop := certIssuerObjectList(caps["drop"])
		if len(caps) != 1 || len(drop) != 1 || drop[0] != "ALL" {
			return bad()
		}
		args = append(args, "--cap-drop", "ALL")
	}
	args = append(args, "--entrypoint", program[0], runtimeImage)
	args = append(args, program[1:]...)
	cmd := exec.CommandContext(ctx, docker, args...)
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return -1, err
		}
		code = exit.ExitCode()
	}
	if len(observers) == 1 {
		idBytes, err := os.ReadFile(cidPath)
		id := strings.TrimSpace(string(idBytes))
		if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
			return bad()
		}
		// Inspect the actual exited container before the owner-checked cleanup.
		// Do not retain its environment or unfiltered Docker configuration.
		format := `[{{json .Id}},{{json .Image}},{{json .Config.Image}},{{json .State.ExitCode}},{{json (index .Config.Labels "rtk.test.owner")}}]`
		raw, err := exec.CommandContext(ctx, docker, "inspect", "--format", format, id).Output()
		var actual []any
		if err != nil || json.Unmarshal(raw, &actual) != nil || len(actual) != 5 || actual[0] != id || actual[2] != runtimeImage || actual[3] != float64(code) || actual[4] != owner || !deploymentImageDigestPattern.MatchString(deploymentplan.String(actual[1])) {
			return bad()
		}
		if err := observers[0](isolatedMigrationRuntime{ContainerID: "docker://" + id, ImageID: deploymentplan.String(actual[1]), ExitCode: code}); err != nil {
			return -1, err
		}
	}
	return code, nil
}

// This test uses a real local PostgreSQL server and the exact selected PKI
// image. Only the Kubernetes Job/Secret envelope is synthetic; no readiness,
// deployment receipt or PKI ACK is inferred from the container result.
func TestIsolatedMigrationJobRunsRealPostgresAndPKIImage(t *testing.T) {
	if os.Getenv("RTK_TEST_REAL_MIGRATION_JOB") != "1" {
		t.Skip("explicit isolated migration integration required")
	}
	pkiImage, postgresImage, accountImage := os.Getenv("RTK_TEST_PKI_IMAGE"), os.Getenv("RTK_TEST_POSTGRES_IMAGE"), os.Getenv("RTK_TEST_ACCOUNT_IMAGE")
	if !immutableEnvironmentImage.MatchString(pkiImage) || !immutableEnvironmentImage.MatchString(postgresImage) || !immutableEnvironmentImage.MatchString(accountImage) {
		t.Fatal("exact selected immutable fixture images required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	owner := "rtk-migration-fixture-" + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, docker, "network", "create", "--label", "rtk.test.owner="+owner, owner).Output()
	if err != nil {
		t.Fatal("isolated network creation failed")
	}
	network := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command(docker, "network", "rm", network).Run() })
	password := hex.EncodeToString(nonce[:])
	envPath := filepath.Join(root, "postgres.env")
	if err := os.WriteFile(envPath, []byte("POSTGRES_USER=postgres\nPOSTGRES_DB=video_cloud\nPOSTGRES_PASSWORD="+password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := "postgresql.video-cloud-dev-platform.svc"
	out, err = exec.CommandContext(ctx, docker, "run", "--detach", "--rm", "--pull=never", "--name", owner, "--label", "rtk.test.owner="+owner, "--network", network, "--network-alias", alias, "--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "--env-file", envPath, postgresImage).Output()
	_ = os.Remove(envPath)
	if err != nil {
		t.Fatal("isolated PostgreSQL startup failed")
	}
	postgres := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "--force", "--volumes", postgres).Run() })
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if exec.CommandContext(ctx, docker, "exec", postgres, "pg_isready", "-U", "postgres", "-d", "video_cloud").Run() == nil {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated real PostgreSQL did not become ready")
	}
	job := deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{
		"restartPolicy": "Never", "automountServiceAccountToken": false,
		"securityContext": deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001), "runAsNonRoot": true, "fsGroup": float64(10001), "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}},
		"containers": []any{deploymentplan.Object{"name": "pki-migrate", "image": pkiImage, "resources": deploymentplan.Object{"limits": deploymentplan.Object{"cpu": "1", "memory": "512Mi"}, "requests": deploymentplan.Object{"cpu": "100m", "memory": "128Mi"}}, "command": []any{"/app/pkicontroller"}, "args": []any{"migrate-runtime"}, "env": []any{
			deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": "dev"},
			deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-migration-database", "key": "url"}}},
		}, "securityContext": deploymentplan.Object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}}},
	}}}}
	secrets := map[string]map[string]string{"pki-migration-database": {"url": "postgres://postgres:" + password + "@" + alias + ":5432/video_cloud?sslmode=disable"}}
	code, err := runIsolatedMigrationJob(ctx, docker, root, network, owner, job, map[string]bool{pkiImage: true}, secrets, filepath.Join(root, "migration.log"))
	if err != nil || code != 0 {
		t.Fatalf("real migration Job failed: exit=%d error=%v (private log retained until fixture cleanup)", code, err)
	}
	readback, err := exec.CommandContext(ctx, docker, "exec", postgres, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "video_cloud", "-c", "SELECT to_regclass('public.pki_issuers') IS NOT NULL").Output()
	if err != nil || strings.TrimSpace(string(readback)) != "t" {
		t.Fatal("real PostgreSQL did not contain the migrated PKI issuer table")
	}
	if err := exec.CommandContext(ctx, docker, "exec", postgres, "createdb", "-U", "postgres", "rtk_account_manager").Run(); err != nil {
		t.Fatal("isolated Account database creation failed")
	}
	accountJob := deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{
		"restartPolicy": "Never", "automountServiceAccountToken": false,
		"containers": []any{deploymentplan.Object{"name": "migrate", "image": accountImage, "command": []any{"/app/rtk-account-manager-migrate"},
			"envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "account-manager-runtime"}}}}},
	}}}}
	accountSecret := map[string]map[string]string{"account-manager-runtime": {
		"DATABASE_URL":      "postgres://postgres:" + password + "@" + alias + ":5432/rtk_account_manager?sslmode=disable",
		"JWT_ACCESS_SECRET": "isolated-access-" + password, "JWT_REFRESH_SECRET": "isolated-refresh-" + password,
		"ACCOUNT_MANAGER_ENV": "dev", "AUTH_TOKEN_BASE_URL": "https://account.example.test",
		"SENDMAIL_HTTP_BASE_URL": "https://sendmail.example.test", "SENDMAIL_HTTP_BEARER_TOKEN": "isolated-mail-" + password,
		"SENDMAIL_HTTP_TIMEOUT": "15s", "EMAIL_OUTBOX_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
	}}
	if _, err := runIsolatedMigrationJob(ctx, docker, root, network, owner, accountJob, map[string]bool{accountImage: true}, nil, filepath.Join(root, "missing-secret.log")); err == nil {
		t.Fatal("missing envFrom Secret was accepted")
	}
	code, err = runIsolatedMigrationJob(ctx, docker, root, network, owner, accountJob, map[string]bool{accountImage: true}, accountSecret, filepath.Join(root, "account-migration.log"))
	if err != nil || code != 0 {
		t.Fatalf("real Account migration Job failed: exit=%d error=%v (private log retained until fixture cleanup)", code, err)
	}
	readback, err = exec.CommandContext(ctx, docker, "exec", postgres, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "rtk_account_manager", "-c", "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Output()
	if err != nil || strings.TrimSpace(string(readback)) != "t" {
		t.Fatal("real PostgreSQL did not contain the Account migration table")
	}
	// The Kubernetes adapter may report completion only after the same real
	// migration executable returns; a repeated wait must not rerun the Job.
	store := makeIsolatedTestSecretStore(t, "dev")
	namespace := "video-cloud-dev-account-manager"
	uid := "11111111-aaaa-4aaa-8aaa-111111111111"
	accountJob["metadata"] = deploymentplan.Object{"name": "account-manager-migrate", "namespace": namespace, "uid": uid, "resourceVersion": "1"}
	encoded := deploymentplan.Object{}
	for name, value := range accountSecret["account-manager-runtime"] {
		encoded[name] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	secret := deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": "account-manager-runtime", "namespace": namespace, "uid": "22222222-aaaa-4aaa-8aaa-222222222222"}, "data": encoded}
	failNamespace := "video-cloud-dev-video-cloud"
	failedUID := "33333333-aaaa-4aaa-8aaa-333333333333"
	failedJob := deploymentplan.Object{"kind": "Job", "metadata": deploymentplan.Object{"name": "pki-grants", "namespace": failNamespace, "uid": failedUID, "resourceVersion": "1"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "failed", "image": pkiImage, "command": []any{"/bin/sh", "-ec", "exit 17"}}}}}}}
	timeoutUID := "44444444-aaaa-4aaa-8aaa-444444444444"
	timeoutJob := deploymentplan.Object{"kind": "Job", "metadata": deploymentplan.Object{"name": "pki-migrate", "namespace": failNamespace, "uid": timeoutUID, "resourceVersion": "1"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "slow", "image": pkiImage, "command": []any{"/bin/sh", "-ec", "sleep 2"}}}}}}}
	objects := map[string]deploymentplan.Object{"Job/" + namespace + "/account-manager-migrate": accountJob, "Secret/" + namespace + "/account-manager-runtime": secret, "Job/" + failNamespace + "/pki-grants": failedJob, "Job/" + failNamespace + "/pki-migrate": timeoutJob}
	backend, objectDir, _ := unifiedSessionNativeBackend(t, store, objects, nil)
	backend.RolloutTimeout = time.Minute
	if err := store.write("kube/kubeconfig.yaml", backend.KubeconfigContent, false); err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{"pki-migrate": pkiImage, "pki-grants": pkiImage, "pki-create-database-logins": postgresImage, "account-manager-migrate": accountImage}
	controller, bridgePort := installIsolatedMigrationController(t, store, objectDir, docker, network, owner, "video-cloud-dev", selected)
	verifyIsolatedMigrationControllerSandbox(t, controller, bridgePort)
	// The underlying file-backed fixture used to return success for every
	// rollout and PVC wait. The controller must not let those claims through.
	for _, id := range []string{"Deployment/video-cloud-dev-video-cloud/unimplemented", "StatefulSet/video-cloud-dev-secrets/openbao", "PersistentVolumeClaim/video-cloud-dev-video-cloud/unimplemented"} {
		if err := backend.Wait(ctx, deploymentplan.Resource{ID: id, Action: "create"}); err == nil {
			t.Fatal("unimplemented readiness was reported successful", id)
		}
	}
	resource := deploymentplan.Resource{ID: "Job/" + namespace + "/account-manager-migrate", Action: "create", UID: uid, Desired: accountJob}
	if err := backend.Wait(ctx, resource); err != nil {
		t.Fatal("real Account migration controller did not complete", err)
	}
	if err := backend.Wait(ctx, resource); err != nil {
		t.Fatal("same Job wait reran or lost its result", err)
	}
	actual, err := backend.Read(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	conditions := certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"])
	if len(conditions) != 1 || deploymentplan.Map(conditions[0])["type"] != "Complete" || deploymentplan.Map(conditions[0])["status"] != "True" || len(controller.runs) != 1 {
		t.Fatal("controller did not retain the single actual Job outcome")
	}
	if result := controller.wait(namespace, "unknown-job"); !result.Error {
		t.Fatal("unknown Job was accepted")
	}
	// A previously recorded intent without an observed result must never be
	// replayed by a newly started controller.
	pendingIntent := filepath.Join(controller.root, "intent-"+failedUID)
	if err := os.WriteFile(pendingIntent, []byte("original in-flight"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := &isolatedMigrationController{objects: controller.objects, root: controller.root, docker: controller.docker, network: controller.network, owner: controller.owner, stack: controller.stack, selected: controller.selected, runs: map[string]*isolatedMigrationPending{}}
	if result := restarted.wait(failNamespace, "pki-grants"); !result.Error {
		t.Fatal("unknown interrupted Job was replayed")
	}
	if err := os.Remove(pendingIntent); err != nil {
		t.Fatal(err)
	}
	timeoutResource := deploymentplan.Resource{ID: "Job/" + failNamespace + "/pki-migrate", Action: "create", UID: timeoutUID, Desired: timeoutJob}
	backend.RolloutTimeout = time.Second
	if err := backend.Wait(ctx, timeoutResource); err == nil {
		t.Fatal("wait ignored its configured timeout")
	}
	backend.RolloutTimeout = time.Minute
	if err := backend.Wait(ctx, timeoutResource); err != nil {
		t.Fatal("timed-out real Job did not resume observation", err)
	}
	failedResource := deploymentplan.Resource{ID: "Job/" + failNamespace + "/pki-grants", Action: "create", UID: failedUID, Desired: failedJob}
	if err := backend.Wait(ctx, failedResource); err == nil {
		t.Fatal("failed real Job was reported ready")
	}
	failedActual, err := backend.Read(ctx, failedResource.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedConditions := certIssuerObjectList(deploymentplan.Map(failedActual["status"])["conditions"])
	if len(failedConditions) != 1 || deploymentplan.Map(failedConditions[0])["type"] != "Failed" {
		t.Fatal("real nonzero Job result was not recorded as failed")
	}
	verifyIsolatedMigrationControllerThroughShell(t, docker, network, owner, selected, accountJob, accountSecret, failedJob)
	// A genuine nonzero container exit must never be recorded as Job success.
	pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(job["spec"])["template"])["spec"])
	pod["securityContext"] = deploymentplan.Object{}
	pod["containers"] = []any{deploymentplan.Object{"name": "failure", "image": postgresImage, "command": []any{"/bin/sh", "-ec", "exit 17"}}}
	code, err = runIsolatedMigrationJob(ctx, docker, root, network, owner, job, map[string]bool{postgresImage: true}, nil, filepath.Join(root, "failure.log"))
	if err != nil || code != 17 {
		t.Fatalf("real failed Job did not retain exit status: exit=%d error=%v", code, err)
	}
	if _, err := runIsolatedMigrationJob(ctx, docker, root, network, "wrong-owner", job, map[string]bool{postgresImage: true}, nil, filepath.Join(root, "foreign.log")); err == nil {
		t.Fatal("foreign network owner was accepted")
	}
}
