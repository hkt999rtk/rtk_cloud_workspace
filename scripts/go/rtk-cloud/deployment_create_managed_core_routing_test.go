package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCoreRoutingFixture(t *testing.T) (*createManagedCorePartialRecovery, environmentCreateJournal, []byte, deploymentplan.Object) {
	t.Helper()
	stack, ns := "video-cloud-dev", "video-cloud-dev-video-cloud"
	binding := environmentAppRoutingBinding{Schema: "rtk-cloud.native-app-routing/v1", Environment: "dev", Stack: stack, Mode: "internal-service", CreatePlanSHA256: strings.Repeat("a", 64), DeclarationUID: "11111111-1111-4111-8111-111111111111", ServiceUID: "22222222-2222-4222-8222-222222222222", DeploymentUID: "33333333-3333-4333-8333-333333333333"}
	routing := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped"}}
	desired := environmentAppRoutingDefinition(binding).Object
	deploymentplan.Map(deploymentplan.Map(desired["metadata"])["labels"])["app.kubernetes.io/managed-by"] = "rtk-cloud"
	id := deploymentplan.ID(desired)
	routing.Resources = []deploymentplan.Resource{{ID: id, Phase: "configuration", Action: "create", Desired: desired}}
	routeJournal := deploymentplan.Journal{PlanSHA256: routing.SHA256(), Accepted: true, Completed: []string{id}, Owners: map[string]string{id: binding.DeclarationUID}}
	apps := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: routing.Inputs}
	appsJournal := deploymentplan.Journal{Accepted: true, Owners: map[string]string{}}
	for index := 0; index < 26; index++ {
		appID, uid := fmt.Sprintf("ConfigMap/%s/app-%d", ns, index), fmt.Sprintf("%08d-1111-4111-8111-111111111111", index+4)
		if index == 0 {
			appID, uid = "Service/"+ns+"/video-cloud-api-app-pki", binding.ServiceUID
		}
		if index == 1 {
			appID, uid = "Deployment/"+ns+"/video-cloud-api-app-pki", binding.DeploymentUID
		}
		apps.Resources = append(apps.Resources, deploymentplan.Resource{ID: appID})
		appsJournal.Completed = append(appsJournal.Completed, appID)
		appsJournal.Owners[appID] = uid
	}
	appsJournal.PlanSHA256 = apps.SHA256()
	raw, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	parent := environmentCreateJournal{Environment: "dev", Stack: stack, PlanSHA256: binding.CreatePlanSHA256, Entries: []environmentCreateJournalEntry{{Key: "native-app-routing-profile-proof", Phase: "bootstrap", State: "completed", IntentSHA256: activeTransportJournalDigest(binding), Receipt: mustProviderRecoveryJSON(binding), CompletedAt: time.Date(2026, 10, 13, 20, 30, 0, 0, time.UTC)}}}
	actual := deploymentplan.Clone(desired)
	deploymentplan.Map(actual["metadata"])["uid"] = binding.DeclarationUID
	return &createManagedCorePartialRecovery{Routing: routing, RoutingJournal: routeJournal, Apps: apps, AppsJournal: appsJournal}, parent, raw, actual
}

