package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreatePublicTLSPlanOmissionPreservesHistoricalWire(t *testing.T) {
	p := environmentCreatePlan{Schema: "rtk-cloud.create-plan/v1", Environment: "dev", Stack: "video-cloud-dev"}
	raw, err := json.Marshal(p)
	if err != nil || strings.Contains(string(raw), "public_tls") {
		t.Fatal("omitted historical public TLS recipe changed the plan wire", err)
	}
	var parsed environmentCreatePlan
	if json.Unmarshal(raw, &parsed) != nil || deploymentplan.Digest(parsed) != deploymentplan.Digest(p) || parsed.PublicTLS != nil {
		t.Fatal("historical create plan cannot round trip")
	}
}

func TestCreatePublicTLSFreshIntentUsesSelectedCatalogWithoutFutureMaterial(t *testing.T) {
	cfg, store, _, _, _ := createDependencyContractFixture(t)
	t.Setenv("LKE_PUBLIC_HTTPS_TLS_SECRET", "ambient-foreign-secret")
	p, err := prepareEnvironmentCreatePublicTLS(t.Context(), cfg, store)
	if err != nil || p == nil || p.Source != nil || p.OwnerUID != "" || p.SecretID != "Secret/"+cfg.Values["CLOUD_STACK_NAME"]+"-ingress/"+cfg.Values["CLOUD_STACK_NAME"]+"-public-tls" || p.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
		t.Fatal("fresh public TLS intent invented material or used ambient input", err)
	}
	values := deploymentEndpointValues(cfg)
	defs, err := decodeEnvironmentCreateManifests(lkePublicHTTPSIngressManifests(values, deploymentPublicHTTPSRoutes(values))...)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindEnvironmentPublicTLSReferences(p, defs)
	if err != nil {
		t.Fatal(err)
	}
	consumers, hosts, id, err := environmentPublicTLSConsumers(bound)
	outputs, contractErr := environmentPublicTLSOutputContract(p)
	if err != nil || contractErr != nil || id != p.SecretID || deploymentplan.Digest(consumers) != deploymentplan.Digest(p.Consumers) || deploymentplan.Digest(hosts) != deploymentplan.Digest(p.Hosts) || len(outputs) != 1 || outputs[id] != deploymentplan.Digest(p) {
		t.Fatal("fresh consumer graph and actual producer intent differ", err, contractErr)
	}
	for _, path := range []string{publicTLSBase(p), "kube/kubeconfig.yaml", "deployment/create/material"} {
		if _, err := os.Lstat(filepath.Join(store.Root, path)); !os.IsNotExist(err) {
			t.Fatal("read-only intent preparation created future native/private state", path, err)
		}
	}
}

