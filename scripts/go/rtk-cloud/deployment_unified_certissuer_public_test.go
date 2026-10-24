package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This fixture starts with maintained native service recipes and the common
// complete catalog. The only stale path is the historical public Ingress9443.
func certIssuerPublicCorrectionFixture(t *testing.T) (deploymentConfig, []deploymentplan.Definition, environmentCertIssuerPublicProfile) {
	t.Helper()
	cfg, p, runtime, ceremony := createServiceRecipeFixture(t, "dev")
	env, bindings, release := completeEnvironmentRenderFixture("dev")
	env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "certissuer.video-cloud-dev.example.invalid"
	cfg.Values = env
	bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(env)
	defer restore()
	defs, err := renderEnvironmentResources(env, bindings, release)
	if err != nil {
		t.Fatal(err)
	}
	native, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, ceremony)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range native {
		id := deploymentplan.ID(d.Object)
		if id == "Deployment/"+p.Stack+"-video-cloud/certissuer" || id == "Service/"+p.Stack+"-video-cloud/certissuer" {
			for i := range defs {
				if deploymentplan.ID(defs[i].Object) == id {
					defs[i] = d
				}
			}
		}
	}
	// Include the actual maintained original claim and dedicated ServiceAccount.
	defs = append(defs, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", "certissuer-service-state"))
	original := defs[:0]
	for _, d := range defs {
		if deploymentplan.ID(d.Object) != "NetworkPolicy/"+p.Stack+"-video-cloud/allow-certissuer-public-mtls" {
			original = append(original, d)
		}
	}
	defs = original
	host := lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(cfg)).PublicHost
	profile := environmentCertIssuerPublicProfile{Schema: "rtk-cloud.certissuer-public-profile/v1", Environment: "dev", Stack: p.Stack, Name: "certissuer." + p.Stack + "-video-cloud.svc", RootSHA256: ceremony.Root.RootFingerprint, StatePath: "/var/lib/pki-service/private/server.json", ClientStatePath: "/var/lib/pki-service/private/client.json", OriginalStateSHA256: strings.Repeat("1", 64), OriginalCertificateSHA256: strings.Repeat("2", 64), OriginalPublicKeySHA256: strings.Repeat("3", 64), RequestID: "certissuer-public-fixture", HelperImage: release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference}
	profile.OriginalDNSNames = []string{profile.Name}
	profile.DNSNames = []string{profile.Name, host}
	slices.Sort(profile.DNSNames)
	profile.NewStatePath = filepath.Join(filepath.Dir(profile.StatePath), "public-"+environmentPreflightBytesSHA256([]byte(profile.RequestID))[:12], "server.json")
	if err := profile.validate(cfg, defs); err != nil {
		t.Fatal(err)
	}
	return cfg, defs, profile
}

func certIssuerPublicOverlay(base, changes []deploymentplan.Definition) []deploymentplan.Definition {
	out := make([]deploymentplan.Definition, 0, len(base)+len(changes))
	ids := map[string]bool{}
	for _, d := range changes {
		ids[deploymentplan.ID(d.Object)] = true
	}
	for _, d := range base {
		if !ids[deploymentplan.ID(d.Object)] {
			out = append(out, d)
		}
	}
	return append(out, changes...)
}

