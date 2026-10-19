package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReleaseProgressUsesOnlyMaintainedRolesAndSteps(t *testing.T) {
	for _, test := range []struct{ key, step, want string }{
		{"workspace", "checkout-head", "deployment.release.verify.workspace.checkout-head"},
		{"LKE_VIDEO_CLOUD_IMAGE", "ci-runs", "deployment.release.verify.video-cloud-api.ci-runs"},
		{"LKE_MQTT_PKI_IMAGE", "artifact-download", "deployment.release.verify.mqtt-pki.artifact-download"},
		{"LKE_OPENBAO_PKI_IMAGE", "artifact-download", "deployment.release.verify.openbao-pki.artifact-download"},
		{"LKE_POSTGRES_BACKUP_RUNNER_IMAGE", "artifact-list", "deployment.release.verify.postgres-backup.artifact-list"},
		{"LKE_ZAMMAD_ELASTICSEARCH_IMAGE", "pull", "deployment.release.verify.zammad-elasticsearch.pull"},
		{"private-role\nhttps://private-url", "oci-labels", "deployment.release.verify.selected-image.oci-labels"},
		{"LKE_VIDEO_CLOUD_IMAGE", "private-step", ""},
	} {
		if got := environmentReleaseProgressStage(test.key, test.step); got != test.want {
			t.Fatal("release progress accepted an unmaintained role or step", got, test.want)
		}
	}
}

func TestReleaseProgressCommandPreservesCallsAndOmitsPrivateInputs(t *testing.T) {
	for _, test := range []struct {
		name, command, step string
		args                []string
		failure             bool
	}{
		{"checkout-head", "git", "checkout-head", []string{"-C", "/private-checkout", "rev-parse", "HEAD"}, false},
		{"workspace-pin", "git", "workspace-pin", []string{"-C", "/private-checkout", "ls-tree", "private-revision", "--", "private-service"}, false},
		{"pull", "docker", "pull", []string{"pull", "--platform", "linux/amd64", "private-image-ref"}, false},
		{"oci-labels", "docker", "oci-labels", []string{"image", "inspect", "private-image-ref", "--format", "{{json .Config.Labels}}"}, false},
		{"ci-runs", "gh", "ci-runs", []string{"api", "repos/private-repo/actions/workflows/ci.yml/runs?private-token"}, false},
		{"release-runs", "gh", "release-runs", []string{"api", "repos/private-repo/actions/workflows/release.yml/runs?private-token"}, false},
		{"publication-jobs", "gh", "publication-jobs", []string{"api", "repos/private-repo/actions/runs/private-id/jobs?private-token"}, false},
		{"workspace-baseline-runs", "gh", "workspace-baseline-runs", []string{"api", "repos/private-repo/actions/workflows/workspace-test-baseline.yml/runs?private-token"}, false},
		{"backup-runs", "gh", "backup-runs", []string{"api", "repos/private-repo/actions/workflows/postgres-backup-image.yml/runs?private-token"}, false},
		{"release-run", "gh", "release-run", []string{"api", "repos/private-repo/actions/runs/private-id"}, false},
		{"artifact-download", "gh", "artifact-download", []string{"api", "repos/private-repo/actions/artifacts/private-id/zip"}, false},
		{"failed-artifact-list", "gh", "artifact-list", []string{"api", "repos/private-repo/actions/runs/private-id/artifacts?private-token"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			primary := errors.New("private-provider-error")
			calls := 0
			run := environmentReleaseProgressCommand("private-role", func(ctx context.Context, command string, args ...string) ([]byte, error) {
				calls++
				progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
				progress.mu.Lock()
				active := len(progress.operations)
				progress.mu.Unlock()
				if active != 1 {
					t.Fatal("actual release command was not tracked as active", active)
				}
				if command != test.command || !reflect.DeepEqual(args, test.args) {
					t.Fatal("progress changed the prerequisite command", command, args)
				}
				if test.failure {
					return nil, primary
				}
				return []byte("private-provider-body"), nil
			}, nil)
			err := runEnvironmentProgressInterval(context.Background(), &output, "deployment.release", time.Hour, func(ctx context.Context) error {
				body, err := run(ctx, test.command, test.args...)
				progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
				progress.mu.Lock()
				active, settled := len(progress.operations), progress.settled
				progress.mu.Unlock()
				if active != 0 || settled != 1 {
					t.Fatal("settled release command retained activity or lost its completion", active, settled)
				}
				if !test.failure && string(body) != "private-provider-body" {
					t.Fatal("progress changed private prerequisite output")
				}
				return err
			})
			if calls != 1 || test.failure != errors.Is(err, primary) || !strings.Contains(output.String(), "deployment.release.verify.selected-image."+test.step) || strings.Contains(output.String(), "private-") {
				t.Fatal("progress changed verification outcome or exposed private inputs", err, output.String())
			}
		})
	}
	if environmentReleaseProgressCommand("workspace", nil, nil) != nil {
		t.Fatal("progress hid a missing command adapter")
	}
}

