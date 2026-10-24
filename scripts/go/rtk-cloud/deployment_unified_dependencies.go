package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
)

type environmentResourceReference struct{ ID, Kind, Namespace, Name string }

// Read only documented API reference fields. Recursively interpreting arbitrary
// Secret data, annotations or ConfigMap text as resource references would both
// expose private values in diagnostics and invent dependencies.
func environmentResourceReferences(object deploymentplan.Object) []environmentResourceReference {
	namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	refs := map[string]environmentResourceReference{}
	add := func(kind, ns, name string) {
		if name == "" {
			return
		}
		id := kind + "/" + ns + "/" + name
		refs[id] = environmentResourceReference{id, kind, ns, name}
	}
	spec := deploymentplan.Map(object["spec"])
	var pod deploymentplan.Object
	switch deploymentplan.String(object["kind"]) {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		pod = deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	case "CronJob":
		pod = deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(spec["jobTemplate"])["spec"])["template"])["spec"])
	case "Pod":
		pod = spec
	}
	add("ServiceAccount", namespace, deploymentplan.String(pod["serviceAccountName"]))
	for _, value := range certIssuerObjectList(pod["imagePullSecrets"]) {
		add("Secret", namespace, deploymentplan.String(deploymentplan.Map(value)["name"]))
	}
	for _, field := range []string{"containers", "initContainers"} {
		for _, value := range certIssuerObjectList(pod[field]) {
			container := deploymentplan.Map(value)
			for _, value := range certIssuerObjectList(container["env"]) {
				from := deploymentplan.Map(deploymentplan.Map(value)["valueFrom"])
				add("Secret", namespace, deploymentplan.String(deploymentplan.Map(from["secretKeyRef"])["name"]))
				add("ConfigMap", namespace, deploymentplan.String(deploymentplan.Map(from["configMapKeyRef"])["name"]))
			}
			for _, value := range certIssuerObjectList(container["envFrom"]) {
				from := deploymentplan.Map(value)
				add("Secret", namespace, deploymentplan.String(deploymentplan.Map(from["secretRef"])["name"]))
				add("ConfigMap", namespace, deploymentplan.String(deploymentplan.Map(from["configMapRef"])["name"]))
			}
		}
	}
	for _, value := range certIssuerObjectList(pod["volumes"]) {
		volume := deploymentplan.Map(value)
		add("Secret", namespace, deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"]))
		add("ConfigMap", namespace, deploymentplan.String(deploymentplan.Map(volume["configMap"])["name"]))
		add("PersistentVolumeClaim", namespace, deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"]))
		for _, value := range certIssuerObjectList(deploymentplan.Map(volume["projected"])["sources"]) {
			source := deploymentplan.Map(value)
			add("Secret", namespace, deploymentplan.String(deploymentplan.Map(source["secret"])["name"]))
			add("ConfigMap", namespace, deploymentplan.String(deploymentplan.Map(source["configMap"])["name"]))
		}
	}
	switch deploymentplan.String(object["kind"]) {
	case "StatefulSet":
		add("Service", namespace, deploymentplan.String(spec["serviceName"]))
	case "Ingress":
		for _, value := range certIssuerObjectList(spec["tls"]) {
			add("Secret", namespace, deploymentplan.String(deploymentplan.Map(value)["secretName"]))
		}
		service := func(backend any) {
			add("Service", namespace, deploymentplan.String(deploymentplan.Map(deploymentplan.Map(backend)["service"])["name"]))
		}
		service(spec["defaultBackend"])
		for _, value := range certIssuerObjectList(spec["rules"]) {
			for _, path := range certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(value)["http"])["paths"]) {
				service(deploymentplan.Map(path)["backend"])
			}
		}
		add("IngressClass", "", deploymentplan.String(spec["ingressClassName"]))
	case "RoleBinding", "ClusterRoleBinding":
		ref := deploymentplan.Map(object["roleRef"])
		ns := namespace
		if ref["kind"] == "ClusterRole" {
			ns = ""
		}
		add(deploymentplan.String(ref["kind"]), ns, deploymentplan.String(ref["name"]))
		for _, value := range certIssuerObjectList(object["subjects"]) {
			subject := deploymentplan.Map(value)
			if subject["kind"] == "ServiceAccount" {
				add("ServiceAccount", deploymentplan.String(subject["namespace"]), deploymentplan.String(subject["name"]))
			}
		}
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		for _, value := range certIssuerObjectList(object["webhooks"]) {
			service := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(value)["clientConfig"])["service"])
			add("Service", deploymentplan.String(service["namespace"]), deploymentplan.String(service["name"]))
		}
	case "HorizontalPodAutoscaler":
		ref := deploymentplan.Map(spec["scaleTargetRef"])
		add(deploymentplan.String(ref["kind"]), namespace, deploymentplan.String(ref["name"]))
	case "ServiceAccount":
		for _, field := range []string{"secrets", "imagePullSecrets"} {
			for _, value := range certIssuerObjectList(object[field]) {
				add("Secret", namespace, deploymentplan.String(deploymentplan.Map(value)["name"]))
			}
		}
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]environmentResourceReference, 0, len(ids))
	for _, id := range ids {
		result = append(result, refs[id])
	}
	return result
}
