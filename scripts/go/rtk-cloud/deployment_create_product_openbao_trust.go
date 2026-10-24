package main

import (
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const productIdentityManagedOpenBaoTrustProfile = "managed-openbao-client-trust/v1"

// The installed managed listener's Root and public trust owner are already
// frozen inputs. They are distinct from the earlier temporary bootstrap CA.
func productIdentityOpenBaoTrustPins(l *createNormalTailProductLifecycle) (id, uid, root string, err error) {
	bad := func() (string, string, string, error) {
		return "", "", "", environmentCreateBootstrapBlocker("managed OpenBao trust has no exact frozen listener Root and owner")
	}
	if l == nil {
		return bad()
	}
	id = "ConfigMap/" + l.Bootstrap.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	declared := false
	for _, d := range l.SourceDefinitions {
		if deploymentplan.ID(d.Object) == id {
			if declared {
				return bad()
			}
			declared = true
		}
		if deploymentplan.ID(d.Object) != "StatefulSet/"+l.Bootstrap.Stack+"-secrets/openbao" {
			continue
		}
		for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(value)
			if c["name"] != "openbao-pki" {
				continue
			}
			if root != "" {
				return bad()
			}
			root = mqttFoundationEnv(c, "OPENBAO_PKI_HOST_ROOT_SHA256")
			if !environmentPreflightDigest.MatchString(root) {
				return bad()
			}
		}
	}
	if root == "" {
		return "", "", "", nil
	} // old or pre-managed recipe remains unchanged
	uid = l.SourceOwners[id]
	if !declared || !environmentBootstrapOwnerUUID.MatchString(uid) {
		return bad()
	}
	return id, uid, root, nil
}

func validateProductIdentityOpenBaoTrust(l *createNormalTailProductLifecycle, object deploymentplan.Object, now time.Time) error {
	id, uid, root, err := productIdentityOpenBaoTrustPins(l)
	if err != nil || root == "" || object == nil || deploymentplan.ID(object) != id || deploymentplan.Map(object["metadata"])["uid"] != uid || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil || !managedLateSeedForwardRoots(deploymentplan.String(deploymentplan.Map(object["data"])["roots.pem"]), root, now) {
		return environmentCreateBootstrapBlocker("managed OpenBao consumer CA differs from its frozen current listener Root or original owner")
	}
	return nil
}

// Also used by a typed trust-only forward of an old, terminal failed Job.
// Its caller authenticates that original failure and current public CA first.
func bindProductIdentityOpenBaoTrust(l *createNormalTailProductLifecycle, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	_, _, root, err := productIdentityOpenBaoTrustPins(l)
	if err != nil || root == "" || (l.OpenBaoTrustProfile != "" && l.OpenBaoTrustProfile != productIdentityManagedOpenBaoTrustProfile) {
		return nil, environmentCreateBootstrapBlocker("product OpenBao client has no qualified current listener trust")
	}
	return bindEnvironmentCreateManagedOpenBaoTrust(definitions, l.Bootstrap.Stack)
}

func bindEnvironmentCreateManagedOpenBaoTrust(definitions []deploymentplan.Definition, stack string) ([]deploymentplan.Definition, error) {
	out := make([]deploymentplan.Definition, len(definitions))
	clients := 0
	for i, source := range definitions {
		d := source
		d.Object = deploymentplan.Clone(source.Object)
		d.Dependencies = append([]string(nil), source.Dependencies...)
		out[i] = d
		pod := environmentPKIPod(d.Object)
		if d.Object["kind"] != "Job" || pod == nil {
			continue
		}
		volumes, err := environmentPKINamedFields(pod["volumes"])
		if err != nil {
			return nil, err
		}
		for _, value := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(value)
			if mqttFoundationEnv(c, "OPENBAO_ADDR") == "" {
				continue
			}
			mounts, err := environmentPKINamedFields(c["volumeMounts"])
			volume, mount := volumes["openbao-ca"], mounts["openbao-ca"]
			if err != nil || deploymentplan.Map(d.Object["metadata"])["namespace"] != stack+"-video-cloud" || mqttFoundationEnv(c, "OPENBAO_ADDR") != "https://openbao."+stack+"-secrets.svc:8200" || mqttFoundationEnv(c, "OPENBAO_CACERT") != "/run/openbao-ca/ca.crt" || volume == nil || len(volume) != 2 || deploymentplan.Map(volume["configMap"])["name"] != "pki-openbao-transport-ca" || mount == nil || mount["mountPath"] != "/run/openbao-ca" || mount["readOnly"] != true || mount["subPath"] != nil || mount["subPathExpr"] != nil {
				return nil, environmentCreateBootstrapBlocker("original OpenBao client projection or endpoint differs from the maintained recipe")
			}
			projection := deploymentplan.Map(volume["configMap"])
			projection["name"] = "pki-create-openbao-tls-consumer-trust"
			projection["items"] = []any{deploymentplan.Object{"key": "roots.pem", "path": "ca.crt"}}
			clients++
		}
	}
	if clients != 1 {
		return nil, environmentCreateBootstrapBlocker("exactly one native session OpenBao client is required")
	}
	return out, nil
}
