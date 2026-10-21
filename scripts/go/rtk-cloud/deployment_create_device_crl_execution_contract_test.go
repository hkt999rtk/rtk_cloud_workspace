package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLExecutionContractDeviceProviderPreservesOriginalKey(t *testing.T) {
	for _, mode := range []string{"read", "rotate", "response-lost", "key-replacement-observed", "foreign-certificate", "missing-token", "issuer-unavailable", "CRL-unavailable", "wrong-domain"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			plan, key, now := deviceRootCRLFixture(t)
			union, err := deviceRootCRLUnion(plan.Root, plan.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			rotated := signedRootCRLFixture(t, plan.Root, key, createRootCRLRequest{IssuerID: plan.Root.ID, Fingerprint: plan.Root.RootFingerprint, Number: "4", ThisUpdate: now, NextUpdate: now.Add(24 * time.Hour), Entries: union})
			observation := deploymentplan.Object{"data": deploymentplan.Object{"certificate": plan.Root.Certificate, "ca_chain": []string{plan.Root.Certificate}, "issuer_id": "99999999-9999-4999-8999-999999999999", "key_id": "88888888-8888-4888-8888-888888888888", "usage": "issuing-certificates,crl-signing"}}
			originalProof, err := deviceTrustSignerObservation(mustProviderRecoveryJSON(observation), plan.Root)
			if err != nil {
				t.Fatal(err)
			}
			token := []byte("isolated-original-provider-token")
			if err := f.store.write("openbao/root-token", token, false); err != nil {
				t.Fatal(err)
			}
			state := f.state(t)
			state.ProviderObservation = mustProviderRecoveryJSON(observation)
			state.ProviderTokenSHA = environmentPreflightBytesSHA256(token)
			state.ProviderCRL = plan.Snapshot.Prior.PEM
			state.ProviderRotatedCRL = rotated.PEM
			switch mode {
			case "key-replacement-observed":
				deploymentplan.Map(observation["data"])["key_id"] = "77777777-7777-4777-8777-777777777777"
				state.ProviderObservation = mustProviderRecoveryJSON(observation)
			case "foreign-certificate":
				foreign, _, _, _ := rootCRLFixture(t)
				deploymentplan.Map(observation["data"])["certificate"] = foreign.Certificate
				state.ProviderObservation = mustProviderRecoveryJSON(observation)
			case "missing-token":
				if err := os.Remove(filepath.Join(f.store.Root, "openbao/root-token")); err != nil {
					t.Fatal(err)
				}
			case "issuer-unavailable":
				state.ProviderFailure = "issuer"
			case "CRL-unavailable":
				state.ProviderFailure = "current"
			case "wrong-domain":
				plan.Root.Domain = "app"
			case "response-lost":
				state.ProviderRotationResponseLost = true
			}
			f.writeState(t, state)
			before := deploymentplan.Digest(f.execution.Journal)
			proof, raw, err := readDeviceRootCRLProvider(t.Context(), f.cfg, f.store, f.execution, f.backend, plan.Root, mode == "rotate" || mode == "response-lost")
			if mode == "response-lost" {
				if err == nil || proof != originalProof || len(raw) != 0 {
					t.Fatal("uncertain rotation response accepted", err)
				}
				proof, raw, err = readDeviceRootCRLProvider(t.Context(), f.cfg, f.store, f.execution, f.backend, plan.Root, false)
			}
			switch mode {
			case "read", "rotate", "response-lost", "key-replacement-observed":
				if err != nil {
					t.Fatal("provider read unavailable", err)
				}
				if mode == "key-replacement-observed" {
					if proof == originalProof {
						t.Fatal("provider key replacement concealed")
					}
				} else if proof != originalProof {
					t.Fatal("original public key proof changed")
				}
				record, err := deviceRootCRLProviderRecord(plan.Root, raw)
				if err != nil {
					t.Fatal(err)
				}
				want := plan.Snapshot.Prior
				if mode == "rotate" || mode == "response-lost" {
					want = rotated
					if _, err := validateDeviceRootCRLSuccessor(plan, raw, now, now.Add(time.Second)); err != nil {
						t.Fatal("rotated CRL lost signed revocation union", err)
					}
				}
				if deploymentplan.Digest(record) != deploymentplan.Digest(want) {
					t.Fatal("provider returned another signed publication")
				}
			default:
				if err == nil {
					t.Fatal("unproven provider source accepted")
				}
			}
			after := f.state(t)
			rotations := 0
			for _, call := range after.ProviderCalls {
				if call == "rotate" {
					rotations++
				}
			}
			if mode == "rotate" || mode == "response-lost" {
				if rotations != 1 {
					t.Fatal("rotation repeated or omitted", after.ProviderCalls)
				}
			} else if rotations != 0 {
				t.Fatal("read-only or rejected provider path rotated", after.ProviderCalls)
			}
			if deploymentplan.Digest(f.execution.Journal) != before || after.SecretWrites != 0 || after.LeaseCreates != 0 {
				t.Fatal("provider observation altered deployment owner")
			}
			if mode != "missing-token" {
				current, err := os.ReadFile(filepath.Join(f.store.Root, "openbao/root-token"))
				if err != nil || string(current) != string(token) {
					t.Fatal("original authentication changed", err)
				}
			}
		})
	}
}