func TestReleaseProgressRejectsUnknownCommandShapesWithoutSkippingCommand(t *testing.T) {
	for _, test := range []struct {
		command string
		args    []string
	}{
		{"git", []string{"-C", "/private-checkout", "rev-parse", "private-revision"}},
		{"git", []string{"-C"}},
		{"docker", []string{"private-operation"}},
		{"gh", []string{"api", "repos/private-repo/actions/workflows/private-workflow/runs"}},
		{"gh", []string{"api", "repos/private-repo/actions/runs//artifacts"}},
		{"gh", []string{"api", "https://private-url/actions/runs/7"}},
		{"private-command", []string{"private-argument"}},
	} {
		advanced, calls := 0, 0
		run := environmentReleaseProgressCommand("private-role", func(_ context.Context, command string, args ...string) ([]byte, error) {
			calls++
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatal("unclassified command was changed")
			}
			return []byte("private-body"), nil
		}, func(string) { advanced++ })
		body, err := run(context.Background(), test.command, test.args...)
		if err != nil || string(body) != "private-body" || calls != 1 || advanced != 0 {
			t.Fatal("unmaintained command produced progress or lost verification", calls, advanced, err)
		}
	}
}

func TestReleaseProgressDistinctActionsRetainEveryVerificationCall(t *testing.T) {
	revision, digest := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
	repo := "hkt999rtk/rtk_video_cloud"
	cfg := deploymentConfig{Workspace: "/private-frozen", Environment: "dev"}
	release := environmentRelease{WorkspaceRevision: revision, Images: map[string]environmentReleaseImage{}}
	for key, pkg := range map[string]string{
		"LKE_MQTT_PKI_IMAGE": "video-cloud-emqx-pki", "LKE_OPENBAO_PKI_IMAGE": "video-cloud-openbao-pki", "LKE_VIDEO_CLOUD_IMAGE": "video-cloud-api",
	} {
		release.Images[key] = environmentReleaseImage{Reference: "ghcr.io/" + repo + "/" + pkg + "@" + digest, SourceRevision: revision}
	}
	manifest, err := json.Marshal(map[string]any{
		"schema": "rtk-service-image/v1", "repository": repo, "source_commit": revision, "run_id": "77", "run_attempt": "2",
		"image": "ghcr.io/" + repo + "/video-cloud-api", "digest": digest,
		"images": map[string]any{
			"emqx_pki":    map[string]string{"image": "ghcr.io/" + repo + "/video-cloud-emqx-pki", "digest": digest},
			"openbao_pki": map[string]string{"image": "ghcr.io/" + repo + "/video-cloud-openbao-pki", "digest": digest},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := publicationZIP(t, "image-manifest.json", manifest)
	type call struct {
		command string
		args    []string
	}
	var calls, expected []call
	expected = append(expected, call{"git", []string{"-C", cfg.Workspace, "rev-parse", "HEAD"}})
	for _, key := range []string{"LKE_MQTT_PKI_IMAGE", "LKE_OPENBAO_PKI_IMAGE", "LKE_VIDEO_CLOUD_IMAGE"} {
		image := release.Images[key]
		source, err := deploymentUpgradeImageSource(image.Reference)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected,
			call{"docker", []string{"pull", "--platform", "linux/amd64", image.Reference}},
			call{"git", []string{"-C", cfg.Workspace, "ls-tree", revision, "--", source.RepoPath}},
			call{"git", []string{"-C", cfg.Workspace + "/" + source.RepoPath, "rev-parse", "HEAD"}},
			call{"git", []string{"-C", cfg.Workspace + "/" + source.RepoPath, "rev-parse", "HEAD"}},
			call{"docker", []string{"image", "inspect", image.Reference, "--format", "{{json .Config.Labels}}"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + revision + "&per_page=100"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + revision + "&per_page=100"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/runs/77/jobs?per_page=100"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + revision + "&per_page=100"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/runs/77"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/runs/77/artifacts?per_page=100"}},
			call{"gh", []string{"api", "repos/" + repo + "/actions/artifacts/99/zip"}},
		)
	}
	var output, log bytes.Buffer
	progress := &environmentProgress{out: &output, log: &log, resources: -1}
	ctx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		calls = append(calls, call{command, append([]string(nil), args...)})
		progress.mu.Lock()
		active := len(progress.operations)
		progress.mu.Unlock()
		if active != 1 {
			t.Fatal("verification request was not tracked", command, active)
		}
		switch command {
		case "git":
			if args[2] == "ls-tree" {
				return []byte("160000 commit " + revision + "\t" + args[5] + "\n"), nil
			}
			return []byte(revision + "\n"), nil
		case "docker":
			if args[0] == "pull" {
				return nil, nil
			}
			return json.Marshal(map[string]string{"org.opencontainers.image.revision": revision})
		case "gh":
			endpoint := args[1]
			switch {
			case strings.Contains(endpoint, "/workflows/"):
				return []byte(`{"total_count":1,"workflow_runs":[{"id":77,"head_sha":"` + revision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}`), nil
			case strings.Contains(endpoint, "/jobs?"):
				return []byte(`{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`), nil
			case strings.HasSuffix(endpoint, "/runs/77"):
				return []byte(`{"id":77,"head_sha":"` + revision + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`), nil
			case strings.Contains(endpoint, "/artifacts?"):
				return []byte(`{"total_count":1,"artifacts":[{"id":99,"name":"image-manifest","size_in_bytes":800,"expired":false}]}`), nil
			case strings.HasSuffix(endpoint, "/artifacts/99/zip"):
				return archive, nil
			}
		}
		t.Fatal("unexpected verification command", command, args)
		return nil, errors.New("unreachable")
	}
	if err := verifyEnvironmentReleaseWithCommand(ctx, cfg, release, run); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatal("progress changed verification command sequence", calls, expected)
	}
	starts := map[string]int{}
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "[RUNNING]" {
			starts[fields[1]]++
		}
	}
	if starts["deployment.release.verify.workspace.checkout-head"] != 1 {
		t.Fatal("workspace checkout action missing", output.String())
	}
	for _, role := range []string{"mqtt-pki", "openbao-pki", "video-cloud-api"} {
		for _, step := range []string{"pull", "workspace-pin", "checkout-head", "oci-labels", "ci-runs", "release-runs", "publication-jobs", "release-run", "artifact-list", "artifact-download"} {
			if count := starts["deployment.release.verify."+role+"."+step]; count != 1 {
				t.Fatal("release role/action did not start exactly once", role, step, count, output.String())
			}
		}
	}
	progress.mu.Lock()
	settled, active := progress.settled, len(progress.operations)
	progress.mu.Unlock()
	// Settled counts describe the current stage. The complete command sequence
	// above proves every observation ran; the final download must also settle.
	if settled != 1 || active != 0 || strings.Contains(output.String(), "private-") || strings.Contains(output.String(), repo) || strings.Contains(output.String(), revision) || strings.Contains(output.String(), digest) || strings.Contains(output.String(), "selected-image") {
		t.Fatal("request accounting or safe role/action display changed", settled, active, output.String())
	}
}

func TestProviderSelectionProgressRequiresValidatedDigest(t *testing.T) {
	for _, outcome := range []string{"valid", "wrong-repository", "pull-failed", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			var output, log bytes.Buffer
			progress := &environmentProgress{out: &output, log: &log, resources: -1}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), environmentProgressKey{}, progress))
			defer cancel()
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "POSTGRES_IMAGE": "postgres:16-alpine"}}
			run := func(_ context.Context, command string, args ...string) ([]byte, error) {
				if command != "docker" {
					return nil, errors.New("private-source-error")
				}
				if args[0] == "pull" {
					if outcome == "pull-failed" {
						return nil, errors.New("private-registry-error")
					}
					return nil, nil
				}
				repository := "postgres"
				if outcome == "wrong-repository" {
					repository = "redis"
				}
				if outcome == "cancel" {
					cancel()
				}
				return json.Marshal([]string{repository + "@sha256:" + strings.Repeat("a", 64)})
			}
			_, _ = loadEnvironmentCIImages(ctx, cfg, run)
			stage := "deployment.release.provider.POSTGRES_IMAGE"
			want := "[FAIL]"
			if outcome == "valid" {
				want = "[OK]"
			}
			got := output.String()
			if strings.Contains(got, "[OK]") || !strings.Contains(log.String(), want+" "+stage) || strings.Contains(got+log.String(), "private-") || strings.Contains(got, "[FAIL] "+stage) != (outcome != "valid") {
				t.Fatal("provider step lost its logged outcome or exposed private output or success", got, log.String())
			}
			if outcome == "valid" && strings.Index(log.String(), "[OK] "+stage) > strings.Index(log.String(), "[RUNNING] deployment.release.sources") {
				t.Fatal("provider completion was delayed until the next step")
			}
		})
	}
}
