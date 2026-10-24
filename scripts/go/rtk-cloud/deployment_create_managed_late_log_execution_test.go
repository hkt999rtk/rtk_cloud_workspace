package main

// PRIVATE PURE TEST DRAFT. No environment store, native commands or allocations.

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

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateLogNeverRestoresWithAnyPrivateClaimHolder(t *testing.T) {
	for _, terminating := range []bool{false, true} {
		pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "uid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "original-service-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-log-service"}}}}}
		if terminating {
			deploymentplan.Map(pod["metadata"])["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		}
		if managedLateLogClaimHolders([]deploymentplan.Object{pod}, "video-cloud-dev-video-cloud", "original-log-service", "") == nil {
			t.Fatal("a native Pod holder, including a terminating Pod, authorized restoration")
		}
		if managedLateLogClaimHolders([]deploymentplan.Object{pod}, "video-cloud-dev-video-cloud", "original-log-service", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb") == nil {
			t.Fatal("an unrelated Pod substituted for the accepted helper")
		}
	}
	if err := managedLateLogClaimHolders(nil, "video-cloud-dev-video-cloud", "original-log-service", ""); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLateLogMarkersRequireActualNativeInspectionThenOwnerRelease(t *testing.T) {
	identity := logACKNativeIdentity{Subject: logACKSubject, Fingerprint: strings.Repeat("1", 64), PublicKeySHA256: strings.Repeat("2", 64), RootSHA256: strings.Repeat("3", 64), StateSHA256: strings.Repeat("4", 64)}
	line := "RTK_LOG_ACK_PUBLIC_STATE " + string(mustProviderRecoveryJSON(identity)) + "\n"
	valid := []byte("unrelated native diagnostic\n" + line + "RTK_LOG_ACK_OWNER_RELEASED\n")
	got, err := readLogACKDraftMarkers(valid)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(identity) {
		t.Fatal("actual two-marker evidence rejected", err)
	}
	for name, raw := range map[string]string{
		"missing release":        line,
		"release before inspect": "RTK_LOG_ACK_OWNER_RELEASED\n" + line,
		"duplicate inspection":   line + line + "RTK_LOG_ACK_OWNER_RELEASED\n",
		"duplicate release":      line + "RTK_LOG_ACK_OWNER_RELEASED\nRTK_LOG_ACK_OWNER_RELEASED\n",
		"unknown proof":          line + "RTK_LOG_ACK_FAKE\nRTK_LOG_ACK_OWNER_RELEASED\n",
		"pending identity":       strings.Replace(line, "\"pending\":false", "\"pending\":true", 1) + "RTK_LOG_ACK_OWNER_RELEASED\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readLogACKDraftMarkers([]byte(raw)); err == nil {
				t.Fatal("incomplete or forged native owner-release evidence accepted")
			}
		})
	}
}

func TestManagedLateLogNativeNineACKSetRejectsDuplicates(t *testing.T) {
	good := []string{"a", "b", "c", "d", "e", "f", "g", "h", logACKSubject}
	if !managedLateLogExactSet(good, good) {
		t.Fatal("exact native nine-subject set rejected")
	}
	bad := append([]string(nil), good...)
	bad[8] = bad[7]
	if managedLateLogExactSet(bad, good) || managedLateLogExactSet(good[:8], good) || managedLateLogExactSet(bad, bad) {
		t.Fatal("missing/duplicate native ACK set accepted")
	}
}

