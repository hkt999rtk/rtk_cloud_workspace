package main

import (
	"strings"
	"testing"
)

func TestCreateRecoveryOwnerReportsFailedBoundary(t *testing.T) {
	for _, mode := range []string{"running", "configuration"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, original, _, plan := providerExecutionContractFixture(t)
			e, artifact := providerExecutionContractFork(t, original, plan, "owner-diagnostics")
			defer e.Close()
			want := "process lock"
			if mode == "configuration" {
				if err := e.Close(); err != nil {
					t.Fatal(err)
				}
				cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] = "changed.invalid"
				want = "configuration"
			}
			loaded, _, err := loadCreateRecoveryExecutionOwner(cfg, store, artifact.Qualification.PlanSHA256)
			if loaded != nil {
				loaded.Close()
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("failure must identify %s boundary: %v", want, err)
			}
			if strings.Contains(err.Error(), "changed.invalid") || strings.Contains(err.Error(), store.Root) {
				t.Fatal("failure exposed private configuration or state path")
			}
		})
	}
}
