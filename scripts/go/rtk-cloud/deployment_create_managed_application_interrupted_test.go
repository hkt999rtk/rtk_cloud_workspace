package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationInterruptedFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedApplicationPartialRecovery, *createManagedApplicationInterruptedForward) {
	t.Helper()
	store, e, _, _ := managedApplicationPartialFixture(t)
	rich, _ := managedApplicationForwardActionsFixture(t)
	// Historical418 lacks the new base Service CRL field. That addition belongs
	// only to the next qualified candidate, never this immutable source.
	for _, index := range []int{22, 23, 24} {
		for _, body := range []deploymentplan.Object{rich.Plan.Resources[index].Desired, rich.Plan.Resources[index].Baseline} {
			if body == nil {
				continue
			}
			for _, raw := range certIssuerObjectList(environmentPKIPod(body)["containers"]) {
				container := deploymentplan.Map(raw)
				var settings []any
				for _, setting := range certIssuerObjectList(container["env"]) {
					if deploymentplan.Map(setting)["name"] != "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST" {
						settings = append(settings, setting)
					}
				}
				container["env"] = settings
			}
		}
	}
	for _, raw := range certIssuerObjectList(environmentPKIPod(rich.Plan.Resources[23].Desired)["containers"]) {
		for _, setting := range certIssuerObjectList(deploymentplan.Map(raw)["env"]) {
			field := deploymentplan.Map(setting)
			if field["name"] == "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS" {
				field["value"] = "true"
			}
		}
	}
	for _, index := range []int{23, 24} {
		domain := "app"
		if index == 24 {
			domain = "device"
		}
		for _, body := range []deploymentplan.Object{rich.Plan.Resources[index].Desired, rich.Plan.Resources[index].Baseline} {
			if body == nil {
				continue
			}
			for _, raw := range certIssuerObjectList(environmentPKIPod(body)["containers"]) {
				container := deploymentplan.Map(raw)
				for _, setting := range certIssuerObjectList(container["env"]) {
					field := deploymentplan.Map(setting)
					if field["name"] == "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST" {
						field["value"] = "/run/pki-" + domain + "/bundles.json"
					}
				}
				var mounts []any
				for _, mount := range certIssuerObjectList(container["volumeMounts"]) {
					if deploymentplan.Map(mount)["name"] != domain+"-trust-manifest" {
						mounts = append(mounts, mount)
					}
				}
				container["volumeMounts"] = mounts
			}
			var volumes []any
			for _, volume := range certIssuerObjectList(environmentPKIPod(body)["volumes"]) {
				if deploymentplan.Map(volume)["name"] != domain+"-trust-manifest" {
					volumes = append(volumes, volume)
				}
			}
			environmentPKIPod(body)["volumes"] = volumes
		}
	}
	for index := 0; index < 2; index++ {
		rich.Plan.Resources[index].Desired["spec"] = deploymentplan.Object{}
	}
	rich.Journal.PlanSHA256 = rich.Plan.SHA256()
	definitions, err := managedApplicationPartialDefinitionsWithServiceCRL(rich, false)
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{}
	for index, resource := range rich.Plan.Resources {
		body, uid := resource.Desired, rich.Journal.Owners[resource.ID]
		if index > 22 {
			body, uid = resource.Baseline, resource.UID
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		if index < 2 {
			deploymentplan.Map(actual["spec"])["volumeName"] = "genuine-recorded-pv-" + deploymentplan.String(deploymentplan.Map(actual["metadata"])["name"])
		}
		live = append(live, actual)
	}
	inputs := rich.Plan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)
	candidate, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	// Replace only the isolated synthetic application stage with the maintained
	// full recipes before its next qualified source is captured.
	write(filepath.Join("deployment/rollback", rich.Plan.SHA256(), "plan.json"), rich.Plan)
	write(filepath.Join("deployment/rollback", rich.Plan.SHA256(), "journal.json"), rich.Journal)
	e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder = rich.Plan.SHA256() + ":owned"
	intent, err := environmentCreateKubernetesStageIntent(rich.Plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = hash
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	_, _, original, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-application-partial-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{"accepted-service-core-managed-application-plan": original.Plan.SHA256(), "accepted-service-core-managed-application-journal": deploymentplan.Digest(original.Journal), "accepted-service-core-managed-application-source": original.SourceSHA, "accepted-service-core-managed-application-admission": original.ArtifactSHA, "accepted-service-core-managed-application-forward-plan": candidate.SHA256()}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	stage := "managed-application-owners-forward-" + review
	actual := *candidate
	defs, err := managedApplicationPartialDefinitionsWithServiceCRL(original, false)
	if err != nil {
		t.Fatal(err)
	}
	actual.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	j := deploymentplan.Journal{PlanSHA256: actual.SHA256(), Owners: map[string]string{}, Completed: slices.Clone(original.Journal.Completed)}
	for id, uid := range original.Journal.Owners {
		j.Owners[id] = uid
	}
	forward := &createManagedApplicationInterruptedForward{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &actual, Journal: j}
	intent, err = environmentCreateKubernetesStageIntent(&actual)
	if err != nil {
		t.Fatal(err)
	}
	hash, err = environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "pending", StartedAt: time.Now(), IntentSHA256: hash})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "genuine-interrupted-lease", Holder: actual.SHA256() + ":owned"})
	dir := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(dir, "qualification.json"), a)
	write(filepath.Join(dir, "source-journal.json"), source)
	write(filepath.Join(dir, "openbao-root-trust-plan.json"), candidate)
	write(filepath.Join("deployment/rollback", actual.SHA256(), "plan.json"), actual)
	write(filepath.Join("deployment/rollback", actual.SHA256(), "journal.json"), j)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return store, e, original, forward
}

