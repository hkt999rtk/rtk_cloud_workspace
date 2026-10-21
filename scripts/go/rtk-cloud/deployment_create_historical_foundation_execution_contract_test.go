package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This optional original producer runs before any recovery source is frozen.
// The StatefulSet, file configuration and TLS projections therefore retain their
// actual accepted owners throughout all later managed-transport checkpoints.
func historicalOriginalFoundationHooks(t *testing.T) coreExecutionContractHooks {
	t.Helper()
	return historicalOriginalFoundationHooksForSession(t, true)
}

// The pre-issuance legacy ordering fixture selects absent bootstrap
// prerequisites before its original foundation is rendered and accepted.
// Every completed-flow fixture retains the original prerequisite producer.
func historicalOriginalFoundationHooksForSession(t *testing.T, includeSessionPrerequisites bool) coreExecutionContractHooks {
	t.Helper()
	return coreExecutionContractHooks{
		OriginalServiceIntermediateLifecycle: true,
		ImageManifests:                       historicalOriginalLogImageManifests(t),
		TransportNames: func(cfg deploymentConfig, role string) map[string][]string {
			if role == "openbao" {
				return map[string][]string{"openbao": lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg))}
			}
			stack := cfg.Values["CLOUD_STACK_NAME"]
			return map[string][]string{"account-manager": {"account-manager." + stack + "-account-manager.svc.cluster.local"}, "pki-controller": {"pki-controller." + stack + "-video-cloud.svc"}, "certissuer": {"certissuer." + stack + "-video-cloud.svc"}, "video-cloud-api": {"video-cloud-api-pki." + stack + "-video-cloud.svc"}, "video-cloud-api-app-pki": {"video-cloud-api-app-pki." + stack + "-video-cloud.svc"}}
		},
		Configure: func(cfg *deploymentConfig, plan *environmentCreatePlan, bootstrap *environmentCreateBootstrapPlan) {
			cfg.Values[deploymentRolloutTimeoutKey] = "300"
			cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "3600"
			// This historical CRL consumer profile requires one native API Pod.
			// Select that capacity before rendering or any original publication.
			cfg.Values["VIDEO_CLOUD_API_EFFECTIVE_REPLICAS"] = "1"
			cfg.Values["LKE_VIDEO_CLOUD_REPLICAS"] = "1"
			servicePolicy := findEnvironmentCreateDomainPolicy(*bootstrap, "service")
			cfg.Values["PKI_REQUIRED_CONSUMERS_SERVICE"] = strings.Join(servicePolicy.Consumers, ",")
			for _, policy := range bootstrap.Domains {
				cfg.Values["PKI_REQUIRED_BUNDLE_CONSUMERS_"+strings.ToUpper(policy.Domain)] = strings.Join(policy.BundleConsumers, ",")
			}
			for key, value := range map[string]string{"OPENBAO_STORAGE_BACKEND": "file", "OPENBAO_EFFECTIVE_REPLICAS": "1", "OPENBAO_DATA_STORAGE": "10Gi", "OPENBAO_AUDIT_STORAGE": "5Gi", "OPENBAO_POD_MANAGEMENT_POLICY": "OrderedReady", "OPENBAO_UPDATE_STRATEGY": "OnDelete", "DEPLOYMENT_PROTECTION_PROFILE": "standard"} {
				cfg.Values[key] = value
			}
			for _, role := range []string{"LKE_OPENBAO_IMAGE", "LKE_OPENBAO_PKI_IMAGE", "LKE_MQTT_PKI_IMAGE", "LKE_VIDEO_CLOUD_IMAGE", "LKE_POSTGRES_IMAGE", "LKE_INGRESS_IMAGE"} {
				image := bootstrap.Image
				if role == "LKE_VIDEO_CLOUD_IMAGE" {
					image = logACKNativeImage
				}
				if role == "LKE_INGRESS_IMAGE" {
					_, digest, ok := strings.Cut(image, "@")
					if !ok {
						t.Fatal("immutable original ingress image required")
					}
					// This external protocol fixture serves the actual synthetic
					// manifest bytes under the maintained official repository.
					image = "registry.k8s.io/ingress-nginx/controller@" + digest
					cfg.Values[role] = image
				}
				if role == "LKE_OPENBAO_PKI_IMAGE" {
					_, digest, ok := strings.Cut(image, "@")
					if !ok {
						t.Fatal("immutable original native image required")
					}
					image = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@" + digest
				}
				plan.Release.Images[role] = environmentReleaseImage{Reference: image}
				bootstrap.Images[role] = image
			}
			bootstrap.Image = plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
		},
		BeforeSource: func(f *acceptedCoreExecutionContract, runtime *environmentCreateBootstrapRuntime) {
			// Core's three original Namespace UIDs are already observed. Produce
			// the two additional maintained namespaces in this same foundation
			// child before its first namespaced write; never replace the originals.
			for _, suffix := range []string{"video-cloud", "account-manager", "secrets"} {
				if f.native.objects["Namespace//"+f.p.Plan.Stack+"-"+suffix] == nil {
					t.Fatal("original Core namespace missing before foundation", suffix)
				}
			}
			f.native.requireNativeNamespaces = true
			var transport environmentCreateTransport
			base := filepath.Join("deployment/create/material", f.e.Journal.PlanSHA256, "openbao-transport")
			if err := readExecutionJSON(filepath.Join(f.store.Root, base, "identities.json"), &transport); err != nil {
				t.Fatal(err)
			}
			identity := transport.Identities["openbao"]
			if identity.Certificate == "" || identity.Key == "" {
				t.Fatal("original OpenBao transport identity missing")
			}
			defs, err := renderEnvironmentCreateOpenBaoFoundation(f.cfg, f.p.Plan.Release, "openbao-transport-tls", "pki-openbao-transport-ca")
			if err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"platform", "ingress"} {
				manifest, err := renderK8SNamespaceManifest(f.p.Plan.Stack+"-"+suffix, f.p.Plan.Stack)
				if err != nil {
					t.Fatal(err)
				}
				namespace, err := decodeEnvironmentCreateManifests(manifest)
				if err != nil || len(namespace) != 1 || namespace[0].Phase != "namespace" {
					t.Fatal("maintained original namespace definition", err)
				}
				defs = append(defs, namespace...)
			}
			if includeSessionPrerequisites {
				// The original Service bootstrap allocates its durable key/CSR claim
				// and service account before any same-key renewal. Render those actual
				// prerequisites now; the later archived renewal owns only its Job.
				subjects, err := environmentCreateBootstrapSubjects(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				bootstrap, err := renderEnvironmentCreatePKIPhase(t.Context(), f.cfg, f.p.Plan.Bootstrap, "service-bootstrap", "--openbao-ca-configmap", "pki-openbao-transport-ca", "--service-bootstrap-subject", "service:deployment-bootstrap", "--service-bootstrap-subjects", strings.Join(subjects, ","), "--service-bootstrap-issuer-id", f.service.Intermediate.ID, "--service-bootstrap-issuer-mount", f.service.Intermediate.Reference, "--service-bootstrap-root-sha256", f.service.Root.RootFingerprint, "--service-bootstrap-deployment-id", "create-"+f.e.Journal.PlanSHA256[:32], "--service-bootstrap-session-id", f.service.BootstrapSessionID, "--service-bootstrap-state-claim", f.service.BootstrapClaim)
				if err != nil {
					t.Fatal(err)
				}
				for _, definition := range bootstrap {
					if definition.Object["kind"] != "Job" {
						defs = append(defs, definition)
					}
				}
			}
			// Preserve the maintained original database controller and all of its
			// dependencies before the first recovery snapshots this foundation.
			values := environmentCreateProviderValues(f.cfg)
			values["LKE_POSTGRES_STORAGE_MODE"], values["LKE_POSTGRES_STORAGE"] = "pvc", "20Gi"
			t.Setenv("LKE_POSTGRES_IMAGE", f.p.Plan.Release.Images["LKE_POSTGRES_IMAGE"].Reference)
			postgres, err := decodeEnvironmentCreateManifests(lkePostgresInitManifest(values), lkePostgresServiceManifest(values), lkePostgresStatefulSetManifest(values))
			if err != nil {
				t.Fatal(err)
			}
			defs = append(defs, postgres...)
			defs = append(defs, environmentCreateSecret(f.p.Plan.Stack+"-platform", "postgresql-runtime", map[string]string{"POSTGRES_PASSWORD": "isolated-original-postgres"}))
			defs = append(defs, environmentCreateSecret(f.p.Plan.Stack+"-secrets", "openbao-transport-tls", map[string]string{"tls.crt": identity.Certificate, "tls.key": identity.Key, "ca.crt": transport.CACert}), deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-openbao-transport-ca", "namespace": f.p.Plan.Stack + "-video-cloud"}, "data": deploymentplan.Object{"ca.crt": transport.CACert}}})
			if err := bindEnvironmentNewStorageClasses(defs, f.cfg.Values["DURABLE_DATA_STORAGE_CLASS"], nil); err != nil {
				t.Fatal(err)
			}
			f.stage(t, "kubernetes/foundation", defs)
			for _, object := range f.native.objects {
				namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
				if namespace != "" && f.native.objects["Namespace//"+namespace] == nil {
					t.Fatal("foundation object lacks its actual native namespace", deploymentplan.ID(object))
				}
			}
			claims := 0
			for _, id := range []string{"StatefulSet/" + f.p.Plan.Stack + "-secrets/openbao", "StatefulSet/" + f.p.Plan.Stack + "-platform/postgresql"} {
				controller := f.native.objects[id]
				meta := deploymentplan.Map(controller["metadata"])
				uid, name := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["name"])
				if !environmentBootstrapOwnerUUID.MatchString(uid) {
					t.Fatal("original foundation controller did not acquire a native UID", id)
				}
				owner := []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": name, "uid": uid, "controller": true, "blockOwnerDeletion": true}}
				// Model the native StatefulSet controller using only the actual
				// accepted template, with new API UIDs and exact original ancestry.
				for _, value := range certIssuerObjectList(deploymentplan.Map(controller["spec"])["volumeClaimTemplates"]) {
					claim := deploymentplan.Clone(deploymentplan.Map(value))
					claim["apiVersion"], claim["kind"] = "v1", "PersistentVolumeClaim"
					claimMeta := deploymentplan.Map(claim["metadata"])
					claimMeta["name"] = deploymentplan.String(claimMeta["name"]) + "-" + name + "-0"
					claimMeta["namespace"], claimMeta["ownerReferences"] = meta["namespace"], owner
					if _, err := f.native.Write(context.Background(), "create", claim, false); err != nil {
						t.Fatal("native StatefulSet claim creation", err)
					}
					claims++
				}
				pod := deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(controller["spec"])["template"]))
				pod["apiVersion"], pod["kind"] = "v1", "Pod"
				podMeta := deploymentplan.Map(pod["metadata"])
				podMeta["name"], podMeta["namespace"], podMeta["ownerReferences"] = name+"-0", meta["namespace"], owner
				if _, err := f.native.Write(context.Background(), "create", pod, false); err != nil {
					t.Fatal("native StatefulSet Pod creation", err)
				}
			}
			if claims != 3 {
				t.Fatal("original foundation did not create exact two OpenBao and one PostgreSQL claims", claims)
			}
			historicalInitializeOriginalOpenBao(t, f, runtime)

			runtime.OpenBao.CACertPath = filepath.Join(f.store.Root, base, "ca.crt")
		},
	}
}

