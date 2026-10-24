package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateAppRootCRLACKReaderUsesAdmittedAppNativeSQL(t *testing.T) {
	for _, mode := range []string{"root2", "ICA3", "foreign-issuer", "MQTT-root", "TLS-root", "wrong-domain", "foreign-admitted-ICA", "wrong-ICA-number", "wrong-Root-number", "changed-review", "wrong-parent", "changed-caller-record", "changed-caller-certificate", "invalid-digest", "foreign-native-issuer", "foreign-native-digest", "missing-native-ACK", "duplicate-native-consumer", "foreign-native-consumer", "future-native-ACK", "native-ACK-before-CRL", "native-ACK-at-expiry", "unknown-native-field", "backend-failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, original := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, original, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			f := newManagedLateBootstrapChainFixture(t)
			root, ica := f.ceremony.Root, f.ceremony.Intermediate
			root.ID, root.Domain = managedLateAppRootID, "app"
			ica.ID, ica.Domain = managedLateAppIntermediateID, "app"
			ica.Environment, ica.Kind, ica.Status = "dev", "intermediate", "active"
			ica.RootFingerprint, ica.ChainPEM = root.RootFingerprint, ica.Certificate+root.Certificate
			root2 := signedRootCRLFixture(t, root, f.rootKey, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "2", ThisUpdate: f.now.Add(-time.Minute), NextUpdate: f.now.Add(24 * time.Hour)})
			ica3 := signedRootCRLFixture(t, ica, f.icaKey, createRootCRLRequest{IssuerID: ica.ID, Fingerprint: environmentPreflightBytesSHA256(f.ica), Number: "3", ThisUpdate: f.now.Add(-time.Hour), NextUpdate: f.now.Add(48 * time.Hour)})
			a := createAppRootCRLArtifact{Schema: createAppRootCRLSchema + "/artifact", Plan: createAppRootCRLPlan{Schema: createAppRootCRLSchema, Parent: e.Journal.PlanSHA256, Root: root, Intermediate: ica, Signed: root2, IntermediateCRL: ica3}}
			a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: deploymentplan.Digest(a.Plan)}
			issuer, record := root, root2
			if mode == "ICA3" {
				issuer, record = ica, ica3
			}
			rows := []managedLatePriorACK{{IssuerID: issuer.ID, Digest: record.Digest, Consumer: "pkibroker", At: f.now.Add(-30 * time.Second)}, {IssuerID: issuer.ID, Digest: record.Digest, Consumer: "video-cloud-api", At: f.now.Add(-20 * time.Second)}}
			beforeQuery := false
			switch mode {
			case "foreign-issuer", "MQTT-root", "TLS-root":
				beforeQuery = true
				issuer.ID = "99999999-9999-4999-8999-999999999999"
				if mode == "MQTT-root" {
					issuer.ID = "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
				} else if mode == "TLS-root" {
					issuer.ID = "734487b8-abcf-40be-a813-8b52c64f7954"
				}
			case "wrong-domain":
				beforeQuery = true
				a.Plan.Root.Domain = "service"
				a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Plan)
			case "foreign-admitted-ICA", "wrong-ICA-number", "wrong-Root-number":
				beforeQuery = true
				if mode == "foreign-admitted-ICA" {
					a.Plan.Intermediate.ID = "99999999-9999-4999-8999-999999999999"
				} else if mode == "wrong-ICA-number" {
					a.Plan.IntermediateCRL.Number = "2"
				} else {
					a.Plan.Signed.Number = "1"
				}
				a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Plan)
			case "changed-review":
				beforeQuery = true
				a.Qualification.PlanSHA256 = strings.Repeat("f", 64)
			case "wrong-parent":
				beforeQuery = true
				a.Plan.Parent = strings.Repeat("f", 64)
				a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Plan)
			case "changed-caller-record":
				beforeQuery = true
				record.Digest = strings.Repeat("f", 64)
			case "changed-caller-certificate":
				beforeQuery = true
				issuer.Certificate = "foreign"
			case "invalid-digest":
				beforeQuery = true
				record.Digest = "' OR true --"
				a.Plan.Signed = record
				a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Plan)
			case "foreign-native-issuer":
				rows[0].IssuerID = ica.ID
			case "foreign-native-digest":
				rows[0].Digest = ica3.Digest
			case "missing-native-ACK":
				rows = rows[:1]
			case "duplicate-native-consumer":
				rows[1].Consumer = rows[0].Consumer
			case "foreign-native-consumer":
				rows[0].Consumer = "foreign"
			case "future-native-ACK":
				rows[0].At = f.now.Add(time.Minute)
			case "native-ACK-before-CRL":
				rows[0].At = record.ThisUpdate.Add(-time.Second)
			case "native-ACK-at-expiry":
				rows[0].At = record.NextUpdate
			}
			dir := t.TempDir()
			response, args := filepath.Join(dir, "rows.json"), filepath.Join(dir, "args.txt")
			raw, _ := json.Marshal(rows)
			if mode == "unknown-native-field" {
				raw = []byte(strings.Replace(string(raw), `"consumer_id":`, `"unreviewed":true,"consumer_id":`, 1))
			}
			if err := os.WriteFile(response, raw, 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$RTK_APP_ACK_TEST_ARGS"
for query do :; done
test "$query" = "$RTK_APP_ACK_TEST_QUERY" || exit 71
test "$RTK_APP_ACK_TEST_FAIL" != yes || exit 72
cat "$RTK_APP_ACK_TEST_ROWS"
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_APP_ACK_TEST_ARGS", args)
			t.Setenv("RTK_APP_ACK_TEST_ROWS", response)
			t.Setenv("RTK_APP_ACK_TEST_QUERY", "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='"+issuer.ID+"' AND digest='"+record.Digest+"'")
			fail := ""
			if mode == "backend-failure" {
				fail = "yes"
			}
			t.Setenv("RTK_APP_ACK_TEST_FAIL", fail)
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-test-kubeconfig")}}
			got, err := readManagedLateAppRootCRLACKs(context.Background(), cfg, runtime, a, issuer, record)
			valid := mode == "root2" || mode == "ICA3"
			if (err == nil) != valid || valid && deploymentplan.Digest(got) != deploymentplan.Digest(rows) {
				t.Fatal("actual App ACK SQL reader accepted wrong authority or refused genuine native rows", mode, err)
			}
			captured, readErr := os.ReadFile(args)
			if beforeQuery {
				if !os.IsNotExist(readErr) {
					t.Fatal("foreign review/issuer/digest reached native SQL")
				}
			} else if readErr != nil || !strings.Contains(string(captured), "PGOPTIONS=-c default_transaction_read_only=on") || !strings.Contains(string(captured), "pki_crl_acknowledgments WHERE issuer_id='") || strings.Contains(string(captured), "pki_bundle_acknowledgments") {
				t.Fatal("production helper failed to inspect genuine read-only CRL ACK table", readErr)
			}
			if err := e.Check(context.Background()); err != nil || len(e.Journal.Entries) != 0 {
				t.Fatal("read-only ACK inspection mutated original parent", err)
			}
		})
	}
}

