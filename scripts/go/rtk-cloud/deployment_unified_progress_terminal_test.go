package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

type terminalPartialWriter struct {
	bytes.Buffer
	remaining int
	failure   error
	limited   bool
}

func (w *terminalPartialWriter) Write(data []byte) (int, error) {
	if !w.limited && !bytes.HasPrefix(data, []byte("\r\x1b")) {
		w.limited = true
		count := min(w.remaining, len(data))
		w.Buffer.Write(data[:count])
		return count, w.failure
	}
	return w.Buffer.Write(data)
}

func TestTerminalProgressResultWriterRetainsOnlyUnwrittenBytes(t *testing.T) {
	for _, failure := range []error{nil, errors.New("synthetic output failure")} {
		for _, count := range []int{0, 5} {
			output := &terminalPartialWriter{remaining: count, failure: failure}
			progress := &environmentProgress{out: output, stage: "deployment.execution.acceptance", started: time.Now(), total: -1, resources: -1, terminal: true, columns: 120}
			ctx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
			writer := environmentProgressResultOutput(ctx, io.Discard)
			row := "[PASS] deployment.pki\n"
			accepted, err := writer.Write([]byte(row))
			wantErr := failure
			if wantErr == nil {
				wantErr = io.ErrShortWrite
			}
			if accepted != len(row) || !errors.Is(err, wantErr) || string(progress.durablePending) != row[count:] {
				t.Fatal("partial transport consumed or lost buffered bytes", accepted, err, string(progress.durablePending))
			}
			if _, err := writer.Write(nil); err != nil {
				t.Fatal(err)
			}
			visible := visibleTerminalProgress(output.String())
			if !strings.HasPrefix(visible, row) || strings.Count(visible, "[PASS] deployment.pki") != 1 || len(progress.durablePending) != 0 {
				t.Fatal("retry duplicated or discarded the partial result row", visible)
			}
		}
	}
}

var terminalProgressCSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var terminalProgressBar = regexp.MustCompile(`\[([#.]+)\]`)
var terminalProgressSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func terminalProgressFixture(columns int, color bool) (*environmentProgress, *bytes.Buffer, context.Context) {
	output := &bytes.Buffer{}
	progress := &environmentProgress{out: output, stage: "deployment.execution.admission", started: time.Now(), total: -1, resources: -1, terminal: true, columns: columns, color: color}
	return progress, output, context.WithValue(context.Background(), environmentProgressKey{}, progress)
}

func visibleTerminalProgress(value string) string {
	return strings.ReplaceAll(terminalProgressCSI.ReplaceAllString(value, ""), "\r", "")
}

func TestProgressProviderTaskUsesSeparateIndentedLine(t *testing.T) {
	for _, mode := range []struct {
		name            string
		terminal, color bool
	}{
		{"plain", false, false},
		{"plain-ignores-color", false, true},
		{"terminal-no-color", true, false},
		{"terminal-color", true, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			progress, output, _ := terminalProgressFixture(120, mode.color)
			progress.terminal = mode.terminal
			progress.stage = "deployment.release.provider.ZAMMAD_MEMCACHED_IMAGE"
			progress.write(false)
			lines := strings.SplitN(output.String(), "\n", 3)
			if len(lines) != 3 || lines[0] != "[RUNNING] deployment.release.provider.ZAMMAD_MEMCACHED_IMAGE" {
				t.Fatal("provider stage and description did not occupy distinct lines", output.String())
			}
			wantTask := "  Resolve official image for zammad-memcached-image"
			if mode.terminal && mode.color {
				wantTask = "  \x1b[36mResolve official image for zammad-memcached-image\x1b[0m"
			}
			if lines[1] != wantTask || terminalProgressSGR.MatchString(lines[0]) || strings.Contains(output.String(), "task=") || strings.Contains(output.String(), `"Resolve`) {
				t.Fatal("task indentation, terminal-only cyan, or plain wording changed", output.String())
			}
			if mode.terminal {
				if !strings.HasPrefix(lines[2], "\r\x1b[2K") || strings.Contains(lines[2], "\n") || !progress.lineVisible {
					t.Fatal("two-line stage record broke the single-row progress frame", output.String())
				}
			} else if lines[2] != "" || strings.ContainsAny(output.String(), "\x1b\r") {
				t.Fatal("plain output emitted terminal controls or an animation frame", output.String())
			}
		})
	}
}

