package main

import (
	"context"
	"io"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Validate the literal receipts emitted by the production five-product native
// protocol, rather than constructing an independent expected journal by hand.
func TestProductStageReaderConsumesProductionProtocolReceipts(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-issuer", true: "successor-issuer"}[successor], func(t *testing.T) {
			f := newProductProtocolFixture(t, successor)
			// Use the same entire current reader graph as the native trust recipe
			// contract, rather than only Controller and CertIssuer stand-ins.
			originalDefinitions := f.l.SourceDefinitions
			originalByID := map[string]deploymentplan.Definition{}
			originalNative := map[string]deploymentplan.Object{}
			for _, d := range originalDefinitions {
				id := deploymentplan.ID(d.Object)
				originalByID[id] = d
				originalNative[id] = deploymentplan.Clone(f.b.objects[id])
			}
			_, _, native, _ := productTrustFixtureFromProtocol(t, f)
			// Keep the real maintained Controller/CertIssuer recipes (including
			// their cleanup identity), and extend that existing profile with the
			// remaining current reader fixtures rather than replacing it.
			for i, d := range f.l.SourceDefinitions {
				id := deploymentplan.ID(d.Object)
				if old, exists := originalByID[id]; exists {
					f.l.SourceDefinitions[i] = old
				}
			}
			for i, object := range native {
				id := deploymentplan.ID(object)
				if old := originalNative[id]; old != nil {
					native[i] = old
					f.l.SourceOwners[id] = deploymentplan.String(deploymentplan.Map(old["metadata"])["uid"])
				}
			}
			declared := map[string]bool{}
			for _, d := range f.l.SourceDefinitions {
				declared[deploymentplan.ID(d.Object)] = true
			}
			for _, d := range originalDefinitions {
				if !declared[deploymentplan.ID(d.Object)] {
					f.l.SourceDefinitions = append(f.l.SourceDefinitions, d)
				}
			}
			for _, object := range native {
				metadata := deploymentplan.Map(object["metadata"])
				metadata["resourceVersion"] = "1"
				metadata["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack}
				metadata["generation"] = float64(1)
				if object["kind"] == "Deployment" {
					metadata["annotations"] = deploymentplan.Object{"deployment.kubernetes.io/revision": "7", "policy.example/original-owner": "preserved"}
				}
				if object["kind"] != "ConfigMap" {
					object["status"] = deploymentplan.Object{"observedGeneration": float64(1), "readyReplicas": float64(1)}
				}
				f.b.objects[deploymentplan.ID(object)] = deploymentplan.Clone(object)
			}
			// Re-freeze these actual API snapshots (including native status and
			// preexisting revision) through the production source selector.
			bindings := environmentIdentityBindings{Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}
			if err := bindCreateNormalTailProductSourceDefinitions(f.l, f.l.SourceDefinitions, native, bindings); err != nil {
				t.Fatal(err)
			}
			for i := range f.l.SourceTrust {
				if f.l.SourceTrust[i].Baseline["kind"] != "ConfigMap" {
					f.l.SourceTrust[i].Baseline["status"] = deploymentplan.Object{"observedGeneration": float64(1), "readyReplicas": float64(1)}
				}
			}
			for i := range f.l.Outputs {
				f.l.Outputs[i].RootID, f.l.Outputs[i].RootSHA256, f.l.Outputs[i].PolicySHA256 = f.l.Root.ID, f.l.Root.RootFingerprint, deploymentplan.Digest(f.l.Policy)
				if !successor {
					f.l.Outputs[i].IssuerID = f.l.Intermediate.ID
				}
			}
			f.runtime.ProductIdentityScope = productIdentityScope(f.l)
			f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
			digest := deploymentplan.Digest("frozen-protocol-inputs")
			p := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), HandoffSHA256: deploymentplan.Digest(f.h.Pins), ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(f.cfg), ClusterSHA256: digest, IdentitySHA256: digest, Release: environmentRelease{Environment: "dev", Stack: f.p.Stack}, Bindings: environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}, Products: f.l, Definitions: f.l.SourceDefinitions}
			checks := map[string]func(context.Context) error{}
			for _, id := range deploymentplan.RequiredGateIDs(false) {
				checks[id] = func(ctx context.Context) error {
					if _, err := createNormalTailProductOutputContracts(f.cfg, f.l); err != nil {
						return err
					}
					return f.e.Check(ctx)
				}
			}
			q, err := qualifyCreateNormalTailProgram(t.Context(), &p, checks, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			a := createNormalTailProgramArtifact{Program: p, Qualification: q, CheckerSHA256: checker, Generation: digest, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
			if err = saveCreateNormalTailProgramArtifact(f.store, f.e, a); err != nil {
				t.Fatal(err)
			}
			if err = writeEnvironmentPreflightActive(f.store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: digest, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(p), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(p) + "/qualification-1"}); err != nil {
				t.Fatal(err)
			}
			if _, err = admitCreateNormalTailProgramWithFence(t.Context(), f.cfg, f.h, a, time.Hour, time.Now().UTC(), f.e.Check); err != nil {
				t.Fatal(err)
			}
			start := len(f.e.Journal.Entries)
			apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
				if f.plans[full] == nil {
					var live []deploymentplan.Object
					for _, o := range f.b.objects {
						live = append(live, o)
					}
					plan, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
					if err != nil {
						return err
					}
					f.plans[full] = plan
				}
				if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, full, f.plans[full]); err != nil {
					return err
				}
				return f.apply(ctx, stage, defs)
			}
			if err = executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, apply); err != nil {
				t.Fatal("production protocol", err)
			}
			for _, original := range f.l.SourceTrust {
				current := f.b.objects[original.ID]
				if deploymentplan.Map(current["metadata"])["uid"] != f.l.SourceOwners[original.ID] {
					t.Fatal("native response changed the original reader owner", original.ID)
				}
				if original.Baseline["kind"] == "Deployment" && successor {
					revision := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(current["metadata"])["annotations"])["deployment.kubernetes.io/revision"])
					if revision == "7" || revision == "" || deploymentplan.Digest(current["status"]) != deploymentplan.Digest(original.Baseline["status"]) {
						t.Fatal("fixture lost actual controller revision or preserved status", original.ID)
					}
				}
			}
			entries := f.e.Journal.Entries[start:]
			consumed, children, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, entries)
			if err != nil || consumed != len(entries) || len(children) != len(f.plans) {
				t.Fatal("production receipts and semantic reader disagree", consumed, len(entries), len(children), len(f.plans), err)
			}
			// Every persisted prefix is readable without requiring the next native
			// artifact. Use that same historical frame when deriving cumulative roles.
			for end := 1; end <= len(entries); end++ {
				prefix := *f.e
				prefix.Journal = f.e.Journal
				prefix.Journal.Entries = f.e.Journal.Entries[:start+end]
				consumed, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, &prefix, f.l, entries[:end])
				if err != nil || consumed != end {
					t.Fatalf("original persisted prefix %d/%d rejected: %v", end, len(entries), err)
				}
			}
		})
	}
}