func TestCertIssuerPublicCorrectionUsesMaintainedNativeCatalog(t *testing.T) {
	cfg, original, profile := certIssuerPublicCorrectionFixture(t)
	before := deploymentplan.Digest(original)
	changes, err := environmentPostCleanupCertIssuerPublicDefinitions(cfg, original, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || deploymentplan.Digest(original) != before {
		t.Fatal("correction changed its original catalog")
	}
	corrected := certIssuerPublicOverlay(original, changes)
	if err := validateEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(cfg), corrected); err != nil {
		t.Fatal(err)
	}
	again, err := bindEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(cfg), corrected)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(corrected) {
		t.Fatal("public route binding is not idempotent", err)
	}
	beforeDeployment := createRecipeObject(t, original, "Deployment/"+profile.Stack+"-video-cloud/certissuer")
	after := deploymentplan.Clone(createRecipeObject(t, corrected, deploymentplan.ID(beforeDeployment)))
	c := managedNamedField(environmentPKIPod(after)["containers"], "certissuer")
	for _, v := range certIssuerObjectList(c["env"]) {
		e := deploymentplan.Map(v)
		if e["name"] == "CERT_ISSUER_HOST_IDENTITY_STATE" {
			e["value"] = profile.StatePath
		}
		if e["name"] == "CERT_ISSUER_HOST_DNS_NAMES" {
			e["value"] = strings.Join(profile.OriginalDNSNames, ",")
		}
	}
	if deploymentplan.Digest(after) != deploymentplan.Digest(beforeDeployment) {
		t.Fatal("correction changed fields beyond exact public profile")
	}
	prep, err := environmentPostCleanupCertIssuerPreparationDefinitions(cfg, original, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep) != 3 {
		t.Fatal("unexpected preparation resources")
	}
	job := prep[2]
	pod := environmentPKIPod(job.Object)
	if job.Phase != "configuration" || !slices.Equal(job.Dependencies, []string{deploymentplan.ID(prep[0].Object), deploymentplan.ID(prep[1].Object)}) {
		t.Fatal("Job prerequisites do not precede its native execution")
	}
	if deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil || deploymentplan.Map(pod["securityContext"])["runAsGroup"] != float64(10001) || nativeNetworkPodLabels(job.Object)["app.kubernetes.io/name"] == "certissuer" {
		t.Fatal("helper joined serving endpoint or changed PVC permissions")
	}
	issuer := createRecipeObject(t, corrected, "Deployment/"+profile.Stack+"-video-cloud/certissuer")
	if !nativeNetworkPolicyAllows(prep, job.Object, issuer, "TCP", 18445, false) {
		t.Fatal("helper cannot reach native authenticated issuer")
	}
	vars := createRecipeEnv(managedNamedField(pod["containers"], "prepare"))
	if vars["PKI_DATABASE_URL"] != "" {
		t.Fatal("database credential was flattened")
	}
	source := managedNamedField(managedNamedField(pod["containers"], "prepare")["env"], "PKI_DATABASE_URL")
	if source["valueFrom"] == nil {
		t.Fatal("helper lost original database role reference")
	}
	// Compile the actual preparation graph, proving phase/dependency ordering.
	inputs := deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: profile.Stack, ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	plan, err := deploymentplan.Compile(inputs, prep, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Resources) != 3 || deploymentplan.ID(plan.Resources[2].Desired) != deploymentplan.ID(job.Object) {
		t.Fatal("helper was admitted before prerequisites")
	}
}

func TestCertIssuerPublicCatalogGraphAllEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-public-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, b, release := completeEnvironmentRenderFixture(environment)
			env["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "certissuer.video-cloud-" + environment + ".example.invalid"
			b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			defs, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentCertIssuerPublicTransport(env, defs); err != nil {
				t.Fatal(err)
			}
			issuer := createRecipeObject(t, defs, "Deployment/"+b.Stack+"-video-cloud/certissuer")
			ingress := createRecipeObject(t, defs, "Deployment/"+b.Stack+"-ingress/ingress-nginx-controller")
			port, err := environmentCertIssuerPublicPort(defs, b.Stack)
			if err != nil {
				t.Fatal(err)
			}
			if !nativeNetworkPolicyAllows(defs, ingress, issuer, "TCP", float64(port), false) {
				t.Fatal("actual controller selectors cannot reach serving listener")
			}
			found := false
			for _, v := range certIssuerObjectList(managedNamedField(environmentPKIPod(ingress)["containers"], "controller")["args"]) {
				found = found || v == "--enable-ssl-passthrough"
			}
			if !found {
				t.Fatal("controller cannot preserve direct caller TLS")
			}
			only := []deploymentplan.Definition{environmentCertIssuerPublicPolicy(env, port)}
			for _, name := range []string{"foreign-namespace", "foreign-pod", "wrong-port", "udp"} {
				t.Run(name, func(t *testing.T) {
					peer := deploymentplan.Clone(ingress)
					protocol := "TCP"
					checkPort := float64(port)
					switch name {
					case "foreign-namespace":
						deploymentplan.Map(peer["metadata"])["namespace"] = "foreign"
					case "foreign-pod":
						nativeNetworkPodLabels(peer)["app.kubernetes.io/name"] = "foreign"
					case "wrong-port":
						checkPort++
					case "udp":
						protocol = "UDP"
					}
					if nativeNetworkPolicyAllows(only, peer, issuer, protocol, checkPort, false) {
						t.Fatal("public policy admits foreign peer or protocol")
					}
				})
			}
		})
	}
}

