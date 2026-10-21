package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateQualifiedArtifactFixture(t *testing.T) (createFoundationRecoveryArtifact, *environmentCreateExecution, createFoundationRecoveryPlan, string, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	pin := strings.Repeat("a", 64)
	e := &environmentCreateExecution{owner: environmentCreateOwner{PlanSHA256: pin, OwnerToken: "isolated-owner"}, Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: pin, OwnerToken: "isolated-owner"}}
	base := createFoundationRecoveryPlan{Schema: managedLateSessionSchema, ParentSHA: pin, ParentOwnerSHA: deploymentplan.Digest(e.owner), ParentJournalSHA: deploymentplan.Digest(e.Journal), CheckerSHA: strings.Repeat("b", 64), KubeconfigSHA: strings.Repeat("c", 64), FoundationSHA: strings.Repeat("d", 64), FoundationJournalSHA: strings.Repeat("e", 64), Materials: map[string]string{"original-key": pin}, Plan: environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: pin}}
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: base, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256, a.Qualification.Decision = review, "GO"
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	return a, e, base, review, now
}

func TestManagedLateQualifiedArtifactUsesFreshSixGOAndUnchangedActualSource(t *testing.T) {
	a, e, base, review, now := managedLateQualifiedArtifactFixture(t)
	if !managedLateQualifiedArtifactValid(a, e, base, review, now) {
		t.Fatal("exact pre-admission sixGO rejected")
	}
	missing := a
	missing.Plan.CheckerSHA = ""
	missingBase := base
	missingBase.CheckerSHA = ""
	missingReview := deploymentplan.Digest(missing.Plan)
	missing.Qualification.PlanSHA256 = missingReview
	missing.Qualification.Gates = slices.Clone(a.Qualification.Gates)
	for i := range missing.Qualification.Gates {
		missing.Qualification.Gates[i].EvidenceSHA256 = missingReview
	}
	if managedLateQualifiedArtifactValid(missing, e, missingBase, missingReview, now) {
		t.Fatal("an absent current checker is not qualification authority")
	}
	for _, tc := range []struct {
		name string
		edit func(*createFoundationRecoveryArtifact, *environmentCreateExecution, *createFoundationRecoveryPlan, *time.Time)
	}{
		{"NO-GO", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Decision = "NO-GO"
		}},
		{"failed gate", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Gates[3].Status = "FAIL"
		}},
		{"optional gate", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Gates[3].Required = false
		}},
		{"gate order", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Gates[0], a.Qualification.Gates[1] = a.Qualification.Gates[1], a.Qualification.Gates[0]
		}},
		{"gate evidence", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Gates[0].EvidenceSHA256 = strings.Repeat("f", 64)
		}},
		{"missing gate", func(a *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			a.Qualification.Gates = a.Qualification.Gates[:5]
		}},
		{"future qualification", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, n *time.Time) {
			*n = a.QualifiedAt.Add(-time.Second)
		}},
		{"15m boundary", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, _ *createFoundationRecoveryPlan, n *time.Time) {
			*n = a.ValidUntil
		}},
		{"new checker", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, p *createFoundationRecoveryPlan, _ *time.Time) {
			p.CheckerSHA = strings.Repeat("f", 64)
		}},
		{"configuration", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, p *createFoundationRecoveryPlan, _ *time.Time) {
			p.Plan.ConfigurationSHA256 = strings.Repeat("f", 64)
		}},
		{"Kubernetes identity", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, p *createFoundationRecoveryPlan, _ *time.Time) {
			p.KubeconfigSHA = strings.Repeat("f", 64)
		}},
		{"owner replacement", func(_ *createFoundationRecoveryArtifact, e *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			e.owner.OwnerToken = "foreign"
		}},
		{"new parent stage", func(_ *createFoundationRecoveryArtifact, e *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unexpected", State: "pending"})
		}},
		{"new parent lease", func(_ *createFoundationRecoveryArtifact, e *environmentCreateExecution, _ *createFoundationRecoveryPlan, _ *time.Time) {
			e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{})
		}},
		{"original material changed", func(_ *createFoundationRecoveryArtifact, _ *environmentCreateExecution, p *createFoundationRecoveryPlan, _ *time.Time) {
			p.Materials["original-key"] = strings.Repeat("f", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed createFoundationRecoveryArtifact
			_ = json.Unmarshal(mustProviderRecoveryJSON(a), &changed)
			var source environmentCreateExecution
			source.owner = e.owner
			source.Journal = e.Journal
			var expected createFoundationRecoveryPlan
			_ = json.Unmarshal(mustProviderRecoveryJSON(base), &expected)
			at := now
			tc.edit(&changed, &source, &expected, &at)
			if managedLateQualifiedArtifactValid(changed, &source, expected, review, at) {
				t.Fatal("changed qualification/source admitted")
			}
		})
	}
}

