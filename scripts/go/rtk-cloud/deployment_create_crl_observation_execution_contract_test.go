package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The external boundary exposes signed registry records and original Kubernetes
// objects. Consumer observation still executes the production lineage, owner,
// cache-signature and native acknowledgement validators together.
func TestCRLExecutionContractNativeConsumerObservation(t *testing.T) {
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		for _, mode := range []string{"installed", "missing-ACK", "duplicate-import", "stale-ACK", "wrong-ACK-owner", "old-cache", "foreign-cache-key", "new-revocation", "replaced-Pod", "changed-ICA-cache"} {
			if domain == "mqtt" && mode == "changed-ICA-cache" {
				continue
			}
			t.Run(domain+"/"+mode, func(t *testing.T) {
				f := newCRLExecutorFixture(t)
				chain := newManagedLateBootstrapChainFixture(t)
				root := chain.ceremony.Root
				root.Domain = domain
				if domain == "openbao_tls" {
					root.ID = "734487b8-abcf-40be-a813-8b52c64f7954"
				}
				now := chain.now.Add(-time.Second)
				prior := signedRootCRLFixture(t, root, chain.rootKey, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "1", ThisUpdate: now.Add(-48 * time.Hour), NextUpdate: now.Add(-24 * time.Hour)})
				snapshot := createRootCRLNativeSnapshot{Prior: prior}
				request, err := buildCreateRootCRLRequestForDomain(root, snapshot, domain, now)
				if err != nil {
					t.Fatal(err)
				}
				signed := signedRootCRLFixture(t, root, chain.rootKey, request)
				current := snapshot
				current.Prior = signed
				ica := chain.ceremony.Intermediate
				ica.ID = openBaoTLSOriginalIntermediateID
				ica.Environment = "dev"
				ica.Domain = domain
				ica.Kind = "intermediate"
				ica.Status = "active"
				ica.RootFingerprint = root.RootFingerprint
				ica.ChainPEM = ica.Certificate + root.Certificate
				ica.Document = deploymentplan.Object{"parent_issuer_id": root.ID, "signer_provider": "openbao"}
				icaRecord := signedRootCRLFixture(t, ica, chain.icaKey, createRootCRLRequest{IssuerID: ica.ID, Fingerprint: environmentPreflightBytesSHA256(chain.icaCertificate.Raw), Number: "3", ThisUpdate: now.Add(-time.Hour), NextUpdate: now.Add(48 * time.Hour)})
				names := []string{"video-cloud-api", "video-cloud-logingester"}
				if domain == "openbao_tls" {
					names = []string{"certissuer", "pki-controller"}
				}
				mqtt := createMQTTRootCRLArtifact{Plan: createMQTTRootCRLPlan{Parent: f.execution.Journal.PlanSHA256, Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed}}
				bao := createOpenBaoTLSRootCRLArtifact{Plan: createOpenBaoTLSRootCRLPlan{Parent: f.execution.Journal.PlanSHA256, Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed, Intermediate: ica, IntermediateCRL: icaRecord}}
				state := f.state(t)
				state.Snapshots = map[string]createRootCRLNativeSnapshot{root.ID: current, ica.ID: {Prior: icaRecord}}
				state.CacheFingerprints = map[string]string{}
				ns := f.execution.Journal.Stack + "-video-cloud"
				targets := []createRootCRLStateTarget{}
				for _, name := range names {
					object := func(kind, n string) deploymentplan.Object {
						return deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": n, "namespace": ns, "uid": "original-" + kind + "-" + n, "resourceVersion": "1"}}
					}
					body := object("Deployment", name)
					body["spec"] = deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"securityContext": deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001)}}}}
					pod := object("Pod", name+"-original")
					claim := object("PersistentVolumeClaim", name+"-trust")
					target := createRootCRLStateTarget{ID: deploymentplan.ID(body), UID: deploymentplan.String(deploymentplan.Map(body["metadata"])["uid"]), Desired: deploymentplan.Projection(body), PodName: name + "-original", PodUID: deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), ClaimID: deploymentplan.ID(claim), ClaimUID: deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), Container: "app", Paths: []string{"/var/lib/mqtt-trust/root-crl.json"}, StateSHA: map[string]string{}}
					if domain == "openbao_tls" {
						target.Container = name
						target.Paths = []string{"/var/lib/pki-service/private/crl/openbao-root.json", "/var/lib/pki-service/private/crl/openbao-root-" + ica.ID + ".json"}
					}
					for _, o := range []deploymentplan.Object{body, pod, claim} {
						state.Objects[deploymentplan.ID(o)] = o
					}
					state.Caches[target.PodName+":"+target.Paths[0]] = signed
					state.CacheFingerprints[target.PodName+":"+target.Paths[0]] = root.RootFingerprint
					if domain == "openbao_tls" {
						state.Caches[target.PodName+":"+target.Paths[1]] = icaRecord
						state.CacheFingerprints[target.PodName+":"+target.Paths[1]] = environmentPreflightBytesSHA256(chain.icaCertificate.Raw)
					}
					targets = append(targets, target)
				}
				acks := []deploymentplan.Object{}
				for _, name := range names {
					acks = append(acks, deploymentplan.Object{"issuer_id": root.ID, "digest": signed.Digest, "consumer_id": name, "acknowledged_at": now})
				}
				publication := deploymentplan.Object{"imports": 1, "imported_at": now, "acks": acks}
				state.Publications = map[string]json.RawMessage{root.ID: mustProviderRecoveryJSON(publication)}
				f.writeState(t, state)
				if domain == "openbao_tls" {
					for i, target := range targets {
						raw, err := readOpenBaoTLSRootCRLIntermediateState(t.Context(), f.cfg, f.backend, target, ica, icaRecord)
						if err != nil {
							t.Fatal("fixture signed ICA3 cache", err)
						}
						targets[i].StateSHA[target.Paths[1]] = environmentPreflightBytesSHA256(raw)
					}
				}
				// Qualify the complete original before injecting a single fault, so a
				// fixture protocol mismatch cannot masquerade as a negative-test pass.
				if domain == "mqtt" {
					_, err = observeMQTTRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, mqtt, targets[0])
				} else {
					_, err = observeOpenBaoTLSRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, bao, targets[0])
				}
				if err != nil {
					t.Fatal("complete observation fixture", err)
				}
				switch mode {
				case "missing-ACK":
					publication["acks"] = acks[:1]
				case "duplicate-import":
					publication["imports"] = 2
				case "stale-ACK":
					acks[0]["acknowledged_at"] = now.Add(-time.Second)
				case "wrong-ACK-owner":
					acks[0]["consumer_id"] = "foreign-consumer"
				case "old-cache":
					state.Caches[targets[0].PodName+":"+targets[0].Paths[0]] = prior
				case "foreign-cache-key":
					state.CacheFingerprints[targets[0].PodName+":"+targets[0].Paths[0]] = deploymentplan.Digest("foreign-key")
				case "new-revocation":
					current.Bindings = []createRootCRLRevocation{{Serial: "99", RevokedAt: now.Add(-time.Minute)}}
					state.Snapshots[root.ID] = current
				case "replaced-Pod":
					deploymentplan.Map(state.Objects["Pod/"+ns+"/"+targets[0].PodName]["metadata"])["uid"] = "replacement"
				case "changed-ICA-cache":
					targets[0].StateSHA[targets[0].Paths[1]] = deploymentplan.Digest("foreign-original-cache")
				}
				state.Publications[root.ID] = mustProviderRecoveryJSON(publication)
				f.writeState(t, state)
				for i, target := range targets {
					var evidence map[string]string
					if domain == "mqtt" {
						evidence, err = observeMQTTRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, mqtt, target)
					} else {
						evidence, err = observeOpenBaoTLSRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, bao, target)
					}
					if mode != "installed" {
						if i == 0 && err == nil {
							t.Fatal("unproven installation accepted", evidence)
						}
						break
					}
					if err != nil || len(evidence) != 4 || evidence[target.ID+":owner"] != deploymentplan.Digest(target) {
						t.Fatal("genuine installation rejected", evidence, err)
					}
					for _, digest := range evidence {
						if !environmentPreflightDigest.MatchString(digest) {
							t.Fatal("missing observed evidence", evidence)
						}
					}
				}
				after := f.state(t)
				if after.Imports != 0 || after.Installs != 0 || after.SecretWrites != 0 || after.LeaseCreates != 0 {
					t.Fatal("read-only verification mutated state", after)
				}
			})
		}
	}
}
