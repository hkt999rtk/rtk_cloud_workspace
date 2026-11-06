package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingCompletedAccountExportUsesPermanentOwner(t *testing.T) {
	for _, mode := range []string{"original", "reader-still-present", "replaced-owner", "replaced-claim", "wrong-certificate", "wrong-root", "changed-pod"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, runtime, service := createServiceRecipeFixture(t, "staging")
			f := newStagingQualificationFixture(t)
			ns := p.Stack + "-account-manager"
			claim := "account-manager-service-state"
			state := environmentCreateServiceState
			client := state + "/private/client.json"
			server := state + "/private/server.json"
			name := "account-manager." + ns + ".svc.cluster.local"
			ca, cakey, capem := coreContractCertificate(t, "isolated-export-root", true, nil, nil, time.Hour)
			intermediate, intermediateKey, intermediatePEM := coreContractCertificate(t, "isolated-export-intermediate", true, ca, cakey, time.Hour)
			_, key, cert := coreContractCertificate(t, "isolated-export-server", false, intermediate, intermediateKey, time.Hour)
			der, err := x509.MarshalECPrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			service.Root.RootFingerprint = environmentPreflightBytesSHA256(ca.Raw)
			service.Root.Certificate, service.Root.ChainPEM = capem, capem
			service.Root.BundleVersion = environmentPreflightBytesSHA256([]byte(capem))
			service.Root.Document["certificate_pem"] = capem
			service.Root.Document["chain_pem"] = capem
			service.Root.Document["certificate_fingerprint_sha256"] = service.Root.RootFingerprint
			service.Root.Document["trust_bundle_version"] = service.Root.BundleVersion
			service.Intermediate.Certificate = intermediatePEM
			service.Intermediate.ChainPEM = intermediatePEM + capem
			service.Intermediate.RootFingerprint = service.Root.RootFingerprint
			service.Intermediate.BundleVersion = environmentPreflightBytesSHA256([]byte(service.Intermediate.ChainPEM))
			service.Intermediate.Document["certificate_pem"] = service.Intermediate.Certificate
			service.Intermediate.Document["chain_pem"] = service.Intermediate.ChainPEM
			service.Intermediate.Document["certificate_fingerprint_sha256"] = environmentPreflightBytesSHA256(intermediate.Raw)
			service.Intermediate.Document["trust_bundle_version"] = service.Intermediate.BundleVersion

			material := environmentCreateOwnedServerMaterial{Environment: "staging", Root: service.Root.RootFingerprint, Name: name, Certificate: cert + intermediatePEM + capem, Key: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))}
			defs, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			owner := createRecipeObject(t, defs, "Deployment/"+ns+"/account-manager")
			core, cj := f.stage(t, "managed-complete-issuer", []deploymentplan.Definition{{Object: owner, Phase: "workload"}}, true)
			owner = core.Resources[0].Desired
			ownerUID := cj.Owners[core.Resources[0].ID]
			readerName := "pki-export-account-manager-" + f.e.Journal.PlanSHA256[:12]
			settings := map[string]string{"PKI_ENVIRONMENT": "staging", "PKI_SERVER_NAME": name, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": client, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:account-manager", "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint}
			reader := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": readerName, "namespace": ns}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "custody", "image": p.Image, "command": []any{"/bin/sh", "-ec", "exec sleep 3600"}, "env": environmentCreateEnv(settings), "volumeMounts": []any{environmentCreateServiceMount("owner-state", state, false)}}}, "volumes": []any{environmentCreateServiceVolume("owner-state", "persistentVolumeClaim", claim)}}}}}
			rp, rj := f.stage(t, "server-custody-account-manager", []deploymentplan.Definition{{Object: reader, Phase: "bootstrap"}}, true)
			readerID := rp.Resources[0].ID
			readerUID := rj.Owners[readerID]
			retired := deploymentplan.Clone(reader)
			deploymentplan.Map(retired["metadata"])["uid"] = readerUID
			retireStage := "retire-" + readerName
			retireDefs := []deploymentplan.Definition{{Object: retired, Phase: "retirement", Retire: true}}
			inputs := rp.Inputs
			inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(retireStage, retireDefs)
			retirement, err := deploymentplan.Compile(inputs, retireDefs, []deploymentplan.Object{f.objects[readerID]})
			if err != nil {
				t.Fatal(err)
			}
			if err := saveEnvironmentCreateStageInput(t.Context(), f.store, f.e, retireStage, retirement); err != nil {
				t.Fatal(err)
			}
			retireIntent, err := environmentCreateKubernetesStageIntent(retirement)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.e.BeginEntry("kubernetes/"+retireStage, retireStage, retireIntent, time.Now()); err != nil {
				t.Fatal(err)
			}
			f.persistStage(t, retirement, deploymentplan.Journal{PlanSHA256: retirement.SHA256(), Owners: map[string]string{readerID: readerUID}, Completed: []string{readerID}, Accepted: true, AcceptanceStarted: true})
			if err := f.e.RecordReceipt("kubernetes/"+retireStage, mustProviderRecoveryJSON(map[string]any{"plan_sha256": retirement.SHA256(), "owners": map[string]string{readerID: readerUID}})); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("kubernetes/"+retireStage, time.Now()); err != nil {
				t.Fatal(err)
			}
			delete(f.objects, readerID)
			receipt := completedAccountServerExport{Certificate: environmentPreflightBytesSHA256([]byte(material.Certificate)), Claim: "33333333-3333-4333-8333-333333333333", Reader: readerUID, Pod: "44444444-4444-4444-8444-444444444444", ReplicaSet: "55555555-5555-4555-8555-555555555555", Server: name}
			intent := deploymentplan.Object{"owner": "account-manager", "namespace": ns, "claim_uid": receipt.Claim, "reader_uid": receipt.Reader, "pod_uid": receipt.Pod, "replicaset_uid": receipt.ReplicaSet, "server_name": name, "root_sha256": service.Root.RootFingerprint}
			if err := f.e.BeginEntry("server-export-account-manager", "bootstrap", mustProviderRecoveryJSON(intent), time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := f.e.RecordReceipt("server-export-account-manager", mustProviderRecoveryJSON(receipt)); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("server-export-account-manager", time.Now()); err != nil {
				t.Fatal(err)
			}
			template := deploymentplan.Map(deploymentplan.Map(owner["spec"])["template"])
			rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "account-owner-rs", "namespace": ns, "uid": "66666666-6666-4666-8666-666666666666", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "account-manager", "uid": ownerUID, "controller": true}}}}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "account-owner-pod", "namespace": ns, "uid": "77777777-7777-4777-8777-777777777777", "labels": deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(template["metadata"])["labels"])), "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "account-owner-rs", "uid": deploymentplan.Map(rs["metadata"])["uid"], "controller": true}}}, "spec": deploymentplan.Clone(deploymentplan.Map(template["spec"])), "status": deploymentplan.Object{"phase": "Running"}}
			pvc := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": ns, "uid": receipt.Claim, "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}}}
			for _, object := range []deploymentplan.Object{rs, pod, pvc} {
				f.objects[deploymentplan.ID(object)] = object
			}
			f.pods = []deploymentplan.Object{pod}
			switch mode {
			case "reader-still-present":
				f.objects[readerID] = reader
			case "replaced-owner":
				deploymentplan.Map(f.objects[deploymentplan.ID(owner)]["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "replaced-claim":
				deploymentplan.Map(pvc["metadata"])["uid"] = "foreign"
			case "wrong-certificate":
				material.Certificate = capem
			case "wrong-root":
				material.Root = "foreign"
			case "changed-pod":
				managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "pkimanagement")["image"] = "foreign:latest"
			}
			f.native(t)
			raw, _ := json.Marshal(material)
			writeTestFile(t, filepath.Join(f.dir, "material.json"), string(raw))
			native := filepath.Join(f.dir, "kubectl")
			script, err := os.ReadFile(native)
			if err != nil {
				t.Fatal(err)
			}
			// Extend only the external export boundary; the owner/Pod/claim checks and
			// accepted-child readers all execute their production implementations.
			inserted := "if 'export-server' in a:\n print((root/'material.json').read_text());sys.exit()\n"
			text := string(script)
			marker := "if 'get' in a:"
			if !strings.Contains(text, marker) {
				t.Fatal("native fixture entry missing")
			}
			if err := os.WriteFile(native, []byte(strings.Replace(text, marker, inserted+marker, 1)), 0700); err != nil {
				t.Fatal(err)
			}
			runtime.Execution = f.e
			runtime.Backend = f.b
			before := createBootstrapStateSnapshot(t, f.store.Root)
			got, err := readStagingCompletedAccountServer(t.Context(), cfg, p, runtime, service, ns, "account-manager", claim, state, client, server, name)
			if mode == "original" {
				if err != nil || got.Certificate != material.Certificate || got.Key != material.Key {
					t.Fatal("original serving material not restored", err)
				}
			} else if err == nil {
				t.Fatal("changed export accepted")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
