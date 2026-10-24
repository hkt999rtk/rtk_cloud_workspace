package main

import (
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Ordinary observation follows the cleanup record mounted by its existing
// Controller. It preserves the original namespace/API trust lineage without
// requiring a still-active create execution or reopening an old ceremony.
func observeEnvironmentCurrentCleanupBinding(bindings *environmentIdentityBindings, live []deploymentplan.Object) error {
	bad := func() error {
		return environmentBootstrapCleanupBlocker("current Controller cleanup mount, immutable record or original namespace/API trust lineage differs")
	}
	if bindings == nil {
		return bad()
	}
	namespace := bindings.Stack + "-video-cloud"
	controllerID := "Deployment/" + namespace + "/pki-controller"
	var controller deploymentplan.Object
	objects := map[string]deploymentplan.Object{}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if objects[id] != nil {
			return bad()
		}
		objects[id] = o
		if id == controllerID {
			controller = o
		}
	}
	if controller == nil {
		if bindings.BootstrapCleanup != nil {
			return bad()
		}
		return nil
	}
	pod := environmentPKIPod(controller)
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return bad()
	}
	volume := volumes["bootstrap-cleanup"]
	if volume == nil {
		if bindings.BootstrapCleanup != nil {
			return bad()
		}
		return nil
	}
	if bindings.BootstrapCleanup == nil || validateEnvironmentBootstrapCleanupBinding(bindings.BootstrapCleanup, bindings.Environment) != nil || bindings.Stack != "video-cloud-"+bindings.Environment {
		return bad()
	}
	original := *bindings.BootstrapCleanup
	name := deploymentplan.String(deploymentplan.Map(volume["configMap"])["name"])
	cmID := "ConfigMap/" + namespace + "/" + name
	cm := objects[cmID]
	for id, o := range map[string]deploymentplan.Object{controllerID: controller, cmID: cm} {
		uid := bindings.Owners[id]
		if o == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(o["metadata"])["uid"] != uid || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			return bad()
		}
	}
	if cm["immutable"] != true || bindings.Owners["Namespace//"+namespace] != original.NamespaceUID || deploymentplan.Map(objects["Namespace//"+namespace]["metadata"])["uid"] != original.NamespaceUID {
		return bad()
	}
	var current environmentBootstrapCleanupBinding
	data := deploymentplan.Map(cm["data"])
	if decodeEnvironmentJSON([]byte(deploymentplan.String(data["binding.json"])), &current) != nil || current.ConfigMapName != "" || current.KubernetesCA != "" {
		return bad()
	}
	current.KubernetesCA = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(data["ca.crt"])))
	if name != "pki-bootstrap-cleanup-binding" {
		current.ConfigMapName = name
	}
	if validateEnvironmentBootstrapCleanupBinding(&current, bindings.Environment) != nil || current.NamespaceUID != original.NamespaceUID || current.KubernetesCA != original.KubernetesCA {
		return bad()
	}
	if uid := bindings.Owners["Secret/"+namespace+"/service-bootstrap-"+current.SessionID]; uid != "" && uid != current.SecretUID {
		return bad()
	}
	containers, err := environmentPKIPodContainers(pod)
	if err != nil {
		return bad()
	}
	container := containers["containers/pki-controller"]
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return bad()
	}
	if pod["serviceAccountName"] != "pki-controller" || pod["automountServiceAccountToken"] != false {
		return bad()
	}
	expectedVolume := deploymentplan.Object{"name": "bootstrap-cleanup", "configMap": deploymentplan.Object{"name": name, "defaultMode": float64(0440)}}
	expectedToken := deploymentplan.Object{"name": "bootstrap-cleanup-api", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(3600), "path": "token"}}}}}
	if deploymentplan.Digest(volume) != deploymentplan.Digest(expectedVolume) || deploymentplan.Digest(volumes["bootstrap-cleanup-api"]) != deploymentplan.Digest(expectedToken) {
		return bad()
	}
	for name, path := range map[string]string{"bootstrap-cleanup": "/run/bootstrap-cleanup", "bootstrap-cleanup-api": "/run/bootstrap-cleanup-api"} {
		if deploymentplan.Digest(mounts[name]) != deploymentplan.Digest(environmentCreateServiceMount(name, path, true)) {
			return bad()
		}
	}
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return bad()
	}
	for key, value := range map[string]string{"PKI_BOOTSTRAP_CLEANUP_BINDING": "/run/bootstrap-cleanup/binding.json", "PKI_BOOTSTRAP_KUBERNETES_CA_FILE": "/run/bootstrap-cleanup/ca.crt", "PKI_BOOTSTRAP_KUBERNETES_TOKEN_FILE": "/run/bootstrap-cleanup-api/token"} {
		if settings[key]["value"] != value || settings[key]["valueFrom"] != nil {
			return bad()
		}
	}
	bindings.BootstrapCleanup = &current
	return nil
}
