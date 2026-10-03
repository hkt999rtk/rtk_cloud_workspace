package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBillingRecoveryKeysRejectedBeforeCIMaterialization(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	script, err := filepath.Abs("../../../scripts/ci/materialize-rtk-cloud-secret-store.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"operator/recovery/billing-backup/key.txt", "operator/recovery/core/key.txt", "pki/misplaced.agekey", "runtime/key.ED25519KEY"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bundle := filepath.Join(dir, "bundle.json")
			body, _ := json.Marshal(map[string]any{"files": map[string]string{name: "must-never-materialize"}})
			if err := os.WriteFile(bundle, body, 0600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "config")
			cmd := exec.Command("bash", script, "staging", bundle)
			cmd.Env = append(os.Environ(), "RUNNER_TEMP="+dir, "RTK_CLOUD_CONFIG_ROOT="+config)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "forbidden in CI bundles") {
				t.Fatalf("expected custody rejection: %v %s", err, out)
			}
			if _, err := os.Stat(config); !os.IsNotExist(err) {
				t.Fatalf("materializer wrote before rejection: %v", err)
			}
		})
	}
}
