package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCertIssuerPublicProfile struct {
	Schema, Environment, Stack, Name, RootSHA256                            string
	StatePath, NewStatePath, ClientStatePath                                string
	OriginalDNSNames, DNSNames                                              []string
	OriginalStateSHA256, OriginalCertificateSHA256, OriginalPublicKeySHA256 string
	RequestID, HelperImage                                                  string
}
type environmentCertIssuerPublicProof struct {
	Schema, IntentSHA256, RequestID, StatePath, OriginalCertificateSHA256 string
	CertificateSHA256, PublicKeySHA256, RootSHA256, StateSHA256           string
	DNSNames                                                              []string
}

func certIssuerPublicBlock(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.certissuer-public", Resource: id, Cause: cause, Next: "repair the declared public CertIssuer Service, same-key serving profile and exact ingress path; preserve original CA, native identities and accepted receipts, then resume the recorded execution"}
}
func certIssuerPublicObject(definitions []deploymentplan.Definition, id string) (deploymentplan.Object, error) {
	var result deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == id && !d.Retire {
			if result != nil {
				return nil, certIssuerPublicBlock(id, "public transport resource is duplicated")
			}
			result = d.Object
		}
	}
	if result == nil {
		return nil, certIssuerPublicBlock(id, "public transport resource is missing")
	}
	return result, nil
}

// Resolve the actual rendered Service and named container listener together.
// An adopted native registry-only issuer uses18445; a static recipe uses9443.
func environmentCertIssuerPublicPort(definitions []deploymentplan.Definition, stack string) (int, error) {
	id := "Deployment/" + stack + "-video-cloud/certissuer"
	deployment, err := certIssuerPublicObject(definitions, id)
	if err != nil {
		return 0, err
	}
	c := managedNamedField(environmentPKIPod(deployment)["containers"], "certissuer")
	_, raw, err := net.SplitHostPort(mqttFoundationEnv(c, "CERT_ISSUER_LISTEN_ADDR"))
	port, parseErr := strconv.Atoi(raw)
	if err != nil || parseErr != nil || (port != 9443 && port != 18445) {
		return 0, certIssuerPublicBlock(id, "public TLS listener is not an admitted issuer profile")
	}
	service, err := certIssuerPublicObject(definitions, "Service/"+stack+"-video-cloud/certissuer")
	if err != nil {
		return 0, err
	}
	ports := certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"])
	if len(ports) != 1 {
		return 0, certIssuerPublicBlock(id, "issuer Service has ambiguous public TLS ports")
	}
	p := deploymentplan.Map(ports[0])
	if p["protocol"] != nil && p["protocol"] != "TCP" || p["port"] != float64(port) {
		return 0, certIssuerPublicBlock(id, "issuer Service and TLS listener ports differ")
	}
	target := p["targetPort"]
	if target == nil {
		target = p["port"]
	}
	if name, ok := target.(string); ok {
		found := 0
		for _, v := range certIssuerObjectList(c["ports"]) {
			q := deploymentplan.Map(v)
			if q["name"] == name && q["containerPort"] == float64(port) {
				found++
			}
		}
		if found != 1 {
			return 0, certIssuerPublicBlock(id, "issuer Service named target does not select its TLS listener")
		}
	} else if target != float64(port) {
		return 0, certIssuerPublicBlock(id, "issuer Service target port differs from its TLS listener")
	}
	return port, nil
}
func environmentCertIssuerPublicPolicy(env map[string]string, port int) deploymentplan.Definition {
	stack := env["CLOUD_STACK_NAME"]
	return environmentNativeIngressPolicy(stack+"-video-cloud", "allow-certissuer-public-mtls", "certissuer", float64(port), []deploymentplan.Object{environmentNativeNetworkPeer(lkeIngressNamespace(env), []string{"ingress-nginx"})})
}

