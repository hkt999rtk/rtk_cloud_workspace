package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func normalTailProgramFixture(t *testing.T) (deploymentConfig, *environmentCreateExecution, createNormalTailProgramArtifact) {
	t.Helper()
	cfg, _, operator, original := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, original, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) == 0 {
		t.Fatal("explicit enabled products required", err)
	}
	root := environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111", RootFingerprint: strings.Repeat("a", 64)}
	policy := environmentCreateIssuerPolicy{}
	for _, output := range outputs {
		policy.ServiceClientIDs = append(policy.ServiceClientIDs, output.Subject)
	}
	for i := range outputs {
		outputs[i].RootID, outputs[i].RootSHA256, outputs[i].PolicySHA256 = root.ID, root.RootFingerprint, deploymentplan.Digest(policy)
	}
	ns := e.Journal.Stack + "-video-cloud"
	app := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "product"}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "product"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "product"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "product", "image": "example.invalid/product@sha256:" + strings.Repeat("b", 64)}}, "volumes": []any{deploymentplan.Object{"name": "identity", "secret": deploymentplan.Object{"secretName": strings.TrimPrefix(outputs[0].ID, "Secret/"+ns+"/")}}}}}}}
	defs := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}, Phase: "namespace"}, {Object: app, Phase: "workload", Dependencies: []string{outputs[0].ID}}}
	digest := deploymentplan.Digest("isolated frozen input")
	p := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), HandoffSHA256: digest, ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(cfg), ClusterSHA256: digest, IdentitySHA256: digest, Bindings: environmentIdentityBindings{Version: 1, Environment: cfg.Environment, Stack: e.Journal.Stack, Owners: map[string]string{}}, Definitions: defs}
	p.Products = &createNormalTailProductLifecycle{ParentSHA256: p.ParentSHA256, OwnerSHA256: p.OwnerSHA256, SourceSHA256: deploymentplan.Digest(e.Journal), Root: root, Policy: policy, Outputs: outputs, RequiresSuccessor: true, SourceDefinitions: defs, Bootstrap: environmentCreateBootstrapPlan{Environment: cfg.Environment, Stack: e.Journal.Stack}, StorageClass: "linode-block-storage-retain"}
	now := time.Now().UTC()
	a := createNormalTailProgramArtifact{Program: p, Generation: digest, CheckerSHA256: digest, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute), Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: deploymentplan.Digest(p)}}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: a.Qualification.PlanSHA256})
	}
	return cfg, e, a
}

func TestCreateNormalTailProgramPreservesRealProducerContracts(t *testing.T) {
	cfg, _, a := normalTailProgramFixture(t)
	outputs, err := createNormalTailProductOutputContracts(cfg, a.Program.Products)
	if err != nil {
		t.Fatal(err)
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "create", ConfigurationSHA256: a.Program.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(a.Program.Release), BindingsSHA256: deploymentplan.Digest(a.Program.Bindings)}
	bound, err := validateEnvironmentDeploymentDependencyContract(inputs, a.Program.Definitions, nil, a.Program.Bindings, outputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range bound {
		if d.Object["kind"] == "Secret" {
			t.Fatal("qualification fabricated a future identity Secret")
		}
	}
	if _, err := deploymentplan.Compile(inputs, bound, nil); err == nil {
		t.Fatal("a future identity contract became an executable target")
	}
	broken := append([]deploymentplan.Definition(nil), a.Program.Definitions...)
	broken[1].Dependencies = append(append([]string(nil), broken[1].Dependencies...), "Secret/video-cloud-dev-video-cloud/undeclared-secret")
	if _, err := validateEnvironmentDeploymentDependencyContract(inputs, broken, nil, a.Program.Bindings, outputs); err == nil {
		t.Fatal("complete target accepted an undeclared missing dependency")
	}
	if _, err := createNormalTailProgramAdmissionPreview(cfg, &a.Program, nil); err != nil {
		t.Fatal("static admission/capacity projection failed", err)
	}
}

func TestCreateNormalTailProgramQualifiesPublicTLSBeforeProduction(t *testing.T) {
	cfg, e, a := normalTailProgramFixture(t)
	cfg.DNSAdapter = "godaddy"
	cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] = "example.test"
	routes := []lkePublicHTTPSRoute{{Host: "api.example.test", Namespace: "video-cloud-dev-video-cloud", Service: "product", ServicePort: 80}}
	manifests := append(lkePublicHTTPSBridgeServiceManifests(cfg.Values, routes), lkePublicHTTPSIngressManifest(cfg.Values, "public", routes, "", ""))
	defs, err := decodeEnvironmentCreateManifests(manifests...)
	if err != nil {
		t.Fatal(err)
	}
	for i := range defs {
		if defs[i].Object["kind"] == "Ingress" {
			deploymentplan.Map(defs[i].Object["spec"])["ingressClassName"] = environmentIngressClass(cfg.Values)
		}
	}
	defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "IngressClass", "metadata": deploymentplan.Object{"name": environmentIngressClass(cfg.Values)}, "spec": deploymentplan.Object{"controller": "k8s.io/ingress-nginx"}}, Phase: "routing"})
	defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": "video-cloud-dev-ingress"}}, Phase: "namespace"}, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "product"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"port": 80, "targetPort": 80}}}}, Phase: "configuration"})
	a.Program.Definitions = append(a.Program.Definitions, defs...)
	a.Program.PublicTLS, err = prepareEnvironmentPublicTLS(t.Context(), cfg, e.Store, a.Program.Definitions, nil, a.Program.Bindings)
	if err != nil || a.Program.PublicTLS == nil || a.Program.PublicTLS.Source != nil || a.Program.PublicTLS.OwnerUID != "" {
		t.Fatal("qualification must describe the real producer without material or a future owner", err)
	}
	outputs, err := createNormalTailProductOutputContracts(cfg, a.Program.Products)
	if err != nil {
		t.Fatal(err)
	}
	publicOutputs, err := environmentPublicTLSOutputContract(a.Program.PublicTLS)
	if err != nil {
		t.Fatal(err)
	}
	for id, sha := range publicOutputs {
		outputs[id] = sha
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: a.Program.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(a.Program.Release), BindingsSHA256: deploymentplan.Digest(a.Program.Bindings)}
	bound, err := validateEnvironmentDeploymentDependencyContract(inputs, a.Program.Definitions, nil, a.Program.Bindings, outputs)
	if err != nil {
		t.Fatal("full catalog rejected its declared certificate producer", err)
	}
	a.Program.Definitions = bound
	if _, err := createNormalTailProgramAdmissionPreview(cfg, &a.Program, nil); err != nil {
		t.Fatal("static preview incorrectly requires the future certificate", err)
	}
	if _, err := deploymentplan.Compile(inputs, bound, nil); err == nil {
		t.Fatal("static producer promise became an executable certificate")
	}
	if _, err := resolveEnvironmentPublicTLSDefinitions(e.Store, a.Program.PublicTLS, bound); err == nil {
		t.Fatal("execution accepted a public certificate without genuine publication")
	}
	delete(outputs, a.Program.PublicTLS.SecretID)
	if _, err := validateEnvironmentDeploymentDependencyContract(inputs, bound, nil, a.Program.Bindings, outputs); err == nil {
		t.Fatal("complete catalog accepted a public TLS consumer without its producer")
	}
	for _, d := range bound {
		if deploymentplan.ID(d.Object) == a.Program.PublicTLS.SecretID {
			t.Fatal("preflight fabricated a TLS Secret")
		}
	}
}

