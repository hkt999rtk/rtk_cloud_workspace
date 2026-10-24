package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Real signing and the maintained five-object compiler provide the authority
// under test. All allocations below are isolated synthetic fixture UUIDs.
func managedLateAcceptedCleanupFixture(t *testing.T) (*createManagedLateSessionRecovery, map[string]managedLateCurrentChild) {
	t.Helper()
	late, service, cleanup, _ := managedLateCleanupFixture(t)
	late.Review = strings.Repeat("e", 64)
	late.SignerPolicySHA = service.CleanupBinding.PolicySHA256
	late.SourceCeremony.BootstrapCredentialNamespace = "video-cloud-dev-video-cloud"
	late.SourceCeremony.BootstrapCredentialNamespaces = []string{"video-cloud-dev-video-cloud", "video-cloud-dev-secrets", "video-cloud-dev-account-manager"}
	f := newManagedLateBootstrapChainFixture(t)
	late.SourceCeremony.Root = f.ceremony.Root
	late.SourceCeremony.Intermediate = f.ceremony.Intermediate
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(711), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"},
		NotBefore: f.now.Add(-time.Minute), NotAfter: f.now.Add(8 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, f.icaCertificate, &key.PublicKey, f.icaKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})) + late.SourceCeremony.Intermediate.Certificate + late.SourceCeremony.Root.Certificate
	late.OriginalKeySHA = environmentPreflightBytesSHA256([]byte(keyPEM))
	var defs []deploymentplan.Definition
	for _, ns := range late.SourceCeremony.BootstrapCredentialNamespaces {
		d := environmentCreateSecret(ns, "service-bootstrap-"+late.NewSessionID, map[string]string{"tls.crt": chain, "tls.key": keyPEM, "ca.crt": late.SourceCeremony.Root.Certificate})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "rtk.cloud/bootstrap-session": late.NewSessionID}
		defs = append(defs, d)
	}
	credentials, err := deploymentplan.Compile(cleanup.Inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-5 * time.Minute)
	credentialChild := managedLateCleanupTestChild(t, credentials, "kubernetes/service-credential-secrets-renewal-"+late.Review, now, now.Add(time.Minute))
	primary := "Secret/video-cloud-dev-video-cloud/service-bootstrap-" + late.NewSessionID
	credentialChild.Journal.Owners[primary] = service.CleanupBinding.SecretUID
	managedLateCleanupTestBindChild(t, &credentialChild)
	cleanupChild := managedLateCleanupTestChild(t, cleanup, "kubernetes/service-cleanup-binding-renewal-"+late.Review, now.Add(2*time.Minute), now.Add(3*time.Minute))
	return late, map[string]managedLateCurrentChild{credentialChild.Entry.Key: credentialChild, cleanupChild.Entry.Key: cleanupChild}
}

func managedLateCleanupTestBindChild(t *testing.T, child *managedLateCurrentChild) {
	t.Helper()
	child.Journal.PlanSHA256 = child.Plan.SHA256()
	intent, err := environmentCreateKubernetesStageIntent(child.Plan)
	if err != nil {
		t.Fatal(err)
	}
	child.Entry.IntentSHA256 = activeTransportJournalDigest(jsonRawObject(intent))
	child.Entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": child.Plan.SHA256(), "owners": child.Journal.Owners})
}

func managedLateCleanupTestChild(t *testing.T, plan *deploymentplan.Plan, key string, start, finish time.Time) managedLateCurrentChild {
	t.Helper()
	child := managedLateCurrentChild{Plan: plan, Journal: deploymentplan.Journal{Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}, Entry: environmentCreateJournalEntry{Key: key, Phase: strings.TrimPrefix(key, "kubernetes/"), State: "completed", StartedAt: start, CompletedAt: finish}}
	for i, r := range plan.Resources {
		uid := r.UID
		if uid == "" {
			uid = fmt.Sprintf("%08d-7777-4777-8777-777777777777", i+1)
		}
		child.Journal.Owners[r.ID] = uid
		child.Journal.Completed = append(child.Journal.Completed, r.ID)
	}
	managedLateCleanupTestBindChild(t, &child)
	return child
}

