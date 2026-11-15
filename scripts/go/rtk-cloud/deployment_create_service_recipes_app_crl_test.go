package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestApplicationCRLInitializationUsesRenderedManifestAndOwner(t *testing.T) {
	cfg, p, r, s := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cm := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "selected-crls", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"custom.json": environmentCreateServiceCRLManifest(s.Root, "/custom/state/root.json", s.Intermediate)}}}
	owner := environmentCreateApplicationOwners()[4]
	workload := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{environmentCreateServiceVolume("trust", "configMap", "selected-crls")}, "containers": []any{deploymentplan.Object{"name": owner.Container, "env": []any{deploymentplan.Object{"name": "FACTORY_ENROLL_CERT_ISSUER_SERVER_CRL_MANIFEST", "value": "/some/trust/custom.json"}}, "volumeMounts": []any{environmentCreateServiceMount("trust", "/some/trust", true)}}}}}}}}
	podSettings := environmentPKIPod(workload.Object)
	podSettings["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "runAsGroup": float64(54321), "fsGroup": float64(54321)}
	managedNamedField(podSettings["containers"], owner.Container)["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "readOnlyRootFilesystem": true}
	defs := []deploymentplan.Definition{cm, workload}
	valid := []environmentCreateRegisteredIssuer{s.Root, s.Intermediate}
	jobs, err := renderEnvironmentCreateApplicationCRLInitialization(context.Background(), cfg, p, r, s, defs, valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs", len(jobs))
	}
	pod := environmentPKIPod(jobs[0].Object)
	c := managedNamedField(pod["containers"], "enroll")
	if deploymentplan.Map(pod["securityContext"])["runAsUser"] != float64(12345) || deploymentplan.Map(pod["securityContext"])["fsGroup"] != float64(54321) || pod["initContainers"] != nil {
		t.Fatal("job did not preserve environment workload security settings")
	}

	cmd := certIssuerObjectList(c["command"])
	if len(cmd) != 8 || cmd[2] != "/custom/state/root.json" || cmd[3] != s.Root.ID || cmd[5] != "/custom/state/root-"+s.Intermediate.ID+".json" {
		t.Fatal("not derived from manifest", cmd)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), jobs)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range projected {
		if j.Object["kind"] == "Job" {
			found = true
			pod = environmentPKIPod(j.Object)
			c = managedNamedField(pod["containers"], "enroll")
			if pod["serviceAccountName"] != owner.Name || !strings.HasSuffix(mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX"), "/"+owner.Name) {
				t.Fatal("wrong owner")
			}
		}
	}
	if !found {
		t.Fatal("job missing")
	}
	// One shared manifest must still initialize every isolated owner's scope.
	all := []deploymentplan.Definition{cm}
	for _, candidate := range environmentCreateApplicationOwners() {
		clone := deploymentplan.Clone(workload.Object)
		deploymentplan.Map(clone["metadata"])["name"] = candidate.Name
		pod := environmentPKIPod(clone)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		container["name"] = candidate.Container
		all = append(all, deploymentplan.Definition{Object: clone})
	}
	allJobs, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, r, s, all, valid)
	if err != nil || len(allJobs) != len(environmentCreateApplicationOwners()) {
		t.Fatalf("owner coverage %d: %v", len(allJobs), err)
	}
	allProjected, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), allJobs)
	if err != nil {
		t.Fatal(err)
	}
	ownersSeen := map[string]bool{}
	jobNames := map[string]bool{}
	for _, job := range allProjected {
		if job.Object["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(job.Object)
		name := deploymentplan.String(deploymentplan.Map(job.Object["metadata"])["name"])
		if len(name) > 63 || jobNames[name] {
			t.Fatal("application CRL Job name is too long or duplicated", name)
		}
		jobNames[name] = true
		c := managedNamedField(pod["containers"], "enroll")
		prefix := mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
		parts := strings.Split(prefix, "/")
		if len(parts) != 3 || pod["serviceAccountName"] != parts[2] {
			t.Fatal("cross-owner projection", prefix)
		}
		ownersSeen[parts[2]] = true
	}
	if len(ownersSeen) != len(environmentCreateApplicationOwners()) {
		t.Fatal("missing owner projections", ownersSeen)
	}
	// The native CLI accepts at most four entries per invocation.
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(environmentCreateServiceCRLManifest(s.Root, "/custom/state/root.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]any, 5)
	for n := range entries {
		entries[n] = map[string]any{"issuer": manifest[0]["issuer"], "state_path": fmt.Sprintf("/custom/state/%d.json", n)}
	}
	many, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	multi := []deploymentplan.Definition{{Object: deploymentplan.Clone(cm.Object)}, {Object: deploymentplan.Clone(workload.Object)}}
	deploymentplan.Map(multi[0].Object["data"])["custom.json"] = string(many)
	multipleJobs, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, r, s, multi, valid)
	if err != nil || len(multipleJobs) != 2 {
		t.Fatalf("five entries must become two jobs: %v", err)
	}
	for _, job := range multipleJobs {
		cmd := certIssuerObjectList(managedNamedField(environmentPKIPod(job.Object)["containers"], "enroll")["command"])
		if len(cmd) > 14 {
			t.Fatal("native command exceeds limit", len(cmd))
		}
	}
	for _, test := range []struct {
		name   string
		mutate func([]deploymentplan.Definition)
	}{
		{"unknown-owner", func(d []deploymentplan.Definition) {
			deploymentplan.Map(d[1].Object["metadata"])["name"] = "unrecognized-workload"
		}},
		{"missing", func(d []deploymentplan.Definition) { delete(deploymentplan.Map(d[0].Object["data"]), "custom.json") }},
		{"foreign-issuer", func(d []deploymentplan.Definition) {
			deploymentplan.Map(d[0].Object["data"])["custom.json"] = strings.ReplaceAll(environmentCreateServiceCRLManifest(s.Root, "/custom/state/root.json", s.Intermediate), s.Root.ID, "foreign")
		}},
		{"wrong-domain", func(d []deploymentplan.Definition) {
			deploymentplan.Map(d[0].Object["data"])["custom.json"] = strings.ReplaceAll(environmentCreateServiceCRLManifest(s.Root, "/custom/state/root.json", s.Intermediate), `"trust_domain":"service"`, `"trust_domain":"mqtt"`)
		}},
		{"ambiguous-mount", func(d []deploymentplan.Definition) {
			pod := environmentPKIPod(d[1].Object)
			c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			c["volumeMounts"] = append(c["volumeMounts"].([]any), environmentCreateServiceMount("trust", "/some/trust", true))
		}},
		{"invalid-path", func(d []deploymentplan.Definition) {
			deploymentplan.Map(d[0].Object["data"])["custom.json"] = environmentCreateServiceCRLManifest(s.Root, "../escape", s.Intermediate)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			x := []deploymentplan.Definition{{Object: deploymentplan.Clone(cm.Object)}, {Object: deploymentplan.Clone(workload.Object)}}
			test.mutate(x)
			if _, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, r, s, x, valid); err == nil {
				t.Fatal("accepted invalid rendered manifest")
			}
		})
	}
}