func TestCreateNormalTailProgramRejectsIncompleteAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "owner", "source", "missing-gate", "false-go", "wrong-gate-evidence", "future-root", "wrong-subject", "extra-producer"} {
		t.Run(mode, func(t *testing.T) {
			cfg, e, a := normalTailProgramFixture(t)
			switch mode {
			case "owner":
				a.Program.OwnerSHA256 = strings.Repeat("f", 64)
			case "source":
				a.Program.Products.SourceDefinitions = nil
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:6]
			case "false-go":
				a.Qualification.Gates[3].Status = "SKIPPED"
			case "wrong-gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("f", 64)
			case "future-root":
				a.Program.Products.Outputs[0].RootID = "replacement-root"
			case "wrong-subject":
				a.Program.Products.Outputs[0].Subject = "service:unexpected"
			case "extra-producer":
				a.Program.Products.Outputs = append(a.Program.Products.Outputs, a.Program.Products.Outputs[0])
			}
			err := validateCreateNormalTailProgramArtifact(a, e)
			if err == nil {
				_, err = createNormalTailProductOutputContracts(cfg, a.Program.Products)
			}
			if (err == nil) != (mode == "valid") {
				t.Fatalf("wrong program authority disposition: %v", err)
			}
		})
	}
}

func TestCreateNormalTailProgramReportsEveryIndependentPrerequisite(t *testing.T) {
	_, _, a := normalTailProgramFixture(t)
	var called []string
	checks := map[string]func(context.Context) error{}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		checks[id] = func(context.Context) error {
			called = append(called, id)
			if id == "deployment.target" || id == "deployment.pki" {
				return errors.New("isolated failed prerequisite")
			}
			return nil
		}
	}
	q, err := qualifyCreateNormalTailProgram(t.Context(), &a.Program, checks, io.Discard)
	if err == nil || q.Decision != "NO-GO" || len(called) != 7 || len(q.Gates) != 7 || q.Gates[0].Status != "FAIL" || q.Gates[3].Status != "FAIL" || q.Gates[6].Status != "PASS" {
		t.Fatal("full preflight did not report the complete prerequisite set")
	}
}

func TestCreateNormalTailProgramReportsFailedReleaseWithoutPrivateProgram(t *testing.T) {
	_, _, a := normalTailProgramFixture(t)
	a.Qualification.Decision = "NO-GO"
	a.Program.Release = environmentRelease{Environment: "dev", Stack: "video-cloud-dev", Images: map[string]environmentReleaseImage{"account-manager": {Reference: "example.test/account-manager@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}}}
	a.Program.PublicTLS = &environmentPublicTLSPlan{Source: &environmentPublicTLSMaterial{PrivateKey: "private-report-canary"}}
	a.Program.Definitions = append(a.Program.Definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "private", "namespace": "video-cloud-dev-video-cloud"}, "stringData": deploymentplan.Object{"password": "private-report-canary"}}})
	var report unifiedDeploymentReport
	setCreateNormalTailProgramReport(&report, a)
	raw, err := json.Marshal(report)
	if err != nil || report.Decision != "NO-GO" || report.Release == nil || deploymentplan.Digest(*report.Release) != deploymentplan.Digest(a.Program.Release) || strings.Contains(string(raw), "private-report-canary") {
		t.Fatal("failed preflight must report its exact release without private producer inputs", err)
	}
}