func TestHistoricalOriginalFoundationPrecedesImmutableCoreSource(t *testing.T) {
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, historicalOriginalFoundationHooks(t), historicalCompleteDomainPolicy(t))
	p := f.p
	p.Materials = map[string]string{}
	if _, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p); err != nil {
		t.Fatal("original foundation changed accepted Service lineage", err)
	}
	plan, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/foundation")
	if err != nil || !j.Accepted {
		t.Fatal("original foundation acceptance missing", err)
	}
	for i, suffix := range []string{"video-cloud", "account-manager", "secrets", "platform", "ingress"} {
		id := "Namespace//" + f.p.Plan.Stack + "-" + suffix
		owner := f.native.objects[id]
		uid := deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			t.Fatal("original namespace owner missing", id)
		}
		if i < 3 && uid != fmt.Sprintf("%08d-4444-4444-8444-444444444444", i+1) {
			t.Fatal("original Core namespace UID changed", id)
		}
		if i >= 3 && j.Owners[id] != uid {
			t.Fatal("new foundation namespace lacks actual accepted owner", id)
		}
	}
	id := "StatefulSet/" + f.p.Plan.Stack + "-secrets/openbao"
	uid := j.Owners[id]
	if uid == "" || deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != uid {
		t.Fatal("foundation writer lost original owner")
	}
	for _, name := range []string{"data-openbao-0", "audit-openbao-0"} {
		claim := f.native.objects["PersistentVolumeClaim/"+f.p.Plan.Stack+"-secrets/"+name]
		owners := certIssuerObjectList(deploymentplan.Map(claim["metadata"])["ownerReferences"])
		if len(owners) != 1 || deploymentplan.Map(owners[0])["uid"] != uid {
			t.Fatal("native original claim lacks actual controller ancestry", name)
		}
	}
	var source createFoundationRecoveryArtifact
	if readExecutionJSON(filepath.Join(f.store.Root, "deployment/create/openbao-recovery", f.review, "qualification.json"), &source) != nil || source.Plan.Plan.Release.Images["LKE_OPENBAO_IMAGE"].Reference != f.p.Plan.Release.Images["LKE_OPENBAO_IMAGE"].Reference {
		t.Fatal("foundation image was not frozen in original source")
	}
	if source.Plan.Plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference != logACKNativeImage || f.p.Plan.Bootstrap.Images["LKE_VIDEO_CLOUD_IMAGE"] != logACKNativeImage {
		t.Fatal("original Log native image was not selected before publication")
	}
	// Exercise both exact bytes through the production index/platform reader.
	if err := f.store.write("operator/env/GHCR_USERNAME", []byte("isolated-user"), false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.write("operator/env/GHCR_TOKEN", []byte("isolated-fake-gh-only"), false); err != nil {
		t.Fatal(err)
	}
	digests, err := environmentRuntimeImageDigests(t.Context(), f.store, logACKNativeImage)
	if err != nil || !digests["sha256:1e9b9820f619a39e4686f1f9260f3422b7d5c239e6a4dbfd8101e488d2472244"] || !digests["sha256:cdaea7eb2760b2ef29278f81e3fed0574ec040ef7717be70b168451b65ac6d41"] {
		t.Fatal("original Log OCI content-addressed platform provenance", err)
	}
	postgres := f.native.objects["StatefulSet/"+f.p.Plan.Stack+"-platform/postgresql"]
	claim := f.native.objects["PersistentVolumeClaim/"+f.p.Plan.Stack+"-platform/data-postgresql-0"]
	owners := certIssuerObjectList(deploymentplan.Map(claim["metadata"])["ownerReferences"])
	if len(owners) != 1 || deploymentplan.Map(owners[0])["uid"] != deploymentplan.Map(postgres["metadata"])["uid"] {
		t.Fatal("original PostgreSQL claim lacks accepted controller ancestry")
	}
	for _, key := range []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config"} {
		entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
		if entry == nil || entry.State != "completed" {
			t.Fatal("actual original OpenBao lifecycle incomplete", key)
		}
	}
	bootstrapID := "PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/pki-service-bootstrap-state"
	bootstrapClaim := f.native.objects[bootstrapID]
	if j.Owners[bootstrapID] == "" || deploymentplan.Map(bootstrapClaim["metadata"])["uid"] != j.Owners[bootstrapID] || f.native.writes[bootstrapID] != 1 {
		t.Fatal("same-key renewal did not retain the actual original bootstrap claim")
	}
	if len(plan.Resources) == 0 {
		t.Fatal("empty original foundation")
	}
	ingress := f.p.Plan.Release.Images["LKE_INGRESS_IMAGE"].Reference
	if environmentImageRepository(ingress) != "registry.k8s.io/ingress-nginx/controller" || f.cfg.Values["LKE_INGRESS_IMAGE"] != ingress || f.p.Plan.Bootstrap.Images["LKE_INGRESS_IMAGE"] != ingress || source.Plan.Plan.Release.Images["LKE_INGRESS_IMAGE"].Reference != ingress {
		t.Fatal("original official ingress selection was not frozen consistently")
	}
	if err := validateEnvironmentCreateIngressAdmissionRecipe(f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release); err != nil {
		t.Fatal("original maintained ingress recipe", err)
	}
	material, err := prepareEnvironmentCreateIngressAdmissionMaterial(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.e)
	if err != nil {
		t.Fatal("actual native ingress identity producer", err)
	}
	definitions, err := renderEnvironmentCreateIngressAdmission(f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, material)
	if err != nil {
		t.Fatal("actual original ingress definitions", err)
	}
	controllers := 0
	for _, definition := range definitions {
		if deploymentplan.ID(definition.Object) != "Deployment/"+f.p.Plan.Stack+"-ingress/ingress-nginx-controller" {
			continue
		}
		controllers++
		containers := certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])
		if len(containers) != 1 || deploymentplan.Map(containers[0])["image"] != ingress {
			t.Fatal("rendered ingress escaped its original frozen image")
		}
	}
	if controllers != 1 {
		t.Fatal("maintained ingress controller missing or duplicated")
	}
	var empty struct {
		Items []deploymentplan.Object `json:"items"`
	}
	raw, err := f.backend.command(t.Context(), nil, "get", "validatingwebhookconfiguration", "-o", "json")
	if err != nil || json.Unmarshal(raw, &empty) != nil || len(empty.Items) != 0 {
		t.Fatal("pre-ingress inventory invented an owner", err)
	}
	accepted := f.stage(t, "kubernetes/isolated-ingress-native-inventory", definitions)
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	live, err := f.backend.Inventory(t.Context(), f.p.Plan.Stack)
	if err != nil {
		t.Fatal("actual completed ingress native inventory", err)
	}
	webhookID, classID := "ValidatingWebhookConfiguration//"+f.p.Plan.Stack+"-ingress-admission", "IngressClass//"+f.p.Plan.Stack+"-nginx"
	found := map[string]string{}
	for _, object := range live {
		found[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	for _, id := range []string{webhookID, classID} {
		if found[id] == "" || found[id] != deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] {
			t.Fatal("actual ingress cluster owner missing from complete inventory", id)
		}
	}
	var acceptedJournal deploymentplan.Journal
	if readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", accepted.SHA256(), "journal.json"), &acceptedJournal) != nil || acceptedJournal.Owners[webhookID] != found[webhookID] {
		t.Fatal("webhook inventory UID differs from actual accepted writer")
	}
	for _, kinds := range []string{environmentClusterInventoryKinds, "validatingwebhookconfiguration"} {
		var filtered struct {
			Items []deploymentplan.Object `json:"items"`
		}
		raw, err := f.backend.command(t.Context(), nil, "get", kinds, "-l", "rtk.realtek.com/stack=foreign-stack", "-o", "json")
		if err != nil || json.Unmarshal(raw, &filtered) != nil || len(filtered.Items) != 0 {
			t.Fatal("foreign selector received original cluster owners", err)
		}
	}
	var webhookOnly struct {
		Items []deploymentplan.Object `json:"items"`
	}
	raw, err = f.backend.command(t.Context(), nil, "get", "validatingwebhookconfiguration", "-l", "rtk.realtek.com/stack="+f.p.Plan.Stack, "-o", "json")
	if err != nil || json.Unmarshal(raw, &webhookOnly) != nil || len(webhookOnly.Items) != 1 || deploymentplan.ID(webhookOnly.Items[0]) != webhookID || deploymentplan.Map(webhookOnly.Items[0]["metadata"])["uid"] != found[webhookID] {
		t.Fatal("single-kind native inventory returned another kind or owner", err)
	}
}

