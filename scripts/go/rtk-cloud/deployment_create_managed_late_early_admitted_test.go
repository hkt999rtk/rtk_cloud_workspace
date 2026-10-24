package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateEarlyAdmittedFixture(t *testing.T) (secretStore, *environmentCreateExecution, deploymentConfig, *createManagedLateSessionRecovery) {
	t.Helper()
	store, e, cfg, late, source := managedLateAdmittedReuseFixture(t)
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	now := time.Now().UTC()
	for i := range source.Entries {
		source.Entries[i].StartedAt = now.Add(-time.Hour)
		source.Entries[i].CompletedAt = now.Add(-time.Hour)
	}
	late.SourceSHA = deploymentplan.Digest(source)
	late.ExpiredRenewal.SourceSHA = late.SourceSHA
	late.OriginalSession = "11111111-1111-4111-8111-111111111111"
	late.ExpiredRenewal.PriorSession.Session = late.OriginalSession
	late.NewSessionID = "22222222-2222-4222-8222-222222222222"
	late.Renewal = &createManagedSessionRecovery{}
	late.SeedCandidate = &deploymentplan.Plan{Version: deploymentplan.Version}
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateExpiredRenewalSchema, ParentSHA: late.ParentSHA, ParentOwnerSHA: late.SourceOwnerSHA, ParentJournalSHA: late.SourceSHA, CheckerSHA: strings.Repeat("1", 64), Plan: environmentCreatePlan{ConfigurationSHA256: deploymentplan.Digest(cfg)}, Materials: map[string]string{"accepted-service-core-late-session-source-descriptor": managedLateSessionSourceDigest(late)}}, QualifiedAt: now.Add(-2 * time.Minute), ValidUntil: now.Add(13 * time.Minute)}
	late.Review = deploymentplan.Digest(a.Plan)
	late.ExpiredRenewal.Review = late.Review
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: late.Review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: late.Review})
	}
	e.Journal = source
	e.Journal.Entries = append(slices.Clone(source.Entries), environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + late.Review, Phase: "recovery", State: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now.Add(-30 * time.Second), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": late.SourceSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review, "audit_sha256": strings.Repeat("2", 64)})})
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": source, "source-owner.json": e.owner, "managed-late-session-source.json": late} {
		managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create/openbao-recovery", late.Review, name), mustProviderRecoveryJSON(value))
	}
	managedLateSourceReuseNegativeWrite(t, store, e.owner.Journal, mustProviderRecoveryJSON(e.Journal))
	if !managedLateExpiredAdmittedOnlyMembership(e, source, a, late, now) {
		t.Fatal("isolated early375 membership refused")
	}
	return store, e, cfg, late
}

func TestManagedLateEarlyAdmittedSourcePhasePrecedesEveryInitialTransportRead(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source unavailable")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(file), "deployment_create_openbao_recovery.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"managedLatePostMintTransportMinimum": false, "foundationRecoveryMaterialsForExecution": false, "verifyFoundationMaterialBindings": false, "readInitialTransportRenewal": false}
	var hook token.Pos
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runEnvironmentCreateOpenBaoRecoveryContext" {
			continue
		}
		for _, statement := range fn.Body.List {
			guard, ok := statement.(*ast.IfStmt)
			if !ok {
				continue
			}
			assignment, ok := guard.Init.(*ast.AssignStmt)
			if !ok || len(assignment.Rhs) != 1 {
				continue
			}
			call, ok := assignment.Rhs[0].(*ast.CallExpr)
			if id, named := callFunctionName(call); ok && named && id == "beginManagedLateEarlyAdmittedSourcePhase" {
				if hook.IsValid() {
					t.Fatal("duplicate early source phase")
				}
				hook = call.Pos()
			}
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			name, named := callFunctionName(call)
			if !ok || !named {
				return true
			}
			if seen, tracked := wanted[name]; tracked && !seen {
				if !hook.IsValid() || call.Pos() <= hook {
					t.Errorf("%s reconstructs admitted history before the strict early source phase", name)
				}
				wanted[name] = true
			}
			return true
		})
	}
	for name, seen := range wanted {
		if !seen {
			t.Error("initial transport seam not checked", name)
		}
	}
}

func callFunctionName(call *ast.CallExpr) (string, bool) {
	if call == nil {
		return "", false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func TestManagedLateEarlyAdmittedSourcePhaseRetainsExactValidatedPhase(t *testing.T) {
	store, e, cfg, late := managedLateEarlyAdmittedFixture(t)
	cap, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
	if err != nil {
		t.Fatal(err)
	}
	// Represent a phase whose full RuntimeProgress already passed. Production
	// never commits these private fields merely from the structural fixture.
	cap.currentSHA, cap.admissionReview, cap.descriptorSHA = deploymentplan.Digest(e.Journal), late.Review, managedLateSessionSourceDigest(late)
	e.lateSourceReuse = cap
	if err := beginManagedLateEarlyAdmittedSourcePhase(context.Background(), cfg, store, e); err != nil || e.lateSourceReuse != cap {
		t.Fatal("early initial transport boundary discarded the exact validated phase", err)
	}
	observed := managedLateTransportReuseObserved(t, store, e)
	view, err := managedLateTransportSourceExecution(store, e.Journal.PlanSHA256, observed, e)
	if err != nil || view.lateSourceReuse != cap {
		t.Fatal("first foundation transport read could not borrow the exact admitted phase", err)
	}
}

func TestManagedLateEarlyAdmittedSourcePhaseCannotPromoteAnEnvelope(t *testing.T) {
	store, e, cfg, _ := managedLateEarlyAdmittedFixture(t)
	before := deploymentplan.Digest(e.Journal)
	if err := beginManagedLateEarlyAdmittedSourcePhase(context.Background(), cfg, store, e); err == nil || managedLateSourceReuseActive(e) {
		t.Fatal("early hook promoted a synthetic envelope without full original history")
	}
	if deploymentplan.Digest(e.Journal) != before || e.Check(context.Background()) != nil {
		t.Fatal("refused early phase changed held parent history")
	}
}

func TestManagedLateEarlyAdmittedSourcePhaseRefusesDrift(t *testing.T) {
	for _, mode := range []string{"configuration", "store", "owner", "descriptor", "pending-admission", "manifest"} {
		t.Run(mode, func(t *testing.T) {
			store, e, cfg, late := managedLateEarlyAdmittedFixture(t)
			selected := store
			cap, err := captureManagedLateAdmittedSourceReuse(context.Background(), store, e, late)
			if err != nil {
				t.Fatal(err)
			}
			cap.currentSHA, cap.admissionReview, cap.descriptorSHA = deploymentplan.Digest(e.Journal), late.Review, managedLateSessionSourceDigest(late)
			e.lateSourceReuse = cap
			switch mode {
			case "configuration":
				cfg.Values["changed"] = "configuration"
			case "store":
				selected.ConfigRoot += "-foreign"
			case "owner":
				e.owner.OwnerToken = "foreign"
			case "descriptor":
				late.NewSessionID = "33333333-3333-4333-8333-333333333333"
				managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-session-source.json"), mustProviderRecoveryJSON(late))
			case "pending-admission":
				e.Journal.Entries[374].State = "pending"
			case "manifest":
				managedLateSourceReuseNegativeWrite(t, store, "openbao/root-token", []byte("changed-private-fixture"))
			}
			if err := beginManagedLateEarlyAdmittedSourcePhase(context.Background(), cfg, selected, e); err == nil {
				t.Fatal("early admitted source accepted drift", mode)
			}
		})
	}
}
