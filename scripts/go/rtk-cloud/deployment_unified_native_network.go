package main

import (
	"context"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Native listeners use different ports and owner names from the legacy API
// skeletons. Keep their paths additive so accepted historical policies retain
// their original bodies.
func environmentNativeConnectivityDefinitions(env map[string]string) []deploymentplan.Definition {
	legacy := environmentNativeConnectivityDefinitionsV1(env)
	openbao := environmentNativeIngressPolicy(env["CLOUD_STACK_NAME"]+"-video-cloud", "allow-native-openbao-controller", "pki-controller", 18446, []deploymentplan.Object{environmentNativeNetworkPeer(env["CLOUD_STACK_NAME"]+"-secrets", []string{"openbao"})})
	return append([]deploymentplan.Definition{legacy[0], openbao}, legacy[1:]...)
}

// The existing runtime-dependency-access stage has this fixed V1 recipe. New
// maintained catalogs add dependencies through the current producer above;
// historical accepted plans must reproduce their original exact policy set.
func environmentNativeConnectivityDefinitionsV1(env map[string]string) []deploymentplan.Definition {
	stack := env["CLOUD_STACK_NAME"]
	enabled := func(name string) bool { return firstNonEmpty(env[name], env["LKE_"+name], "false") == "true" }
	peer := func(suffix string, names ...string) deploymentplan.Object {
		return environmentNativeNetworkPeer(stack+"-"+suffix, names)
	}
	policy := func(suffix, name, destination string, port float64, peers ...deploymentplan.Object) deploymentplan.Definition {
		return environmentNativeIngressPolicy(stack+"-"+suffix, name, destination, port, peers)
	}
	aliases := []string{"video-cloud-api-pki", "video-cloud-api-app-pki"}
	definitions := []deploymentplan.Definition{
		policy("video-cloud", "allow-native-service-issuer", "certissuer", 18445,
			peer("video-cloud", "certissuer", "pki-controller", "factoryenroll", "mqtt-pki", "video-cloud-api", "video-cloud-api-pki", "video-cloud-api-app-pki", "video-cloud-logingester"),
			peer("account-manager", "account-manager"), peer("secrets", "openbao")),
		policy("account-manager", "allow-native-account-service", "account-manager", 18443, peer("video-cloud", "factoryenroll")),
		policy("video-cloud", "allow-native-api-mqtt", "mqtt-pki", 8883, peer("video-cloud", "video-cloud-api-pki")),
		policy("video-cloud", "allow-native-api-turnregistry", "video-cloud-turnregistry", 18190, peer("video-cloud", aliases...)),
		policy("observability", "allow-grafana-prometheus-ingress", "video-cloud-prometheus", 9090, peer("observability", "video-cloud-grafana")),
	}
	if enabled("OTA_SERVICE_REGISTRATION_ENABLED") || enabled("OTA_CORE_CUTOVER_ENABLED") {
		definitions = append(definitions, policy("video-cloud", "allow-native-api-otaservice", "video-cloud-otaservice", 18084, peer("video-cloud", aliases...)))
	}
	var products []string
	for _, product := range []struct {
		name    string
		enabled bool
	}{
		{"video-cloud-mqttfoundation", enabled("MQTT_FOUNDATION_REGISTRATION_ENABLED")},
		{"video-cloud-shadowworker", enabled("SHADOW_WORKER_REGISTRATION_ENABLED")},
		{"video-cloud-otaservice", enabled("OTA_SERVICE_REGISTRATION_ENABLED")},
		{"video-cloud-videostorage", enabled("VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED")},
		{"video-cloud-webrtcservice", enabled("WEBRTC_SERVICE_REGISTRATION_ENABLED")},
	} {
		if product.enabled {
			products = append(products, product.name)
		}
	}
	if len(products) != 0 {
		definitions = append(definitions, policy("video-cloud", "allow-product-bootstrap-ack", "pki-controller", 18446, peer("video-cloud", products...)))
	}
	// The selected LKE cluster uses the standard kube-system DNS producer.
	// Both protocols are needed: DNS may retry a truncated UDP reply over TCP.
	definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": deploymentplan.Object{"name": "allow-grafana-dns", "namespace": stack + "-observability"},
		"spec": deploymentplan.Object{
			"podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "video-cloud-grafana"}},
			"policyTypes": []any{"Egress"},
			"egress": []any{deploymentplan.Object{
				"to": []any{deploymentplan.Object{
					"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"kubernetes.io/metadata.name": "kube-system"}},
					"podSelector":       deploymentplan.Object{"matchLabels": deploymentplan.Object{"k8s-app": "kube-dns"}},
				}},
				"ports": []any{deploymentplan.Object{"protocol": "UDP", "port": float64(53)}, deploymentplan.Object{"protocol": "TCP", "port": float64(53)}},
			}},
		},
	}})
	return definitions
}

func environmentNativeNetworkPeer(namespace string, names []string) deploymentplan.Object {
	names = append([]string(nil), names...)
	sort.Strings(names)
	values := make([]any, len(names))
	for i, name := range names {
		values[i] = name
	}
	return deploymentplan.Object{
		"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"kubernetes.io/metadata.name": namespace}},
		"podSelector":       deploymentplan.Object{"matchExpressions": []any{deploymentplan.Object{"key": "app.kubernetes.io/name", "operator": "In", "values": values}}},
	}
}

