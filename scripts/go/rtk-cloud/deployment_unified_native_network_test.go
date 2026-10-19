package main

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Model Kubernetes selector intersections and named destination ports. Checking
// the union of all selecting policies catches a wrong destination, namespace,
// direction or Service target even if the expected strings occur in a manifest.
func nativeNetworkSelectorMatches(selector, labels deploymentplan.Object) bool {
	for key, value := range deploymentplan.Map(selector["matchLabels"]) {
		if labels[key] != value {
			return false
		}
	}
	for _, raw := range certIssuerObjectList(selector["matchExpressions"]) {
		e := deploymentplan.Map(raw)
		_, exists := labels[deploymentplan.String(e["key"])]
		found := false
		for _, value := range certIssuerObjectList(e["values"]) {
			found = found || labels[deploymentplan.String(e["key"])] == value
		}
		switch e["operator"] {
		case "In":
			if !found {
				return false
			}
		case "NotIn":
			if found {
				return false
			}
		case "Exists":
			if !exists {
				return false
			}
		case "DoesNotExist":
			if exists {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func nativeNetworkPodLabels(object deploymentplan.Object) deploymentplan.Object {
	return deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["metadata"])["labels"].(map[string]any)
}

func nativeNetworkPolicyAllows(definitions []deploymentplan.Definition, source, destination deploymentplan.Object, protocol string, port float64, egress bool) bool {
	selected := source
	if !egress {
		selected = destination
	}
	selectedNS := deploymentplan.String(deploymentplan.Map(selected["metadata"])["namespace"])
	isolated := false
	for _, d := range definitions {
		if d.Object["kind"] != "NetworkPolicy" || deploymentplan.Map(d.Object["metadata"])["namespace"] != selectedNS {
			continue
		}
		spec := deploymentplan.Map(d.Object["spec"])
		if !nativeNetworkSelectorMatches(deploymentplan.Map(spec["podSelector"]), nativeNetworkPodLabels(selected)) {
			continue
		}
		direction, rules, peerField := "Ingress", "ingress", "from"
		if egress {
			direction, rules, peerField = "Egress", "egress", "to"
		}
		foundDirection := false
		for _, p := range certIssuerObjectList(spec["policyTypes"]) {
			foundDirection = foundDirection || p == direction
		}
		if !foundDirection {
			continue
		}
		isolated = true
		for _, raw := range certIssuerObjectList(spec[rules]) {
			rule := deploymentplan.Map(raw)
			portOK := len(certIssuerObjectList(rule["ports"])) == 0
			for _, rawPort := range certIssuerObjectList(rule["ports"]) {
				p := deploymentplan.Map(rawPort)
				if firstNonEmpty(deploymentplan.String(p["protocol"]), "TCP") != protocol {
					continue
				}
				if p["port"] == port {
					portOK = true
				}
				if name, ok := p["port"].(string); ok {
					for _, rawContainer := range certIssuerObjectList(environmentPKIPod(destination)["containers"]) {
						for _, rawNamed := range certIssuerObjectList(deploymentplan.Map(rawContainer)["ports"]) {
							named := deploymentplan.Map(rawNamed)
							if named["name"] == name && named["containerPort"] == port {
								portOK = true
							}
						}
					}
				}
			}
			if !portOK {
				continue
			}
			peers := certIssuerObjectList(rule[peerField])
			if len(peers) == 0 {
				return true
			}
			other := source
			if egress {
				other = destination
			}
			otherNS := deploymentplan.String(deploymentplan.Map(other["metadata"])["namespace"])
			for _, rawPeer := range peers {
				peer := deploymentplan.Map(rawPeer)
				if peer["ipBlock"] != nil {
					continue
				}
				if peer["namespaceSelector"] != nil {
					labels := deploymentplan.Object{"kubernetes.io/metadata.name": otherNS}
					for _, namespace := range definitions {
						if namespace.Object["kind"] == "Namespace" && deploymentplan.Map(namespace.Object["metadata"])["name"] == otherNS {
							for k, v := range deploymentplan.Map(deploymentplan.Map(namespace.Object["metadata"])["labels"]) {
								labels[k] = v
							}
						}
					}
					if !nativeNetworkSelectorMatches(deploymentplan.Map(peer["namespaceSelector"]), labels) {
						continue
					}
				} else if otherNS != selectedNS {
					continue
				}
				if peer["podSelector"] != nil && !nativeNetworkSelectorMatches(deploymentplan.Map(peer["podSelector"]), nativeNetworkPodLabels(other)) {
					continue
				}
				return true
			}
		}
	}
	return !isolated
}

func nativeNetworkServicePort(t *testing.T, definitions []deploymentplan.Definition, service deploymentplan.Object, exposed float64) (deploymentplan.Object, float64) {
	t.Helper()
	spec, ns := deploymentplan.Map(service["spec"]), deploymentplan.Map(service["metadata"])["namespace"]
	for _, d := range definitions {
		if environmentPKIPod(d.Object) == nil || deploymentplan.Map(d.Object["metadata"])["namespace"] != ns || !nativeNetworkSelectorMatches(deploymentplan.Object{"matchLabels": spec["selector"]}, nativeNetworkPodLabels(d.Object)) {
			continue
		}
		for _, raw := range certIssuerObjectList(spec["ports"]) {
			p := deploymentplan.Map(raw)
			if p["port"] != exposed {
				continue
			}
			if p["targetPort"] == nil {
				return d.Object, exposed
			}
			if number, ok := p["targetPort"].(float64); ok {
				return d.Object, number
			}
			for _, c := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				for _, rawNamed := range certIssuerObjectList(deploymentplan.Map(c)["ports"]) {
					named := deploymentplan.Map(rawNamed)
					if named["name"] == p["targetPort"] {
						return d.Object, named["containerPort"].(float64)
					}
				}
			}
		}
	}
	t.Fatal("maintained Service has no actual destination/listener", deploymentplan.ID(service), exposed)
	return nil, 0
}

func TestNativeConnectivityFullCatalogUsesActualListenersAndNamespacePodPeers(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-native-network")
			makeIsolatedTestSecretStore(t, environment)
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			// Native managed Account/issuer recipes supply their genuine named
			// listeners. Merge their exact observed bindings into the full renderer.
			cfg, bootstrap, _, runtime, service, roots, intermediates, initialApplications := createApplicationFinalizeFixture(t, environment)
			cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], cfg.Values["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "true", "true", "9444"
			fresh, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, bootstrap, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			var native []deploymentplan.Object
			for _, id := range []string{"Deployment/" + bootstrap.Stack + "-account-manager/account-manager", "Service/" + bootstrap.Stack + "-account-manager/account-manager"} {
				o := deploymentplan.Clone(createRecipeObject(t, fresh, id))
				deploymentplan.Map(o["metadata"])["uid"] = "original-" + deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
				native = append(native, o)
			}
			applications, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, bootstrap, release, runtime, service, roots, intermediates, initialApplications)
			if err != nil {
				t.Fatal(err)
			}
			_, _, issuer, secrets := registryIssuerCanonicalFixture(t, environment)
			issuerService := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": bootstrap.Stack + "-video-cloud", "uid": "original-native-issuer-service"}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"app.kubernetes.io/name": "certissuer"}, "ports": []any{deploymentplan.Object{"name": "https", "port": float64(18445), "targetPort": float64(18445)}}}}
			native = append(native, issuer, issuerService)
			native = append(native, secrets...)
			observed, err := observeEnvironmentBindings(environment, release, native)
			if err != nil {
				t.Fatal(err)
			}
			var kept []environmentPodBindings
			for _, binding := range bindings.Pods {
				if binding.Name != "certissuer" {
					kept = append(kept, binding)
				}
			}
			bindings.Pods = append(kept, observed.Pods...)
			for id, uid := range observed.Owners {
				bindings.Owners[id] = uid
			}
			env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "true", "true", "9444"
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range applications {
				if deploymentplan.ID(d.Object) == "Deployment/"+bootstrap.Stack+"-video-cloud/video-cloud-api-app-pki" {
					definitions = append(definitions, d)
				}
			}
			// Include each actual product renderer without fabricating a Pod or
			// Secret. The exact enablement is also the ACK policy input.
			productEnv := appendMap(env, map[string]string{"MQTT_FOUNDATION_REGISTRATION_ENABLED": "true", "SHADOW_WORKER_REGISTRATION_ENABLED": "true", "OTA_SERVICE_REGISTRATION_ENABLED": "true", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "true", "WEBRTC_SERVICE_REGISTRATION_ENABLED": "true"})
			for _, manifest := range []string{lkeMQTTFoundationDeploymentManifest(productEnv), lkeShadowWorkerDeploymentManifest(productEnv), lkeOTAServiceDeploymentManifest(productEnv), lkeVideoStorageServiceDeploymentManifest(productEnv), lkeWebRTCServiceDeploymentManifest(productEnv), lkeOTAServiceServiceManifest(productEnv)} {
				extra, err := decodeEnvironmentCreateManifests(manifest)
				if err != nil {
					t.Fatal(err)
				}
				definitions = append(definitions, extra...)
			}
			for _, d := range environmentNativeConnectivityDefinitions(productEnv) {
				if deploymentplan.Map(d.Object["metadata"])["name"] == "allow-product-bootstrap-ack" || deploymentplan.Map(d.Object["metadata"])["name"] == "allow-native-api-otaservice" {
					definitions = append(definitions, d)
				}
			}
			byID := map[string]deploymentplan.Object{}
			for _, d := range definitions {
				byID[deploymentplan.ID(d.Object)] = d.Object
			}
			stack := bootstrap.Stack
			pod := func(suffix, name string) deploymentplan.Object {
				o := byID["Deployment/"+stack+"-"+suffix+"/"+name]
				if o == nil {
					o = byID["StatefulSet/"+stack+"-"+suffix+"/"+name]
				}
				if o == nil {
					t.Fatal("missing maintained Pod", suffix, name)
				}
				return o
			}
			check := func(sourceSuffix, sourceName, targetSuffix, serviceName string, servicePort float64) {
				source := pod(sourceSuffix, sourceName)
				target, targetPort := nativeNetworkServicePort(t, definitions, byID["Service/"+stack+"-"+targetSuffix+"/"+serviceName], servicePort)
				if !nativeNetworkPolicyAllows(definitions, source, target, "TCP", targetPort, false) || !nativeNetworkPolicyAllows(definitions, source, target, "TCP", targetPort, true) {
					t.Fatal("maintained endpoint is denied", sourceName, serviceName, servicePort, targetPort)
				}
			}
			for _, caller := range []struct{ suffix, name string }{{"account-manager", "account-manager"}, {"video-cloud", "certissuer"}, {"video-cloud", "pki-controller"}, {"video-cloud", "factoryenroll"}, {"video-cloud", "mqtt-pki"}, {"video-cloud", "video-cloud-api"}, {"video-cloud", "video-cloud-api-pki"}, {"video-cloud", "video-cloud-logingester"}, {"secrets", "openbao"}} {
				check(caller.suffix, caller.name, "video-cloud", "certissuer", 18445)
			}
			// OpenBao is rendered from its retained StatefulSet profile and has no
			// generic PKI-consumer Pod label. Its native CRL sweeper still calls
			// the Controller, independently of its CertIssuer renewal endpoint.
			check("secrets", "openbao", "video-cloud", "pki-controller", 18446)
			openbaoSource := deploymentplan.Clone(pod("secrets", "openbao"))
			delete(nativeNetworkPodLabels(openbaoSource), "rtk.cloud/pki-consumer")
			if !nativeNetworkPolicyAllows(definitions, openbaoSource, pod("video-cloud", "pki-controller"), "TCP", 18446, false) {
				t.Fatal("retained OpenBao with no generic consumer label cannot fetch/ACK Controller CRLs")
			}
			// The exact new policy is necessary even if an original retained Pod
			// has no generic consumer label. Removing it or changing its port
			// must not be hidden by a broad peer elsewhere in the catalog.
			for _, mode := range []string{"missing-policy", "changed-policy-port"} {
				var broken []deploymentplan.Definition
				found := false
				for _, d := range definitions {
					if deploymentplan.ID(d.Object) != "NetworkPolicy/"+stack+"-video-cloud/allow-native-openbao-controller" {
						broken = append(broken, d)
						continue
					}
					if found || d.Phase != "configuration" {
						t.Fatal("OpenBao Controller dependency lacks one early configuration producer")
					}
					found = true
					if mode == "changed-policy-port" {
						d.Object = deploymentplan.Clone(d.Object)
						ingress := certIssuerObjectList(deploymentplan.Map(d.Object["spec"])["ingress"])
						ports := certIssuerObjectList(deploymentplan.Map(ingress[0])["ports"])
						deploymentplan.Map(ports[0])["port"] = float64(18445)
						broken = append(broken, d)
					}
				}
				if !found || nativeNetworkPolicyAllows(broken, openbaoSource, pod("video-cloud", "pki-controller"), "TCP", 18446, false) {
					t.Fatal("missing or changed OpenBao Controller policy was accepted", mode)
				}
			}
			for _, mode := range []string{"foreign-namespace", "foreign-pod", "wrong-port", "wrong-protocol"} {
				foreign := deploymentplan.Clone(openbaoSource)
				protocol, port := "TCP", float64(18446)
				switch mode {
				case "foreign-namespace":
					deploymentplan.Map(foreign["metadata"])["namespace"] = "foreign-secrets"
				case "foreign-pod":
					nativeNetworkPodLabels(foreign)["app.kubernetes.io/name"] = "unrelated"
				case "wrong-port":
					port = 18445
				case "wrong-protocol":
					protocol = "UDP"
				}
				if nativeNetworkPolicyAllows(definitions, foreign, pod("video-cloud", "pki-controller"), protocol, port, false) {
					t.Fatal("OpenBao Controller policy broadened peer or port", mode)
				}
			}
			check("video-cloud", "factoryenroll", "account-manager", "account-manager", 18443)
			for _, name := range []string{"video-cloud-mqttfoundation", "video-cloud-shadowworker", "video-cloud-otaservice", "video-cloud-videostorage", "video-cloud-webrtcservice"} {
				check("video-cloud", name, "account-manager", "account-manager", 8443)
				check("video-cloud", name, "video-cloud", "pki-controller", 18446)
			}
			_, named := nativeNetworkServicePort(t, definitions, byID["Service/"+stack+"-account-manager/account-manager"], 8443)
			if named != 9444 {
				t.Fatal("registration Service exposed port was confused with listener", named)
			}
			check("video-cloud", "certissuer", "secrets", "openbao", 8200)
			check("video-cloud", "mqtt-pki", "video-cloud", "video-cloud-api-pki", 18447)
			check("video-cloud", "video-cloud-api-pki", "video-cloud", "mqtt-pki", 8883)
			check("observability", "video-cloud-grafana", "observability", "video-cloud-prometheus", 9090)
			check("video-cloud", "video-cloud-api-pki", "video-cloud", "video-cloud-turnregistry", 18190)
			check("video-cloud", "video-cloud-api-app-pki", "video-cloud", "video-cloud-turnregistry", 18190)
			check("video-cloud", "video-cloud-api-pki", "video-cloud", "video-cloud-otaservice", 18084)
			check("video-cloud", "video-cloud-api-app-pki", "video-cloud", "video-cloud-otaservice", 18084)
			check("video-cloud", "video-cloud-api", "platform", "redis", 6379)
			// DNS producer labels are provider metadata confirmed independently;
			// the autoscaler is deliberately not accepted by this selector.
			dns := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"namespace": "kube-system", "name": "coredns"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"k8s-app": "kube-dns"}}, "spec": deploymentplan.Object{}}}}
			for _, protocol := range []string{"UDP", "TCP"} {
				if !nativeNetworkPolicyAllows(definitions, pod("observability", "video-cloud-grafana"), dns, protocol, 53, true) {
					t.Fatal("Grafana DNS blocked", protocol)
				}
			}
			for _, tc := range []struct {
				suffix, name, targetSuffix, target string
				port                               float64
			}{{"frontend", "frontend", "video-cloud", "certissuer", 18445}, {"video-cloud", "video-cloud-mqttfoundation", "account-manager", "account-manager", 18443}, {"video-cloud", "video-cloud-api", "video-cloud", "pki-controller", 18445}} {
				if nativeNetworkPolicyAllows(definitions, pod(tc.suffix, tc.name), pod(tc.targetSuffix, tc.target), "TCP", tc.port, false) {
					t.Fatal("additive policy broadened caller or port", tc)
				}
			}
			foreign := deploymentplan.Clone(pod("video-cloud", "video-cloud-mqttfoundation"))
			deploymentplan.Map(foreign["metadata"])["namespace"] = "foreign-video-cloud"
			if nativeNetworkPolicyAllows(definitions, foreign, pod("video-cloud", "pki-controller"), "TCP", 18446, false) {
				t.Fatal("namespace and Pod selectors were incorrectly ORed")
			}
			wrongDNS := deploymentplan.Clone(dns)
			nativeNetworkPodLabels(wrongDNS)["k8s-app"] = "kube-dns-autoscaler"
			if nativeNetworkPolicyAllows(definitions, pod("observability", "video-cloud-grafana"), wrongDNS, "UDP", 53, true) || nativeNetworkPolicyAllows(definitions, pod("observability", "video-cloud-grafana"), dns, "TCP", 9153, true) {
				t.Fatal("Grafana gained non-DNS egress")
			}
		})
	}
}

