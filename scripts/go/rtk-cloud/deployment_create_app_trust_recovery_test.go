package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestAppTrustCheckpointRequiresCompletedDeviceAndOriginalRoot(t *testing.T) {
	for _, mode := range []string{"original", "unfinished-device", "root-missing", "foreign-pending", "trust-receipt", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			j := environmentCreateJournal{Environment: "dev"}
			for _, key := range []string{"device-root-bootstrap", "kubernetes/device-root-active", "kubernetes/device-root-trust", "pki-reserve-app-root"} {
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(map[string]string{"native": "original"})})
			}
			j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "kubernetes/app-root-trust", State: "pending", IntentSHA256: strings.Repeat("a", 64)})
			switch mode {
			case "unfinished-device":
				j.Entries[0].State = "allocated"
			case "root-missing":
				j.Entries[3].Key = "other-root"
			case "foreign-pending":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending"})
			case "trust-receipt":
				j.Entries[4].Receipt = mustProviderRecoveryJSON(map[string]string{"uncertain": "true"})
			case "duplicate":
				j.Entries = append(j.Entries, j.Entries[4])
			}
			profile, _ := createRecoveryCheckpointProfile(j)
			if isAppTrustCheckpoint(j) != (mode == "original") || (profile == "app-ready-trust-broker-enforcement") != (mode == "original") {
				t.Fatal("unsafe App checkpoint", mode, profile)
			}
		})
	}
}

func TestAppTrustFailureRequiresExactCompletedConfigurationsAndOwnedBroker(t *testing.T) {
	for _, mode := range []string{"original", "uncertain", "api-written", "wrong-uid", "missing-config", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			stack := "video-cloud-dev"
			ns := stack + "-video-cloud"
			var defs []deploymentplan.Definition
			for _, x := range []struct{ k, n, p string }{{"ConfigMap", "pki-create-app-trust", "configuration"}, {"ConfigMap", "pki-create-device-trust", "configuration"}, {"Secret", "factoryenroll-runtime", "configuration"}, {"Secret", "video-cloud-runtime", "configuration"}, {"Secret", "video-cloud-workers-runtime", "configuration"}, {"Deployment", "mqtt-pki", "workload"}, {"Deployment", "video-cloud-api-app-pki", "workload"}} {
				defs = append(defs, deploymentplan.Definition{Phase: x.p, Object: deploymentplan.Object{"apiVersion": "v1", "kind": x.k, "metadata": deploymentplan.Object{"name": x.n, "namespace": ns}}})
			}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			j := deploymentplan.Journal{Failed: "Deployment/" + ns + "/mqtt-pki", Owners: map[string]string{}}
			for i := range p.Resources {
				v := &p.Resources[i]
				if strings.HasSuffix(v.ID, "/video-cloud-api-app-pki") {
					continue
				}
				j.Owners[v.ID] = "uid-" + v.ID
				if v.ID == j.Failed {
					v.Action = "update"
					v.UID = j.Owners[v.ID]
				} else {
					j.Completed = append(j.Completed, v.ID)
				}
			}
			switch mode {
			case "uncertain":
				j.InFlight = j.Failed
			case "api-written":
				j.Owners["Deployment/"+ns+"/video-cloud-api-app-pki"] = "unexpected"
			case "wrong-uid":
				j.Owners[j.Failed] = "foreign"
			case "missing-config":
				j.Completed = j.Completed[:4]
			case "accepted":
				j.Accepted = true
			}
			j.PlanSHA256 = p.SHA256()
			if knownAppTrustFailure(p, j, stack) != (mode == "original") {
				t.Fatal("unsafe App trust outcome", mode)
			}
		})
	}
}

func TestRecoveryTransportLifeRetainsFutureBudgetAndFixedExecutionDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if createRecoveryTransportMinimumLife(ctx, 20*time.Minute, false) != 35*time.Minute {
		t.Fatal("qualification failed to cover the whole future run and proof margin")
	}
	life := createRecoveryTransportMinimumLife(ctx, 20*time.Minute, true)
	if life > 25*time.Minute || life < 25*time.Minute-time.Second {
		t.Fatal("execution reset the budget already spent or lost proof margin", life)
	}
	if createRecoveryTransportMinimumLife(context.Background(), 20*time.Minute, true) != 35*time.Minute {
		t.Fatal("missing deadline shortened required lifetime")
	}
}
