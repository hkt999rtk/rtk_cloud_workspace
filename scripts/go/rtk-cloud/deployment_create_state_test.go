package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createStateFixture(t *testing.T, environment string) (deploymentConfig, secretStore, string, *environmentCreateArtifact) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600"}}
	operatorSHA := deploymentplan.Digest("private-selected-operator-fixture")
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedAt := time.Now().UTC().Add(-time.Second)
	release := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], WorkspaceRevision: strings.Repeat("a", 40), Images: map[string]environmentReleaseImage{}}
	proofCtx := newEnvironmentReleaseProofCapture(context.Background())
	for index, role := range []string{"LKE_VIDEO_CLOUD_IMAGE", "LKE_ACCOUNT_MANAGER_IMAGE"} {
		repository, name := "hkt999rtk/rtk_video_cloud", "video-cloud"
		if role == "LKE_ACCOUNT_MANAGER_IMAGE" {
			repository, name = "hkt999rtk/rtk_account_manager", "account-manager"
		}
		revision := strings.Repeat(string(rune('b'+index)), 40)
		publication, err := json.Marshal(map[string]string{"schema": "rtk-service-image/v1", "repository": repository, "source_commit": revision, "run_id": "70", "run_attempt": "2", "image": "ghcr.io/" + repository + "/" + name, "digest": "sha256:" + strings.Repeat(string(rune('d'+index)), 64)})
		if err != nil {
			t.Fatal(err)
		}
		image, err := selectEnvironmentPublishedImage(publication, repository, 70, 2, revision, "ghcr.io/"+repository+"/"+name, false)
		if err != nil {
			t.Fatal(err)
		}
		release.Images[role] = image
		captureEnvironmentPublicationProof(proofCtx, repository, 70, 2, revision, 71, "image-manifest", false, []byte(`{"id":70,"run_attempt":2}`), []byte(`{"artifact_id":71}`), []byte("isolated synthetic create publication archive"), publication)
	}
	release.Images["LKE_POSTGRES_IMAGE"] = environmentReleaseImage{Reference: "docker.io/library/postgres@sha256:" + strings.Repeat("f", 64)}
	proofs := snapshotVerifiedReleaseProofs(proofCtx, release)
	for i := range proofs {
		proofs[i].ObservedAt = qualifiedAt.Add(-time.Second)
	}
	plan, _ := json.Marshal(environmentCreatePlan{Schema: "rtk-cloud.create-plan/v1", Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: operatorSHA, Release: release, Provider: environmentCreateProviderPlan{Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"]}})
	sha, err := environmentCreatePlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	qualification := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range environmentCreateRequiredGates {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest("verified-native-stage-proof:" + id)})
	}
	artifact, err := publishEnvironmentCreateArtifact(context.Background(), cfg, generation, plan, operatorSHA, qualification, proofs, qualifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, store, operatorSHA, artifact
}

func TestCreateArtifactFreezesCanonicalPlanAndAdmissionEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
			loaded, err := loadEnvironmentCreateArtifact(cfg, operatorSHA, artifact.PlanSHA256, time.Now().UTC())
			if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(artifact) || loaded.Directory != artifact.Directory {
				t.Fatal("complete create qualification did not preserve its original target", err)
			}
			var shared, create environmentPreflightActive
			shared, err = readEnvironmentPreflightActive(store)
			if err != nil || readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment", "create", "preflight", "active.json"), &create) != nil || deploymentplan.Digest(shared) != deploymentplan.Digest(create) || shared.Decision != "GO" || shared.ProofSHA256 != deploymentplan.Digest(artifact) {
				t.Fatal("create has an independent or incomplete admission pointer", err)
			}
			if !strings.HasPrefix(artifact.Directory, filepath.Join(store.Root, "deployment", "create", "preflight", artifact.PlanSHA256)+string(filepath.Separator)) {
				t.Fatal("create qualification escaped selected private environment state")
			}
			for _, path := range []string{artifact.Directory, filepath.Dir(artifact.Directory), filepath.Join(store.Root, "deployment", "create", "preflight")} {
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("create artifact directory permissions", path, err)
				}
			}
			info, err := os.Lstat(filepath.Join(artifact.Directory, "artifact.json"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("create artifact permissions", err)
			}
			if _, err := loadEnvironmentCreateArtifact(cfg, operatorSHA, artifact.PlanSHA256, artifact.ValidUntil); err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "valid_until=") {
				t.Fatal("create evidence expiry did not preserve clear times", err)
			}
			if _, err := loadEnvironmentCreateArtifact(cfg, operatorSHA, artifact.PlanSHA256, artifact.QualifiedAt.Add(-time.Nanosecond)); err == nil || !strings.Contains(err.Error(), "future-dated") {
				t.Fatal("future create qualification became executable", err)
			}
		})
	}
}

