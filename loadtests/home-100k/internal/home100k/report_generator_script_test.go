package home100k

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReportScriptRendersTemplateWithResourceTimelines(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	script := filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "generate-report.sh"))
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	result := RunResult{
		RunID:  "script-report-test",
		Status: "INCOMPLETE",
		Plan:   plan,
		StageResults: []StageResult{{
			Name:             "25k",
			ConnectedDevices: 25000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:   10,
				ConnectSuccess:    10,
				Subscribes:        10,
				Publishes:         20,
				ReceivedMessages:  20,
				DeltaReceived:     10,
				ReportedPublishes: 10,
				BytesSent:         1000,
				BytesReceived:     2000,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts:       5,
				LoginSuccess:        5,
				ListDevicesRequests: 5,
				ReadShadowRequests:  10,
				DesiredWrites:       10,
				ReceivedAcks:        10,
				BytesSent:           700,
				BytesReceived:       900,
			},
			ShadowGetP95MS:       42,
			DesiredReportedP95MS: 88,
		}},
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts: 10,
			Publishes:       20,
		},
		AppUserTotals: AppUserTotals{
			LoginAttempts: 5,
			DesiredWrites: 10,
		},
		ServerCorrelation: ServerCorrelation{
			Status:  "incomplete",
			Reasons: []string{"missing server counters"},
		},
		SyncTelemetry: SyncTelemetry{VMs: []VMSyncTelemetry{{
			Label:            "home-100k-mixed-000",
			FilesTransferred: 3,
			BytesTransferred: 4096,
			ElapsedMS:        1200,
		}}},
		ReportFile: filepath.Join(outDir, "TEST_REPORT.md"),
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile(results.json) error = %v", err)
	}
	resourceDir := filepath.Join(outDir, "resource-samples")
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(resource-samples) error = %v", err)
	}
	loadRows := strings.Join([]string{
		"time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct",
		"2026-06-15T00:00:00Z\tscript-report-test\trun-stages\thome-100k-mixed-000\t192.0.2.10\tmixed\t123\tok\t10.0\t0.50\t100\t1000\t2G\t25G\t8",
		"2026-06-15T00:00:30Z\tscript-report-test\trun-stages\thome-100k-mixed-000\t192.0.2.10\tmixed\t123\tok\t90.0\t1.50\t700\t1000\t3G\t25G\t12",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(resourceDir, "load-vms.tsv"), []byte(loadRows), 0o644); err != nil {
		t.Fatalf("WriteFile(load-vms.tsv) error = %v", err)
	}
	k8sRows := strings.Join([]string{
		"time\trun_id\tphase\tname\tstatus\tcpu\tcpu_pct\tmem\tmem_pct\treason",
		"2026-06-15T00:00:00Z\tscript-report-test\trun-stages\tlke-node-a\tok\t100m\t10\t1Gi\t30\t",
		"2026-06-15T00:00:30Z\tscript-report-test\trun-stages\tlke-node-a\tok\t850m\t85\t3Gi\t75\t",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(resourceDir, "k8s-nodes.tsv"), []byte(k8sRows), 0o644); err != nil {
		t.Fatalf("WriteFile(k8s-nodes.tsv) error = %v", err)
	}

	cmd := exec.Command("bash", script, "--out-dir", outDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate-report.sh error = %v output=%s", err, string(output))
	}
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "TEST_REPORT.md"))
	if err != nil {
		t.Fatalf("ReadFile(TEST_REPORT.md) error = %v", err)
	}
	report := string(reportRaw)
	for _, want := range []string{
		"## Load Machine Resource Usage",
		"home-100k-mixed-000",
		"CPU p95",
		"## K8s Node Resource Usage During Test",
		"lke-node-a",
		"Mem max",
		"## Report Source Artifacts",
		"resource-samples/load-vms.tsv",
		"resource-samples/k8s-nodes.tsv",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("generated report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "{{") {
		t.Fatalf("generated report still contains template marker:\n%s", report)
	}
}
