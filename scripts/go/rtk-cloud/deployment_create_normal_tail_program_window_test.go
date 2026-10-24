package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailProgramWindowPinsOriginalAdmissionAndNeverResets(t *testing.T) {
	for _, mode := range []string{"live-original", "expired-original", "lower-total", "expired-total", "changed-program", "canceled", "missing-fence", "tampered-owner", "tampered-anchor", "unsafe-file", "future-record"} {
		t.Run(mode, func(t *testing.T) {
			cfg, e, a := normalTailProgramFixture(t)
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			a.CheckerSHA256 = checker
			if err = saveCreateNormalTailProgramArtifact(e.Store, e, a); err != nil {
				t.Fatal(err)
			}
			if err = writeEnvironmentPreflightActive(e.Store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: deploymentplan.Digest(a.Program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(a.Program) + "/qualification-1"}); err != nil {
				t.Fatal(err)
			}
			h := &createNormalTailHandoff{Execution: e}
			fence := func(ctx context.Context) error { return e.Check(ctx) }
			timeout := 45 * time.Minute
			if mode == "expired-original" {
				timeout = time.Nanosecond
			}
			original, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, timeout, time.Now().UTC(), fence)
			if err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest(e.Journal)
			artifactBefore, err := os.ReadFile(createNormalTailProgramPath(e))
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "missing-fence" {
				fence = func(context.Context) error { return context.Canceled }
			}
			if mode == "changed-program" {
				a.Program.ConfigurationSHA256 = deploymentplan.Digest("changed inputs")
			}
			got, err := admitCreateNormalTailProgramWithFence(ctx, cfg, h, a, 45*time.Minute, time.Now().UTC(), fence, 90*time.Minute)
			if mode == "canceled" || mode == "missing-fence" || mode == "changed-program" {
				if err == nil {
					t.Fatal("invalid scope/fence granted a window")
				}
				return
			}
			if err != nil || !got.AdmittedAt.Equal(original.AdmittedAt) || !got.Deadline.Equal(original.AdmittedAt.Add(90*time.Minute)) {
				t.Fatal("explicit total was not anchored", err)
			}
			var w createNormalTailProgramWindow
			raw, err := readBoundedEnvironmentFile(createNormalTailProgramWindowPath(e), true)
			if err != nil || json.Unmarshal(raw, &w) != nil {
				t.Fatal(err)
			}
			if w.OriginalAdmissionSHA256 != deploymentplan.Digest(original) || !w.OriginalDeadline.Equal(original.Deadline) {
				t.Fatal("original receipt changed")
			}
			if mode == "tampered-owner" {
				w.OwnerSHA256 = deploymentplan.Digest("foreign owner")
			}
			if mode == "tampered-anchor" {
				w.OriginalAdmittedAt = w.OriginalAdmittedAt.Add(time.Minute)
				w.Deadline = w.OriginalAdmittedAt.Add(w.TotalTimeout)
			}
			if mode == "future-record" {
				w.RecordedAt = time.Now().Add(time.Hour)
			}
			if mode == "unsafe-file" {
				_ = os.Chmod(createNormalTailProgramWindowPath(e), 0644)
			}
			if mode == "tampered-owner" || mode == "tampered-anchor" || mode == "future-record" {
				if os.WriteFile(createNormalTailProgramWindowPath(e), mustProviderRecoveryJSON(w), 0600) != nil {
					t.Fatal("fixture write")
				}
			}
			requested, now := 90*time.Minute, time.Now().UTC()
			if mode == "lower-total" {
				requested = 60 * time.Minute
			}
			if mode == "expired-total" {
				now = got.Deadline
			}
			retry, retryErr := admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, 3*time.Hour, now, fence, requested)
			valid := mode == "live-original" || mode == "expired-original"
			if (retryErr == nil) != valid || valid && retry != got {
				t.Fatal("retry reset or accepted changed window", retryErr)
			}
			if valid {
				after, _ := os.ReadFile(createNormalTailProgramWindowPath(e))
				if string(after) != string(raw) {
					t.Fatal("window bytes rewritten")
				}
				effective, err := readCreateNormalTailProgramAdmission(e.Store, e)
				if err != nil || effective != got {
					t.Fatal("semantic readers disagree", err)
				}
				recorded, err := readCreateNormalTailProgramOriginalAdmission(e.Store, e)
				if err != nil || recorded != original {
					t.Fatal("original admission rewritten", err)
				}
			}
			artifactAfter, _ := os.ReadFile(createNormalTailProgramPath(e))
			if deploymentplan.Digest(e.Journal) != before || string(artifactAfter) != string(artifactBefore) {
				t.Fatal("supplement mutated original program or journal")
			}
		})
	}
}

