package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagedLateFreshAPIProofCannotCompareCopiedPinToItself(t *testing.T) {
	original := strings.Repeat("a", 64)
	changed := strings.Repeat("b", 64)
	for _, test := range []struct {
		name, fresh, expected string
		pass                  bool
	}{{"genuine fresh", original, original, true}, {"changed native rows", changed, original, false}, {"missing native read", "", original, false}, {"missing original authority", original, "", false}} {
		t.Run(test.name, func(t *testing.T) {
			material := map[string]string{managedLateAPICurrentMaterial: original, managedLateAPINativeMaterial: test.fresh}
			err := bindManagedLateFreshAPIServers(material, test.expected)
			if (err == nil) != test.pass {
				t.Fatal("copied pin substituted for fresh rows", err)
			}
			if test.pass && material[managedLateAPICurrentMaterial] != test.fresh {
				t.Fatal("fresh proof not bound")
			}
		})
	}
}

func TestManagedLateOldEightAllowsOnlyActualNewActiveAuthority(t *testing.T) {
	_, _, service, subjects, now := managedHistoricalSeedFixture(t)
	old := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("c", 64), Status: "active", Expires: now.Add(-time.Hour), Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects[:8])}
	for _, test := range []struct {
		name      string
		newActive bool
		edit      func(*managedSessionState)
		pass      bool
	}{
		{"before mint", false, func(*managedSessionState) {}, true}, {"after accepted mint", true, func(s *managedSessionState) { s.OtherActive = 1; s.Status = "expired" }, true}, {"finalized new no other active", false, func(s *managedSessionState) { s.Status = "expired" }, true}, {"uncaptured active competitor", false, func(s *managedSessionState) { s.OtherActive = 1 }, false}, {"missing expected genuine new", true, func(*managedSessionState) {}, false}, {"changed leaf", false, func(s *managedSessionState) { s.Fingerprint = strings.Repeat("d", 64) }, false}, {"nine old ACKs", false, func(s *managedSessionState) { s.Installed = slices.Clone(subjects) }, false}, {"wrong missing subject", false, func(s *managedSessionState) { s.Installed = append(slices.Clone(subjects[1:8]), subjects[8]) }, false}, {"duplicate ACK", false, func(s *managedSessionState) { s.Installed[7] = s.Installed[6] }, false}, {"old sealed", false, func(s *managedSessionState) { s.Status = "sealed" }, false}, {"expiry changed", false, func(s *managedSessionState) { s.Expires = s.Expires.Add(time.Minute) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := old
			current.Installed = slices.Clone(old.Installed)
			test.edit(&current)
			if managedLateOriginalSessionCurrent(current, old, test.newActive, subjects, now) != test.pass {
				t.Fatal("wrong e0 lifecycle authority")
			}
		})
	}
}

func TestManagedLateCurrentChildrenUseOnlyWrittenPrefix(t *testing.T) {
	late, plan := managedLateOwnersFixture(t)
	// A real compiled plan may include dependency closure; test all actual rows.
	for boundary := 0; boundary < len(plan.Resources); boundary++ {
		for _, mode := range []string{"ordered", "replacement", "unwritten owner", "body drift", "foreign owner", "completion order"} {
			t.Run(mode+"/"+plan.Resources[boundary].ID, func(t *testing.T) {
				owners := map[string]deploymentplan.Resource{}
				ready := map[string]bool{}
				for id, resource := range late.Owners {
					owners[id] = resource
					ready[id] = true
				}
				for id, resource := range late.DependencyOwners {
					owners[id] = resource
					ready[id] = true
				}
				j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{}}
				for index, resource := range plan.Resources {
					if index < boundary {
						j.Completed = append(j.Completed, resource.ID)
						j.Owners[resource.ID] = resource.UID
					}
				}
				written := plan.Resources[boundary]
				j.Owners[written.ID] = written.UID
				var changed deploymentplan.Plan
				raw, _ := json.Marshal(plan)
				_ = json.Unmarshal(raw, &changed)
				pass := mode == "ordered"
				switch mode {
				case "replacement":
					j.Owners[written.ID] = "99999999-9999-4999-8999-999999999999"
				case "unwritten owner":
					if boundary+1 < len(plan.Resources) {
						j.Owners[plan.Resources[boundary+1].ID] = plan.Resources[boundary+1].UID
					} else {
						j.Owners["Deployment/foreign/foreign"] = "99999999-9999-4999-8999-999999999999"
					}
				case "body drift":
					deploymentplan.Map(changed.Resources[boundary].Baseline["spec"])["replicas"] = float64(2)
					j.PlanSHA256 = changed.SHA256()
				case "foreign owner":
					j.Owners["Deployment/foreign/foreign"] = "99999999-9999-4999-8999-999999999999"
				case "completion order":
					if boundary > 0 {
						j.Completed[0] = "foreign"
					} else {
						j.Completed = append(j.Completed, "foreign")
					}
				}
				err := overlayManagedLateCurrentChild(owners, ready, managedLateCurrentChild{Plan: &changed, Journal: j})
				if (err == nil) != pass {
					t.Fatal("incorrect written boundary", err)
				}
				if pass {
					for index, resource := range plan.Resources {
						actual := owners[resource.ID]
						if index <= boundary && deploymentplan.Digest(actual.Desired) != deploymentplan.Digest(resource.Desired) {
							t.Fatal("written owner not actual desired")
						}
						if index > boundary && deploymentplan.Digest(actual.Desired) != deploymentplan.Digest(late.Owners[resource.ID].Desired) {
							t.Fatal("future desired admitted")
						}
					}
					if ready[written.ID] {
						t.Fatal("written pending owner falsely ready")
					}
				}
			})
		}
	}
}