// A new render follows its actual Service port, while retained histories keep
// their literal recipes. This does not fabricate a serving certificate/profile.
func bindEnvironmentCertIssuerPublicTransport(env map[string]string, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	if host == "" {
		return definitions, nil
	}
	stack := env["CLOUD_STACK_NAME"]
	port, err := environmentCertIssuerPublicPort(definitions, stack)
	if err != nil {
		return nil, err
	}
	return bindEnvironmentCertIssuerPublicRoute(env, definitions, port)
}

func bindEnvironmentCertIssuerPublicRoute(env map[string]string, definitions []deploymentplan.Definition, port int) ([]deploymentplan.Definition, error) {
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	id := "Ingress/" + env["CLOUD_STACK_NAME"] + "-video-cloud/certissuer-public-mtls"
	ingress, err := certIssuerPublicObject(definitions, id)
	if err != nil {
		return nil, err
	}
	if err = environmentCertIssuerPublicIngress(ingress, host, port, true); err != nil {
		return nil, err
	}
	policy := environmentCertIssuerPublicPolicy(env, port)
	found := false
	for i, d := range definitions {
		if deploymentplan.ID(d.Object) == deploymentplan.ID(policy.Object) {
			if found {
				return nil, certIssuerPublicBlock(id, "public ingress policy is duplicated")
			}
			definitions[i] = policy
			found = true
		}
	}
	if !found {
		definitions = append(definitions, policy)
	}
	return definitions, nil
}
func environmentCertIssuerPublicIngress(o deploymentplan.Object, host string, port int, bind bool) error {
	id := deploymentplan.ID(o)
	meta, spec := deploymentplan.Map(o["metadata"]), deploymentplan.Map(o["spec"])
	annotations := deploymentplan.Map(meta["annotations"])
	if annotations["nginx.ingress.kubernetes.io/ssl-passthrough"] != "true" || annotations["nginx.ingress.kubernetes.io/backend-protocol"] != "HTTPS" {
		return certIssuerPublicBlock(id, "public route must preserve direct client TLS")
	}
	rules := certIssuerObjectList(spec["rules"])
	if len(rules) != 1 || deploymentplan.Map(rules[0])["host"] != host {
		return certIssuerPublicBlock(id, "public route hostname differs from selected intent")
	}
	paths := certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(rules[0])["http"])["paths"])
	if len(paths) != 1 {
		return certIssuerPublicBlock(id, "public route path is ambiguous")
	}
	p := deploymentplan.Map(paths[0])
	s := deploymentplan.Map(deploymentplan.Map(p["backend"])["service"])
	if p["path"] != "/" || p["pathType"] != "Prefix" || s["name"] != "certissuer" {
		return certIssuerPublicBlock(id, "public route does not select the original issuer Service")
	}
	if bind {
		s["port"] = deploymentplan.Object{"number": float64(port)}
	} else if deploymentplan.Map(s["port"])["number"] != float64(port) {
		return certIssuerPublicBlock(id, "public route backend port differs from the actual issuer Service")
	}
	return nil
}