func TestManagedCoreRoutingPublicationBindsAcceptedAllocationAndNativeOwner(t *testing.T) {
	modes := []string{"original", "native-api-metadata", "unknown-json", "malformed-json", "foreign-environment", "foreign-mode", "foreign-parent", "foreign-service", "foreign-deployment", "foreign-declaration", "route-not-accepted", "apps-not-accepted", "route-owner", "apps-owner", "route-completed", "route-extra", "desired-binding", "missing-parent-proof", "duplicate-parent-proof", "pending-parent-proof", "parent-intent", "parent-receipt", "parent-time", "parent-phase", "native-replacement", "native-deleting", "native-binding", "native-mutable", "native-foreign-name"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			core, parent, raw, actual := managedCoreRoutingFixture(t)
			var binding environmentAppRoutingBinding
			if json.Unmarshal(raw, &binding) != nil {
				t.Fatal("fixture")
			}
			pass := mode == "original" || mode == "native-api-metadata"
			switch mode {
			case "native-api-metadata":
				deploymentplan.Map(actual["metadata"])["resourceVersion"] = "73"
			case "unknown-json":
				raw = append(raw[:len(raw)-1], []byte(`,"foreign":"identity"}`)...)
			case "malformed-json":
				raw = []byte("invalid")
			case "foreign-environment":
				binding.Environment = "staging"
			case "foreign-mode":
				binding.Mode = "ingress"
			case "foreign-parent":
				binding.CreatePlanSHA256 = strings.Repeat("b", 64)
			case "foreign-service":
				binding.ServiceUID = "44444444-4444-4444-8444-444444444444"
			case "foreign-deployment":
				binding.DeploymentUID = "44444444-4444-4444-8444-444444444444"
			case "foreign-declaration":
				binding.DeclarationUID = "44444444-4444-4444-8444-444444444444"
			case "route-not-accepted":
				core.RoutingJournal.Accepted = false
			case "apps-not-accepted":
				core.AppsJournal.Accepted = false
			case "route-owner":
				core.RoutingJournal.Owners[core.Routing.Resources[0].ID] = "44444444-4444-4444-8444-444444444444"
			case "apps-owner":
				core.AppsJournal.Owners["Service/"+parent.Stack+"-video-cloud/video-cloud-api-app-pki"] = "44444444-4444-4444-8444-444444444444"
			case "route-completed":
				core.RoutingJournal.Completed[0] = "foreign"
			case "route-extra":
				core.Routing.Resources = append(core.Routing.Resources, core.Routing.Resources[0])
			case "desired-binding":
				deploymentplan.Map(core.Routing.Resources[0].Desired["data"])["binding.json"] = "foreign"
				core.RoutingJournal.PlanSHA256 = core.Routing.SHA256()
			case "missing-parent-proof":
				parent.Entries = nil
			case "duplicate-parent-proof":
				parent.Entries = append(parent.Entries, parent.Entries[0])
			case "pending-parent-proof":
				parent.Entries[0].State = "pending"
			case "parent-intent":
				parent.Entries[0].IntentSHA256 = environmentPreflightBytesSHA256(raw)
			case "parent-receipt":
				parent.Entries[0].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"foreign": "proof"})
			case "parent-time":
				parent.Entries[0].CompletedAt = time.Time{}
			case "parent-phase":
				parent.Entries[0].Phase = "foreign"
			case "native-replacement":
				deploymentplan.Map(actual["metadata"])["uid"] = "44444444-4444-4444-8444-444444444444"
			case "native-deleting":
				deploymentplan.Map(actual["metadata"])["deletionTimestamp"] = "2026-10-13T21:00:00Z"
			case "native-binding":
				deploymentplan.Map(actual["data"])["binding.json"] = "foreign"
			case "native-mutable":
				actual["immutable"] = false
			case "native-foreign-name":
				deploymentplan.Map(actual["metadata"])["name"] = "foreign"
			}
			if strings.HasPrefix(mode, "foreign-") {
				raw = mustProviderRecoveryJSON(binding)
			}
			before := deploymentplan.Digest(core)
			proof, err := validateManagedCoreRoutingPublication(core, parent, raw, actual)
			if (err == nil) != pass {
				t.Fatal("wrong exact routing allowance", mode, err)
			}
			if before != deploymentplan.Digest(core) {
				t.Fatal("routing observation rewrote history")
			}
			if pass && (proof.FileSHA != environmentPreflightBytesSHA256(raw) || proof.FileSHA == activeTransportJournalDigest(binding) || proof.ParentProofSHA != deploymentplan.Digest(parent.Entries[0]) || proof.NativeSHA != deploymentplan.Digest(deploymentplan.Projection(actual))) {
				t.Fatal("lost separate file, parent, or native pins")
			}
		})
	}
}

func TestManagedCapabilityLineageRoutingFileIsExactAndSeparate(t *testing.T) {
	modes := []string{"accepted-routing", "missing-allowance", "invalid-hash", "changed-file", "missing-file", "unknown-app-file", "unknown-device-file", "routing-symlink", "routing-directory", "routing-permissions", "missing-issuer-pin", "missing-root-owner", "extra-routing-argument"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			store, _, roots, _ := managedPublicationJournalFixture(t)
			verifiedRoots, custody, publications := map[string]bool{}, map[string]bool{"roots": true}, map[string]string{}
			for _, domain := range managedPKIDomains {
				relative := "pki/" + domain + "/issuer.json"
				raw, err := store.read(relative)
				if err != nil {
					t.Fatal(err)
				}
				publications[relative] = environmentPreflightBytesSHA256([]byte(raw))
				if domain != "device" {
					verifiedRoots[roots[domain].ID] = true
					custody[domain+"-root-v1-key-passphrase"] = true
				}
			}
			_, _, raw, _ := managedCoreRoutingFixture(t)
			relative, path := "pki/app/routing.json", filepath.Join(store.Root, "pki/app/routing.json")
			if err := store.write(relative, raw, false); err != nil {
				t.Fatal(err)
			}
			routing := []string{environmentPreflightBytesSHA256(raw)}
			switch mode {
			case "missing-allowance":
				routing = nil
			case "invalid-hash":
				routing[0] = "unknown"
			case "changed-file":
				if err := store.write(relative, []byte(`{"changed":"binding"}`), true); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unknown-app-file", "unknown-device-file":
				domain := "app"
				if mode == "unknown-device-file" {
					domain = "device"
				}
				if err := store.write("pki/"+domain+"/unverified.json", []byte("unverified"), false); err != nil {
					t.Fatal(err)
				}
			case "routing-symlink":
				outside := filepath.Join(t.TempDir(), "routing.json")
				if err := os.WriteFile(outside, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "routing-directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "routing-permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-issuer-pin":
				delete(publications, "pki/device/issuer.json")
			case "missing-root-owner":
				delete(verifiedRoots, roots["service"].ID)
			case "extra-routing-argument":
				routing = append(routing, routing[0])
			}
			before := deploymentplan.Digest(publications)
			err := qualifyEnvironmentCreateBootstrapLineage(store, verifiedRoots, custody, publications, routing...)
			if (err == nil) != (mode == "accepted-routing") {
				t.Fatal("unsafe routing directory allowance", mode, err)
			}
			if deploymentplan.Digest(publications) != before {
				t.Fatal("changed five issuer pins")
			}
		})
	}
}
