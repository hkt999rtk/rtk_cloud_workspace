package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productTrustFixture(t *testing.T) (*createNormalTailProductLifecycle, environmentCreateRegisteredIssuer, []deploymentplan.Object, environmentIdentityBindings) {
	t.Helper()
	f := newProductProtocolFixture(t, true)
	return productTrustFixtureFromProtocol(t, f)
}

func productTrustFixtureFromProtocol(t *testing.T, f *productProtocolFixture) (*createNormalTailProductLifecycle, environmentCreateRegisteredIssuer, []deploymentplan.Object, environmentIdentityBindings) {
	t.Helper()
	l := f.l
	l.SourceDefinitions, l.SourceTrust = nil, nil
	l.SourceOwners = map[string]string{}
	var live []deploymentplan.Object
	add := func(object deploymentplan.Object, phase string) {
		id := deploymentplan.ID(object)
		uid := fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", len(live)+1)
		deploymentplan.Map(object["metadata"])["uid"] = uid
		l.SourceOwners[id] = uid
		live = append(live, object)
		l.SourceDefinitions = append(l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(object), Phase: phase})
	}
	manifest, err := environmentCreateServiceManifest(l.Root, l.Intermediate)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{l.Bootstrap.Stack + "-account-manager", l.Bootstrap.Stack + "-video-cloud", l.Bootstrap.Stack + "-secrets"} {
		cm := environmentCreateServiceTrustDefinition(ns, l.Root, f.runtime.TransportCAPEM, manifest).Object
		data := deploymentplan.Map(cm["data"])
		data["listener-crls.json"] = environmentCreateServiceCRLManifest(l.Root, environmentCreateServiceState+"/private/crl/listener-root.json", l.Intermediate)
		data["outbound-crls.json"] = environmentCreateServiceCRLManifest(l.Root, environmentCreateServiceState+"/private/crl/outbound-root.json", l.Intermediate)
		add(cm, "configuration")
	}
	ns := l.Bootstrap.Stack + "-video-cloud"
	add(deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-service-consumer-crls"}, "data": deploymentplan.Object{"crls.json": environmentCreateServiceCRLManifest(l.Root, environmentCreateServiceState+"/private/crl/outbound-root.json", l.Intermediate)}}, "configuration")
	add(deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-transport-trust"}, "data": deploymentplan.Object{"ca.crt": f.runtime.TransportCAPEM}}, "configuration")
	for _, role := range []string{"account-manager", "pki-controller", "certissuer", "factory-enroll", "video-cloud-api", "video-cloud-logingester", "pkibroker", "mqtt-pki", "openbao"} {
		namespace, kind, trust := ns, "Deployment", "pki-create-service-trust"
		if role == "account-manager" {
			namespace = l.Bootstrap.Stack + "-account-manager"
		}
		if role == "openbao" {
			namespace, kind = l.Bootstrap.Stack+"-secrets", "StatefulSet"
		}
		if role == "video-cloud-logingester" {
			trust = "pki-create-service-consumer-crls"
		}
		container := deploymentplan.Object{"name": role, "image": l.Bootstrap.Image, "volumeMounts": []any{deploymentplan.Object{"name": "trust", "mountPath": "/run/pki-service", "readOnly": true}}}
		if role == "pki-controller" {
			ids, _ := managedTransportServiceConsumerIDs(l.Policy.Consumers)
			container["env"] = environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": strings.Join(ids, ",")})
		}
		object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": namespace, "name": role}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"annotations": deploymentplan.Object{"original": "preserved"}}, "spec": deploymentplan.Object{"containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("trust", "configMap", trust)}}}}}
		if role == "openbao" {
			deploymentplan.Map(object["spec"])["updateStrategy"] = deploymentplan.Object{"type": "OnDelete"}
		}
		add(object, "workload")
	}
	bindings := environmentIdentityBindings{Environment: "dev", Stack: l.Bootstrap.Stack, Owners: l.SourceOwners}
	if err := bindCreateNormalTailProductSourceDefinitions(l, l.SourceDefinitions, live, bindings); err != nil {
		t.Fatal(err)
	}
	return l, f.next, live, bindings
}