func TestManagedApplicationInterruptedRequiresActualQualifiedTwoEntryOneLeaseSource(t *testing.T) {
	for _, mode := range []string{"original", "admission", "prefix", "metadata", "original-alias", "extra-lease", "wrong-lease", "wrong-intent", "extra-stage", "completed-forward"} {
		t.Run(mode, func(t *testing.T) {
			store, e, original, forward := managedApplicationInterruptedFixture(t)
			switch mode {
			case "admission":
				e.Journal.Entries[len(e.Journal.Entries)-2].IntentSHA256 = strings.Repeat("0", 64)
			case "prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("0", 64)
			case "original-alias":
				e.Journal.Entries[len(e.Journal.Entries)-3].State = "completed"
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "wrong-lease":
				e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder = strings.Repeat("0", 64) + ":foreign"
			case "wrong-intent":
				e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = strings.Repeat("0", 64)
			case "extra-stage":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-1])
			case "completed-forward":
				e.Journal.Entries[len(e.Journal.Entries)-1].State = "completed"
			}
			source, _, loaded, err := loadManagedApplicationInterruptedSource(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified interrupted source accepted", mode, err)
			}
			if mode == "original" {
				if deploymentplan.Digest(source.Journal) != forward.SourceSHA || loaded.Plan.SHA256() != forward.Plan.SHA256() {
					t.Fatal("source or written candidate changed")
				}
				original.Interrupted = loaded
				plan, j, err := managedApplicationPartialCurrentPlan(original)
				if err != nil || plan.SHA256() != loaded.Plan.SHA256() || j.Failed != "" || original.Journal.Failed == "" {
					t.Fatal("actual interrupted state lost or original Failed fabricated", err)
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-application-interrupted-forward" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong interrupted selector", profile, flag)
				}
			}
		})
	}
}

func TestManagedApplicationInterruptedRejectsUnqualifiedProgressRecipeAndOwnerChanges(t *testing.T) {
	_, _, original, forward := managedApplicationInterruptedFixture(t)
	for _, mode := range []string{"original", "fabricated-failed", "inflight", "accepted", "acceptance-started", "more-completed", "unwritten-owner", "replaced-api-owner", "changed-image", "changed-private-key", "changed-baseline", "changed-dag", "unknown-source-sha"} {
		t.Run(mode, func(t *testing.T) {
			var changed createManagedApplicationInterruptedForward
			if json.Unmarshal(mustProviderRecoveryJSON(forward), &changed) != nil {
				t.Fatal("fixture cloning failed")
			}
			switch mode {
			case "fabricated-failed":
				changed.Journal.Failed = changed.Plan.Resources[22].ID
			case "inflight":
				changed.Journal.InFlight = changed.Plan.Resources[22].ID
			case "accepted":
				changed.Journal.Accepted = true
			case "acceptance-started":
				changed.Journal.AcceptanceStarted = true
			case "more-completed":
				changed.Journal.Completed = append(changed.Journal.Completed, changed.Plan.Resources[22].ID)
			case "unwritten-owner":
				changed.Journal.Owners[changed.Plan.Resources[23].ID] = changed.Plan.Resources[23].UID
			case "replaced-api-owner":
				changed.Journal.Owners[changed.Plan.Resources[22].ID] = "88888888-8888-4888-8888-888888888888"
			case "changed-image":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Plan.Resources[22].Desired)["containers"])[0])["image"] = "ghcr.io/foreign/api@sha256:" + strings.Repeat("f", 64)
			case "changed-private-key":
				for _, raw := range certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Plan.Resources[24].Desired)["containers"])[0])["env"]) {
					m := deploymentplan.Map(raw)
					if m["name"] == "VIDEO_CLOUD_API_TLS_KEY_PATH" {
						m["value"] = "/foreign/key.pem"
					}
				}
			case "changed-baseline":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Plan.Resources[23].Baseline)["containers"])[0])["image"] = "foreign"
			case "changed-dag":
				changed.Plan.Resources[22].Dependencies = append(changed.Plan.Resources[22].Dependencies, changed.Plan.Resources[4].ID)
			case "unknown-source-sha":
				changed.SourceSHA = ""
			}
			changed.Journal.PlanSHA256 = changed.Plan.SHA256()
			if (validateManagedApplicationInterruptedForward(original, &changed) == nil) != (mode == "original") {
				t.Fatal("unqualified interrupted native recipe/progress accepted", mode)
			}
		})
	}
}

func TestManagedApplicationInterruptedRejectsShortAndRecursiveForwardSuffix(t *testing.T) {
	for length := 0; length < 15; length++ {
		if managedApplicationInterruptedReview(environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, length)}) != "" {
			t.Fatal("short journal selected")
		}
	}
	_, e, _, _ := managedApplicationInterruptedFixture(t)
	e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-2:]...)
	if managedApplicationInterruptedReview(e.Journal) != "" {
		t.Fatal("unqualified repeated forward selected")
	}
}
