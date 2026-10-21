package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func managedCoreObservationFixture() *createManagedCorePartialRecovery {
	session := "e0a93344-3c64-4a00-87d5-dc57a4e0d48b"
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped"}}
	j := deploymentplan.Journal{Owners: map[string]string{}, Failed: "Deployment/video-cloud-dev-video-cloud/pki-controller"}
	for index, id := range managedCorePartialResourceIDs(plan.Stack, session) {
		parts := strings.Split(id, "/")
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", index+1)
		body := deploymentplan.Object{"apiVersion": "v1", "kind": parts[0], "metadata": deploymentplan.Object{"name": parts[2], "namespace": parts[1]}}
		if index >= 13 {
			body["spec"] = deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": parts[2], "image": "formal-core", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": session}, deploymentplan.Object{"name": "PKI_FIXED_DEVICE_ROOT_TRUST", "value": "false"}, deploymentplan.Object{"name": "PKI_DEVICE_ROOT_ID", "value": "11111111-1111-4111-8111-111111111111"}, deploymentplan.Object{"name": "PKI_DEVICE_ROOT_SHA256", "value": strings.Repeat("a", 64)}, deploymentplan.Object{"name": "EMPTY", "value": ""}}}}}}}
		}
		baseline := deploymentplan.Clone(body)
		deploymentplan.Map(baseline["metadata"])["uid"] = uid
		if index >= 13 {
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(baseline)["containers"])[0])["image"] = "original-source"
		}
		phase, action := "configuration", "retain"
		if index >= 13 {
			phase = "workload"
		}
		if index == 4 || index == 13 || index == 14 {
			action = "update"
		}
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Phase: phase, Action: action, UID: uid, Desired: body, Baseline: baseline})
		if index < 14 {
			j.Owners[id] = uid
		}
		if index < 13 {
			j.Completed = append(j.Completed, id)
		}
	}
	j.PlanSHA256 = plan.SHA256()
	return &createManagedCorePartialRecovery{Action: managedCoreRestoreOnly, OriginalRepairReview: strings.Repeat("b", 64), ForwardReview: strings.Repeat("c", 64), SourceSHA: strings.Repeat("d", 64), ArtifactSHA: strings.Repeat("e", 64), Plan: plan, Journal: j}
}

func TestManagedCoreCurrentOwnerRealBoundary(t *testing.T) {
	for _, index := range []int{0, 4, 12, 13, 14, 15} {
		for _, mode := range []string{"original", "defaults", "replacement", "terminating", "drift", "old-written", "future-tail", "missing-owner", "fake-progress", "cleared-failure"} {
			t.Run(fmt.Sprintf("%d/%s", index, mode), func(t *testing.T) {
				core := managedCoreObservationFixture()
				resource := core.Plan.Resources[index]
				body, uid := resource.Desired, core.Journal.Owners[resource.ID]
				if index > 13 {
					body, uid = resource.Baseline, resource.UID
				}
				actual := deploymentplan.Clone(body)
				deploymentplan.Map(actual["metadata"])["uid"] = uid
				pass := true
				switch mode {
				case "defaults":
					if index == 13 {
						c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(actual)["containers"])[0])
						c["imagePullPolicy"] = "IfNotPresent"
						delete(deploymentplan.Map(certIssuerObjectList(c["env"])[4]), "value")
					}
				case "replacement":
					deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
					pass = false
				case "terminating":
					deploymentplan.Map(actual["metadata"])["deletionTimestamp"] = time.Now().Format(time.RFC3339)
					pass = false
				case "drift":
					if index >= 13 {
						deploymentplan.Map(certIssuerObjectList(environmentPKIPod(actual)["containers"])[0])["image"] = "foreign"
					} else {
						deploymentplan.Map(actual["metadata"])["name"] = "foreign"
					}
					pass = false
				case "old-written":
					if index == 13 {
						actual = deploymentplan.Clone(resource.Baseline)
						pass = false
					}
				case "future-tail":
					if index > 13 {
						actual = deploymentplan.Clone(resource.Desired)
						deploymentplan.Map(actual["metadata"])["uid"] = uid
						pass = false
					}
				case "missing-owner":
					if index < 14 {
						delete(core.Journal.Owners, resource.ID)
						pass = false
					}
				case "fake-progress":
					core.Journal.Completed = append(core.Journal.Completed, core.Plan.Resources[13].ID)
					pass = false
				case "cleared-failure":
					core.Journal.Failed = ""
					pass = false
				}
				before := deploymentplan.Digest(core)
				if (validateManagedCoreCurrentOwner(core, index, actual) == nil) != pass {
					t.Fatal("wrong current boundary", index, mode)
				}
				if before != deploymentplan.Digest(core) {
					t.Fatal("observer rewrote original history")
				}
			})
		}
	}
}

