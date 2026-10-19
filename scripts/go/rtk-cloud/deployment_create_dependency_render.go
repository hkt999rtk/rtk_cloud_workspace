package main

import (
	"context"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This private render input is constructed only from the initial bootstrap
// recipe. The executable renderer never accepts it or its graph projection.
type environmentCreateDependencyRender struct {
	managed              map[string]bool
	outputs              map[string]string
	publicCertIssuerHost string
}

func environmentCreateDependencyOutputs(cfg deploymentConfig, p environmentCreateBootstrapPlan, values map[string]string) (*environmentCreateDependencyRender, error) {
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Stack != "video-cloud-"+cfg.Environment || p.IngressAdmissionProfile != environmentCreateIngressAdmissionProfile || len(p.Claims) == 0 {
		return nil, environmentCreateBootstrapBlocker("fresh dependency qualification requires the complete selected bootstrap recipe")
	}
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(products) != len(p.ProductIdentities) {
		return nil, environmentCreateBootstrapBlocker("enabled product consumers lack their declared native identity producers")
	}
	host := lkeResolveCertIssuerTLSConfig(values).PublicHost
	if p.PublicCertIssuerHost != host {
		return nil, environmentCreateBootstrapBlocker("fresh public transport differs from its declared native bootstrap host")
	}
	intent := &environmentCreateDependencyRender{managed: map[string]bool{}, outputs: map[string]string{}, publicCertIssuerHost: host}
	add := func(id, stage string) {
		intent.outputs[id] = deploymentplan.Digest(struct {
			Recipe          environmentCreateBootstrapPlan
			Stage, Resource string
		}{p, stage, id})
	}
	for _, name := range p.Claims {
		suffix := "video-cloud"
		if name == "account-manager-service-state" {
			suffix = "account-manager"
		}
		if name == "openbao-pki-identity" {
			suffix = "secrets"
		}
		add("PersistentVolumeClaim/"+p.Stack+"-"+suffix+"/"+name, "managed-enrollment")
	}
	for _, owner := range []struct{ kind, suffix, name string }{
		{"Deployment", "account-manager", "account-manager"},
		{"Deployment", "video-cloud", "certissuer"}, {"Deployment", "video-cloud", "factoryenroll"},
		{"Deployment", "video-cloud", "certissuer-service-pki"}, {"Deployment", "video-cloud", "pki-controller"},
		{"Deployment", "video-cloud", "video-cloud-api"}, {"Deployment", "video-cloud", "video-cloud-api-pki"},
		{"Deployment", "video-cloud", "video-cloud-api-app-pki"}, {"Deployment", "video-cloud", "video-cloud-logingester"},
		{"StatefulSet", "video-cloud", "mqtt-pki"}, {"StatefulSet", "secrets", "openbao"},
	} {
		intent.managed[owner.name] = true
		add(owner.kind+"/"+p.Stack+"-"+owner.suffix+"/"+owner.name, "managed-owners")
	}
	for i, output := range p.ProductIdentities {
		base := output
		base.RootID, base.RootSHA256, base.IssuerID, base.PolicySHA256 = "", "", "", ""
		if deploymentplan.Digest(base) != deploymentplan.Digest(products[i]) || output.RootID != "" || output.RootSHA256 != "" || output.IssuerID != "" || output.PolicySHA256 != deploymentplan.Digest(findEnvironmentCreateDomainPolicy(p, "service")) || !slices.Contains(p.Claims, output.Claim) {
			return nil, environmentCreateBootstrapBlocker("fresh product output must bind its exact subject, claim and qualified Service policy without a fabricated issuer")
		}
		intent.outputs[output.ID] = deploymentplan.Digest(output)
	}
	add("Secret/"+p.Stack+"-account-manager/account-manager-certissuer-client", "managed-owners")
	add("Secret/"+p.Stack+"-account-manager/account-manager-service-registration-tls", "account-registration")
	add("IngressClass//"+environmentIngressClass(values), "ingress-controller")
	for _, suffix := range []string{"video-cloud", "account-manager", "secrets"} {
		add("Secret/"+p.Stack+"-"+suffix+"/"+lkeImagePullSecretName(values), "foundation")
	}
	// Namespace creation precedes the native bootstrap. Kubernetes' namespace
	// controller creates this API-defined account; it is not a secret producer
	// or a fabricated installed UID in the executable target.
	for _, namespace := range lkeNamespaces(values) {
		add("ServiceAccount/"+namespace.Name+"/default", "namespace-controller")
	}
	return intent, nil
}

// Fresh dependency qualification has a declared native issuer producer, not an
// installed Deployment or identity. Project only its public Service/route edges;
// executable rendering must still derive the port from the actual issuer owner.
func bindEnvironmentCreateDependencyPublicTransport(env map[string]string, definitions []deploymentplan.Definition, intent *environmentCreateDependencyRender) ([]deploymentplan.Definition, error) {
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	if host == "" {
		return definitions, nil
	}
	owner := "Deployment/" + env["CLOUD_STACK_NAME"] + "-video-cloud/certissuer"
	if intent == nil || !intent.managed["certissuer"] || !environmentPreflightDigest.MatchString(intent.outputs[owner]) || intent.publicCertIssuerHost != host {
		return nil, certIssuerPublicBlock(owner, "fresh public transport requires its exact declared native issuer producer and host")
	}
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == owner {
			return nil, certIssuerPublicBlock(owner, "fresh dependency projection cannot substitute an installed issuer owner")
		}
	}
	service, err := certIssuerPublicObject(definitions, "Service/"+env["CLOUD_STACK_NAME"]+"-video-cloud/certissuer")
	if err != nil {
		return nil, err
	}
	// The maintained native bootstrap listener is 18445, independent of any
	// future certificate, private key, provider allocation or Kubernetes UID.
	deploymentplan.Map(service["spec"])["ports"] = []any{deploymentplan.Object{"name": "https", "port": float64(18445), "targetPort": float64(18445), "protocol": "TCP"}}
	return bindEnvironmentCertIssuerPublicRoute(env, definitions, 18445)
}

