package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func managedLateOwnersFixture(t *testing.T) (*createManagedLateSessionRecovery, *deploymentplan.Plan) {
	t.Helper()
	old := "11111111-1111-4111-8111-111111111111"
	next := "22222222-2222-4222-8222-222222222222"
	late := &createManagedLateSessionRecovery{OriginalSession: old, OriginalEnrollmentSession: old, NewSessionID: next, SourceCeremony: environmentCreateServiceCeremony{BootstrapSessionID: old}, AcceptedCore: &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}}, Owners: map[string]deploymentplan.Resource{}}
	var live []deploymentplan.Object
	for index, id := range managedLateNativeOwnerIDs("video-cloud-dev") {
		parts := strings.Split(id, "/")
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", index+1)
		container := deploymentplan.Object{"name": "native", "image": "original@sha256:" + strings.Repeat("a", 64), "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old}, deploymentplan.Object{"name": "PRIVATE_IDENTITY_STATE", "value": "/original/private/client.json"}}, "volumeMounts": []any{deploymentplan.Object{"name": "identity", "mountPath": "/original/private"}}}
		pod := deploymentplan.Object{"containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original"}}}}
		if parts[2] == "pki-controller" {
			pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), deploymentplan.Object{"name": "bootstrap-cleanup", "configMap": deploymentplan.Object{"name": "pki-bootstrap-cleanup-binding"}})
		}
		object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": parts[0], "metadata": deploymentplan.Object{"name": parts[2], "namespace": parts[1], "labels": deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": pod}}}
		owner := deploymentplan.Resource{ID: id, Phase: "workload", UID: uid, Desired: object}
		late.Owners[id] = owner
		actual := deploymentplan.Clone(object)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	service := late.SourceCeremony
	service.BootstrapSessionID = next
	defs, err := managedLateSessionOwnerDefinitions(late, service)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedLateSessionOwnerCandidate(late, plan); err != nil {
		t.Fatal(err)
	}
	return late, plan
}

func TestManagedLateOwnersNineUpdatesAndOriginalLogRetain(t *testing.T) {
	late, plan := managedLateOwnersFixture(t)
	updates, retains := 0, 0
	for _, r := range plan.Resources {
		if r.Action == "update" {
			updates++
		}
		if r.Action == "retain" {
			retains++
		}
		field := mqttFoundationEnv(managedNamedField(environmentPKIPod(r.Desired)["containers"], "native"), "PKI_BOOTSTRAP_SESSION_ID")
		want := late.NewSessionID
		if strings.HasSuffix(r.ID, "/video-cloud-logingester") {
			want = late.OriginalSession
		}
		if field != want {
			t.Fatalf("%s SID=%s", r.ID, field)
		}
	}
	if updates != 9 || retains != 1 {
		t.Fatalf("scope update=%d retain=%d", updates, retains)
	}
	for _, test := range []struct {
		name string
		edit func(*deploymentplan.Plan)
	}{
		{"Log premature SID", func(p *deploymentplan.Plan) {
			for i := range p.Resources {
				r := &p.Resources[i]
				if strings.HasSuffix(r.ID, "/video-cloud-logingester") {
					r.Action = "update"
					c := managedNamedField(environmentPKIPod(r.Desired)["containers"], "native")
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": late.NewSessionID}))
				}
			}
		}},
		{"completed owner UID", func(p *deploymentplan.Plan) { p.Resources[0].UID = "99999999-1111-4111-8111-111111111111" }},
		{"image", func(p *deploymentplan.Plan) {
			managedNamedField(environmentPKIPod(p.Resources[0].Desired)["containers"], "native")["image"] = "foreign"
		}},
		{"private state", func(p *deploymentplan.Plan) {
			c := managedNamedField(environmentPKIPod(p.Resources[0].Desired)["containers"], "native")
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PRIVATE_IDENTITY_STATE": "/new/private/client.json"}))
		}},
		{"extra env", func(p *deploymentplan.Plan) {
			c := managedNamedField(environmentPKIPod(p.Resources[0].Desired)["containers"], "native")
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"FOREIGN": "x"}))
		}},
		{"DAG", func(p *deploymentplan.Plan) { p.Resources[0].Dependencies = []string{"foreign"} }},
		{"omit", func(p *deploymentplan.Plan) { p.Resources = p.Resources[:9] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed deploymentplan.Plan
			raw, _ := json.Marshal(plan)
			_ = json.Unmarshal(raw, &changed)
			test.edit(&changed)
			if validateManagedLateSessionOwnerCandidate(late, &changed) == nil {
				t.Fatal("foreign owner delta admitted")
			}
		})
	}
}

func TestManagedLateSourceDigestExcludesOnlyAssignedReview(t *testing.T) {
	late, _ := managedLateOwnersFixture(t)
	late.SourceSHA = strings.Repeat("b", 64)
	before := managedLateSessionSourceDigest(late)
	late.Review = strings.Repeat("c", 64)
	if managedLateSessionSourceDigest(late) != before {
		t.Fatal("fresh review made descriptor circular")
	}
	late.OriginalSession = "33333333-3333-4333-8333-333333333333"
	if managedLateSessionSourceDigest(late) == before {
		t.Fatal("historical session was not bound")
	}
}