func managedLateLogTerminalFixture(t *testing.T) (*createLogACKHelperDraft, deploymentplan.Object, deploymentplan.Object, managedSessionState, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: logACKSubject}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	identity := logACKNativeIdentity{Subject: logACKSubject, Fingerprint: environmentPreflightBytesSHA256(parsed.Raw), PublicKeySHA256: environmentPreflightBytesSHA256(parsed.RawSubjectPublicKeyInfo), RootSHA256: strings.Repeat("1", 64), StateSHA256: strings.Repeat("2", 64)}
	state := managedSessionState{Session: "11111111-1111-4111-8111-111111111111", Issuer: "22222222-2222-4222-8222-222222222222", Root: identity.RootSHA256, Fingerprint: strings.Repeat("3", 64), Status: "active", Expires: now.Add(time.Hour), Subjects: []string{logACKSubject}, Installed: []string{logACKSubject}}
	a := &createLogACKHelperDraft{Parent: strings.Repeat("a", 64), Review: strings.Repeat("b", 64), FreshReview: strings.Repeat("c", 64), Original: deploymentplan.Resource{ID: "Deployment/video-cloud-dev-video-cloud/video-cloud-logingester", UID: "33333333-3333-4333-8333-333333333333", Desired: deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-logingester", "namespace": "video-cloud-dev-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"rtk.cloud/pki-consumer": "true", "app.kubernetes.io/name": "video-cloud-logingester", "rtk.realtek.com/stack": "video-cloud-dev"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": logACKNativeImage}}}}}}}, Identity: identity, OriginalClientsSHA: strings.Repeat("d", 64), OriginalPrivateBytesSHA: identity.StateSHA256, LeafPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), OriginalSession: "44444444-4444-4444-8444-444444444444", NewSession: state}
	definition, err := renderLogACKDraftHelper(a)
	if err != nil {
		t.Fatal(err)
	}
	jobUID, podUID := "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"
	job := deploymentplan.Clone(definition.Object)
	deploymentplan.Map(job["metadata"])["uid"] = jobUID
	job["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"uid": podUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Job", "name": a.helperName(), "uid": jobUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(job)), "status": deploymentplan.Object{"phase": "Succeeded", "containerStatuses": []any{deploymentplan.Object{"name": "app", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed", "startedAt": now.Add(-3 * time.Second).Format(time.RFC3339Nano), "finishedAt": now.Add(-time.Second).Format(time.RFC3339Nano)}}}}}}
	return a, job, pod, state, now
}

func TestManagedLateLogCompletionRejectsEveryUncertainProcess(t *testing.T) {
	a, job, pod, state, now := managedLateLogTerminalFixture(t)
	jobUID := deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"])
	podUID := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	if _, err := verifyLogACKDraftCompleted(a, jobUID, podUID, job, []deploymentplan.Object{pod}, state, a.Identity, a.OriginalClientsSHA, a.Identity.StateSHA256, now); err != nil {
		t.Fatal("exact terminal native proof rejected", err)
	}
	for _, mode := range []string{"extra Pod", "restart", "deadline", "wrong owner", "key bytes changed", "new CSR pending", "missing native ACK", "wrong session", "image changed"} {
		t.Run(mode, func(t *testing.T) {
			p, j, s, identity := deploymentplan.Clone(pod), deploymentplan.Clone(job), state, a.Identity
			pods := []deploymentplan.Object{p}
			switch mode {
			case "extra Pod":
				pods = append(pods, deploymentplan.Clone(p))
			case "restart":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["status"])["containerStatuses"])[0])["restartCount"] = float64(1)
			case "deadline":
				deploymentplan.Map(j["status"])["conditions"] = []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}
			case "wrong owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "77777777-7777-4777-8777-777777777777"
			case "key bytes changed":
				identity.StateSHA256 = strings.Repeat("e", 64)
			case "new CSR pending":
				identity.Pending = true
			case "missing native ACK":
				s.Installed = nil
			case "wrong session":
				s.Session = a.OriginalSession
			case "image changed":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["spec"])["containers"])[0])["image"] = "unqualified:latest"
			}
			if _, err := verifyLogACKDraftCompleted(a, jobUID, podUID, j, pods, s, identity, a.OriginalClientsSHA, identity.StateSHA256, now); err == nil {
				t.Fatal("uncertain native lifecycle authorized business restoration")
			}
		})
	}
}

