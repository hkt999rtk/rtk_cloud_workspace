package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type accountRegistrationProjectionView struct {
	Stack, Image                string
	Original, Baseline, Service deploymentplan.Object
}

func accountRegistrationProjectionFixture(t *testing.T) (deploymentConfig, accountRegistrationProjectionView) {
	t.Helper()
	cfg, p, installed, cert, key, crl, defs := accountRegistrationFixture(t)
	defs, err := renderEnvironmentCreateAccountRegistration(cfg, p, installed, cert, key, crl, defs)
	if err != nil {
		t.Fatal(err)
	}
	o := deploymentplan.Clone(createRecipeObject(t, defs, "Deployment/"+p.Stack+"-account-manager/account-manager"))
	deploymentplan.Map(o["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	baseline := deploymentplan.Clone(o)
	deploymentplan.Map(baseline["metadata"])["uid"] = "01000000-0000-4000-8000-000000000001"
	deploymentplan.Map(baseline["metadata"])["resourceVersion"] = "41"
	service := deploymentplan.Clone(createRecipeObject(t, defs, "Service/"+p.Stack+"-account-manager/account-manager"))
	deploymentplan.Map(service["metadata"])["uid"] = "01000000-0000-4000-8000-000000000002"
	image := "registry.private.example/native@sha256:" + strings.Repeat("9", 64)
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] = accountRegistrationCRLRefreshProfile
	cfg.Values["PKI_MANAGEMENT_IMAGE"] = image
	return cfg, accountRegistrationProjectionView{p.Stack, image, o, baseline, service}
}

func TestAccountRegistrationCRLPlainRenderPreservesCorrectedCohort(t *testing.T) {
	x := newProductExpiryE2EFixture(t, true, true, true)
	x.qualifyInstalledPostCleanupFixture(t)
	f, s := x.f, x.postCleanup
	// The lifecycle fixture intentionally resolves only its narrow architecture.
	// Ordinary full-catalog rendering needs the complete maintained shared
	// settings and the complete existing file-backend OpenBao profile.
	shared, _, _ := completeEnvironmentRenderFixture("dev")
	fileProfile, _, _ := existingFileOpenBaoFixture("dev", true)
	configuration := map[string]string{}
	for key, value := range shared {
		configuration[key] = value
	}
	for key, value := range f.cfg.Values {
		if value != "" {
			configuration[key] = value
		}
	}
	for key, value := range fileProfile {
		if strings.HasPrefix(key, "OPENBAO_") {
			configuration[key] = value
		}
	}
	renderCfg := f.cfg
	renderCfg.Values = configuration
	j := deploymentplan.Journal{Owners: map[string]string{}}
	for _, r := range x.previous.Resources {
		if r.UID != "" {
			j.Owners[r.ID] = r.UID
		}
	}
	source := createNormalTailPostCleanupCorrectionSource{CertIssuerPublic: &s.public, LogIngester: &s.log, ControllerImage: s.controllerImage}
	changes, err := postCleanupCorrectionDefinitions(f.cfg, source, x.previous, j, "apply", nil, deploymentplan.Journal{})
	if err != nil {
		t.Fatal(err)
	}
	// These are isolated native observations derived by the already qualified
	// finite recipes. No parent completion/child receipt is made for this unit
	// render test, and no Kubernetes API or signing operation is invoked.
	retired, err := productPreparationDefinitions(f.cfg, f.store, f.e, f.l, x.installed, true)
	if err != nil {
		t.Fatal("original accepted temporary producer declarations", err)
	}
	retiredIDs := map[string]string{}
	for _, d := range retired {
		retiredIDs[deploymentplan.ID(d.Object)] = deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
	}
	objects := map[string]deploymentplan.Object{}
	for id, o := range f.b.objects {
		if uid, temporary := retiredIDs[id]; temporary {
			if deploymentplan.Map(o["metadata"])["uid"] != uid {
				t.Fatal("temporary producer no longer has its accepted owner", id)
			}
			continue // A post-retirement render-only view, with no changed receipt.
		}
		objects[id] = deploymentplan.Clone(o)
	}
	expected := map[string]deploymentplan.Object{}
	for _, d := range changes {
		id := deploymentplan.ID(d.Object)
		o := deploymentplan.Clone(d.Object)
		if old := objects[id]; old != nil {
			deploymentplan.Map(o["metadata"])["uid"] = deploymentplan.Map(old["metadata"])["uid"]
		} else {
			// A render-only native fixture owner, not an execution receipt.
			deploymentplan.Map(o["metadata"])["uid"] = "04000000-0000-4000-8000-000000000004"
		}
		objects[id] = o
		expected[id] = o
	}
	if len(expected) != 5 {
		t.Fatal("exact three corrected Deployments and public Ingress/policy required")
	}
	operator := map[string]string{
		"PKI_CONTROLLER_IMAGE": s.controllerImage,
	}
	for key, value := range operator {
		if err := f.store.write(filepath.Join("operator", "env", key), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	// The same maintained loader reads durable operator settings, with no
	// hand-injected release aliases or copied live target body.
	snapshot, err := readEnvironmentDeploymentSnapshot(renderCfg, f.store)
	if err != nil {
		t.Fatal(err)
	}
	operator = snapshot.Operator
	release, err := accountRegistrationCRLRefreshRelease(renderCfg, x.program.Release, operator)
	if err != nil {
		t.Fatal(err)
	}
	f.b.objects = objects
	live, err := f.runtime.Backend.Inventory(t.Context(), f.p.Stack)
	if err != nil {
		t.Fatal("maintained native inventory contract", err)
	}
	// Plain ordinary observation deliberately has no create/handoff context.
	bindings, err := observeEnvironmentTargetBindings(context.Background(), renderCfg, release, "upgrade", live)
	if err != nil {
		t.Fatal("plain current identity observation", err)
	}
	var target []deploymentplan.Definition
	err = withEnvironmentRenderInputs(renderCfg, f.store, release, snapshot, func(values map[string]string) error {
		var e error
		target, e = renderObservedCorrectedTargetsForAccountCRLTest(values, bindings, release, f.l.SourceDefinitions)
		return e
	})
	if err != nil {
		t.Fatal("plain ordinary rendering", err)
	}
	for id, wanted := range expected {
		got := createRecipeObject(t, target, id)
		if wanted["kind"] == "Ingress" {
			// The scaled fixture changes the donor rule hostname before Program
			// freeze but leaves its illustrative TLS.hosts behind. Ordinary
			// rendering must use this cohort's selected public hostname for both;
			// never preserve the unrelated donor's TLS hostname as target intent.
			wanted = deploymentplan.Clone(wanted)
			tls := certIssuerObjectList(deploymentplan.Map(wanted["spec"])["tls"])
			if len(tls) != 1 {
				t.Fatal("one original public passthrough TLS declaration required")
			}
			deploymentplan.Map(tls[0])["hosts"] = []any{lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(renderCfg)).PublicHost}
		}
		if wanted["kind"] == "Deployment" {
			if bindings.Owners[id] != deploymentplan.Map(wanted["metadata"])["uid"] {
				t.Error("ordinary observation changed original corrected owner", id)
			}
			wp, gp := environmentPKIPod(wanted), environmentPKIPod(got)
			claims := func(p deploymentplan.Object) map[string]string {
				out := map[string]string{}
				for _, raw := range certIssuerObjectList(p["volumes"]) {
					v := deploymentplan.Map(raw)
					if claim := deploymentplan.String(deploymentplan.Map(v["persistentVolumeClaim"])["claimName"]); claim != "" {
						out[deploymentplan.String(v["name"])] = claim
					}
				}
				return out
			}
			if deploymentplan.Digest(claims(wp)) != deploymentplan.Digest(claims(gp)) {
				t.Error("ordinary target changed original private PVC bindings", id)
			}
			for _, raw := range certIssuerObjectList(wp["containers"]) {
				c := deploymentplan.Map(raw)
				name := deploymentplan.String(c["name"])
				actual := managedNamedField(gp["containers"], name)
				if c["image"] != actual["image"] {
					t.Error("ordinary target changed retained corrected image", id, name)
				}
				for _, raw := range certIssuerObjectList(c["env"]) {
					e := deploymentplan.Map(raw)
					key := deploymentplan.String(e["name"])
					if !environmentIdentitySetting(key) || environmentControllerPolicySetting(key) {
						continue
					}
					actualEntry := managedNamedField(actual["env"], key)
					wantOne := deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "env": []any{e}}}}}
					haveOne := deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "env": []any{actualEntry}}}}}
					if !deploymentplan.Matches(wantOne, haveOne) {
						t.Errorf("ordinary target lost identity setting %s %s %s", id, name, key)
					}
				}
				for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
					mount := deploymentplan.Map(raw)
					mountName := deploymentplan.String(mount["name"])
					actualMount := managedNamedField(actual["volumeMounts"], mountName)
					wantOne := deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "volumeMounts": []any{mount}}}}}
					haveOne := deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "volumeMounts": []any{actualMount}}}}}
					if !deploymentplan.Matches(wantOne, haveOne) {
						t.Error("ordinary target lost private mount", id, name, mountName)
					}
				}
				if !deploymentplan.Matches(deploymentplan.Object{"securityContext": c["securityContext"]}, deploymentplan.Object{"securityContext": actual["securityContext"]}) {
					t.Error("ordinary target changed declared container security", id, name)
				}

			}
			if deploymentplan.Map(wanted["metadata"])["name"] == "video-cloud-logingester" {
				app := managedNamedField(gp["containers"], "app")
				if deploymentplan.Map(app["securityContext"])["runAsUser"] != float64(10001) || deploymentplan.Map(app["securityContext"])["runAsGroup"] != float64(10001) || deploymentplan.Map(got["spec"])["replicas"] != float64(1) || deploymentplan.Map(deploymentplan.Map(got["spec"])["strategy"])["type"] != "Recreate" {
					t.Error("ordinary target reverted LogIngester single-writer correction")
				}
			}
		} else if !deploymentplan.Matches(deploymentplan.Object{"kind": wanted["kind"], "spec": wanted["spec"]}, got) || !deploymentplan.Matches(deploymentplan.Object{"kind": got["kind"], "spec": got["spec"]}, wanted) {
			t.Error("ordinary target changed exact public route or ingress policy", id)
			t.Log("public spec differing fields", accountRegistrationRenderDiffPaths(wanted["spec"], got["spec"], "spec"))
			if wanted["kind"] == "Ingress" {
				t.Logf("public route expected=%s target=%s", mustProviderRecoveryJSON(wanted["spec"]), mustProviderRecoveryJSON(got["spec"]))
			}
		}
		if wanted["kind"] == "Ingress" {
			for _, key := range []string{"nginx.ingress.kubernetes.io/ssl-passthrough", "nginx.ingress.kubernetes.io/backend-protocol"} {
				if deploymentplan.Map(deploymentplan.Map(wanted["metadata"])["annotations"])[key] != deploymentplan.Map(deploymentplan.Map(got["metadata"])["annotations"])[key] {
					t.Error("ordinary target changed public end-to-end mTLS", key)
				}
			}
		}
	}
	// This scaled lifecycle has no owned AM/socket. The separate ordinary
	// owned-AM roundtrip below proves its loaded optional profile. This test
	// proves the five real corrected bodies survive ordinary target rendering.
	// It does not qualify unrelated missing full-platform fixture identities.
}