func TestCreateArtifactRequiresExactPublicationProofForEveryRTKReleaseRole(t *testing.T) {
	for _, mode := range []string{"complete", "missing-all", "missing-one", "duplicate", "foreign-role", "foreign-image", "foreign-source", "foreign-repository", "wrong-run-attempt", "changed-manifest", "future-publication", "missing-release", "empty-release-images", "mutable-image"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			before := createBootstrapStateSnapshot(t, store.Root)
			var plan environmentCreatePlan
			if err := decodeEnvironmentJSON(artifact.Plan, &plan); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-all":
				artifact.ReleaseProofs = nil
			case "missing-one":
				artifact.ReleaseProofs = artifact.ReleaseProofs[:1]
			case "duplicate":
				artifact.ReleaseProofs = append(artifact.ReleaseProofs, artifact.ReleaseProofs[0])
			case "foreign-role":
				artifact.ReleaseProofs[0].ImageKey = "LKE_PRIVATE_UNSELECTED_IMAGE"
			case "foreign-image":
				artifact.ReleaseProofs[0].Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/unselected@sha256:" + strings.Repeat("9", 64)
			case "foreign-source":
				artifact.ReleaseProofs[0].SourceRevision = strings.Repeat("9", 40)
			case "foreign-repository":
				artifact.ReleaseProofs[0].Repository = "foreign/unselected"
			case "wrong-run-attempt":
				artifact.ReleaseProofs[0].RunAttempt++
			case "changed-manifest":
				artifact.ReleaseProofs[0].Manifest = []byte(`{"private-unverified-publication":"payload"}`)
				artifact.ReleaseProofs[0].ManifestSHA256 = environmentPreflightBytesSHA256(artifact.ReleaseProofs[0].Manifest)
			case "future-publication":
				artifact.ReleaseProofs[0].ObservedAt = artifact.QualifiedAt.Add(time.Second)
			case "missing-release":
				plan.Release = environmentRelease{}
			case "empty-release-images":
				plan.Release.Images = nil
			case "mutable-image":
				plan.Release.Images["LKE_POSTGRES_IMAGE"] = environmentReleaseImage{Reference: "docker.io/library/postgres:latest"}
			}
			// Keep the plan and qualification hashes consistent so rejection must
			// come from real release/proof coverage rather than an earlier hash fence.
			artifact.Plan, _ = json.Marshal(plan)
			sha, err := environmentCreatePlanSHA256(artifact.Plan)
			if err != nil {
				t.Fatal(err)
			}
			artifact.PlanSHA256, artifact.Qualification.PlanSHA256 = sha, sha
			err = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
			if mode == "complete" {
				if err != nil || len(artifact.ReleaseProofs) != 2 {
					t.Fatal("exact formal publications or proof-free immutable provider image were rejected", err)
				}
			} else {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.create.preflight.evidence" || !strings.Contains(blocker.Cause, "complete exact-source publication evidence") || strings.Contains(err.Error(), "private-") {
					t.Fatal("incomplete or foreign create publication evidence passed or leaked payload", err)
				}
			}
			if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("release-proof validation rewrote original qualification history")
			}
		})
	}
}

