package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyRegistrationReprojectsAcceptedKVOwnersWithoutLosingIdentity(t *testing.T) {
	cfg, p, service, certificate, key, crl, _ := accountRegistrationFixture(t)
	for _, issuer := range []*environmentCreateRegisteredIssuer{&service.Root, &service.Intermediate} {
		cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		doc := deploymentplan.Clone(issuer.Document)
		chain := issuer.Certificate
		if issuer.Kind == "intermediate" {
			chain += service.Root.Certificate
		}
		doc["certificate_pem"], doc["chain_pem"] = issuer.Certificate, chain
		doc["certificate_fingerprint_sha256"], doc["trust_bundle_version"] = environmentPreflightBytesSHA256(cert.Raw), environmentPreflightBytesSHA256([]byte(chain))
		doc["not_before"], doc["not_after"] = cert.NotBefore.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339)
		updated, err := environmentCreateRegisteredIssuerFromMap(doc, service.Root.RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		*issuer = updated
	}
	_, _, runtime, _ := createServiceRecipeFixture(t, "dev")
	store, e, template, backend := freshStageFixture(t)
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	runtime.Execution, runtime.ExecutionSHA256 = e, e.Journal.PlanSHA256
	runtime.Backend = environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-account-registration-kubeconfig")}
	runtime.Custody, _ = createEnvironmentCustodyBinding(cfg, p, e.Journal.PlanSHA256, strings.Repeat("a", 64))
	if runtime.Custody == nil {
		t.Fatal("original custody binding unavailable")
	}
	runtime.Custody.ServiceRootSHA256 = service.Root.RootFingerprint
	project := func(defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
		return projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, runtime.Custody, "", defs)
	}
	apply := func(stage string, defs []deploymentplan.Definition) (*deploymentplan.Plan, error) {
		projected, err := project(defs)
		if err != nil {
			return nil, err
		}
		var live []deploymentplan.Object
		for _, object := range backend.objects {
			live = append(live, object)
		}
		in := template.Inputs
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, projected)
		plan, err := deploymentplan.Compile(in, projected, live)
		if err != nil {
			return nil, err
		}
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
		err = executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, backend, func(ctx context.Context, plan *deploymentplan.Plan) error {
			return plan.AdmitResumable(ctx, backend, store.Root)
		}, check, func(ctx context.Context, plan *deploymentplan.Plan) error { return plan.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard)
		return plan, err
	}
	core, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply("managed-complete-issuer", core); err != nil {
		t.Fatal("genuine original KV core", err)
	}
	current, err := currentEnvironmentCreateAccountRegistrationCoreWithBackend(t.Context(), cfg, runtime, core, backend)
	if err != nil {
		t.Fatal("authenticated current KV registration prerequisites", err)
	}
	registration, err := renderEnvironmentCreateAccountRegistration(cfg, p, service, certificate, key, crl, current)
	if err != nil {
		t.Fatal(err)
	}
	registration, err = environmentCreateAccountRegistrationStage(p, registration)
	if err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(registration)
	projected, err := project(registration)
	if err != nil {
		t.Fatal("real KV registration apply cannot reproject accepted owner metadata", err)
	}
	access := map[string]deploymentplan.Definition{}
	for _, d := range registration {
		if slices.Contains([]string{"Role", "RoleBinding", "NetworkPolicy"}, deploymentplan.String(d.Object["kind"])) {
			access[deploymentplan.ID(d.Object)] = d
		}
	}
	if len(access) == 0 {
		t.Fatal("registration did not include accepted custody access dependencies")
	}
	for _, d := range projected {
		if old, ok := access[deploymentplan.ID(d.Object)]; ok && (deploymentplan.Digest(old.Object) != deploymentplan.Digest(d.Object) || deploymentplan.Digest(old.Dependencies) != deploymentplan.Digest(d.Dependencies) || old.Retain != d.Retain) {
			t.Fatal("custody projection dropped original native metadata, UID or dependency intent", deploymentplan.ID(d.Object))
		}
	}
	if deploymentplan.Digest(registration) != before {
		t.Fatal("projection mutated authenticated registration source")
	}
	for _, mode := range []string{"rules", "subjects", "roleRef", "network-spec", "unknown-field", "foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			defs := slices.Clone(registration)
			changed := ""
			for i := range defs {
				o := deploymentplan.Clone(defs[i].Object)
				kind := deploymentplan.String(o["kind"])
				if mode == "rules" && kind == "Role" {
					o["rules"] = []any{deploymentplan.Object{"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"}}}
				} else if mode == "subjects" && kind == "RoleBinding" {
					o["subjects"] = []any{deploymentplan.Object{"kind": "ServiceAccount", "name": "foreign", "namespace": p.Stack + "-account-manager"}}
				} else if mode == "roleRef" && kind == "RoleBinding" {
					deploymentplan.Map(o["roleRef"])["name"] = "foreign"
				} else if mode == "network-spec" && kind == "NetworkPolicy" {
					deploymentplan.Map(o["spec"])["ingress"] = []any{deploymentplan.Object{}}
				} else if mode == "unknown-field" && kind == "Role" {
					o["unreviewed"] = true
				} else if mode == "foreign-owner" && kind == "Role" {
					deploymentplan.Map(deploymentplan.Map(o["metadata"])["labels"])["rtk.realtek.com/stack"] = "video-cloud-staging"
				} else {
					continue
				}
				defs[i].Object, changed = o, deploymentplan.ID(o)
				break
			}
			if changed == "" {
				t.Fatal("missing original access object for refusal", mode)
			}
			if _, err := project(defs); err == nil || !strings.Contains(err.Error(), changed) {
				t.Fatal("changed native access accepted or conflicting resource absent from diagnostic", mode, err)
			}
		})
	}
	accepted, err := apply("account-manager-registration", registration)
	if err != nil {
		t.Fatal("real ordinary KV registration projection/compile/admission/acceptance", err)
	}
	for _, r := range accepted.Resources {
		if d, ok := access[r.ID]; ok && (r.Action != "retain" || r.UID != deploymentplan.Map(d.Object["metadata"])["uid"]) {
			t.Fatal("registration replaced an accepted native access owner", r.ID)
		}
	}
	t.Run("foreign-native-uid", func(t *testing.T) {
		for id := range access {
			old := backend.objects[id]
			foreign := deploymentplan.Clone(old)
			deploymentplan.Map(foreign["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			backend.objects[id] = foreign
			defer func() { backend.objects[id] = old }()
			if err := accepted.Verify(t.Context(), backend); err == nil {
				t.Fatal("registration lost original native UID fence")
			}
			return
		}
	})
}
