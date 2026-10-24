package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCreateOpenBaoIdentity = "/var/lib/openbao-pki/identity"
const environmentCreateMQTTIdentity = "/var/lib/emqx-pki/identity"
const environmentCreateBrokerIdentity = "/run/pki-state/broker-identity/private/client.json"

// These references come from the actual native ceremony. Neither a future
// issuer ID nor a fabricated Root-distribution acknowledgement is a recipe.
func validateEnvironmentCreateTransportIssuers(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer) error {
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, service); err != nil {
		return err
	}
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		r, i := roots[domain], intermediates[domain]
		if r.Environment != p.Environment || r.Domain != domain || r.Kind != "root" || (r.Status != "ready" && r.Status != "active") || !environmentBootstrapOwnerUUID.MatchString(r.ID) || !environmentPreflightDigest.MatchString(r.RootFingerprint) || environmentPreflightBytesSHA256([]byte(r.ChainPEM)) != r.BundleVersion || i.Environment != p.Environment || i.Domain != domain || i.Kind != "intermediate" || i.RootFingerprint != r.RootFingerprint || (i.Status != "ready" && i.Status != "active") || !environmentBootstrapOwnerUUID.MatchString(i.ID) || environmentPreflightBytesSHA256([]byte(i.ChainPEM)) != i.BundleVersion {
			return environmentCreateBootstrapBlocker("transport recipe lacks original registered issuer lineage")
		}
		b, rest := pem.Decode([]byte(r.Certificate))
		if b == nil || b.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return environmentCreateBootstrapBlocker("transport Root certificate is malformed")
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil || !c.IsCA || c.CheckSignatureFrom(c) != nil || environmentPreflightBytesSHA256(c.Raw) != r.RootFingerprint {
			return environmentCreateBootstrapBlocker("transport Root certificate differs from the native registered fingerprint")
		}
	}
	return nil
}

func environmentCreateTransportNames(p environmentCreateBootstrapPlan, domain string) ([]string, error) {
	for _, d := range p.Domains {
		if d.Domain == domain && len(d.ServerDNSNames) > 0 {
			return slices.Clone(d.ServerDNSNames), nil
		}
	}
	return nil, environmentCreateBootstrapBlocker("transport owner has no frozen native DNS policy")
}

func environmentCreateTransportTrust(namespace, name string, r, i environmentCreateRegisteredIssuer) deploymentplan.Definition {
	manifest, _ := json.Marshal([]map[string]string{{"issuer_id": r.ID, "trust_bundle_version": r.BundleVersion}, {"issuer_id": i.ID, "trust_bundle_version": i.BundleVersion}})
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "data": deploymentplan.Object{"root.pem": r.Certificate, "bundles.json": string(manifest)}}}
}

// Client Jobs install private state under its final owner UID and PVC. They
// run before server enrollment and do not emit installation acknowledgements.
func renderEnvironmentCreateTransportClientDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, owner := range []struct {
		role, namespace, claim, root, state string
		uid                                 int
	}{
		{"openbao", p.Stack + "-secrets", "openbao-pki-identity", "/var/lib/openbao-pki", environmentCreateOpenBaoIdentity + "/service-client.json", 100},
		{"emqx-pki", p.Stack + "-video-cloud", "mqtt-pki-host-identity", "/var/lib/emqx-pki", environmentCreateMQTTIdentity + "/service-client.json", 1000},
		{"pkibroker", p.Stack + "-video-cloud", "mqtt-pki-trust", "/run/pki-state", environmentCreateBrokerIdentity, 10001},
	} {
		job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, s, owner.namespace, owner.role, owner.claim)
		if err != nil {
			return nil, err
		}
		environmentCreateTransportJobOwner(job.Object, p.Image, owner.root, owner.state, owner.uid)
		out = append(out, environmentCreateServiceClaimDefinition(cfg, owner.namespace, owner.claim), job)
	}
	out = append(out, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", "mqtt-pki-data"))
	return out, nil
}