func managedLateFinalizationFixture(t *testing.T) (*createManagedLateSessionRecovery, createManagedLateSeedObservation, managedSessionState, environmentCreateBootstrapFinalization, managedLateCustodyTombstone, []string, time.Time) {
	t.Helper()
	root, key, _, now := rootCRLFixture(t)
	root.Environment = "dev"
	ca, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(808), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(8 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + root.Certificate
	_, _, _, subjects, _ := managedHistoricalSeedFixture(t)
	late := &createManagedLateSessionRecovery{OriginalSession: "11111111-1111-4111-8111-111111111111", NewSessionID: "22222222-2222-4222-8222-222222222222", OriginalKeySHA: strings.Repeat("a", 64), OriginalCSRSHA: strings.Repeat("b", 64), SourceCeremony: environmentCreateServiceCeremony{Root: root, Intermediate: root}}
	state := managedSessionState{Session: late.NewSessionID, Issuer: root.ID, Root: root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leafDER), Status: "sealed", Expires: now.Add(8 * time.Hour), Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects)}
	// The immutable observation is from actual allocation, before any installation.
	allocation := state
	allocation.Status = "active"
	allocation.Installed = nil
	observed := createManagedLateSeedObservation{Session: allocation, CertificatePEM: chain, KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, ObservedAt: now.Add(-time.Second)}
	native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: late.NewSessionID, LeafSHA256: state.Fingerprint, CRLSHA256: strings.Repeat("c", 64), CleanupSHA256: strings.Repeat("d", 64), FinalizedAt: now}
	tombstone := managedLateCustodyTombstone{Schema: "rtk-cloud.bootstrap-credentials/v1", Finalization: native, Root: state.Root, Files: map[string]string{"bootstrap.key": late.OriginalKeySHA, "bootstrap.csr": late.OriginalCSRSHA, "bootstrap.crt": environmentPreflightBytesSHA256([]byte(chain))}}
	return late, observed, state, native, tombstone, subjects, now
}

func TestManagedLateHistoricalCleanupRequiresGenuineSealedNineAndTombstone(t *testing.T) {
	for _, mode := range []string{"sealed genuine", "timezone equivalent", "expired genuine", "active nine", "sealed eight", "competing", "future expired", "changed leaf", "changed session", "changed expiry", "changed CRL", "changed cleanup", "missing finalized time", "future finalized time", "changed root", "changed key tombstone", "changed CSR tombstone", "changed certificate tombstone", "extra tombstone file", "missing subject", "duplicate ACK"} {
		t.Run(mode, func(t *testing.T) {
			late, allocation, state, native, tombstone, subjects, now := managedLateFinalizationFixture(t)
			pass := mode == "sealed genuine" || mode == "expired genuine" || mode == "timezone equivalent"
			switch mode {
			case "timezone equivalent":
				native.FinalizedAt = native.FinalizedAt.In(time.FixedZone("native-db-zero-offset", 0))
			case "expired genuine":
				now = state.Expires.Add(time.Second)
				state.Status = "expired"
			case "active nine":
				state.Status = "active"
			case "sealed eight":
				state.Installed = state.Installed[:8]
			case "competing":
				state.OtherActive = 1
			case "future expired":
				state.Status = "expired"
			case "changed leaf":
				state.Fingerprint = strings.Repeat("e", 64)
			case "changed session":
				state.Session = late.OriginalSession
			case "changed expiry":
				state.Expires = state.Expires.Add(time.Minute)
			case "changed CRL":
				native.CRLSHA256 = strings.Repeat("e", 64)
			case "changed cleanup":
				native.CleanupSHA256 = strings.Repeat("e", 64)
			case "missing finalized time":
				native.FinalizedAt = time.Time{}
			case "future finalized time":
				native.FinalizedAt = now.Add(time.Second)
			case "changed root":
				tombstone.Root = strings.Repeat("e", 64)
			case "changed key tombstone":
				tombstone.Files["bootstrap.key"] = strings.Repeat("e", 64)
			case "changed CSR tombstone":
				tombstone.Files["bootstrap.csr"] = strings.Repeat("e", 64)
			case "changed certificate tombstone":
				tombstone.Files["bootstrap.crt"] = strings.Repeat("e", 64)
			case "extra tombstone file":
				tombstone.Files["extra"] = strings.Repeat("e", 64)
			case "missing subject":
				state.Subjects = state.Subjects[:8]
			case "duplicate ACK":
				state.Installed[8] = state.Installed[7]
			}
			if managedLateFinalizedSessionCurrent(state, allocation, late, subjects, native, tombstone, now) != pass {
				t.Fatal("incorrect native finalization cleanup authority")
			}
		})
	}
}