func TestApplicationCRLJobNamesAreBoundedUniqueAndPreserveOwners(t *testing.T) {
	_, p, _, service := createServiceRecipeFixture(t, "dev")
	p.Stack = "non-default-environment-stack"
	valid := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	names := map[string]bool{}
	owners := environmentCreateApplicationOwners()
	owners = append(owners,
		environmentCreateApplicationOwner{Name: strings.Repeat("a", 62) + "b", Claim: "first-original-claim", ClientState: "/first/original/client.json"},
		environmentCreateApplicationOwner{Name: strings.Repeat("a", 62) + "c", Claim: "second-original-claim", ClientState: "/second/original/client.json"})
	for _, owner := range owners {
		for _, batch := range []int{0, 1, 999} {
			key := environmentCreateApplicationCRLJobKey(owner.Name, batch)
			if key != environmentCreateApplicationCRLJobKey(owner.Name, batch) || len(key) > 39 {
				t.Fatal("application CRL key is not stable and bounded")
			}
			job := environmentCreateServiceEnrollmentJob(p, p.Stack+"-video-cloud", key, owner.Claim, service, []any{"/app/serviceidentity-bootstrap", "initialize-public-crl"}, map[string]string{"PKI_CLIENT_IDENTITY_STATE": owner.ClientState}, false)
			meta := deploymentplan.Map(job.Object["metadata"])
			name := deploymentplan.String(meta["name"])
			if len(name) > 63 || !valid.MatchString(name) || names[name] || meta["namespace"] != p.Stack+"-video-cloud" || deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] != p.Stack || !strings.HasSuffix(name, strings.ReplaceAll(service.BootstrapSessionID, "-", "")[:12]) {
				t.Fatal("invalid, colliding or foreign Job name", name)
			}
			names[name] = true
			pod := environmentPKIPod(job.Object)
			c := managedNamedField(pod["containers"], "enroll")
			if mqttFoundationEnv(c, "PKI_CLIENT_IDENTITY_STATE") != owner.ClientState {
				t.Fatal("original identity selection changed", owner.Name)
			}
			claim := false
			for _, raw := range certIssuerObjectList(pod["volumes"]) {
				v := deploymentplan.Map(raw)
				if v["name"] == "service-state" && deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] == owner.Claim {
					claim = true
				}
			}
			if !claim {
				t.Fatal("original claim changed", owner.Name)
			}
		}
	}
}