func TestManagedLateQualifiedAncestryReportsTheExactChangedAuthority(t *testing.T) {
	pin := func(value string) string { return strings.Repeat(value, 64) }
	session := "11111111-1111-4111-8111-111111111111"
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: pin("a")}}
	original := &createManagedLateSessionRecovery{
		Mode: managedLateSessionACKReady, ParentSHA: pin("1"), SourceOwnerSHA: pin("2"), SourceSHA: pin("3"), CoreSourceSHA: pin("4"), SourceArtifactSHA: pin("5"), OriginalRepairReview: pin("6"), RestoreReview: pin("7"), OriginalSession: session, OriginalEnrollmentSession: session, OriginalClientsSHA: pin("8"), OriginalKeySHA: pin("9"), OriginalCSRSHA: pin("a"), AcceptedCore: plan,
		AcceptedCoreJournal: deploymentplan.Journal{PlanSHA256: plan.SHA256()}, SourceCeremony: environmentCreateServiceCeremony{BootstrapSessionID: session}, SourceRenewedIntent: createServiceSessionIntent{SessionID: session},
	}
	var archived createManagedLateSessionRecovery
	if json.Unmarshal(mustProviderRecoveryJSON(original), &archived) != nil || managedLateQualifiedAncestryDifference(original, &archived) != "" {
		t.Fatal("JSON roundtrip changed immutable ancestry")
	}
	for _, tc := range []struct {
		name string
		edit func(*createManagedLateSessionRecovery)
	}{
		{"source", func(v *createManagedLateSessionRecovery) { v.SourceSHA = pin("b") }},
		{"accepted-core", func(v *createManagedLateSessionRecovery) { v.AcceptedCore.ConfigurationSHA256 = pin("c") }},
		{"ceremony", func(v *createManagedLateSessionRecovery) {
			v.SourceCeremony.BootstrapSessionID = "22222222-2222-4222-8222-222222222222"
		}},
		{"renewed-intent", func(v *createManagedLateSessionRecovery) {
			v.SourceRenewedIntent.SessionID = "22222222-2222-4222-8222-222222222222"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed createManagedLateSessionRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(&archived), &changed) != nil {
				t.Fatal("copy fixture")
			}
			tc.edit(&changed)
			if difference := managedLateQualifiedAncestryDifference(original, &changed); difference != tc.name {
				t.Fatal("wrong ancestry diagnostic", difference)
			}
		})
	}
}

func managedLateQualifiedPreviewsFixture(t *testing.T) (secretStore, string, *createManagedLateSessionRecovery, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store := secretStore{ConfigRoot: root, Root: filepath.Join(root, "dev"), Environment: "dev"}
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	late, owners := managedLateOwnersFixture(t)
	late.OwnerCandidate = owners
	late.ParentSHA, late.SourceSHA, late.SourceOwnerSHA = strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	late.TrustOwners, late.VerifierOwners = map[string]deploymentplan.Resource{}, map[string]deploymentplan.Resource{}
	producer, logLate := managedLateLogNativeContractFixture(t)
	prospective := *producer
	prospective.NewSession = managedSessionState{Session: late.NewSessionID, Issuer: producer.NewSession.Issuer, Root: producer.NewSession.Root, Status: "qualified-not-issued", Subjects: producer.NewSession.Subjects}
	late.LogProducer = &prospective
	late.HelperBindingSHA, late.LogNotDueUntil = prospective.Review, logLate.LogNotDueUntil
	parent := environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: strings.Repeat("4", 64)}
	helper, err := renderLogACKDraftHelper(&prospective)
	if err != nil {
		t.Fatal(err)
	}
	actual := deploymentplan.Clone(prospective.Original.Desired)
	deploymentplan.Map(actual["metadata"])["uid"] = prospective.Original.UID
	late.LogStopCandidate, err = managedLateCompile(parent, "managed-late-log-stop", []deploymentplan.Definition{renderLogACKDraftQuiesce(&prospective)}, []deploymentplan.Object{actual})
	if err != nil {
		t.Fatal(err)
	}
	late.LogHelperCandidate, err = managedLateCompile(parent, "managed-late-log-existing-ack", []deploymentplan.Definition{helper}, nil)
	if err != nil {
		t.Fatal(err)
	}
	late.LogRetireCandidate = &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: late.LogHelperCandidate.Inputs, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(helper.Object), Phase: "retirement", Action: "retire", Desired: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired), Baseline: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired)}}}
	stopped := deploymentplan.Clone(actual)
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	late.LogRestoreCandidate, err = managedLateCompile(parent, "managed-late-log-restore", []deploymentplan.Definition{renderLogACKDraftRestore(&prospective)}, []deploymentplan.Object{stopped})
	if err != nil {
		t.Fatal(err)
	}
	late.SeedCandidate = late.LogHelperCandidate
	late.CustodyCandidate = late.LogStopCandidate
	late.Renewal = &createManagedSessionRecovery{CandidateSHA256: late.SeedCandidate.SHA256()}
	late.SeedAuthorization = deploymentplan.Object{"schema": managedLateSessionSchema, "new_session": late.NewSessionID}
	late.CleanupAuthorization = deploymentplan.Object{"schema": managedLateSessionSchema, "new_session": late.NewSessionID, "cleanup": true}
	review := strings.Repeat("5", 64)
	late.Review = review
	materials := map[string]string{}
	for key, value := range managedLateQualifiedMaterialPins(late) {
		materials["accepted-service-core-late-session-"+key] = value
	}
	for filename, plan := range map[string]*deploymentplan.Plan{"used-bootstrap-renewal-plan.json": late.SeedCandidate, managedLateSessionOwnerPlanFilename: late.OwnerCandidate, "managed-late-session-custody-plan.json": late.CustodyCandidate, "managed-late-log-stop-plan.json": late.LogStopCandidate, "managed-late-log-helper-plan.json": late.LogHelperCandidate, "managed-late-log-retire-plan.json": late.LogRetireCandidate, "managed-late-log-restore-plan.json": late.LogRestoreCandidate} {
		path := filepath.Join(store.Root, "deployment/create/openbao-recovery", review, filename)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, mustProviderRecoveryJSON(plan), 0600) != nil {
			t.Fatal("isolated preview")
		}
	}
	return store, review, late, materials
}

