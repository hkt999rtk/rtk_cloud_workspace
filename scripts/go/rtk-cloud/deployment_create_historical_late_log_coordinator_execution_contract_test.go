package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is a stage/coordinator contract with explicit synthetic historical
// admission. Original signed identities, native process outcomes, all four
// accepted children and durable completion authentication are real producers;
// the upstream late-source profile and public six-gate preflight are separate.
func TestHistoricalLateLogCoordinatorResumesAcceptedStopAndAuthenticatesDurableCompletion(t *testing.T) {
	f, late, apply, backend := newHistoricalLateLogNativeContract(t)
	clients := slices.Clone(f.protocol["historical_clients"].([]managedSessionClient))
	keys := f.protocol["historical_client_keys"].(map[string]string)
	subjects := late.LogProducer.NewSession.Subjects
	for _, subject := range subjects {
		if subject == logACKSubject {
			continue
		}
		count := 1
		if subject == "service:video-cloud-api" {
			count = 3
		}
		for i := 0; i < count; i++ {
			certificate, key, csr := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, subject, nil)
			leaf, err := parseFirstCertificatePEM([]byte(certificate))
			if err != nil {
				t.Fatal(err)
			}
			request := fmt.Sprintf("%s:original-coordinator-client:%d", subject, i)
			clients = append(clients, managedSessionClient{Subject: subject, Caller: subject, Request: request, Issuer: f.service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "succeeded", CSR: csr, Certificate: certificate})
			keys[request] = key
		}
	}
	slices.SortFunc(clients, func(a, b managedSessionClient) int {
		return strings.Compare(a.Subject+"\x00"+a.Caller+"\x00"+a.Request, b.Subject+"\x00"+b.Caller+"\x00"+b.Request)
	})
	if len(clients) != 11 || !validateManagedSessionClients(clients, f.service, subjects, time.Now().UTC()) {
		t.Fatal("original eleven native signed clients are incomplete")
	}
	installHistoricalTransportProtocol(t, f, historicalTransportIdentityObservations{Clients: clients, ClientKeys: keys})
	ledger := historicalTransportCaptureVolumes(t, f)
	claim, err := managedLateLogClaim(late.LogProducer.Original.Desired, ledger)
	if err != nil || claim.PVCUID != late.LogProducer.OriginalClaimUID {
		t.Fatal("actual captured original Log claim owner", err)
	}
	late.LogProducer.OriginalVolumesSHA = deploymentplan.Digest(ledger.Volumes)
	late.OriginalClientsSHA = deploymentplan.Digest(clients)
	late.LogProducer.OriginalClientsSHA, late.LogProducer.FreshReview = late.OriginalClientsSHA, ""
	if err := validateManagedLateLogCandidates(late); err != nil {
		t.Fatal("client binding changed the exact original lifecycle recipes", err)
	}
	late.Review, _ = f.admitRecovery(t, "rtk-cloud.create-managed-late-session-recovery/v1", map[string]string{
		"accepted-service-core-late-session-log-producer":       deploymentplan.Digest(late.LogProducer),
		"accepted-service-core-late-session-log-helper-binding": late.HelperBindingSHA,
	})
	installHistoricalLateLogRegistryProjection(t, f)
	backend.publish(t)
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend, ServiceSessionRecovery: &createServiceSessionRecovery{Ceremony: f.service, ManagedLateSession: late}}
	privatePath := f.protocol["historical_log_identity"].(map[string]string)["path"]
	privateBefore := readTestFile(t, privatePath)
	clientsBefore := deploymentplan.Digest([]any{clients, keys})
	nativeBefore := deploymentplan.Digest(f.native.objects)
	storeBefore := createBootstrapStateSnapshot(t, f.store.Root)
	fenceCalls := 0
	check := func(ctx context.Context) error {
		fenceCalls++
		admitted, _, err := loadAdmittedCreateRecovery(f.store, f.e, late.Review)
		if err != nil {
			return err
		}
		if admitted.Plan.Materials["accepted-service-core-late-session-log-producer"] != deploymentplan.Digest(late.LogProducer) {
			return errors.New("original admitted producer changed")
		}
		return f.e.Check(ctx)
	}
	if proof, err := executeManagedLateLogACK(t.Context(), f.cfg, runtime, late, apply, func(context.Context) error { return errors.New("external source fence unavailable") }); err == nil || proof != nil {
		t.Fatal("unqualified mutation boundary proceeded")
	}
	if deploymentplan.Digest(f.native.objects) != nativeBefore {
		t.Fatal("failed source fence changed original native owner")
	}
	assertRecoveryContractReadOnly(t, f.store, storeBefore)
	// Simulate process loss only after the actual stop transaction has accepted.
	// A retry must authenticate and reuse that child, without another stop write.
	interrupted := errors.New("process interrupted after accepted original stop")
	interruptedApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "managed-late-log-stop-"+late.Review {
			return interrupted
		}
		return nil
	}
	if proof, err := executeManagedLateLogACK(t.Context(), f.cfg, runtime, late, interruptedApply, check); !errors.Is(err, interrupted) || proof != nil {
		t.Fatal("accepted-stop interruption not preserved", err)
	}
	_, stopJournal, err := managedLateLogAccepted(f.store, f.e, late, "stop", "")
	if err != nil || envObjectReplicas(f.native.objects[late.LogProducer.Original.ID]) != 0 || backend.attempted {
		t.Fatal("interruption was not exactly after accepted stop", err)
	}
	stopWrites := f.native.writes[late.LogProducer.Original.ID]
	proof, err := executeManagedLateLogACK(t.Context(), f.cfg, runtime, late, apply, check)
	if err != nil {
		t.Fatal("complete actual Log coordinator", err)
	}
	if proof.StopJournalSHA != deploymentplan.Digest(stopJournal) || f.native.writes[late.LogProducer.Original.ID] != stopWrites+1 || proof.HelperJobUID == "" || proof.HelperPodUID == "" || fenceCalls < 5 {
		t.Fatal("completion replayed stop or omitted exact native lifecycle", fenceCalls)
	}
	if f.native.objects[late.LogHelperCandidate.Resources[0].ID] != nil || backend.job != nil || envObjectReplicas(f.native.objects[late.LogProducer.Original.ID]) != 1 {
		t.Fatal("accepted completion left helper or lost original business owner")
	}
	if readTestFile(t, privatePath) != privateBefore || deploymentplan.Digest([]any{f.protocol["historical_clients"], f.protocol["historical_client_keys"]}) != clientsBefore {
		t.Fatal("completion changed original client identity bytes")
	}
	state := f.protocol["historical_sessions"].(map[string]managedSessionState)[late.NewSessionID]
	if !managedLateLogExactSet(state.Installed, subjects) || proof.NewSessionID != state.Session || proof.NativeClientsSHA != late.OriginalClientsSHA {
		t.Fatal("completion did not preserve exact nine real installed subjects")
	}
	completedStore := createBootstrapStateSnapshot(t, f.store.Root)
	completedNative := deploymentplan.Digest(f.native.objects)
	t.Run("durable-retry-needs-no-native-access", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "unexpected-native-call")
		command := "#!/bin/sh\nprintf attempted > '" + marker + "'\nexit 97\n"
		bin := filepath.Join(dir, "kubectl")
		if err := os.WriteFile(bin, []byte(command), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RTK_CLOUD_KUBECTL", bin)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		replay, err := executeManagedLateLogACK(t.Context(), f.cfg, runtime, late, func(context.Context, string, []deploymentplan.Definition) error {
			t.Fatal("durable completion replayed a mutation")
			return nil
		}, func(context.Context) error { t.Fatal("durable completion requested a runtime fence"); return nil })
		if err != nil || deploymentplan.Digest(replay) != deploymentplan.Digest(proof) {
			t.Fatal("durable completion retry", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("durable-only reader accessed native API")
		}
	})
	assertRecoveryContractReadOnly(t, f.store, completedStore)
	if deploymentplan.Digest(f.native.objects) != completedNative {
		t.Fatal("durable retry changed native state")
	}
	for _, name := range []string{"managed-late-log-proof.json", "managed-late-log-observation.json", "managed-late-log-final-session.json", "qualification.json"} {
		t.Run("reject-tampered-"+name, func(t *testing.T) {
			path := managedLateLogFile(f.store, late.Review, name)
			original := readTestFile(t, path)
			if err := os.WriteFile(path, []byte(`{"schema":"foreign-proof"}`), 0600); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(path, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}()
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if result, err := readManagedLateLogCompletion(f.store, f.e, late, time.Now().UTC()); err == nil || result != nil {
				t.Fatal("tampered durable authority accepted")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
	assertRecoveryContractReadOnly(t, f.store, completedStore)
}

// Read-only SQL projects the actual isolated issued rows. It does not produce
// installed ACKs: only the original native Log process adapter can add the ninth.
func installHistoricalLateLogRegistryProjection(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import datetime,json,os,sys
original=%q
a=sys.argv[1:];joined=' '.join(a)
if 'FROM pki_deployment_bootstrap_sessions' in joined or 'FROM pki_service_client_issuances' in joined:
 assert 'PGOPTIONS=-c default_transaction_read_only=on' in a and a[-2]=='registry-read'
 query=a[-1]
 assert query.startswith('SELECT ') and ';' not in query and '\n' not in query
 assert "WHERE environment='"+%q+"'" in query
 assert "issuer_id='" not in query and "root_sha256='" not in query
 assert '--namespace' in a and a[a.index('--namespace')+1]==%q
 assert 'exec' in a and a[a.index('exec')+1]=='postgresql-0'
 assert '--container' in a and a[a.index('--container')+1]=='postgres'
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
 if 'FROM pki_deployment_bootstrap_sessions' in joined:
  marker="AND session_id='";sid=joined.split(marker,1)[1].split("'",1)[0]
  state=dict(d['historical_sessions'][sid]);now=datetime.datetime.now(datetime.timezone.utc)
  if state['status']=='active' and datetime.datetime.fromisoformat(state['expires_at'].replace('Z','+00:00'))<=now:state['status']='expired'
  state['other_active']=sum(1 for key,value in d['historical_sessions'].items() if key!=sid and value['status']=='active' and datetime.datetime.fromisoformat(value['expires_at'].replace('Z','+00:00'))>now)
  print(json.dumps(state))
 else:
  assert "subject<>'service:deployment-bootstrap'" in joined
  print(json.dumps(sorted(d['historical_clients'],key=lambda x:(x['subject'],x['caller'],x['request_id']))))
 sys.exit(0)
os.execv(original,[original]+a)
`, original, f.cfg.Environment, f.p.Plan.Stack+"-platform")
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