func environmentCreateTransportJobOwner(object deploymentplan.Object, image, root, state string, uid int) {
	pod := environmentPKIPod(object)
	group := 1000
	if uid == 10001 && state != environmentCreateBrokerIdentity {
		// API/Factory custody readers and final App owners retain group 10001.
		// Broker state stays with its final MQTT Pod's shared group 1000.
		group = 10001
	}
	pod["securityContext"] = deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(uid), "runAsGroup": float64(group), "fsGroup": float64(group), "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	c["securityContext"] = deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(uid), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}
	for _, e := range certIssuerObjectList(c["env"]) {
		v := deploymentplan.Map(e)
		if v["name"] == "PKI_CLIENT_IDENTITY_STATE" {
			v["value"] = state
		}
	}
	for _, m := range certIssuerObjectList(c["volumeMounts"]) {
		v := deploymentplan.Map(m)
		if v["name"] == "service-state" {
			v["mountPath"] = root
		}
	}
	parent := filepath.Dir(state)
	preparation := "install -d -m 0700 " + shellQuote(parent)
	if uid == 10001 {
		// Actual native Service CRL consumers open their lock beside these
		// cache files. Prepare the original same-owner parent before startup.
		preparation += " " + shellQuote(filepath.Join(root, "private", "crl"))
	}
	pod["initContainers"] = []any{deploymentplan.Object{"name": "prepare-owner-identity", "image": image, "command": []any{"/bin/sh", "-ec", preparation}, "securityContext": c["securityContext"], "volumeMounts": []any{environmentCreateServiceMount("service-state", root, false)}}}
}

func environmentCreateTransportHostSettings(p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony, domain, prefix, name, state, clientState string, root environmentCreateRegisteredIssuer) (map[string]string, error) {
	names, err := environmentCreateTransportNames(p, domain)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(names, name) {
		return nil, environmentCreateBootstrapBlocker("native transport owner name is outside the frozen DNS policy")
	}
	issuer := "certissuer." + p.Stack + "-video-cloud.svc"
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID, prefix + "_HOST_IDENTITY_STATE": state, prefix + "_HOST_NAME": name, prefix + "_HOST_DNS_NAMES": strings.Join(names, ","), prefix + "_HOST_ROOT_SHA256": root.RootFingerprint, prefix + "_HOST_RENEWAL_URL": "https://" + issuer + ":18445", prefix + "_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256": s.Root.RootFingerprint, prefix + "_HOST_RENEWAL_SERVER_PKI_NAME": issuer, prefix + "_HOST_RENEWAL_TLS_CA": environmentCreateServiceTrust + "/root.pem", prefix + "_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL": "10s", prefix + "_SERVICE_CLIENT_IDENTITY_STATE": clientState, prefix + "_SERVICE_CLIENT_ROOT_SHA256": s.Root.RootFingerprint}
	return settings, nil
}

// Server enrollment uses each owner's current registry-verified Service client
// and the native host executable. It leaves private keys on the same PVC.
func renderEnvironmentCreateTransportServerDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateTransportIssuers(cfg, p, runtime, s, roots, intermediates); err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, o := range []struct {
		role, domain, prefix, namespace, claim, root, state, client, name, binary string
		uid                                                                       int
	}{
		{"openbao", "openbao_tls", "OPENBAO_PKI", p.Stack + "-secrets", "openbao-pki-identity", "/var/lib/openbao-pki", environmentCreateOpenBaoIdentity + "/state.json", environmentCreateOpenBaoIdentity + "/service-client.json", "openbao." + p.Stack + "-secrets.svc", "/app/openbaopkihost", 100},
		{"emqx-pki", "mqtt", "EMQX_PKI", p.Stack + "-video-cloud", "mqtt-pki-host-identity", "/var/lib/emqx-pki", environmentCreateMQTTIdentity + "/state.json", environmentCreateMQTTIdentity + "/service-client.json", "mqtt-pki." + p.Stack + "-video-cloud.svc", "/app/emqxpkihost", 1000},
	} {
		settings, err := environmentCreateTransportHostSettings(p, s, o.domain, o.prefix, o.name, o.state, o.client, roots[o.domain])
		if err != nil {
			return nil, err
		}
		settings[o.prefix+"_BOOTSTRAP_REQUEST_ID"] = s.BootstrapSessionID + ":" + o.role + ":server"
		// Only the retained native host acknowledges installation. A
		// short-lived enrollment Job cannot attest final runtime adoption.
		delete(settings, "PKI_BOOTSTRAP_SESSION_ID")
		if o.domain == "openbao_tls" {
			settings["OPENBAO_PKI_RUNTIME_DIR"] = "/run/openbao-pki/private"
			settings["OPENBAO_PKI_PROCESS_PID_FILE"] = "/run/openbao-pki/private/openbao.pid"
		}
		job := environmentCreateServiceEnrollmentJob(p, o.namespace, o.role+"-server", o.claim, s, []any{o.binary, "bootstrap"}, settings, false)
		environmentCreateTransportJobOwner(job.Object, p.Image, o.root, o.state, o.uid)
		// This stage has no bootstrap credential use; first client enrollment
		// already installed the final private client verified by the registry.
		pod := environmentPKIPod(job.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		pod["volumes"] = environmentCreateRemoveNamed(pod["volumes"], "bootstrap")
		c["volumeMounts"] = environmentCreateRemoveNamed(c["volumeMounts"], "bootstrap")
		out = append(out, environmentCreateTransportTrust(o.namespace, "pki-create-"+strings.ReplaceAll(o.domain, "_", "-")+"-trust", roots[o.domain], intermediates[o.domain]), job)
	}
	return out, nil
}