func TestApplicationCRLInitializationAgainstFinalRenderedWorkloads(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, environment)
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			for _, d := range initial {
				if d.Object["kind"] != "Deployment" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				pod["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "runAsGroup": float64(54321), "fsGroup": float64(54321)}
				for _, raw := range certIssuerObjectList(pod["containers"]) {
					deploymentplan.Map(raw)["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "readOnlyRootFilesystem": true}
				}
			}
			final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise the final, effective environment workload security settings.
			for _, d := range final {
				if d.Object["kind"] != "Deployment" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				pod["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "runAsGroup": float64(54321), "fsGroup": float64(54321)}
				for _, raw := range certIssuerObjectList(pod["containers"]) {
					deploymentplan.Map(raw)["securityContext"] = deploymentplan.Object{"runAsUser": float64(12345), "readOnlyRootFilesystem": true}
				}
			}
			serviceCM := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"outbound-crls.json": environmentCreateServiceCRLManifest(service.Root, environmentCreateCoreCRLPath("outbound"), service.Intermediate)}}}
			final = append(final, serviceCM)
			allowed := []environmentCreateRegisteredIssuer{service.Root, service.Intermediate, roots["mqtt"], intermediates["mqtt"], roots["device"], roots["app"], intermediates["app"]}
			jobs, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, runtime, service, final, allowed)
			if err != nil {
				t.Fatal("final rendered workloads rejected:", err)
			}
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
			if err != nil {
				t.Fatal(err)
			}
			// Recovery receives the already-projected desired bodies from its
			// accepted historical plan; it must derive the same source coverage.
			projectedWorkloads, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, strings.Repeat("b", 64), final)
			if err != nil {
				t.Fatal("project workload fixture:", err)
			}
			recoveryJobs, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, runtime, service, projectedWorkloads, allowed)
			if err != nil {
				t.Fatal("recover projected workload manifests:", err)
			}
			if len(recoveryJobs) != len(jobs) {
				t.Fatalf("projected source changed job count: %d != %d", len(recoveryJobs), len(jobs))
			}
			projected, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, strings.Repeat("b", 64), jobs)
			if err != nil {
				t.Fatal(err)
			}
			byOwner := map[string]map[string]bool{}
			for _, job := range projected {
				if job.Object["kind"] != "Job" {
					continue
				}
				pod := environmentPKIPod(job.Object)
				c := managedNamedField(pod["containers"], "enroll")
				prefix := mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
				parts := strings.Split(prefix, "/")
				if len(parts) != 3 {
					t.Fatal("unscoped", prefix)
				}
				owner := parts[2]
				if pod["serviceAccountName"] != owner || deploymentplan.Map(pod["securityContext"])["runAsUser"] != float64(12345) || deploymentplan.Map(pod["securityContext"])["fsGroup"] != float64(54321) {
					t.Fatalf("owner or environment security context changed %s: %#v SA=%v", owner, pod["securityContext"], pod["serviceAccountName"])
				}
				if byOwner[owner] == nil {
					byOwner[owner] = map[string]bool{}
				}
				cmd := certIssuerObjectList(c["command"])
				for n := 2; n < len(cmd); n += 3 {
					path := deploymentplan.String(cmd[n])
					byOwner[owner][path] = true
					writable := false
					for _, v := range certIssuerObjectList(c["volumeMounts"]) {
						m := deploymentplan.Map(v)
						root := deploymentplan.String(m["mountPath"])
						if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
							volume := managedNamedField(pod["volumes"], deploymentplan.String(m["name"]))
							if deploymentplan.Map(volume["emptyDir"])["medium"] == "Memory" && m["readOnly"] != true {
								writable = true
							}
						}
					}
					if !writable {
						t.Fatalf("%s missing writable memory mount for %s", owner, path)
					}
				}
			}
			want := map[string][]string{"factoryenroll": {"/var/lib/pki-service/private/crl/outbound-root.json"}, "video-cloud-api": {"/var/lib/pki-service/private/crl/outbound-root.json", "/var/lib/mqtt-trust/root-crl.json"}, "video-cloud-api-pki": {"/var/lib/pki-service/private/crl/outbound-root.json", "/run/pki-state/device-root-crl.json"}, "video-cloud-api-app-pki": {"/var/lib/pki-service/private/crl/outbound-root.json", "/run/pki-state/app-root-crl.json"}, "video-cloud-logingester": {"/var/lib/pki-service/private/crl/outbound-root.json", "/var/lib/mqtt-trust/root-crl.json"}, "mqtt-pki": {"/var/lib/pki-service/private/crl/outbound-root.json", "/run/pki-state/device-crl.json", "/run/pki-state/app-crl.json"}}
			for owner, paths := range want {
				for _, path := range paths {
					if !byOwner[owner][path] {
						t.Errorf("%s missing rendered CRL %s", owner, path)
					}
				}
			}
		})
	}
}