func TestManagedLateAcceptedCleanupProjectsOriginalRBACWithoutCreatedCM(t *testing.T) {
	late, children := managedLateAcceptedCleanupFixture(t)
	before := deploymentplan.Digest(late)
	accepted, err := managedLateAcceptedCleanupRBAC(late, children)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 4 {
		t.Fatalf("expected four original owners, got %d", len(accepted))
	}
	roleID := "Role/video-cloud-dev-video-cloud/bootstrap-cleanup"
	for id, r := range accepted {
		if strings.HasPrefix(id, "ConfigMap/") || r.UID != late.AcceptedCoreJournal.Owners[id] {
			t.Fatal("created or replacement owner projected")
		}
		if id == roleID {
			want := []any{"service-bootstrap-" + late.NewSessionID}
			if deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(r.Desired["rules"])[0])["resourceNames"]) != deploymentplan.Digest(want) {
				t.Fatal("new allocated SID permission not projected")
			}
		} else {
			for _, original := range late.AcceptedCore.Resources {
				if original.ID == id && deploymentplan.Digest(r.Desired) != deploymentplan.Digest(original.Desired) {
					t.Fatal("unrelated retained RBAC changed")
				}
			}
		}
	}
	definitions, err := managedLateAcceptedCoreDefinitions(late, accepted)
	if err != nil {
		t.Fatal(err)
	}
	var role deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == roleID {
			role = d.Object
		}
	}
	if role == nil || deploymentplan.Digest(role) != deploymentplan.Digest(accepted[roleID].Desired) {
		t.Fatal("final core reverted the accepted Role")
	}
	deploymentplan.Map(certIssuerObjectList(role["rules"])[0])["resourceNames"] = []any{"mutated result"}
	if deploymentplan.Digest(late) != before {
		t.Fatal("original accepted source was mutated")
	}
	if deploymentplan.Digest(accepted[roleID].Desired) == deploymentplan.Digest(role) {
		t.Fatal("final definitions alias accepted proof")
	}
	copy := accepted[roleID]
	copy.UID = "88888888-8888-4888-8888-888888888888"
	accepted[roleID] = copy
	if _, err := managedLateAcceptedCoreDefinitions(late, accepted); err == nil {
		t.Fatal("replacement Role UID admitted by final core")
	}
}