func environmentCreateRemoveNamed(values any, name string) []any {
	var out []any
	for _, v := range certIssuerObjectList(values) {
		if deploymentplan.Map(v)["name"] != name {
			out = append(out, v)
		}
	}
	return out
}

func renderEnvironmentCreateTransportDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, release environmentRelease) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateTransportIssuers(cfg, p, runtime, s, roots, intermediates); err != nil {
		return nil, err
	}
	if release.Environment != p.Environment || release.Stack != p.Stack {
		return nil, environmentCreateBootstrapBlocker("transport release differs from the qualified exact environment")
	}
	v := environmentCreateProviderValues(cfg)
	for _, key := range []string{"LKE_OPENBAO_IMAGE", "LKE_OPENBAO_PKI_IMAGE", "LKE_MQTT_PKI_IMAGE", "LKE_VIDEO_CLOUD_IMAGE"} {
		v[key] = release.Images[key].Reference
		if !immutableEnvironmentImage.MatchString(v[key]) {
			return nil, environmentCreateBootstrapBlocker("managed transport requires every formal immutable provider/native image")
		}
	}
	ns := p.Stack + "-secrets"
	name := "openbao." + ns + ".svc"
	hcl := `ui = false
disable_mlock = true
listener "tcp" {
  address = "[::]:8200"
  cluster_address = "[::]:8201"
  tls_cert_file = "/run/openbao-pki/private/current/chain.pem"
  tls_key_file = "/run/openbao-pki/private/current/key.pem"
  tls_min_version = "tls12"
}
storage "file" {
  path = "/openbao/data"
}
audit "file" "deployment-audit" {
  options {
    file_path = "/openbao/audit/audit.jsonl"
    log_raw = "false"
  }
}
`
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "openbao-file", "namespace": ns}, "data": deploymentplan.Object{"file.hcl": hcl}}
	profile := &environmentOpenBaoProfile{Backend: "file", ConfigKey: "file.hcl", ConfigurationSHA256: deploymentplan.Digest(config["data"]), RuntimeDir: "/run/openbao-pki/private", PIDFile: "/run/openbao-pki/private/openbao.pid"}
	openbao, err := renderEnvironmentOpenBao(v, environmentPodBindings{Kind: "StatefulSet", NamespaceSuffix: "secrets", Name: "openbao", Component: "managed-openbao", OpenBao: profile})
	if err != nil {
		return nil, err
	}
	pod := environmentPKIPod(openbao)
	deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] = "OnRootMismatch"
	pod["volumes"] = []any{environmentCreateServiceVolume("file-config", "configMap", "openbao-file"), environmentCreateServiceVolume("host-state", "persistentVolumeClaim", "openbao-pki-identity"), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust"), environmentCreateServiceVolume("host-trust", "configMap", "pki-create-openbao-tls-trust"), deploymentplan.Object{"name": "host-runtime", "emptyDir": deploymentplan.Object{}}, environmentCreateOpenBaoReviewerVolume()}
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	main["env"] = environmentCreateEnv(map[string]string{"BAO_ADDR": "https://127.0.0.1:8200", "BAO_CACERT": "/run/openbao-pki-root/root.pem", "BAO_TLS_SERVER_NAME": name})
	main["readinessProbe"] = deploymentplan.Object{"exec": deploymentplan.Object{"command": []any{"/bin/sh", "-ec", "test -f /run/openbao-pki/private/ready && bao status -format=json >/dev/null"}}, "periodSeconds": float64(5), "timeoutSeconds": float64(3)}
	main["volumeMounts"] = []any{environmentCreateServiceMount("data", "/openbao/data", false), environmentCreateServiceMount("audit", "/openbao/audit", false), environmentCreateServiceMount("file-config", "/openbao/config", true), environmentCreateServiceMount("host-runtime", "/run/openbao-pki", false), environmentCreateServiceMount("host-trust", "/run/openbao-pki-root", true), environmentCreateServiceMount("openbao-kubernetes-reviewer", "/var/run/secrets/kubernetes.io/serviceaccount", true)}
	settings, err := environmentCreateTransportHostSettings(p, s, "openbao_tls", "OPENBAO_PKI", name, environmentCreateOpenBaoIdentity+"/state.json", environmentCreateOpenBaoIdentity+"/service-client.json", roots["openbao_tls"])
	if err != nil {
		return nil, err
	}
	environmentCreateServiceHTTPSettings(settings, "OPENBAO_PKI_HOST_RENEWAL", "certissuer."+p.Stack+"-video-cloud.svc", s.Root.RootFingerprint, true, p)
	settings["OPENBAO_PKI_RUNTIME_DIR"] = profile.RuntimeDir
	settings["OPENBAO_PKI_PROCESS_PID_FILE"] = profile.PIDFile
	for _, group := range []string{"containers", "initContainers"} {
		for _, value := range certIssuerObjectList(pod[group]) {
			c := deploymentplan.Map(value)
			if c["name"] == "openbao" {
				continue
			}
			c["env"] = append(environmentCreateEnv(settings), environmentCreateServiceDatabaseEnv("pki-verifier-database"))
			c["volumeMounts"] = []any{environmentCreateServiceMount("host-state", "/var/lib/openbao-pki", false), environmentCreateServiceMount("host-runtime", "/run/openbao-pki", false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)}
		}
	}

	for _, group := range []string{"containers", "initContainers"} {
		for _, value := range certIssuerObjectList(pod[group]) {
			c := deploymentplan.Map(value)
			if c["name"] != "openbao" {
				applyCreateTransportServiceCache(pod, c, "service-state", "openbao-pki-identity")
			}
		}
	}
	// Provider process and sidecar retain the same file backend allocations.
	deploymentplan.Map(openbao["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	mqtt, err := renderManagedEnvironmentComponent(v, environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "mqtt-pki", Component: "managed-mqtt", Containers: []environmentContainerBindings{{Name: "mqtt"}, {Name: "pkibroker"}}, InitContainers: []environmentContainerBindings{{Name: "prepare-pki-state"}, {Name: "prepare-mqtt-host-state"}}})
	if err != nil {
		return nil, err
	}
	mp := environmentPKIPod(mqtt)
	mc := deploymentplan.Map(certIssuerObjectList(mp["containers"])[0])
	broker := deploymentplan.Map(certIssuerObjectList(mp["containers"])[1])
	mc["image"] = v["LKE_MQTT_PKI_IMAGE"]
	broker["image"] = p.Image
	mqttName := "mqtt-pki." + p.Stack + "-video-cloud.svc"
	mqttSettings, err := environmentCreateTransportHostSettings(p, s, "mqtt", "EMQX_PKI", mqttName, environmentCreateMQTTIdentity+"/state.json", environmentCreateMQTTIdentity+"/service-client.json", roots["mqtt"])
	if err != nil {
		return nil, err
	}
	if len(p.ProductIdentities) > 0 {
		mqttSettings["EMQX_LISTENERS__WS__TESTLAB__ENABLE"] = "false"
		if lkeTestLabEnabled(v) {
			mqttSettings["EMQX_LISTENERS__WS__TESTLAB__ENABLE"] = "true"
		}
		mqttSettings["EMQX_LISTENERS__WS__TESTLAB__ENABLE_AUTHN"] = "true"
	}
	environmentCreateServiceHTTPSettings(mqttSettings, "EMQX_PKI_HOST_RENEWAL", "certissuer."+p.Stack+"-video-cloud.svc", s.Root.RootFingerprint, true, p)
	mc["env"] = mergeEnvironmentNamedFields(mc["env"], environmentCreateEnv(mqttSettings))
	mc["volumeMounts"] = mergeEnvironmentNamedFields(mc["volumeMounts"], []any{environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
	controller := "pki-controller." + p.Stack + "-video-cloud.svc"
	brokerSettings := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID, "PKI_BROKER_IDENTITY_STATE": environmentCreateBrokerIdentity, "PKI_BROKER_ROOT_SHA256": s.Root.RootFingerprint, "PKI_BROKER_PKI_CONTROLLER_URL": "https://" + controller + ":18446", "PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256": s.Root.RootFingerprint, "PKI_BROKER_IDENTITY_SERVER_PKI_NAME": controller, "PKI_BROKER_IDENTITY_TLS_CA": environmentCreateServiceTrust + "/root.pem", "PKI_BROKER_IDENTITY_RENEWAL_URL": "https://certissuer." + p.Stack + "-video-cloud.svc:18445", "PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256": s.Root.RootFingerprint, "PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_NAME": "certissuer." + p.Stack + "-video-cloud.svc", "PKI_BROKER_IDENTITY_RENEWAL_TLS_CA": environmentCreateServiceTrust + "/root.pem"}
	for _, endpoint := range []struct{ prefix, name string }{{"PKI_BROKER_IDENTITY", controller}, {"PKI_BROKER_IDENTITY_RENEWAL", "certissuer." + p.Stack + "-video-cloud.svc"}} {
		environmentCreateServiceHTTPSettings(brokerSettings, endpoint.prefix, endpoint.name, s.Root.RootFingerprint, true, p)
	}
	broker["env"] = mergeEnvironmentNamedFields(broker["env"], environmentCreateEnv(brokerSettings))
	broker["volumeMounts"] = mergeEnvironmentNamedFields(broker["volumeMounts"], []any{environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)})
	device, app := roots["device"], roots["app"]
	if device.ID != "" && app.ID != "" {
		broker["env"] = mergeEnvironmentNamedFields(broker["env"], environmentCreateEnv(map[string]string{
			"PKI_BROKER_REQUIRE_CRLS": "true", "PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_DEVICE_ROOT_ID": device.ID, "PKI_BROKER_DEVICE_ROOT_STATE": "/run/pki-state/device-root.json", "PKI_BROKER_DEVICE_ROOTS": "/run/pki-device/roots.pem", "PKI_BROKER_DEVICE_CRL_MANIFEST": "/run/pki-device/broker-crls.json", "PKI_BROKER_APP_PKI_ENABLED": "true", "PKI_BROKER_APP_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_APP_ROOT_ID": app.ID, "PKI_BROKER_APP_ROOT_STATE": "/run/pki-state/app-root.json", "PKI_BROKER_APP_ROOTS": "/run/pki-app/roots.pem", "PKI_BROKER_APP_CRL_MANIFEST": "/run/pki-app/broker-crls.json",
		}))
		broker["volumeMounts"] = mergeEnvironmentNamedFields(broker["volumeMounts"], []any{environmentCreateServiceMount("pki-device", "/run/pki-device", true), environmentCreateServiceMount("pki-app", "/run/pki-app", true)})
		mp["volumes"] = mergeEnvironmentNamedFields(mp["volumes"], []any{environmentCreateServiceVolume("pki-device", "configMap", "pki-create-device-trust"), environmentCreateServiceVolume("pki-app", "configMap", "pki-create-app-trust")})
	}
	mp["volumes"] = mergeEnvironmentNamedFields(mp["volumes"], []any{environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")})
	applyCreateTransportServiceCache(mp, mc, "mqtt-service-state", "mqtt-pki-host-identity")
	applyCreateTransportServiceCache(mp, broker, "service-state", "mqtt-pki-trust")
	deploymentplan.Map(mqtt["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	dependencies, err := renderEnvironmentCreateManagedMQTTDependencies(ctx, cfg, release, runtime)
	if err != nil {
		return nil, err
	}
	// Transport startup retains the installed Device/App trust consumer until
	// final applications expose their business callback and normal worker role.
	broker["command"] = []any{"/app/pkibroker", "--deployment-bootstrap-trust"}
	removeCreateBootstrapWorkerSecretRefs(broker)
	broker["env"] = mergeEnvironmentNamedFields(broker["env"], environmentCreateEnv(map[string]string{"PKI_BROKER_BOOTSTRAP_TRUST_ADDR": "0.0.0.0:19310", "PKI_BROKER_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}))
	broker["ports"] = []any{deploymentplan.Object{"name": "bootstrap", "containerPort": float64(19310)}}
	broker["readinessProbe"] = deploymentplan.Object{"httpGet": deploymentplan.Object{"path": "/readyz", "port": "bootstrap"}, "periodSeconds": float64(3)}
	servicePolicy, err := renderCreateTransportServicePolicyDefinitions(ctx, cfg, p, runtime, s)
	if err != nil {
		return nil, err
	}
	dependencies = append(dependencies, servicePolicy...)
	controllerID := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
	mqttDependencies := []string{controllerID, "ConfigMap/" + p.Stack + "-video-cloud/mqtt-pki-config", "Secret/" + p.Stack + "-video-cloud/mqtt-pki-runtime"}
	dependencies = append(dependencies, deploymentplan.Definition{Phase: "configuration", Object: config}, deploymentplan.Definition{Phase: "workload", Object: openbao, Dependencies: []string{controllerID, "ConfigMap/" + ns + "/pki-create-service-trust"}}, deploymentplan.Definition{Phase: "workload", Object: mqtt, Dependencies: mqttDependencies})
	return dependencies, nil
}

func environmentCreateOpenBaoReviewerVolume() any {
	return deploymentplan.Object{"name": "openbao-kubernetes-reviewer", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(900), "path": "token"}}, deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}}}}}
}

// Called only after the shared OnDelete controller has observed a replacement
// Pod on its desired revision. It uses the original initialized share once;
// it never invokes init, changes custody, or uses the Pod's sealed state as an
// installation acknowledgement.
func unsealEnvironmentCreateOpenBaoRestart(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, pod deploymentplan.Object) error {
	if runtime.Execution == nil || runtime.Execution.Store.Root != store.Root || store.Environment != cfg.Environment || runtime.Backend.Store.Environment != cfg.Environment {
		return environmentCreateBootstrapBlocker("managed OpenBao restart lacks its original execution owner")
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	controller, err := runtime.Backend.Read(ctx, "StatefulSet/"+ns+"/openbao")
	if err != nil || controller == nil || validateOpenBaoPodOwner(controller, pod) != nil || deploymentplan.Map(pod["metadata"])["namespace"] != ns || deploymentplan.Map(pod["metadata"])["name"] != "openbao-0" {
		return environmentCreateBootstrapBlocker("managed OpenBao replacement Pod is not owned by the exact original StatefulSet")
	}
	uid := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	revision := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"])
	if uid == "" || revision == "" || revision != deploymentplan.Map(controller["status"])["updateRevision"] {
		return environmentCreateBootstrapBlocker("managed OpenBao replacement revision cannot be verified")
	}
	initPath, err := store.safePath("openbao/init.json")
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(initPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original OpenBao initialization evidence is unavailable")
	}
	var initialized struct {
		Keys  []string `json:"unseal_keys_b64"`
		Token string   `json:"root_token"`
	}
	if json.Unmarshal(raw, &initialized) != nil || len(initialized.Keys) != 1 || initialized.Token == "" {
		return environmentCreateBootstrapBlocker("original OpenBao initialization evidence is malformed")
	}
	var original deploymentplan.Object
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key == "openbao-initialize" && entry.State == "completed" {
			if json.Unmarshal(entry.Receipt, &original) != nil {
				return environmentCreateBootstrapBlocker("original OpenBao initialization receipt cannot be read")
			}
		}
	}
	if original == nil || original["init_sha256"] != environmentPreflightBytesSHA256(raw) || original["statefulset_uid"] != deploymentplan.Map(controller["metadata"])["uid"] || original["root_token_ref"] != "openbao/root-token" || original["unseal_ref"] != "openbao/unseal-key" {
		return environmentCreateBootstrapBlocker("managed OpenBao replacement differs from the completed original initialization owner and evidence")
	}
	keyPath, err := store.safePath("openbao/unseal-key")
	if err != nil {
		return err
	}
	key, err := readBoundedEnvironmentFile(keyPath, true)
	if err != nil || strings.TrimSpace(string(key)) != initialized.Keys[0] {
		return environmentCreateBootstrapBlocker("retained original OpenBao share differs from initialization evidence")
	}
	decoded, err := base64.StdEncoding.DecodeString(initialized.Keys[0])
	if err != nil || len(decoded) < 16 {
		return environmentCreateBootstrapBlocker("retained original OpenBao share is invalid")
	}
	tokenPath, err := store.safePath("openbao/root-token")
	if err != nil {
		return err
	}
	token, err := readBoundedEnvironmentFile(tokenPath, true)
	if err != nil || strings.TrimSpace(string(token)) != initialized.Token {
		return environmentCreateBootstrapBlocker("retained original OpenBao root credential differs")
	}
	exec := func(input []byte, script string) ([]byte, error) {
		current, e := runtime.Backend.Read(ctx, "Pod/"+ns+"/openbao-0")
		if e != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != uid || validateOpenBaoPodOwner(controller, current) != nil {
			return nil, environmentCreateBootstrapBlocker("managed OpenBao Pod changed before original-share use")
		}
		return runtime.Backend.command(ctx, input, "--namespace", ns, "exec", "-i", "openbao-0", "--container", "openbao", "--", "env", "BAO_ADDR=https://127.0.0.1:8200", "BAO_CACERT=/run/openbao-pki-root/root.pem", "BAO_TLS_SERVER_NAME=openbao."+ns+".svc", "/bin/sh", "-ec", script)
	}
	var state struct {
		Initialized *bool `json:"initialized"`
		Sealed      *bool `json:"sealed"`
	}
	for {
		status, e := exec(nil, "set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]")
		if e == nil && json.Unmarshal(status, &state) == nil && state.Initialized != nil && state.Sealed != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if !*state.Initialized {
		return environmentCreateBootstrapBlocker("managed OpenBao replacement lost original initialized storage; reinitialization is forbidden")
	}
	if !*state.Sealed {
		return nil
	}
	entryKey := "openbao-managed-unseal-" + uid
	intent, _ := json.Marshal(deploymentplan.Object{"pod_uid": uid, "controller_uid": deploymentplan.Map(controller["metadata"])["uid"], "revision": revision, "original_init_sha256": environmentPreflightBytesSHA256(raw), "unseal_sha256": environmentPreflightBytesSHA256([]byte(initialized.Keys[0]))})
	if err = runtime.Execution.BeginEntry(entryKey, "openbao-unseal", intent, time.Now().UTC()); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"key": initialized.Keys[0]})
	if _, err = exec(body, "bao write -format=json sys/unseal -"); err != nil {
		return environmentCreateBootstrapBlocker("managed OpenBao original-share unseal outcome is uncertain; retain journal and inspect exact Pod")
	}
	status, err := exec(nil, "bao status -format=json")
	if err != nil || json.Unmarshal(status, &state) != nil || state.Initialized == nil || state.Sealed == nil || !*state.Initialized || *state.Sealed {
		return environmentCreateBootstrapBlocker("managed OpenBao replacement remains sealed or cannot be verified")
	}
	receipt, _ := json.Marshal(deploymentplan.Object{"pod_uid": uid, "initialized": true, "sealed": false, "revision": revision})
	if err = runtime.Execution.RecordReceipt(entryKey, receipt); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(entryKey, time.Now().UTC())
}