func TestCertIssuerPublicCatalogRejectsIncompleteGraph(t *testing.T) {
	cfg, original, profile := certIssuerPublicCorrectionFixture(t)
	changes, err := environmentPostCleanupCertIssuerPublicDefinitions(cfg, original, profile)
	if err != nil {
		t.Fatal(err)
	}
	corrected := certIssuerPublicOverlay(original, changes)
	for _, name := range []string{"old-route", "service-port", "target-port", "missing-san", "terminating-ingress", "missing-policy", "foreign-peer", "wrong-policy-port"} {
		t.Run(name, func(t *testing.T) {
			var defs []deploymentplan.Definition
			for _, d := range corrected {
				d.Object = deploymentplan.Clone(d.Object)
				defs = append(defs, d)
			}
			issuer := createRecipeObject(t, defs, "Deployment/"+profile.Stack+"-video-cloud/certissuer")
			service := createRecipeObject(t, defs, "Service/"+profile.Stack+"-video-cloud/certissuer")
			ingress := createRecipeObject(t, defs, "Ingress/"+profile.Stack+"-video-cloud/certissuer-public-mtls")
			policy := createRecipeObject(t, defs, "NetworkPolicy/"+profile.Stack+"-video-cloud/allow-certissuer-public-mtls")
			switch name {
			case "old-route":
				_ = environmentCertIssuerPublicIngress(ingress, lkeResolveCertIssuerTLSConfig(cfg.Values).PublicHost, 9443, true)
			case "service-port":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"])[0])["port"] = float64(9443)
			case "target-port":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"])[0])["targetPort"] = float64(9443)
			case "missing-san":
				for _, v := range certIssuerObjectList(managedNamedField(environmentPKIPod(issuer)["containers"], "certissuer")["env"]) {
					e := deploymentplan.Map(v)
					if e["name"] == "CERT_ISSUER_HOST_DNS_NAMES" {
						e["value"] = profile.Name
					}
				}
			case "terminating-ingress":
				deploymentplan.Map(deploymentplan.Map(ingress["metadata"])["annotations"])["nginx.ingress.kubernetes.io/ssl-passthrough"] = "false"
			case "missing-policy":
				for i, d := range defs {
					if deploymentplan.ID(d.Object) == deploymentplan.ID(policy) {
						defs = append(defs[:i], defs[i+1:]...)
						break
					}
				}
			case "foreign-peer":
				rule := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(policy["spec"])["ingress"])[0])
				peer := deploymentplan.Map(certIssuerObjectList(rule["from"])[0])
				deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])["kubernetes.io/metadata.name"] = "foreign"
			case "wrong-policy-port":
				rule := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(policy["spec"])["ingress"])[0])
				deploymentplan.Map(certIssuerObjectList(rule["ports"])[0])["port"] = float64(9443)
			}
			if err := validateEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(cfg), defs); err == nil {
				t.Fatal("incomplete public path admitted")
			}
		})
	}
}

func TestCertIssuerPublicFreshProfilePreservesHistoricalRecipe(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, runtime, s := createServiceRecipeFixture(t, environment)
			oldBytes, _ := json.Marshal(p)
			old, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			oldManaged, err := renderEnvironmentCreateManagedServiceIssuerDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			oldAgain, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil || deploymentplan.Digest(oldAgain) != deploymentplan.Digest(old) || strings.Contains(string(oldBytes), "public_certissuer_host") {
				t.Fatal("zero-field historical recipe changed")
			}
			host := lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(cfg)).PublicHost
			p.PublicCertIssuerHost = host
			p.Domains[0].ServerDNSNames = append(p.Domains[0].ServerDNSNames, host)
			next, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			nextManaged, err := renderEnvironmentCreateManagedServiceIssuerDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{"certissuer." + p.Stack + "-video-cloud.svc", host}
			slices.Sort(names)
			jobEnv := createRecipeEnv(managedNamedField(environmentPKIPod(next[1].Object)["containers"], "enroll"))
			if jobEnv["PKI_SERVER_DNS_NAMES"] == "" {
				jobEnv = createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(next[1].Object)["containers"])[0]))
			}
			if jobEnv["PKI_SERVER_DNS_NAMES"] != strings.Join(names, ",") {
				t.Fatal("new enrollment CSR omits public DNS")
			}
			nextIssuer := createRecipeObject(t, nextManaged, "Deployment/"+p.Stack+"-video-cloud/certissuer")
			if mqttFoundationEnv(managedNamedField(environmentPKIPod(nextIssuer)["containers"], "certissuer"), "CERT_ISSUER_HOST_DNS_NAMES") != strings.Join(names, ",") {
				t.Fatal("managed profile differs from first enrolled CSR")
			}
			if deploymentplan.Digest(oldManaged) == deploymentplan.Digest(nextManaged) {
				t.Fatal("new profile was not selected")
			}
			p.PublicCertIssuerHost = "foreign.example"
			if _, err := renderEnvironmentCreateManagedServiceIssuerDefinitions(context.Background(), cfg, p, runtime, s); err == nil {
				t.Fatal("foreign source selector accepted")
			}
		})
	}
}

