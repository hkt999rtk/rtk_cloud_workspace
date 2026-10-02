package cloudmonitor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONReportSurvivesRendererFailureWithoutSecrets(t *testing.T) {
	now := time.Now().UTC()
	s := Snapshot{SchemaVersion: 1, Environment: "dev", StartedAt: now, FinishedAt: now, Profile: "check", Results: []Result{{Service: "app", CheckID: "token", Layer: "credential", Required: true, Status: Fail, ObservedAt: now, Reason: "Authorization=private-proof-credential"}}}
	r := BuildReport([]Snapshot{s}, "dev", now.Add(-time.Second), now.Add(time.Second), "Asia/Taipei")
	prefix := filepath.Join(t.TempDir(), "report")
	html, pdf, e := RenderReport(context.Background(), r, prefix, "", nil)
	if e == nil || html == "" || pdf != "" {
		t.Fatal("missingrendererresult", html, pdf, e)
	}
	raw, e := os.ReadFile(prefix + ".json")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "private-proof-credential") {
		t.Fatal("secret reachedreportJSON")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result["schema_version"] != float64(1) || result["environment"] != "dev" || result["overall"] != "FAIL" {
		t.Fatal("unversionedreportJSON")
	}
	if info, e := os.Stat(prefix + ".json"); e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("reportJSONnotprivate")
	}
	if _, e := os.Stat(html); e != nil {
		t.Fatal("lostreviewableHTML")
	}
}
