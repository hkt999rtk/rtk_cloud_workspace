package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// A projected, short-lived audience-bound token is mounted only in the native
// client. Its explicit read mode must not depend on fsGroup: retained private
// key/CSR files on the other volume must remain owner-only.
func bindProductSessionForwardReadableToken(definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, productSessionForwardBlock("exact native projected token contract")
	}
	if len(definitions) != 1 || definitions[0].Object["kind"] != "Job" {
		return bad()
	}
	d := definitions[0]
	d.Object = deploymentplan.Clone(d.Object)
	d.Dependencies = append([]string(nil), d.Dependencies...)
	pod := environmentPKIPod(d.Object)
	security := deploymentplan.Map(pod["securityContext"])
	if pod == nil || security["fsGroup"] != nil || security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) {
		return bad()
	}
	containers := certIssuerObjectList(pod["containers"])
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil || len(containers) != 1 || volumes["openbao-auth"] == nil || len(volumes["openbao-auth"]) != 2 {
		return bad()
	}
	c := deploymentplan.Map(containers[0])
	mounts, err := environmentPKINamedFields(c["volumeMounts"])
	mount := mounts["openbao-auth"]
	if err != nil || mount == nil || mount["mountPath"] != "/run/openbao-auth" || mount["readOnly"] != true || mount["subPath"] != nil || mount["subPathExpr"] != nil || mqttFoundationEnv(c, "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE") != "/run/openbao-auth/token" || mqttFoundationEnv(c, "OPENBAO_AUTH_METHOD") != "kubernetes" {
		return bad()
	}
	projected := deploymentplan.Map(volumes["openbao-auth"]["projected"])
	wanted := []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"audience": "openbao", "expirationSeconds": float64(3600), "path": "token"}}}
	if len(projected) != 2 || projected["defaultMode"] != float64(0440) || deploymentplan.Digest(projected["sources"]) != deploymentplan.Digest(wanted) {
		return bad()
	}
	projected["defaultMode"] = float64(0444)
	return []deploymentplan.Definition{d}, nil
}
