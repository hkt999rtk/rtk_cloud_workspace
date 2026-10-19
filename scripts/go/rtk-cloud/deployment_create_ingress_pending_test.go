package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createPendingIngressFixture(t *testing.T) (deploymentConfig, environmentCreateBootstrapPlan, environmentRelease, secretStore, *environmentCreateExecution, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	cfg, p, release, store, e := createIngressAdmissionFixture(t, "dev")
	at := time.Now().UTC().Add(-time.Minute)
	for i := range 431 {
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("original-completed-%d", i), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, Receipt: []byte(`{"isolated_original":true}`)})
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	m, err := prepareEnvironmentCreateIngressAdmissionMaterial(context.Background(), cfg, p, release, store, e)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := renderEnvironmentCreateIngressAdmission(cfg, p, release, m)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}, definitions[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{plan.Resources[0].ID: "11111111-1111-4111-8111-111111111111"}, Completed: []string{plan.Resources[0].ID}}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/ingress-admission-identity", Phase: "ingress-admission-identity", State: "completed", StartedAt: m.CreatedAt, CompletedAt: m.CreatedAt, IntentSHA256: intentSHA, Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})})
	for name, value := range map[string]any{"plan.json": plan, "journal.json": journal} {
		if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	return cfg, p, release, store, e, plan, journal
}

func TestCreateIngressPendingIdentityIsReadOnlyAndKeepsOriginalMaterial(t *testing.T) {
	cfg, p, release, store, e, _, _ := createPendingIngressFixture(t)
	materialPath := filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(e.Journal.PlanSHA256))
	before, _ := os.ReadFile(materialPath)
	journalBefore, _ := os.ReadFile(e.JournalPath)
	m, definitions, err := loadEnvironmentCreatePendingIngressAdmission(context.Background(), cfg, p, release, store, e)
	if err != nil || len(definitions) != 13 || validateEnvironmentCreateIngressAdmissionMaterial(p, e.Journal.PlanSHA256, m, time.Now()) != nil {
		t.Fatal("original accepted ingress identity could not be read", err)
	}
	after, _ := os.ReadFile(materialPath)
	journalAfter, _ := os.ReadFile(e.JournalPath)
	if !bytes.Equal(before, after) || !bytes.Equal(journalBefore, journalAfter) || e.Journal.Entries[431].State != "pending" {
		t.Fatal("read-only pending identity replayed generation or parent progress")
	}
}

func TestCreateIngressPendingIdentityRejectsUnknownOrChangedHistory(t *testing.T) {
	for _, mode := range []string{"missing-material", "changed-material", "intent", "completed-admission", "missing-identity-receipt", "unaccepted-child", "foreign-owner", "extra-tail", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, release, store, e, plan, journal := createPendingIngressFixture(t)
			ctx := context.Background()
			switch mode {
			case "missing-material":
				if err := os.Remove(filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(e.Journal.PlanSHA256))); err != nil {
					t.Fatal(err)
				}
			case "changed-material":
				if err := store.write(environmentCreateIngressAdmissionMaterialPath(e.Journal.PlanSHA256), []byte(`{"foreign_material":true}`), true); err != nil {
					t.Fatal(err)
				}
			case "intent":
				e.Journal.Entries[431].IntentSHA256 = deploymentplan.Digest("foreign intent")
			case "completed-admission":
				e.Journal.Entries[431].State = "completed"
				e.Journal.Entries[431].CompletedAt = time.Now().UTC()
			case "missing-identity-receipt":
				e.Journal.Entries[432].Receipt = nil
			case "unaccepted-child":
				journal.Accepted = false
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), mustProviderRecoveryJSON(journal), true); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				journal.Owners[plan.Resources[0].ID] = "not-an-original-owner"
				e.Journal.Entries[432].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), mustProviderRecoveryJSON(journal), true); err != nil {
					t.Fatal(err)
				}
			case "extra-tail":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[432])
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadEnvironmentCreatePendingIngressAdmission(ctx, cfg, p, release, store, e); err == nil {
				t.Fatal("unproved ingress identity was reusable", mode)
			}
		})
	}
}