func TestCertIssuerPublicPreparationSelectsOnlySuccessfulOwnedPod(t *testing.T) {
	cfg, defs, p := certIssuerPublicCorrectionFixture(t)
	prep, err := environmentPostCleanupCertIssuerPreparationDefinitions(cfg, defs, p)
	if err != nil {
		t.Fatal(err)
	}
	job := deploymentplan.Clone(prep[2].Object)
	deploymentplan.Map(job["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	job["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	name, ns := environmentCertIssuerPublicPreparationName(p), p.Stack+"-video-cloud"
	pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": name + "-successful", "namespace": ns, "ownerReferences": []any{deploymentplan.Object{"kind": "Job", "name": name, "uid": deploymentplan.Map(job["metadata"])["uid"], "controller": true}}}, "spec": deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(job["spec"])["template"])["spec"])), "status": deploymentplan.Object{"phase": "Succeeded", "containerStatuses": []any{deploymentplan.Object{"name": "prepare", "restartCount": float64(1), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}}}
	failed := deploymentplan.Clone(pod)
	deploymentplan.Map(failed["metadata"])["name"] = name + "-failed"
	deploymentplan.Map(failed["status"])["phase"] = "Failed"
	if selected, err := environmentCertIssuerPublicSuccessfulPod(p, job, []deploymentplan.Object{failed, pod}); err != nil || selected != name+"-successful" {
		t.Fatal("lost-response retry did not select final exact ordinary Job Pod", err)
	}
	if deploymentplan.Map(environmentPKIPod(job)["securityContext"])["fsGroup"] != nil || environmentPKIPod(job)["restartPolicy"] != "OnFailure" || deploymentplan.Map(job["spec"])["backoffLimit"] != float64(2) {
		t.Fatal("native response reconciliation lost bounded same-claim retry")
	}
	for _, mode := range []string{"no-completion", "failed-only", "foreign-owner", "wrong-image", "wrong-command", "exit-failure", "duplicate-success", "terminating-job"} {
		t.Run(mode, func(t *testing.T) {
			j, x := deploymentplan.Clone(job), deploymentplan.Clone(pod)
			pods := []deploymentplan.Object{x}
			switch mode {
			case "no-completion":
				delete(j, "status")
			case "failed-only":
				pods = []deploymentplan.Object{failed}
			case "foreign-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(x["metadata"])["ownerReferences"])[0])["uid"] = "22222222-2222-4222-8222-222222222222"
			case "wrong-image":
				managedNamedField(deploymentplan.Map(x["spec"])["containers"], "prepare")["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
			case "wrong-command":
				managedNamedField(deploymentplan.Map(x["spec"])["containers"], "prepare")["command"] = []any{"foreign"}
			case "exit-failure":
				deploymentplan.Map(deploymentplan.Map(managedNamedField(deploymentplan.Map(x["status"])["containerStatuses"], "prepare")["state"])["terminated"])["exitCode"] = float64(1)
			case "duplicate-success":
				pods = append(pods, deploymentplan.Clone(x))
			case "terminating-job":
				deploymentplan.Map(j["metadata"])["deletionTimestamp"] = "2026-10-20T00:00:00Z"
			}
			if _, err := environmentCertIssuerPublicSuccessfulPod(p, j, pods); err == nil {
				t.Fatal("foreign or non-success proof source accepted")
			}
		})
	}
}
