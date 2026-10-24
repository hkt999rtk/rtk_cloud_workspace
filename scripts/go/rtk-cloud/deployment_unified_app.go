package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

const environmentAppTrustInitialize = "umask 077; mkdir -p /run/pki-state/app; chmod 700 /run/pki-state/app; if [ ! -e /run/pki-state/app/root-policy.json ]; then cp /run/pki-app/root-trust.json /run/pki-state/app/root-policy.json.new; chmod 600 /run/pki-state/app/root-policy.json.new; mv /run/pki-state/app/root-policy.json.new /run/pki-state/app/root-policy.json; fi"
const environmentAppNativeTrustInitialize = "umask 077; mkdir -p /run/pki-state/app; chmod 700 /run/pki-state/app; if [ -e /run/pki-state/app/root-policy.json ]; then chmod 600 /run/pki-state/app/root-policy.json; fi"

func checkEnvironmentAppTURNPlan(p *deploymentplan.Plan) error {
	for _, r := range p.Resources {
		if r.Action != "retire" {
			if err := validateEnvironmentAppTURNPolicy([]deploymentplan.Definition{{Object: r.Desired}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Match the formal API's App PKI TURN revocation cutoff. Admission cannot
// validate application durations, and preflight must not depend on a live API.
func validateEnvironmentAppTURNPolicy(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		if d.Object["kind"] != "Deployment" || deploymentplan.Map(d.Object["metadata"])["name"] != "video-cloud-api-app-pki" {
			continue
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] != "app" {
				continue
			}
			settings, err := environmentPKINamedFields(c["env"])
			blocked := func() error {
				return &deploymentplan.Blocker{ID: "deployment.configuration.app-turn-ttl", Resource: deploymentplan.ID(d.Object), Cause: "App PKI with TURN requires an explicit positive VIDEO_CLOUD_TURN_CREDENTIAL_TTL no greater than 1m", Next: "maintain the App API's bounded TTL in the common renderer, preserve the other API roles and original identities, then repeat full preflight"}
			}
			if err != nil {
				return blocked()
			}
			if settings["VIDEO_CLOUD_AUTH_APP_PKI_ENABLED"]["value"] != "true" || deploymentplan.String(settings["VIDEO_CLOUD_WEBRTC_TURN_URLS"]["value"]) == "" {
				continue
			}
			ttl := settings["VIDEO_CLOUD_TURN_CREDENTIAL_TTL"]
			duration, err := time.ParseDuration(deploymentplan.String(ttl["value"]))
			if ttl["valueFrom"] != nil || err != nil || duration <= 0 || duration > time.Minute {
				return blocked()
			}
		}
	}
	return nil
}

type environmentAppIngressBinding struct {
	Name string                     `json:"name"`
	Host string                     `json:"host"`
	TLS  []environmentAppTLSBinding `json:"tls,omitempty"`
}

type environmentAppTLSBinding struct {
	Hosts      []string `json:"hosts"`
	SecretName string   `json:"secret_name"`
}

func environmentAppBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.app-pki", Resource: id, Cause: cause, Next: "verify the existing App API trust, TLS, management and persistent-state references and its original passthrough route; preserve identity state rather than initializing replacement identities"}
}
func validateEnvironmentAppBinding(pod environmentPodBindings) error {
	id := pod.Kind + "/" + pod.NamespaceSuffix + "/" + pod.Name
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "app" || len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "initialize-app-trust-state" {
		return environmentAppBlocker(id, "App API requires its known primary consumer and retry-safe trust initializer")
	}
	for _, name := range []string{"pki-server", "pki-app", "pki-state", "pki-management"} {
		found := false
		for _, value := range certIssuerObjectList(pod.PodFields["volumes"]) {
			volume := deploymentplan.Map(value)
			if volume["name"] == name {
				source, field := "secret", "secretName"
				if name == "pki-app" {
					source, field = "configMap", "name"
				}
				if name == "pki-state" {
					source, field = "persistentVolumeClaim", "claimName"
				}
				if name == "pki-management" && pod.NativeTrustInitialization {
					source, field = "persistentVolumeClaim", "claimName"
				}
				found = deploymentplan.String(deploymentplan.Map(volume[source])[field]) != ""
			}
		}
		if !found {
			return environmentAppBlocker(id, "App API is missing its original "+name+" lifecycle reference")
		}
	}
	if pod.NativeTrustInitialization {
		claim, identityClaim, identityMount := "", "", false
		for _, value := range certIssuerObjectList(pod.PodFields["volumes"]) {
			volume := deploymentplan.Map(value)
			if volume["name"] == "pki-management" {
				claim = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
			}
			if volume["name"] == "service-identity" {
				identityClaim = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
			}
		}
		for _, value := range certIssuerObjectList(pod.Containers[0].Fields["volumeMounts"]) {
			mount := deploymentplan.Map(value)
			identityMount = identityMount || mount["name"] == "service-identity" && mount["mountPath"] == "/var/lib/video-cloud-app-service" && mount["readOnly"] != true
		}
		if claim != "video-cloud-api-app-service-state" || identityClaim != claim || !identityMount || mqttFoundationEnv(pod.Containers[0].Fields, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE") != "/var/lib/video-cloud-app-service/private/client.json" {
			return environmentAppBlocker(id, "native App trust must retain its original owned managed Service client claim and private state path")
		}
	}
	for _, consumer := range append(append([]environmentContainerBindings{}, pod.Containers...), pod.InitContainers...) {
		names := []string{"pki-state", "pki-app"}
		if consumer.Name == "app" {
			names = append(names, "pki-server", "pki-management")
		}
		for _, name := range names {
			found := false
			for _, value := range certIssuerObjectList(consumer.Fields["volumeMounts"]) {
				mount := deploymentplan.Map(value)
				if mount["name"] == name && mount["mountPath"] == "/run/"+name && (name == "pki-state" || mount["readOnly"] == true) {
					found = true
				}
			}
			if !found {
				return environmentAppBlocker(id, "App API has an incompatible "+consumer.Name+"/"+name+" mount")
			}
		}
	}
	return nil
}
func observeEnvironmentAppIngress(b *environmentIdentityBindings, object deploymentplan.Object) error {
	spec := deploymentplan.Map(object["spec"])
	for _, value := range certIssuerObjectList(spec["rules"]) {
		rule := deploymentplan.Map(value)
		for _, value := range certIssuerObjectList(deploymentplan.Map(rule["http"])["paths"]) {
			path := deploymentplan.Map(value)
			service := deploymentplan.Map(deploymentplan.Map(path["backend"])["service"])
			if service["name"] != "video-cloud-api-app-pki" {
				continue
			}
			id := deploymentplan.ID(object)
			annotations := deploymentplan.Map(deploymentplan.Map(object["metadata"])["annotations"])
			host := deploymentplan.String(rule["host"])
			if b.AppIngress != nil || deploymentplan.Map(object["metadata"])["namespace"] != b.Stack+"-video-cloud" || len(certIssuerObjectList(spec["rules"])) != 1 || len(certIssuerObjectList(deploymentplan.Map(rule["http"])["paths"])) != 1 || path["path"] != "/" || host == "" || strings.ContainsAny(host, "/\r\n ") || annotations["nginx.ingress.kubernetes.io/ssl-passthrough"] != "true" {
				return environmentAppBlocker(id, "original App route is missing, ambiguous or does not pass client TLS through")
			}
			if deploymentplan.Map(service["port"])["number"] != float64(8443) && deploymentplan.Map(service["port"])["name"] != "https" {
				return environmentAppBlocker(id, "App route has an unsupported backend port")
			}
			route := &environmentAppIngressBinding{Name: deploymentplan.String(deploymentplan.Map(object["metadata"])["name"]), Host: host}
			for _, value := range certIssuerObjectList(spec["tls"]) {
				tls := deploymentplan.Map(value)
				secret := deploymentplan.String(tls["secretName"])
				if len(tls) != 2 || !secretEnvironmentPattern.MatchString(secret) {
					return environmentAppBlocker(id, "App route TLS reference is invalid")
				}
				hosts := []string{}
				for _, value := range certIssuerObjectList(tls["hosts"]) {
					if value != host {
						return environmentAppBlocker(id, "App route TLS host differs from its original hostname")
					}
					hosts = append(hosts, host)
				}
				if len(hosts) != 1 {
					return environmentAppBlocker(id, "App route TLS hosts are ambiguous")
				}
				route.TLS = append(route.TLS, environmentAppTLSBinding{Hosts: hosts, SecretName: secret})
			}
			b.AppIngress = route
		}
	}
	return nil
}
func environmentAppDependencies(env map[string]string, b environmentIdentityBindings) ([]deploymentplan.Definition, error) {
	if b.AppRouting != nil {
		if err := validateEnvironmentAppRoutingBinding(b); err != nil {
			return nil, err
		}
		return append(environmentAppInternalServiceDependencies(env), environmentAppRoutingDefinition(*b.AppRouting)), nil
	}
	route := b.AppIngress
	ns := lkeNamespaceName(env, "video-cloud")
	if route == nil || !secretEnvironmentPattern.MatchString(route.Name) || route.Host == "" || b.Owners["Ingress/"+ns+"/"+route.Name] == "" {
		return nil, environmentAppBlocker("video-cloud-api-app-pki", "original owned App ingress is unavailable")
	}
	selector := map[string]any{"app.kubernetes.io/name": "video-cloud-api-app-pki"}
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": ns} }
	ingressMeta := meta(route.Name)
	ingressMeta["annotations"] = map[string]any{"nginx.ingress.kubernetes.io/ssl-passthrough": "true", "nginx.ingress.kubernetes.io/backend-protocol": "HTTPS"}
	spec := map[string]any{"ingressClassName": environmentIngressClass(env), "rules": []any{map[string]any{"host": route.Host, "http": map[string]any{"paths": []any{map[string]any{"path": "/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "video-cloud-api-app-pki", "port": map[string]any{"number": float64(8443)}}}}}}}}}
	if len(route.TLS) > 0 {
		tls := []any{}
		for _, value := range route.TLS {
			if !secretEnvironmentPattern.MatchString(value.SecretName) || len(value.Hosts) != 1 || value.Hosts[0] != route.Host {
				return nil, environmentAppBlocker("video-cloud-api-app-pki", "App route TLS reference is invalid")
			}
			tls = append(tls, map[string]any{"hosts": []any{route.Host}, "secretName": value.SecretName})
		}
		spec["tls"] = tls
	}
	return []deploymentplan.Definition{
		{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": meta("video-cloud-api-app-pki"), "spec": map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "https", "port": float64(8443), "targetPort": "https"}}}}, Phase: "configuration"},
		{Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("allow-video-cloud-api-app-pki"), "spec": map[string]any{"podSelector": map[string]any{"matchLabels": selector}, "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": lkeNamespaceName(env, "ingress")}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": float64(8443)}}}}}}, Phase: "configuration"},
		{Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": ingressMeta, "spec": spec}, Phase: "routing"},
	}, nil
}
