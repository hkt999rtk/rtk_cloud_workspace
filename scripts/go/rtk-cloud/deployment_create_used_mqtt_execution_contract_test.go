package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newHistoricalUsedMQTTRenewalExecutionContract(t *testing.T, hooks coreExecutionContractHooks, stop string) (*postServiceMQTTExecutionContract, *createServiceSessionRecovery, map[string]*ecdsa.PrivateKey) {
	t.Helper()
	f, identityReview := newHistoricalAcceptedCoreApplicationsWithHooks(t, true, hooks, historicalCompleteDomainPolicy(t))
	// The earlier runtime database setup owns this primary credential;
	// the accepted Core source already contains its two copied owners.
	if f.native.objects["Secret/"+f.service.BootstrapCredentialNamespace+"/pki-verifier-database"] == nil {
		primary := environmentCreateSecret(f.service.BootstrapCredentialNamespace, "pki-verifier-database", map[string]string{"url": "postgres://isolated/verification"})
		f.acceptedHistoricalStage(t, "isolated-original-primary-verifier", []deploymentplan.Definition{primary})
	}
	keys := map[string]*ecdsa.PrivateKey{}
	root := f.trustAuthority(t, "mqtt", 31, false, keys)
	f.installHistoricalAuthorityProtocol(t)
	mqtt := finishHistoricalPostServiceMQTT(t, f, identityReview, &root)
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil || !r.PostServiceResume || !r.PostServiceRenewal || r.RenewalCandidate == nil || len(r.RenewalCandidate.Resources) != 1 || r.MQTTPendingPlan.SHA256() != mqtt.original.SHA256() {
		t.Fatal("full expired original MQTT source did not qualify", err)
	}
	candidate := r.Candidate.SHA256()
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-application-trust-recovery/v1", p.Materials)
	originalOwners := map[string]string{}
	for id, object := range f.native.objects {
		originalOwners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	certificate, session := historicalTransportSeedLeaf(t, f, r.RenewedIntent.SessionID)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
	runtime := f.runtime
	runtime.Backend = f.backend
	runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
	runtime.ServiceSessionRecovery = r
	applyNative := historicalTransportSessionApply(t, f, r, certificate, session)
	interrupted := false
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := applyNative(ctx, stage, defs); err != nil {
			return err
		}
		if stop != "complete" && !interrupted && stage == stop+r.Review {
			interrupted = true
			return context.Canceled
		}
		return nil
	}
	actual, err := executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
	if stop != "complete" {
		if !interrupted || !errors.Is(err, context.Canceled) || f.native.held || r.Candidate.SHA256() != candidate {
			t.Fatal("durable interruption lost the qualified original candidate or lease", err)
		}
		actual, err = executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
	}
	if err != nil {
		t.Fatal("qualified source-to-used-renewal execution failed", err)
	}
	if actual.BootstrapSessionID != session.Session || actual.Root.ID != f.service.Root.ID || actual.Intermediate.ID != f.service.Intermediate.ID || r.Candidate.SHA256() != candidate {
		t.Fatal("used renewal changed permanent authority or MQTT candidate")
	}
	for prefix, size := range map[string]int{"service-session-original-key-renewal-": 1, "service-custody-reader-renewal-": 1, "service-credential-secrets-renewal-": 7, "service-managed-used-renewal-": 15, "application-current-installation-renewal-": 12} {
		plan, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+prefix+r.Review)
		if err != nil || len(plan.Resources) != size || !journal.Accepted {
			t.Fatal("used renewal omitted an accepted native stage", prefix, err)
		}
		if prefix == "application-current-installation-renewal-" {
			jobs := 0
			for _, resource := range plan.Resources {
				if resource.Desired["kind"] != "Job" {
					continue
				}
				jobs++
				container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(resource.Desired)["containers"])[0])
				command := certIssuerObjectList(container["command"])
				secret := ""
				for _, v := range certIssuerObjectList(environmentPKIPod(resource.Desired)["volumes"]) {
					volume := deploymentplan.Map(v)
					if volume["name"] == "bootstrap" {
						secret = deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"])
					}
				}
				if len(command) != 3 || !strings.Contains(deploymentplan.String(command[2]), `grep -q '"current"'`) || !strings.Contains(deploymentplan.String(command[2]), `! grep -q '"pending"'`) || secret != actual.BootstrapCredentialSecret {
					t.Fatal("retained owner enrollment did not require original current identity")
				}
			}
			if jobs != len(environmentCreateApplicationOwners()) {
				t.Fatal("used enrollment lost an original application subject")
			}
		}
	}
	for id, uid := range originalOwners {
		if deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != uid {
			t.Fatal("renewal replaced an original native owner", id)
		}
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	entries := deploymentplan.Digest(f.e.Journal.Entries)
	again, err := executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(actual) || writes != deploymentplan.Digest(f.native.writes) {
		t.Fatal("completed used renewal replayed mutation or changed accepted result", err)
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	journalPath, err := filepath.Rel(f.store.Root, f.e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range before {
		if path != journalPath && after[path] != value {
			t.Fatal("completed retry changed original material", path)
		}
	}
	for path := range after {
		if _, existed := before[path]; !existed && path != "deployment/recovery" && !strings.HasPrefix(path, "deployment/recovery/revalidated-") {
			t.Fatal("completed retry created unrelated evidence", path)
		}
	}
	if deploymentplan.Digest(f.e.Journal.Entries) != entries {
		t.Fatal("completed retry changed original receipts")
	}
	var native map[string]any
	if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native); err != nil || native["policy_writes"] != float64(2) || native["role_writes"] != float64(2) {
		t.Fatal("resumed used renewal repeated exact native policy/role writes", err)
	}
	custody := f.protocol["custody"].(map[string]string)
	if custody["/run/pki-bootstrap/bootstrap.key"] != f.key || custody["/run/pki-bootstrap/bootstrap.csr"] != f.csr || custody[r.RenewedCertPath] != certificate {
		t.Fatal("used renewal replaced original key/CSR custody")
	}
	f.service, f.session, f.certificate = actual, session.Session, certificate
	r.Ceremony = actual
	f.protocol["session"] = map[string]any{"session_id": session.Session, "issuer_id": session.Issuer, "root": session.Root, "fingerprint": session.Fingerprint, "status": session.Status, "subjects": session.Subjects, "installed": session.Installed, "expires": session.Expires, "other_active": session.OtherActive}
	f.publish(t)
	return mqtt, r, keys
}

func TestHistoricalUsedMQTTExecutionContractRenewsQualifiedSourceAndResumes(t *testing.T) {
	for _, stop := range []string{"complete", "service-session-original-key-renewal-", "service-custody-reader-renewal-", "service-credential-secrets-renewal-", "service-managed-used-renewal-", "application-current-installation-renewal-"} {
		t.Run(strings.TrimSuffix(stop, "-"), func(t *testing.T) {
			newHistoricalUsedMQTTRenewalExecutionContract(t, coreExecutionContractHooks{}, stop)
		})
	}
}
