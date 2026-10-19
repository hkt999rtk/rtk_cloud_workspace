package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productExpiryE2EFixture struct {
	f                         *productProtocolFixture
	program                   createNormalTailProgram
	start                     int
	apply                     environmentCreateBootstrapApply
	original, previous        *deploymentplan.Plan
	installed                 *environmentProductIdentitySession
	oldSession                productSessionRenewalNative
	certificates              map[string][]byte
	sourceKey, sourceCSR      []byte
	files                     map[string][]byte
	renewals                  int
	secondRenewal             bool
	sessionHistory            map[string]productSessionRenewalNative
	deploymentID              string
	cleanupStep               int
	originalDeleteInterrupted bool
	checkPrefixes             bool
	operatorLogins            int
	currentRootCRL            *createRootCRLRecord
	rootCRLACKReads           int
	historicalRootCRL         *createRootCRLRecord
	postCleanup               *postCleanupCorrectionFixtureState
}

type productExpiryWorkloadBackend struct {
	*productOuterTargetBackend
	failed bool
}

func (b *productExpiryWorkloadBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.ID == "StatefulSet/video-cloud-dev-platform/postgresql" && !b.failed {
		b.failed = true
		return errors.New("known PostgreSQL readiness interruption")
	}
	return b.productOuterTargetBackend.Wait(ctx, r)
}