func TestCreateNormalTailProgramWindowUsesOnePartialProducerAndNarrowerContext(t *testing.T) {
	cfg, store, e, defs, p, ops, _, _, apply := publicTLSProtocolFixture(t)
	cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	p.ConfigurationSHA256 = deploymentplan.Digest(cfg)
	original, read := admitPublicTLSReaderProgram(t, cfg, store, e, defs, p, time.Nanosecond)
	var a createNormalTailProgramArtifact
	if readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &a) != nil {
		t.Fatal("program missing")
	}
	h := &createNormalTailHandoff{Execution: e}
	fence := func(ctx context.Context) error { return e.Check(ctx) }
	got, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, time.Hour, time.Now().UTC(), fence, 90*time.Minute)
	if err != nil || got.AdmittedAt != original.AdmittedAt {
		t.Fatal(err)
	}
	first, err := os.ReadFile(createNormalTailProgramWindowPath(e))
	if err != nil {
		t.Fatal(err)
	}
	got, err = admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, time.Hour, time.Now().UTC(), fence, 360*time.Minute)
	if err != nil || !got.Deadline.Equal(original.AdmittedAt.Add(360*time.Minute)) {
		t.Fatal("partial producer append lost its original anchor", err)
	}
	unchanged, err := os.ReadFile(createNormalTailProgramWindowPath(e))
	if err != nil || string(first) != string(unchanged) {
		t.Fatal("partial producer original supplement was rewritten", err)
	}
	ops.WaitTXT = func(context.Context, string, string, map[string]string) error { return context.Canceled }
	if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil {
		t.Fatal("partial producer did not stop")
	}
	if !time.Now().After(original.Deadline) {
		t.Fatal("fixture base window did not expire")
	}
	if err = read(); err != nil {
		t.Fatal("partial producer could not consume effective window", err)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bounded, close := context.WithDeadline(parent, got.Deadline)
	defer close()
	deadline, _ := bounded.Deadline()
	parentDeadline, _ := parent.Deadline()
	if deadline != parentDeadline {
		t.Fatal("effective window widened shorter caller context")
	}
}

func TestCreateNormalTailProgramWindowCLIAndFirstAdmissionStayScoped(t *testing.T) {
	sha := deploymentplan.Digest("review")
	o, err := parseUnifiedDeploymentOptions("create", []string{"--environment", "dev", "--plan-sha256", sha, "--total-timeout", "90m"}, io.Discard)
	if err != nil || o.TotalTimeout != 90*time.Minute || o.Timeout != 45*time.Minute {
		t.Fatal("explicit total option or default changed", err)
	}
	for _, action := range []string{"preflight", "upgrade"} {
		if _, err = parseUnifiedDeploymentOptions(action, []string{"--environment", "dev", "--plan-sha256", sha, "--total-timeout", "90m"}, io.Discard); err == nil {
			t.Fatal("total option escaped admitted create", action)
		}
	}
	cfg, e, a := normalTailProgramFixture(t)
	if _, err = admitCreateNormalTailProgramWithFence(t.Context(), cfg, &createNormalTailHandoff{Execution: e}, a, time.Hour, time.Now().UTC(), func(ctx context.Context) error { return e.Check(ctx) }, 90*time.Minute); err == nil {
		t.Fatal("explicit total replaced first qualification/admission")
	}
}