func createCompletedIngressFixture(t *testing.T) (deploymentConfig, environmentCreateBootstrapPlan, environmentRelease, secretStore, *environmentCreateExecution, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	cfg, p, release, store, e, identity, identityJournal := createPendingIngressFixture(t)
	m, definitions, err := loadEnvironmentCreatePendingIngressAdmission(context.Background(), cfg, p, release, store, e)
	if err != nil {
		t.Fatal(err)
	}
	secretID := identity.Resources[0].ID
	secret := deploymentplan.Clone(identity.Resources[0].Desired)
	deploymentplan.Map(secret["metadata"])["uid"] = identityJournal.Owners[secretID]
	definitions[0].Retain = true
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}, definitions, []deploymentplan.Object{secret})
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for i, resource := range plan.Resources {
		uid := fmt.Sprintf("%08x-2222-4222-8222-%012x", i+2, i+2)
		if resource.ID == secretID {
			uid = identityJournal.Owners[secretID]
		}
		journal.Owners[resource.ID] = uid
		journal.Completed = append(journal.Completed, resource.ID)
	}
	intent, _ := environmentCreateKubernetesStageIntent(plan)
	intentSHA, _ := environmentCreatePlanSHA256(intent)
	at := time.Now().UTC()
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/ingress-controller", Phase: "ingress-controller", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: intentSHA, Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})})
	raw, err := os.ReadFile(filepath.Join(store.Root, environmentCreateIngressAdmissionMaterialPath(e.Journal.PlanSHA256)))
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries[431].State = "completed"
	e.Journal.Entries[431].CompletedAt = time.Now().UTC()
	e.Journal.Entries[431].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"profile": p.IngressAdmissionProfile, "controller_image": p.Images["LKE_INGRESS_IMAGE"], "secret_uid": identityJournal.Owners[secretID], "controller_uid": journal.Owners["Deployment/"+p.Stack+"-ingress/ingress-nginx-controller"], "webhook_uid": journal.Owners["ValidatingWebhookConfiguration//"+p.Stack+"-ingress-admission"], "ca_sha256": environmentPreflightBytesSHA256([]byte(m.CAPEM)), "certificate_sha256": environmentPreflightBytesSHA256([]byte(m.CertificatePEM)), "private_key_sha256": environmentPreflightBytesSHA256([]byte(m.PrivateKeyPEM)), "material_sha256": environmentPreflightBytesSHA256(raw)})
	for name, value := range map[string]any{"plan.json": plan, "journal.json": journal} {
		if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	return cfg, p, release, store, e, plan, journal
}

func TestCreateIngressCompletedCheckpointReusesOnlyReadback(t *testing.T) {
	for _, mode := range []string{"completed-wrapper", "pending-wrapper"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, release, store, e, _, _ := createCompletedIngressFixture(t)
			if mode == "pending-wrapper" {
				e.Journal.Entries[431].State = "pending"
				e.Journal.Entries[431].Receipt = nil
				e.Journal.Entries[431].CompletedAt = time.Time{}
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(e.JournalPath)
			if _, _, err := loadEnvironmentCreateCompletedIngressAdmission(context.Background(), cfg, p, release, store, e); err != nil {
				t.Fatal("genuine accepted434 controller cannot be read", err)
			}
			if _, _, err := loadEnvironmentCreatePendingIngressAdmission(context.Background(), cfg, p, release, store, e); err == nil {
				t.Fatal("accepted controller was routed back to pending installation")
			}
			after, _ := os.ReadFile(e.JournalPath)
			if !bytes.Equal(before, after) || mode == "pending-wrapper" && e.Journal.Entries[431].State != "pending" {
				t.Fatal("read-only ingress proof fabricated completion or rewrote accepted child")
			}
		})
	}
}

func TestCreateIngressCompletedCheckpointRefusesUnprovedReadback(t *testing.T) {
	for _, mode := range []string{"wrong-receipt", "late-wrapper", "pending-with-receipt", "pending-controller", "unaccepted-controller", "foreign-owner", "extra-checkpoint", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, release, store, e, plan, journal := createCompletedIngressFixture(t)
			ctx := context.Background()
			switch mode {
			case "wrong-receipt":
				e.Journal.Entries[431].Receipt = []byte(`{"foreign_receipt":true}`)
			case "late-wrapper":
				e.Journal.Entries[431].CompletedAt = e.Journal.Entries[433].StartedAt.Add(-time.Second)
			case "pending-with-receipt":
				e.Journal.Entries[431].State = "pending"
				e.Journal.Entries[431].CompletedAt = time.Time{}
			case "pending-controller":
				e.Journal.Entries[433].State = "pending"
			case "unaccepted-controller":
				journal.Accepted = false
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), mustProviderRecoveryJSON(journal), true); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				for id := range journal.Owners {
					journal.Owners[id] = "11111111-1111-4111-8111-111111111111"
				}
				e.Journal.Entries[433].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), mustProviderRecoveryJSON(journal), true); err != nil {
					t.Fatal(err)
				}
			case "extra-checkpoint":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[433])
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadEnvironmentCreateCompletedIngressAdmission(ctx, cfg, p, release, store, e); err == nil {
				t.Fatal("unproved accepted434 checkpoint was reusable", mode)
			}
		})
	}
}