func TestManagedCoreCompletedSessionOriginalNineACKOnly(t *testing.T) {
	_, _, service, subjects, now := managedHistoricalSeedFixture(t)
	original := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("a", 64), Status: "active", Expires: now.Add(time.Minute), Subjects: subjects, Installed: slices.Clone(subjects)}
	saved := original
	saved.Installed = nil
	for _, mode := range []string{"active", "at-expiry", "active-expired", "expired", "future-expired", "sealed", "finalized", "session", "issuer", "root", "fingerprint", "expiry", "competing", "missing-ack", "duplicate-ack", "foreign-ack", "duplicate-subject", "subjects", "reordered-acks", "missing-source-expiry"} {
		t.Run(mode, func(t *testing.T) {
			state := original
			state.Subjects = slices.Clone(original.Subjects)
			state.Installed = slices.Clone(original.Installed)
			source := saved
			clock := now
			pass := true
			switch mode {
			case "at-expiry":
				clock = state.Expires
			case "active-expired":
				clock = state.Expires.Add(time.Minute)
			case "expired":
				state.Status = "expired"
				clock = state.Expires.Add(time.Minute)
			case "future-expired":
				state.Status = "expired"
				pass = false
			case "sealed", "finalized":
				state.Status = mode
				pass = false
			case "session":
				state.Session = "11111111-1111-4111-8111-111111111111"
				pass = false
			case "issuer":
				state.Issuer = "foreign"
				pass = false
			case "root":
				state.Root = strings.Repeat("b", 64)
				pass = false
			case "fingerprint":
				state.Fingerprint = strings.Repeat("b", 64)
				pass = false
			case "expiry":
				state.Expires = state.Expires.Add(time.Minute)
				pass = false
			case "competing":
				state.OtherActive = 1
				pass = false
			case "missing-ack":
				state.Installed = state.Installed[:8]
				pass = false
			case "duplicate-ack":
				state.Installed[0] = state.Installed[1]
				pass = false
			case "foreign-ack":
				state.Installed[0] = "service:foreign"
				pass = false
			case "duplicate-subject":
				state.Subjects[0] = state.Subjects[1]
				pass = false
			case "subjects":
				state.Subjects[0] = "service:foreign"
				pass = false
			case "reordered-acks":
				slices.Reverse(state.Installed)
			case "missing-source-expiry":
				source.Expires = time.Time{}
				pass = false
			}
			proof, err := managedCoreSessionComplete(state, source, service, subjects, clock)
			if (err == nil) != pass {
				t.Fatal("unqualified lifecycle accepted", mode, err)
			}
			if pass {
				baseline, err := managedCoreSessionComplete(original, saved, service, subjects, now)
				if err != nil || deploymentplan.Digest(proof) != deploymentplan.Digest(baseline) || proof.Action != "finalize-complete-original-session" {
					t.Fatal("bookkeeping changed finalizer authority", mode, err)
				}
			}
		})
	}
}