func managedLateAppConsumerFixture(t *testing.T, name string) (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer, deploymentplan.Resource, deploymentplan.Resource, []deploymentplan.Object, string) {
	t.Helper()
	f := newManagedLateBootstrapChainFixture(t)
	root := f.ceremony.Root
	root.ID, root.Domain = managedLateAppRootID, "app"
	root.Document = deploymentplan.Object{"issuer_id": root.ID, "environment": "dev", "trust_domain": "app", "kind": "root", "status": "active", "certificate_pem": root.Certificate, "chain_pem": root.ChainPEM, "certificate_fingerprint_sha256": root.RootFingerprint, "trust_bundle_version": root.BundleVersion, "signer_provider": "offline"}
	ica := environmentCreateRegisteredIssuer{ID: "88888888-8888-4888-8888-888888888888", Environment: "dev", Domain: "app", Kind: "intermediate", Status: "active", Certificate: f.ceremony.Intermediate.Certificate, ChainPEM: f.ceremony.Intermediate.Certificate + root.Certificate, RootFingerprint: root.RootFingerprint}
	ica.BundleVersion = environmentPreflightBytesSHA256([]byte(ica.ChainPEM))
	ica.Document = deploymentplan.Object{"issuer_id": ica.ID, "environment": "dev", "trust_domain": "app", "kind": "intermediate", "status": "active", "certificate_pem": ica.Certificate, "chain_pem": ica.ChainPEM, "trust_bundle_version": ica.BundleVersion, "parent_issuer_id": root.ID}
	_, objects, digest := targetWorkloadFixture(t, "dev", "Deployment")
	for i, object := range objects {
		raw, _ := json.Marshal(object)
		text := strings.NewReplacer("video-cloud-dev-application", "video-cloud-dev-video-cloud", "original-controller", "11111111-1111-4111-8111-111111111111", "original-pod", "22222222-2222-4222-8222-222222222222", "original-replicaset", "33333333-3333-4333-8333-333333333333", "application-selected", name+"-selected", "application-0", name+"-0", `"application"`, `"`+name+`"`).Replace(string(raw))
		if json.Unmarshal([]byte(text), &objects[i]) != nil {
			t.Fatal("fixture native objects")
		}
	}
	controller, pod := objects[0], objects[1]
	container, key, path, claim := "app", "api-crls.json", "/run/pki-state/app-root-crl.json", "video-cloud-api-app-pki-trust"
	setting := "VIDEO_CLOUD_AUTH_CRL_MANIFEST"
	if name == "mqtt-pki" {
		container, key, path, claim, setting = "pkibroker", "broker-crls.json", "/run/pki-state/app-crl.json", "mqtt-pki-trust", "PKI_BROKER_APP_CRL_MANIFEST"
	}
	podSpec := environmentPKIPod(controller)
	c := managedNamedField(podSpec["containers"], "app")
	c["name"] = container
	c["env"] = environmentCreateEnv(map[string]string{setting: "/run/pki-app/" + key})
	c["volumeMounts"] = []any{environmentCreateServiceMount("pki-app", "/run/pki-app", true), environmentCreateServiceMount("pki-state", "/run/pki-state", false)}
	podSpec["volumes"] = []any{environmentCreateServiceVolume("pki-app", "configMap", "pki-create-app-trust"), environmentCreateServiceVolume("pki-state", "persistentVolumeClaim", claim)}
	pod["spec"] = deploymentplan.Clone(podSpec)
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["name"] = container
	resource := deploymentplan.Resource{ID: deploymentplan.ID(controller), UID: "11111111-1111-4111-8111-111111111111", Desired: deploymentplan.Projection(controller)}
	cmBody := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-app-trust", "namespace": "video-cloud-dev-video-cloud", "uid": "44444444-4444-4444-8444-444444444444"}, "data": deploymentplan.Object{"roots.pem": root.Certificate, key: environmentCreateServiceCRLManifest(root, path, ica)}}
	cm := deploymentplan.Resource{ID: deploymentplan.ID(cmBody), UID: "44444444-4444-4444-8444-444444444444", Desired: deploymentplan.Projection(cmBody)}
	claimBody := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": "video-cloud-dev-video-cloud", "uid": "55555555-5555-4555-8555-555555555555"}, "status": deploymentplan.Object{"phase": "Bound"}}
	objects = append(objects, cmBody, claimBody)
	return root, ica, cm, resource, objects, digest
}