func TestTerminalProgressPercentageAndBarFollowActualBatchCounts(t *testing.T) {
	progress, output, _ := terminalProgressFixture(120, true)
	progress.checked, progress.total = 130, 352
	progress.write(true)
	raw, visible := output.String(), visibleTerminalProgress(output.String())
	bar := terminalProgressBar.FindStringSubmatch(visible)
	if !strings.HasPrefix(raw, "\r\x1b[2K") || !terminalProgressSGR.MatchString(raw) || !strings.Contains(visible, "36% checks 130/352") || strings.Contains(visible, "37%") || len(bar) != 2 || strings.Count(bar[1], "#") != 7 || len(bar[1]) != 20 || strings.Contains(raw, "\n") {
		t.Fatal("TTY batch progress was rounded, fabricated, uncolored, or printed as another log line", raw)
	}
	output.Reset()
	progress.checked = 176
	progress.write(true)
	visible = visibleTerminalProgress(output.String())
	bar = terminalProgressBar.FindStringSubmatch(visible)
	if !strings.Contains(visible, "50% checks 176/352") || len(bar) != 2 || strings.Count(bar[1], "#") != 10 {
		t.Fatal("the bar did not advance with the completed check count", visible)
	}
	output.Reset()
	progress.total = -1
	progress.completed, progress.resources = 2, 5
	progress.stage = "deployment.execution.configuration.apply"
	progress.write(true)
	if visible = visibleTerminalProgress(output.String()); !strings.Contains(visible, "40% resources 2/5") {
		t.Fatal("resource progress used another batch's percentage", visible)
	}
}

func TestTerminalProgressUnknownWorkAnimatesWithoutInventingPercentage(t *testing.T) {
	progress, output, _ := terminalProgressFixture(120, false)
	progress.stage = "deployment.release.resolve"
	progress.write(true)
	first := visibleTerminalProgress(output.String())
	output.Reset()
	progress.write(true)
	second := visibleTerminalProgress(output.String())
	firstBar, secondBar := terminalProgressBar.FindStringSubmatch(first), terminalProgressBar.FindStringSubmatch(second)
	if strings.Contains(first+second, "%") || first[0] == second[0] || len(firstBar) != 2 || len(secondBar) != 2 || strings.Count(firstBar[1], "#") != 1 || strings.Count(secondBar[1], "#") != 1 || firstBar[1] == secondBar[1] {
		t.Fatal("unknown-duration work showed a fake percentage or a frozen spinner", first, second)
	}
	for _, stage := range []string{"deployment.execution.verification", "deployment.execution.acceptance", "deployment.execution.workload.rollout"} {
		output.Reset()
		progress.stage = stage
		progress.completed, progress.resources = 352, 352
		progress.write(true)
		visible := visibleTerminalProgress(output.String())
		bar := terminalProgressBar.FindStringSubmatch(visible)
		if strings.Contains(visible, "%") || strings.Contains(visible, "[PASS]") || strings.Contains(visible, "[OK]") || !strings.Contains(visible, "resources 352/352") || len(bar) != 2 || strings.Count(bar[1], "#") != 1 {
			t.Fatal("post-apply work appeared accepted or finished because every resource was processed", stage, visible)
		}
		if stage != "deployment.execution.workload.rollout" && !strings.Contains(visible, "not complete yet") {
			t.Fatal("final validation lost its pending-deployment explanation", visible)
		}
	}
}