func newProductExpiryE2EFixture(t *testing.T, runtimeCorrection ...bool) *productExpiryE2EFixture {
	t.Helper()
	billingRuntime := len(runtimeCorrection) > 0 && runtimeCorrection[0]
	secondRenewal := len(runtimeCorrection) > 1 && runtimeCorrection[1]
	postCleanup := len(runtimeCorrection) > 2 && runtimeCorrection[2]
	selected, snapshotStore, _, _ := maintainedDeploymentSnapshotFixture(t, "dev")
	architecture := filepath.Join(selected.Workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	text := readTestFile(t, architecture)
	settings, _, _ := completeEnvironmentRenderFixture("dev")
	settings["MQTT_FOUNDATION_REGISTRATION_ENABLED"], settings["SHADOW_WORKER_REGISTRATION_ENABLED"], settings["OTA_SERVICE_REGISTRATION_ENABLED"], settings["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"], settings["WEBRTC_SERVICE_REGISTRATION_ENABLED"] = "true", "true", "true", "true", "true"
	settings["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	settings["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
	if billingRuntime {
		maintained, err := readStrictEnv(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy/architectures/kubernetes/architecture.env"))
		if err != nil {
			t.Fatal("maintained support image configuration", err)
		}
		for _, key := range []string{"ZAMMAD_ELASTICSEARCH_IMAGE", "ZAMMAD_POSTGRES_IMAGE"} {
			settings[key] = maintained[key]
		}
	}
	for key, value := range settings {
		if !(environmentAuthArchitectureKey(key) || environmentControllerPolicySetting(key) || sharedWebRTCDeploymentKey(key) || sharedVideoStorageDeploymentKey(key) || sharedShadowDeploymentKey(key) || sharedOTADeploymentKey(key) || key == "MQTT_FOUNDATION_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED" || key == "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT" || billingRuntime && (key == "ZAMMAD_ELASTICSEARCH_IMAGE" || key == "ZAMMAD_POSTGRES_IMAGE")) {
			continue
		}
		lines, found := strings.Split(text, "\n"), false
		for i, line := range lines {
			if strings.HasPrefix(line, key+"=") {
				lines[i] = key + "=" + value
				found = true
			}
		}
		if !found {
			lines = append(lines, key+"="+value)
		}
		text = strings.Join(lines, "\n")
	}
	// The fresh-process ordinary continuation explicitly selects standard.
	// Declare it in maintained synthetic intent before resolution and freezing.
	text = strings.TrimRight(text, "\n") + "\nDEPLOYMENT_PROTECTION_PROFILE=standard\n"
	writeTestFile(t, architecture, text)
	writeTestFile(t, filepath.Join(selected.EnvironmentRoot, "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\nRUNTIME_OTA_STORAGE_MODE=dedicated\nRUNTIME_OTA_STORAGE_POLICY=colocated\nRUNTIME_OTA_STORAGE_BUCKET=rtk-cloud-dev-ota-firmware-us-sea\nRUNTIME_OTA_STORAGE_PREFIX=environments/video-cloud-dev\n")
	var selectErr error
	selected, selectErr = resolveDeploymentConfig(selected.Workspace, "dev", "")
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if selected.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" {
		t.Fatal("ordinary continuation fixture must resolve its declared standard profile before Program freeze")
	}
	_ = snapshotStore
	x := &productExpiryE2EFixture{files: map[string][]byte{}, certificates: map[string][]byte{}, secondRenewal: secondRenewal, sessionHistory: map[string]productSessionRenewalNative{}}
	setup := func(f *productProtocolFixture, p *createNormalTailProgram) {
		if secondRenewal && f.l.Intermediate.ID == f.next.ID {
			t.Fatal("CRL recovery requires the real successor-issuer protocol fixture before Program freeze")
		}
		ns := f.p.Stack + "-video-cloud"
		// Preserve maintained MQTT recipes and the independent original public trust
		// sources; the product claims still come from genuine enrollment children.
		var source []deploymentplan.Definition
		for _, d := range f.l.SourceDefinitions {
			name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
			if name != "mqtt" && name != "shadow" {
				source = append(source, d)
			}
		}
		f.l.SourceDefinitions = source
		known := map[string]bool{}
		for _, d := range source {
			known[deploymentplan.ID(d.Object)] = true
		}
		add := func(d deploymentplan.Definition, existing bool) {
			id := deploymentplan.ID(d.Object)
			if known[id] {
				return
			}
			known[id] = true
			delete(deploymentplan.Map(d.Object["metadata"]), "uid")
			if existing {
				o := deploymentplan.Clone(d.Object)
				if values := deploymentplan.Map(o["stringData"]); values != nil {
					data := deploymentplan.Object{}
					for key, value := range values {
						data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
					}
					o["data"] = data
					delete(o, "stringData")
				}
				m := deploymentplan.Map(o["metadata"])
				uid := "77777777-aaaa-4aaa-8aaa-" + deploymentplan.Digest(id)[:12]
				m["uid"], m["resourceVersion"] = uid, "1"
				f.b.objects[id] = o
				f.l.SourceOwners[id] = uid
				d.Retain = true
			}
			f.l.SourceDefinitions = append(f.l.SourceDefinitions, d)
		}
		if secondRenewal {
			add(deploymentplan.Definition{Phase: "namespace", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": f.p.Stack + "-account-manager"}}}, true)
			add(deploymentplan.Definition{Phase: "configuration", Object: accountCRLReaderOriginalSecret(t, f)}, true)
		}
		for _, d := range managedMQTTConsumersFixture(t) {
			name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
			if d.Object["kind"] == "Deployment" && strings.HasPrefix(name, "pki-product-") {
				continue
			}
			if d.Object["kind"] == "Deployment" && name == "mqtt-pki" {
				mqttFoundationPrimary(d.Object)["image"] = p.Release.Images["LKE_MQTT_PKI_IMAGE"].Reference
				broker, err := environmentManagedSidecar("pkibroker")
				if err != nil {
					t.Fatal(err)
				}
				broker["image"] = p.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
				pod := environmentPKIPod(d.Object)
				pod["containers"] = append(certIssuerObjectList(pod["containers"]), broker)
			}
			add(d, name != "video-cloud-api-pki" && name != "video-cloud-mqttfoundation" && name != "video-cloud-shadowworker")
		}
		policies, err := decodeEnvironmentCreateManifests(lkeAllowPostgresClientsNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": f.p.Stack}))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range policies {
			d.Phase = "configuration"
			add(d, false)
		}
		for _, suffix := range []string{"platform", "observability"} {
			add(deploymentplan.Definition{Phase: "namespace", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": f.p.Stack + "-" + suffix}}}, true)
		}
		pg := plannedEnvelopeStatefulSet("postgresql", 1, "data")
		pgContainer := mqttFoundationPrimary(pg)
		pgContainer["name"], pgContainer["image"] = "postgres", p.Release.Images["LKE_POSTGRES_IMAGE"].Reference
		pgSpec := deploymentplan.Map(pg["spec"])
		pgLabels := deploymentplan.Object{"app.kubernetes.io/name": "postgresql"}
		pgSpec["selector"] = deploymentplan.Object{"matchLabels": deploymentplan.Clone(pgLabels)}
		deploymentplan.Map(deploymentplan.Map(pgSpec["template"])["metadata"])["labels"] = deploymentplan.Clone(pgLabels)
		base := deploymentplan.Clone(pg)
		deploymentplan.Map(base["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
		deploymentplan.Map(base["metadata"])["resourceVersion"] = "1"
		f.b.objects[deploymentplan.ID(base)] = base
		f.l.SourceOwners[deploymentplan.ID(base)] = "99999999-1111-4111-8111-111111111111"
		deploymentplan.Map(base["metadata"])["annotations"] = deploymentplan.Object{"isolated-original-revision": "before-selected-update"}
		add(deploymentplan.Definition{Phase: "configuration", Object: pg}, false)
		_, _, openbao := existingFileOpenBaoFixture("dev", true)
		for _, o := range openbao {
			if o["kind"] == "StatefulSet" {
				for _, group := range []string{"containers", "initContainers"} {
					for _, raw := range certIssuerObjectList(environmentPKIPod(o)[group]) {
						c := deploymentplan.Map(raw)
						key := "LKE_OPENBAO_PKI_IMAGE"
						if c["name"] == "openbao" {
							key = "LKE_OPENBAO_IMAGE"
						} else {
							c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_ROOT_SHA256": f.l.Root.RootFingerprint}))
						}
						c["image"] = p.Release.Images[key].Reference
					}
				}
			}
			add(deploymentplan.Definition{Phase: environmentResourcePhase(deploymentplan.String(o["kind"])), Object: o}, true)
		}
		add(environmentCreateServiceClaimDefinition(f.cfg, f.p.Stack+"-secrets", "openbao-pki-identity"), true)
		add(environmentCreateSecret(f.p.Stack+"-secrets", "original-pki-database", map[string]string{"url": "postgres://isolated-original-bootstrap"}), true)
		add(deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-openbao-tls-consumer-trust", "namespace": ns}, "data": deploymentplan.Object{"roots.pem": f.l.Root.Certificate}}}, true)
		for _, entry := range rtkSecretCatalog() {
			if err := f.store.write(filepath.Join("runtime", entry.ID), []byte(strings.Repeat("a", 32)), true); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.store.write("kube/kubeconfig.yaml", f.runtime.Backend.KubeconfigContent, true); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
		proof := []byte("isolated original storage migration proof")
		proofSHA := environmentPreflightBytesSHA256(proof)
		for name, value := range map[string][]byte{"storage-migration-ota.json": proof, "storage-cutover-ota.json": mustProviderRecoveryJSON(deploymentplan.Object{"environment": "dev", "bucket": f.cfg.Storage.OTAFirmware.Bucket, "region": f.cfg.Storage.OTAFirmware.Region, "prefix": f.cfg.Storage.OTAFirmware.Prefix, "cutover_id": "isolated-expiry-original", "migration_receipt_sha256": proofSHA, "cutover_at": time.Now().UTC(), "rollback_credentials_retained": true, "service_ready": true})} {
			if err := f.store.write(filepath.Join("deployment/storage", name), value, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.store.write(storageCutoverJournalName("ota"), mustProviderRecoveryJSON(storageCutoverJournal{Environment: "dev", Purpose: "ota", Status: "complete", ID: "isolated-expiry-original", MigrationSHA256: proofSHA}), false); err != nil {
			t.Fatal(err)
		}
		for name, target := range map[string]deploymentStorageTarget{environmentOTAObservationPath: f.cfg.Storage.OTAFirmware, environmentMediaObservationPath: f.cfg.Storage.RuntimeMedia} {
			o := environmentOTAStorageObservation{Version: 1, Environment: f.cfg.Environment, Provider: "linode", Bucket: target.Bucket, Region: target.Region, Prefix: target.Prefix, Endpoint: "https://isolated-allocation.example.invalid", EndpointType: "E3"}
			record := environmentOTAStorageRecord{Observation: o, SHA256: deploymentplan.Digest(o), ObservedAt: time.Now().UTC().Format(time.RFC3339)}
			if err := f.store.write(name, mustProviderRecoveryJSON(record), false); err != nil {
				t.Fatal(err)
			}
		}
		if billingRuntime {
			installProductRuntimeBillingInputs(t, f)
		}
		snapshot, err := readEnvironmentDeploymentSnapshot(f.cfg, f.store)
		if err != nil {
			t.Fatal("real frozen input snapshot", err)
		}
		p.ConfigurationSHA256 = environmentDeploymentSnapshotDigest(withCreateNormalTailHandoff(t.Context(), f.h), f.cfg, snapshot)
		for _, claim := range []string{"original-client-identity", "video-cloud-api-pki-controller-identity"} {
			add(environmentCreateServiceClaimDefinition(f.cfg, ns, claim), true)
		}
		if err := withEnvironmentRenderInputs(f.cfg, f.store, p.Release, snapshot, func(values map[string]string) error {
			definitions, err := decodeEnvironmentCreateManifests(lkeVideoCloudRuntimeSecretManifest(values), lkePostgresServiceManifest(values))
			if err != nil {
				return err
			}
			for _, d := range definitions {
				d.Phase = "configuration"
				add(d, true)
			}
			for _, o := range environmentOpenBaoRaftDependencies(values) {
				name := deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
				if o["kind"] == "ServiceAccount" && name == "openbao" || o["kind"] == "Service" && name == "openbao-internal" {
					add(deploymentplan.Definition{Phase: "configuration", Object: o}, true)
				}
			}
			return nil
		}); err != nil {
			t.Fatal("maintained original runtime foundation dependencies", err)
		}
		if billingRuntime {
			installProductRuntimeBillingDefinitions(t, f, p, snapshot, add)
		}
		if postCleanup {
			x.postCleanup = installPostCleanupCorrectionFixture(t, f, p, add)
		}
		if billingRuntime {
			var imageChecks []deploymentplan.Definition
			for _, d := range f.l.SourceDefinitions {
				d.Object = deploymentplan.Clone(d.Object)
				imageChecks = append(imageChecks, d)
			}
			if err := pinEnvironmentResourceImages(imageChecks, p.Release); err != nil {
				t.Fatal("complete original fixture image-role cohort", err)
			}
		}
		var current []deploymentplan.Object
		for _, o := range f.b.objects {
			current = append(current, o)
		}
		if billingRuntime {
			for _, d := range f.l.SourceDefinitions {
				if d.Object["kind"] != "StatefulSet" || f.b.objects[deploymentplan.ID(d.Object)] != nil {
					continue
				}
				o := deploymentplan.Clone(d.Object)
				deploymentplan.Map(o["metadata"])["uid"] = "15151515-aaaa-4aaa-8aaa-" + deploymentplan.Digest(deploymentplan.ID(o))[:12]
				current = append(current, o)
			}
		}
		if _, err := observeEnvironmentBindings(f.cfg.Environment, p.Release, current); err != nil {
			t.Fatal("maintained original full compiler inventory fixture", err)
		}
		if _, err := createNormalTailProductOutputContracts(f.cfg, f.l); err != nil {
			t.Fatal("original enabled output cohort after correction setup", err)
		}
		if err := f.e.Check(t.Context()); err != nil {
			t.Fatal("original held owner after correction cohort", err)
		}

	}
	f, p, _, start, ordinary := newProductOuterTargetFixtureWithSetup(t, secondRenewal, setup, selected)
	x.f, x.program, x.start = f, p, start
	f.h.OriginalPlan = &environmentCreatePlan{Environment: f.cfg.Environment, Stack: f.p.Stack, Release: p.Release}
	x.apply = func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage == "producer-retire" {
			return x.applyRetire(ctx, stage, defs)
		}
		if err := ordinary(ctx, stage, defs); err != nil {
			if strings.HasSuffix(stage, "-controls") {
				for _, d := range defs {
					if d.Object["kind"] == "Role" || d.Object["kind"] == "RoleBinding" || d.Object["kind"] == "ClusterRole" || d.Object["kind"] == "ClusterRoleBinding" {
						id := deploymentplan.ID(d.Object)
						t.Log("renewal RBAC owner", id, "expected", deploymentplan.Map(d.Object["metadata"])["uid"], "current", deploymentplan.Map(f.b.objects[id]["metadata"])["uid"])
					}
				}
			}
			return err
		}
		for _, d := range defs {
			if d.Object["kind"] == "Job" {
				o := f.b.objects[deploymentplan.ID(d.Object)]
				if o != nil {
					o["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
				}
			}
		}
		if stage == "service-session" && x.oldSession.Session == "" {
			pair, err := tls.X509KeyPair([]byte(f.bootstrap["bootstrap.crt"]), []byte(f.bootstrap["bootstrap.key"]))
			if err != nil {
				t.Fatal(err)
			}
			leaf, _ := x509.ParseCertificate(pair.Certificate[0])
			expires := time.Now().UTC().Truncate(time.Second).Add(20 * time.Second)
			leaf.NotAfter = expires
			der, err := x509.CreateCertificate(rand.Reader, leaf, f.ica, pair.PrivateKey.(*ecdsa.PrivateKey).Public(), f.icaKey)
			if err != nil {
				t.Fatal(err)
			}
			f.bootstrap["bootstrap.crt"] = productPEM("CERTIFICATE", der) + f.next.Certificate + f.next.Certificate + f.l.Root.Certificate
			row := f.rows["service:deployment-bootstrap"]
			row.Fingerprint = environmentPreflightBytesSHA256(der)
			f.rows["service:deployment-bootstrap"] = row
			f.expires = expires
			for _, d := range defs {
				if d.Object["kind"] == "Job" {
					c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
					x.deploymentID = mqttFoundationEnv(c, "PKI_BOOTSTRAP_DEPLOYMENT_ID")
				}
			}
			x.oldSession = productSessionRenewalNative{productSessionInstallations: productSessionInstallations{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Subjects: slices.Clone(f.subjects), Status: f.status, Expires: expires}, Fingerprint: row.Fingerprint, Caller: "service:deployment-bootstrap", DeploymentID: x.deploymentID}
		}
		if strings.HasSuffix(stage, "-job") && strings.HasPrefix(stage, "session-renewal-") {
			var job deploymentplan.Object
			for _, d := range defs {
				if d.Object["kind"] == "Job" {
					job = d.Object
				}
			}
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])
			sid := mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")
			path := mqttFoundationEnv(c, "PKI_BOOTSTRAP_CERT_PATH")
			if x.renewals != 0 && (sid == f.session || x.sessionHistory[sid].Session != "") {
				return nil
			}
			if x.renewals >= 2 || x.renewals != 0 && !x.secondRenewal {
				t.Fatal("unexpected extra native renewal Job")
			}
			if x.renewals != 0 {
				x.sessionHistory[f.session] = productSessionRenewalNative{productSessionInstallations: productSessionInstallations{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Subjects: slices.Clone(f.subjects), Installed: slices.Clone(f.installed), Status: "expired", Expires: f.expires}, Fingerprint: f.rows["service:deployment-bootstrap"].Fingerprint, Caller: "service:deployment-bootstrap", DeploymentID: x.deploymentID}
			}
			pair, err := tls.X509KeyPair(x.files["/run/pki-bootstrap/bootstrap.crt"], x.sourceKey)
			if err != nil {
				t.Fatal(err)
			}
			leaf, _ := x509.ParseCertificate(pair.Certificate[0])
			leaf.NotBefore = time.Now().UTC().Add(-time.Minute)
			leaf.NotAfter = time.Now().UTC().Truncate(time.Second).Add(8 * time.Hour)
			if x.secondRenewal && x.renewals == 0 {
				// An actually issued short first leaf expires naturally while the
				// ordinary runtime prefix advances. No saved proof or clock is edited.
				leaf.NotAfter = time.Now().UTC().Truncate(time.Second).Add(45 * time.Second)
			}
			leaf.SerialNumber = big.NewInt(int64(9001 + x.renewals))
			der, err := x509.CreateCertificate(rand.Reader, leaf, f.ica, pair.PrivateKey.(*ecdsa.PrivateKey).Public(), f.icaKey)
			if err != nil {
				t.Fatal(err)
			}
			x.files[path] = []byte(productPEM("CERTIFICATE", der) + f.next.Certificate + f.next.Certificate + f.l.Root.Certificate)
			f.session, f.expires, f.status = sid, leaf.NotAfter, "active"
			x.deploymentID = mqttFoundationEnv(c, "PKI_BOOTSTRAP_DEPLOYMENT_ID")
			f.installed = nil
			row := f.rows["service:deployment-bootstrap"]
			row.Fingerprint = environmentPreflightBytesSHA256(der)
			f.rows["service:deployment-bootstrap"] = row
			x.oldSession.Status = "expired"
			x.renewals++
		}
		if x.checkPrefixes {
			x.assertCurrentPrefix(t)
		}
		return nil
	}
	x.installBoundary(t)
	if billingRuntime {
		operator, err := f.store.readOperator()
		if err != nil {
			t.Fatal(err)
		}
		f.runtime.PKIClient.token = ""
		if _, err := f.runtime.PKIClient.Authenticate(t.Context(), operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
			t.Fatal("genuine initial fixture operator login", err)
		}
		f.runtime.PKIClient.operatorCredentials = operator
	}
	bin := t.TempDir()
	writeTestFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$*\" in *'/repos/'*) printf '"+strings.Repeat("b", 40)+"\\n';; *) printf '"+strings.Repeat("a", 40)+"\\n';; esac\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, x.apply); err != nil {
		t.Fatal("genuine original five-product protocol", err)
	}
	var err error
	x.installed, err = readProductInstalled(f.store, f.l)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range f.l.Outputs {
		x.certificates[o.Subject] = append([]byte(nil), f.states[o.Subject]...)
	}
	for k, v := range f.bootstrap {
		x.files["/run/pki-bootstrap/"+k] = []byte(v)
	}
	x.sourceKey = append([]byte(nil), x.files["/run/pki-bootstrap/bootstrap.key"]...)
	x.sourceCSR = append([]byte(nil), x.files["/run/pki-bootstrap/bootstrap.csr"]...)
	// Build the truthful original target from its frozen source catalog, without
	// applying the later MQTT correction; all writes remain ordinary engine work.
	var defs []deploymentplan.Definition
	outputs := map[string]environmentCreateProductIdentity{}
	counts := map[string]int{}
	for _, o := range f.l.Outputs {
		outputs[o.ID] = o
	}
	// Preserve the actual qualified successor trust in this historical target.
	// Only the three MQTT consumer bodies retain their original transport defect.
	qualifiedTrust := map[string]deploymentplan.Definition{}
	if f.l.RequiresSuccessor {
		trust, err := productIdentityTrustDefinitions(f.l, x.installed.Ceremony.Intermediate)
		if err != nil {
			t.Fatal("qualified original successor trust", err)
		}
		stage := "product-identities-" + productIdentityScope(f.l)[:12] + "-successor-crls"
		accepted, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
		if err != nil || accepted == nil || !journal.Accepted {
			t.Fatal("genuine original successor CRL child", err)
		}
		for _, definition := range trust {
			id := deploymentplan.ID(definition.Object)
			current := f.b.objects[id]
			matched := false
			for _, resource := range accepted.Resources {
				if resource.ID == id && deploymentplan.Digest(resource.Desired["data"]) == deploymentplan.Digest(definition.Object["data"]) && journal.Owners[id] == f.l.SourceOwners[id] {
					matched = true
				}
			}
			if !matched || current == nil || deploymentplan.Map(current["metadata"])["uid"] != f.l.SourceOwners[id] || !deploymentplan.Matches(definition.Object, current) {
				t.Fatal("historical target must preserve its genuine accepted successor trust", id)
			}
			qualifiedTrust[id] = definition
		}
	}
	for _, source := range f.l.SourceDefinitions {
		d := source
		d.Object = deploymentplan.Clone(source.Object)
		if trust, ok := qualifiedTrust[deploymentplan.ID(d.Object)]; ok {
			d.Object = deploymentplan.Clone(trust.Object)
			d.Retain = trust.Retain
		}
		if err := resolveCreateNormalTailProductACK(d.Object, outputs, counts, x.installed.Ceremony, true); err != nil {
			t.Fatal(err)
		}
		defs = append(defs, d)
	}
	preparations, err := productPreparationDefinitions(f.cfg, f.store, f.e, f.l, x.installed, false)
	if err != nil {
		t.Fatal(err)
	}
	defs = append(defs, preparations...)
	// The full target retains the claims already allocated by the genuine
	// session/enrollment children. Keep their original accepted UID proofs in
	// this frozen historical target, just as the maintained compiler does.
	allocationStages := []string{"service-session"}
	for _, output := range f.l.Outputs {
		allocationStages = append(allocationStages, "enroll-"+strings.TrimPrefix(output.Subject, "service:"))
	}
	for _, suffix := range allocationStages {
		stage := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + suffix
		plan, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
		if err != nil {
			t.Fatal("genuine original product allocation source", stage, err)
		}
		_, verified, err := readCreateNormalTailStage(f.store, f.e, stage, plan)
		if err != nil || !verified.Accepted || deploymentplan.Digest(verified) != deploymentplan.Digest(journal) {
			t.Fatal("original product allocation child lost its parent/lease binding", stage, err)
		}
		for _, resource := range plan.Resources {
			if resource.Desired["kind"] != "PersistentVolumeClaim" {
				continue
			}
			owner := journal.Owners[resource.ID]
			actual, err := f.b.Read(t.Context(), resource.ID)
			if err != nil || !environmentBootstrapOwnerUUID.MatchString(owner) || deploymentplan.Map(actual["metadata"])["uid"] != owner {
				t.Fatal("original product allocation owner differs from accepted child", resource.ID, err)
			}
			object := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(object["metadata"])["uid"] = owner
			defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: object, Retain: true})
		}
	}

	for id, o := range f.b.objects {
		if o["kind"] != "PersistentVolumeClaim" {
			continue
		}
		meta := deploymentplan.Map(o["metadata"])
		name := "pv-" + deploymentplan.Digest(id)[:12]
		deploymentplan.Map(o["spec"])["volumeName"] = name
		o["status"] = deploymentplan.Object{"phase": "Bound"}
		f.b.objects["PersistentVolume//"+name] = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": name, "uid": "88888888-aaaa-4aaa-8aaa-" + deploymentplan.Digest(name)[:12]}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": meta["namespace"], "name": meta["name"], "uid": meta["uid"]}, "storageClassName": deploymentplan.Map(o["spec"])["storageClassName"], "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "native-" + deploymentplan.Digest(id)[:12]}}, "status": deploymentplan.Object{"phase": "Bound"}}
	}
	if billingRuntime {
		installProductRuntimeNativeVolumes(t, f)
		completeProductNativeStatefulSetObservations(t, f)
	}
	var live []deploymentplan.Object
	for _, o := range f.b.objects {
		live = append(live, o)
	}
	inputs := deploymentplan.Inputs{Operation: "create", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: p.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: deploymentplan.Digest(p.Bindings), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}
	x.original, err = deploymentplan.Compile(inputs, defs, live, f.l.SourceOwners)
	if err != nil {
		t.Fatal("original frozen product target", err)
	}
	storage, err := f.runtime.Backend.storageBindingsFromInventory(t.Context(), x.original, f.b.objects)
	if err != nil {
		t.Fatal("original storage inputs", err)
	}
	x.original.StorageSHA256 = deploymentplan.Digest(storage)
	if err = saveCreateNormalTailTarget(&compiledEnvironmentDeployment{Store: f.store, Plan: x.original, Release: p.Release, Bindings: p.Bindings}, f.h); err != nil {
		t.Fatal(err)
	}
	boundary := &productExpiryWorkloadBackend{productOuterTargetBackend: &productOuterTargetBackend{productProtocolBackend: f.b}}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	if err = executeCreateNormalTailWorkloadBackend(t.Context(), f.store, f.h, x.original, boundary, check, check, check, io.Discard, func(context.Context) error { return nil }); err == nil {
		t.Fatal("missing original known-write interruption")
	}
	if billingRuntime {
		installProductRuntimeNativeVolumes(t, f)
		captureProductRuntimeVolumes(t, f)
	}
	c := &compiledEnvironmentDeployment{Store: f.store, Configuration: f.cfg, Plan: x.original, Release: p.Release, Bindings: p.Bindings, Backend: f.runtime.Backend}
	applyAccess := func(ctx context.Context, defs []deploymentplan.Definition) error { return x.applyAccess(ctx, defs) }
	if err = ensureCreateNormalTailPostgresAccessWithApply(t.Context(), f.cfg, f.h, &p, f.b.Read, applyAccess); err != nil {
		t.Fatal("real early runtime policy child", err)
	}
	if _, err := compileCreateNormalTailProductTarget(withCreateNormalTailHandoff(t.Context(), f.h), f.cfg, f.h, &p, f.runtime.Backend, false); err != nil {
		// This new case preserves an already admitted historical public profile.
		// The current compiler must refuse that known defect before correction;
		// its real steady compile after the owned repair must succeed.
		if !postCleanup || !strings.Contains(err.Error(), "certissuer-public") {
			t.Fatal("actual initial target compiler before expiry recovery", err)
		}
		t.Log("historical admitted public defect correctly rejected by current compiler; immutable original target retained")
	} else if postCleanup {
		t.Fatal("historical public profile unexpectedly passed current compiler")
	}
	t.Log("milestone: actual initial common target compiler checked original dependencies and access policies")
	c, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
		var rows []deploymentplan.Object
		for _, o := range f.b.objects {
			rows = append(rows, o)
		}
		return rows, nil
	})
	if err != nil {
		t.Fatal("real previous forward preparation", err)
	}
	x.previous = c.Plan
	blocked := func(context.Context, *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		return nil, errors.New("known first phase input failure before native effect")
	}
	if err = executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, boundary, check, check, check, io.Discard, func(context.Context) error { return nil }, blocked); err == nil {
		t.Fatal("previous forward did not stop before first write")
	}
	previous, exists, err := x.previous.ReadExecution(f.store.Root)
	if err != nil || !exists || !productSessionForwardKnownZeroWrite(x.previous, previous) {
		t.Fatal("original previous forward is not genuine zero-write outcome", err)
	}
	if postCleanup {
		x.qualifyInstalledPostCleanupFixture(t)
	}
	return x
}

func (x *productExpiryE2EFixture) applyAccess(ctx context.Context, defs []deploymentplan.Definition) error {
	f := x.f
	var live []deploymentplan.Object
	for _, o := range f.b.objects {
		live = append(live, o)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(x.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(createNormalTailPostgresAccessStage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
	if err != nil {
		return err
	}
	if err = saveCreateNormalTailStagePlan(ctx, f.store, f.e, createNormalTailPostgresAccessStage, p); err != nil {
		return err
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	return executeCreateNormalTailStageBackend(ctx, f.store, f.h, createNormalTailPostgresAccessStage, p, f.b, check, check, check, io.Discard, func(context.Context) error { return nil })
}
func (x *productExpiryE2EFixture) installBoundary(t *testing.T) {
	f := x.f
	base := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if x.currentRootCRL != nil && r.Method == http.MethodGet && r.URL.Path == "/v1/platform/pki/issuers/"+f.l.Root.ID+"/crl" {
			json.NewEncoder(w).Encode(x.currentRootCRL)
			return
		}
		if r.URL.Path == "/v1/auth/login" {
			var credentials map[string]string
			operator, err := f.store.readOperator()
			if err != nil || json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials["email"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || credentials["password"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "foreign synthetic operator credentials", http.StatusUnauthorized)
				return
			}
			x.operatorLogins++
			json.NewEncoder(w).Encode(deploymentplan.Object{"user": deploymentplan.Object{"id": f.runtime.PKIClient.OperatorID, "email": credentials["email"], "signup_pending_verification": false}, "tokens": deploymentplan.Object{"access_token": fmt.Sprintf("isolated-original-operator-token-%d", x.operatorLogins)}})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/boundary/") {
			if !f.final.FinalizedAt.IsZero() && r.URL.Path == "/v1/platform/pki/deployment-bootstrap/"+f.final.SessionID+"/finalize" {
				json.NewEncoder(w).Encode(f.final)
				return
			}
			base.ServeHTTP(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request struct {
			Args  []string `json:"args"`
			Input string   `json:"input"`
		}
		if json.Unmarshal(raw, &request) != nil {
			t.Error("invalid isolated native boundary")
			w.WriteHeader(500)
			return
		}
		joined := strings.Join(request.Args, " ")
		if x.currentRootCRL != nil && strings.Contains(joined, "registry-read") && strings.Contains(joined, "pki_crl_acknowledgments") && strings.Contains(joined, "issuer_id='"+f.l.Root.ID+"'") {
			consumers, err := productIdentityCRLConsumers(f.l)
			if err != nil || len(consumers) != 9 || !productHistoricalCRLPublicTrustMatches(f) || !strings.Contains(joined, "digest='"+x.currentRootCRL.Digest+"'") {
				json.NewEncoder(w).Encode(deploymentplan.Object{"output": "", "code": 1})
				return
			}
			x.rootCRLACKReads++
			json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(mustProviderRecoveryJSON(consumers)), "code": 0})
			return
		}
		respond := func(raw []byte, code int) {
			json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(raw), "code": code})
		}
		if i := slices.Index(request.Args, "get"); i >= 0 && len(request.Args) > i+1 && request.Args[i+1] == "persistentvolumes" {
			items := []deploymentplan.Object{}
			for _, object := range f.b.objects {
				if object["kind"] == "PersistentVolume" {
					items = append(items, deploymentplan.Clone(object))
				}
			}
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"items": items}), 0)
			return
		}
		if slices.Contains(request.Args, "delete") {
			for _, arg := range request.Args {
				if !strings.HasPrefix(arg, "--raw=/api/v1/namespaces/") {
					continue
				}
				parts := strings.Split(strings.TrimPrefix(arg, "--raw=/api/v1/namespaces/"), "/")
				if len(parts) != 3 {
					respond(nil, 1)
					return
				}
				kind := ""
				switch parts[1] {
				case "secrets":
					kind = "Secret"
				case "configmaps":
					kind = "ConfigMap"
				default:
					respond(nil, 1)
					return
				}
				id := kind + "/" + parts[0] + "/" + parts[2]
				current := f.b.objects[id]
				var opts struct {
					Preconditions struct {
						UID     string `json:"uid"`
						Version string `json:"resourceVersion"`
					} `json:"preconditions"`
				}
				body, _ := base64.StdEncoding.DecodeString(request.Input)
				if json.Unmarshal(body, &opts) != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != opts.Preconditions.UID || deploymentplan.Map(current["metadata"])["resourceVersion"] != opts.Preconditions.Version {
					respond(nil, 1)
					return
				}
				delete(f.b.objects, id)
				if kind == "Secret" && parts[2] == x.installed.Ceremony.BootstrapCredentialSecret && !x.originalDeleteInterrupted {
					x.originalDeleteInterrupted = true
					respond(nil, 1)
					return
				}
				respond(nil, 0)
				return
			}
		}
		if strings.Contains(joined, "registry-read") && strings.Contains(joined, "'count',count(*)") {
			respond([]byte(`{"count":0}`), 0)
			return
		}
		if strings.Contains(joined, "registry-read") && strings.Contains(joined, "pki_deployment_bootstrap_sessions") && !strings.Contains(joined, "finalized_at IS NOT NULL") && x.oldSession.Session != "" {
			value := productSessionRenewalNative{productSessionInstallations: productSessionInstallations{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Subjects: f.subjects, Installed: f.installed, Status: f.status, Expires: f.expires}, Fingerprint: f.rows["service:deployment-bootstrap"].Fingerprint, Caller: "service:deployment-bootstrap", DeploymentID: x.deploymentID}
			if !f.final.FinalizedAt.IsZero() {
				at := f.final.FinalizedAt
				value.FinalizedAt = &at
			}
			if strings.Contains(joined, x.oldSession.Session) {
				value = x.oldSession
			} else {
				for sid, historical := range x.sessionHistory {
					if strings.Contains(joined, sid) {
						value = historical
						break
					}
				}
			}
			var selected map[string]any
			_ = json.Unmarshal(mustProviderRecoveryJSON(value), &selected)
			for key := range selected {
				if !strings.Contains(joined, "'"+key+"'") {
					delete(selected, key)
				}
			}
			respond(mustProviderRecoveryJSON(selected), 0)
			return
		}
		if strings.Contains(joined, "for f in bootstrap.key bootstrap.csr;") {
			respond([]byte("600 10001 10001\n600 10001 10001\n"), 0)
			return
		}
		if i := slices.Index(request.Args, "cat"); i >= 0 && i+1 < len(request.Args) && strings.HasPrefix(request.Args[i+1], "/run/pki-bootstrap/") && len(x.files) > 0 {
			path := request.Args[i+1]
			if strings.HasSuffix(path, "bootstrap.finalized.json") {
				respond(f.marker, 0)
				return
			}
			v, exists := x.files[path]
			if exists {
				respond(v, 0)
			} else {
				respond(nil, 1)
			}
			return
		}
		if strings.Contains(joined, "archive=") && strings.Contains(joined, "bootstrap.crt") {
			source, err := readProductSessionRenewalSource(f.store, f.e, f.l)
			if err != nil || source == nil {
				respond(nil, 1)
				return
			}
			if source.Generation == 2 {
				first, er := readProductSessionRenewalFirst(f.store, f.e, f.l)
				if er != nil || first == nil {
					respond(nil, 1)
					return
				}
				canonical := x.files["/run/pki-bootstrap/bootstrap.crt"]
				if environmentPreflightBytesSHA256(canonical) == first.Source.OriginalCertificateSHA256 {
					x.files["/run/pki-bootstrap/expired-"+first.Source.OriginalSession.Session+"/bootstrap.crt"] = append([]byte(nil), canonical...)
					x.files["/run/pki-bootstrap/bootstrap.crt"] = append([]byte(nil), x.files[first.Source.CertificatePath]...)
				}
			}
			old := x.files["/run/pki-bootstrap/bootstrap.crt"]
			renewed := x.files[source.CertificatePath]
			archived := x.files["/run/pki-bootstrap/expired-"+source.OriginalSession.Session+"/bootstrap.crt"]
			if environmentPreflightBytesSHA256(archived) == source.OriginalCertificateSHA256 && bytes.Equal(old, renewed) {
				respond(nil, 0)
				return
			}
			if environmentPreflightBytesSHA256(old) != source.OriginalCertificateSHA256 || len(renewed) == 0 {
				respond(nil, 1)
				return
			}
			x.files["/run/pki-bootstrap/expired-"+source.OriginalSession.Session+"/bootstrap.crt"] = append([]byte(nil), old...)
			x.files["/run/pki-bootstrap/bootstrap.crt"] = append([]byte(nil), renewed...)
			f.bootstrap["bootstrap.crt"] = string(renewed)
			respond(nil, 0)
			return
		}
		if strings.Contains(joined, "openbao-0") && strings.Contains(joined, "bao policy list -format=json") {
			var names []string
			for name := range f.policies {
				names = append(names, name)
			}
			slices.Sort(names)
			respond(mustProviderRecoveryJSON(names), 0)
			return
		}
		if strings.Contains(joined, "openbao-0") && strings.Contains(joined, "bao policy delete ") {
			name := strings.Fields(strings.Split(joined, "bao policy delete ")[1])[0]
			delete(f.policies, name)
			respond(nil, 0)
			return
		}
		if strings.Contains(joined, "cleanup-credentials") {
			if len(f.marker) == 0 {
				hashes := map[string]string{}
				for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
					hashes[name] = environmentPreflightBytesSHA256(x.files["/run/pki-bootstrap/"+name])
				}
				f.marker = append(mustProviderRecoveryJSON(managedLateCustodyTombstone{Schema: "rtk-cloud.bootstrap-credentials/v1", Finalization: f.final, Root: f.l.Root.RootFingerprint, Files: hashes}), '\n')
			}
			if x.cleanupStep < 4 {
				for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"}[:x.cleanupStep] {
					delete(x.files, "/run/pki-bootstrap/"+name)
					delete(f.bootstrap, name)
				}
				x.cleanupStep++
				respond(nil, 1)
				return
			}
			for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
				delete(x.files, "/run/pki-bootstrap/"+name)
				delete(f.bootstrap, name)
			}
			delete(f.b.objects, "Secret/"+f.p.Stack+"-video-cloud/service-bootstrap-"+f.session)
			respond(nil, 0)
			return
		}
		base.ServeHTTP(w, r)
		if strings.Contains(joined, "cleanup-credentials") {
			for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
				delete(x.files, "/run/pki-bootstrap/"+name)
			}
		}
	})
}