func TestManagedLateOriginalExpiryIsOnlyMonotonicBookkeeping(t *testing.T) {
	now := time.Now().UTC()
	old := managedSessionState{Session: "original", Issuer: "issuer", Root: "root", Fingerprint: "leaf", Status: "active", Expires: now.Add(-time.Hour), Subjects: []string{"a", "b"}, Installed: []string{"a"}}
	next := old
	next.Status = "expired"
	if !managedLateOriginalSessionUnchanged(old, next, now) {
		t.Fatal("native genuine expiry rejected")
	}
	for _, test := range []struct {
		name string
		edit func(*managedSessionState)
	}{{"new leaf", func(s *managedSessionState) { s.Fingerprint = "new" }}, {"expiry rewrite", func(s *managedSessionState) { s.Expires = now.Add(time.Hour) }}, {"fake ACK", func(s *managedSessionState) { s.Installed = append(slices.Clone(s.Installed), "b") }}, {"foreign active", func(s *managedSessionState) { s.OtherActive = 1 }}, {"sealed", func(s *managedSessionState) { s.Status = "sealed" }}} {
		t.Run(test.name, func(t *testing.T) {
			changed := next
			test.edit(&changed)
			if managedLateOriginalSessionUnchanged(old, changed, now) {
				t.Fatal("unqualified old-session change admitted")
			}
		})
	}
}

func TestManagedLateNativeAuthorizationRequiresRealInnerReceipts(t *testing.T) {
	late := &createManagedLateSessionRecovery{Review: strings.Repeat("d", 64)}
	late.SeedAuthorization = deploymentplan.Object{"policy_intent": deploymentplan.Object{"environment": "dev", "policy": "new-policy", "policy_sha256": strings.Repeat("e", 64)}, "role_intent": deploymentplan.Object{"role": "pki-service-bootstrap-dev", "namespace": "video-cloud-dev-video-cloud", "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{"new-policy"}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}}
	now := time.Now().UTC()
	outer := environmentCreateJournalEntry{Key: "late-seed-authorization-" + late.Review, Phase: "late-native-authorization", State: "completed", IntentSHA256: activeTransportJournalDigest(late.SeedAuthorization), StartedAt: now, CompletedAt: now.Add(5 * time.Second), Receipt: mustProviderRecoveryJSON(late.SeedAuthorization)}
	j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{outer}}
	for index, kind := range []string{"policy_intent", "role_intent"} {
		key, intent, err := managedLateAuthorizationEntryIntent(late.SeedAuthorization, kind)
		if err != nil {
			t.Fatal(err)
		}
		j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", IntentSHA256: activeTransportJournalDigest(intent), StartedAt: now.Add(time.Duration(index+1) * time.Second), CompletedAt: now.Add(time.Duration(index+2) * time.Second), Receipt: mustProviderRecoveryJSON(intent)})
	}
	if !managedLateSeedAuthorizationCompleted(j, late) {
		t.Fatal("genuine exact wrapper rejected")
	}
	for _, test := range []struct {
		name string
		edit func(*environmentCreateJournal)
	}{{"missing inner", func(j *environmentCreateJournal) { j.Entries = j.Entries[:2] }}, {"future receipt", func(j *environmentCreateJournal) { j.Entries[2].CompletedAt = now.Add(6 * time.Second) }}, {"pending inner", func(j *environmentCreateJournal) { j.Entries[2].State = "pending" }}, {"extra inner", func(j *environmentCreateJournal) { j.Entries = append(j.Entries, j.Entries[1]) }}, {"wrong recipe", func(j *environmentCreateJournal) { j.Entries[2].IntentSHA256 = strings.Repeat("f", 64) }}} {
		t.Run(test.name, func(t *testing.T) {
			copy := j
			copy.Entries = slices.Clone(j.Entries)
			test.edit(&copy)
			if managedLateSeedAuthorizationCompleted(copy, late) {
				t.Fatal("unproved native authorization admitted")
			}
		})
	}
}

func TestManagedLateCredentialsCryptographicallyPreserveKeyAndChain(t *testing.T) {
	root, rootKey, _, now := rootCRLFixture(t)
	rootCert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(20), Subject: pkix.Name{CommonName: "isolated-original-Service-ICA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, rootCert, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ica, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	icaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(21), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(8 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leaf, err := x509.CreateCertificate(rand.Reader, leafTemplate, ica, &key.PublicKey, intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})) + icaPEM + root.Certificate
	late := &createManagedLateSessionRecovery{NewSessionID: "22222222-2222-4222-8222-222222222222", OriginalKeySHA: environmentPreflightBytesSHA256([]byte(keyPEM)), SourceCeremony: environmentCreateServiceCeremony{Root: root, Intermediate: environmentCreateRegisteredIssuer{Certificate: icaPEM}, BootstrapCredentialNamespaces: []string{"video-cloud-dev-video-cloud", "video-cloud-dev-secrets", "video-cloud-dev-account-manager"}}}
	var defs []deploymentplan.Definition
	for _, ns := range late.SourceCeremony.BootstrapCredentialNamespaces {
		d := environmentCreateSecret(ns, "service-bootstrap-"+late.NewSessionID, map[string]string{"tls.crt": chain, "tls.key": keyPEM, "ca.crt": root.Certificate})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "rtk.cloud/bootstrap-session": late.NewSessionID}
		defs = append(defs, d)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedLateCredentialChild(late, plan); err != nil {
		t.Fatal(err)
	}
	duplicated := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})) + icaPEM + icaPEM + root.Certificate
	duplicatePlan := *plan
	duplicatePlan.Resources = slices.Clone(plan.Resources)
	for i := range duplicatePlan.Resources {
		duplicatePlan.Resources[i].Desired = deploymentplan.Clone(plan.Resources[i].Desired)
		deploymentplan.Map(duplicatePlan.Resources[i].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(duplicated))
	}
	if err := validateManagedLateCredentialChild(late, &duplicatePlan); err != nil {
		t.Fatal("selected native producer's identical ICA serialization rejected", err)
	}
	foreignSequence := *plan
	foreignSequence.Resources = slices.Clone(plan.Resources)
	for i := range foreignSequence.Resources {
		foreignSequence.Resources[i].Desired = deploymentplan.Clone(plan.Resources[i].Desired)
		deploymentplan.Map(foreignSequence.Resources[i].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(chain + root.Certificate))
	}
	if err := validateManagedLateCredentialChild(late, &foreignSequence); err == nil {
		t.Fatal("unreviewed duplicate Root serialization accepted")
	}
	for _, test := range []struct {
		name string
		edit func(*deploymentplan.Plan)
	}{{"different key", func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["data"])["tls.key"] = base64.StdEncoding.EncodeToString([]byte("foreign key"))
	}}, {"foreign root", func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["data"])["ca.crt"] = base64.StdEncoding.EncodeToString([]byte("foreign root"))
	}}, {"missing ICA", func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}))
	}}, {"extra credential", func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["data"])["unreviewed"] = base64.StdEncoding.EncodeToString([]byte("other"))
	}}, {"invented UID", func(p *deploymentplan.Plan) { p.Resources[0].UID = "11111111-1111-4111-8111-111111111111" }}, {"different copies", func(p *deploymentplan.Plan) {
		deploymentplan.Map(p.Resources[0].Desired["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(chain + "\n"))
	}}} {
		t.Run(test.name, func(t *testing.T) {
			var changed deploymentplan.Plan
			raw, _ := json.Marshal(plan)
			_ = json.Unmarshal(raw, &changed)
			test.edit(&changed)
			if validateManagedLateCredentialChild(late, &changed) == nil {
				t.Fatal("unqualified credential material admitted")
			}
		})
	}
}

