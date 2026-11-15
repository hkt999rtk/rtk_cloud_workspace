package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func produceOpenBaoFirstTestMaterial(t *testing.T, ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, key string) error {
	t.Helper()
	return createOpenBaoFirstMaterial(ctx, cfg, store, e, key, func() error {
		if key == "management-transport-material" {
			_, err := createEnvironmentTransport(store, e.Journal.PlanSHA256, "management", openBaoFirstManagementNames(cfg), time.Now().UTC())
			return err
		}
		_, err := ensureEnvironmentCreateAccountSigner(store)
		return err
	})
}

func TestOpenBaoFirstMaterialRequiresOriginalServicesOwners(t *testing.T) {
	for _, key := range []string{"management-transport-material", "account-signer-material"} {
		t.Run(key, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			if err := produceOpenBaoFirstTestMaterial(t, t.Context(), cfg, store, e, key); err != nil {
				t.Fatal(err)
			}
			values, _, _, err := readOpenBaoFirstMaterial(t.Context(), cfg, store, e, key, nil)
			if err != nil {
				t.Fatal(err)
			}
			var defs []deploymentplan.Definition
			if key == "management-transport-material" {
				var material environmentCreateTransport
				if err := decodeEnvironmentJSON(values["identities.json"], &material); err != nil {
					t.Fatal(err)
				}
				for name, identity := range material.Identities {
					ns := e.Journal.Stack + "-video-cloud"
					if name == "account-manager" {
						ns = e.Journal.Stack + "-account-manager"
					}
					defs = append(defs, environmentCreateSecret(ns, "pki-create-"+name+"-transport", map[string]string{"ca.crt": material.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}))
				}
			} else {
				signer := map[string]string{}
				for name, value := range values {
					signer[name] = string(value)
				}
				defs = append(defs, environmentCreateSecret(e.Journal.Stack+"-account-manager", "account-manager-jwt", signer))
			}
			parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
			if err != nil {
				t.Fatal(err)
			}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("foundation-services", defs)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
			fence := func(ctx context.Context, p *deploymentplan.Plan) error {
				return b.CheckParentExecution(ctx, p, store.Root)
			}
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "foundation-services", p, b, fence, fence, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }, e.Check, io.Discard); err != nil {
				t.Fatal("record original service owners", err)
			}
			if _, _, _, err := readOpenBaoFirstMaterial(t.Context(), cfg, store, e, key, b); err != nil {
				t.Fatal("recorded original material was rejected", err)
			}
			id := p.Resources[0].ID
			deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "foreign-owner"
			if _, _, _, err := readOpenBaoFirstMaterial(t.Context(), cfg, store, e, key, b); err == nil {
				t.Fatal("foreign service Secret owner was accepted")
			}
		})
	}
}

func TestOpenBaoFirstMaterialCheckpointPreservesOriginalIdentity(t *testing.T) {
	for _, key := range []string{"management-transport-material", "account-signer-material"} {
		for _, mode := range []string{"complete", "pending", "allocated", "foreign-existing", "missing-private", "missing-derived", "changed-bytes"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				cfg, store, e := openBaoFirstTransportFixture(t)
				ctx := t.Context()
				_, paths, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, key)
				if err != nil {
					t.Fatal(err)
				}
				private, derived := "identities.json", "ca.crt"
				if key == "account-signer-material" {
					private, derived = "access.key", "access.pub"
				}
				switch mode {
				case "foreign-existing":
					if err := store.write(paths[private], []byte("foreign original"), false); err != nil {
						t.Fatal(err)
					}
					called := false
					if err := createOpenBaoFirstMaterial(ctx, cfg, store, e, key, func() error { called = true; return nil }); err == nil || called || createOpenBaoACKCompletionEntry(e.Journal, key) != nil {
						t.Fatal("unrecorded existing material was adopted")
					}
					return
				case "pending":
					err := createOpenBaoFirstMaterial(ctx, cfg, store, e, key, func() error {
						if err := store.write(paths[private], []byte("partial original"), false); err != nil {
							return err
						}
						return errors.New("interrupted")
					})
					if err == nil {
						t.Fatal("interruption was not preserved")
					}
					if _, _, _, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, key, nil); err == nil {
						t.Fatal("pending unknown material was accepted")
					}
					if err := produceOpenBaoFirstTestMaterial(t, ctx, cfg, store, e, key); err == nil {
						t.Fatal("pending original operation was replayed")
					}
					return
				default:
					if err := produceOpenBaoFirstTestMaterial(t, ctx, cfg, store, e, key); err != nil {
						t.Fatal("initial checkpoint", err)
					}
				}
				if mode == "allocated" {
					entry := createOpenBaoACKCompletionEntry(e.Journal, key)
					entry.State = "allocated"
					entry.CompletedAt = time.Time{}
					if err := e.save(); err != nil {
						t.Fatal(err)
					}
				}
				switch mode {
				case "missing-private", "missing-derived":
					name := private
					if mode == "missing-derived" {
						name = derived
					}
					if err := os.Remove(filepath.Join(store.Root, paths[name])); err != nil {
						t.Fatal(err)
					}
				case "changed-bytes":
					if err := store.write(paths[private], []byte("foreign material"), true); err != nil {
						t.Fatal(err)
					}
				}
				before := deploymentplan.Digest(e.Journal)
				values, missing, state, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, key, nil)
				if deploymentplan.Digest(e.Journal) != before {
					t.Fatal("preflight material check mutated journal")
				}
				if mode == "missing-private" || mode == "changed-bytes" {
					if err == nil {
						t.Fatal("missing or changed private identity accepted")
					}
					return
				}
				if err != nil || len(values) != len(paths) {
					t.Fatal("original material unavailable", err)
				}
				if mode == "missing-derived" {
					if len(missing) != 1 || missing[0] != derived {
						t.Fatal("derived file not identified")
					}
					if _, err := os.Lstat(filepath.Join(store.Root, paths[derived])); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("preflight wrote derived file")
					}
				} else if len(missing) != 0 {
					t.Fatal("unexpected missing material")
				}
				if mode == "allocated" && state != "allocated" {
					t.Fatal("original allocated state lost")
				}
				if err := completeOpenBaoFirstMaterial(ctx, cfg, store, e, key, nil); err != nil {
					t.Fatal("safe original material completion failed", err)
				}
				if _, missing, state, err = readOpenBaoFirstMaterial(ctx, cfg, store, e, key, nil); err != nil || len(missing) != 0 || state != "completed" {
					t.Fatal("original material retry did not complete", err)
				}
			})
		}
	}
}