func TestManagedLateAppRootCRLTargetsPreserveManifestClaimsAndRuntimeImages(t *testing.T) {
	for _, name := range []string{"video-cloud-api-app-pki", "mqtt-pki"} {
		t.Run(name, func(t *testing.T) {
			root, ica, cm, resource, objects, digest := managedLateAppConsumerFixture(t, name)
			before := deploymentplan.Digest([]any{cm, resource, objects})
			target, err := managedLateAppRootCRLTargetBinding(root, ica, cm, resource)
			if err != nil {
				t.Fatal(err)
			}
			if err = managedLateAppRootCRLInventoryTarget(objects, &target, true); err != nil {
				t.Fatal(err)
			}
			calls := 0
			if err = verifyManagedLateAppRootCRLPodImages(context.Background(), target, objects, func(context.Context, string) (map[string]bool, error) {
				calls++
				return map[string]bool{digest: true}, nil
			}); err != nil || calls != 1 {
				t.Fatal("immutable runtime images not verified", err, calls)
			}
			if target.Paths[1] != strings.TrimSuffix(target.Paths[0], ".json")+"-"+ica.ID+".json" || target.ClaimUID == "" || target.PodUID == "" {
				t.Fatal("original issuer/cache/PVC/Pod binding missing")
			}
			if deploymentplan.Digest([]any{cm, resource, objects}) != before {
				t.Fatal("existing producer or native snapshot mutated")
			}
			deploymentplan.Map(objects[1]["status"])["conditions"] = []any{deploymentplan.Object{"type": "Ready", "status": "False"}}
			if managedLateAppRootCRLInventoryTarget(objects, &target, false) != nil {
				t.Fatal("expired prior-dependent health invents publication prerequisite")
			}
			if managedLateAppRootCRLInventoryTarget(objects, &target, true) == nil {
				t.Fatal("fresh publication accepted without real readiness")
			}
			deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(objects[1]["status"])["containerStatuses"])[0])["imageID"] = "containerd://sha256:" + strings.Repeat("f", 64)
			if verifyManagedLateAppRootCRLPodImages(context.Background(), target, objects, func(context.Context, string) (map[string]bool, error) { return map[string]bool{digest: true}, nil }) == nil {
				t.Fatal("unready prior bypassed runtime image proof")
			}
		})
	}
}

