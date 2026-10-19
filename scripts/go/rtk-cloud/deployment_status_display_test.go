package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDeploymentStatusColorsAndPlainReportsPreserveEvidence(t *testing.T) {
	for status, want := range map[string]string{"PASS": "\x1b[32m[OK]\x1b[0m", "FAIL": "\x1b[31m[FAIL]\x1b[0m", "BLOCKED": "\x1b[31m[BLOCKED]\x1b[0m", "ERROR": "\x1b[31m[ERROR]\x1b[0m"} {
		p, _, ctx := terminalProgressFixture(120, true)
		if got := deploymentStatusLabel(environmentProgressResultOutput(ctx, p.out), status); got != want {
			t.Errorf("%s: %q, want %q", status, got, want)
		}
		p.color = false
		if got := deploymentStatusLabel(environmentProgressResultOutput(ctx, p.out), status); strings.Contains(got, "\x1b") {
			t.Errorf("NO_COLOR emitted ANSI: %q", got)
		}
	}
	var out bytes.Buffer
	r := newDeploymentCheckReporter(&out)
	r.emit(deploymentCredentialCheck{ID: "test.pass", Status: "PASS", Passed: true})
	r.emit(deploymentCredentialCheck{ID: "test.blocked", Status: "BLOCKED"})
	if !strings.Contains(out.String(), "[OK] test.pass") || !strings.Contains(out.String(), "[BLOCKED] test.blocked") || strings.Contains(out.String(), "\x1b") || r.results["test.pass"].Status != "PASS" {
		t.Fatal("display changed report evidence or plain output", out.String(), r.results)
	}
}

func TestProgressResultStopsRunningUntilNextStage(t *testing.T) {
	p, out, ctx := terminalProgressFixture(120, true)
	var log bytes.Buffer
	p.log = &log
	environmentDeploymentAdmissionProgress(ctx, 12, 12)
	if strings.Contains(out.String(), "[OK]") {
		t.Fatal("finished queries fabricated success")
	}
	environmentDeploymentProgressResult(ctx, "deployment.execution", "BLOCKED")
	before := out.String()
	p.write(true)
	if out.String() != before || !strings.HasSuffix(before, "\x1b[31m[BLOCKED]\x1b[0m deployment.execution\n") {
		t.Fatal("completed gate still animated or missed red outcome", out.String())
	}
	environmentDeploymentProgress(ctx, "deployment.pki")
	if p.status != "" || !p.lineVisible {
		t.Fatal("next gate failed to start its progress")
	}
	// Resource completion remains evidence in the log while acceptance runs.
	environmentDeploymentResourceProgress(ctx, 0, 1, "Service/ns/api", "update", "complete")
	if strings.Contains(out.String(), "[OK]") || !strings.Contains(log.String(), "[OK] Service/ns/api") || p.status != "" || !p.lineVisible {
		t.Fatal("resource completion lost its log or stopped acceptance progress")
	}
}

func TestSuccessfulProgressResultClearsAndStopsTerminalAnimation(t *testing.T) {
	for _, status := range []string{"PASS", "OK"} {
		t.Run(status, func(t *testing.T) {
			p, out, ctx := terminalProgressFixture(120, true)
			var log bytes.Buffer
			p.log = &log
			p.write(true)
			out.Reset()
			environmentDeploymentProgressResult(ctx, p.stage, status)
			before := out.String()
			p.write(true)
			if before != "\r\x1b[2K" || out.String() != before || p.lineVisible || !strings.Contains(log.String(), "[OK] "+p.stage+" elapsed=") {
				t.Fatal("successful stage stayed animated, printed a success row, or lost its log", out.String(), log.String())
			}
		})
	}
}

func TestProgressScopeOutcomeDoesNotPassErrorsCancellationOrPanic(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			var out, log bytes.Buffer
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), environmentProgressLogKey{}, io.Writer(&log)))
			defer cancel()
			func() {
				defer func() { _ = recover() }()
				_ = runEnvironmentProgressInterval(ctx, &out, "deployment.compile", time.Hour, func(context.Context) error {
					switch outcome {
					case "failure":
						return errors.New("private-error-canary")
					case "cancel":
						cancel()
					case "panic":
						panic("private-error-canary")
					}
					return nil
				})
			}()
			if strings.Contains(out.String()+log.String(), "private-error-canary") || strings.Contains(out.String(), "[OK]") || strings.Contains(log.String(), "[OK] deployment.compile") != (outcome == "success") {
				t.Fatal("scope fabricated success or leaked error", out.String())
			}
			if outcome == "failure" || outcome == "cancel" {
				if !strings.Contains(out.String(), "[FAIL] deployment.compile") {
					t.Fatal("failed scope remained running", out.String())
				}
			}
		})
	}
}
