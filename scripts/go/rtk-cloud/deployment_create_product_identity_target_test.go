package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productTargetFixture(t *testing.T) (deploymentConfig, *createNormalTailProductLifecycle, *environmentProductIdentitySession) {
	t.Helper()
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}}
	ns := "video-cloud-dev-video-cloud"
	owner := "11111111-1111-4111-8111-111111111111"
	object := func(name string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"serviceAccountName": "pki-controller", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": name, "image": "registry.invalid/unchanged@sha256:" + strings.Repeat("a", 64), "env": []any{deploymentplan.Object{"name": "PERMANENT_SETTING", "value": "preserved"}}}}}}}}
	}
	controller, issuer, product := object("pki-controller"), object("certissuer"), object("shadow-worker")
	pod := environmentPKIPod(product)
	pod["volumes"] = []any{deploymentplan.Object{"name": "identity", "secret": deploymentplan.Object{"secretName": shadowWorkerIdentitySecretName}}}
	deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["volumeMounts"] = []any{deploymentplan.Object{"name": "identity", "mountPath": "/run/platform-identity", "readOnly": true}}
	id := "Secret/" + ns + "/" + shadowWorkerIdentitySecretName
	root := environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", RootFingerprint: strings.Repeat("b", 64), Certificate: "original-root"}
	ica := environmentCreateRegisteredIssuer{ID: "33333333-3333-4333-8333-333333333333", RootFingerprint: root.RootFingerprint, Reference: "pki/service/original"}
	l := &createNormalTailProductLifecycle{ParentSHA256: strings.Repeat("c", 64), OwnerSHA256: strings.Repeat("d", 64), Root: root, Intermediate: ica, Outputs: []environmentCreateProductIdentity{{ID: id, Subject: "service:shadow", Claim: "pki-product-shadow-identity"}}, SourceDefinitions: []deploymentplan.Definition{{Object: controller, Phase: "workload", Retain: true}, {Object: issuer, Phase: "workload", Retain: true}, {Object: product, Phase: "workload"}}, SourceOwners: map[string]string{deploymentplan.ID(controller): owner, deploymentplan.ID(issuer): owner}}
	service := environmentCreateServiceCeremony{Root: root, Intermediate: ica, BootstrapSessionID: "44444444-4444-4444-8444-444444444444", CleanupBinding: environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: "44444444-4444-4444-8444-444444444444", NamespaceUID: owner, SecretUID: owner, PolicySHA256: strings.Repeat("e", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t)), ConfigMapName: "pki-bootstrap-cleanup-binding-44444444-444"}}
	i := &environmentProductIdentitySession{Schema: "rtk-cloud.product-identities/v1", ParentSHA256: l.ParentSHA256, PlanSHA256: productIdentityScope(l), OwnerSHA256: l.OwnerSHA256, Ceremony: service, Outputs: []environmentProductIdentityReceipt{{ResourceID: id, Subject: "service:shadow", SecretUID: owner, ClaimUID: owner, CertificateSHA256: strings.Repeat("f", 64)}}}
	return cfg, l, i
}

func TestCreateNormalTailProductTargetUsesFrozenCatalogAndActualACKOutputs(t *testing.T) {
	cfg, l, i := productTargetFixture(t)
	before := deploymentplan.Digest(l)
	for _, bootstrap := range []bool{true, false} {
		definitions, err := resolveCreateNormalTailProductTarget(cfg, l, i, bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, d := range definitions {
			if deploymentplan.ID(d.Object) == "Deployment/video-cloud-dev-video-cloud/shadow-worker" {
				seen = true
				container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
				values := map[string]string{}
				for _, e := range certIssuerObjectList(container["env"]) {
					entry := deploymentplan.Map(e)
					values[deploymentplan.String(entry["name"])] = deploymentplan.String(entry["value"])
				}
				if values["PERMANENT_SETTING"] != "preserved" || bootstrap && (values["PKI_BOOTSTRAP_SESSION_ID"] != i.Ceremony.BootstrapSessionID || values["PKI_BOOTSTRAP_ISSUER_ID"] != i.Ceremony.Intermediate.ID || values["PKI_BOOTSTRAP_CONTROLLER_URL"] != "https://pki-controller.video-cloud-dev-video-cloud.svc:18446") || !bootstrap && len(values) != 1 {
					t.Fatal("wrong transient registration projection")
				}
			}
			if deploymentplan.ID(d.Object) == "Deployment/video-cloud-dev-video-cloud/pki-controller" && d.Retain {
				t.Fatal("resolved control write was left retained")
			}
			if deploymentplan.ID(d.Object) == "Deployment/video-cloud-dev-video-cloud/certissuer" {
				for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
					container := deploymentplan.Map(raw)
					for _, name := range []string{"CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS"} {
						if mqttFoundationEnv(container, name) != "" {
							t.Fatal("normal target reopened issuer provisioning", name)
						}
					}
				}
			}
		}
		if !seen || deploymentplan.Digest(l) != before {
			t.Fatal("frozen source mutated")
		}
	}
}

