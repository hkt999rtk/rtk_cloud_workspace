package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestConfiguredRegistrationCRLTransitionPreservesIdentity(t *testing.T) {
	for _, mode := range []string{"selected", "already-selected", "unselected", "wrong-path", "private-seed", "missing-producer", "root-changed", "key-secret-changed", "socket-source-changed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, current, image := registrationCRLRefreshFixture(t)
			operator := map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": image}
			target, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, current, image)
			if err != nil {
				t.Fatal(err)
			}
			pod := environmentPKIPod(target.Object)
			app := managedNamedField(pod["containers"], "app")
			manager := managedNamedField(pod["containers"], "pkimanagement")
			switch mode {
			case "already-selected":
				current = deploymentplan.Clone(target.Object)
			case "unselected":
				delete(operator, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE")
				delete(operator, "PKI_MANAGEMENT_IMAGE")
			case "wrong-path":
				registrationCRLSetEnvironment(app, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL", "/different.crl")
			case "private-seed":
				seed := managedNamedField(pod["volumes"], accountRegistrationCRLSeedVolume)
				deploymentplan.Map(seed["secret"])["items"] = []any{deploymentplan.Object{"key": "tls.key", "path": "client.crl"}}
			case "missing-producer":
				manager["env"] = environmentCreateServiceWithoutEnv(manager["env"], accountRegistrationCRLExportEnv)
			case "root-changed":
				registrationCRLSetEnvironment(manager, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_ROOT_SHA256", "replacement-root")
			case "key-secret-changed":
				v := managedNamedField(pod["volumes"], "account-manager-service-registration-tls")
				deploymentplan.Map(v["secret"])["secretName"] = "replacement-key"
			case "socket-source-changed":
				v := managedNamedField(pod["volumes"], "pki-socket")
				delete(v, "emptyDir")
				v["persistentVolumeClaim"] = deploymentplan.Object{"claimName": "foreign"}
			}
			before, after := deploymentplan.Digest(current), deploymentplan.Digest(target.Object)
			err = validateConfiguredEnvironmentPKITransition(cfg, operator, []deploymentplan.Definition{target}, []deploymentplan.Object{current})
			if (err == nil) != (mode == "selected" || mode == "already-selected") {
				t.Fatalf("incorrect public CRL compatibility decision: %v", err)
			}
			if before != deploymentplan.Digest(current) || after != deploymentplan.Digest(target.Object) {
				t.Fatal("transition qualification mutated original or target")
			}
		})
	}
}
