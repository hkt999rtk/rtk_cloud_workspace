package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedTransportPostManagedFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedTransportPartialRecovery) {
	t.Helper()
	store, e, partial := managedTransportAcceptedForwardFixture(t)
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	defs, live := []deploymentplan.Definition{}, []deploymentplan.Object{}
	for _, resource := range partial.AcceptedForward.Plan.Resources {
		actual := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
		body := deploymentplan.Clone(resource.Desired)
		if strings.HasSuffix(resource.ID, "/openbao-file") {
			deploymentplan.Map(body["data"])["file.hcl"] = deploymentplan.String(deploymentplan.Map(body["data"])["file.hcl"]) + managedTransportOriginalAuditBlock
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	inputs := partial.Original.Inputs
	inputs.BindingsSHA256 = strings.Repeat("d", 64)
	preview, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256()}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	appendAccepted := func(stage string, plan *deploymentplan.Plan, ownerPrefix string) deploymentplan.Journal {
		journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
		for index, resource := range plan.Resources {
			owner := resource.UID
			if owner == "" {
				owner = ownerPrefix + "0000000-0000-4000-8000-000000000000"
			}
			journal.Completed = append(journal.Completed, resource.ID)
			journal.Owners[resource.ID] = owner
			_ = index
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := environmentCreatePlanSHA256(intent)
		if err != nil {
			t.Fatal(err)
		}
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "completed", CompletedAt: time.Now(), IntentSHA256: hash, Receipt: mustProviderRecoveryJSON(map[string]any{"plan_sha256": plan.SHA256(), "owners": journal.Owners})})
		e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution", UID: "genuine-" + stage + "-lease", Holder: plan.SHA256() + ":native-owned"})
		write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
		write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
		return journal
	}
	plan := *preview
	plan.BindingsSHA256 = strings.Repeat("e", 64)
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})})
	journal := appendAccepted("transport-managed-hosts-forward-"+review, &plan, "a")
	root, share := "original-root-token", "original-retained-share"
	if err := store.write("openbao/root-token", []byte(root), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("openbao/unseal-key", []byte(share), false); err != nil {
		t.Fatal(err)
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	setID, configID := "StatefulSet/"+plan.Stack+"-secrets/openbao", "ConfigMap/"+plan.Stack+"-secrets/openbao-file"
	audit := strings.Repeat("f", 64)
	hcl := ""
	for _, resource := range plan.Resources {
		if resource.ID == configID {
			hcl = deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["file.hcl"])
		}
	}
	intent := deploymentplan.Object{"pod_uid": "33333333-3333-4333-8333-333333333333", "statefulset_uid": journal.Owners[setID], "configmap_uid": journal.Owners[configID], "hcl_sha256": environmentPreflightBytesSHA256([]byte(hcl)), "provider_process_sha256": strings.Repeat("a", 64), "signal": "SIGHUP", "audit_path": "/openbao/audit/audit.jsonl", "log_raw": false}
	signal := deploymentplan.Clone(intent)
	signal["native_audit_sha256"] = audit
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-managed-audit-reload-" + review, Phase: "openbao-audit-reload", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(signal)})
	proof := deploymentplan.Object{"pod_uid": intent["pod_uid"], "revision": "openbao-original-managed-revision", "statefulset_uid": journal.Owners[setID], "written_plan_sha256": partial.AcceptedForward.Plan.SHA256(), "written_journal_sha256": deploymentplan.Digest(partial.AcceptedForward.Journal), "original_init_sha256": environmentPreflightBytesSHA256(raw), "original_root_token_sha256": environmentPreflightBytesSHA256([]byte(root)), "original_unseal_sha256": environmentPreflightBytesSHA256([]byte(share)), "original_audit_sha256": audit, "native_audit_sha256": audit}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-managed-recovery-verified-" + review, Phase: "openbao-verification", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(proof), Receipt: mustProviderRecoveryJSON(proof)})
	session := "22222222-2222-4222-8222-222222222222"
	aliases := map[string]string{"kubernetes/transport-managed-hosts": partial.Original.SHA256(), "kubernetes/transport-managed-hosts-forward-" + partial.PriorForward.Review: partial.PriorForward.Plan.SHA256(), "kubernetes/transport-managed-hosts-forward-" + partial.PriorForward.Previous.Review: partial.PriorForward.Previous.Plan.SHA256()}
	for index, entry := range e.Journal.Entries {
		if failed, found := aliases[entry.Key]; found {
			e.Journal.Entries[index].State = "completed"
			e.Journal.Entries[index].CompletedAt = time.Now()
			e.Journal.Entries[index].Receipt = mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/transport-managed-hosts-forward-" + review, "original_failed_plan_sha256": failed, "retained_session_id": session})
		}
	}
	post := &createManagedTransportPostManaged{Review: review, Session: session, Plan: &plan, Journal: journal, ExportPlans: map[string]*deploymentplan.Plan{}, ExportJournals: map[string]deploymentplan.Journal{}}
	for _, stage := range []string{"server-enrollment-video-cloud-api-pki", "server-custody-video-cloud-api-pki"} {
		kind, name := "Job", "pki-enroll-video-cloud-api-pki-server-"+strings.ReplaceAll(session, "-", "")[:12]
		if strings.Contains(stage, "custody") {
			kind, name = "Deployment", "pki-export-video-cloud-api-pki-"+source.PlanSHA256[:12]
		}
		body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": plan.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "original", "image": "original-formal-image"}}}}}}
		child, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Phase: "bootstrap", Object: body}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		post.ExportPlans["kubernetes/"+stage], post.ExportJournals["kubernetes/"+stage] = child, appendAccepted(stage, child, "b")
	}
	path := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(path, "qualification.json"), a)
	write(filepath.Join(path, "source-journal.json"), source)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), preview)
	partial.PostManaged = post
	return store, e, partial
}