// The ACME server signs the actual frozen CSR, and the ordinary child executor
// records native writes, leases and acceptance. Only HTTP/DNS/Kubernetes
// transport are replaced; no native success entry is manufactured here.
func TestCreatePublicTLSRealProtocolProgressResumesThroughSemanticReader(t *testing.T) {
	for _, failure := range []string{"none", "finalize-response", "before-admission", "after-write-configuration", "acceptance", "parent-receipt", "after-publication", "unknown-write", "unknown-order"} {
		t.Run(failure, func(t *testing.T) {
			cfg, store, e, defs, p, ops, server, b, _ := publicTLSProtocolFixture(t)
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
			p.ConfigurationSHA256 = deploymentplan.Digest(cfg)
			outputs, err := environmentCreateProductIdentityIntent(cfg)
			if err != nil || len(outputs) == 0 {
				t.Fatal(err)
			}
			h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"public-tls": deploymentplan.Digest(p)}}
			digest := deploymentplan.Digest("isolated frozen input")
			program := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), HandoffSHA256: deploymentplan.Digest(h.Pins), ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(cfg), ClusterSHA256: digest, IdentitySHA256: digest, Release: environmentRelease{Environment: "dev", Stack: e.Journal.Stack}, Bindings: environmentIdentityBindings{Environment: "dev", Stack: e.Journal.Stack}, Definitions: defs, PublicTLS: p}
			program.Products = &createNormalTailProductLifecycle{ParentSHA256: program.ParentSHA256, OwnerSHA256: program.OwnerSHA256, SourceDefinitions: defs, Outputs: outputs}
			checks := map[string]func(context.Context) error{}
			for _, id := range deploymentplan.RequiredGateIDs(false) {
				checks[id] = func(ctx context.Context) error { return e.Check(ctx) }
			}
			q, err := qualifyCreateNormalTailProgram(t.Context(), &program, checks, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			a := createNormalTailProgramArtifact{Program: program, Qualification: q, CheckerSHA256: checker, Generation: digest, QualifiedAt: now, ValidUntil: now.Add(10 * time.Minute)}
			if saveCreateNormalTailProgramArtifact(store, e, a) != nil || writeEnvironmentPreflightActive(store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(program) + "/qualification-1"}) != nil {
				t.Fatal("cannot publish isolated genuine program qualification")
			}
			admission, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, time.Hour, now, func(ctx context.Context) error { return e.Check(ctx) })
			if err != nil {
				t.Fatal(err)
			}
			start := len(e.Journal.Entries)
			read := func() error {
				n, children, err := validateCreateNormalTailPublicTLSStages(store, e, p, e.Journal.Entries[start:])
				if err == nil && n != len(e.Journal.Entries)-start {
					return errors.New("genuine public TLS prefix was not fully consumed")
				}
				for _, child := range children {
					matched := false
					for _, lease := range e.Journal.KubernetesLeases {
						matched = matched || managedLateLeaseMatches(lease, e.Journal.Stack, child.SHA256())
					}
					if !matched {
						return errors.New("public TLS child lacks its genuine parent lease")
					}
				}
				return err
			}
			var child *deploymentplan.Plan
			var originalInput string
			lost := false
			apply := func(ctx context.Context, d deploymentplan.Definition) (string, error) {
				if err := read(); err != nil {
					return "", err
				}
				if child == nil {
					originalInput = deploymentplan.Digest(d)
					child, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: program.MaintainedConfigSHA256, ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA("public-tls", []deploymentplan.Definition{d}), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{d}, nil)
					if err != nil {
						return "", err
					}
					if err = saveCreateNormalTailStagePlan(ctx, store, e, "public-tls", child); err != nil {
						return "", err
					}
				} else if originalInput != deploymentplan.Digest(d) {
					return "", errors.New("original TLS publication input changed")
				}
				err = executeCreateNormalTailStageBackend(ctx, store, h, "public-tls", child, b, func(ctx context.Context, p *deploymentplan.Plan) error {
					if err := read(); err != nil {
						return err
					}
					if err := b.trip("before-admission"); err != nil {
						return err
					}
					return p.AdmitResumable(ctx, b, store.Root)
				}, func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error {
					if err := p.Verify(ctx, b); err != nil {
						return err
					}
					return b.trip("acceptance")
				}, io.Discard, func(context.Context) error { return b.trip("parent-receipt") })
				if err != nil {
					return "", err
				}
				if failure == "after-publication" && !lost {
					lost = true
					return "", errors.New("published original child response lost")
				}
				return environmentCreatePublicTLSAcceptedUID(ctx, store, e, d)
			}
			if failure == "finalize-response" {
				server.failFinalize = true
			} else if failure == "unknown-order" {
				server.failOrder = true
			} else {
				b.failure = failure
			}
			err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops)
			if failure != "none" && err == nil {
				t.Fatal("interruption was not reached", failure)
			}
			readErr := read()
			if failure == "unknown-write" || failure == "unknown-order" {
				if readErr == nil || executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil || server.orders != 1 || b.writes[p.SecretID] > 1 {
					t.Fatal("unknown outcome allowed a duplicate request/write")
				}
				return
			}
			if readErr != nil {
				t.Fatal("genuine persisted protocol prefix cannot be read", readErr)
			}
			if err != nil {
				if err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err != nil {
					t.Fatal("known original protocol could not resume", err)
				}
			}
			if err = read(); err != nil {
				t.Fatal("complete genuine protocol was not consumed", err)
			}
			entriesBefore := deploymentplan.Digest(e.Journal.Entries)
			leasesBefore := append([]deploymentplan.LeaseOwner(nil), e.Journal.KubernetesLeases...)
			if err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err != nil || deploymentplan.Digest(e.Journal.Entries) != entriesBefore || len(e.Journal.KubernetesLeases) < len(leasesBefore) || deploymentplan.Digest(e.Journal.KubernetesLeases[:len(leasesBefore)]) != deploymentplan.Digest(leasesBefore) || server.orders != 1 || server.finalizes != 1 || b.writes[p.SecretID] != 1 {
				t.Fatal("completed certificate or original child was replayed", err)
			}
			after, err := readCreateNormalTailProgramAdmission(store, e)
			if err != nil || after != admission {
				t.Fatal("original program window changed", err)
			}
			// Every completed prefix comes from this actual producer, not a
			// synthesized receipt. Pure history must leave the parent unchanged.
			before := deploymentplan.Digest(e.Journal)
			for n := 0; n <= len(e.Journal.Entries)-start; n++ {
				if _, _, err := validateCreateNormalTailPublicTLSStages(store, e, p, e.Journal.Entries[start:start+n]); err != nil {
					t.Fatal("genuine completed historical prefix rejected", n, err)
				}
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("pure reader changed the original journal")
			}
			if failure == "none" {
				var material environmentPublicTLSMaterial
				if publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "material.json"), &material) != nil {
					t.Fatal("genuine material missing")
				}
				definition := publicTLSDefinition(p, material, p.OwnerUID)
				if uid, err := environmentCreatePublicTLSAcceptedUID(t.Context(), store, e, definition); err != nil || uid == "" {
					t.Fatal("fresh callback cannot consume the genuine original child", err)
				}
				deploymentplan.Map(definition.Object["metadata"])["name"] = "foreign-secret"
				if _, err := environmentCreatePublicTLSAcceptedUID(t.Context(), store, e, definition); err == nil {
					t.Fatal("fresh callback accepted another Secret")
				}
				if publicTLSOriginalURLValid(p, "https://foreign.example.test/account/1") || publicTLSOriginalURLValid(p, p.DirectoryURL+"#foreign") {
					t.Fatal("historical ACME evidence accepted a different origin")
				}
			}
			// A genuine child cannot bless a replacement completion owner.
			var completion environmentPublicTLSReceipt
			path := filepath.Join(publicTLSBase(p), "completed.json")
			if publicTLSReadJSON(store, path, &completion) != nil {
				t.Fatal("completion missing")
			}
			completion.SecretUID = "99999999-9999-4999-8999-999999999999"
			absolute, _ := store.safePath(path)
			if os.WriteFile(absolute, mustProviderRecoveryJSON(completion), 0600) != nil || read() == nil {
				t.Fatal("foreign public TLS completion owner passed")
			}
		})
	}
}