func TestCRLExecutionContractDeviceCacheBindsCurrentSignedPublication(t *testing.T) {
	for _, mode := range []string{"prior-three", "current-successor", "foreign-owner", "foreign-cache", "foreign-key", "bad-signature", "wrong-path-count"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			p, key, now := deviceRootCRLFixture(t)
			entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			current := signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{IssuerID: p.Root.ID, Fingerprint: p.Root.RootFingerprint, Number: "4", ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(24 * time.Hour), Entries: entries})
			record := current
			if mode == "prior-three" {
				record = p.Snapshot.Prior
			}
			target := f.artifact.Plan.Targets[0]
			target.Paths = []string{"/run/pki-state/device-root-crl.json"}
			state := f.state(t)
			cache := target.PodName + ":" + target.Paths[0]
			state.Caches[cache] = record
			state.Fingerprint = p.Root.RootFingerprint
			switch mode {
			case "foreign-owner":
				deploymentplan.Map(state.Objects[target.ClaimID]["metadata"])["uid"] = "foreign"
			case "foreign-cache":
				state.Caches[cache] = p.Snapshot.Prior
			case "foreign-key":
				state.Fingerprint = deploymentplan.Digest("foreign")
			case "bad-signature":
				record.PEM = "invalid"
				state.Caches[cache] = record
			case "wrong-path-count":
				target.Paths = append(target.Paths, "/unreviewed")
			}
			f.writeState(t, state)
			raw, err := readDeviceRootCRLCache(t.Context(), f.cfg, f.backend, p, target, record)
			if slices.Contains([]string{"prior-three", "current-successor"}, mode) {
				if err != nil || !json.Valid(raw) {
					t.Fatal("genuine signed cache rejected", err)
				}
			} else if err == nil {
				t.Fatal("unqualified cache accepted")
			}
			after := f.state(t)
			if after.Installs != 0 || after.Imports != 0 || after.SecretWrites != 0 {
				t.Fatal("cache read mutated installed identity")
			}
		})
	}
}

func TestCRLExecutionContractDeviceExecutorKeepsFixedAdmissionBoundary(t *testing.T) {
	for _, mode := range []string{"expired-qualification", "recorded-deadline", "changed-journal", "registry-changed", "duplicate-completion-authority", "null-completion-authority"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			p, _, now := deviceRootCRLFixture(t)
			p.Parent = f.execution.Journal.PlanSHA256
			p.OwnerSHA = deploymentplan.Digest(f.execution.owner)
			p.SourceSHA = deploymentplan.Digest(f.execution.Journal)
			p.Configuration = f.cfg
			p.ConfigurationSHA = deploymentplan.Digest(f.cfg)
			a := deviceRootCRLArtifactFixture(p, now)
			var completions []*createDeviceRootCRLCompletionArtifact
			expected := ""
			switch mode {
			case "expired-qualification":
				a.QualifiedAt = now.Add(-20 * time.Minute)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
				expected = "fresh-sixGO"
			case "recorded-deadline", "changed-journal":
				started := now.Add(-time.Minute)
				if mode == "recorded-deadline" {
					a.QualifiedAt = now.Add(-40 * time.Minute)
					a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
					started = now.Add(-35 * time.Minute)
				}
				j := createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: deploymentplan.Digest(p), SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "admitted", StartedAt: started, Deadline: started.Add(30 * time.Minute), Lease: deploymentplan.LeaseOwner{ResourceID: "Lease/" + f.execution.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "55555555-5555-4555-8555-555555555555", Holder: "original-held-repair"}, LeaseReleased: true, Evidence: map[string]string{}}
				if err := saveDeviceRootCRLJournal(f.store, a, j); err != nil {
					t.Fatal("valid historical admission", err)
				}
				expected = "original-repair-deadline"
				if mode == "changed-journal" {
					j.SourceSHA = deploymentplan.Digest("foreign")
					if err := f.store.write(filepath.Join(deviceRootCRLBase(j.Review), "journal.json"), mustProviderRecoveryJSON(j), true); err != nil {
						t.Fatal(err)
					}
					expected = "existing-standalone-journal"
				}
			case "registry-changed":
				state := f.state(t)
				native := p.Snapshot
				native.Bindings = append(native.Bindings, createRootCRLRevocation{Serial: "ff", RevokedAt: now.Add(-time.Minute)})
				state.Snapshots = map[string]createRootCRLNativeSnapshot{p.Root.ID: native}
				f.writeState(t, state)
				expected = "reviewed-registry-and-revocation-union"
			case "duplicate-completion-authority":
				completions = []*createDeviceRootCRLCompletionArtifact{{}, {}}
				expected = "one-explicit-completion-authority"
			case "null-completion-authority":
				completions = []*createDeviceRootCRLCompletionArtifact{nil}
				expected = "one-explicit-completion-authority"
			}
			path := filepath.Join(f.store.Root, deviceRootCRLBase(deploymentplan.Digest(p)), "journal.json")
			before, readErr := os.ReadFile(path)
			err := executeDeviceRootCRL(t.Context(), f.cfg, f.store, f.execution, f.backend, nil, a, io.Discard, completions...)
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatal("admission boundary changed", expected, err)
			}
			after, afterErr := os.ReadFile(path)
			if !bytes.Equal(before, after) || os.IsNotExist(readErr) != os.IsNotExist(afterErr) {
				t.Fatal("failed resume changed original journal or deadline")
			}
			state := f.state(t)
			if state.LeaseCreates != 0 || state.SecretWrites != 0 || len(state.ProviderCalls) != 0 {
				t.Fatal("invalid or expired admission mutated native state")
			}
		})
	}
}