func TestNativeConnectivityPoliciesPreserveHistoricalRulesAndExactEnrollmentPeers(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	before := deploymentplan.Digest(f.l)
	_, _, _, service := createServiceRecipeFixture(t, "dev")
	service.Root, service.Intermediate = f.l.Root, f.l.Intermediate
	definitions, err := environmentProductEnrollmentConnectivityDefinitions(t.Context(), f.cfg, f.l, service)
	if err != nil || len(definitions) != 1 {
		t.Fatal(err)
	}
	if deploymentplan.Digest(f.l) != before {
		t.Fatal("dependency generation changed immutable product scope")
	}
	selector := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(definitions[0].Object["spec"])["ingress"])[0])["from"].([]any)[0])["podSelector"])
	var names []string
	for _, product := range f.l.Outputs {
		defs, err := productStageDefinitions(t.Context(), f.cfg, f.l, service, "enroll-"+strings.TrimPrefix(product.Subject, "service:"))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range defs {
			if d.Object["kind"] != "Deployment" {
				continue
			}
			labels := nativeNetworkPodLabels(d.Object)
			if !nativeNetworkSelectorMatches(selector, labels) {
				t.Fatal("actual enrollment producer denied")
			}
			names = append(names, deploymentplan.String(labels["app.kubernetes.io/name"]))
		}
	}
	if len(names) != 5 || len(slices.Compact(slices.Sorted(slices.Values(names)))) != 5 {
		t.Fatal("expected five unique exact enrollment owners")
	}
	if nativeNetworkSelectorMatches(selector, deploymentplan.Object{"app.kubernetes.io/name": "pki-product-foreign"}) {
		t.Fatal("temporary prefix granted arbitrary producer access")
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		env, _, _ := completeEnvironmentRenderFixture(environment)
		old, err := decodeEnvironmentCreateManifests(lkeAllowAccountManagerCertIssuerNetworkPolicyManifest(env), lkeAllowVideoCloudAccountManagerNetworkPolicyManifest(env), lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env))
		if err != nil {
			t.Fatal(err)
		}
		original := deploymentplan.Digest(old)
		extra := environmentNativeConnectivityDefinitions(env)
		for _, o := range old {
			for _, d := range extra {
				if deploymentplan.ID(o.Object) == deploymentplan.ID(d.Object) {
					t.Fatal("historical policy was replaced")
				}
			}
		}
		if original != deploymentplan.Digest(old) {
			t.Fatal("original policy changed")
		}
	}
}