func TestProductSessionExpiryOuterPreservesFiveAndReadsEveryRenewalPrefix(t *testing.T) {
	runProductSessionExpiryOuter(t, false)
}

func TestProductSessionRuntimeConfigurationOuterKeepsAllThreeWorkloadSources(t *testing.T) {
	runProductSessionExpiryOuter(t, true)
}

func TestProductSessionRuntimeCRLRecoveryOuterRenewsTwiceAndPreservesAllSources(t *testing.T) {
	runProductSessionExpiryOuter(t, true, true)
}

func runProductSessionExpiryOuter(t *testing.T, runtimeCorrection bool, laterRenewal ...bool) {
	second := len(laterRenewal) > 0 && laterRenewal[0]
	postCleanup := len(laterRenewal) > 1 && laterRenewal[1]
	x := newProductExpiryE2EFixture(t, runtimeCorrection, second, postCleanup)
	f := x.f
	if second {
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			if err := x.exportFailedReaderCheckpoint(t); err != nil {
				t.Log("failed synthetic checkpoint export", err)
			}
		})
	}
	originalRaw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", x.original.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	previousRaw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", x.previous.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(x.oldSession.Expires.Add(time.Millisecond)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	t.Log("milestone: original five identities + genuine zero-write old forward, waiting original leaf expiry")
	oldPolicy := "rtk-" + f.cfg.Environment + "-service-bootstrap-" + x.oldSession.Session
	oldHCL, exists := f.policies[oldPolicy]
	if !exists {
		t.Fatal("original signer policy fixture absent")
	}
	countBefore := len(f.e.Journal.Entries)
	f.policies[oldPolicy] = "foreign original signer policy"
	_, negative := executeProductSessionRenewal(t.Context(), f.cfg, f.store, f.runtime, f.l, x.apply)
	if negative == nil || !strings.Contains(negative.Error(), "original signer policy native body") || x.renewals != 0 || len(f.e.Journal.Entries) != countBefore {
		t.Fatal("foreign original signer HCL was not rejected before renewal effects", negative)
	}
	if source, er := readProductSessionRenewalSource(f.store, f.e, f.l); er != nil || source != nil {
		t.Fatal("failed dependency check published source", er)
	}
	f.policies[oldPolicy] = oldHCL
	x.checkPrefixes = true
	r, err := executeProductSessionRenewal(t.Context(), f.cfg, f.store, f.runtime, f.l, x.apply)
	if err != nil {
		t.Fatal("actual product-only renewal producer", err)
	}
	t.Log("milestone: actual renewal producer and each standard child prefix accepted")
	if r == nil || r.Installed.Ceremony.BootstrapSessionID == r.Original.Ceremony.BootstrapSessionID || x.renewals != 1 {
		t.Fatal("missing genuine linked renewal")
	}
	for subject, before := range x.certificates {
		if !bytes.Equal(before, f.states[subject]) {
			t.Fatal("permanent identity reissued", subject)
		}
	}
	if !bytes.Equal(x.sourceKey, x.files["/run/pki-bootstrap/bootstrap.key"]) || !bytes.Equal(x.sourceCSR, x.files["/run/pki-bootstrap/bootstrap.csr"]) {
		t.Fatal("original key/CSR changed")
	}
	for _, pair := range []struct {
		p   *deploymentplan.Plan
		raw []byte
	}{{x.original, originalRaw}, {x.previous, previousRaw}} {
		after, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/rollback", pair.p.SHA256(), "journal.json"))
		if err != nil || !bytes.Equal(after, pair.raw) {
			t.Fatal("preserved failed native journal changed", err)
		}
	}
	consumed, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, f.e.Journal.Entries[x.start:])
	if err != nil || consumed != len(f.e.Journal.Entries)-x.start {
		t.Fatal("real renewal prefix reader", consumed, len(f.e.Journal.Entries)-x.start, err)
	}

	original, oj, err := readCreateNormalTailWorkloadOriginal(f.store, f.e, x.original)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := readCreateNormalTailWorkloadForwardDescriptor(f.store, f.e, original, oj)
	if err != nil {
		t.Fatal(err)
	}
	c := &compiledEnvironmentDeployment{Store: f.store, Configuration: f.cfg, Plan: x.previous, Release: x.program.Release, Bindings: x.program.Bindings, Backend: f.runtime.Backend}
	c, err = prepareCreateNormalTailProductSessionForward(t.Context(), f.cfg, f.h, c, prior, original, oj)
	if err != nil {
		t.Fatal("genuine successor prepare", err)
	}
	d, err := readCreateNormalTailProductSessionForward(f.store, f.e, original, oj, prior)
	if err != nil || d == nil {
		t.Fatal("successor descriptor", err)
	}
	// Production validates the active renewed target before entering the
	// standard workload backend. Placement remains tied to the original
	// enrollment while the bootstrap ACK must use the renewed session.
	oldSID, newSID := r.Original.Ceremony.BootstrapSessionID, r.Installed.Ceremony.BootstrapSessionID
	if oldSID == newSID || r.Installed.EnrollmentSessionID != oldSID {
		t.Fatal("renewed target lost its distinct ACK and original enrollment sessions")
	}
	for _, worker := range []struct{ name, role string }{{"video-cloud-mqttfoundation", "mqtt"}, {"video-cloud-shadowworker", "shadow"}} {
		var consumer, producer deploymentplan.Object
		producerName := "pki-product-" + worker.role + "-" + strings.ReplaceAll(oldSID, "-", "")[:12]
		for _, resource := range c.Plan.Resources {
			name := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])
			if resource.Desired["kind"] == "Deployment" && name == worker.name {
				consumer = resource.Desired
			}
			if resource.Desired["kind"] == "Deployment" && name == producerName {
				if producer != nil || resource.Action != "retain" || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
					t.Fatal("original same-PVC enrollment producer lost its genuine retained owner")
				}
				producer = resource.Desired
			}
		}
		if consumer == nil || producer == nil || mqttFoundationEnv(mqttFoundationPrimary(consumer), "PKI_BOOTSTRAP_SESSION_ID") != newSID {
			t.Fatal("active renewed MQTT target does not retain its original producer and new ACK session")
		}
		terms := certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(environmentPKIPod(consumer)["affinity"])["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])
		if len(terms) != 1 {
			t.Fatal("original same-PVC placement is absent")
		}
		expressions := certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(terms[0])["labelSelector"])["matchExpressions"])
		if len(expressions) != 1 || deploymentplan.Digest(deploymentplan.Map(expressions[0])["values"]) != deploymentplan.Digest([]any{worker.name, producerName}) {
			t.Fatal("renewed ACK changed original enrollment affinity")
		}
	}
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	if err := checkEnvironmentManagedMQTTConsumers(ctx, c.Plan, f.b.Read); err != nil {
		t.Fatal("production active renewed MQTT validation before workload backend", err)
	}
	t.Log("milestone: shared active renewed target check preserves original enrollment placement and new ACK session")
	phaseCalls := 0
	phase := func(ctx context.Context, p *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		phaseCalls++
		return c.FenceInventory(ctx, p)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error { _, err := phase(ctx, p); return err }
	boundary := &productOuterTargetBackend{productProtocolBackend: f.b}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, boundary, f.store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, boundary) }
	if _, err = phase(ctx, c.Plan); err != nil {
		t.Fatal("complete successor input fence", err)
	}
	if runtimeCorrection {
		c = executeProductRuntimeBillingRecovery(t, x, c, d)
		if x.secondRenewal {
			r, err = readProductSessionRenewalAt(f.store, f.e, f.l, 2)
			if err != nil || r == nil || x.renewals != 2 {
				t.Fatal("completed exact second renewal", err)
			}
		}
	} else {
		if err = executeCreateNormalTailProductSessionForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return errors.New("known accepted successor before parent completion") }, phase); err == nil {
			t.Fatal("missing accepted successor interruption")
		}
		x.assertCurrentPrefix(t)
		knownWrites := deploymentplan.Digest(f.b.writes)
		if err = executeCreateNormalTailProductSessionForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase); err != nil {
			t.Fatal("actual phase-fenced successor", err)
		}
		if deploymentplan.Digest(f.b.writes) != knownWrites {
			t.Fatal("accepted successor retry rewrote native resources")
		}
	}
	t.Log("milestone: full input fence and actual successor standard child accepted")
	if !runtimeCorrection && phaseCalls < 3 {
		t.Fatal("phase inventory fence omitted")
	}
	var operator map[string]string
	if runtimeCorrection {
		operator, err = f.store.readOperator()
		if err != nil {
			t.Fatal("selected finalizer operator", err)
		}
		if operator == nil || operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] == "" || operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] == "" {
			t.Fatal("finalizer requires the genuine selected original operator pair")
		}
		source, err := readCreateNormalTailRuntimeSource(f.store, f.e)
		if err != nil || source == nil {
			t.Fatal("finalizer original runtime source", err)
		}
		descriptor, err := readCreateNormalTailRuntimeForward(f.store, f.e)
		if err != nil || descriptor == nil {
			t.Fatal("finalizer selected runtime descriptor", err)
		}
		snapshot, err := createNormalTailRuntimeEffectiveSnapshot(*source, descriptor.Inputs)
		if err != nil || deploymentplan.Digest(snapshot.Operator) != deploymentplan.Digest(operator) {
			t.Fatal("finalizer operator differs from its authenticated effective snapshot", err)
		}
	}
	x.readyACKs(t, r)
	t.Log("milestone: five original keypairs complete mTLS ready/ACK adapters")
	var checkCleanupFault func(int)
	if x.secondRenewal {
		x.assertCurrentPrefix(t)
		checkCleanupFault = x.cleanupFaultChecks(t, r)
	}
	for i := 0; i < 5; i++ {
		if err = completeProductIdentitySession(ctx, f.cfg, f.p, f.store, f.runtime, f.l, &r.Installed, operator, x.apply); err == nil {
			t.Fatal("cleanup interruption was not exercised", i)
		}
		expectedRenewals := 1
		if x.secondRenewal {
			expectedRenewals = 2
		}
		if x.renewals != expectedRenewals {
			t.Fatal("cleanup regenerated session")
		}
		t.Log("cleanup interruption", i, "step", x.cleanupStep, err)
		if checkCleanupFault != nil {
			checkCleanupFault(i)
		} else {
			x.assertCurrentPrefix(t)
		}
	}
	if err = completeProductIdentitySession(ctx, f.cfg, f.p, f.store, f.runtime, f.l, &r.Installed, operator, x.apply); err != nil {
		t.Fatal("renewed native finalization and cleanup", err)
	}
	t.Log("milestone: renewed finalization + all cleanup interruptions reconciled")
	if checkCleanupFault != nil {
		x.assertCurrentPrefix(t)
	}
	if _, err = verifyProductFinalization(ctx, f.cfg, f.runtime, f.l, &r.Installed); err != nil {
		t.Fatal("finalization proof", err)
	}
	if err = applyProductSteadyCleanup(ctx, f.cfg, f.store, f.e, f.l, &r.Installed, x.apply); err != nil {
		t.Fatal("steady and original producer retirement", err)
	}
	if x.postCleanup != nil {
		x.exercisePostCleanupCorrection(t, boundary)
	}
	final, err := compileCreateNormalTailProductTarget(ctx, f.cfg, f.h, &x.program, f.runtime.Backend, true)
	if err != nil {
		t.Fatal("actual renewed steady target compiler", err)
	}
	if err := final.Plan.Verify(ctx, boundary); err != nil {
		t.Fatal("renewed steady full target verification", err)
	}
	t.Log("milestone: actual renewed steady compiler and final target verification accepted")
	productEnd := len(f.e.Journal.Entries)
	if x.postCleanup != nil {
		correction, err := readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e)
		if err != nil || correction == nil || correction.Completion == nil {
			t.Fatal("full postcleanup semantic reader", err)
		}
		productEnd = correction.Source.PrefixEntries
	}
	if n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, f.e.Journal.Entries[x.start:productEnd]); err != nil || n != productEnd-x.start {
		t.Fatal("full expiry recovery semantic reader", n, productEnd-x.start, err)
	}
	t.Log("milestone: steady and exact producer retirement accepted")
	if err = executeCreateNormalTailSequence(ctx, f.e, []func(context.Context) error{func(ctx context.Context) error { return final.Plan.Verify(ctx, boundary) }}); err != nil || f.e.Journal.CompletedAt.IsZero() {
		t.Fatal("original parent closure", err)
	}
	t.Log("milestone: original journal closed and archived")
	live, err := f.runtime.Backend.Inventory(t.Context(), f.p.Stack)
	if err != nil {
		t.Fatal("independent ordinary current inventory", err)
	}
	var current environmentIdentityBindings
	if x.postCleanup != nil {
		correction, readErr := readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e)
		if readErr != nil {
			t.Fatal("accepted correction for ordinary observation", readErr)
		}
		effective, releaseErr := postCleanupControllerObservationRelease(x.program.Release, correction, live)
		if releaseErr != nil {
			t.Fatal("accepted controller observation role", releaseErr)
		}
		// The final compiler uses create with its original handoff. This check
		// deliberately has no create context and must observe ordinary cleanup
		// and managed worker placement through the maintained upgrade reader.
		current, err = observeEnvironmentTargetBindings(t.Context(), f.cfg, effective, "upgrade", live)
	} else {
		current, err = observeEnvironmentTargetBindings(t.Context(), f.cfg, x.program.Release, "upgrade", live)
	}
	if err != nil {
		t.Fatal("ordinary health binding observation after renewal cleanup and original closure", err)
	}
	profiles := 0
	for _, pod := range current.Pods {
		if pod.ManagedMQTT != nil {
			profiles++
		}
	}
	if current.BootstrapCleanup == nil || current.BootstrapCleanup.SessionID != r.Source.SessionID || current.ProductIdentities != nil || profiles != 2 {
		t.Fatal("ordinary observation lost renewed cleanup or original managed worker placement")
	}
	if err := checkEnvironmentManagedMQTTConsumers(t.Context(), final.Plan, f.b.Read); err != nil {
		t.Fatal("ordinary managed MQTT target health contract", err)
	}
	t.Log("milestone: independent ordinary health binding/managed MQTT observation accepted without create context")
	x.verifyPython(t)
	t.Log("real original product protocol, expiry, renewal, phase-fenced successor, five same-identity mTLS ready ACKs, finalization, cleanup, steady, retirement and original closure accepted")

}

