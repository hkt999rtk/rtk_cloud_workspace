package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func crlAcceptedTargetExecutionFixture(t *testing.T, domain string) (*crlExecutorFixture, *createServiceSessionRecovery, *createManagedLateSessionRecovery, environmentCreateRegisteredIssuer, createRootCRLRecord) {
	t.Helper()
	f := newCRLExecutorFixture(t)
	chain := newManagedLateBootstrapChainFixture(t)
	root := chain.ceremony.Root
	root.Domain = domain
	if domain == "openbao_tls" {
		root.ID = "734487b8-abcf-40be-a813-8b52c64f7954"
	}
	root.Document = deploymentplan.Clone(root.Document)
	root.Document["issuer_id"], root.Document["trust_domain"] = root.ID, domain
	prior := signedRootCRLFixture(t, root, chain.rootKey, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "1", ThisUpdate: chain.now.Add(-48 * time.Hour), NextUpdate: chain.now.Add(-24 * time.Hour)})
	ica := chain.ceremony.Intermediate
	ica.ID, ica.Domain, ica.Kind, ica.Status = openBaoTLSOriginalIntermediateID, domain, "intermediate", "active"
	ica.RootFingerprint, ica.ChainPEM = root.RootFingerprint, ica.Certificate+root.Certificate
	ica.Document = deploymentplan.Object{"parent_issuer_id": root.ID, "signer_provider": "openbao"}
	icaRecord := signedRootCRLFixture(t, ica, chain.icaKey, createRootCRLRequest{IssuerID: ica.ID, Fingerprint: environmentPreflightBytesSHA256(chain.icaCertificate.Raw), Number: "3", ThisUpdate: chain.now.Add(-time.Hour), NextUpdate: chain.now.Add(24 * time.Hour)})
	ns := f.execution.Journal.Stack + "-video-cloud"
	cmName := "pki-create-mqtt-consumer-trust"
	if domain == "openbao_tls" {
		cmName = "pki-create-openbao-tls-consumer-trust"
	}
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": cmName, "namespace": ns, "uid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, "data": deploymentplan.Object{}}
	if domain == "openbao_tls" {
		deploymentplan.Map(cm["data"])["crls.json"] = environmentCreateServiceCRLManifest(root, "/var/lib/pki-service/private/crl/openbao-root.json", ica)
	}
	objects := []deploymentplan.Object{{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}, cm}
	names := []string{"pki-controller", "video-cloud-api", "video-cloud-logingester"}
	if domain == "openbao_tls" {
		names = []string{"pki-controller", "certissuer"}
	}
	state := f.state(t)
	state.Objects = map[string]deploymentplan.Object{}
	state.Caches = map[string]createRootCRLRecord{}
	state.CacheFingerprints = map[string]string{}
	state.Snapshots = map[string]createRootCRLNativeSnapshot{ica.ID: {Prior: icaRecord}}
	for i, name := range names {
		_, native, _ := targetWorkloadFixture(t, "dev", "Deployment")
		uid := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", 100*i+n) }
		for j, o := range native {
			text := strings.NewReplacer("video-cloud-dev-application", ns, "original-controller", uid(1), "original-pod", uid(2), "original-replicaset", uid(3), "application-selected", name+"-selected", "application-0", name+"-0", `"application"`, `"`+name+`"`).Replace(string(mustProviderRecoveryJSON(o)))
			if err := json.Unmarshal([]byte(text), &native[j]); err != nil {
				t.Fatal(err)
			}
		}
		controller, pod := native[0], native[1]
		spec := environmentPKIPod(controller)
		spec["securityContext"] = deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001)}
		app := managedNamedField(spec["containers"], "app")
		claim := ""
		if domain == "openbao_tls" {
			app["name"] = name
			app["env"] = environmentCreateEnv(map[string]string{"OPENBAO_SERVER_CRL_MANIFEST": "/run/pki-openbao-create/crls.json", "OPENBAO_SERVER_PKI_ROOT_SHA256": root.RootFingerprint})
			app["volumeMounts"] = []any{environmentCreateServiceMount("openbao-create-trust", "/run/pki-openbao-create", true), environmentCreateServiceMount("service-state", "/var/lib/pki-service", false)}
			claim = environmentCreateServiceOwnerClaim(name)
			spec["volumes"] = []any{environmentCreateServiceVolume("openbao-create-trust", "configMap", cmName), environmentCreateServiceVolume("service-state", "persistentVolumeClaim", claim)}
			state.Caches[name+"-0:/var/lib/pki-service/private/crl/openbao-root.json"] = prior
			state.Caches[name+"-0:/var/lib/pki-service/private/crl/openbao-root-"+ica.ID+".json"] = icaRecord
			state.CacheFingerprints[name+"-0:/var/lib/pki-service/private/crl/openbao-root.json"] = root.RootFingerprint
			state.CacheFingerprints[name+"-0:/var/lib/pki-service/private/crl/openbao-root-"+ica.ID+".json"] = environmentPreflightBytesSHA256(chain.icaCertificate.Raw)
		} else if name != "pki-controller" {
			claim = name + "-mqtt-trust"
			app["env"] = environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "/run/mqtt-pki/crls.json"})
			app["volumeMounts"] = []any{environmentCreateServiceMount("mqtt-pki-crls", "/run/mqtt-pki", true), environmentCreateServiceMount("mqtt-bundle-state", "/var/lib/mqtt-trust", false)}
			spec["volumes"] = []any{environmentCreateServiceVolume("mqtt-pki-crls", "configMap", cmName), environmentCreateServiceVolume("mqtt-bundle-state", "persistentVolumeClaim", claim)}
			state.Caches[name+"-0:/var/lib/mqtt-trust/root-crl.json"] = prior
			state.CacheFingerprints[name+"-0:/var/lib/mqtt-trust/root-crl.json"] = root.RootFingerprint
		}
		pod["spec"] = deploymentplan.Clone(spec)
		if domain == "openbao_tls" {
			deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["name"] = name
		}
		objects = append(objects, native...)
		if claim != "" {
			objects = append(objects, deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": ns, "uid": uid(4)}, "status": deploymentplan.Object{"phase": "Bound"}})
		}
	}
	defs := []deploymentplan.Definition{}
	for _, o := range objects {
		meta := deploymentplan.Map(o["metadata"])
		meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.execution.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
		meta["resourceVersion"] = "1"
		state.Objects[deploymentplan.ID(o)] = o
		if o["kind"] == "Pod" || o["kind"] == "ReplicaSet" || o["kind"] == "Namespace" {
			continue
		}
		defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Projection(o), Phase: "workload", Retain: true})
	}
	stage := "isolated-accepted-" + strings.ReplaceAll(domain, "_", "-") + "-consumer-source"
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: f.execution.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("isolated accepted release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, objects)
	if err != nil {
		t.Fatal("accepted target stage compile", err)
	}
	memory := freshStageBackend{&normalTailE2EBackend{e: f.execution, objects: state.Objects, writes: map[string]int{}}}
	if err := executeEnvironmentCreateStagePlan(t.Context(), f.store, f.execution, stage, p, memory, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, memory, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error {
		return memory.CheckParentExecution(ctx, p, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, memory) }, func(ctx context.Context) error { return f.execution.Check(ctx) }, io.Discard); err != nil {
		t.Fatal("actual accepted target source", err)
	}
	accepted, j, err := acceptedCreateStage(f.store, f.execution, "kubernetes/"+stage)
	if err != nil {
		t.Fatal("actual accepted target evidence", err)
	}
	state.Objects = memory.objects
	f.writeState(t, state)
	r := &createServiceSessionRecovery{ManagedCorePartial: &createManagedCorePartialRecovery{Apps: accepted, AppsJournal: j}, OpenBaoIntermediate: &ica}
	late := &createManagedLateSessionRecovery{AcceptedCore: accepted, AcceptedCoreJournal: j}
	return f, r, late, root, prior
}