func TestNativeConnectivityMarkerWiresOriginalControlsWithoutChangingHistoricalStages(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	_, _, _, service := createServiceRecipeFixture(t, "dev")
	service.Root, service.Intermediate = f.l.Root, f.l.Intermediate
	legacy, err := productIdentityControlDefinitions(f.cfg, f.l, service, true)
	if err != nil {
		t.Fatal(err)
	}
	legacySHA := deploymentplan.Digest(legacy)
	for _, d := range environmentNativeConnectivityDefinitions(deploymentEndpointValues(f.cfg)) {
		if deploymentplan.Map(d.Object["metadata"])["name"] == "allow-native-service-issuer" {
			f.l.SourceDefinitions = append(f.l.SourceDefinitions, d)
		}
	}
	markerSHA := deploymentplan.Digest(f.l)
	for _, bootstrap := range []bool{true, false} {
		controls, err := productIdentityControlDefinitions(f.cfg, f.l, service, bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		var dependencies []deploymentplan.Definition
		for _, d := range controls {
			if d.Object["kind"] == "NetworkPolicy" {
				dependencies = append(dependencies, d)
			}
		}
		want, err := environmentProductEnrollmentConnectivityDefinitions(t.Context(), f.cfg, f.l, service)
		if err != nil || len(dependencies) != 1 || deploymentplan.Digest(dependencies) != deploymentplan.Digest(want) {
			t.Fatal("shared controls omitted exact declared producer before enrollment or from final catalog", err)
		}
		if bootstrap && deploymentplan.Digest(controls[:len(controls)-1]) != legacySHA {
			t.Fatal("network producer changed historical control bodies")
		}
	}
	if deploymentplan.Digest(f.l) != markerSHA {
		t.Fatal("control rendering mutated the frozen source marker")
	}
	marker := &f.l.SourceDefinitions[len(f.l.SourceDefinitions)-1]
	deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(marker.Object["spec"])["ingress"])[0])["ports"].([]any)[0])["port"] = float64(9443)
	if _, err := productIdentityControlDefinitions(f.cfg, f.l, service, true); err == nil {
		t.Fatal("foreign marker silently activated the new producer")
	}
	f.l.SourceDefinitions = f.l.SourceDefinitions[:len(f.l.SourceDefinitions)-1]
	recovered, err := productStageDefinitions(t.Context(), f.cfg, f.l, service, "session-controls")
	if err != nil || deploymentplan.Digest(recovered) != legacySHA {
		t.Fatal("historical source lost its exact original session-controls recipe", err)
	}
}

