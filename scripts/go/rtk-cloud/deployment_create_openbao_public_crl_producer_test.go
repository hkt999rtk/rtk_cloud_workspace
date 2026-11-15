package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoPublicCRLProducerKeepsRuntimeImageAndOriginalOwnerPurposes(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	root, intermediate := service.Root, service.Intermediate
	root.Domain, intermediate.Domain = "openbao_tls", "openbao_tls"
	root.Document, intermediate.Document = deploymentplan.Clone(root.Document), deploymentplan.Clone(intermediate.Document)
	root.Document["trust_domain"], intermediate.Document["trust_domain"] = "openbao_tls", "openbao_tls"
	core, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	core, err = applyEnvironmentCreateManagedOpenBaoConsumers(p, service, map[string]environmentCreateRegisteredIssuer{"openbao_tls": root}, map[string]environmentCreateRegisteredIssuer{"openbao_tls": intermediate}, core)
	if err != nil {
		t.Fatal(err)
	}
	tool := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	before, runtimeImage := deploymentplan.Digest(core), p.Image
	jobs, err := renderEnvironmentCreateOpenBaoPublicCRLDefinitions(t.Context(), cfg, p, service, core, tool)
	if err != nil || len(jobs) != 2 {
		t.Fatal("exact two original provider owners require native public-cache producers", len(jobs), err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("c", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	binding.OpenBaoRootSHA256, binding.ServiceRootSHA256 = root.RootFingerprint, service.Root.RootFingerprint
	projected, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, "", jobs)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]bool{}
	for _, definition := range projected {
		if definition.Object["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(definition.Object)
		container := managedNamedField(pod["containers"], "enroll")
		if container["image"] != tool.Reference {
			t.Fatal("runtime image selected as repair helper")
		}
		args := certIssuerObjectList(container["command"])
		if len(args) != 8 || args[0] != "/app/serviceidentity-bootstrap" || args[1] != "initialize-public-crl" || args[2] != "/var/lib/pki-service/private/crl/openbao-root.json" || args[3] != root.ID || args[4] != root.RootFingerprint || args[5] != "/var/lib/pki-service/private/crl/openbao-root-"+intermediate.ID+".json" || args[6] != intermediate.ID || args[7] != intermediate.Document["certificate_fingerprint_sha256"] {
			t.Fatal("public producer changed the admitted purpose, path or issuer")
		}
		prefix := mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX")
		for _, role := range []string{"certissuer", "pki-controller"} {
			if prefix == "dev/"+binding.Generation+"/"+role {
				owners[role] = true
			}
		}
		if createPodHasVolume(pod, "bootstrap") || createPodHasVolume(pod, "service-trust") || mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID") != "" {
			t.Fatal("public repair retained private bootstrap authority")
		}
	}
	if !owners["certissuer"] || !owners["pki-controller"] || p.Image != runtimeImage || deploymentplan.Digest(core) != before {
		t.Fatal("producer changed runtime selection/source bodies or original owner scope")
	}
	for _, mode := range []string{"foreign-tool", "missing-manifest", "foreign-domain", "missing-owner"} {
		t.Run(mode, func(t *testing.T) {
			changed := make([]deploymentplan.Definition, len(core))
			copy(changed, core)
			for i := range changed {
				changed[i].Object = deploymentplan.Clone(changed[i].Object)
			}
			selected := tool
			switch mode {
			case "foreign-tool":
				selected.Reference = "mutable:latest"
			case "missing-manifest", "foreign-domain":
				for _, d := range changed {
					if deploymentplan.ID(d.Object) == "ConfigMap/"+p.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust" {
						data := deploymentplan.Map(d.Object["data"])
						if mode == "missing-manifest" {
							delete(data, "crls.json")
						} else {
							data["crls.json"] = environmentCreateServiceCRLManifest(service.Root, "/var/lib/pki-service/private/crl/openbao-root.json", service.Intermediate)
						}
					}
				}
			case "missing-owner":
				for i, d := range changed {
					if deploymentplan.ID(d.Object) == "Deployment/"+p.Stack+"-video-cloud/certissuer" {
						changed = append(changed[:i:i], changed[i+1:]...)
						break
					}
				}
			}
			if _, err := renderEnvironmentCreateOpenBaoPublicCRLDefinitions(t.Context(), cfg, p, service, changed, selected); err == nil {
				t.Fatal("unqualified public cache producer accepted", mode)
			}
		})
	}
}