func TestManagedCoreOldFiveCleanupUsesFreshAdmission(t *testing.T) {
	for _, mode := range []string{"new-core", "old-third", "missing-fresh", "reused-original", "changed-original", "changed-source", "changed-journal", "bad-review"} {
		t.Run(mode, func(t *testing.T) {
			core := managedCoreObservationFixture()
			r := &createServiceSessionRecovery{Review: core.OriginalRepairReview, ManagedCorePartial: core}
			pass := true
			switch mode {
			case "old-third":
				r.ManagedCorePartial = nil
			case "missing-fresh":
				core.ForwardReview = ""
				pass = false
			case "reused-original":
				core.ForwardReview = core.OriginalRepairReview
				pass = false
			case "changed-original":
				core.OriginalRepairReview = strings.Repeat("f", 64)
				pass = false
			case "changed-source":
				core.SourceSHA = "unknown"
				pass = false
			case "changed-journal":
				core.Journal.Accepted = true
				pass = false
			case "bad-review":
				r.Review = "unknown"
				pass = false
			}
			review, schema, err := managedHistoricalSeedAdmissionContext(r)
			if (err == nil) != pass {
				t.Fatal("unqualified admission", mode, err)
			}
			if pass && r.ManagedCorePartial != nil && (review != core.ForwardReview || schema != "rtk-cloud.create-managed-complete-issuer-partial-recovery/v1") {
				t.Fatal("old736 used as fresh GO")
			}
			if pass && r.ManagedCorePartial == nil && (review != r.Review || schema != "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1") {
				t.Fatal("prior behavior changed")
			}
		})
	}
}

func TestManagedCoreRestoreOnlyBindsRealMissingLogACKAndNeverFinalizes(t *testing.T) {
	_, _, service, subjects, now := managedHistoricalSeedFixture(t)
	original := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("a", 64), Status: "active", Expires: now.Add(-time.Minute), Subjects: subjects, Installed: slices.Clone(subjects[:8])}
	saved := original
	saved.Installed = nil
	for _, mode := range []string{"active-expired-eight", "native-expired-eight", "future-expiry", "seven", "nine", "other-missing", "duplicate", "foreign", "sealed", "finalized", "unknown-action", "finalizer-action"} {
		t.Run(mode, func(t *testing.T) {
			state := original
			state.Installed = slices.Clone(original.Installed)
			source := saved
			action := managedCoreRestoreOnly
			pass := true
			switch mode {
			case "native-expired-eight":
				state.Status = "expired"
			case "future-expiry":
				state.Expires = now.Add(time.Minute)
				source.Expires = state.Expires
				pass = false
			case "seven":
				state.Installed = state.Installed[:7]
				pass = false
			case "nine":
				state.Installed = slices.Clone(subjects)
				pass = false
			case "other-missing":
				state.Installed[0] = "service:video-cloud-logingester"
				pass = false
			case "duplicate":
				state.Installed[0] = state.Installed[1]
				pass = false
			case "foreign":
				state.Installed[0] = "service:foreign"
				pass = false
			case "sealed", "finalized":
				state.Status = mode
				pass = false
			case "unknown-action":
				action = "arbitrary-resume"
				pass = false
			case "finalizer-action":
				action = managedCoreFinalizeComplete
				pass = false
			}
			proof, err := managedCoreSessionObservation(state, source, service, subjects, now, action)
			if (err == nil) != pass {
				t.Fatal("unqualified missingACK/action accepted", mode, err)
			}
			if pass && (proof.Action != "restore-core-only" || len(proof.Installed) != 8 || slices.Contains(proof.Installed, "service:video-cloud-logingester")) {
				t.Fatal("observed8 promoted to9/finalization")
			}
			if pass {
				if _, err := managedCoreSessionComplete(state, source, service, subjects, now); err == nil {
					t.Fatal("core-onlyproof authorizedfinalizer")
				}
			}
		})
	}
}