// Each actual cleanup interruption must preserve literal authority and its
// exact native prefix. Whole-history semantics are checked before and after
// cleanup, rather than repeating the same ancestry proof after every unlink.
func (x *productExpiryE2EFixture) cleanupFaultChecks(t *testing.T, renewal *productSessionRenewal) func(int) {
	t.Helper()
	f := x.f
	frozen := map[string][]byte{}
	if err := filepath.WalkDir(f.store.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == f.e.JournalPath {
			return nil
		}
		name := entry.Name()
		immutable := strings.HasPrefix(name, "normal-tail-") || strings.Contains(path, string(filepath.Separator)+"rollback"+string(filepath.Separator)) && (name == "plan.json" || name == "journal.json") || name == "source.json" || name == "observed.json" || name == "completed.json" || name == "installed.json" || name == "owner.json"
		if immutable {
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("immutable synthetic history became a symlink")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			frozen[path] = raw
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prefix := append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
	prefixSHA, writes := deploymentplan.Digest(prefix), deploymentplan.Digest(f.b.writes)
	specs, err := productSessionRenewalFinalizerSpecs(f.store, f.e, f.l, renewal)
	if err != nil || len(specs) != 6 || len(frozen) == 0 {
		t.Fatal("exact two-generation cleanup authority", err)
	}
	return func(fault int) {
		t.Helper()
		if x.renewals != 2 || x.cleanupStep != min(fault+1, 4) {
			t.Fatal("cleanup regenerated authority or skipped its native fault prefix")
		}
		if deploymentplan.Digest(f.b.writes) != writes {
			t.Fatal("cleanup repeated a Kubernetes create or update")
		}
		for path, raw := range frozen {
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, current) {
				t.Fatal("cleanup changed immutable synthetic history", filepath.Base(path), err)
			}
		}
		for subject, raw := range x.certificates {
			if !bytes.Equal(raw, f.states[subject]) {
				t.Fatal("cleanup reissued a permanent product identity", subject)
			}
		}
		var marker managedLateCustodyTombstone
		if json.Unmarshal(f.marker, &marker) != nil || marker.Schema != "rtk-cloud.bootstrap-credentials/v1" || len(marker.Files) != 3 || marker.Root != f.l.Root.RootFingerprint || deploymentplan.Digest(marker.Finalization) != deploymentplan.Digest(f.final) || marker.Files["bootstrap.key"] != environmentPreflightBytesSHA256(x.sourceKey) || marker.Files["bootstrap.csr"] != environmentPreflightBytesSHA256(x.sourceCSR) || marker.Files["bootstrap.crt"] != environmentPreflightBytesSHA256(x.files[renewal.Source.CertificatePath]) {
			t.Fatal("cleanup lost its genuine finalization and original key/CSR tombstone")
		}
		for i, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
			current, exists := x.files["/run/pki-bootstrap/"+name]
			_, mirrored := f.bootstrap[name]
			if exists != (i >= min(fault, 3)) || mirrored != exists || exists && (environmentPreflightBytesSHA256(current) != marker.Files[name] || f.bootstrap[name] != string(current)) {
				t.Fatal("unexpected native partial-erasure prefix", fault, name)
			}
		}
		entries := f.e.Journal.Entries
		if len(entries) < len(prefix)+1 || deploymentplan.Digest(entries[:len(prefix)]) != prefixSHA {
			t.Fatal("cleanup rewrote an earlier logical receipt")
		}
		tail := entries[len(prefix):]
		outer := tail[0]
		if outer.Key != "product-identities-"+productIdentityScope(f.l)+"-service-session-finalize" || outer.Phase != "bootstrap" || outer.State != "pending" || len(outer.Receipt) != 0 || len(tail) > 1+len(specs) {
			t.Fatal("cleanup did not retain its genuine pending finalizer")
		}
		for i, entry := range tail[1:] {
			intent, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(specs[i].Intent))
			if err != nil || entry.Key != specs[i].Key || entry.Phase != specs[i].Phase || entry.IntentSHA256 != intent || i > 0 && tail[i].State != "completed" || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State != "pending" && deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(specs[i].Receipt) {
				t.Fatal("cleanup left the exact typed conditional-delete prefix", i, err)
			}
		}
		if fault < 4 && len(tail) != 1 || fault == 4 && (len(tail) != 2 || tail[1].State != "pending") {
			t.Fatal("cleanup skipped or fabricated a conditional-delete response")
		}
		if fault == 4 {
			var intent struct {
				ResourceID string `json:"resource_id"`
			}
			if json.Unmarshal(mustProviderRecoveryJSON(specs[0].Intent), &intent) != nil || intent.ResourceID == "" || f.b.objects[intent.ResourceID] != nil {
				t.Fatal("lost conditional-delete response lacks its observed original-copy absence")
			}
		}
	}
}

