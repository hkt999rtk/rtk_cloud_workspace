package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The current owner retains only its qualified RWO placement. Workload behavior
// remains catalog-owned and public transport is derived from current trust roles.
type environmentManagedMQTTPlacement struct {
	OwnerUID string                `json:"owner_uid"`
	Affinity deploymentplan.Object `json:"affinity"`
}

func managedMQTTWorkerRole(name string) (container, role string) {
	switch name {
	case "video-cloud-mqttfoundation":
		return "mqttfoundation", "mqtt"
	case "video-cloud-shadowworker":
		return "shadowworker", "shadow"
	}
	return "", ""
}

func validateEnvironmentManagedMQTTPlacement(b environmentIdentityBindings, p environmentPodBindings) error {
	if p.ManagedMQTT == nil {
		return nil
	}
	id := p.Kind + "/" + b.Stack + "-" + p.NamespaceSuffix + "/" + p.Name
	fail := func() error {
		return managedMQTTConsumerBlocker(id, "current MQTT placement differs from its exact original worker owner and dedicated identity")
	}
	container, role := managedMQTTWorkerRole(p.Name)
	if container == "" || p.Kind != "Deployment" || p.NamespaceSuffix != "video-cloud" || !environmentBootstrapOwnerUUID.MatchString(p.ManagedMQTT.OwnerUID) || b.Owners[id] != p.ManagedMQTT.OwnerUID || len(p.Containers) != 1 || p.Containers[0].Name != container {
		return fail()
	}
	// Only the already installed self/enrollment pair is retained. It grants no
	// authority to an arbitrary label, namespace, node or private claim.
	a := p.ManagedMQTT.Affinity
	terms := certIssuerObjectList(deploymentplan.Map(a["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])
	if len(terms) != 1 {
		return fail()
	}
	term := deploymentplan.Map(terms[0])
	expressions := certIssuerObjectList(deploymentplan.Map(term["labelSelector"])["matchExpressions"])
	if len(expressions) != 1 {
		return fail()
	}
	values := certIssuerObjectList(deploymentplan.Map(expressions[0])["values"])
	if len(values) != 2 || values[0] != p.Name {
		return fail()
	}
	producer := deploymentplan.String(values[1])
	prefix := "pki-product-" + role + "-"
	suffix := strings.TrimPrefix(producer, prefix)
	if !strings.HasPrefix(producer, prefix) || len(suffix) != 12 || strings.Trim(suffix, "0123456789abcdef") != "" {
		return fail()
	}
	expected := deploymentplan.Object{"podAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{deploymentplan.Object{"labelSelector": deploymentplan.Object{"matchExpressions": []any{deploymentplan.Object{"key": "app.kubernetes.io/name", "operator": "In", "values": []any{p.Name, producer}}}}, "namespaces": []any{b.Stack + "-video-cloud"}, "topologyKey": "kubernetes.io/hostname"}}}}
	if deploymentplan.Digest(a) != deploymentplan.Digest(expected) {
		return fail()
	}
	o := deploymentplan.Object{"kind": p.Kind, "metadata": deploymentplan.Object{"name": p.Name, "namespace": b.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": p.PodFields["volumes"], "containers": []any{deploymentplan.Object{"name": container, "env": p.Containers[0].Fields["env"], "volumeMounts": p.Containers[0].Fields["volumeMounts"]}}}}}}
	c := mqttFoundationPrimary(o)
	secret := mqttFoundationIdentitySecretName
	if role == "shadow" {
		secret = shadowWorkerIdentitySecretName
	}
	if mqttFoundationMountedSecret(o, c, "/etc/video_cloud/platform-service") != secret || !environmentPreflightDigest.MatchString(mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")) {
		return fail()
	}
	volumes, err := environmentPKINamedFields(environmentPKIPod(o)["volumes"])
	mounts, mountErr := environmentPKINamedFields(c["volumeMounts"])
	if err != nil || mountErr != nil || deploymentplan.Map(volumes["mqtt-bundle-state"]["persistentVolumeClaim"])["claimName"] != managedMQTTConsumerClaim(o, c) || mounts["mqtt-bundle-state"]["mountPath"] != "/var/lib/mqtt-trust" || mounts["mqtt-bundle-state"]["readOnly"] == true {
		return fail()
	}
	return nil
}

func observeEnvironmentManagedMQTTPlacements(b *environmentIdentityBindings, live []deploymentplan.Object) error {
	if b == nil {
		return managedMQTTConsumerBlocker("", "current MQTT owner inventory is unavailable")
	}
	selected := map[string]deploymentplan.Object{}
	plan := &deploymentplan.Plan{}
	current := map[string]deploymentplan.Object{}
	for _, o := range live {
		id := deploymentplan.ID(o)
		current[id] = o
		if o["kind"] == "Pod" || o["kind"] == "ReplicaSet" {
			continue
		}
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: b.Owners[id], Desired: o})
		name := deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
		container, _ := managedMQTTWorkerRole(name)
		if container != "" && mqttFoundationEnv(mqttFoundationPrimary(o), "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != "" {
			selected[id] = o
		}
	}
	if len(selected) == 0 {
		return nil
	}
	// Existing independent MQTT Root, all public CM owners and each dedicated
	// claim are checked before any placement is admitted as a lifecycle input.
	if err := checkEnvironmentManagedMQTTConsumers(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) { return current[id], nil }); err != nil {
		return err
	}
	for i := range b.Pods {
		p := &b.Pods[i]
		id := p.Kind + "/" + b.Stack + "-" + p.NamespaceSuffix + "/" + p.Name
		if o := selected[id]; o != nil {
			p.ManagedMQTT = &environmentManagedMQTTPlacement{OwnerUID: b.Owners[id], Affinity: deploymentplan.Clone(deploymentplan.Map(environmentPKIPod(o)["affinity"]))}
			if err := validateEnvironmentManagedMQTTPlacement(*b, *p); err != nil {
				return err
			}
		}
	}
	return nil
}

func bindEnvironmentCurrentMQTTConsumers(defs []deploymentplan.Definition, b environmentIdentityBindings) error {
	selected := map[string]environmentPodBindings{}
	for _, p := range b.Pods {
		if p.ManagedMQTT == nil {
			continue
		}
		if err := validateEnvironmentManagedMQTTPlacement(b, p); err != nil {
			return err
		}
		selected[p.Kind+"/"+b.Stack+"-"+p.NamespaceSuffix+"/"+p.Name] = p
	}
	if len(selected) == 0 {
		return nil
	}
	var ready []deploymentplan.Definition
	for _, d := range defs {
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		container, _ := managedMQTTWorkerRole(name)
		p, exists := selected[deploymentplan.ID(d.Object)]
		if container != "" && !exists {
			continue
		}
		if exists {
			environmentPKIPod(d.Object)["affinity"] = deploymentplan.Clone(p.ManagedMQTT.Affinity)
			delete(selected, deploymentplan.ID(d.Object))
		}
		ready = append(ready, d)
	}
	if len(selected) != 0 {
		return managedMQTTConsumerBlocker("", "current MQTT worker has no maintained target")
	}
	return bindEnvironmentManagedMQTTConsumersInternal(ready, true)
}