func environmentNativeIngressPolicy(namespace, name, destination string, port float64, peers []deploymentplan.Object) deploymentplan.Definition {
	from := make([]any, len(peers))
	for i, peer := range peers {
		from[i] = peer
	}
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": deploymentplan.Object{"name": name, "namespace": namespace},
		"spec": deploymentplan.Object{
			"podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": destination}},
			"policyTypes": []any{"Ingress"},
			"ingress":     []any{deploymentplan.Object{"from": from, "ports": []any{deploymentplan.Object{"protocol": "TCP", "port": port}}}},
		},
	}}
}

// A resumed/native product producer may run after namespace isolation already
// exists. Its temporary names come from the authenticated maintained recipes,
// never a wildcard or a user-supplied label prefix. Publish this dependency
// before the first enrollment child; keep it in that lifecycle's final catalog.
func environmentProductEnrollmentConnectivityDefinitions(ctx context.Context, cfg deploymentConfig, l *createNormalTailProductLifecycle, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if l == nil || len(l.Outputs) == 0 {
		return nil, nil
	}
	var names []string
	for _, output := range l.Outputs {
		definitions, err := productStageDefinitions(ctx, cfg, l, service, "enroll-"+strings.TrimPrefix(output.Subject, "service:"))
		if err != nil {
			return nil, err
		}
		found := false
		for _, definition := range definitions {
			if definition.Object["kind"] != "Deployment" {
				continue
			}
			meta := deploymentplan.Map(definition.Object["metadata"])
			template := deploymentplan.Map(deploymentplan.Map(definition.Object["spec"])["template"])
			labels := deploymentplan.Map(deploymentplan.Map(template["metadata"])["labels"])
			name := deploymentplan.String(labels["app.kubernetes.io/name"])
			if found || meta["namespace"] != l.Bootstrap.Stack+"-video-cloud" || name == "" || name != meta["name"] {
				return nil, environmentCreateBootstrapBlocker("product enrollment network dependency differs from its exact maintained owner")
			}
			names = append(names, name)
			found = true
		}
		if !found {
			return nil, environmentCreateBootstrapBlocker("product enrollment network dependency lacks its maintained owner")
		}
	}
	return []deploymentplan.Definition{environmentNativeIngressPolicy(l.Bootstrap.Stack+"-video-cloud", "allow-product-enrollment-"+productIdentityScope(l)[:12], "certissuer", 18445, []deploymentplan.Object{environmentNativeNetworkPeer(l.Bootstrap.Stack+"-video-cloud", names)})}, nil
}

// The original program's absence of this declared recipe preserves historical
// stage bytes. A new catalog marker must carry the complete maintained spec.
func environmentProductConnectivityDeclared(cfg deploymentConfig, l *createNormalTailProductLifecycle) (bool, error) {
	if l == nil {
		return false, nil
	}
	id := "NetworkPolicy/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/allow-native-service-issuer"
	var expected deploymentplan.Definition
	for _, d := range environmentNativeConnectivityDefinitions(deploymentEndpointValues(cfg)) {
		if deploymentplan.ID(d.Object) == id {
			expected = d
		}
	}
	found := false
	for _, d := range l.SourceDefinitions {
		if deploymentplan.ID(d.Object) != id {
			continue
		}
		if found || d.Phase != expected.Phase || d.Object["apiVersion"] != expected.Object["apiVersion"] || deploymentplan.Digest(d.Object["spec"]) != deploymentplan.Digest(expected.Object["spec"]) {
			return false, environmentCreateBootstrapBlocker("declared native enrollment network recipe differs from its frozen maintained catalog")
		}
		found = true
	}
	return found, nil
}

func environmentNativeConnectivityMarker(cfg deploymentConfig) deploymentplan.Definition {
	id := "NetworkPolicy/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/allow-native-service-issuer"
	for _, d := range environmentNativeConnectivityDefinitions(deploymentEndpointValues(cfg)) {
		if deploymentplan.ID(d.Object) == id {
			return d
		}
	}
	return deploymentplan.Definition{}
}

func environmentFreshConnectivitySource(cfg deploymentConfig, p environmentCreateBootstrapPlan) ([]deploymentplan.Definition, error) {
	if p.NativeConnectivitySHA256 == "" {
		return nil, nil
	}
	marker := environmentNativeConnectivityMarker(cfg)
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || len(p.ProductIdentities) == 0 || p.NativeConnectivitySHA256 != deploymentplan.Digest(marker) {
		return nil, environmentCreateBootstrapBlocker("fresh native connectivity recipe differs from its original qualified plan")
	}
	return []deploymentplan.Definition{marker}, nil
}

func validateEnvironmentFreshConnectivityCatalog(cfg deploymentConfig, p environmentCreateBootstrapPlan, definitions []deploymentplan.Definition) error {
	markers, err := environmentFreshConnectivitySource(cfg, p)
	if err != nil || len(markers) == 0 {
		return err
	}
	found := 0
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == deploymentplan.ID(markers[0].Object) {
			if deploymentplan.Digest(d) != p.NativeConnectivitySHA256 {
				return environmentCreateBootstrapBlocker("fresh full catalog differs from its declared native connectivity producer")
			}
			found++
		}
	}
	if found != 1 {
		return environmentCreateBootstrapBlocker("fresh full catalog lacks its declared native connectivity producer")
	}
	return nil
}