// Check the complete maintained consumer catalog before any native create.
// Future keys/certificates, owner UIDs and provider endpoints are not invented:
// their exact declared producer contracts satisfy only this static graph pass.
func qualifyEnvironmentCreateDependencyContract(ctx context.Context, cfg deploymentConfig, release environmentRelease, store secretStore, operator map[string]string, p environmentCreateBootstrapPlan) (int, error) {
	publicTLS, err := prepareEnvironmentCreatePublicTLS(ctx, cfg, store)
	if err != nil {
		return 0, err
	}
	publicOutputs, err := environmentPublicTLSOutputContract(publicTLS)
	if err != nil {
		return 0, err
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return 0, err
	}
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}
	var support []deploymentplan.Definition
	if cfg.Values["SUPPORT_TICKETS_ENABLED"] == "true" {
		values, chart, err := readEnvironmentZammadInputs(cfg)
		if err != nil {
			return 0, err
		}
		support, err = renderEnvironmentZammad(ctx, cfg, store, release, "create", values, chart)
		if err != nil {
			return 0, err
		}
	}
	values := appendMap(appendMap(cfg.Values, cfg.AdapterValues), cfg.AdapterResolved)
	values = appendMap(values, deploymentLegacyLKEValues(values, cfg.Environment))
	values = appendMap(values, cfg.DNSValues)
	values["CLOUD_ENV_NAME"] = cfg.Environment
	allocations := 0
	err = withEnvironmentRenderValues(store, release, snapshot, values, func(env map[string]string) error {
		intent, err := environmentCreateDependencyOutputs(cfg, p, env)
		if err != nil {
			return err
		}
		for id, contract := range publicOutputs {
			intent.outputs[id] = contract
		}
		bindings := environmentIdentityBindings{Version: 1, Environment: cfg.Environment, Stack: p.Stack, Owners: map[string]string{}}
		defs, err := renderEnvironmentResourceCatalog(env, bindings, release, intent, support)
		if err != nil {
			return err
		}
		if err := validateEnvironmentFreshConnectivityCatalog(cfg, p, defs); err != nil {
			return err
		}
		defs, err = bindEnvironmentPublicTLSReferences(publicTLS, defs)
		if err != nil {
			return err
		}
		runtime, err := renderEnvironmentRuntimeDependencyDefinitions(env, secrets, operator)
		if err != nil {
			return err
		}
		// These are the same canonical mirrors produced by foundation and
		// application owners, not generated identity or fallback credentials.
		foundation, err := decodeEnvironmentCreateManifests(lkePostgresSecretManifest(env), lkeAccountManagerSecretManifest(env), lkeVideoCloudRuntimeSecretManifest(env), lkeFactoryEnrollRuntimeSecretManifest(env), lkeVideoCloudWorkersSecretManifest(env))
		if err != nil {
			return err
		}
		defs = append(defs, runtime...)
		defs = append(defs, foundation...)
		defs, err = bindEnvironmentInitialSchemaOwner(defs, cfg.Environment, release.Stack, "create", release, env["LKE_IMAGE_PULL_SECRET_NAME"])
		if err != nil {
			return err
		}
		if err := bindEnvironmentNewStorageClasses(defs, env["DURABLE_DATA_STORAGE_CLASS"], nil); err != nil {
			return err
		}
		openbao, err := renderEnvironmentCreateOpenBaoFoundation(cfg, release, "openbao-transport-tls", "pki-openbao-transport-ca")
		if err != nil {
			return err
		}
		if err := bindEnvironmentNewStorageClasses(openbao, cfg.Values["DURABLE_DATA_STORAGE_CLASS"], nil); err != nil {
			return err
		}
		allocations, err = countEnvironmentCreateDependencyAllocations(p, append(slices.Clone(defs), openbao...), intent.outputs)
		if err != nil {
			return err
		}
		// The shared native bootstrap renders these owners earlier. A static
		// dependency pass must not schedule a second executable owner.
		filtered := defs[:0]
		seen := map[string]string{}
		for _, d := range defs {
			id := deploymentplan.ID(d.Object)
			if previous := seen[id]; previous != "" {
				if previous != deploymentplan.Digest(d.Object) {
					return environmentCreateBootstrapBlocker("maintained dependency producers conflict: " + id)
				}
				continue
			}
			seen[id] = deploymentplan.Digest(d.Object)
			if intent.outputs[id] == "" {
				filtered = append(filtered, d)
			}
		}
		input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(p)}
		if _, err := validateEnvironmentDeploymentDependencyContract(input, filtered, nil, bindings, intent.outputs); err != nil {
			return err
		}
		return ctx.Err()
	})
	return allocations, err
}

