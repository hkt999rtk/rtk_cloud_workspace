package main

import (
	"crypto/sha256"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const accountRegistrationCRLRefreshProfile = "managed-public-pem-v1"
const accountRegistrationCRLBundlePath = "/run/account-pki/private/service-registration.crl.pem"
const accountRegistrationCRLExportEnv = "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_PUBLIC_CRL_BUNDLE"
const accountRegistrationCRLSeedVolume = "account-registration-crl-seed"

// A public-only compatibility seed lets the unchanged AM constructor parse its
// CRL path before either process starts. It grants no current authority: the
// app still checks issuer/signature/revocation/freshness on every request, and
// the managed owner atomically installs the complete verified registry snapshot.
const accountManagerPublicCRLPreparation = `umask 077
mkdir -p /socket/private
[ ! -L /socket/private ] && [ -d /socket/private ]
[ "$(stat -c '%u:%g' /socket/private)" = '10001:10001' ]
case "$(stat -c '%a' /socket/private)" in 700|2700) ;; *) exit 1;; esac
chmod 700 /socket/private
file=/socket/private/service-registration.crl.pem
if [ -e "$file" ] || [ -L "$file" ]; then
 [ ! -L "$file" ] && [ -f "$file" ]
 [ "$(stat -c '%a:%u:%g:%h' "$file")" = '600:10001:10001:1' ]
else
 [ -f /registration-crl-seed/client.crl ] && [ -s /registration-crl-seed/client.crl ]
 [ "$(wc -c < /registration-crl-seed/client.crl)" -le 1048576 ]
 temporary=$(mktemp /socket/private/.registration-seed-XXXXXX)
 trap 'rm -f "$temporary"' EXIT
 cat /registration-crl-seed/client.crl > "$temporary"
 chmod 600 "$temporary"
 cmp /registration-crl-seed/client.crl "$temporary"
 [ "$(stat -c '%a:%u:%g:%h' "$temporary")" = '600:10001:10001:1' ]
 [ ! -e "$file" ] && [ ! -L "$file" ]
 mv "$temporary" "$file"
 trap - EXIT
fi`

func accountRegistrationCRLSeedMount() deploymentplan.Object {
	return deploymentplan.Object{"name": accountRegistrationCRLSeedVolume, "mountPath": "/registration-crl-seed", "readOnly": true}
}
func accountRegistrationCRLSeed() deploymentplan.Object {
	return deploymentplan.Object{"name": accountRegistrationCRLSeedVolume, "secret": deploymentplan.Object{"secretName": "account-manager-service-registration-tls", "defaultMode": float64(256), "items": []any{deploymentplan.Object{"key": "client.crl", "path": "client.crl"}}}}
}
func registrationCRLSetEnvironment(c deploymentplan.Object, name, value string) error {
	count := 0
	for _, raw := range certIssuerObjectList(c["env"]) {
		e := deploymentplan.Map(raw)
		if e["name"] == name {
			count++
			if e["valueFrom"] != nil {
				return fmt.Errorf("public CRL profile requires literal owned setting")
			}
			e["value"] = value
		}
	}
	if count > 1 {
		return fmt.Errorf("duplicate public CRL profile setting")
	}
	if count == 0 {
		c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": name, "value": value})
	}
	return nil
}

// This finite desired producer is shared by ordinary rendering and the scoped
// one-owner update. Operator selection is validated by the common loader; no
// live image, private material or former journal is adopted here.
func environmentAccountRegistrationCRLRefreshDefinition(cfg deploymentConfig, original deploymentplan.Object, image string) (deploymentplan.Definition, error) {
	fail := func() (deploymentplan.Definition, error) {
		return deploymentplan.Definition{}, fmt.Errorf("managed Account registration CRL profile does not match its original owned socket, listener or public seed")
	}
	if cfg.Environment != "dev" || !immutableEnvironmentImage.MatchString(image) || deploymentplan.ID(original) != "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-account-manager/account-manager" {
		return fail()
	}
	o := deploymentplan.Clone(original)
	pod := environmentPKIPod(o)
	if pod == nil || deploymentplan.Map(o["spec"])["replicas"] != float64(1) || deploymentplan.Map(deploymentplan.Map(o["spec"])["strategy"])["type"] != "Recreate" {
		return fail()
	}

	security := deploymentplan.Map(pod["securityContext"])
	if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["fsGroup"] != float64(10001) || security["runAsNonRoot"] != true {
		return fail()
	}
	var app, manager deploymentplan.Object
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		switch c["name"] {
		case "app":
			if app != nil {
				return fail()
			}
			app = c
		case "pkimanagement":
			if manager != nil {
				return fail()
			}
			manager = c
		default:
			return fail()
		}
	}
	if app == nil || manager == nil || mqttFoundationEnv(app, "APP_CERT_ISSUER_SOCKET") != "/run/account-pki/private/controller.sock" || mqttFoundationEnv(manager, "PKI_MANAGEMENT_SOCKET") != "/run/account-pki/private/controller.sock" || mqttFoundationEnv(manager, "PKI_MANAGEMENT_FIRST_TRUST_ONLY") == "true" || mqttFoundationEnv(manager, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST") == "" {
		return fail()
	}
	if current := mqttFoundationEnv(manager, accountRegistrationCRLExportEnv); current != "" && current != accountRegistrationCRLBundlePath {
		return fail()
	}
	publicPath := mqttFoundationEnv(app, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL")
	if publicPath != "/etc/rtk-account-manager/service-registration/client.crl" && publicPath != accountRegistrationCRLBundlePath {
		return fail()
	}
	socketVolume := ""
	registrationVolume := ""
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		if v["name"] == accountRegistrationCRLSeedVolume {
			if deploymentplan.Digest(v) != deploymentplan.Digest(accountRegistrationCRLSeed()) {
				return fail()
			}
			continue
		}
		if v["emptyDir"] != nil {
			for _, raw := range certIssuerObjectList(manager["volumeMounts"]) {
				m := deploymentplan.Map(raw)
				if m["name"] == v["name"] && m["mountPath"] == "/run/account-pki" && m["readOnly"] != true && m["subPath"] == nil && m["subPathExpr"] == nil {
					if socketVolume != "" {
						return fail()
					}
					socketVolume = deploymentplan.String(v["name"])
				}
			}
		}
		if deploymentplan.Map(v["secret"])["secretName"] == "account-manager-service-registration-tls" {
			if registrationVolume != "" {
				return fail()
			}
			registrationVolume = deploymentplan.String(v["name"])
		}
	}
	if socketVolume == "" || registrationVolume == "" {
		return fail()
	}
	appSocket := false
	publicMount := false
	for _, raw := range certIssuerObjectList(app["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if m["name"] == socketVolume && m["mountPath"] == "/run/account-pki" && m["readOnly"] != true && m["subPath"] == nil && m["subPathExpr"] == nil {
			appSocket = true
		}
		if m["name"] == registrationVolume && m["readOnly"] == true && m["mountPath"] == "/etc/rtk-account-manager/service-registration" {
			publicMount = true
		}
		for _, vraw := range certIssuerObjectList(pod["volumes"]) {
			v := deploymentplan.Map(vraw)
			if v["name"] == m["name"] && v["persistentVolumeClaim"] != nil {
				return fail()
			}
		}
	}
	if !appSocket || !publicMount {
		return fail()
	}
	init := certIssuerObjectList(pod["initContainers"])
	if len(init) != 1 {
		return fail()
	}
	c := deploymentplan.Map(init[0])
	if c["name"] != "management-socket-directory" || !immutableEnvironmentImage.MatchString(deploymentplan.String(c["image"])) {
		return fail()
	}

	for _, container := range []deploymentplan.Object{app, manager, c} {
		sc := deploymentplan.Map(container["securityContext"])
		for _, key := range []string{"runAsUser", "runAsGroup"} {
			if v := sc[key]; v != nil && v != float64(10001) {
				return fail()
			}
		}
	}
	cmds := append(certIssuerObjectList(c["command"]), certIssuerObjectList(c["args"])...)
	if len(cmds) != 3 || (cmds[0] != "sh" && cmds[0] != "/bin/sh") || (cmds[1] != "-c" && cmds[1] != "-ec") {
		return fail()
	}
	script := deploymentplan.String(cmds[2])
	if script != accountManagerSocketPreparation && script != accountManagerPublicCRLPreparation && fmt.Sprintf("%x", sha256.Sum256([]byte(script))) != "c5fce41047873694c256411b06c258c56a1f58c9528ff75655f68b24d6b1e4d8" {
		return fail()
	}
	mounts := certIssuerObjectList(c["volumeMounts"])
	if len(mounts) < 1 || len(mounts) > 2 {
		return fail()
	}
	m := deploymentplan.Map(mounts[0])
	if m["name"] != socketVolume || m["mountPath"] != "/socket" || m["readOnly"] == true || m["subPath"] != nil || m["subPathExpr"] != nil {
		return fail()
	}
	if len(mounts) == 2 && deploymentplan.Digest(mounts[1]) != deploymentplan.Digest(accountRegistrationCRLSeedMount()) {
		return fail()
	}
	c["command"] = []any{"/bin/sh", "-ec", accountManagerPublicCRLPreparation}
	delete(c, "args")
	c["volumeMounts"] = []any{mounts[0], accountRegistrationCRLSeedMount()}
	found := false
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(raw)["name"] == accountRegistrationCRLSeedVolume {
			found = true
		}
	}
	if !found {
		pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), accountRegistrationCRLSeed())
	}
	if err := registrationCRLSetEnvironment(manager, accountRegistrationCRLExportEnv, accountRegistrationCRLBundlePath); err != nil {
		return deploymentplan.Definition{}, err
	}
	if err := registrationCRLSetEnvironment(app, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL", accountRegistrationCRLBundlePath); err != nil {
		return deploymentplan.Definition{}, err
	}
	manager["image"] = image
	return deploymentplan.Definition{Phase: "workload", Object: o}, nil
}