func accountRegistrationRenderDiffPaths(want, have any, prefix string) []string {
	var out []string
	wm, wok := want.(map[string]any)
	hm, hok := have.(map[string]any)
	if wok && hok {
		keys := map[string]string{}
		for k := range wm {
			keys[k] = ""
		}
		for k := range hm {
			keys[k] = ""
		}
		for _, k := range sortedMapKeys(keys) {
			out = append(out, accountRegistrationRenderDiffPaths(wm[k], hm[k], prefix+"."+k)...)
		}
		return out
	}
	wl, wok := want.([]any)
	hl, hok := have.([]any)
	if wok && hok && len(wl) == len(hl) {
		for i := range wl {
			out = append(out, accountRegistrationRenderDiffPaths(wl[i], hl[i], fmt.Sprintf("%s[%d]", prefix, i))...)
		}
		return out
	}
	if deploymentplan.Digest(want) != deploymentplan.Digest(have) {
		out = append(out, prefix)
	}
	return out
}

// Use the same ordinary component producers and binders, scoped to the five
// corrected targets. Frozen Services are read-only dependency declarations.
// This intentionally does not synthesize unrelated API/Factory/AM identities.
func renderObservedCorrectedTargetsForAccountCRLTest(values map[string]string, bindings environmentIdentityBindings, release environmentRelease, source []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	var definitions []deploymentplan.Definition
	for _, b := range bindings.Pods {
		if b.Name != "certissuer" && b.Name != "pki-controller" && b.Name != "video-cloud-logingester" {
			continue
		}
		var object deploymentplan.Object
		var err error
		if b.Name == "video-cloud-logingester" {
			for _, worker := range k8sAuxiliaryWorkloads() {
				if worker.Name != b.Name {
					continue
				}
				defs, e := decodeEnvironmentCreateManifests(lkeVideoCloudAuxiliaryDeploymentManifest(values, worker))
				if e != nil || len(defs) != 1 {
					return nil, e
				}
				object = defs[0].Object
			}
		} else {
			object, err = renderManagedEnvironmentComponent(values, b)
		}
		if err != nil {
			return nil, err
		}
		if object == nil {
			return nil, errors.New("maintained corrected component unavailable")
		}
		if err = applyEnvironmentPodBindings(object, bindings, release); err != nil {
			return nil, err
		}
		if err = applyEnvironmentAuthRole(values, object); err != nil {
			return nil, err
		}
		if err = applyEnvironmentIssuerRuntime(values, object, b); err != nil {
			return nil, err
		}
		if b.Name == "pki-controller" {
			if err = applyControllerIdentityPreparation(object, release); err != nil {
				return nil, err
			}
			controllerValues := values
			for _, other := range bindings.Pods {
				if other.Component == "managed-mqtt" || other.Component == "managed-openbao" {
					ids, e := managedTransportServiceConsumerIDs(strings.Split(values["PKI_REQUIRED_CONSUMERS_SERVICE"], ","))
					if e != nil {
						return nil, e
					}
					controllerValues = appendMap(values, map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": strings.Join(ids, ",")})
					break
				}
			}
			if err = applyEnvironmentControllerPolicy(controllerValues, object); err != nil {
				return nil, err
			}
			if bindings.BootstrapCleanup != nil {
				deps, e := applyEnvironmentBootstrapCleanup(values, object, *bindings.BootstrapCleanup)
				if e != nil {
					return nil, e
				}
				definitions = append(definitions, deps...)
			}
		}
		canonicalEnvironmentEmptyEnvLiterals(object)
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "workload"})
	}
	for _, d := range source {
		if d.Object["kind"] == "Service" {
			definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(d.Object), Phase: "configuration"})
		}
	}
	for _, d := range definitions {
		if err := applyEnvironmentRegistryIssuerService(d.Object, bindings); err != nil {
			return nil, err
		}
	}
	ingress, err := decodeEnvironmentCreateManifests(lkeCertIssuerPassthroughIngressManifest(values, lkeResolveCertIssuerTLSConfig(values).PublicRoute()))
	if err != nil {
		return nil, err
	}
	for _, d := range ingress {
		deploymentplan.Map(d.Object["spec"])["ingressClassName"] = environmentIngressClass(values)
		definitions = append(definitions, d)
	}
	definitions, err = bindEnvironmentCertIssuerPublicTransport(values, definitions)
	if err != nil {
		return nil, err
	}
	if err = deriveEnvironmentLogMQTTTransport(values, definitions); err != nil {
		return nil, err
	}
	if err = pinEnvironmentResourceImages(definitions, release); err != nil {
		return nil, err
	}
	if err = validateEnvironmentCertIssuerPublicTransport(values, definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func TestAccountRegistrationCRLHistoricalCompletionPrecedesOptionalOverrides(t *testing.T) {
	f := newCreateOpenBaoACKCompletionFixture(t)
	if err := f.publish(t); err != nil {
		t.Fatal(err)
	}
	live := createOpenBaoACKInventoryFixture(t, f)
	operator := map[string]string{
		"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile,
		"PKI_MANAGEMENT_IMAGE": "private.registry.example/manager@sha256:" + strings.Repeat("9", 64),
		"PKI_CONTROLLER_IMAGE": "private.registry.example/controller@sha256:" + strings.Repeat("8", 64),
	}
	before := createOpenBaoACKStoreSnapshot(t, f.image.store)
	originalSHA := deploymentplan.Digest(f.original)
	selected, err := effectiveEnvironmentDeploymentReleaseForInventory(context.Background(), f.image.cfg, f.image.store, "upgrade", f.original, operator, live, f.image.command(t))
	if err != nil || selected.Images["LKE_OPENBAO_PKI_IMAGE"] != f.ack.Image || selected.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference != operator["PKI_MANAGEMENT_IMAGE"] || selected.Images["LKE_PKI_CONTROLLER_IMAGE"].Reference != operator["PKI_CONTROLLER_IMAGE"] || deploymentplan.Digest(f.original) != originalSHA {
		t.Fatal("literal completed history must authenticate before explicit ordinary dev selection", err)
	}
	// The historical reader stays strict. The same new-role release supplied
	// as historical provenance must still be refused rather than normalized.
	wrong, err := accountRegistrationCRLRefreshRelease(f.image.cfg, f.original, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effectiveCreateOpenBaoACKReleaseForInventoryWithCommand(t.Context(), f.image.cfg, f.image.store, wrong, live, f.image.command(t)); err == nil {
		t.Fatal("historical release mismatch was relaxed")
	}
	deploymentplan.Map(live[3]["metadata"])["uid"] = "foreign"
	if _, err := effectiveEnvironmentDeploymentReleaseForInventory(t.Context(), f.image.cfg, f.image.store, "upgrade", f.original, operator, live, f.image.command(t)); err == nil {
		t.Fatal("new ordinary overrides bypassed original native owner validation")
	}
	if deploymentplan.Digest(before) != deploymentplan.Digest(createOpenBaoACKStoreSnapshot(t, f.image.store)) {
		t.Fatal("release resolution changed historical state")
	}
}

func TestAccountRegistrationCRLOperatorProjectionIsOptionalAndScoped(t *testing.T) {
	for _, mode := range []string{"legacy", "dev", "missing-image", "missing-profile", "unknown-profile", "mutable-image", "staging", "prod", "foreign-repository"} {
		t.Run(mode, func(t *testing.T) {
			cfg, review := accountRegistrationProjectionFixture(t)
			_, _, release := completeEnvironmentRenderFixture("dev")
			operator := map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": review.Image}
			switch mode {
			case "legacy":
				operator = map[string]string{}
			case "missing-image":
				delete(operator, "PKI_MANAGEMENT_IMAGE")
			case "missing-profile":
				delete(operator, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE")
			case "unknown-profile":
				operator["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] = "unknown"
			case "mutable-image":
				operator["PKI_MANAGEMENT_IMAGE"] = "native:latest"
			case "staging", "prod":
				cfg.Environment = mode
			case "foreign-repository":
				operator["PKI_MANAGEMENT_IMAGE"] = "foreign/package@sha256:" + strings.Repeat("9", 64)
			}
			before := deploymentplan.Digest(release)
			selected, err := accountRegistrationCRLRefreshRelease(cfg, release, operator)
			want := mode == "legacy" || mode == "dev" || mode == "foreign-repository"
			if (err == nil) != want {
				t.Fatal("operator selection outcome differs", mode, err)
			}
			if deploymentplan.Digest(release) != before {
				t.Fatal("operator projection changed original release")
			}
			if !want {
				return
			}
			if mode == "legacy" && deploymentplan.Digest(selected) != before {
				t.Fatal("absent profile changed legacy release")
			}
			if mode == "dev" && (len(selected.Images) != len(release.Images)+1 || selected.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference != review.Image || selected.Images["LKE_VIDEO_CLOUD_IMAGE"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"]) {
				t.Fatal("management image escaped its one optional role")
			}
			values := map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": "ambient", "PKI_MANAGEMENT_IMAGE": "ambient"}
			if err := projectAccountRegistrationCRLRefreshInputs(cfg.Environment, operator, values); err != nil {
				t.Fatal(err)
			}
			if mode == "legacy" && (values["PKI_MANAGEMENT_IMAGE"] != "" || values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] != "") {
				t.Fatal("ambient profile survived an absent selection")
			}
		})
	}
}

func TestAccountRegistrationCRLProfileRemovalRefusesOrphanedRuntime(t *testing.T) {
	cfg, review := accountRegistrationProjectionFixture(t)
	values := map[string]string{"CLOUD_ENV_NAME": cfg.Environment, "CLOUD_STACK_NAME": review.Stack}
	legacy := []deploymentplan.Definition{{Phase: "workload", Object: review.Original}}
	if got, err := applyAccountRegistrationCRLRefreshProfile(values, legacy); err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(legacy) {
		t.Fatal("absent optional profile changed legacy rendering", err)
	}
	d, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, review.Original, review.Image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyAccountRegistrationCRLRefreshProfile(values, []deploymentplan.Definition{d}); err == nil {
		t.Fatal("removed selection would leave a legacy binary serving the managed export path")
	}
	values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] = accountRegistrationCRLRefreshProfile
	values["PKI_MANAGEMENT_IMAGE"] = review.Image
	if got, err := applyAccountRegistrationCRLRefreshProfile(values, []deploymentplan.Definition{d}); err != nil || deploymentplan.Digest(got[0].Object) != deploymentplan.Digest(d.Object) {
		t.Fatal("retained explicit profile changed its idempotent target", err)
	}
}

func TestAccountRegistrationCRLOrdinaryObserverRendererRoundTrip(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "formal-old-owner", true: "updated-owner"}[after], func(t *testing.T) {
			cfg, review := accountRegistrationProjectionFixture(t)
			snapshotCfg, store, _, _ := maintainedDeploymentSnapshotFixture(t, "dev")
			for key, value := range map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": review.Image} {
				if err := store.write(filepath.Join("operator", "env", key), []byte(value), false); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := readEnvironmentDeploymentSnapshot(snapshotCfg, store)
			if err != nil {
				t.Fatal("durable ordinary AM configuration loader", err)
			}
			env, bindings, release := completeEnvironmentRenderFixture("dev")
			selected, err := accountRegistrationCRLRefreshRelease(cfg, release, snapshot.Operator)
			if err != nil {
				t.Fatal(err)
			}
			current := deploymentplan.Clone(review.Baseline)
			if after {
				d, e := environmentAccountRegistrationCRLRefreshDefinition(cfg, current, review.Image)
				if e != nil {
					t.Fatal(e)
				}
				current = d.Object
			}
			before := deploymentplan.Digest(current)
			service := review.Service
			observed, err := observeEnvironmentBindings("dev", selected, []deploymentplan.Object{current, service})
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, p := range observed.Pods {
				if p.Name == "account-manager" {
					for _, c := range p.Containers {
						if c.Name == "pkimanagement" {
							found++
							if c.ImageKey != "LKE_PKI_MANAGEMENT_IMAGE" {
								t.Fatal("management image lost its selected role")
							}
						}
					}
				}
			}
			if found != 1 {
				t.Fatal("owned management sidecar not observed")
			}
			bindings.Pods = append(bindings.Pods, observed.Pods...)
			for id, uid := range observed.Owners {
				bindings.Owners[id] = uid
			}
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "true", "true", "9444"
			if err := projectAccountRegistrationCRLRefreshInputs("dev", snapshot.Operator, env); err != nil {
				t.Fatal(err)
			}
			// Keep unrelated previously corrected image roles literal in the
			// supplied release; management selection cannot overwrite them.
			for key, value := range selected.Images {
				env[key] = value.Reference
			}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			defs, err := renderEnvironmentResources(env, bindings, selected)
			if err != nil {
				t.Fatal(err)
			}
			o := createRecipeObject(t, defs, deploymentplan.ID(current))
			app := managedNamedField(environmentPKIPod(o)["containers"], "app")
			manager := managedNamedField(environmentPKIPod(o)["containers"], "pkimanagement")
			if manager["image"] != review.Image || mqttFoundationEnv(manager, accountRegistrationCRLExportEnv) != accountRegistrationCRLBundlePath || mqttFoundationEnv(app, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL") != accountRegistrationCRLBundlePath {
				t.Fatal("ordinary target dropped the configured managed CRL path/image")
			}
			if deploymentplan.Digest(current) != before {
				t.Fatal("observation or render changed existing native owner")
			}
			if !environmentManagedImageRoleAllowed("existing-workload", "account-manager", "pkimanagement", 1, "LKE_PKI_MANAGEMENT_IMAGE") || environmentManagedImageRoleAllowed("existing-workload", "account-manager", "app", 0, "LKE_PKI_MANAGEMENT_IMAGE") || environmentManagedImageRoleAllowed("pki-controller", "pki-controller", "pki-controller", 0, "LKE_PKI_MANAGEMENT_IMAGE") {
				t.Fatal("management role escaped the selected sidecar")
			}
			bad := deploymentplan.Clone(current)
			managedNamedField(environmentPKIPod(bad)["containers"], "pkimanagement")["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("8", 64)
			if _, err := observeEnvironmentBindings("dev", selected, []deploymentplan.Object{bad}); err == nil {
				t.Fatal("unknown management image was adopted")
			}
		})
	}
}