func TestManagedLateLogHelperRetainsControllerAdmissionWithoutBusinessSelector(t *testing.T) {
	a, _, _, _, _ := managedLateLogTerminalFixture(t)
	definition, err := renderLogACKDraftHelper(a)
	if err != nil {
		t.Fatal(err)
	}
	labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(definition.Object["spec"])["template"])["metadata"])["labels"])
	if len(labels) != 3 || labels["rtk.cloud/pki-consumer"] != "true" || labels["rtk.realtek.com/stack"] != "video-cloud-dev" || labels["app.kubernetes.io/name"] != nil {
		t.Fatal("helper loses Controller ingress admission or matches the original business selector")
	}
	// The actual accepted business Log template has no consumer label. The
	// new reviewed helper supplies its own exact network peer label.
	missing := *a
	missing.Original.Desired = deploymentplan.Clone(a.Original.Desired)
	sourceLabels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(missing.Original.Desired["spec"])["template"])["metadata"])["labels"])
	delete(sourceLabels, "rtk.cloud/pki-consumer")
	before := deploymentplan.Digest(missing.Original.Desired)
	if _, err := renderLogACKDraftHelper(&missing); err != nil || deploymentplan.Digest(missing.Original.Desired) != before {
		t.Fatal("original absent helper label was rejected or original business labels changed", err)
	}
	for _, mode := range []string{"consumer false", "consumer foreign", "consumer bool", "foreign stack", "foreign template stack", "foreign business selector", "foreign namespace", "foreign owner UID"} {
		t.Run(mode, func(t *testing.T) {
			bad := *a
			bad.Original.Desired = deploymentplan.Clone(a.Original.Desired)
			source := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(bad.Original.Desired["spec"])["template"])["metadata"])["labels"])
			switch mode {
			case "consumer false":
				source["rtk.cloud/pki-consumer"] = "false"
			case "consumer foreign":
				source["rtk.cloud/pki-consumer"] = "foreign"
			case "consumer bool":
				source["rtk.cloud/pki-consumer"] = true
			case "foreign template stack":
				source["rtk.realtek.com/stack"] = "video-cloud-prod"
			case "foreign business selector":
				source["app.kubernetes.io/name"] = "video-cloud-api"
			case "foreign namespace":
				deploymentplan.Map(bad.Original.Desired["metadata"])["namespace"] = "video-cloud-prod-video-cloud"
			case "foreign owner UID":
				bad.Original.UID = "not-a-native-uid"
			case "foreign stack":
				deploymentplan.Map(deploymentplan.Map(bad.Original.Desired["metadata"])["labels"])["rtk.realtek.com/stack"] = "video-cloud-prod"
			}
			if _, err := renderLogACKDraftHelper(&bad); err == nil {
				t.Fatal("an unqualified network label was supplied instead of original admission")
			}
		})
	}
}

func TestManagedLateLogHelperLabelsMatchMaintainedControllerAndRegistryNetwork(t *testing.T) {
	a, _, _, _, _ := managedLateLogTerminalFixture(t)
	helper, err := renderLogACKDraftHelper(a)
	if err != nil {
		t.Fatal(err)
	}
	labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(helper.Object["spec"])["template"])["metadata"])["labels"])
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	var controller deploymentplan.Object
	for _, definition := range environmentManagedControllerDependencies(env) {
		if definition.Object["kind"] == "NetworkPolicy" {
			controller = definition.Object
		}
	}
	admitted := false
	for _, rule := range certIssuerObjectList(deploymentplan.Map(controller["spec"])["ingress"]) {
		for _, raw := range certIssuerObjectList(deploymentplan.Map(rule)["from"]) {
			peer := deploymentplan.Map(raw)
			namespace := deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])
			pod := deploymentplan.Map(deploymentplan.Map(peer["podSelector"])["matchLabels"])
			if namespace["rtk.realtek.com/stack"] == "video-cloud-dev" && pod["rtk.cloud/pki-consumer"] == labels["rtk.cloud/pki-consumer"] && len(pod) == 1 {
				admitted = true
			}
		}
	}
	if !admitted {
		t.Fatal("unique helper lost the actual maintained Controller ingress peer")
	}
	var postgres deploymentplan.Object
	if err := yaml.Unmarshal([]byte(lkeAllowPostgresClientsNetworkPolicyManifest(env)), &postgres); err != nil {
		t.Fatal(err)
	}
	registry := false
	for _, rule := range certIssuerObjectList(deploymentplan.Map(postgres["spec"])["ingress"]) {
		for _, raw := range certIssuerObjectList(deploymentplan.Map(rule)["from"]) {
			peer := deploymentplan.Map(raw)
			match := deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])
			if match["kubernetes.io/metadata.name"] == lkeNamespaceName(env, "video-cloud") && peer["podSelector"] == nil {
				registry = true
			}
		}
	}
	if !registry {
		t.Fatal("registry connection unexpectedly requires the original Log business selector")
	}
	var deny deploymentplan.Object
	if err := yaml.Unmarshal([]byte(lkeDefaultDenyIngressNetworkPolicyManifest(env, lkeNamespaceName(env, "video-cloud"))), &deny); err != nil {
		t.Fatal(err)
	}
	types := certIssuerObjectList(deploymentplan.Map(deny["spec"])["policyTypes"])
	if len(types) != 1 || types[0] != "Ingress" {
		t.Fatal("maintained namespace policy changed outgoing DNS/controller/registry isolation")
	}
}

