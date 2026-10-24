package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native client enrollment producer already signed this key/certificate.
// Once its actual workload owns the claim, model the unchanged native private
// state and digest-only inspection. No new key, CSR, issuance or installed ACK
// is produced here. Private bytes stay inside the isolated fixture root.
func installHistoricalLogIdentityProtocol(t *testing.T, device *deviceTrustExecutionContract) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	if f.protocol["historical_log_identity"] != nil {
		t.Fatal("original native Log identity already installed")
	}
	clients, ok := f.protocol["historical_clients"].([]managedSessionClient)
	keys, keysOK := f.protocol["historical_client_keys"].(map[string]string)
	if !ok || !keysOK {
		t.Fatal("original native Log enrollment producer unavailable")
	}
	var original managedSessionClient
	for _, client := range clients {
		if client.Subject == logACKSubject {
			if original.Subject != "" {
				t.Fatal("multiple original Log identities")
			}
			original = client
		}
	}
	if original.Subject == "" || keys[original.Request] == "" || original.Issuer != f.service.Intermediate.ID {
		t.Fatal("original Log certificate and key missing")
	}
	chain := original.Certificate + f.service.Intermediate.ChainPEM
	pair, err := tls.X509KeyPair([]byte(chain), []byte(keys[original.Request]))
	leaf, leafErr := parseFirstCertificatePEM([]byte(chain))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	if err != nil || leafErr != nil || issuerErr != nil || len(pair.Certificate) != 3 || leaf.CheckSignatureFrom(issuer) != nil || leaf.Subject.CommonName != logACKSubject || environmentPreflightBytesSHA256(leaf.Raw) != original.Fingerprint || !leaf.NotAfter.After(time.Now()) {
		t.Fatal("original native Log signed identity differs")
	}
	claimID := "PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/video-cloud-logingester-service-state"
	claim := f.native.objects[claimID]
	claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
	if !environmentBootstrapOwnerUUID.MatchString(claimUID) {
		t.Fatal("original Log claim has no native owner")
	}
	path := filepath.Join("deployment/native-fixture/log", claimUID, "client.json")
	body := map[string]any{"version": 1, "subject": logACKSubject, "current": map[string]any{"private_key_pem": keys[original.Request], "certificate_chain_pem": chain, "installed_at": time.Now().UTC()}}
	if err := f.store.write(path, mustProviderRecoveryJSON(body), false); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(f.store.Root, path)
	root := f.service.Root.RootFingerprint
	f.protocol["historical_log_identity"] = map[string]string{"claim_uid": claimUID, "path": absolute, "root": root}
	f.publish(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fail := func() { http.Error(w, "original native Log inspection unavailable", http.StatusConflict) }
		var request struct {
			Args []string `json:"args"`
		}
		if req.Method != http.MethodPost || json.NewDecoder(req.Body).Decode(&request) != nil {
			fail()
			return
		}
		a := request.Args
		index := -1
		for i, v := range a {
			if v == "inspect" {
				index = i
				break
			}
		}
		if index < 0 || len(a) != index+4 || a[index+1] != logACKStatePath || a[index+2] != logACKSubject || a[index+3] != root {
			fail()
			return
		}
		var native struct {
			Objects map[string]deploymentplan.Object `json:"objects"`
		}
		rawNative, err := readBoundedEnvironmentFile(f.protocolPath, true)
		if err != nil || json.Unmarshal(rawNative, &native) != nil {
			fail()
			return
		}
		podName, namespace, containerName := "", "", ""
		for i, v := range a {
			if i+1 < len(a) && v == "exec" {
				podName = strings.TrimPrefix(a[i+1], "pod/")
			}
			if i+1 < len(a) && v == "--namespace" {
				namespace = a[i+1]
			}
			if i+1 < len(a) && v == "--container" {
				containerName = a[i+1]
			}
		}
		pod := native.Objects["Pod/"+namespace+"/"+podName]
		owner, ownerErr := environmentPodController(pod, native.Objects)
		if ownerErr != nil || owner != "Deployment/"+f.p.Plan.Stack+"-video-cloud/video-cloud-logingester" || deploymentplan.Map(native.Objects[claimID]["metadata"])["uid"] != claimUID {
			fail()
			return
		}
		mounted := false
		for _, rawContainer := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
			container := deploymentplan.Map(rawContainer)
			if containerName != "app" || container["name"] != containerName {
				continue
			}
			for _, rawMount := range certIssuerObjectList(container["volumeMounts"]) {
				mount := deploymentplan.Map(rawMount)
				if !strings.HasPrefix(logACKStatePath, deploymentplan.String(mount["mountPath"])+"/") || mount["subPath"] != nil {
					continue
				}
				for _, rawVolume := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["volumes"]) {
					volume := deploymentplan.Map(rawVolume)
					if volume["name"] == mount["name"] && deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] == "video-cloud-logingester-service-state" {
						mounted = true
					}
				}
			}
		}

		if !mounted {
			fail()
			return
		}
		raw, err := readBoundedEnvironmentFile(absolute, true)
		var state struct {
			Version int    `json:"version"`
			Subject string `json:"subject"`
			Current *struct {
				Key   string `json:"private_key_pem"`
				Chain string `json:"certificate_chain_pem"`
			} `json:"current"`
			Pending any `json:"pending"`
		}
		if err != nil || json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Subject != logACKSubject || state.Current == nil || state.Pending != nil {
			fail()
			return
		}
		pair, pairErr := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
		current, currentErr := parseFirstCertificatePEM([]byte(state.Current.Chain))
		if pairErr != nil || currentErr != nil || len(pair.Certificate) != 3 || current.CheckSignatureFrom(issuer) != nil || current.Subject.CommonName != logACKSubject || current.NotBefore.After(time.Now()) || !current.NotAfter.After(time.Now()) || environmentPreflightBytesSHA256(pair.Certificate[2]) != root {
			fail()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logACKNativeIdentity{Subject: logACKSubject, Fingerprint: environmentPreflightBytesSHA256(current.Raw), PublicKeySHA256: environmentPreflightBytesSHA256(current.RawSubjectPublicKeyInfo), RootSHA256: root, StateSHA256: environmentPreflightBytesSHA256(raw)})
	}))
	t.Cleanup(server.Close)
	originalCommand := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys,urllib.request