func TestNativeConnectivityFreshQualifiedProducerPublishesBeforeActualFiveEnrollments(t *testing.T) {
	f := newProductProtocolFixture(t, false, true)
	f.unscoped = true
	f.runtime.ProductIdentityScope = ""
	f.runtime.PKIClient.mutationScope = ""
	var err error
	f.subjects, err = environmentCreatePlannedServiceSubjects(f.cfg, f.p)
	if err != nil || len(f.subjects) != 14 {
		t.Fatal(err)
	}
	crl, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := createEnvironmentServiceSession(t.Context(), f.cfg, f.p, f.store, f.runtime, environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, RootCRLPEM: crl, RootCRLSecret: "pki-service-root-crl"}, f.subjects, f.apply)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := f.controls(service, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.apply(t.Context(), "managed-complete-issuer", controls); err != nil {
		t.Fatal(err)
	}
	enrolled := 0
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if strings.Contains(stage, "-enroll-") {
			for _, d := range defs {
				if d.Object["kind"] != "Deployment" {
					continue
				}
				var policies []deploymentplan.Definition
				for _, o := range f.b.objects {
					if o["kind"] == "NetworkPolicy" {
						policies = append(policies, deploymentplan.Definition{Object: o})
					}
				}
				// Isolation is explicit before the real native enrollment boundary.
				deny, err := decodeEnvironmentCreateManifests(lkeDefaultDenyIngressNetworkPolicyManifest(deploymentEndpointValues(f.cfg), f.p.Stack+"-video-cloud"))
				if err != nil {
					return err
				}
				policies = append(policies, deny...)
				target := f.b.objects["Deployment/"+f.p.Stack+"-video-cloud/certissuer"]
				if !nativeNetworkPolicyAllows(policies, d.Object, target, "TCP", 18445, false) {
					t.Fatal("actual fresh enrollment started before its declared network producer")
				}
				enrolled++
			}
		}
		return f.apply(ctx, stage, defs)
	}
	if err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, service, apply); err != nil {
		t.Fatal(err)
	}
	if enrolled != 5 {
		t.Fatal("actual producer did not run all five enrollments", enrolled)
	}
	program, err := readFreshProductProgram(f.store, f.e)
	if err != nil || len(program.Lifecycle.SourceDefinitions) != 1 {
		t.Fatal("fresh source lost its qualified marker", err)
	}
	installed, err := readProductInstalled(f.store, &program.Lifecycle)
	if err != nil || len(installed.Outputs) != 5 {
		t.Fatal("native producer did not publish five genuine outputs", err)
	}
	key := "product-identities-" + productIdentityScope(&program.Lifecycle)[:12] + "-enrollment-network"
	plan, err := readEnvironmentCreateStageInput(f.store, f.e, key)
	if err != nil || plan == nil {
		t.Fatal("missing ordinary network child", err)
	}
	journal, _, err := readEnvironmentCreateStageProgress(f.store, f.e, key, plan)
	if err != nil || !journal.Accepted || len(journal.Owners) != 1 {
		t.Fatal("network producer has no genuine accepted owner", err)
	}
	writes := deploymentplan.Digest(f.b.writes)
	if err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, service, apply); err != nil {
		t.Fatal("same accepted fresh producer retry", err)
	}
	if deploymentplan.Digest(f.b.writes) != writes || enrolled != 5 {
		t.Fatal("retry rewrote or reenrolled native identities")
	}
}