func TestManagedLateLogTerminalObservationSurvivesOnlyMonotonicGenuineACKs(t *testing.T) {
	a, job, pod, state, now := managedLateLogTerminalFixture(t)
	a.OriginalClaimUID = "77777777-7777-4777-8777-777777777777"
	state.Subjects = []string{logACKSubject, "service:another-qualified-owner"}
	a.NewSession = state
	jobUID := deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"])
	podUID := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	native, err := verifyLogACKDraftCompleted(a, jobUID, podUID, job, []deploymentplan.Object{pod}, state, a.Identity, a.OriginalClientsSHA, a.Identity.StateSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustProviderRecoveryJSON(state)
	native["native_session_raw_sha256"] = environmentPreflightBytesSHA256(raw)
	late := &createManagedLateSessionRecovery{ParentSHA: a.Parent, Review: a.FreshReview, NewSessionID: state.Session, OriginalClientsSHA: a.OriginalClientsSHA, LogProducer: a}
	stored := createManagedLateLogObservation{Schema: "rtk-cloud.create-managed-late-log-observation/v1", Parent: a.Parent, Review: a.FreshReview, ProducerSHA: deploymentplan.Digest(a), ClaimUID: a.OriginalClaimUID, Job: job, Pod: pod, Identity: a.Identity, Session: state, SessionRaw: raw, ClientsSHA: a.OriginalClientsSHA, PublicLogSHA: strings.Repeat("8", 64), Native: native}
	if err := validateManagedLateLogObservation(late, stored, jobUID); err != nil {
		t.Fatal("genuine stored observation rejected", err)
	}
	before := deploymentplan.Digest(stored)
	current := stored
	current.Session.Installed = append([]string(nil), state.Subjects...)
	current.SessionRaw = mustProviderRecoveryJSON(current.Session)
	current.Native = deploymentplan.Clone(stored.Native)
	current.Native["native_session_sha256"] = deploymentplan.Digest(current.Session)
	current.Native["native_session_raw_sha256"] = environmentPreflightBytesSHA256(current.SessionRaw)
	if err := managedLateLogObservationCurrent(stored, current); err != nil {
		t.Fatal("another genuine qualified ACK prevented retry of the same terminal helper", err)
	}
	if before != deploymentplan.Digest(stored) {
		t.Fatal("retry rewrote historical terminal session evidence")
	}
	for _, mode := range []string{"unqualified subjects", "unqualified issuer", "unqualified root"} {
		t.Run(mode, func(t *testing.T) {
			bad := stored
			switch mode {
			case "unqualified subjects":
				bad.Session.Subjects = []string{logACKSubject}
			case "unqualified issuer":
				bad.Session.Issuer = "99999999-9999-4999-8999-999999999999"
			case "unqualified root":
				bad.Session.Root = strings.Repeat("f", 64)
			}
			bad.SessionRaw = mustProviderRecoveryJSON(bad.Session)
			selfCompared := *a
			selfCompared.NewSession = bad.Session
			selfProof, err := verifyLogACKDraftCompleted(&selfCompared, jobUID, podUID, job, []deploymentplan.Object{pod}, bad.Session, a.Identity, a.OriginalClientsSHA, a.Identity.StateSHA256, now)
			if err != nil {
				t.Fatal("fixture did not reach the source-binding guard", err)
			}
			selfProof["native_session_raw_sha256"] = environmentPreflightBytesSHA256(bad.SessionRaw)
			bad.Native = selfProof
			if validateManagedLateLogObservation(late, bad, jobUID) == nil {
				t.Fatal("self-consistent native object replaced the qualified root/issuer/subjects")
			}
		})
	}
	for _, mode := range []string{"installation regression", "different session", "different helper", "changed private bytes", "changed log", "changed expiry"} {
		t.Run(mode, func(t *testing.T) {
			bad := current
			bad.Native = deploymentplan.Clone(current.Native)
			switch mode {
			case "installation regression":
				bad.Session.Installed = []string{"service:another-qualified-owner"}
			case "different session":
				bad.Session.Session = a.OriginalSession
			case "different helper":
				bad.Native["helper_pod_uid"] = "88888888-8888-4888-8888-888888888888"
			case "changed private bytes":
				bad.Identity.StateSHA256 = strings.Repeat("f", 64)
			case "changed log":
				bad.PublicLogSHA = strings.Repeat("9", 64)
			case "changed expiry":
				bad.Session.Expires = bad.Session.Expires.Add(time.Minute)
			}
			if managedLateLogObservationCurrent(stored, bad) == nil {
				t.Fatal("changed terminal native evidence authorized retirement")
			}
		})
	}
}
