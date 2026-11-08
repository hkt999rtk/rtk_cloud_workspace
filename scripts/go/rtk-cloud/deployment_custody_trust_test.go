package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyTrustPublicationResumesWithoutReintroducingBootstrap(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, bootstrap, _, original := createServiceRecipeFixture(t, environment)
			_, _, _, next := createServiceRecipeFixture(t, environment)
			cfg, store, operator, artifact := createStateFixture(t, environment)
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			// Exercise this producer with an admitted isolated owner; full backend
			// qualification belongs to the separate complete create-flow tests.
			cfg.Values["PKI_STATE_BACKEND"] = "openbao"
			originalCA := filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport", "ca.crt")
			if err = store.write(originalCA, []byte(original.Root.Certificate), false); err != nil {
				t.Fatal(err)
			}
			runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, OpenBao: environmentCreateOpenBaoBootstrap{CACertPath: filepath.Join(store.Root, originalCA)}}
			var applied environmentCustodyTransportTrust
			failApply := false
			interrupted := errors.New("isolated apply interrupted")
			apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
				if !strings.HasPrefix(stage, "custody-transport-trust-") || len(definitions) != 2 {
					t.Fatal("unbound custody publication")
				}
				applied = environmentCustodyTransportTrust{}
				if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(definitions[0].Object["data"])["transport.json"])), &applied) != nil {
					t.Fatal("invalid public snapshot")
				}
				if failApply {
					return interrupted
				}
				return nil
			}
			failApply = true
			_, _, err = continueEnvironmentCreateAfterOpenBao(t.Context(), cfg, &environmentCreatePlan{Bootstrap: bootstrap}, store, nil, e, apply, runtime, environmentCreateTransport{})
			if !errors.Is(err, interrupted) {
				t.Fatal("custody trust must publish before database/Service bootstrap:", err)
			}
			failApply = false
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, nil, false, apply); err != nil {
				t.Fatal("initial publication:", err)
			}
			if applied.BootstrapPEM != original.Root.Certificate || applied.ManagedOnly {
				t.Fatal("wrong original bootstrap snapshot")
			}
			foreignRuntime := runtime
			foreignRuntime.OpenBao.CACertPath = filepath.Join(store.Root, "openbao", "other-ca.pem")
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, foreignRuntime, nil, false, apply); err == nil {
				t.Fatal("alternate generation authority path accepted")
			}
			foreignRuntime = runtime
			foreignRuntime.ExecutionSHA256 = ""
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, foreignRuntime, nil, false, apply); err == nil {
				t.Fatal("custody generation inferred from a possibly different recovery plan")
			}
			managed := next.Root
			managed.Domain, managed.Status = "openbao_tls", "ready"
			failApply = true
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, &managed, false, apply); err == nil {
				t.Fatal("apply failure hidden")
			}
			failApply = false
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, nil, false, apply); err != nil || applied.ManagedRootID != managed.ID {
				t.Fatal("recovery discarded prepared original authority", err)
			}
			managed.Status = "active"
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, &managed, true, apply); err != nil {
				t.Fatal(err)
			}
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, nil, false, apply); err != nil || !applied.ManagedOnly || applied.BootstrapPEM != "" {
				t.Fatal("resumed foundation reintroduced bootstrap admission", err)
			}
			path := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "custody", "transport.json")
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("snapshot escaped private canonical storage", err)
			}
			parent, err := os.Stat(filepath.Dir(path))
			if err != nil || parent.Mode().Perm() != 0700 {
				t.Fatal("snapshot directory permissions", err)
			}
			foreign := managed
			foreign.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			if err = publishEnvironmentCreateCustodyTrust(t.Context(), cfg, runtime, &foreign, true, apply); err == nil {
				t.Fatal("substituted managed authority accepted")
			}
		})
	}
}

func TestCustodyTrustSnapshotKeepsOriginalBootstrapAndBindsManagedHandoff(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, _, _, original := createServiceRecipeFixture(t, environment)
			_, _, _, next := createServiceRecipeFixture(t, environment)
			managed := next.Root
			managed.Domain = "openbao_tls"
			generation := strings.Repeat("a", 64)
			for _, phase := range []string{"bootstrap", "prepared", "managed"} {
				t.Run(phase, func(t *testing.T) {
					var root *environmentCreateRegisteredIssuer
					if phase != "bootstrap" {
						root = &managed
					}
					defs, err := renderEnvironmentCustodyTrust(cfg, generation, original.Root.Certificate, root, phase == "managed")
					if err != nil {
						t.Fatal(err)
					}
					if len(defs) != 2 {
						t.Fatal("both original consumer namespaces require the public snapshot")
					}
					for _, d := range defs {
						data := deploymentplan.Map(d.Object["data"])
						var snapshot map[string]any
						if json.Unmarshal([]byte(deploymentplan.String(data["transport.json"])), &snapshot) != nil || snapshot["schema"] != "rtk.custody.transport/v1" || snapshot["environment"] != environment || snapshot["generation"] != generation || snapshot["server_name"] != "openbao.video-cloud-"+environment+"-secrets.svc" || snapshot["managed_only"] != (phase == "managed") {
							t.Fatal("snapshot changed the qualified environment/generation/phase")
						}
						if phase == "managed" {
							if snapshot["bootstrap_pem"] != nil || strings.Contains(deploymentplan.String(data["transport.json"]), original.Root.Certificate) || data["managed-root.pem"] != managed.Certificate {
								t.Fatal("final snapshot retains bootstrap admission or loses the managed anchor")
							}
						} else if snapshot["bootstrap_pem"] != original.Root.Certificate {
							t.Fatal("creation substituted a different bootstrap authority")
						}
					}
				})
			}
			for _, mutation := range []string{"foreign-environment", "wrong-domain", "wrong-pin", "not-root", "retiring-root", "unactivated-final", "same-authority", "private-key", "invalid-generation"} {
				t.Run(mutation, func(t *testing.T) {
					bad, gen, originalPEM := managed, generation, original.Root.Certificate
					switch mutation {
					case "foreign-environment":
						bad.Environment = "other"
					case "wrong-domain":
						bad.Domain = "service"
					case "wrong-pin":
						bad.RootFingerprint = strings.Repeat("f", 64)
					case "not-root":
						bad.Kind = "intermediate"
					case "retiring-root":
						bad.Status = "retiring"
					case "unactivated-final":
						bad.Status = "ready"
					case "same-authority":
						originalPEM = managed.Certificate
					case "private-key":
						originalPEM += "\n-----BEGIN PRIVATE KEY-----\nnot-public\n-----END PRIVATE KEY-----\n"
					case "invalid-generation":
						gen = "../other"
					}
					if _, err := renderEnvironmentCustodyTrust(cfg, gen, originalPEM, &bad, true); err == nil {
						t.Fatal("unsafe or foreign custody handoff accepted")
					}
				})
			}
			if _, err := renderEnvironmentCustodyTrust(cfg, generation, original.Root.Certificate, nil, true); err == nil {
				t.Fatal("bootstrap withdrawn before managed authority exists")
			}
		})
	}
}
