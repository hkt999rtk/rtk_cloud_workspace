package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestProductControlEnvironmentUsesResolvedConfigurationAcrossLifecycle(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Values["CLOUD_ENV_NAME"] != "" {
		t.Fatal("fixture must model raw resolved configuration without renderer-only environment")
	}
	t.Setenv("CLOUD_ENV_NAME", "staging")
	_, l, installed := productTargetFixture(t)
	beforeCfg, beforeLife := deploymentplan.Digest(cfg), deploymentplan.Digest(l)
	for _, name := range []string{"session-controls", "enrollment-closed", "initial", "final", "steady"} {
		t.Run(name, func(t *testing.T) {
			var defs []deploymentplan.Definition
			var err error
			switch name {
			case "session-controls":
				defs, err = productIdentityControlDefinitions(cfg, l, installed.Ceremony, true)
			case "enrollment-closed":
				defs, err = productEnrollmentClosedDefinitions(cfg, l, installed.Ceremony)
			case "initial":
				defs, err = resolveCreateNormalTailProductDefinitions(cfg, l, installed)
			case "final":
				defs, err = resolveCreateNormalTailProductFinalDefinitions(cfg, l, installed)
			case "steady":
				defs, err = productSteadyDefinitions(cfg, l, installed)
			}
			if err != nil || len(defs) == 0 {
				t.Fatal(name, err)
			}
			for _, d := range defs {
				if d.Object["kind"] == "ConfigMap" && strings.HasPrefix(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "pki-bootstrap-cleanup-binding-") {
					var binding environmentBootstrapCleanupBinding
					if decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(d.Object["data"])["binding.json"])), &binding) != nil || binding.Environment != "dev" || binding.SessionID != installed.Ceremony.BootstrapSessionID {
						t.Fatal("wrong projected cleanup environment")
					}
				}
			}
		})
	}
	if beforeCfg != deploymentplan.Digest(cfg) || beforeLife != deploymentplan.Digest(l) {
		t.Fatal("resolved input or immutable program mutated")
	}
	installed.Ceremony.CleanupBinding.Environment = "staging"
	if _, err := productIdentityControlDefinitions(cfg, l, installed.Ceremony, true); err == nil {
		t.Fatal("foreign environment binding accepted")
	}
}