func TestCreateTargetsRetainOriginalSharedRuntimeWithoutMediaCredentialRewrite(t *testing.T) {
	cfg, l, installed := productTargetFixture(t)
	namespace, uid := "video-cloud-dev-video-cloud", "55555555-5555-4555-8555-555555555555"
	data := deploymentplan.Object{}
	for n := 0; n < 17; n++ {
		data[strings.Repeat("K", n+1)] = base64.StdEncoding.EncodeToString([]byte("original-" + strings.Repeat("v", n+1)))
	}
	data["AWS_ACCESS_KEY_ID"], data["AWS_SECRET_ACCESS_KEY"] = data["K"], data["KK"]
	delete(data, "K")
	delete(data, "KK")
	shared := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": namespace, "name": "video-cloud-runtime", "uid": uid, "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}}, "type": "Opaque", "data": data}
	id := deploymentplan.ID(shared)
	core := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": namespace, "name": "shared-core"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "video-cloud-runtime"}}}}}}}}}
	ns := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace}}
	bindings := environmentIdentityBindings{Stack: "video-cloud-dev", Owners: map[string]string{id: uid}}
	before := deploymentplan.Digest(shared)
	defs, err := bindRetainedEnvironmentDependencies([]deploymentplan.Definition{{Object: ns, Phase: "namespace"}, {Object: core, Phase: "workload"}}, []deploymentplan.Object{shared}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	l.SourceDefinitions = append(l.SourceDefinitions, defs...)
	l.SourceOwners[id] = uid
	installed.PlanSHA256 = productIdentityScope(l)
	frozen := deploymentplan.Digest(l)
	for _, phase := range []string{"fresh", "program-initial", "program-steady"} {
		t.Run(phase, func(t *testing.T) {
			target := defs
			if phase != "fresh" {
				var err error
				target, err = resolveCreateNormalTailProductTarget(cfg, l, installed, phase == "program-initial")
				if err != nil {
					t.Fatal(err)
				}
			}
			var selected *deploymentplan.Definition
			for _, d := range target {
				if deploymentplan.ID(d.Object) == id {
					if selected != nil || !d.Retain {
						t.Fatal("shared runtime became a producer or mutable target")
					}
					copy := d
					copy.Dependencies = nil
					selected = &copy
				}
			}
			if selected == nil {
				t.Fatal("shared runtime was dropped")
			}
			// The exact same dependency compiler is used by fresh and resolved
			// targets. Retaining it must never propose even a two-key update.
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("selected-release"), BindingsSHA256: deploymentplan.Digest(bindings)}, []deploymentplan.Definition{*selected}, []deploymentplan.Object{shared}, bindings.Owners)
			if err != nil || len(plan.Resources) != 1 {
				t.Fatal("shared runtime cannot compile as its original owner", err)
			}
			r := plan.Resources[0]
			if r.Action != "retain" || r.UID != uid || deploymentplan.Digest(r.Baseline["data"]) != deploymentplan.Digest(data) || len(deploymentplan.Map(r.Baseline["data"])) != 17 || deploymentplan.Digest(shared) != before || deploymentplan.Digest(l) != frozen {
				t.Fatal("shared credentials, raw fields, owner or frozen source changed")
			}
		})
	}
}

func TestCreateNormalTailProductTargetRejectsUnboundNativeOutputs(t *testing.T) {
	for _, mode := range []string{"schema", "parent", "scope", "root", "issuer", "session", "missing-receipt", "wrong-subject", "wrong-owner", "missing-consumer", "duplicate-consumer"} {
		t.Run(mode, func(t *testing.T) {
			cfg, l, i := productTargetFixture(t)
			switch mode {
			case "schema":
				i.Schema = "foreign"
			case "parent":
				i.ParentSHA256 = strings.Repeat("0", 64)
			case "scope":
				i.PlanSHA256 = strings.Repeat("0", 64)
			case "root":
				i.Ceremony.Root.Certificate = "foreign"
			case "issuer":
				i.Ceremony.Intermediate.ID = "55555555-5555-4555-8555-555555555555"
			case "session":
				i.Ceremony.BootstrapSessionID = ""
			case "missing-receipt":
				i.Outputs = nil
			case "wrong-subject":
				i.Outputs[0].Subject = "service:other"
			case "wrong-owner":
				i.Outputs[0].SecretUID = ""
			case "missing-consumer":
				environmentPKIPod(l.SourceDefinitions[2].Object)["volumes"] = nil
				i.PlanSHA256 = productIdentityScope(l)
			case "duplicate-consumer":
				d := l.SourceDefinitions[2]
				d.Object = deploymentplan.Clone(d.Object)
				deploymentplan.Map(d.Object["metadata"])["name"] = "duplicate"
				l.SourceDefinitions = append(l.SourceDefinitions, d)
				i.PlanSHA256 = productIdentityScope(l)
			}
			if _, err := resolveCreateNormalTailProductDefinitions(cfg, l, i); err == nil {
				t.Fatal("unbound native product output accepted")
			}
		})
	}
}