// Count exactly the same rendered direct claims and native StatefulSet ordinal
// expansion, plus the earlier bootstrap's declared durable claims. No provider
// allocation is represented by a guessed UID or a fixed catalog count.
func countEnvironmentCreateDependencyAllocations(p environmentCreateBootstrapPlan, definitions []deploymentplan.Definition, outputs map[string]string) (int, error) {
	graph := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack}}
	seen := map[string]string{}
	for _, d := range definitions {
		if d.Retain || d.Retire || d.Object["kind"] != "PersistentVolumeClaim" && d.Object["kind"] != "StatefulSet" {
			continue
		}
		id, digest := deploymentplan.ID(d.Object), deploymentplan.Digest(d.Object)
		if previous := seen[id]; previous != "" {
			if previous != digest {
				return 0, environmentCreateBootstrapBlocker("maintained allocation producers conflict: " + id)
			}
			continue
		}
		seen[id] = digest
		graph.Resources = append(graph.Resources, deploymentplan.Resource{ID: id, Action: "create", Desired: deploymentplan.Clone(d.Object)})
	}
	claims, err := managedLatePlannedVolumeClaims(graph)
	if err != nil {
		for _, resource := range graph.Resources {
			one := *graph
			one.Resources = []deploymentplan.Resource{resource}
			if _, cause := managedLatePlannedVolumeClaims(&one); cause != nil {
				return 0, environmentCreateBootstrapBlocker("maintained allocation intent is incomplete: " + resource.ID)
			}
		}
		return 0, err
	}
	ids := map[string]bool{}
	for id := range claims {
		ids[id] = true
	}
	for id := range outputs {
		if strings.HasPrefix(id, "PersistentVolumeClaim/") {
			ids[id] = true
		}
	}
	return len(ids), nil
}
