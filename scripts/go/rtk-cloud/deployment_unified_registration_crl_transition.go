package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// Qualify the explicitly selected public-only refresh recipe before comparing
// lifecycle inputs. Only its two public-file settings are normalized; original
// private keys, Root pins, configuration sources and all mounts still compare.
func validateConfiguredEnvironmentPKITransition(cfg deploymentConfig, operator map[string]string, definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	image, err := accountRegistrationCRLRefreshInputs(cfg.Environment, operator)
	if err != nil {
		return err
	}
	if image == "" {
		return validateEnvironmentPKITransition(definitions, live)
	}
	id := "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-account-manager/account-manager"
	var target deploymentplan.Object
	for _, definition := range definitions {
		if deploymentplan.ID(definition.Object) == id && !definition.Retire && !definition.Retain {
			target = definition.Object
		}
	}
	normalized := append([]deploymentplan.Object(nil), live...)
	for i, current := range live {
		if deploymentplan.ID(current) != id || target == nil {
			continue
		}
		originalProfile, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, current, image)
		if err != nil {
			return environmentPKITransitionBlocker(id, "original registration listener does not match the selected public CRL refresh profile")
		}
		wantedProfile, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, target, image)
		if err != nil || deploymentplan.Digest(wantedProfile.Object) != deploymentplan.Digest(target) {
			return environmentPKITransitionBlocker(id, "target registration listener lacks the exact selected public CRL producer and public-only seed")
		}
		normalized[i] = deploymentplan.Clone(current)
		for name, setting := range map[string]string{"app": "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL", "pkimanagement": accountRegistrationCRLExportEnv} {
			c := managedNamedField(environmentPKIPod(normalized[i])["containers"], name)
			expected := managedNamedField(environmentPKIPod(originalProfile.Object)["containers"], name)
			if err := registrationCRLSetEnvironment(c, setting, mqttFoundationEnv(expected, setting)); err != nil {
				return environmentPKITransitionBlocker(id, "registration public CRL setting is ambiguous")
			}
		}
	}
	return validateEnvironmentPKITransition(definitions, normalized)
}