func TestManagedTransportPartialPostManagedAuthenticatesAcceptedAuditAliasesAndFirstServerOwners(t *testing.T) {
	for _, mode := range []string{"original", "changed-admission", "changed-source-prefix", "changed-metadata", "wrong-alias-plan", "wrong-alias-source", "changed-lease", "extra-lease", "bad-reload-intent", "bad-verified-intent", "missing-verified", "bad-server-intent", "bad-custody-owner", "extra-stage"} {
		t.Run(mode, func(t *testing.T) {
			store, e, partial := managedTransportPostManagedFixture(t)
			end := len(e.Journal.Entries)
			switch mode {
			case "changed-admission":
				e.Journal.Entries[end-6].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-source-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("0", 64)
			case "wrong-alias-plan", "wrong-alias-source":
				for index, entry := range e.Journal.Entries {
					if entry.Key == "kubernetes/transport-managed-hosts" {
						failed, stage := strings.Repeat("0", 64), partial.PostManaged.Review
						if mode == "wrong-alias-source" {
							stage = strings.Repeat("0", 64)
						}
						e.Journal.Entries[index].Receipt = mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/transport-managed-hosts-forward-" + stage, "original_failed_plan_sha256": failed, "retained_session_id": partial.PostManaged.Session})
					}
				}
			case "changed-lease":
				e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder = strings.Repeat("0", 64) + ":foreign"
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "bad-reload-intent":
				e.Journal.Entries[end-4].IntentSHA256 = strings.Repeat("0", 64)
			case "bad-verified-intent":
				e.Journal.Entries[end-3].IntentSHA256 = strings.Repeat("0", 64)
			case "missing-verified":
				e.Journal.Entries[end-3].State = "pending"
			case "bad-server-intent":
				e.Journal.Entries[end-2].IntentSHA256 = strings.Repeat("0", 64)
			case "bad-custody-owner":
				e.Journal.Entries[end-1].Receipt = mustProviderRecoveryJSON(map[string]any{"plan_sha256": partial.PostManaged.ExportPlans["kubernetes/server-custody-video-cloud-api-pki"].SHA256(), "owners": map[string]string{"foreign": "foreign"}})
			case "extra-stage":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "foreign", State: "completed", CompletedAt: time.Now(), IntentSHA256: strings.Repeat("0", 64), Receipt: []byte(`{}`)})
			}
			base, prior, accepted, post, err := loadManagedTransportPostManaged(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified completed managed source accepted", mode, err)
			}
			if mode == "original" {
				if base == nil || prior == nil || accepted == nil || post == nil || !isManagedTransportBaseCheckpoint(base.Journal) || len(post.ExportPlans) != 2 || !environmentPreflightDigest.MatchString(post.ReloadSHA) || !environmentPreflightDigest.MatchString(post.VerifiedSHA) {
					t.Fatal("original managed/server proof lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-transports-complete-api-export" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong post-managed selector", profile, flag)
				}
			}
		})
	}
}

func TestManagedTransportPartialPostManagedOnlyObservesSevenCompletedOwners(t *testing.T) {
	for _, mode := range []string{"original", "changed-image", "changed-hcl", "extra-object", "host-update"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportPostManagedFixture(t)
			defs, live := []deploymentplan.Definition{}, []deploymentplan.Object{}
			for _, resource := range partial.PostManaged.Plan.Resources {
				body := deploymentplan.Clone(resource.Desired)
				actual := deploymentplan.Clone(resource.Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
				live = append(live, actual)
				if mode == "changed-image" && strings.HasSuffix(resource.ID, "/openbao") {
					environmentPKIPod(body)["foreign-image"] = "new"
				}
				if mode == "changed-hcl" && strings.HasSuffix(resource.ID, "/openbao-file") {
					deploymentplan.Map(body["data"])["file.hcl"] = "foreign"
				}
				defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
			}
			if mode == "extra-object" {
				defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "foreign", "namespace": partial.Original.Stack + "-secrets"}}})
			}
			err := validateManagedTransportPartialForwardDefinitions(partial, defs)
			if (err == nil) != (mode == "original" || mode == "host-update") {
				t.Fatal("completed host replay or drift allowed", mode, err)
			}
			if err == nil {
				candidate, err := deploymentplan.Compile(partial.Original.Inputs, defs, live)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "host-update" {
					candidate.Resources[0].Action = "update"
				}
				if err := validateManagedTransportPartialForwardActions(partial, candidate); (err == nil) != (mode == "original") {
					t.Fatal("completed owner reapplied", mode, err)
				}
			}
		})
	}
}