func TestManagedLateCompletedCopyRetirementRequiresExactReceiptAndNativeAbsence(t *testing.T) {
	for _, mode := range []string{"completed absent", "replacement exists", "read failure", "pending deletion", "wrong UID receipt", "wrong resource receipt", "wrong session intent", "receipt lacks absence", "changed completion order"} {
		t.Run(mode, func(t *testing.T) {
			e, b, _, object := managedApplicationLeaseFixture(t)
			sid := "22222222-2222-4222-8222-222222222222"
			ns := e.Journal.Stack + "-video-cloud"
			uid := "33333333-3333-4333-8333-333333333333"
			service := environmentCreateServiceCeremony{BootstrapSessionID: sid, BootstrapCredentialSecret: "service-bootstrap-" + sid, CredentialUIDs: map[string]string{ns: uid}}
			id := "Secret/" + ns + "/" + service.BootstrapCredentialSecret
			intent := deploymentplan.Object{"resource_id": id, "uid": uid, "session_id": sid}
			receipt := deploymentplan.Object{"resource_id": id, "uid": uid, "absent": true}
			now := time.Now().UTC()
			entry := environmentCreateJournalEntry{Key: "bootstrap-copy-retire-" + ns + "-" + sid, Phase: "bootstrap", State: "completed", StartedAt: now.Add(-time.Second), CompletedAt: now}
			switch mode {
			case "pending deletion":
				entry.State = "pending"
				entry.CompletedAt = time.Time{}
			case "wrong UID receipt":
				receipt["uid"] = "44444444-4444-4444-8444-444444444444"
			case "wrong resource receipt":
				receipt["resource_id"] = "Secret/foreign/foreign"
			case "wrong session intent":
				intent["session_id"] = "11111111-1111-4111-8111-111111111111"
			case "receipt lacks absence":
				receipt["absent"] = false
			case "changed completion order":
				entry.CompletedAt = entry.StartedAt.Add(-time.Second)
			}
			entry.IntentSHA256 = activeTransportJournalDigest(intent)
			entry.Receipt = mustProviderRecoveryJSON(receipt)
			e.Journal.Entries = append(e.Journal.Entries, entry)
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if mode == "replacement exists" {
				if err := os.WriteFile(os.Getenv("RTK_TEST_INTERRUPTED_LEASE_FILE"), mustProviderRecoveryJSON(object), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(os.Getenv("RTK_TEST_INTERRUPTED_LEASE_FILE"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "read failure" {
				t.Setenv("RTK_TEST_INTERRUPTED_LEASE_FAIL", "yes")
			}
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: &createServiceSessionRecovery{ManagedLateSession: &createManagedLateSessionRecovery{}}}
			before := deploymentplan.Digest(e.Journal)
			ok, err := managedLateCompletedCredentialRetirement(context.Background(), deploymentConfig{}, runtime, service, ns)
			if (err == nil && ok) != (mode == "completed absent") {
				t.Fatal("replay or foreign absence authorized retirement", err)
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("completed read-only retirement changed journal")
			}
		})
	}
}

func TestManagedLateOriginalExpiredCopiesPreserveExactKeyLeafAndChain(t *testing.T) {
	root, rootKey, _, now := rootCRLFixture(t)
	rootCert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	icaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	icaDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(401), Subject: pkix.Name{CommonName: "original Service ICA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, NotBefore: now.Add(-12 * time.Hour), NotAfter: now.Add(72 * time.Hour)}, rootCert, &icaKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ica, err := x509.ParseCertificate(icaDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(402), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-9 * time.Hour), NotAfter: now.Add(-time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, ica, &key.PublicKey, icaKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	icaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: icaDER}))
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + icaPEM + root.Certificate
	sid := "11111111-1111-4111-8111-111111111111"
	ns := "video-cloud-dev-video-cloud"
	uid := "33333333-3333-4333-8333-333333333333"
	late := &createManagedLateSessionRecovery{OriginalSession: sid, Renewal: &createManagedSessionRecovery{OriginalSession: sid, OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte(chain))}, OriginalKeySHA: environmentPreflightBytesSHA256(keyPEM), OriginalSessionState: managedSessionState{Fingerprint: environmentPreflightBytesSHA256(leafDER), Expires: leaf.NotAfter}, SourceCeremony: environmentCreateServiceCeremony{Root: root, Intermediate: environmentCreateRegisteredIssuer{Certificate: icaPEM}, CredentialUIDs: map[string]string{ns: uid}}}
	original := deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": "service-bootstrap-" + sid, "namespace": ns, "uid": uid, "labels": deploymentplan.Object{"rtk.cloud/bootstrap-session": sid}}, "immutable": true, "data": deploymentplan.Object{"tls.crt": base64.StdEncoding.EncodeToString([]byte(chain)), "tls.key": base64.StdEncoding.EncodeToString(keyPEM), "ca.crt": base64.StdEncoding.EncodeToString([]byte(root.Certificate))}}
	for _, mode := range []string{"original expired copy", "replacement UID", "mutable", "wrong label", "changed key", "changed leaf bytes", "missing ICA", "wrong root", "extra credential", "pinned repeated original ICA", "unpinned repeated ICA", "pinned reordered chain", "pinned duplicate root", "missing original certificate pin", "wrong original session", "changed original expiry"} {
		t.Run(mode, func(t *testing.T) {
			copyLate := *late
			copyRenewal := *late.Renewal
			copyLate.Renewal = &copyRenewal
			secret := deploymentplan.Clone(original)
			data := deploymentplan.Map(secret["data"])
			meta := deploymentplan.Map(secret["metadata"])
			switch mode {
			case "replacement UID":
				meta["uid"] = "44444444-4444-4444-8444-444444444444"
			case "mutable":
				secret["immutable"] = false
			case "wrong label":
				deploymentplan.Map(meta["labels"])["rtk.cloud/bootstrap-session"] = "22222222-2222-4222-8222-222222222222"
			case "changed key":
				data["tls.key"] = base64.StdEncoding.EncodeToString([]byte("foreign private key"))
			case "changed leaf bytes":
				data["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(icaPEM + root.Certificate))
			case "missing ICA":
				data["tls.crt"] = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
			case "wrong root":
				data["ca.crt"] = base64.StdEncoding.EncodeToString([]byte(icaPEM))
			case "extra credential":
				data["foreign"] = base64.StdEncoding.EncodeToString([]byte("other"))
			case "pinned repeated original ICA", "unpinned repeated ICA":
				duplicate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + icaPEM + icaPEM + root.Certificate
				data["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(duplicate))
				if mode == "pinned repeated original ICA" {
					copyRenewal.OriginalCertificateSHA256 = environmentPreflightBytesSHA256([]byte(duplicate))
				}
			case "pinned reordered chain", "pinned duplicate root":
				changed := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + icaPEM + root.Certificate + icaPEM
				if mode == "pinned duplicate root" {
					changed = chain + root.Certificate
				}
				data["tls.crt"] = base64.StdEncoding.EncodeToString([]byte(changed))
				copyRenewal.OriginalCertificateSHA256 = environmentPreflightBytesSHA256([]byte(changed))
			case "missing original certificate pin":
				copyRenewal.OriginalCertificateSHA256 = ""
			case "wrong original session":
				copyRenewal.OriginalSession = "22222222-2222-4222-8222-222222222222"
			case "changed original expiry":
				copyLate.OriginalSessionState.Expires = copyLate.OriginalSessionState.Expires.Add(time.Minute)
			}
			if (validateManagedLateOriginalCredentialCopy(secret, ns, &copyLate) == nil) != (mode == "original expired copy" || mode == "pinned repeated original ICA") {
				t.Fatal("wrong original copy identity proof")
			}
		})
	}
}