func (x *productExpiryE2EFixture) applyRetire(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
	f := x.f
	full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
	p := f.plans[full]
	if p == nil {
		var live []deploymentplan.Object
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		var err error
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(x.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
		if err != nil {
			return err
		}
		f.plans[full] = p
	}
	if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, full, p); err != nil {
		return err
	}
	b := &productOuterTargetBackend{productProtocolBackend: f.b}

	q := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
	return executeCreateNormalTailStageBackend(ctx, f.store, f.h, full, p, b, q, fence, accept, io.Discard, func(context.Context) error { return nil })
}

// Real TLS handshakes retain the original permanent client keys. Registry and
// readiness endpoints are isolated adapters; native registry enforcement is
// exercised separately by the rendered Job / PostgreSQL bridge test.
func (x *productExpiryE2EFixture) readyACKs(t *testing.T, r *productSessionRenewal) {
	t.Helper()
	f := x.f
	pool := x509.NewCertPool()
	pool.AddCert(f.root)
	seen := map[string]bool{}
	fingerprints := map[string]string{}
	for _, o := range f.l.Outputs {
		var state struct {
			Current struct {
				Chain string `json:"certificate_chain_pem"`
				Key   string `json:"private_key_pem"`
			} `json:"current"`
		}
		if json.Unmarshal(f.states[o.Subject], &state) != nil {
			t.Fatal("original product state")
		}
		pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
		if err != nil {
			t.Fatal(err)
		}
		fingerprints[environmentPreflightBytesSHA256(pair.Certificate[0])] = o.Subject
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.TLS == nil || len(q.TLS.VerifiedChains) == 0 {
			http.Error(w, "identity", 403)
			return
		}
		subject := fingerprints[environmentPreflightBytesSHA256(q.TLS.PeerCertificates[0].Raw)]
		if subject == "" {
			http.Error(w, "foreign identity", 403)
			return
		}
		if q.URL.Path == "/readyz" {
			seen[subject] = true
			w.WriteHeader(204)
			return
		}
		if q.URL.Path != "/v1/pki/issuers/"+r.Installed.Ceremony.Intermediate.ID+"/bootstrap-ack" {
			http.Error(w, "issuer", 404)
			return
		}
		var body struct {
			Session string `json:"session_id"`
		}
		if json.NewDecoder(q.Body).Decode(&body) != nil || body.Session != r.Installed.Ceremony.BootstrapSessionID || !seen[subject] {
			http.Error(w, "binding or readiness", 409)
			return
		}
		if !slices.Contains(f.installed, subject) {
			f.installed = append(f.installed, subject)
		}
		w.WriteHeader(204)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	for _, o := range f.l.Outputs {
		var state struct {
			Current struct {
				Chain string `json:"certificate_chain_pem"`
				Key   string `json:"private_key_pem"`
			} `json:"current"`
		}
		_ = json.Unmarshal(f.states[o.Subject], &state)
		pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
		if err != nil {
			t.Fatal(err)
		}
		client := srv.Client()
		transport := client.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig.Certificates = []tls.Certificate{pair}
		client.Transport = transport
		// Ready is checked against the genuinely accepted workload and its exact ACK inputs.
		var owner deploymentplan.Object
		for _, candidate := range f.b.objects {
			if candidate["kind"] != "Deployment" {
				continue
			}
			counts := map[string]int{}
			if er := resolveCreateNormalTailProductACK(deploymentplan.Clone(candidate), map[string]environmentCreateProductIdentity{o.ID: o}, counts, r.Installed.Ceremony, true); er == nil && counts[o.ID] == 1 {
				if owner != nil {
					t.Fatal("ambiguous live consumer", o.ID)
				}
				owner = candidate
			}
		}
		if owner == nil {
			t.Fatal("product workload absent", o.ID)
		}
		c := mqttFoundationPrimary(owner)
		if mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != r.Installed.Ceremony.BootstrapSessionID {
			t.Fatal("workload ACK targets old session", o.ID)
		}
		for _, step := range []struct{ path, body string }{{"/v1/pki/issuers/" + r.Installed.Ceremony.Intermediate.ID + "/bootstrap-ack", `{"session_id":"` + r.Installed.Ceremony.BootstrapSessionID + `"}`}, {"/readyz", ""}, {"/v1/pki/issuers/" + r.Installed.Ceremony.Intermediate.ID + "/bootstrap-ack", `{"session_id":"` + r.Installed.Ceremony.BootstrapSessionID + `"}`}} {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+step.path, strings.NewReader(step.body))
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if step.path != "/readyz" && !seen[o.Subject] {
				if response.StatusCode != 409 {
					t.Fatal("ACK accepted before readiness")
				}
			} else if response.StatusCode != 204 {
				t.Fatal("same identity ready ACK", response.StatusCode)
			}
		}
	}
	sort.Strings(f.installed)
	if !slices.Equal(f.installed, f.subjects) {
		t.Fatal("not all five installed")
	}
}