// Compiler admission must catch the complete public edge before rollout.
func validateEnvironmentCertIssuerPublicTransport(env map[string]string, definitions []deploymentplan.Definition) error {
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	if host == "" {
		return nil
	}
	stack := env["CLOUD_STACK_NAME"]
	port, err := environmentCertIssuerPublicPort(definitions, stack)
	if err != nil {
		return err
	}
	id := "Ingress/" + stack + "-video-cloud/certissuer-public-mtls"
	ingress, err := certIssuerPublicObject(definitions, id)
	if err != nil {
		return err
	}
	if err = environmentCertIssuerPublicIngress(ingress, host, port, false); err != nil {
		return err
	}
	deployment, _ := certIssuerPublicObject(definitions, "Deployment/"+stack+"-video-cloud/certissuer")
	c := managedNamedField(environmentPKIPod(deployment)["containers"], "certissuer")
	if mqttFoundationEnv(c, "CERT_ISSUER_HOST_IDENTITY_STATE") != "" && !slices.Contains(strings.Split(mqttFoundationEnv(c, "CERT_ISSUER_HOST_DNS_NAMES"), ","), host) {
		return certIssuerPublicBlock(deploymentplan.ID(deployment), "managed serving profile omits the required public DNS name")
	}
	wanted := environmentCertIssuerPublicPolicy(env, port)
	policy, err := certIssuerPublicObject(definitions, deploymentplan.ID(wanted.Object))
	if err != nil {
		return err
	}
	if deploymentplan.Digest(deploymentplan.Map(policy["spec"])) != deploymentplan.Digest(deploymentplan.Map(wanted.Object["spec"])) {
		return certIssuerPublicBlock(deploymentplan.ID(policy), "public ingress policy differs from exact controller namespace, Pod and TLS port")
	}
	return nil
}
func (p environmentCertIssuerPublicProfile) validate(cfg deploymentConfig, original []deploymentplan.Definition) error {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	env := deploymentEndpointValues(cfg)
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	if p.Schema != "rtk-cloud.certissuer-public-profile/v1" || cfg.Environment != "dev" || p.Environment != cfg.Environment || p.Stack != stack || p.Name != "certissuer."+stack+"-video-cloud.svc" || p.StatePath != "/var/lib/pki-service/private/server.json" || p.ClientStatePath != "/var/lib/pki-service/private/client.json" || !slices.Equal(p.OriginalDNSNames, []string{p.Name}) || p.RequestID == "" || len(p.RequestID) > 128 || strings.TrimSpace(p.RequestID) != p.RequestID || !immutableEnvironmentImage.MatchString(p.HelperImage) {
		return certIssuerPublicBlock("", "exact original dev same-key public profile required")
	}
	for _, pin := range []string{p.RootSHA256, p.OriginalStateSHA256, p.OriginalCertificateSHA256, p.OriginalPublicKeySHA256} {
		if !environmentPreflightDigest.MatchString(pin) {
			return certIssuerPublicBlock("", "original public identity pins are incomplete")
		}
	}
	names := []string{p.Name, host}
	slices.Sort(names)
	if host == "" || !slices.Equal(p.DNSNames, names) || p.NewStatePath != filepath.Join(filepath.Dir(p.StatePath), "public-"+environmentPreflightBytesSHA256([]byte(p.RequestID))[:12], "server.json") {
		return certIssuerPublicBlock("", "public profile changes more than its required DNS name")
	}
	o, err := certIssuerPublicObject(original, "Deployment/"+stack+"-video-cloud/certissuer")
	if err != nil {
		return err
	}
	c := managedNamedField(environmentPKIPod(o)["containers"], "certissuer")
	for key, want := range map[string]string{"CERT_ISSUER_HOST_IDENTITY_STATE": p.StatePath, "CERT_ISSUER_HOST_NAME": p.Name, "CERT_ISSUER_HOST_DNS_NAMES": strings.Join(p.OriginalDNSNames, ","), "CERT_ISSUER_HOST_ROOT_SHA256": p.RootSHA256, "CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE": p.ClientStatePath, "CERT_ISSUER_REGISTRY_ONLY_ENABLED": "true"} {
		if mqttFoundationEnv(c, key) != want {
			return certIssuerPublicBlock(deploymentplan.ID(o), "original native public profile differs: "+key)
		}
	}
	port, err := environmentCertIssuerPublicPort(original, stack)
	if err != nil {
		return err
	}
	if port != 18445 {
		return certIssuerPublicBlock(deploymentplan.ID(o), "public correction requires original native listener18445")
	}
	return nil
}
func environmentPostCleanupCertIssuerPublicDefinitions(cfg deploymentConfig, original []deploymentplan.Definition, p environmentCertIssuerPublicProfile) ([]deploymentplan.Definition, error) {
	if err := p.validate(cfg, original); err != nil {
		return nil, err
	}
	env := deploymentEndpointValues(cfg)
	stack := p.Stack
	o, _ := certIssuerPublicObject(original, "Deployment/"+stack+"-video-cloud/certissuer")
	deployment := deploymentplan.Clone(o)
	c := managedNamedField(environmentPKIPod(deployment)["containers"], "certissuer")
	for _, v := range certIssuerObjectList(c["env"]) {
		e := deploymentplan.Map(v)
		switch e["name"] {
		case "CERT_ISSUER_HOST_IDENTITY_STATE":
			e["value"] = p.NewStatePath
		case "CERT_ISSUER_HOST_DNS_NAMES":
			e["value"] = strings.Join(p.DNSNames, ",")
		}
	}
	o, err := certIssuerPublicObject(original, "Ingress/"+stack+"-video-cloud/certissuer-public-mtls")
	if err != nil {
		return nil, err
	}
	ingress := deploymentplan.Clone(o)
	if err = environmentCertIssuerPublicIngress(ingress, lkeResolveCertIssuerTLSConfig(env).PublicHost, 18445, true); err != nil {
		return nil, err
	}
	return []deploymentplan.Definition{{Object: deployment, Phase: "workload"}, {Object: ingress, Phase: "routing"}, environmentCertIssuerPublicPolicy(env, 18445)}, nil
}
func environmentCertIssuerPublicPreparationName(p environmentCertIssuerPublicProfile) string {
	return "pki-certissuer-public-" + deploymentplan.Digest(p)[:12]
}
func environmentPostCleanupCertIssuerPreparationDefinitions(cfg deploymentConfig, original []deploymentplan.Definition, p environmentCertIssuerPublicProfile) ([]deploymentplan.Definition, error) {
	if err := p.validate(cfg, original); err != nil {
		return nil, err
	}
	name, ns := environmentCertIssuerPublicPreparationName(p), p.Stack+"-video-cloud"
	o, _ := certIssuerPublicObject(original, "Deployment/"+ns+"/certissuer")
	pod := environmentPKIPod(o)
	c := managedNamedField(pod["containers"], "certissuer")
	var sourceVolume deploymentplan.Object
	for _, v := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(v)
		if deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] == "certissuer-service-state" {
			if sourceVolume != nil {
				return nil, certIssuerPublicBlock(deploymentplan.ID(o), "original issuer state claim is ambiguous")
			}
			sourceVolume = deploymentplan.Clone(v)
		}
	}
	if sourceVolume == nil {
		return nil, certIssuerPublicBlock(deploymentplan.ID(o), "original issuer state claim is missing")
	}
	volumeName := deploymentplan.String(sourceVolume["name"])
	found := 0
	for _, v := range certIssuerObjectList(c["volumeMounts"]) {
		m := deploymentplan.Map(v)
		if m["name"] == volumeName && m["mountPath"] == "/var/lib/pki-service" && m["readOnly"] != true && m["subPath"] == nil {
			found++
		}
	}
	if found != 1 {
		return nil, certIssuerPublicBlock(deploymentplan.ID(o), "original issuer state mount differs")
	}
	db := managedNamedField(c["env"], "CERT_ISSUER_DB_DSN")
	if db == nil || db["valueFrom"] == nil {
		return nil, certIssuerPublicBlock(deploymentplan.ID(o), "original issuer database role reference missing")
	}
	intent, _ := json.Marshal(p)
	cm := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name + "-intent", "namespace": ns}, "immutable": true, "data": deploymentplan.Object{"intent.json": string(intent)}}, Phase: "configuration"}
	policy := environmentNativeIngressPolicy(ns, name, "certissuer", 18445, []deploymentplan.Object{environmentNativeNetworkPeer(ns, []string{name})})
	labels := deploymentplan.Object{"app.kubernetes.io/name": name, "rtk.realtek.com/stack": p.Stack}
	container := deploymentplan.Object{"name": "prepare", "image": p.HelperImage, "command": []any{"/app/serviceidentity-bootstrap", "public-server-profile", "prepare", "/run/public-profile/intent.json"}, "env": []any{deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Clone(deploymentplan.Map(db["valueFrom"]))}}, "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": []any{environmentCreateServiceMount(volumeName, "/var/lib/pki-service", false), environmentCreateServiceMount("public-profile-intent", "/run/public-profile", true)}}
	jobPod := deploymentplan.Object{"restartPolicy": "OnFailure", "automountServiceAccountToken": false, "serviceAccountName": pod["serviceAccountName"], "imagePullSecrets": pod["imagePullSecrets"], "securityContext": deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}, "containers": []any{container}, "volumes": []any{sourceVolume, environmentCreateServiceVolume("public-profile-intent", "configMap", name+"-intent")}, "affinity": deploymentplan.Object{"podAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{deploymentplan.Object{"topologyKey": "kubernetes.io/hostname", "namespaces": []any{ns}, "labelSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "certissuer"}}}}}}}
	job := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "spec": deploymentplan.Object{"backoffLimit": float64(2), "activeDeadlineSeconds": float64(180), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": jobPod}}}, Phase: "configuration", Dependencies: []string{deploymentplan.ID(cm.Object), deploymentplan.ID(policy.Object)}}
	return []deploymentplan.Definition{cm, policy, job}, nil
}
func qualifyEnvironmentPostCleanupCertIssuerProfile(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, original []deploymentplan.Definition, image string) (environmentCertIssuerPublicProfile, error) {
	var p environmentCertIssuerPublicProfile
	stack := cfg.Values["CLOUD_STACK_NAME"]
	o, err := certIssuerPublicObject(original, "Deployment/"+stack+"-video-cloud/certissuer")
	if err != nil {
		return p, err
	}
	c := managedNamedField(environmentPKIPod(o)["containers"], "certissuer")
	p = environmentCertIssuerPublicProfile{Schema: "rtk-cloud.certissuer-public-profile/v1", Environment: cfg.Environment, Stack: stack, Name: mqttFoundationEnv(c, "CERT_ISSUER_HOST_NAME"), RootSHA256: mqttFoundationEnv(c, "CERT_ISSUER_HOST_ROOT_SHA256"), StatePath: mqttFoundationEnv(c, "CERT_ISSUER_HOST_IDENTITY_STATE"), ClientStatePath: mqttFoundationEnv(c, "CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE"), OriginalDNSNames: strings.Split(mqttFoundationEnv(c, "CERT_ISSUER_HOST_DNS_NAMES"), ","), HelperImage: image}
	host := lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(cfg)).PublicHost
	p.DNSNames = append(slices.Clone(p.OriginalDNSNames), host)
	slices.Sort(p.DNSNames)
	raw, err := backend.command(ctx, nil, "-n", stack+"-video-cloud", "exec", "deployment/certissuer", "-c", "certissuer", "--", "/app/serviceidentity-bootstrap", "inspect-server", p.StatePath, "service", p.Name, p.RootSHA256, strings.Join(p.OriginalDNSNames, ",")) // Native inspection emits lower-case public metadata; never read private state.
	var observed struct {
		Subject         string `json:"subject"`
		Fingerprint     string `json:"fingerprint"`
		PublicKeySHA256 string `json:"public_key_sha256"`
		StateSHA256     string `json:"state_sha256"`
		RootSHA256      string `json:"root_sha256"`
		Pending         bool   `json:"pending"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &observed) != nil || observed.Pending || observed.Subject != p.Name || observed.RootSHA256 != p.RootSHA256 {
		return p, certIssuerPublicBlock(deploymentplan.ID(o), "original native serving identity inspection unavailable")
	}
	p.OriginalStateSHA256, p.OriginalCertificateSHA256, p.OriginalPublicKeySHA256 = observed.StateSHA256, observed.Fingerprint, observed.PublicKeySHA256
	p.RequestID = "certissuer-public-" + deploymentplan.Digest([]string{p.OriginalStateSHA256, host})[:32]
	p.NewStatePath = filepath.Join(filepath.Dir(p.StatePath), "public-"+environmentPreflightBytesSHA256([]byte(p.RequestID))[:12], "server.json")
	if err = p.validate(cfg, original); err != nil {
		return p, err
	}
	return p, nil
}

// Select a genuinely successful Pod of the current exact ordinary Job. A
// failed prior Pod cannot supply its stale log when the same claim recovered.
func environmentCertIssuerPublicSuccessfulPod(p environmentCertIssuerPublicProfile, job deploymentplan.Object, pods []deploymentplan.Object) (string, error) {
	name, ns := environmentCertIssuerPublicPreparationName(p), p.Stack+"-video-cloud"
	meta := deploymentplan.Map(job["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if job["kind"] != "Job" || meta["name"] != name || meta["namespace"] != ns || uid == "" || meta["deletionTimestamp"] != nil {
		return "", certIssuerPublicBlock("Job/"+ns+"/"+name, "accepted same-key helper Job unavailable")
	}
	completed := false
	for _, v := range certIssuerObjectList(deploymentplan.Map(job["status"])["conditions"]) {
		c := deploymentplan.Map(v)
		completed = completed || c["type"] == "Complete" && c["status"] == "True"
	}
	selected := ""
	for _, pod := range pods {
		m := deploymentplan.Map(pod["metadata"])
		if m["namespace"] != ns || m["deletionTimestamp"] != nil || deploymentplan.Map(pod["status"])["phase"] != "Succeeded" {
			continue
		}
		owned := false
		for _, v := range certIssuerObjectList(m["ownerReferences"]) {
			r := deploymentplan.Map(v)
			owned = owned || r["kind"] == "Job" && r["name"] == name && r["uid"] == uid && r["controller"] == true
		}
		c := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "prepare")
		state := managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "prepare")
		if !owned {
			continue
		}
		if c["image"] != p.HelperImage || deploymentplan.Digest(c["command"]) != deploymentplan.Digest([]any{"/app/serviceidentity-bootstrap", "public-server-profile", "prepare", "/run/public-profile/intent.json"}) || deploymentplan.Map(deploymentplan.Map(state["state"])["terminated"])["exitCode"] != float64(0) || selected != "" {
			return "", certIssuerPublicBlock("Job/"+ns+"/"+name, "successful same-key helper Pod differs")
		}
		selected = deploymentplan.String(m["name"])
	}
	if !completed || selected == "" {
		return "", certIssuerPublicBlock("Job/"+ns+"/"+name, "same-key helper completion unavailable")
	}
	return selected, nil
}
func verifyEnvironmentCertIssuerPublicProfile(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, p environmentCertIssuerPublicProfile) (environmentCertIssuerPublicProof, error) {
	var proof environmentCertIssuerPublicProof
	name, ns := environmentCertIssuerPublicPreparationName(p), p.Stack+"-video-cloud"
	raw, err := backend.command(ctx, nil, "-n", ns, "get", "job", name, "-o", "json")
	var job deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &job) != nil {
		return proof, certIssuerPublicBlock("Job/"+ns+"/"+name, "same-key helper Job read unavailable")
	}
	raw, err = backend.command(ctx, nil, "-n", ns, "get", "pods", "-l", "batch.kubernetes.io/job-name="+name, "-o", "json")
	var inventory struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &inventory) != nil {
		return proof, certIssuerPublicBlock("Job/"+ns+"/"+name, "same-key helper Pods read unavailable")
	}
	selected, err := environmentCertIssuerPublicSuccessfulPod(p, job, inventory.Items)
	if err != nil {
		return proof, err
	}
	raw, err = backend.command(ctx, nil, "-n", ns, "logs", selected, "-c", "prepare")
	if err != nil || decodeEnvironmentJSON(raw, &proof) != nil {
		return proof, certIssuerPublicBlock("Job/"+ns+"/"+name, "same-key public preparation proof unavailable")
	}
	if proof.Schema != "rtk-cloud.certissuer-public-profile-proof/v1" || proof.IntentSHA256 != deploymentplan.Digest(p) || proof.RequestID != p.RequestID || proof.StatePath != p.NewStatePath || proof.OriginalCertificateSHA256 != p.OriginalCertificateSHA256 || proof.PublicKeySHA256 != p.OriginalPublicKeySHA256 || proof.RootSHA256 != p.RootSHA256 || !slices.Equal(proof.DNSNames, p.DNSNames) || !environmentPreflightDigest.MatchString(proof.CertificateSHA256) || !environmentPreflightDigest.MatchString(proof.StateSHA256) {
		return proof, certIssuerPublicBlock("Job/"+ns+"/"+name, "same-key public preparation receipt differs")
	}
	// Fresh owner-state inspection is authoritative after the genuine helper Job.
	raw, err = backend.command(ctx, nil, "-n", ns, "exec", "deployment/certissuer", "-c", "certissuer", "--", "/app/serviceidentity-bootstrap", "inspect-server", p.NewStatePath, "service", p.Name, p.RootSHA256, strings.Join(p.DNSNames, ","))
	var state struct {
		Subject         string `json:"subject"`
		Fingerprint     string `json:"fingerprint"`
		PublicKeySHA256 string `json:"public_key_sha256"`
		StateSHA256     string `json:"state_sha256"`
		RootSHA256      string `json:"root_sha256"`
		Pending         bool   `json:"pending"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil || state.Pending || state.Subject != p.Name || state.Fingerprint != proof.CertificateSHA256 || state.PublicKeySHA256 != proof.PublicKeySHA256 || state.StateSHA256 != proof.StateSHA256 || state.RootSHA256 != proof.RootSHA256 {
		return proof, certIssuerPublicBlock("Deployment/"+ns+"/certissuer", "fresh same-key public profile inspection differs")
	}
	return proof, nil
}