func TestProductIdentitySuccessorTrustReloadsAllOriginalProfilesInTwoNativePhases(t *testing.T) {
	l, active, _, _ := productTrustFixture(t)
	before := deploymentplan.Digest(l)
	if len(l.SourceTrust) != 13 { // Three complete trust CMs, one CRL-only CM, nine native readers.
		t.Fatalf("incomplete frozen trust reader catalog: %d", len(l.SourceTrust))
	}
	ready := active
	ready.Document = deploymentplan.Clone(active.Document)
	ready.Status, ready.Document["status"] = "ready", "ready"
	for _, issuer := range []environmentCreateRegisteredIssuer{ready, active} {
		defs, err := productIdentityTrustStageDefinitions(l, issuer)
		if err != nil {
			t.Fatal(err)
		}
		maps, readers := 0, 0
		for _, d := range defs {
			if d.Object["kind"] == "ConfigMap" {
				maps++
				for key, value := range deploymentplan.Map(d.Object["data"]) {
					if !strings.Contains(key, "crls.json") {
						continue
					}
					var refs []productIdentityCRLReference
					if json.Unmarshal([]byte(deploymentplan.String(value)), &refs) != nil || len(refs) != map[bool]int{true: 3, false: 2}[issuer.Status == "active"] {
						t.Fatal("READY leaked into CRL members or ACTIVE successor was omitted")
					}
					if deploymentplan.Digest(refs[0].Issuer) != deploymentplan.Digest(l.Root.Document) || deploymentplan.Digest(refs[1].Issuer) != deploymentplan.Digest(l.Intermediate.Document) {
						t.Fatal("original root or ICA document changed")
					}
					if issuer.Status == "active" && (deploymentplan.Digest(refs[2].Issuer) != deploymentplan.Digest(active.Document) || refs[2].StatePath != strings.TrimSuffix(refs[0].StatePath, ".json")+"-"+active.ID+".json") {
						t.Fatal("successor CRL lineage or native private state path changed")
					}
				}
				continue
			}
			readers++
			var original deploymentplan.Object
			for _, source := range l.SourceTrust {
				if source.ID == deploymentplan.ID(d.Object) {
					original = deploymentplan.Clone(source.Baseline)
				}
			}
			annotations := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"])["annotations"]
			if deploymentplan.Map(annotations)["rtk.realtek.com/pki-create-bundle-sha256"] == nil || deploymentplan.Map(d.Object["metadata"])["uid"] != l.SourceOwners[deploymentplan.ID(d.Object)] {
				t.Fatal("native reload lacks original owner or actual template change")
			}
			clean := deploymentplan.Clone(d.Object)
			delete(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(clean["spec"])["template"])["metadata"])["annotations"]), "rtk.realtek.com/pki-create-bundle-sha256")
			if deploymentplan.Digest(original) != deploymentplan.Digest(clean) {
				t.Fatal("early reload changed original image, identity, mounts or other intent")
			}
		}
		wantMaps, wantReaders := 3, 8
		if issuer.Status == "active" {
			wantMaps, wantReaders = 4, 9
		}
		if maps != wantMaps || readers != wantReaders {
			t.Fatalf("%s distribution omitted current readers: maps=%d readers=%d", issuer.Status, maps, readers)
		}
		resolved, err := productIdentityTrustDefinitions(l, issuer)
		if err != nil || len(resolved) != wantMaps {
			t.Fatal("final target trust resolver copied early current workload baselines", err)
		}
	}
	consumers, err := productIdentityCRLConsumers(l)
	want, _ := managedTransportServiceConsumerIDs(l.Policy.Consumers)
	if err != nil || !slices.Equal(consumers, want) || len(consumers) != 9 || deploymentplan.Digest(l) != before {
		t.Fatal("native CRL roster or frozen source changed", err)
	}
}

func TestProductIdentitySuccessorTrustRejectsForeignLineagePathsAndReaders(t *testing.T) {
	for _, mode := range []string{"old-document", "old-path", "duplicate", "foreign-root", "unknown-field", "ready-crl", "owner", "roster"} {
		t.Run(mode, func(t *testing.T) {
			l, next, _, _ := productTrustFixture(t)
			cm := deploymentplan.Map(l.SourceTrust[0].Baseline["data"])
			var refs []productIdentityCRLReference
			json.Unmarshal([]byte(deploymentplan.String(cm["listener-crls.json"])), &refs)
			switch mode {
			case "old-document":
				refs[1].Issuer["signer_reference"] = "foreign"
			case "old-path":
				refs[1].StatePath = "/var/lib/pki-service/private/crl/../foreign.json"
			case "duplicate":
				refs = append(refs, refs[1])
			case "foreign-root":
				refs[0].Issuer["certificate_pem"] = "foreign"
			case "unknown-field":
				var malformed []deploymentplan.Object
				json.Unmarshal([]byte(deploymentplan.String(cm["listener-crls.json"])), &malformed)
				malformed[1]["unexpected"] = true
				raw, _ := json.Marshal(malformed)
				cm["listener-crls.json"] = string(raw)
				if _, err := productIdentityTrustStageDefinitions(l, next); err == nil {
					t.Fatal("malformed original issuer reference silently disappeared")
				}
				return
			case "ready-crl":
				next.Status = "ready"
				if _, _, _, err := productIdentityTrustManifest(l, deploymentplan.String(cm["listener-crls.json"]), &next, true); err == nil {
					t.Fatal("READY CRL member accepted")
				}
				return
			case "owner":
				for i := range l.SourceTrust {
					if l.SourceTrust[i].Baseline["kind"] == "Deployment" {
						deploymentplan.Map(l.SourceTrust[i].Baseline["metadata"])["uid"] = "foreign"
						break
					}
				}
			case "roster":
				for _, d := range l.SourceDefinitions {
					if deploymentplan.Map(d.Object["metadata"])["name"] == "pki-controller" {
						c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
						setEnvironmentContainerEnv(c, "PKI_REQUIRED_CONSUMERS_SERVICE", "account-manager")
					}
				}
				if _, err := productIdentityCRLConsumers(l); err == nil {
					t.Fatal("incomplete current CRL roster accepted")
				}
				return
			}
			raw, _ := json.Marshal(refs)
			cm["listener-crls.json"] = string(raw)
			if _, err := productIdentityTrustStageDefinitions(l, next); err == nil {
				t.Fatal("foreign trust transition accepted")
			}
		})
	}
}