func TestManagedLateAppRootCRLTargetBindingRejectsPublicTrustOrPrivateClaimDrift(t *testing.T) {
	for _, mode := range []string{"foreign-CM", "foreign-root-PEM", "foreign-issuer", "foreign-path", "foreign-bundle-version", "reordered-authorities", "wrong-claim", "read-only-cache", "subpath-cache", "foreign-manifest-env", "foreign-container", "foreign-owner-UID"} {
		t.Run(mode, func(t *testing.T) {
			root, ica, cm, resource, _, _ := managedLateAppConsumerFixture(t, "video-cloud-api-app-pki")
			c := managedNamedField(environmentPKIPod(resource.Desired)["containers"], "app")
			switch mode {
			case "foreign-CM":
				deploymentplan.Map(managedNamedField(environmentPKIPod(resource.Desired)["volumes"], "pki-app")["configMap"])["name"] = "foreign"
			case "foreign-root-PEM":
				deploymentplan.Map(cm.Desired["data"])["roots.pem"] = "foreign"
			case "wrong-claim":
				deploymentplan.Map(managedNamedField(environmentPKIPod(resource.Desired)["volumes"], "pki-state")["persistentVolumeClaim"])["claimName"] = "foreign"
			case "read-only-cache":
				managedNamedField(c["volumeMounts"], "pki-state")["readOnly"] = true
			case "subpath-cache":
				managedNamedField(c["volumeMounts"], "pki-state")["subPath"] = "foreign"
			case "foreign-manifest-env":
				managedNamedField(c["env"], "VIDEO_CLOUD_AUTH_CRL_MANIFEST")["value"] = "/foreign/api-crls.json"
			case "foreign-container":
				c["name"] = "foreign"
			case "foreign-owner-UID":
				resource.UID = ""
			default:
				var entries []deploymentplan.Object
				_ = json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(cm.Desired["data"])["api-crls.json"])), &entries)
				switch mode {
				case "foreign-issuer":
					deploymentplan.Map(entries[0]["issuer"])["issuer_id"] = "99999999-9999-4999-8999-999999999999"
				case "foreign-path":
					entries[0]["state_path"] = "/foreign/root.json"
				case "foreign-bundle-version":
					deploymentplan.Map(entries[0]["issuer"])["trust_bundle_version"] = strings.Repeat("f", 64)
				case "reordered-authorities":
					slices.Reverse(entries)
				}
				raw, _ := json.Marshal(entries)
				deploymentplan.Map(cm.Desired["data"])["api-crls.json"] = string(raw)
			}
			if _, err := managedLateAppRootCRLTargetBinding(root, ica, cm, resource); err == nil {
				t.Fatal("foreign public/private scope admitted")
			}
		})
	}
}