original=%q
endpoint=%q
a=sys.argv[1:];joined=' '.join(a)
if '/app/serviceidentity-bootstrap' in a and 'inspect' in a:
 request=urllib.request.Request(endpoint,data=json.dumps({'args':a}).encode(),headers={'Content-Type':'application/json'},method='POST')
 with urllib.request.urlopen(request,timeout=10) as response:sys.stdout.buffer.write(response.read())
elif 'registry-read' in a and "SELECT json_build_object('certificate_pem',certificate_pem,'fingerprint',fingerprint)" in joined and "subject='service:video-cloud-logingester'" in joined:
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:native=json.load(source)
 rows=[row for row in native['historical_clients'] if row['subject']=='service:video-cloud-logingester']
 if len(rows)!=1:sys.exit('original native Log issuance not unique')
 print(json.dumps({'certificate_pem':rows[0]['certificate_pem'],'fingerprint':rows[0]['fingerprint']}))
else:os.execv(original,[original]+a)
`, originalCommand, server.URL)
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHistoricalLogInspectorRetainsNativeSignedIdentityAndRejectsDrift(t *testing.T) {
	// This is the native inspector boundary only, not a full late source. The
	// workload child is genuinely accepted and its original private identity
	// uses the same Service signer as the complete historical fixture.
	f := newAcceptedCoreExecutionContract(t)
	certificate, key, csr := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, logACKSubject, nil)
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	if err != nil {
		t.Fatal(err)
	}
	row := managedSessionClient{Subject: logACKSubject, Caller: logACKSubject, Request: "isolated-original-log-client", Issuer: f.service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "succeeded", CSR: csr, Certificate: certificate}
	f.protocol["historical_clients"] = []managedSessionClient{row}
	f.protocol["historical_client_keys"] = map[string]string{row.Request: key}
	namespace := f.p.Plan.Stack + "-video-cloud"
	claim := environmentCreateServiceClaimDefinition(f.cfg, namespace, "video-cloud-logingester-service-state")
	labels := deploymentplan.Object{"app.kubernetes.io/name": "video-cloud-logingester"}
	podSpec := deploymentplan.Object{
		"containers": []any{deploymentplan.Object{"name": "app", "image": f.p.Plan.Bootstrap.Image, "command": []any{"/app/logingester"}, "volumeMounts": []any{environmentCreateServiceMount("private", "/var/lib/video-cloud-log-service", false)}}},
		"volumes":    []any{environmentCreateServiceVolume("private", "persistentVolumeClaim", "video-cloud-logingester-service-state")},
	}
	owner := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-logingester", "namespace": namespace},
		"spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": podSpec}},
	}}

	f.stage(t, "kubernetes/isolated-original-log-owner", []deploymentplan.Definition{claim, owner})
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	device := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: &postServiceMQTTExecutionContract{acceptedCoreExecutionContract: f}}
	installHistoricalLogIdentityProtocol(t, device)
	state := f.protocol["historical_log_identity"].(map[string]string)
	before, err := readBoundedEnvironmentFile(state["path"], true)
	if err != nil {
		t.Fatal(err)
	}
	pod := "video-cloud-logingester-pod"
	got, err := readLogACKDraftIdentity(t.Context(), f.backend, pod, f.service.Root.RootFingerprint)
	if err != nil || got.Subject != logACKSubject || got.Fingerprint != row.Fingerprint || got.PublicKeySHA256 != environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo) || got.StateSHA256 != environmentPreflightBytesSHA256(before) || got.Pending {
		t.Fatal("actual native Log inspection did not bind original bytes", err)
	}
	query := "SELECT json_build_object('certificate_pem',certificate_pem,'fingerprint',fingerprint) FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:video-cloud-logingester' AND status='succeeded' AND revoked_at IS NULL"
	registry, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend}, query)
	var public struct {
		Certificate string `json:"certificate_pem"`
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || decodeEnvironmentJSON(registry, &public) != nil || public.Certificate != certificate || public.Fingerprint != row.Fingerprint {
		t.Fatal("native Log registry changed original issuance", err)
	}
	after, err := readBoundedEnvironmentFile(state["path"], true)
	if err != nil || string(after) != string(before) {
		t.Fatal("read-only inspection changed original private identity", err)
	}
	for _, mode := range []string{"pending", "wrong-key", "changed-subject", "private-mode", "foreign-claim", "foreign-mount"} {
		t.Run(mode, func(t *testing.T) {
			defer func() {
				if err := os.WriteFile(state["path"], before, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(state["path"], 0600); err != nil {
					t.Fatal(err)
				}
				f.protocol["objects"] = historicalNativeInventory(f.native.objects)
				f.publish(t)
			}()
			var changed map[string]any
			if err := json.Unmarshal(before, &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "pending":
				changed["pending"] = map[string]string{"request_id": "unaccepted-original"}
			case "wrong-key":
				deploymentplan.Map(changed["current"])["private_key_pem"] = "invalid-original-key"
			case "changed-subject":
				changed["subject"] = "service:foreign"
			case "private-mode":
				if err := os.Chmod(state["path"], 0644); err != nil {
					t.Fatal(err)
				}
			case "foreign-mount":
				objects := historicalNativeInventory(f.native.objects)
				pod := objects["Pod/"+namespace+"/video-cloud-logingester-pod"]
				container := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])
				container["volumeMounts"] = []any{}
				f.protocol["objects"] = objects
				f.publish(t)
			case "foreign-claim":
				objects := historicalNativeInventory(f.native.objects)
				deploymentplan.Map(objects[deploymentplan.ID(claim.Object)]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				f.protocol["objects"] = objects
				f.publish(t)
			}
			if mode == "pending" || mode == "wrong-key" || mode == "changed-subject" {
				if err := os.WriteFile(state["path"], mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readLogACKDraftIdentity(t.Context(), f.backend, pod, f.service.Root.RootFingerprint); err == nil {
				t.Fatal("drifted original native identity accepted", mode)
			}
		})
	}
}