// Only the external OpenBao/Kubernetes protocols are simulated here. The real
// initializer owns sensitive persistence, parent receipts, unseal, audit and
// Kubernetes-auth readbacks before any historical recovery source is frozen.
func historicalInitializeOriginalOpenBao(t *testing.T, f *acceptedCoreExecutionContract, runtime *environmentCreateBootstrapRuntime) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "native.json")
	initial := map[string]any{"objects": f.native.objects, "initialized": false, "sealed": true, "auth": false, "config": false, "token": "isolated-original-bao-token", "unseal": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))}
	if err := os.WriteFile(state, mustProviderRecoveryJSON(initial), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,os,sys
path=os.environ['RTK_HISTORICAL_FOUNDATION_STATE']
with open(path) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
def save():
 with open(path,'w') as target:json.dump(d,target)
def respond(value):print(json.dumps(value))
if 'get' in a:
 i=a.index('get');ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 value=d['objects'].get(a[i+1]+'/'+ns+'/'+a[i+2])
 if value is None:sys.exit('unknown original foundation owner')
 respond(value)
elif 'bao status -format=json' in joined:respond({'initialized':d['initialized'],'sealed':d['sealed']})
elif 'bao operator init ' in joined:
 if d['initialized']:sys.exit('original OpenBao already initialized')
 d['initialized']=True;save();respond({'root_token':d['token'],'unseal_keys_b64':[d['unseal']]})
elif 'bao write -format=json sys/unseal -' in joined:
 body=json.load(sys.stdin)
 if not d['initialized'] or body!={'key':d['unseal']}:sys.exit('wrong original unseal share')
 for name in ['init.json','root-token','unseal-key']:
  if not os.path.getsize(os.path.join(os.environ['RTK_HISTORICAL_FOUNDATION_STORE'],'openbao',name)):sys.exit('private initialization was not persisted first')
 d['sealed']=False;save();respond({'initialized':True,'sealed':False})
else:
 if d['sealed'] or sys.stdin.readline().strip()!=d['token']:sys.exit('original OpenBao authentication failed')
 if 'bao audit list -format=json' in joined:respond({'deployment-audit/':{'type':'file','options':{'file_path':'/openbao/audit/audit.jsonl','log_raw':'false'}}})
 elif 'bao auth enable -path=kubernetes' in joined:d['auth']=True;save()
 elif 'bao auth list -format=json' in joined:respond({'kubernetes/':{'type':'kubernetes'}} if d['auth'] else {})
 elif 'bao write auth/kubernetes/config ' in joined:
  if not d['auth']:sys.exit('auth mount missing')
  d['config']=True;save()
 elif 'bao read -format=json auth/kubernetes/config' in joined:respond({'data':{'kubernetes_host':'https://kubernetes.default.svc:443','disable_local_ca_jwt':False,'token_reviewer_jwt_set':False}} if d['config'] else {})
 else:sys.exit('unhandled original foundation request')
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_FOUNDATION_NATIVE", path)
	t.Setenv("RTK_HISTORICAL_FOUNDATION_STATE", state)
	t.Setenv("RTK_HISTORICAL_FOUNDATION_STORE", f.store.Root)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	backend := environmentKubernetesBackend{Store: f.store, KubeconfigContent: []byte("isolated-original-frozen-kubeconfig")}

	original, err := initializeEnvironmentCreateOpenBao(t.Context(), f.cfg, f.store, backend, f.e)
	if err != nil {
		t.Fatal("actual original OpenBao initialization", err)
	}
	if _, _, err := verifyInitializedOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, backend); err != nil {
		t.Fatal("original OpenBao identity readback", err)
	}
	runtime.OpenBao = original
}

func historicalOriginalLogImageManifests(t *testing.T) map[string]string {
	t.Helper()
	repository, index, ok := strings.Cut(logACKNativeImage, "@")
	if !ok {
		t.Fatal("original Log image is not immutable")
	}
	images := map[string]string{}
	for file, digest := range map[string]string{"index.json": index, "linux-amd64.json": "sha256:cdaea7eb2760b2ef29278f81e3fed0574ec040ef7717be70b168451b65ac6d41"} {
		raw, err := os.ReadFile(filepath.Join("testdata/oci/log-ack-original", file))
		if err != nil || "sha256:"+environmentPreflightBytesSHA256(raw) != digest {
			t.Fatal("original Log manifest fixture digest differs", file, err)
		}
		images[repository+"@"+digest] = string(raw)
	}
	return images
}
