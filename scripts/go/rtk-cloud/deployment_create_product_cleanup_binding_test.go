package main

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Validate the literal receipts emitted by the production five-product native
// protocol, rather than constructing an independent expected journal by hand.
func TestProductCleanupBindingRealProtocolAndHandoffObservation(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-issuer", true: "successor-issuer"}[successor], func(t *testing.T) {
			f := newProductProtocolFixture(t, successor)
			// Reuse the maintained native Controller/CertIssuer source recipes and
			// actual five-product protocol. The separate trust-only nine-reader
			// fixture deliberately omits OpenBao configuration and is not a full
			// environment observation fixture.
			for i := range f.l.Outputs {
				f.l.Outputs[i].RootID, f.l.Outputs[i].RootSHA256, f.l.Outputs[i].PolicySHA256 = f.l.Root.ID, f.l.Root.RootFingerprint, deploymentplan.Digest(f.l.Policy)
				if !successor {
					f.l.Outputs[i].IssuerID = f.l.Intermediate.ID
				}
			}
			f.runtime.ProductIdentityScope = productIdentityScope(f.l)
			f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
			_, _, formal := completeEnvironmentRenderFixture("dev")
			installProductCleanupHandoffFixture(t, f, formal)
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
			var current []deploymentplan.Object
			for _, object := range f.b.objects {
				current = append(current, deploymentplan.Clone(object))
			}
			installed, err := readProductInstalled(f.store, f.l)
			if err != nil || len(installed.Outputs) != 5 {
				t.Fatal("missing genuine installed products", err)
			}
			currentDigest := deploymentplan.Digest(current)
			observed, err := observeEnvironmentTargetBindings(withCreateNormalTailHandoff(t.Context(), f.h), f.cfg, formal, "create", current)
			if err != nil || deploymentplan.Digest(observed.BootstrapCleanup) != deploymentplan.Digest(installed.Ceremony.CleanupBinding) {
				t.Fatal("actual next target callback rejected product cleanup", err)
			}
			if observed.BootstrapCleanup.SessionID == f.h.Bindings.BootstrapCleanup.SessionID || deploymentplan.Digest(current) != currentDigest {
				t.Fatal("old cleanup selected or inventory mutated")
			}
			for _, initial := range []bool{true, false} {
				defs, err := resolveCreateNormalTailProductTarget(f.cfg, f.l, installed, initial)
				if err != nil {
					t.Fatal(err)
				}
				preparations, err := productPreparationDefinitions(f.cfg, f.store, f.e, f.l, installed, false)
				if err != nil {
					t.Fatal("original accepted preparation owners", err)
				}
				defs = append(defs, preparations...)
				if err = validateEnvironmentPKITransition(defs, current); err != nil {
					t.Fatal("resolved target changed identity", initial, err)
				}
			}
			for _, mode := range []string{"cm-uid", "controller-uid", "record", "ca", "old-mount", "missing-controls", "pending-controls", "bad-receipt"} {
				t.Run(mode, func(t *testing.T) {
					live := make([]deploymentplan.Object, len(current))
					for i, o := range current {
						live[i] = deploymentplan.Clone(o)
					}
					e := *f.e
					e.Journal = f.e.Journal
					e.Journal.Entries = append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
					h := *f.h
					h.Execution = &e
					cmID := "ConfigMap/" + f.p.Stack + "-video-cloud/" + installed.Ceremony.CleanupBinding.ConfigMapName
					for _, o := range live {
						if deploymentplan.ID(o) == cmID {
							switch mode {
							case "cm-uid":
								deploymentplan.Map(o["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
							case "record":
								deploymentplan.Map(o["data"])["binding.json"] = "{}"
							case "ca":
								deploymentplan.Map(o["data"])["ca.crt"] = "foreign"
							}
						}
						if deploymentplan.ID(o) == "Deployment/"+f.p.Stack+"-video-cloud/pki-controller" {
							if mode == "controller-uid" {
								deploymentplan.Map(o["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
							}
							if mode == "old-mount" {
								deploymentplan.Map(managedNamedField(environmentPKIPod(o)["volumes"], "bootstrap-cleanup")["configMap"])["name"] = firstNonEmpty(f.h.Bindings.BootstrapCleanup.ConfigMapName, "pki-bootstrap-cleanup-binding")
							}
						}
					}
					key := "kubernetes/product-identities-" + productIdentityScope(f.l)[:12] + "-session-controls"
					for i := range e.Journal.Entries {
						if e.Journal.Entries[i].Key == key {
							switch mode {
							case "missing-controls":
								e.Journal.Entries = append(e.Journal.Entries[:i], e.Journal.Entries[i+1:]...)
							case "pending-controls":
								e.Journal.Entries[i].State = "pending"
								e.Journal.Entries[i].CompletedAt = time.Time{}
								e.Journal.Entries[i].Receipt = nil
							case "bad-receipt":
								e.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": deploymentplan.Digest("foreign")})
							}
							break
						}
					}
					if _, err := observeEnvironmentTargetBindings(withCreateNormalTailHandoff(t.Context(), &h), f.cfg, formal, "create", live); err == nil {
						t.Fatal("foreign product cleanup accepted", mode)
					}
				})
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

func installProductCleanupHandoffFixture(t *testing.T, f *productProtocolFixture, release environmentRelease) {
	t.Helper()
	controller := f.b.objects["Deployment/"+f.p.Stack+"-video-cloud/pki-controller"]
	name := deploymentplan.String(deploymentplan.Map(managedNamedField(environmentPKIPod(controller)["volumes"], "bootstrap-cleanup")["configMap"])["name"])
	cm := f.b.objects["ConfigMap/"+f.p.Stack+"-video-cloud/"+name]
	if cm == nil {
		// The protocol fixture retains the real seed Controller but previously
		// dropped its generated ConfigMap while filtering control Deployments.
		// Restore that exact declared seed producer, not a future dependency.
		seed := environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: "55555555-5555-4555-8555-555555555555", NamespaceUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SecretUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", PolicySHA256: deploymentplan.Digest("original exact policy"), KubernetesCA: base64.StdEncoding.EncodeToString([]byte(f.l.Root.Certificate))}
		definitions, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(f.cfg), deploymentplan.Clone(controller), seed)
		if err != nil {
			t.Fatal(err)
		}
		cm = deploymentplan.Clone(createRecipeObject(t, definitions, "ConfigMap/"+f.p.Stack+"-video-cloud/"+name))
		deploymentplan.Map(cm["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
		f.b.objects[deploymentplan.ID(cm)] = cm
		f.l.SourceOwners[deploymentplan.ID(cm)] = deploymentplan.String(deploymentplan.Map(cm["metadata"])["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(cm), Phase: "configuration", Retain: true})
	}
	var old environmentBootstrapCleanupBinding
	data := deploymentplan.Map(cm["data"])
	if decodeEnvironmentJSON([]byte(deploymentplan.String(data["binding.json"])), &old) != nil {
		t.Fatal("original binding missing")
	}
	old.KubernetesCA = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(data["ca.crt"])))
	if name != "pki-bootstrap-cleanup-binding" {
		old.ConfigMapName = name
	}
	deploymentplan.Map(f.b.objects["Namespace//"+f.p.Stack+"-video-cloud"]["metadata"])["uid"] = old.NamespaceUID
	var live []deploymentplan.Object
	for _, o := range f.b.objects {
		live = append(live, o)
	}
	captured, err := observeEnvironmentBindings("dev", release, live)
	if err != nil {
		t.Fatal(err)
	}
	captured.BootstrapCleanup = &old
	rel := filepath.Join(managedLateRedisDurableBase(f.e.Journal.PlanSHA256), "bindings.json")
	if _, err = writeEnvironmentCreateBootstrapJSON(f.store, rel, captured); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.store.Root, rel))
	if err != nil {
		t.Fatal(err)
	}
	f.h.Bindings = captured
	f.h.Pins["owner"], f.h.Pins["bindings"] = deploymentplan.Digest(f.e.owner), environmentPreflightBytesSHA256(raw)
	before, err := observeEnvironmentTargetBindings(withCreateNormalTailHandoff(t.Context(), f.h), f.cfg, release, "create", live)
	if err != nil || deploymentplan.Digest(before.BootstrapCleanup) != deploymentplan.Digest(&old) {
		t.Fatal("original handoff fixture invalid", err)
	}
}