func TestTerminalProgressFramesStayWithinNarrowColumnWidths(t *testing.T) {
	for _, columns := range []int{20, 24, 40, 80, 160} {
		progress, output, _ := terminalProgressFixture(columns, true)
		progress.checked, progress.total = 130, 352
		progress.resource = "Deployment/video-cloud-dev-video-cloud/a-long-but-valid-resource-name"
		progress.action, progress.step = "update", "dry-run"
		progress.write(true)
		visible := visibleTerminalProgress(output.String())
		if len(visible) > columns-1 || strings.ContainsAny(visible, "\n\r") || !strings.HasPrefix(output.String(), "\r\x1b[2K") {
			t.Fatal("ANSI-stripped progress would wrap or spill into a second terminal row", columns, len(visible), visible)
		}
	}
}

func TestTerminalProgressStageChangesKeepTwoLineRecordThenRedraw(t *testing.T) {
	progress, output, ctx := terminalProgressFixture(120, true)
	progress.checked, progress.total = 130, 352
	progress.write(true)
	output.Reset()
	environmentDeploymentProgress(ctx, "deployment.execution.verification")
	raw := output.String()
	lines := strings.SplitN(raw, "\n", 3)
	if len(lines) != 3 || lines[0] != "\r\x1b[2K[RUNNING] deployment.execution.verification" || terminalProgressSGR.MatchString(lines[0]) || lines[1] != "  \x1b[36mVerify updated resources; deployment is not complete yet\x1b[0m" || !strings.HasPrefix(lines[2], "\r\x1b[2K") || !progress.lineVisible {
		t.Fatal("stage change lost the two-line record, task color, or inline redraw", raw)
	}
	output.Reset()
	resource := environmentProgressResourceID("Deployment", "video-cloud-dev-video-cloud", "app")
	environmentDeploymentResourceProgress(ctx, 0, 2, resource, "update", "read")
	raw = output.String()
	if !strings.Contains(raw, "[RUNNING] deployment.execution.verification") || !strings.Contains(raw, "resource="+resource) || !strings.Contains(raw, "\n\r\x1b[2K") {
		t.Fatal("resource change lost its durable record or inline redraw", raw)
	}
}

func TestTerminalProgressResultWriterBuffersPartialResultLines(t *testing.T) {
	progress, output, ctx := terminalProgressFixture(120, true)
	progress.write(true)
	before := output.String()
	var fallback bytes.Buffer
	writer := environmentProgressResultOutput(ctx, &fallback)
	if n, err := writer.Write([]byte("[PA")); err != nil || n != 3 || output.String() != before {
		t.Fatal("partial PASS row was written over the active frame", n, err, output.String())
	}
	progress.write(true)
	if output.String() != before {
		t.Fatal("animation interrupted a buffered result row")
	}
	part := "SS] deployment.pki\n[FA"
	if n, err := writer.Write([]byte(part)); err != nil || n != len(part) || output.String() != before+"\r\x1b[2K[PASS] deployment.pki\n" || string(progress.durablePending) != "[FA" {
		t.Fatal("complete result row was split or its following partial row escaped buffering", n, err, output.String())
	}
	progress.write(true)
	part = "IL] deployment.execution\n"
	if n, err := writer.Write([]byte(part)); err != nil || n != len(part) || len(progress.durablePending) != 0 || !progress.lineVisible || fallback.Len() != 0 {
		t.Fatal("durable writer recursed, lost buffered text, or failed to redraw", n, err)
	}
	if !strings.Contains(output.String(), "[PASS] deployment.pki\n[FAIL] deployment.execution\n\r\x1b[2K") || strings.Count(output.String(), "[PASS]") != 1 || strings.Count(output.String(), "[FAIL]") != 1 {
		t.Fatal("animation split or duplicated durable result lines", output.String())
	}
}