func TestCreateArtifactRejectsDriftIncompleteGatesAndCrossOperationGeneration(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"wrong-plan", "wrong-operator", "configuration-drift", "checker-drift", "failed-gate", "missing-required-gates", "missing-one-gate", "duplicate-gate", "missing-gate-evidence", "changed-plan", "invalidated-by-upgrade", "wrong-create-pointer", "artifact-permissions", "future-expiry-extension"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
				requested := artifact.PlanSHA256
				var directErr error
				switch mode {
				case "wrong-plan":
					requested = deploymentplan.Digest("another-original-plan")
				case "wrong-operator":
					operatorSHA = deploymentplan.Digest("private-other-operator")
				case "configuration-drift":
					cfg.Values["DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS"] = "601"
				case "checker-drift":
					artifact.CheckerSHA256 = deploymentplan.Digest("other-checker")
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "failed-gate":
					artifact.Qualification.Gates[0].Status = "FAIL"
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "missing-required-gates":
					artifact.Qualification.Gates = nil
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "missing-one-gate":
					artifact.Qualification.Gates = artifact.Qualification.Gates[1:]
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "duplicate-gate":
					artifact.Qualification.Gates = append(artifact.Qualification.Gates, artifact.Qualification.Gates[0])
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "missing-gate-evidence":
					artifact.Qualification.Gates[0].EvidenceSHA256 = ""
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "changed-plan":
					artifact.Plan = json.RawMessage(`{"operation":"upgrade","private-changed":"payload"}`)
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				case "invalidated-by-upgrade":
					if _, err := invalidateEnvironmentPreflightProofWithToken(cfg); err != nil {
						t.Fatal(err)
					}
				case "wrong-create-pointer":
					pointer, err := readEnvironmentPreflightActive(store)
					if err != nil {
						t.Fatal(err)
					}
					pointer.Generation = deploymentplan.Digest("another-generation")
					if err := writeEnvironmentCreateActive(store, pointer); err != nil {
						t.Fatal(err)
					}
				case "artifact-permissions":
					if err := os.Chmod(filepath.Join(artifact.Directory, "artifact.json"), 0644); err != nil {
						t.Fatal(err)
					}
				case "future-expiry-extension":
					artifact.ValidUntil = artifact.ValidUntil.Add(time.Hour)
					directErr = validateEnvironmentCreateArtifact(*artifact, cfg, operatorSHA, time.Now().UTC())
				}
				if strings.Contains(mode, "gate") || mode == "checker-drift" || mode == "changed-plan" || mode == "future-expiry-extension" {
					if directErr == nil || strings.Contains(directErr.Error(), "private-") {
						t.Fatal("invalid create evidence passed validation or exposed payload", directErr)
					}
					return
				}
				if _, err := loadEnvironmentCreateArtifact(cfg, operatorSHA, requested, time.Now().UTC()); err == nil || strings.Contains(err.Error(), "private-") {
					t.Fatal("stale/drifting create authorization remained executable", err)
				}
			})
		}
	}
}

func TestCreatePublicationCannotOverwriteNewerUpgradeOrCreateGeneration(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
			newGeneration, err := invalidateEnvironmentPreflightProofWithToken(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := publishEnvironmentCreateArtifact(context.Background(), cfg, artifact.Generation, artifact.Plan, operatorSHA, artifact.Qualification, artifact.ReleaseProofs, time.Now().UTC()); err == nil {
				t.Fatal("older create qualification overwrote a newer operation")
			}
			active, err := readEnvironmentPreflightActive(store)
			if err != nil || active.Decision != "NO-GO" || active.Generation != newGeneration {
				t.Fatal("failed stale publication changed current operation authority", err)
			}
		})
	}
}

func TestCreateJournalDurablyRecordsIntentIDAndCompletionBeyondEvidenceExpiry(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
			admitted := time.Now().UTC()
			execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, admitted)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Close()
			intent, receipt := json.RawMessage(`{"kind":"lke","label":"selected-original"}`), json.RawMessage(`{"native_id":"123","label":"selected-original"}`)
			later := artifact.ValidUntil.Add(time.Minute)
			if err := execution.BeginEntry("provider.lke", "provider", intent, later); err != nil {
				t.Fatal("admitted create was truncated by the qualification TTL", err)
			}
			var journal environmentCreateJournal
			if err := readEnvironmentPreflightJSON(execution.JournalPath, &journal); err != nil || len(journal.Entries) != 1 || journal.Entries[0].State != "pending" || len(journal.Entries[0].Receipt) != 0 {
				t.Fatal("mutation intent was not durably committed before allocation", err)
			}
			if err := execution.RecordReceipt("provider.lke", receipt); err != nil {
				t.Fatal(err)
			}
			if err := readEnvironmentPreflightJSON(execution.JournalPath, &journal); err != nil || journal.Entries[0].State != "allocated" || !bytes.Contains(journal.Entries[0].Receipt, []byte(`"native_id":"123"`)) {
				t.Fatal("native ID was not committed before polling", err)
			}
			lease := deploymentplan.LeaseOwner{ResourceID: "Lease/" + artifact.Stack + "-video-cloud/rtk-deployment-execution", UID: "native-original-uid", Holder: "original-child-plan:nonce"}
			if err := execution.RecordKubernetesLease(lease); err != nil {
				t.Fatal(err)
			}
			if err := execution.CompleteEntry("provider.lke", later.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := execution.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := execution.CompleteAndRelease(later.Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(store.Root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed create retained active ownership", err)
			}
			if _, err := os.Lstat(filepath.Join(execution.Directory, "owner", "owner.json")); err != nil {
				t.Fatal("original owner archive was not retained", err)
			}
			if err := readEnvironmentPreflightJSON(execution.JournalPath, &journal); err != nil || journal.CompletedAt.IsZero() || len(journal.KubernetesLeases) != 1 || journal.KubernetesLeases[0] != lease {
				t.Fatal("completion erased original ownership/native receipt history", err)
			}
			if _, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, admitted); err == nil {
				t.Fatal("same create plan overwrote prior execution history")
			}
		})
	}
}

