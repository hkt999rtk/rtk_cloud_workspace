package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoFirstTransportFixture(t *testing.T) (deploymentConfig, secretStore, *environmentCreateExecution) {
	t.Helper()
	cfg, store, operator, prior := createStateFixture(t, "dev")
	var plan environmentCreatePlan
	if err := json.Unmarshal(prior.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	plan.OpenBaoFirst = true
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := prior.Qualification
	q.PlanSHA256 = sha
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := publishEnvironmentCreateArtifact(t.Context(), cfg, generation, raw, operator, q, prior.ReleaseProofs, prior.QualifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return cfg, store, e
}

func recordOpenBaoFirstTestTransport(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, dns []string, state string) environmentCreateTransport {
	t.Helper()
	intent := openBaoFirstTransportIntent(cfg)
	if err := e.BeginEntry("openbao-transport-material", "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	material, err := createEnvironmentTransport(store, e.Journal.PlanSHA256, "openbao", map[string][]string{"openbao": dns}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if state == "pending" {
		return material
	}
	raw, err := readProviderStorageKeyFile(filepath.Join(store.Root, "deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport", "identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(map[string]string{"identities_sha256": environmentPreflightBytesSHA256(raw), "ca_sha256": environmentPreflightBytesSHA256([]byte(material.CACert))})
	if err := e.RecordReceipt("openbao-transport-material", receipt); err != nil {
		t.Fatal(err)
	}
	if state == "completed" {
		if err := e.CompleteEntry("openbao-transport-material", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return material
}

func TestOpenBaoFirstTransportRequiresRecordedSecretOwner(t *testing.T) {
	for _, mode := range []string{"original", "foreign-uid", "foreign-transport"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			original := recordOpenBaoFirstTestTransport(t, cfg, store, e, lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg)), "completed")
			parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
			if err != nil {
				t.Fatal(err)
			}
			identity := original.Identities["openbao"]
			def := environmentCreateSecret(e.Journal.Stack+"-secrets", "openbao-transport-tls", map[string]string{"ca.crt": original.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key})
			defs := []deploymentplan.Definition{def}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("foundation", defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return b.CheckParentExecution(ctx, p, store.Root)
			}
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "foundation", p, b, fence, fence, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }, e.Check, io.Discard); err != nil {
				t.Fatal("record original Secret owner", err)
			}
			id := deploymentplan.ID(def.Object)
			switch mode {
			case "foreign-uid":
				deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign-owner"
			case "foreign-transport":
				foreign, err := createEnvironmentTransport(store, strings.Repeat("e", 64), "openbao", map[string][]string{"openbao": lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg))}, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyOpenBaoFirstTransportStage(t.Context(), cfg, store, e, parent, foreign, b); err == nil {
					t.Fatal("recorded child accepted a self-consistent foreign authority")
				}
				base := filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport")
				raw, _ := json.Marshal(foreign)
				if err := store.write(filepath.Join(base, "identities.json"), raw, true); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(store.Root, base, "ca.crt")); err != nil {
					t.Fatal(err)
				}
				if _, err := restoreOpenBaoFirstDerivedCA(t.Context(), cfg, store, e, b); err == nil {
					t.Fatal("foreign self-consistent authority restored a CA")
				}
			}
			got, exists, missing, err := readOpenBaoFirstRetainedTransport(t.Context(), cfg, store, e, b)
			if mode == "original" {
				if err != nil || !exists || missing || deploymentplan.Digest(got) != deploymentplan.Digest(original) {
					t.Fatal("recorded original Secret owner was not retained", err)
				}
			} else if err == nil {
				t.Fatal("foreign original Secret owner or authority accepted")
			}
		})
	}
}

func TestOpenBaoFirstTransportRetainsOriginalAndRestoresOnlyDerivedCA(t *testing.T) {
	for _, mode := range []string{"absent", "ready", "allocated", "pending", "unrecorded", "missing-derived", "missing-identities", "wrong-key", "wrong-dns", "changed-config", "prior-foundation"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			var initial environmentCreateTransport
			if mode != "absent" && mode != "prior-foundation" {
				dns := lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg))
				if mode == "wrong-dns" {
					dns = []string{"wrong.example.test"}
				}
				state := "completed"
				if mode == "allocated" || mode == "pending" {
					state = mode
				}
				if mode == "unrecorded" {
					var err error
					initial, err = createEnvironmentTransport(store, e.Journal.PlanSHA256, "openbao", map[string][]string{"openbao": dns}, time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
				} else {
					initial = recordOpenBaoFirstTestTransport(t, cfg, store, e, dns, state)
				}
			}
			base := filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport")
			switch mode {
			case "missing-derived":
				if err := os.Remove(filepath.Join(store.Root, base, "ca.crt")); err != nil {
					t.Fatal(err)
				}
			case "missing-identities":
				if err := os.Remove(filepath.Join(store.Root, base, "identities.json")); err != nil {
					t.Fatal(err)
				}
			case "wrong-key":
				identity := initial.Identities["openbao"]
				identity.Key = initial.CACert
				initial.Identities["openbao"] = identity
				raw, _ := json.Marshal(initial)
				if err := store.write(filepath.Join(base, "identities.json"), raw, true); err != nil {
					t.Fatal(err)
				}
			case "changed-config":
				cfg.Values["CLOUD_STACK_NAME"] = "video-cloud-foreign"
			case "prior-foundation":
				if err := e.BeginEntry("kubernetes/foundation", "foundation", []byte(`{"stage":"foundation"}`), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			got, exists, missing, err := readOpenBaoFirstRetainedTransport(t.Context(), cfg, store, e, nil)
			switch mode {
			case "absent":
				if err != nil || exists || missing {
					t.Fatal("unstarted generation was not recognized", err)
				}
			case "ready":
				if err != nil || !exists || missing || deploymentplan.Digest(got) != deploymentplan.Digest(initial) {
					t.Fatal("original transport was not retained", err)
				}
			case "allocated":
				if err != nil || !exists || missing || createOpenBaoACKCompletionEntry(e.Journal, "openbao-transport-material").State != "allocated" {
					t.Fatal("preflight changed or rejected recorded allocated material", err)
				}
				completed, err := completeOpenBaoFirstRetainedTransport(t.Context(), cfg, store, e, nil)
				if err != nil || deploymentplan.Digest(completed) != deploymentplan.Digest(initial) || createOpenBaoACKCompletionEntry(e.Journal, "openbao-transport-material").State != "completed" {
					t.Fatal("recorded original material could not be completed", err)
				}
			case "missing-derived":
				if err != nil || !exists || !missing {
					t.Fatal("missing derived CA was not recognized", err)
				}
				restored, err := restoreOpenBaoFirstDerivedCA(t.Context(), cfg, store, e, nil)
				if err != nil || deploymentplan.Digest(restored) != deploymentplan.Digest(initial) {
					t.Fatal("original CA restoration changed transport", err)
				}
				if _, exists, missing, err = readOpenBaoFirstRetainedTransport(t.Context(), cfg, store, e, nil); err != nil || !exists || missing {
					t.Fatal("restored CA is not reusable", err)
				}
			default:
				if err == nil {
					t.Fatal("changed or incomplete original transport accepted")
				}
			}
		})
	}
}