func TestManagedLateCurrentOwnersConsumesAcceptedCleanupBeforeOwnerChild(t *testing.T) {
	late, children := managedLateAcceptedCleanupFixture(t)
	empty := &deploymentplan.Plan{Inputs: late.AcceptedCore.Inputs}
	j := deploymentplan.Journal{PlanSHA256: empty.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	r := &createServiceSessionRecovery{ManagedCorePartial: &createManagedCorePartialRecovery{Apps: empty, AppsJournal: j, Routing: empty, RoutingJournal: j}}
	roleID := "Role/video-cloud-dev-video-cloud/bootstrap-cleanup"
	owners, ready, err := managedLateCurrentOwners(r, late, children)
	if err != nil || !ready[roleID] {
		t.Fatal("accepted Role unavailable", err)
	}
	accepted := owners[roleID]
	plan := &deploymentplan.Plan{Inputs: late.AcceptedCore.Inputs, Resources: []deploymentplan.Resource{accepted}}
	plan.Resources[0].Baseline = deploymentplan.Clone(accepted.Desired)
	plan.Resources[0].Action = "retain"
	key := "kubernetes/managed-late-session-owners-" + late.Review
	children[key] = managedLateCleanupTestChild(t, plan, key, time.Now().Add(-time.Minute), time.Now().Add(-30*time.Second))
	if _, _, err := managedLateCurrentOwners(r, late, children); err != nil {
		t.Fatal("same UID accepted cleanup dependency refused", err)
	}
	plan.Resources[0].Baseline = deploymentplan.Clone(accepted.Baseline)
	stale := children[key]
	managedLateCleanupTestBindChild(t, &stale)
	children[key] = stale
	if _, _, err := managedLateCurrentOwners(r, late, children); err == nil {
		t.Fatal("stale e0 dependency rebaselined after genuine cleanup")
	}
}

func TestManagedLateAcceptedCleanupRejectsIncompleteForeignAndPrivilegeProofs(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*createManagedLateSessionRecovery, map[string]managedLateCurrentChild)
	}{
		{"unaccepted original core", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			l.AcceptedCoreJournal.Accepted = false
		}},
		{"missing credential", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			delete(c, "kubernetes/service-credential-secrets-renewal-"+l.Review)
		}},
		{"pending credential", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-credential-secrets-renewal-" + l.Review
			v := c[k]
			v.Entry.State = "pending"
			c[k] = v
		}},
		{"unaccepted cleanup", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Journal.Accepted = false
			c[k] = v
		}},
		{"acceptance not begun", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Journal.AcceptanceStarted = false
			c[k] = v
		}},
		{"failed cleanup", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Journal.Failed = v.Plan.Resources[0].ID
			c[k] = v
		}},
		{"zero completed time", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Entry.CompletedAt = time.Time{}
			c[k] = v
		}},
		{"cleanup before allocation", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Entry.StartedAt = v.Entry.StartedAt.Add(-3 * time.Minute)
			c[k] = v
		}},
		{"wrong parent intent", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Entry.IntentSHA256 = strings.Repeat("f", 64)
			c[k] = v
		}},
		{"wrong parent receipt", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			v.Entry.Receipt = json.RawMessage(`{"plan_sha256":"foreign","owners":{}}`)
			c[k] = v
		}},
		{"reordered completed child", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			slices.Reverse(v.Journal.Completed)
			c[k] = v
		}},
		{"wrong allocated Secret", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-credential-secrets-renewal-" + l.Review
			v := c[k]
			v.Journal.Owners["Secret/video-cloud-dev-video-cloud/service-bootstrap-"+l.NewSessionID] = "88888888-8888-4888-8888-888888888888"
			managedLateCleanupTestBindChild(t, &v)
			c[k] = v
		}},
		{"foreign original UID", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			for i := range l.AcceptedCore.Resources {
				if l.AcceptedCore.Resources[i].Desired["kind"] == "Role" {
					l.AcceptedCore.Resources[i].UID = "88888888-8888-4888-8888-888888888888"
				}
			}
		}},
		{"broader privileges with consistent child hashes", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			for i := range v.Plan.Resources {
				if v.Plan.Resources[i].Desired["kind"] == "Role" {
					deploymentplan.Map(certIssuerObjectList(v.Plan.Resources[i].Desired["rules"])[0])["verbs"] = []any{"*"}
				}
			}
			managedLateCleanupTestBindChild(t, &v)
			c[k] = v
		}},
		{"changed source baseline with consistent hashes", func(l *createManagedLateSessionRecovery, c map[string]managedLateCurrentChild) {
			k := "kubernetes/service-cleanup-binding-renewal-" + l.Review
			v := c[k]
			for i := range v.Plan.Resources {
				if v.Plan.Resources[i].Desired["kind"] == "Role" {
					deploymentplan.Map(certIssuerObjectList(v.Plan.Resources[i].Baseline["rules"])[0])["resourceNames"] = []any{"foreign"}
				}
			}
			managedLateCleanupTestBindChild(t, &v)
			c[k] = v
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			late, children := managedLateAcceptedCleanupFixture(t)
			test.edit(late, children)
			if _, err := managedLateAcceptedCleanupRBAC(late, children); err == nil {
				t.Fatal("unqualified cleanup authority admitted")
			}
		})
	}
}

func TestManagedLateAcceptedCleanupAbsentPreservesOriginalAuthority(t *testing.T) {
	late, children := managedLateAcceptedCleanupFixture(t)
	delete(children, "kubernetes/service-cleanup-binding-renewal-"+late.Review)
	if owners, err := managedLateAcceptedCleanupRBAC(late, children); err != nil || len(owners) != 0 {
		t.Fatal("missing cleanup invented authority", err)
	}
}