func TestProductMQTTCRLInitializationKeepsEachOriginalOwnerScope(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, r, s := createServiceRecipeFixture(t, environment)
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"], cfg.Values["SHADOW_WORKER_REGISTRATION_ENABLED"] = "true", "true"
			var err error
			p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cm := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "mqtt-crls", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"crls.json": environmentCreateServiceCRLManifest(s.Root, "/var/lib/mqtt-trust/root-crl.json", s.Intermediate)}}}
			defs := []deploymentplan.Definition{cm}
			for _, name := range []string{"video-cloud-mqttfoundation", "video-cloud-shadowworker"} {
				c := deploymentplan.Object{"name": strings.TrimPrefix(name, "video-cloud-"), "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "/run/trust/crls.json"}), "volumeMounts": []any{environmentCreateServiceMount("trust", "/run/trust", true)}}
				defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}, "volumes": []any{environmentCreateServiceVolume("trust", "configMap", "mqtt-crls")}, "securityContext": deploymentplan.Object{"runAsUser": float64(12345), "fsGroup": float64(54321)}}}}}})
			}
			before := deploymentplan.Digest(defs)
			jobs, err := renderEnvironmentCreateApplicationCRLInitialization(t.Context(), cfg, p, r, s, defs, []environmentCreateRegisteredIssuer{s.Root, s.Intermediate})
			if err != nil || len(jobs) != 2 {
				t.Fatalf("product trust initializers missing: jobs=%d err=%v", len(jobs), err)
			}
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
			if err != nil {
				t.Fatal(err)
			}
			projected, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, strings.Repeat("b", 64), jobs)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, d := range projected {
				if d.Object["kind"] != "Job" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				c := managedNamedField(pod["containers"], "enroll")
				prefix := mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
				owner := strings.Split(prefix, "/")[2]
				seen[owner] = true
				if pod["serviceAccountName"] != owner || deploymentplan.Map(pod["securityContext"])["fsGroup"] != float64(54321) {
					t.Fatal("owner/security changed")
				}
				args := certIssuerObjectList(c["command"])
				if len(args) != 8 || args[1] != "initialize-public-crl" || args[2] != "/var/lib/mqtt-trust/root-crl.json" {
					t.Fatal("not original public CRL", args)
				}
				if managedNamedField(pod["volumes"], "bootstrap") != nil {
					t.Fatal("initializer can enroll identity")
				}
			}
			if !seen["video-cloud-mqttfoundation"] || !seen["video-cloud-shadowworker"] || len(seen) != 2 || before != deploymentplan.Digest(defs) {
				t.Fatal("isolated owner scope lost")
			}
			productJobs, err := renderCreateProductPublicCRLs(t.Context(), cfg, p, r, s, defs)
			if err != nil || deploymentplan.Digest(productJobs) != deploymentplan.Digest(jobs) {
				t.Fatal("fresh/recovery producer differs", err)
			}
			r.ServiceCeremony = &s
			stages := []string{}
			if err := initializeCreateProductPublicCRLs(t.Context(), cfg, &environmentCreatePlan{Bootstrap: p}, r, defs, func(_ context.Context, stage string, got []deploymentplan.Definition) error {
				stages = append(stages, stage)
				if deploymentplan.Digest(got) != deploymentplan.Digest(jobs) {
					t.Fatal("fresh producer changed native commands")
				}
				return nil
			}); err != nil || len(stages) != 1 || stages[0] != productPublicCRLStage {
				t.Fatal("fresh product trust not prepared", err)
			}
		})
	}
}