func TestManagedCoreNativePolicyCarrierKeepsOriginalRecipeAndDBReference(t *testing.T) {
	for _, mode := range []string{"original", "api-defaults", "wrong-uid", "wrong-image", "wrong-mount", "wrong-environment", "wrong-secret", "wrong-key", "literal-dsn", "duplicate-dsn", "missing-dsn", "database-override", "fake-source-journal"} {
		t.Run(mode, func(t *testing.T) {
			core := managedCoreObservationFixture()
			baseline := core.Plan.Resources[14].Baseline
			container := mqttFoundationPrimary(baseline)
			container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": "dev"}, deploymentplan.Object{"name": "CERT_ISSUER_DB_DSN", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-issuer-database", "key": "url"}}})
			pass := true
			switch mode {
			case "wrong-environment":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_ENVIRONMENT": "staging"}))
				pass = false
			case "wrong-secret", "wrong-key", "literal-dsn":
				env := deploymentplan.Map(certIssuerObjectList(container["env"])[6])
				ref := deploymentplan.Map(deploymentplan.Map(env["valueFrom"])["secretKeyRef"])
				if mode == "wrong-secret" {
					ref["name"] = "foreign"
				} else if mode == "wrong-key" {
					ref["key"] = "foreign"
				} else {
					delete(env, "valueFrom")
					env["value"] = "literal-never-a-credential"
				}
				pass = false
			case "duplicate-dsn":
				container["env"] = append(certIssuerObjectList(container["env"]), certIssuerObjectList(container["env"])[6])
				pass = false
			case "missing-dsn":
				container["env"] = certIssuerObjectList(container["env"])[:6]
				pass = false
			case "database-override":
				container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": "PKI_DATABASE_URL", "value": "foreign"})
				pass = false
			}
			// Untouched owners bind the captured native baseline, including API
			// defaults already present when that original body was recorded.
			if mode == "api-defaults" {
				container["imagePullPolicy"] = "IfNotPresent"
			}
			core.Journal.PlanSHA256 = core.Plan.SHA256()
			actual := deploymentplan.Clone(baseline)
			switch mode {
			case "api-defaults":
				mqttFoundationPrimary(actual)["imagePullPolicy"] = "IfNotPresent"
			case "wrong-uid":
				deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				pass = false
			case "wrong-image":
				mqttFoundationPrimary(actual)["image"] = "foreign"
				pass = false
			case "wrong-mount":
				mqttFoundationPrimary(actual)["volumeMounts"] = []any{deploymentplan.Object{"name": "foreign", "mountPath": "/var/lib/pki-service"}}
				pass = false
			case "fake-source-journal":
				core.Journal.Failed = ""
				pass = false
			}
			before := deploymentplan.Digest(core)
			resource := core.Plan.Resources[14]
			if (validateManagedCorePolicyCarrierResource(core, resource, resource.Baseline, actual) == nil) != pass {
				t.Fatal("unqualifiednativecarrier", mode)
			}
			if before != deploymentplan.Digest(core) {
				t.Fatal("carriercheckchangedsource")
			}
		})
	}
}

func TestManagedCoreNativePolicyCarrierAcceptsOnlyTheCompletedForwardOwner(t *testing.T) {
	core, candidate := managedCorePartialFixture(t)
	resource := candidate.Resources[14]
	actual := deploymentplan.Clone(resource.Desired)
	deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
	if err := validateManagedCorePolicyCarrierResource(core, resource, resource.Desired, actual); err != nil {
		t.Fatal("accepted forward CertIssuer was not a valid policy carrier", err)
	}
	for _, mode := range []string{"old-owner", "old-recipe", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			observed := deploymentplan.Clone(actual)
			selected := resource
			carrier := resource.Desired
			switch mode {
			case "old-owner":
				selected.UID = core.Plan.Resources[14].UID
				selected.UID = "99999999-9999-4999-8999-999999999999"
			case "old-recipe":
				carrier = core.Plan.Resources[14].Baseline
			case "replacement":
				deploymentplan.Map(observed["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			}
			if validateManagedCorePolicyCarrierResource(core, selected, carrier, observed) == nil {
				t.Fatal("accepted a carrier outside the completed forward authority", mode)
			}
		})
	}
}