// The accepted helper may already be retired. Recheck the actual owner state
// and public TLS leaf, then the existing incomplete authenticated/anonymous
// probe. No helper Job or temporary credential is used as readiness evidence.
func verifyEnvironmentPostCleanupCertIssuerServing(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, p environmentCertIssuerPublicProfile, proof environmentCertIssuerPublicProof) error {
	ns := p.Stack + "-video-cloud"
	raw, err := backend.command(ctx, nil, "-n", ns, "exec", "deployment/certissuer", "-c", "certissuer", "--", "/app/serviceidentity-bootstrap", "inspect-server", p.NewStatePath, "service", p.Name, p.RootSHA256, strings.Join(p.DNSNames, ","))
	var state struct {
		Subject         string `json:"subject"`
		Fingerprint     string `json:"fingerprint"`
		PublicKeySHA256 string `json:"public_key_sha256"`
		StateSHA256     string `json:"state_sha256"`
		RootSHA256      string `json:"root_sha256"`
		Pending         bool   `json:"pending"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil || state.Pending || state.Subject != p.Name || state.Fingerprint != proof.CertificateSHA256 || state.PublicKeySHA256 != p.OriginalPublicKeySHA256 || state.StateSHA256 != proof.StateSHA256 || state.RootSHA256 != p.RootSHA256 {
		return certIssuerPublicBlock("Deployment/"+ns+"/certissuer", "current public serving profile differs from its prepared same-key proof")
	}
	host := lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(cfg)).PublicHost
	raw, err = backend.command(ctx, nil, "-n", p.Stack+"-account-manager", "exec", "deployment/account-manager", "-c", "pkimanagement", "--", "/bin/sh", "-ec", `timeout 12 openssl s_client -connect "$1:443" -servername "$1" -showcerts -verify_return_error -verify_hostname "$1" -CAfile /run/pki-service/root.pem </dev/null 2>/dev/null || true`, "public-peer", host)
	if err != nil {
		return certIssuerPublicBlock("Ingress/"+ns+"/certissuer-public-mtls", "public TLS leaf is unavailable from original Account Manager")
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || environmentPreflightBytesSHA256(block.Bytes) != proof.CertificateSHA256 {
		return certIssuerPublicBlock("Ingress/"+ns+"/certissuer-public-mtls", "actual public TLS leaf differs from the prepared same-key certificate")
	}
	kubeconfig, cleanup, err := backend.privateKubeconfig()
	if err != nil {
		return certIssuerPublicBlock("", "original Kubernetes identity unavailable for public probe")
	}
	defer cleanup()
	return runDeploymentCertIssuerPublicProbe(ctx, cfg.Workspace, cfg.Environment, backend.Store.ConfigRoot, kubeconfig, host)
}