func TestTerminalProgressCleanupJoinsAnimationAndFlushesFinalPartialRow(t *testing.T) {
	var output bytes.Buffer
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	private := errors.New("private-terminal-cleanup-error")
	err := runEnvironmentProgressInterval(parent, &output, "deployment.execution.acceptance", time.Millisecond, func(ctx context.Context) error {
		progress := ctx.Value(environmentProgressKey{}).(*environmentProgress)
		progress.mu.Lock()
		progress.terminal, progress.columns, progress.color = true, 120, false
		progress.write(true)
		progress.mu.Unlock()
		writer := environmentProgressResultOutput(ctx, &output)
		if _, err := writer.Write([]byte("[FAIL] deployment.execution")); err != nil {
			t.Fatal(err)
		}
		cancel()
		return private
	})
	before := output.String()
	if err != private || !strings.HasSuffix(before, "\r\x1b[2K[FAIL] deployment.execution\n[FAIL] deployment.execution.acceptance\n") || strings.Contains(before, private.Error()) {
		t.Fatal("terminal cleanup lost a partial final row, left a frame visible, or printed a private work error", err, before)
	}
	time.Sleep(10 * time.Millisecond)
	if output.String() != before {
		t.Fatal("reporter kept redrawing after final output cleanup")
	}
}

func TestTerminalProgressNoColorKeepsInlineControlsAndPlainOutputHasNoANSI(t *testing.T) {
	progress, output, ctx := terminalProgressFixture(120, false)
	progress.write(true)
	if !strings.Contains(output.String(), "\r\x1b[2K") || terminalProgressSGR.MatchString(output.String()) {
		t.Fatal("color-disabled terminal lost inline updates or emitted SGR colors", output.String())
	}
	progress.terminal = false
	output.Reset()
	progress.write(true)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 || strings.ContainsAny(output.String(), "\x1b\r") || !strings.HasPrefix(lines[0], "[RUNNING]") || !strings.HasSuffix(lines[0], "still running") || lines[1] != "  Read baselines and test exact resource bodies" {
		t.Fatal("plain progress emitted terminal control sequences", output.String())
	}
	var fallback bytes.Buffer
	if writer := environmentProgressResultOutput(ctx, &fallback); writer != &fallback {
		t.Fatal("plain result output was wrapped by the interactive renderer")
	}
	for _, term := range []string{"dumb", "xterm-256color"} {
		t.Run(term, func(t *testing.T) {
			t.Setenv("TERM", term)
			file, err := os.CreateTemp(t.TempDir(), "non-terminal-")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if terminal, _, color := environmentProgressTerminal(file); terminal || color {
				t.Fatal("a redirected file was rendered as an interactive terminal")
			}
			if terminal, _, color := environmentProgressTerminal(&bytes.Buffer{}); terminal || color {
				t.Fatal("a non-FD log writer was rendered as an interactive terminal")
			}
		})
	}
}

func TestTerminalProgressRejectsPrivateOrMalformedTargetsBeforeRendering(t *testing.T) {
	progress, output, ctx := terminalProgressFixture(160, true)
	resource := environmentProgressResourceID("Secret", "video-cloud-dev-platform", "runtime")
	stop := environmentDeploymentOperation(ctx, "read", resource)
	for _, id := range []string{resource + "?token=private-terminal-canary", "Secret/video-cloud-dev-platform/private\nterminal-canary", "UnknownKind//private-terminal-canary"} {
		environmentDeploymentOperation(ctx, "read", id)()
		environmentDeploymentResourceProgress(ctx, 0, 1, id, "update", "read")
		environmentDeploymentRolloutProgress(ctx, id, 0, 0, 1, 300)
	}
	environmentDeploymentOperation(ctx, "private-terminal-canary", resource)()
	environmentDeploymentResourceProgress(ctx, 0, 1, resource, "private-terminal-canary", "read")
	progress.write(false)
	visible := visibleTerminalProgress(output.String())
	if strings.Contains(output.String(), "private-terminal-canary") || !strings.Contains(visible, resource) || !strings.Contains(visible, "read Secret/runtime") || len(progress.operations) != 1 {
		t.Fatal("interactive progress exposed unvalidated input or dropped its safe resource", output.String())
	}
	stop()
}
