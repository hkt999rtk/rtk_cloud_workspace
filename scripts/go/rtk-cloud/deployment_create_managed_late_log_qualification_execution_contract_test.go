package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Qualifies the native Log boundary with a real Service-signed leaf and its
// original writable claim. Unrelated upstream bootstrap ancestry is separate.
func TestManagedLateLogCandidateQualificationExecutionContract(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate-pod", "foreign-replicaset", "foreign-deployment", "changed-template", "pending-identity", "registry-fingerprint", "wrong-issuer", "missing-claim", "changed-owner-stage", "native-owner-drift"} {
		t.Run(mode, func(t *testing.T) {
			store, e, seed, _ := freshStageFixture(t)
			source, late := managedLateLogNativeContractFixture(t)
			chain := newManagedLateBootstrapChainFixture(t)
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			template := &x509.Certificate{SerialNumber: big.NewInt(901), Subject: pkix.Name{CommonName: logACKSubject}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			der, err := x509.CreateCertificate(rand.Reader, template, chain.icaCertificate, &key.PublicKey, chain.icaKey)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			identity := source.Identity
			identity.Fingerprint = environmentPreflightBytesSHA256(der)
			identity.PublicKeySHA256 = environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo)
			identity.RootSHA256 = chain.ceremony.Root.RootFingerprint
			owner := source.Original
			owner.Desired = deploymentplan.Clone(owner.Desired)
			deploymentplan.Map(deploymentplan.Map(owner.Desired["metadata"])["labels"])["app.kubernetes.io/managed-by"] = "rtk-cloud"
			deploymentplan.Map(owner.Desired["spec"])["replicas"] = float64(1)
			podSpec := environmentPKIPod(owner.Desired)
			container := managedNamedField(podSpec["containers"], "app")
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256": identity.RootSHA256}))
			container["volumeMounts"] = []any{deploymentplan.Object{"name": "state", "mountPath": "/var/lib/video-cloud-log-service"}}
			podSpec["volumes"] = []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-log-state"}}}
			actual := deploymentplan.Clone(owner.Desired)
			deploymentplan.Map(actual["metadata"])["uid"] = owner.UID
			deploymentplan.Map(actual["metadata"])["resourceVersion"] = "original-1"
			parent := environmentCreatePlan{Environment: "dev", Stack: e.Journal.Stack, ConfigurationSHA256: strings.Repeat("a", 64)}
			retained, err := managedLateCompile(parent, "managed-late-session-owners", []deploymentplan.Definition{{Object: owner.Desired, Phase: "workload", Retain: true}}, []deploymentplan.Object{actual})
			if err != nil {
				t.Fatal(err)
			}
			late.ParentSHA = e.Journal.PlanSHA256
			late.SourceSHA = deploymentplan.Digest(e.Journal)
			late.SourceOwnerSHA = deploymentplan.Digest(e.owner)
			late.OriginalKeySHA = strings.Repeat("b", 64)
			late.OriginalCSRSHA = strings.Repeat("c", 64)
			late.OwnerCandidate = retained
			late.AcceptedCore = retained
			late.Owners = map[string]deploymentplan.Resource{owner.ID: owner}
			ceremony := chain.ceremony
			ceremony.BootstrapSessionID = late.NewSessionID
			recovery := &createServiceSessionRecovery{Ceremony: ceremony, RenewalCandidate: seed, ManagedCorePartial: &createManagedCorePartialRecovery{Apps: retained, AppsJournal: deploymentplan.Journal{PlanSHA256: retained.SHA256()}}, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{ServerSHA: strings.Repeat("d", 64)}}}
			recovery.RenewedIntent.Subjects = []string{logACKSubject}
			plan := createFoundationRecoveryPlan{Plan: parent, Materials: map[string]string{"accepted-service-core-managed-application-current-api-servers": strings.Repeat("e", 64)}}
			ledger, err := readEnvironmentCreateLedger(e)
			if err != nil {
				t.Fatal(err)
			}
			ledger.Volumes = []environmentCreateVolumeOwner{{NativeID: "17", PVName: "original-log-pv", PVUID: "44444444-4444-4444-8444-444444444444", PVCName: "original-log-state", PVCUID: "77777777-7777-4777-8777-777777777777", Namespace: e.Journal.Stack + "-video-cloud"}}
			if mode == "missing-claim" {
				ledger.Volumes = []environmentCreateVolumeOwner{}
			}
			if err := saveEnvironmentCreateLedger(t.Context(), e, ledger); err != nil {
				t.Fatal(err)
			}
			ns := e.Journal.Stack + "-video-cloud"
			rsUID := "22222222-2222-4222-8222-222222222222"
			rs := deploymentplan.Object{"kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "original-log-rs", "namespace": ns, "uid": rsUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "video-cloud-logingester", "uid": owner.UID, "controller": true}}}, "spec": deploymentplan.Object{"template": deploymentplan.Map(owner.Desired["spec"])["template"]}}
			pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "original-log-pod", "namespace": ns, "uid": "55555555-5555-4555-8555-555555555555", "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "original-log-rs", "uid": rsUID, "controller": true}}}}
			pods := []any{pod}
			fingerprint := identity.Fingerprint
			switch mode {
			case "duplicate-pod":
				pods = append(pods, deploymentplan.Clone(pod))
			case "foreign-replicaset":
				deploymentplan.Map(rs["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "foreign-deployment":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])[0])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "changed-template":
				rs = deploymentplan.Clone(rs)
				managedNamedField(environmentPKIPod(rs)["containers"], "app")["image"] = "unqualified:latest"
			case "pending-identity":
				identity.Pending = true
			case "registry-fingerprint":
				fingerprint = strings.Repeat("f", 64)
			case "wrong-issuer":
				other := newManagedLateBootstrapChainFixture(t)
				recovery.Ceremony.Intermediate = other.ceremony.Intermediate
			case "changed-owner-stage":
				late.OwnerCandidate.Resources[0].Action = "update"
			case "native-owner-drift":
				deploymentplan.Map(actual["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			}
			query := "SELECT json_build_object('certificate_pem',certificate_pem,'fingerprint',fingerprint) FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:video-cloud-logingester' AND status='succeeded' AND revoked_at IS NULL"
			replies := map[string][]byte{
				"--namespace " + ns + " get pods -l app.kubernetes.io/name=video-cloud-logingester -o json":                                                                                             mustProviderRecoveryJSON(deploymentplan.Object{"items": pods}),
				"get ReplicaSet original-log-rs --namespace " + ns + " --ignore-not-found=true -o json":                                                                                                 mustProviderRecoveryJSON(rs),
				"get Deployment video-cloud-logingester --namespace " + ns + " --ignore-not-found=true -o json":                                                                                         mustProviderRecoveryJSON(actual),
				"--namespace " + ns + " exec original-log-pod --container app -- /app/serviceidentity-bootstrap inspect " + logACKStatePath + " " + logACKSubject + " " + ceremony.Root.RootFingerprint: mustProviderRecoveryJSON(identity),
				lateRegistryContractCommand(e.Journal.Stack, query):                                                                                                                                     mustProviderRecoveryJSON(deploymentplan.Object{"certificate_pem": cert, "fingerprint": fingerprint}),
			}
			backend, _ := lateNativeCommandContractBackend(t, store, replies)
			before := deploymentplan.Digest(e.Journal)
			err = prepareManagedLateLogCandidates(t.Context(), deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}, store, e, backend, &plan, recovery, late)
			if mode != "valid" {
				want := map[string]string{"duplicate-pod": "one original source Pod", "foreign-replicaset": "source original ReplicaSet", "foreign-deployment": "original Log Deployment chain", "changed-template": "original Log source template", "pending-identity": "native inspector proof", "registry-fingerprint": "one original native Log leaf", "wrong-issuer": "original Service signature", "missing-claim": "original host volume custody is absent or changed", "changed-owner-stage": "owner stage retains genuine Log source", "native-owner-drift": "owner stage retains genuine Log source"}[mode]
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatal("changed native source rejection", err)
				}
				if deploymentplan.Digest(e.Journal) != before {
					t.Fatal("refusal changed history")
				}
				return
			}
			if err != nil {
				t.Fatal("native log candidate qualification", err)
			}
			if validateManagedLateLogCandidates(late) != nil || late.LogProducer.Identity.Fingerprint != identity.Fingerprint || late.LogProducer.OriginalClaimUID != ledger.Volumes[0].PVCUID || late.LogProducer.NewSession.Status != "qualified-not-issued" {
				t.Fatal("producer did not preserve original identity and claim")
			}
			for _, name := range []string{"stop", "helper", "retire", "restore"} {
				if !environmentPreflightDigest.MatchString(plan.Materials["accepted-service-core-late-session-log-"+name+"-plan"]) {
					t.Fatal("missing qualified recipe", name)
				}
			}
			if late.LogStopCandidate.Resources[0].UID != owner.UID || late.LogRestoreCandidate.Resources[0].UID != owner.UID || late.LogHelperCandidate.Resources[0].UID != "" || late.LogRetireCandidate.Resources[0].UID != "" || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("qualification allocated new native owner or changed history")
			}
		})
	}
}
