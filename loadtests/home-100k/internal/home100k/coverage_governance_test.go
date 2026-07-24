package home100k

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageGovernanceEvidenceParsingHelpers(t *testing.T) {
	t.Parallel()

	event, duration, session, fields := lokiTraceEventFields(`{"event":"first_rtp","duration_ms":"42","session_id":" session-1 "}`)
	if event != "first_rtp" || duration != 42 || session != "session-1" || fields == nil {
		t.Fatalf("lokiTraceEventFields direct = %q %d %q %#v", event, duration, session, fields)
	}
	event, duration, session, _ = lokiTraceEventFields(`{"fields":{"event":"close","duration_ms":12,"session_id":"session-2"}}`)
	if event != "close" || duration != 12 || session != "session-2" {
		t.Fatalf("lokiTraceEventFields nested = %q %d %q", event, duration, session)
	}
	if got := lokiTraceEventName(`{"event":"relay"}`); got != "relay" {
		t.Fatalf("lokiTraceEventName = %q", got)
	}
	if event, duration, session, fields := lokiTraceEventFields("not-json"); event != "" || duration != -1 || session != "" || fields != nil {
		t.Fatalf("invalid Loki event = %q %d %q %#v", event, duration, session, fields)
	}
	if got := jsonStringField(map[string]any{"name": "value"}, "name"); got != "value" {
		t.Fatalf("jsonStringField = %q", got)
	}
	for name, test := range map[string]struct {
		value any
		want  int64
	}{
		"float":  {float64(4), 4},
		"int64":  {int64(5), 5},
		"int":    {6, 6},
		"number": {json.Number("7"), 7},
		"string": {" 8 ", 8},
		"bad":    {"bad", -1},
		"nil":    {nil, -1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := jsonInt64Field(map[string]any{"value": test.value}, "value", -1); got != test.want {
				t.Fatalf("jsonInt64Field = %d, want %d", got, test.want)
			}
		})
	}
	if percentileInt64(nil, 95) != 0 || percentileInt64([]int64{1, 2, 3, 4}, 95) != 4 ||
		percentileInt64([]int64{1, 2, 3, 4}, 0) != 1 {
		t.Fatal("percentileInt64 returned an unexpected value")
	}
	if totalCounterValues(map[string]int64{"a": 2, "b": 3}) != 5 {
		t.Fatal("totalCounterValues returned an unexpected value")
	}
	if got := escapeLogQLLiteral(`a\"b`); got != `a\\\"b` {
		t.Fatalf("escapeLogQLLiteral = %q", got)
	}

	lines := []string{
		`{"event_id":"e1","component":"device_runtime_log","source":"device-runtime"}`,
		`{"event_id":"e1","component":"device_runtime_log","source":"device-runtime"}`,
		`{"event_id":"e2","component":"other","source":"device-runtime"}`,
		`not-json`,
	}
	events := parseCentralLoggerRuntimeLokiLines(lines)
	if len(events) != 1 || events[0].EventID != "e1" {
		t.Fatalf("parseCentralLoggerRuntimeLokiLines = %#v", events)
	}
}

func TestCoverageGovernanceRunScopedFileHelpers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	assignmentPath := filepath.Join(root, "assignment.json")
	wantAssignment := VMAssignment{Role: "mixed", Index: 2, Label: "load-02"}
	raw, _ := json.Marshal(wantAssignment)
	if err := os.WriteFile(assignmentPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	gotAssignment, err := loadVMAssignment(assignmentPath)
	if err != nil || gotAssignment.Label != wantAssignment.Label {
		t.Fatalf("loadVMAssignment = %#v, %v", gotAssignment, err)
	}
	if _, err := loadVMAssignment(filepath.Join(root, "missing.json")); err == nil {
		t.Fatal("loadVMAssignment unexpectedly read missing file")
	}
	if err := os.WriteFile(assignmentPath, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVMAssignment(assignmentPath); err == nil {
		t.Fatal("loadVMAssignment unexpectedly accepted invalid JSON")
	}

	writePreflightFailure(root, errors.New("preflight failed"))
	preflight, err := os.ReadFile(filepath.Join(root, "preflight.json"))
	if err != nil || !strings.Contains(string(preflight), "preflight failed") {
		t.Fatalf("preflight evidence = %q, %v", preflight, err)
	}
	writePreflightFailure("", errors.New("ignored"))

	if got := readOptionalText(filepath.Join(root, "preflight.json")); !strings.Contains(got, "preflight failed") {
		t.Fatalf("readOptionalText = %q", got)
	}
	if got := readOptionalText(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("readOptionalText missing = %q", got)
	}

	if err := os.MkdirAll(filepath.Join(root, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "env", "stack.env"), []byte("CLOUD_STACK_NAME=coverage-run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stackStateRelPath(root) != "" {
		t.Fatal("stackStateRelPath found state before it existed")
	}
	if err := os.WriteFile(filepath.Join(root, "state", "coverage-run.state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stackStateRelPath(root); got != filepath.Join("state", "coverage-run.state.json") {
		t.Fatalf("stackStateRelPath = %q", got)
	}
	if got := envRootRsyncFilters(); len(got) == 0 || got[len(got)-1] != "*" {
		t.Fatalf("envRootRsyncFilters = %#v", got)
	}
	if got := sqlLiteral("customer's"); got != "'customer''s'" {
		t.Fatalf("sqlLiteral = %q", got)
	}
}

func TestCoverageGovernanceCoordinatorTimingHelpers(t *testing.T) {
	t.Parallel()

	plan := Plan{Stages: []Stage{
		{WarmUp: "1s", SteadyState: "2s", CoolDown: "3s"},
		{WarmUp: "invalid", SteadyState: "4s"},
	}}
	if got := planTotalDuration(plan); got != 10*time.Second {
		t.Fatalf("planTotalDuration = %s", got)
	}
	items := []VMStartTelemetry{
		{StageStartedAt: "2026-07-24T00:00:00.000Z"},
		{FirstConnectAt: "2026-07-24T00:00:00.125Z"},
		{StageStartedAt: "invalid"},
		{},
	}
	if got := computeMaxStartSkewMS(items); got != 125 {
		t.Fatalf("computeMaxStartSkewMS = %d", got)
	}
	if got := computeMaxStartSkewMS(nil); got != 0 {
		t.Fatalf("computeMaxStartSkewMS(nil) = %d", got)
	}
}