func TestCreateJournalRetainsUnknownOutcomesAndRejectsOwnerReplacement(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"pending-outcome", "allocated-outcome", "owner-replaced", "journal-replaced", "process-lock-replaced", "closed-owner", "foreign-kube-lease", "duplicate-intent", "receipt-without-intent", "changed-receipt", "cancelled-check"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
				execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				defer execution.Close()
				intent, receipt := json.RawMessage(`{"resource":"selected-original"}`), json.RawMessage(`{"native_id":"123"}`)
				if err := execution.BeginEntry("original", "provider", intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				fence := filepath.Join(store.Root, "deployment", ".execution-active")
				var failure error
				switch mode {
				case "pending-outcome":
					failure = execution.CompleteAndRelease(time.Now().UTC())
				case "allocated-outcome":
					if err := execution.RecordReceipt("original", receipt); err != nil {
						t.Fatal(err)
					}
					failure = execution.CompleteAndRelease(time.Now().UTC())
				case "owner-replaced":
					if err := os.Rename(fence, filepath.Join(execution.Directory, "original-owner")); err != nil || os.Mkdir(fence, 0700) != nil {
						t.Fatal("could not prepare replacement owner fixture", err)
					}
					failure = execution.Check(context.Background())
				case "journal-replaced":
					if err := os.WriteFile(execution.JournalPath, []byte(`{"private-replacement":"unknown-outcome"}`), 0600); err != nil {
						t.Fatal(err)
					}
					failure = execution.Check(context.Background())
				case "process-lock-replaced":
					if err := os.Rename(filepath.Join(fence, "running.lock"), filepath.Join(fence, "original-running.lock")); err != nil || os.WriteFile(filepath.Join(fence, "running.lock"), []byte("private-other-process"), 0600) != nil {
						t.Fatal("could not prepare replaced process lock", err)
					}
					failure = execution.Check(context.Background())
				case "closed-owner":
					if err := execution.Close(); err != nil {
						t.Fatal(err)
					}
					failure = execution.Check(context.Background())
				case "foreign-kube-lease":
					failure = execution.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/foreign/rtk-deployment-execution", UID: "foreign-uid", Holder: "foreign-holder"})
				case "duplicate-intent":
					failure = execution.BeginEntry("original", "provider", intent, time.Now().UTC())
				case "receipt-without-intent":
					failure = execution.RecordReceipt("foreign", receipt)
				case "changed-receipt":
					if err := execution.RecordReceipt("original", receipt); err != nil {
						t.Fatal(err)
					}
					failure = execution.RecordReceipt("original", json.RawMessage(`{"native_id":"456"}`))
				case "cancelled-check":
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					failure = execution.Check(ctx)
				}
				if failure == nil || strings.Contains(failure.Error(), "private-") {
					t.Fatal("uncertain/replaced create owner was accepted or private payload leaked", failure)
				}
				if err := execution.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(fence); err != nil {
					t.Fatal("uncertainty or interruption discarded the active owner", err)
				}
				if _, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC()); err == nil {
					t.Fatal("unknown outcome was automatically replayed or owner was stolen")
				}
			})
		}
	}
}