func TestManagedLateAppRootCRLNativeCacheRequiresExactFreshSignedRootAndICA(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	root := f.ceremony.Root
	root.ID, root.Domain = managedLateAppRootID, "app"
	root2 := signedRootCRLFixture(t, root, f.rootKey, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "2", ThisUpdate: f.now.Add(-time.Minute), NextUpdate: f.now.Add(24 * time.Hour)})
	for _, mode := range []string{"valid", "foreign-pin", "changed-record", "changed-PEM", "terminal", "retained", "expired", "unknown-field", "root2-as-historical", "foreign-root1-as-historical"} {
		t.Run(mode, func(t *testing.T) {
			record := root2
			state := deploymentplan.Object{"issuer_fingerprint": root.RootFingerprint, "crl": record}
			historical := false
			now := f.now
			switch mode {
			case "foreign-pin":
				state["issuer_fingerprint"] = strings.Repeat("f", 64)
			case "changed-record":
				bad := record
				bad.Number = "3"
				state["crl"] = bad
			case "changed-PEM":
				record.PEM = "foreign"
				state["crl"] = record
			case "terminal":
				state["terminal_issuer_id"] = root.ID
			case "retained":
				state["retained_crl"] = record
			case "expired":
				now = record.NextUpdate
			case "unknown-field":
				state["unreviewed"] = true
			case "root2-as-historical":
				historical = true
			case "foreign-root1-as-historical":
				record = signedRootCRLFixture(t, root, f.rootKey, createRootCRLRequest{IssuerID: root.ID, Fingerprint: root.RootFingerprint, Number: "1", ThisUpdate: f.now.Add(-2 * time.Hour), NextUpdate: f.now.Add(-time.Hour)})
				state["crl"] = record
				historical = true
			}
			raw, _ := json.Marshal(state)
			err := validateManagedLateAppRootCRLCache(raw, root, record, historical, now)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("wrong signed cache result %s: %v", mode, err)
			}
		})
	}
	ica := environmentCreateRegisteredIssuer{ID: "88888888-8888-4888-8888-888888888888", Domain: "app", Kind: "intermediate", Status: "active", Certificate: f.ceremony.Intermediate.Certificate, ChainPEM: f.ceremony.Intermediate.Certificate + root.Certificate, RootFingerprint: root.RootFingerprint}
	ica3 := signedRootCRLFixture(t, ica, f.icaKey, createRootCRLRequest{IssuerID: ica.ID, Fingerprint: root.RootFingerprint, Number: "3", ThisUpdate: f.now.Add(-time.Minute), NextUpdate: f.now.Add(24 * time.Hour)})
	raw, _ := json.Marshal(deploymentplan.Object{"issuer_fingerprint": environmentPreflightBytesSHA256(f.icaCertificate.Raw), "crl": ica3})
	if err := validateManagedLateAppRootCRLCache(raw, ica, ica3, false, f.now); err != nil {
		t.Fatal("genuine original ICA3 refused", err)
	}
}

func TestManagedLateAppRootCRLTargetRejectsReplacementLineageAndDeletedClaim(t *testing.T) {
	for _, mode := range []string{"replacement-Pod", "foreign-RS", "second-Pod", "deleted-owner", "deleted-claim", "claim-unbound"} {
		t.Run(mode, func(t *testing.T) {
			root, ica, cm, resource, objects, _ := managedLateAppConsumerFixture(t, "video-cloud-api-app-pki")
			target, err := managedLateAppRootCRLTargetBinding(root, ica, cm, resource)
			if err != nil {
				t.Fatal(err)
			}
			if managedLateAppRootCRLInventoryTarget(objects, &target, true) != nil {
				t.Fatal("valid native fixture")
			}
			switch mode {
			case "replacement-Pod":
				deploymentplan.Map(objects[1]["metadata"])["uid"] = "66666666-6666-4666-8666-666666666666"
			case "foreign-RS":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(objects[1]["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			case "second-Pod":
				extra := deploymentplan.Clone(objects[1])
				deploymentplan.Map(extra["metadata"])["name"] = "extra"
				objects = append(objects, extra)
			case "deleted-owner":
				deploymentplan.Map(objects[0]["metadata"])["deletionTimestamp"] = "now"
			case "deleted-claim":
				deploymentplan.Map(objects[len(objects)-1]["metadata"])["deletionTimestamp"] = "now"
			case "claim-unbound":
				deploymentplan.Map(objects[len(objects)-1]["status"])["phase"] = "Pending"
			}
			if managedLateAppRootCRLInventoryTarget(objects, &target, false) == nil {
				t.Fatal("expired Root prior admitted different ownership")
			}
		})
	}
}