func TestManagedLateQualifiedReaderRequiresEveryExactPreviewAndRecipePin(t *testing.T) {
	store, review, late, materials := managedLateQualifiedPreviewsFixture(t)
	if err := validateManagedLateQualifiedPreviews(store, review, late, materials); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "source-descriptor", "original-enrollment-session", "original-key-seed-plan", "owners-plan", "custody-plan", "retained-trust", "retained-verifiers", "log-producer", "log-helper-binding", "log-stop-plan", "log-helper-plan", "log-retire-plan", "log-restore-plan", "native-late-seed-authorization", "native-late-cleanup-authorization", "old-auth-roles"} {
		t.Run(key, func(t *testing.T) {
			copy := map[string]string{}
			for k, v := range materials {
				copy[k] = v
			}
			copy["accepted-service-core-late-session-"+key] = strings.Repeat("f", 64)
			if validateManagedLateQualifiedPreviews(store, review, late, copy) == nil {
				t.Fatal("changed material admitted")
			}
		})
	}
	for _, name := range []string{"used-bootstrap-renewal-plan.json", managedLateSessionOwnerPlanFilename, "managed-late-session-custody-plan.json", "managed-late-log-stop-plan.json", "managed-late-log-helper-plan.json", "managed-late-log-retire-plan.json", "managed-late-log-restore-plan.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(store.Root, "deployment/create/openbao-recovery", review, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(path, raw, 0600)
			var changed deploymentplan.Plan
			_ = json.Unmarshal(raw, &changed)
			changed.Resources[0].Dependencies = append(changed.Resources[0].Dependencies, "foreign")
			if os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600) != nil {
				t.Fatal("fixture write")
			}
			if validateManagedLateQualifiedPreviews(store, review, late, materials) == nil {
				t.Fatal("changed archived preview admitted")
			}
		})
	}
}

func TestManagedLateQualifiedDescriptorStrictJSONRoundTrip(t *testing.T) {
	_, _, late, _ := managedLateQualifiedPreviewsFixture(t)
	raw := mustProviderRecoveryJSON(late)
	var decoded createManagedLateSessionRecovery
	if err := decodeEnvironmentJSON(raw, &decoded); err != nil {
		t.Fatal("qualified late descriptor cannot pass its strict reader", err)
	}
	if managedLateSessionSourceDigest(&decoded) != managedLateSessionSourceDigest(late) {
		t.Fatal("qualified late descriptor changed during strict JSON round trip")
	}
}

func TestManagedLateQualifiedRecipeDoesNotRebaselineOrWidenScope(t *testing.T) {
	store, _, late, _ := managedLateQualifiedPreviewsFixture(t)
	_ = store
	parent := environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: strings.Repeat("4", 64)}
	definition := renderLogACKDraftQuiesce(late.LogProducer)
	if !managedLateQualifiedRecipeMatches(parent, "managed-late-log-stop", []deploymentplan.Definition{definition}, late.LogStopCandidate) {
		t.Fatal("exact frozen recipe rejected")
	}
	for _, edit := range []func(*deploymentplan.Plan){func(p *deploymentplan.Plan) { p.Resources[0].Desired["foreign"] = true }, func(p *deploymentplan.Plan) { p.Resources[0].UID = "" }, func(p *deploymentplan.Plan) { p.ConfigurationSHA256 = strings.Repeat("f", 64) }, func(p *deploymentplan.Plan) { p.Resources[0].Dependencies = []string{"foreign"} }, func(p *deploymentplan.Plan) { p.Resources[0].Action = "create" }} {
		var changed deploymentplan.Plan
		_ = json.Unmarshal(mustProviderRecoveryJSON(late.LogStopCandidate), &changed)
		edit(&changed)
		if managedLateQualifiedRecipeMatches(parent, "managed-late-log-stop", []deploymentplan.Definition{definition}, &changed) {
			t.Fatal("unreviewed recipe or baseline admitted")
		}
	}
}
