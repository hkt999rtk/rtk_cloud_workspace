package main

import (
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// A dev-only ordinary configuration override selects one existing sidecar.
// Deployment evidence remains the ordinary scoped plan and execution journal;
// future renders do not replay an unrelated create program's history.
func accountRegistrationCRLRefreshInputs(environment string, values map[string]string) (string, error) {
	profile, image := values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"], values["PKI_MANAGEMENT_IMAGE"]
	if profile == "" && image == "" {
		return "", nil
	}
	if environment != "dev" || profile != accountRegistrationCRLRefreshProfile || !immutableEnvironmentImage.MatchString(image) {
		return "", errors.New("registration CRL refresh requires the exact dev profile and immutable management image")
	}
	return image, nil
}

func accountRegistrationCRLRefreshRelease(cfg deploymentConfig, original environmentRelease, operator map[string]string) (environmentRelease, error) {
	selection, operator, err := selectedRegistrationCRLOperator(cfg.Environment, operator)
	if err != nil {
		return original, err
	}
	image, err := accountRegistrationCRLRefreshInputs(cfg.Environment, operator)
	if err != nil {
		return original, err
	}
	if _, exists := original.Images["LKE_PKI_MANAGEMENT_IMAGE"]; exists && selection.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference == "" {
		return original, errors.New("management image role must come only from selected operator configuration")
	}
	controller := operator["PKI_CONTROLLER_IMAGE"]
	if controller != "" && (cfg.Environment != "dev" && selection.Images["LKE_PKI_CONTROLLER_IMAGE"].Reference == "" || !immutableEnvironmentImage.MatchString(controller)) {
		return original, errors.New("controller override requires the qualified immutable dev native package")
	}
	if image == "" && controller == "" && selection.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference == "" {
		return original, nil
	}
	next := original
	next.Images = make(map[string]environmentReleaseImage, len(original.Images)+1)
	for key, value := range original.Images {
		next.Images[key] = value
	}
	if image != "" {
		next.Images["LKE_PKI_MANAGEMENT_IMAGE"] = environmentReleaseImage{Reference: image}
	}
	if controller != "" {
		next.Images["LKE_PKI_CONTROLLER_IMAGE"] = environmentReleaseImage{Reference: controller}
	}
	for _, role := range []string{"LKE_PKI_MANAGEMENT_IMAGE", "LKE_PKI_CONTROLLER_IMAGE"} {
		if selected, ok := selection.Images[role]; ok {
			next.Images[role] = selected
		}
	}
	return next, nil
}

// Work with a copy: canonical role selections outrank legacy aliases without
// changing stored settings or enabling the optional registration-CRL profile.
func selectedRegistrationCRLOperator(environment string, operator map[string]string) (environmentImageSelection, map[string]string, error) {
	selection, err := parseEnvironmentImageSelection(environment, operator[environmentImagesKey])
	if err != nil {
		return selection, nil, err
	}
	selected := appendMap(operator, nil)
	for role, image := range selection.Images {
		selected[strings.TrimPrefix(role, "LKE_")] = image.Reference
	}
	if selected["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] == "" && selection.Images["LKE_PKI_MANAGEMENT_IMAGE"].Reference != "" {
		delete(selected, "PKI_MANAGEMENT_IMAGE")
	}
	return selection, selected, nil
}

func projectAccountRegistrationCRLRefreshInputs(environment string, operator, values map[string]string) error {
	selection, operator, err := selectedRegistrationCRLOperator(environment, operator)
	if err != nil {
		return err
	}
	image, err := accountRegistrationCRLRefreshInputs(environment, operator)
	if err != nil {
		return err
	}
	// Clear unselected ambient/configuration inputs too. Only the selected
	// canonical operator snapshot supplies this optional dev profile.
	values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] = ""
	values["PKI_MANAGEMENT_IMAGE"] = ""
	values["PKI_CONTROLLER_IMAGE"] = ""
	if image != "" {
		values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] = accountRegistrationCRLRefreshProfile
		values["PKI_MANAGEMENT_IMAGE"] = image
	}
	if controller := operator["PKI_CONTROLLER_IMAGE"]; controller != "" {
		if environment != "dev" && selection.Images["LKE_PKI_CONTROLLER_IMAGE"].Reference == "" || !immutableEnvironmentImage.MatchString(controller) {
			return errors.New("controller override requires the qualified immutable dev native package")
		}
		values["PKI_CONTROLLER_IMAGE"] = controller
	}
	return nil
}

func applyAccountRegistrationCRLRefreshProfile(values map[string]string, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	image, err := accountRegistrationCRLRefreshInputs(values["CLOUD_ENV_NAME"], values)
	if err != nil {
		return nil, err
	}
	if image == "" {
		for _, d := range definitions {
			if deploymentplan.ID(d.Object) != "Deployment/"+values["CLOUD_STACK_NAME"]+"-account-manager/account-manager" {
				continue
			}
			pod := environmentPKIPod(d.Object)
			for _, raw := range certIssuerObjectList(pod["containers"]) {
				c := deploymentplan.Map(raw)
				if c["name"] == "app" && mqttFoundationEnv(c, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL") == accountRegistrationCRLBundlePath || c["name"] == "pkimanagement" && mqttFoundationEnv(c, accountRegistrationCRLExportEnv) != "" {
					return nil, errors.New("managed registration CRL declaration requires its explicit operator profile and image")
				}
			}
			for _, raw := range certIssuerObjectList(pod["volumes"]) {
				if deploymentplan.Map(raw)["name"] == accountRegistrationCRLSeedVolume {
					return nil, errors.New("managed registration CRL seed requires its explicit operator profile and image")
				}
			}
		}
		return definitions, nil
	}
	id := "Deployment/" + values["CLOUD_STACK_NAME"] + "-account-manager/account-manager"
	out := make([]deploymentplan.Definition, len(definitions))
	copy(out, definitions)
	found := 0
	for i, d := range out {
		if deploymentplan.ID(d.Object) != id {
			continue
		}
		found++
		next, err := environmentAccountRegistrationCRLRefreshDefinition(deploymentConfig{Environment: values["CLOUD_ENV_NAME"], Values: values}, d.Object, image)
		if err != nil {
			return nil, err
		}
		out[i].Object = next.Object
	}
	if found != 1 {
		return nil, errors.New("registration CRL refresh requires exactly one maintained Account Manager declaration")
	}
	return out, nil
}