func (x *productExpiryE2EFixture) verifyPython(t *testing.T) {
	t.Helper()
	helper := os.Getenv("RTK_PRODUCT_FINAL_READER")
	if helper == "" {
		// The maintained Go assertions above exercise the complete producer and
		// recovery flow. An independently supplied operator verifier is an
		// additional qualification and is not a dependency of repository tests.
		t.Log("optional independent Python verifier not selected; Go producer/recovery assertions completed")
		return
	}
	f := x.f
	payload := filepath.Join(t.TempDir(), "expiry-reader.json")
	root, directory := f.store.Root, f.e.Directory
	if destination := os.Getenv("RTK_PRODUCT_EXPIRY_EXPORT"); destination != "" {
		if !productSyntheticExportDestination(destination) {
			t.Fatal("synthetic export must use isolated local test directory")
		}
		if err := os.Mkdir(destination, 0700); err != nil {
			t.Fatal(err)
		}
		exported := filepath.Join(destination, "dev")
		if err := filepath.WalkDir(f.store.Root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(f.store.Root, path)
			if err != nil {
				return err
			}
			target := filepath.Join(exported, relative)
			if entry.IsDir() {
				return os.Mkdir(target, 0700)
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("synthetic export refuses symlink")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, raw, 0600)
		}); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(f.store.Root, f.e.Directory)
		if err != nil {
			t.Fatal(err)
		}
		root, directory = exported, filepath.Join(exported, relative)
		payload = filepath.Join(destination, "expiry-reader.json")
	}
	if err := os.WriteFile(payload, mustProviderRecoveryJSON(deploymentplan.Object{"root": root, "directory": directory, "owner": deploymentplan.Digest(f.e.owner), "journal": f.e.Journal}), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import importlib.util,json,pathlib,sys
s=importlib.util.spec_from_file_location("final_reader",sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
x=json.load(open(sys.argv[2]));m.ROOT=pathlib.Path(x['root']);m.PARENT=x['journal']['plan_sha256'];m.OWNER_SHA=x['owner'];m.JOURNAL=str(pathlib.Path(x['directory']).relative_to(m.ROOT)/'journal.json')
runtime_count=len(list(pathlib.Path(x['directory']).glob('normal-tail-workload-forward-[0-9][0-9][0-9][0-9].json')))
sources=m.workload_allocation_sources(x['journal']);assert len(sources)==2+runtime_count
claims=m.planned_volume_claims(x['journal']);assert len(claims)==(12 if runtime_count else 6)
m.closure(require_result=False)
print('renewed workload independent allocation sources:',len(sources))
`
	cmd := exec.Command("python3", "-B", "-c", script, helper, payload)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("independent Python renewal reader: %v %s", err, out)
	}
	t.Log("explicit independent Python renewal reader accepted")
}

// Optional retained exports must remain below the operating system's temporary
// directory. Resolve the existing parent so a symlink cannot select live state.
func productSyntheticExportDestination(destination string) bool {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return false
	}
	temporary, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(temporary, parent)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}

func (x *productExpiryE2EFixture) assertCurrentPrefix(t *testing.T) {
	t.Helper()
	f := x.f
	n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, f.e.Journal.Entries[x.start:])
	if err != nil || n != len(f.e.Journal.Entries)-x.start {
		t.Fatal("genuine persisted renewal stage prefix", n, len(f.e.Journal.Entries)-x.start, err)
	}
}

// Retain only generated test material when a late assertion fails. This permits
// parser/source diagnosis without replaying the native-boundary lifecycle.
func (x *productExpiryE2EFixture) exportFailedReaderCheckpoint(t *testing.T) error {
	destination := os.Getenv("RTK_PRODUCT_EXPIRY_EXPORT")
	if !productSyntheticExportDestination(destination) {
		return errors.New("explicit isolated synthetic export path required")
	}
	destination += "-failed-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	f := x.f
	root := filepath.Join(destination, "dev")
	err := filepath.WalkDir(f.store.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(f.store.Root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("synthetic export refuses symlink")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(f.store.Root, f.e.Directory)
	if err != nil {
		return err
	}
	payload := deploymentplan.Object{"root": root, "directory": filepath.Join(root, rel), "owner": deploymentplan.Digest(f.e.owner), "journal": f.e.Journal}
	if err := os.WriteFile(filepath.Join(destination, "expiry-reader.json"), mustProviderRecoveryJSON(payload), 0600); err != nil {
		return err
	}
	t.Log("retained generated partial reader checkpoint", filepath.Join(destination, "expiry-reader.json"))
	return nil
}