func TestManagedLateAdmissionRequiresAllSixAndOriginalWindow(t *testing.T) {
	source := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("1", 64), OwnerToken: "isolated-owner"}
	late := &createManagedLateSessionRecovery{ParentSHA: source.PlanSHA256, SourceSHA: deploymentplan.Digest(source), SourceOwnerSHA: strings.Repeat("2", 64)}
	now := time.Now().UTC().Add(-time.Minute)
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: managedLateSessionSchema, ParentSHA: late.ParentSHA, ParentJournalSHA: late.SourceSHA, ParentOwnerSHA: late.SourceOwnerSHA, Materials: map[string]string{"accepted-service-core-late-session-source-descriptor": managedLateSessionSourceDigest(late)}}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	late.Review = deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256 = late.Review
	a.Qualification.Decision = "GO"
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: late.Review})
	}
	entry := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + late.Review, Phase: "recovery", State: "completed", StartedAt: now.Add(time.Second), CompletedAt: now.Add(2 * time.Second), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": late.SourceSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review, "audit_sha256": strings.Repeat("3", 64)})}
	if !managedLateAdmissionMatches(a, source, entry, late) {
		t.Fatal("exact six-GO admission rejected")
	}
	for _, test := range []struct {
		name string
		edit func(*createFoundationRecoveryArtifact, *environmentCreateJournalEntry)
	}{{"failed gate", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		a.Qualification.Gates[0].Status = "FAIL"
	}}, {"optional gate", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		a.Qualification.Gates[0].Required = false
	}}, {"foreign gate", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		a.Qualification.Gates[0].ID = "foreign"
	}}, {"expired admission", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		e.StartedAt = a.ValidUntil
	}}, {"before GO", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		e.StartedAt = a.QualifiedAt.Add(-time.Second)
	}}, {"extended window", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		a.ValidUntil = a.ValidUntil.Add(time.Minute)
	}}, {"unverified audit", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) {
		e.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review})
	}}, {"pending admission", func(a *createFoundationRecoveryArtifact, e *environmentCreateJournalEntry) { e.State = "pending" }}} {
		t.Run(test.name, func(t *testing.T) {
			copy := a
			copy.Qualification.Gates = slices.Clone(a.Qualification.Gates)
			changed := entry
			test.edit(&copy, &changed)
			if managedLateAdmissionMatches(copy, source, changed, late) {
				t.Fatal("unqualified source admission admitted")
			}
		})
	}
}