func TestNativeConnectivityFreshPlanQualifiesItsActualFullCatalogMarker(t *testing.T) {
	cfg, store, release, operator, p := createDependencyContractFixture(t)
	if p.NativeConnectivitySHA256 == "" || p.NativeConnectivitySHA256 != deploymentplan.Digest(environmentNativeConnectivityMarker(cfg)) {
		t.Fatal("new plan omitted qualified network producer")
	}
	if _, err := qualifyEnvironmentCreateDependencyContract(t.Context(), cfg, release, store, operator, p); err != nil {
		t.Fatal("actual full fresh catalog", err)
	}
	marker := environmentNativeConnectivityMarker(cfg)
	if err := validateEnvironmentFreshConnectivityCatalog(cfg, p, nil); err == nil {
		t.Fatal("missing declared producer accepted")
	}
	bad := deploymentplan.Definition{Object: deploymentplan.Clone(marker.Object), Phase: marker.Phase}
	deploymentplan.Map(bad.Object["spec"])["podSelector"] = deploymentplan.Object{}
	if err := validateEnvironmentFreshConnectivityCatalog(cfg, p, []deploymentplan.Definition{bad}); err == nil {
		t.Fatal("broadened catalog producer accepted")
	}
	p.NativeConnectivitySHA256 = ""
	if source, err := environmentFreshConnectivitySource(cfg, p); err != nil || len(source) != 0 {
		t.Fatal("historical fresh plan gained a new recipe", err)
	}
	if strings.Contains(string(mustProviderRecoveryJSON(p)), "native_connectivity_sha256") {
		t.Fatal("empty marker changed historical serialized plan bytes")
	}
}