func TestCRLExecutionContractAcceptedTargetLoadersBindNativeOwners(t *testing.T) {
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		t.Run(domain, func(t *testing.T) {
			f, r, late, root, prior := crlAcceptedTargetExecutionFixture(t, domain)
			load := func(r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) ([]createRootCRLStateTarget, error) {
				var c, cm deploymentplan.Resource
				var targets []createRootCRLStateTarget
				var pod, uid string
				var err error
				if domain == "mqtt" {
					c, cm, targets, pod, uid, err = loadMQTTRootCRLTargets(t.Context(), f.cfg, f.execution, f.backend, r, late, root, prior)
				} else {
					c, cm, targets, pod, uid, err = loadOpenBaoTLSRootCRLTargets(t.Context(), f.cfg, f.execution, f.backend, r, late, root, prior)
				}
				if err == nil && (c.UID == "" || cm.UID == "" || pod != "pki-controller-0" || uid == "" || len(targets) != 2) {
					t.Fatal("missing bound original target proofs")
				}
				return targets, err
			}
			targets, err := load(r, late)
			if err != nil {
				t.Fatal("actual accepted consumer source rejected", err)
			}
			for _, target := range targets {
				if len(target.StateSHA) != len(target.Paths) || target.PodUID == "" || target.ClaimUID == "" {
					t.Fatal("missing native cache/owner pins")
				}
			}
			original := f.state(t)
			originalBytes := mustProviderRecoveryJSON(original)
			sourceSHA := deploymentplan.Digest(f.execution.Journal)
			for _, mode := range []string{"missing-source", "missing-owner", "foreign-manifest-mount", "unknown-claim", "foreign-cache", "duplicate-Pod", "deleted-Pod", "unbound-Pod"} {
				t.Run(mode, func(t *testing.T) {
					var state crlExecutorBoundary
					if err := json.Unmarshal(originalBytes, &state); err != nil {
						t.Fatal(err)
					}
					var recovery createServiceSessionRecovery
					var current createManagedLateSessionRecovery
					if json.Unmarshal(mustProviderRecoveryJSON(r), &recovery) != nil || json.Unmarshal(mustProviderRecoveryJSON(late), &current) != nil {
						t.Fatal("clone isolated accepted source")
					}
					target := targets[0]
					if mode == "missing-source" {
						recovery.ManagedCorePartial = nil
						recovery.OpenBaoIntermediate = nil
					}
					if mode == "missing-owner" {
						delete(recovery.ManagedCorePartial.AppsJournal.Owners, target.ID)
						delete(current.AcceptedCoreJournal.Owners, target.ID)
					}
					if mode == "foreign-manifest-mount" {
						plans := []*deploymentplan.Plan{recovery.ManagedCorePartial.Apps, current.AcceptedCore}
						for _, p := range plans {
							for _, resource := range p.Resources {
								if resource.ID == target.ID {
									v := "mqtt-pki-crls"
									if domain == "openbao_tls" {
										v = "openbao-create-trust"
									}
									deploymentplan.Map(managedNamedField(environmentPKIPod(resource.Desired)["volumes"], v)["configMap"])["name"] = "foreign"
								}
							}
						}
					}
					if mode == "unknown-claim" {
						deploymentplan.Map(state.Objects[target.ClaimID]["metadata"])["uid"] = ""
					}
					if mode == "foreign-cache" {
						state.CacheFingerprints[target.PodName+":"+target.Paths[0]] = deploymentplan.Digest("foreign")
					}
					podID := "Pod/" + f.execution.Journal.Stack + "-video-cloud/" + target.PodName
					if mode == "duplicate-Pod" {
						pod := deploymentplan.Clone(state.Objects[podID])
						deploymentplan.Map(pod["metadata"])["name"] = "foreign-extra"
						deploymentplan.Map(pod["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
						state.Objects[deploymentplan.ID(pod)] = pod
					}
					if mode == "deleted-Pod" {
						deploymentplan.Map(state.Objects[podID]["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
					}
					if mode == "unbound-Pod" {
						deploymentplan.Map(state.Objects[podID]["metadata"])["ownerReferences"] = []any{}
					}
					f.writeState(t, state)
					before := deploymentplan.Digest(state)
					if _, err := load(&recovery, &current); err == nil {
						t.Fatal("foreign native target accepted", mode)
					}
					if deploymentplan.Digest(f.state(t)) != before || deploymentplan.Digest(f.execution.Journal) != sourceSHA {
						t.Fatal("target observation changed original source")
					}
				})
			}
		})
	}
}
