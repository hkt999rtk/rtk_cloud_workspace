package main

import (
	"context"
	"fmt"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedMQTTConsumerBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.mqtt-transport", Resource: id, Cause: cause, Next: "repair the declared MQTT dependency and revalidate the recorded target; resume the admitted execution with its original identities and clock; qualify again only if deployment intent changes"}
}

// These native clients already authenticate with their own installed Service
// identity. The MQTT server pool and public CRL documents are shared; private
// identity files and writable CRL caches are never borrowed from another owner.
func managedMQTTConsumerClaim(o, c deploymentplan.Object) string {
	if o["kind"] != "Deployment" {
		return ""
	}
	switch deploymentplan.String(deploymentplan.Map(o["metadata"])["name"]) {
	case "video-cloud-api-pki":
		if c["name"] == "app" {
			return "video-cloud-api-pki-controller-identity"
		}
	case "video-cloud-mqttfoundation":
		if c["name"] == "mqttfoundation" {
			return "pki-product-mqtt-identity"
		}
	case "video-cloud-shadowworker":
		if c["name"] == "shadowworker" {
			return "pki-product-shadow-identity"
		}
	}
	return ""
}

func managedMQTTConsumerSettings(name string) bool {
	return keySet("VIDEO_CLOUD_MQTT_TLS", "VIDEO_CLOUD_MQTT_ADDR", "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL", "VIDEO_CLOUD_MQTT_MANAGEMENT_CA", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_PKI_CONTROLLER_URL", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANAGEMENT_CA")[name]
}

func bindEnvironmentManagedMQTTConsumers(defs []deploymentplan.Definition, sessions ...string) error {
	return bindEnvironmentManagedMQTTConsumersInternal(defs, false, sessions...)
}

func bindEnvironmentManagedMQTTConsumersForRender(defs []deploymentplan.Definition, binding *environmentFreshProductBinding) error {
	if binding != nil {
		return bindEnvironmentManagedMQTTConsumers(defs, binding.SessionID)
	}
	// Future product outputs do not yet have a SID. Their typed resolver binds
	// the cache and RWO placement after native enrollment; existing API derives now.
	var ready []deploymentplan.Definition
	for _, d := range defs {
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		if name != "video-cloud-mqttfoundation" && name != "video-cloud-shadowworker" {
			ready = append(ready, d)
		}
	}
	return bindEnvironmentManagedMQTTConsumers(ready)
}

func bindEnvironmentManagedMQTTConsumersInternal(defs []deploymentplan.Definition, declared bool, sessions ...string) error {
	return bindEnvironmentManagedMQTTConsumersProfile(defs, declared, true, sessions...)
}

// Only the exact, never-started rejected target uses the previous derivation.
// It cannot authorize a child execution or a completed workload receipt.
func bindEnvironmentManagedMQTTConsumersLegacy(defs []deploymentplan.Definition) error {
	return bindEnvironmentManagedMQTTConsumersProfile(defs, false, false)
}

func bindEnvironmentManagedMQTTConsumersProfile(defs []deploymentplan.Definition, declared, reuseAliases bool, sessions ...string) error {
	if len(sessions) > 1 {
		return managedMQTTConsumerBlocker("", "one original product session is required")
	}
	donor := mqttFoundationTarget(defs, "Deployment", "video-cloud-api")
	source := mqttFoundationPrimary(donor)
	pin := mqttFoundationEnv(source, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")
	if pin == "" {
		return nil
	} // An unadopted historical profile cannot create a Root binding.
	if !environmentPreflightDigest.MatchString(pin) {
		return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "original MQTT Root binding is invalid")
	}
	ns := deploymentplan.String(deploymentplan.Map(donor["metadata"])["namespace"])
	host, port, err := coreMQTTService(defs, ns, "mqtt-pki", "mqtts")
	if err != nil {
		return managedMQTTConsumerBlocker("Service/"+ns+"/mqtt-pki", "selected broker has no unique MQTT TLS listener")
	}
	broker := mqttFoundationTarget(defs, "Deployment", "mqtt-pki")
	brokerPin := ""
	for _, raw := range certIssuerObjectList(environmentPKIPod(broker)["containers"]) {
		c := deploymentplan.Map(raw)
		if c["name"] == "mqtt" {
			brokerPin = mqttFoundationEnv(c, "EMQX_PKI_HOST_ROOT_SHA256")
		}
	}
	if brokerPin != pin {
		return managedMQTTConsumerBlocker(deploymentplan.ID(broker), "selected broker and original consumer have different independent MQTT Roots")
	}
	for _, key := range []string{"VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_ID", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_STATE", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOTS"} {
		if mqttFoundationEnv(source, key) != "" {
			return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "fixed MQTT Root cannot select a dynamic Root profile")
		}
	}
	values := map[string]string{}
	for _, raw := range certIssuerObjectList(source["env"]) {
		setting := deploymentplan.Map(raw)
		name := deploymentplan.String(setting["name"])
		if managedMQTTConsumerSettings(name) {
			if setting["valueFrom"] != nil {
				return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "original public MQTT transport setting is indirect")
			}
			values[name] = deploymentplan.String(setting["value"])
		}
	}
	for _, key := range []string{"VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL", "VIDEO_CLOUD_MQTT_MANAGEMENT_CA", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_PKI_CONTROLLER_URL", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANAGEMENT_CA"} {
		if values[key] == "" {
			return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "original managed MQTT transport is incomplete: "+key)
		}
	}
	values["VIDEO_CLOUD_MQTT_TLS"], values["VIDEO_CLOUD_MQTT_ADDR"], values["VIDEO_CLOUD_MQTT_SERVER_PKI_NAME"] = "true", fmt.Sprintf("%s:%d", host, port), host
	if values["VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256"] != pin {
		return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "prepared MQTT pool and registry Root differ")
	}
	sourceVolumes, err := environmentPKINamedFields(environmentPKIPod(donor)["volumes"])
	if err != nil {
		return err
	}
	sourceMounts, err := environmentPKINamedFields(source["volumeMounts"])
	if err != nil {
		return err
	}
	roles := []string{"mqtt-pki-bundles", "mqtt-pki-crls", "service-root"}
	for _, role := range roles {
		if deploymentplan.Map(sourceVolumes[role]["configMap"])["name"] == nil || sourceMounts[role]["readOnly"] != true {
			return managedMQTTConsumerBlocker(deploymentplan.ID(donor), "original MQTT public trust mount is unavailable: "+role)
		}
	}
	for _, d := range defs {
		pod := environmentPKIPod(d.Object)
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(raw)
			claim := managedMQTTConsumerClaim(d.Object, c)
			if claim == "" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "true" {
				continue
			}
			if deploymentplan.Map(d.Object["metadata"])["namespace"] != ns {
				return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "MQTT consumer is outside its bound broker namespace")
			}
			if c["name"] != "app" {
				prefix := "VIDEO_CLOUD_MQTT_FOUNDATION_"
				secret := mqttFoundationIdentitySecretName
				if c["name"] == "shadowworker" {
					prefix = "VIDEO_CLOUD_SHADOW_WORKER_"
					secret = shadowWorkerIdentitySecretName
				}
				if mqttFoundationEnv(c, prefix+"CLIENT_CERT") != "/etc/video_cloud/platform-service/client.crt" || mqttFoundationEnv(c, prefix+"CLIENT_KEY") != "/etc/video_cloud/platform-service/client.key" || mqttFoundationMountedSecret(d.Object, c, "/etc/video_cloud/platform-service") != secret {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native MQTT consumer lost its own installed product identity")
				}
			} else {
				own := false
				for _, v := range certIssuerObjectList(pod["volumes"]) {
					if deploymentplan.Map(deploymentplan.Map(v)["persistentVolumeClaim"])["claimName"] == claim {
						own = true
					}
				}
				if !own || mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE") == "" {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native API MQTT consumer lost its original managed identity claim")
				}
			}
			// Reject collisions rather than silently replacing an unrelated file source.
			volumes, err := environmentPKINamedFields(pod["volumes"])
			if err != nil {
				return err
			}
			mounts, err := environmentPKINamedFields(c["volumeMounts"])
			if err != nil {
				return err
			}
			for _, role := range roles {
				if reuseAliases && volumes[role] == nil && mounts[role] == nil {
					alias, err := managedMQTTPublicMountAlias(d.Object, c, sourceVolumes[role], sourceMounts[role])
					if err != nil {
						return err
					}
					if alias != "" {
						continue
					}
				}
				if volumes[role] != nil && deploymentplan.Digest(volumes[role]) != deploymentplan.Digest(sourceVolumes[role]) || mounts[role] != nil && deploymentplan.Digest(mounts[role]) != deploymentplan.Digest(sourceMounts[role]) {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "MQTT public mount collides with another original role: "+role)
				}
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{deploymentplan.Clone(sourceVolumes[role])})
				c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{deploymentplan.Clone(sourceMounts[role])})
			}
			stateVolume := environmentCreateServiceVolume("mqtt-bundle-state", "persistentVolumeClaim", claim)
			stateMount := environmentCreateServiceMount("mqtt-bundle-state", "/var/lib/mqtt-trust", false)
			if volumes["mqtt-bundle-state"] != nil && deploymentplan.Digest(volumes["mqtt-bundle-state"]) != deploymentplan.Digest(stateVolume) || mounts["mqtt-bundle-state"] != nil && deploymentplan.Digest(mounts["mqtt-bundle-state"]) != deploymentplan.Digest(stateMount) {
				return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "MQTT CRL cache is not on this consumer's original claim")
			}
			if c["name"] != "app" {
				session := ""
				if len(sessions) == 1 {
					session = sessions[0]
				} else if !declared {
					// Initial rendering may derive placement from its enrollment session.
					// Validation retains the declared producer/affinity instead: a
					// renewed registration ACK does not move the original RWO identity.
					session = mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")
				}
				name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
				producer := ""
				for _, candidate := range defs {
					if candidate.Object["kind"] != "Deployment" || deploymentplan.Map(candidate.Object["metadata"])["namespace"] != ns || deploymentplan.ID(candidate.Object) == deploymentplan.ID(d.Object) {
						continue
					}
					owns := false
					for _, volume := range certIssuerObjectList(environmentPKIPod(candidate.Object)["volumes"]) {
						if deploymentplan.Map(deploymentplan.Map(volume)["persistentVolumeClaim"])["claimName"] == claim {
							owns = true
						}
					}
					if owns {
						label := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(candidate.Object["spec"])["template"])["metadata"])["labels"])["app.kubernetes.io/name"])
						if producer != "" || label == "" {
							return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "same-PVC enrollment owner is ambiguous")
						}
						producer = label
					}
				}
				if session != "" {
					if !environmentBootstrapOwnerUUID.MatchString(session) {
						return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "product MQTT session is invalid")
					}
					role := "mqtt"
					if c["name"] == "shadowworker" {
						role = "shadow"
					}
					expected := "pki-product-" + role + "-" + strings.ReplaceAll(session, "-", "")[:12]
					if producer != "" && producer != expected {
						return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "same-PVC producer differs from its installed session")
					}
					producer = expected
				}
				if producer == "" && declared {
					existing := deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAffinity"])
					terms := certIssuerObjectList(existing["requiredDuringSchedulingIgnoredDuringExecution"])
					if len(terms) == 1 {
						term := deploymentplan.Map(terms[0])
						expressions := certIssuerObjectList(deploymentplan.Map(term["labelSelector"])["matchExpressions"])
						if len(expressions) == 1 {
							exp := deploymentplan.Map(expressions[0])
							values := certIssuerObjectList(exp["values"])
							role := "mqtt"
							if c["name"] == "shadowworker" {
								role = "shadow"
							}
							prefix := "pki-product-" + role + "-"
							if len(values) == 2 && values[0] == name {
								value := deploymentplan.String(values[1])
								suffix := strings.TrimPrefix(value, prefix)
								if strings.HasPrefix(value, prefix) && len(suffix) == 12 && strings.Trim(suffix, "0123456789abcdef") == "" {
									producer = value
								}
							}
						}
					}
				}
				if producer == "" {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "same-PVC enrollment lineage is unavailable")
				}
				labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"])["labels"]
				if deploymentplan.Map(labels)["app.kubernetes.io/name"] != name {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "self-affinity lacks its exact stable worker label")
				}
				term := deploymentplan.Object{"labelSelector": deploymentplan.Object{"matchExpressions": []any{deploymentplan.Object{"key": "app.kubernetes.io/name", "operator": "In", "values": []any{name, producer}}}}, "namespaces": []any{ns}, "topologyKey": "kubernetes.io/hostname"}
				affinity := deploymentplan.Map(pod["affinity"])
				if affinity == nil {
					affinity = deploymentplan.Object{}
					pod["affinity"] = affinity
				}
				expectedAffinity := deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{term}}
				if current := affinity["podAffinity"]; current != nil && deploymentplan.Digest(current) != deploymentplan.Digest(expectedAffinity) {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "RWO identity cache has a foreign affinity rule")
				}
				affinity["podAffinity"] = expectedAffinity
				security := deploymentplan.Map(pod["securityContext"])
				if security == nil {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native product runtime UID is unavailable")
				}
				if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native product runtime UID changed")
				}
				if group := security["fsGroup"]; group != nil && group != float64(10001) {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "foreign filesystem group cannot own original identity files")
				}
				delete(security, "fsGroup")
				security["supplementalGroups"] = []any{float64(0)}
			}
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{stateVolume})
			c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{stateMount})
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(values))
		}
	}

	controller := mqttFoundationTarget(defs, "Deployment", "pki-controller")
	var required []string
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if managedMQTTConsumerClaim(d.Object, c) == "" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "true" {
				continue
			}
			switch c["name"] {
			case "mqttfoundation":
				required = append(required, "mqtt")
			case "shadowworker":
				required = append(required, "shadow")
			}
		}
	}
	if len(required) > 0 {
		found := false
		for _, raw := range certIssuerObjectList(environmentPKIPod(controller)["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] != "pki-controller" {
				continue
			}
			found = true
			for _, key := range []string{"PKI_REQUIRED_CONSUMERS_MQTT"} {
				current := mqttFoundationEnv(c, key)

				if mqttFoundationEnv(c, "PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT") == "" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT": current}))
				}
				if current == "" && key == "PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT" {
					continue
				}
				if current == "" {
					return managedMQTTConsumerBlocker(deploymentplan.ID(controller), "original controller MQTT consumer policy is absent")
				}
				parts := strings.Split(current, ",")
				seen := map[string]bool{}
				for _, part := range parts {
					if strings.TrimSpace(part) != part || part == "" || seen[part] {
						return managedMQTTConsumerBlocker(deploymentplan.ID(controller), "original controller MQTT consumer policy is malformed")
					}
					seen[part] = true
				}
				for _, consumer := range required {
					if !seen[consumer] {
						parts = append(parts, consumer)
						seen[consumer] = true
					}
				}
				c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": key, "value": strings.Join(parts, ",")}})
			}
		}
		if !found {
			return managedMQTTConsumerBlocker(deploymentplan.ID(controller), "native MQTT controller consumer policy owner is unavailable")
		}
	}
	if reuseAliases {
		for _, d := range defs {
			for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(raw)
				if managedMQTTConsumerClaim(d.Object, c) == "" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "true" {
					continue
				}
				for _, role := range [][2]string{{"mqtt-pki-bundles", "roots.pem"}, {"mqtt-pki-crls", "crls.json"}, {"service-root", "root.pem"}} {
					if _, err := managedMQTTConsumerFile(d.Object, c, role[0], role[1]); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateEnvironmentManagedMQTTConsumers(defs []deploymentplan.Definition) error {
	expected := make([]deploymentplan.Definition, len(defs))
	for i, d := range defs {
		expected[i] = d
		expected[i].Object = deploymentplan.Clone(d.Object)
	}
	if err := bindEnvironmentManagedMQTTConsumersInternal(expected, true); err != nil {
		return err
	}
	for i, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if (managedMQTTConsumerClaim(d.Object, c) != "" || deploymentplan.ID(d.Object) == "Deployment/"+deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])+"/pki-controller") && deploymentplan.Digest(d.Object) != deploymentplan.Digest(expected[i].Object) {
				return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native MQTT destination, TLS pool or mounted identity differs from its selected broker")
			}
		}
	}
	return nil
}

func checkEnvironmentManagedMQTTConsumers(ctx context.Context, p *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error)) error {
	defs := []deploymentplan.Definition{}
	byID := map[string]deploymentplan.Resource{}
	for _, r := range p.Resources {
		if r.Action != "retire" {
			defs = append(defs, deploymentplan.Definition{Object: r.Desired})
			byID[r.ID] = r
		}
	}
	if err := validateEnvironmentManagedMQTTConsumers(defs); err != nil {
		return err
	}
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			claim := managedMQTTConsumerClaim(d.Object, c)
			if claim == "" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") == "" {
				continue
			}
			ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
			for _, role := range [][2]string{{"mqtt-pki-bundles", "roots.pem"}, {"mqtt-pki-crls", "crls.json"}, {"service-root", "root.pem"}} {
				file, err := managedMQTTConsumerFile(d.Object, c, role[0], role[1])
				if err != nil {
					return err
				}
				id := "ConfigMap/" + ns + "/" + file.reference
				r := byID[id]
				actual, err := read(ctx, id)
				if err != nil || actual == nil || r.Action != "retain" || r.UID == "" || deploymentplan.Map(actual["metadata"])["uid"] != r.UID {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "managed MQTT trust source owner changed")
				}
				data := deploymentplan.String(deploymentplan.Map(actual["data"])[file.key])
				if strings.TrimSpace(data) == "" || role[1] == "roots.pem" && !coreMQTTRootMatches(data, mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")) {
					return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "managed MQTT public trust data does not match its original binding")
				}
			}
			claimID := "PersistentVolumeClaim/" + ns + "/" + claim
			r := byID[claimID]
			actual, err := read(ctx, claimID)
			if err != nil || actual == nil || r.Action != "retain" || r.UID == "" || deploymentplan.Map(actual["metadata"])["uid"] != r.UID {
				return managedMQTTConsumerBlocker(deploymentplan.ID(d.Object), "native MQTT consumer original state claim changed")
			}
		}
	}
	return nil
}

// A managed client may already mount the same public ConfigMap under another
// role. Reuse that exact read-only mapping instead of mounting over its path.
func managedMQTTPublicMountAlias(object, container, sourceVolume, sourceMount deploymentplan.Object) (string, error) {
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return "", err
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return "", err
	}
	withoutName := func(o deploymentplan.Object) deploymentplan.Object {
		c := deploymentplan.Clone(o)
		delete(c, "name")
		return c
	}
	alias := ""
	for name, mount := range mounts {
		if mount["mountPath"] != sourceMount["mountPath"] {
			continue
		}
		if alias != "" || volumes[name] == nil || deploymentplan.Digest(withoutName(volumes[name])) != deploymentplan.Digest(withoutName(sourceVolume)) || deploymentplan.Digest(withoutName(mount)) != deploymentplan.Digest(withoutName(sourceMount)) {
			return "", managedMQTTConsumerBlocker(deploymentplan.ID(object), "MQTT public file path collides with a different original mount")
		}
		alias = name
	}
	return alias, nil
}

func managedMQTTConsumerFile(object, container deploymentplan.Object, role, key string) (coreMQTTMountedFile, error) {
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return coreMQTTMountedFile{}, err
	}
	if volumes[role] != nil || role != "service-root" {
		return coreMQTTFile(object, container, role, key)
	}
	// The binder has already proved this mapping identical to its original
	// public donor. Resolve the one file at the declared management CA path.
	var result coreMQTTMountedFile
	found := false
	for name := range volumes {
		file, err := coreMQTTFile(object, container, name, key)
		if err == nil && file.kind == "ConfigMap" && file.file == mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_MANAGEMENT_CA") {
			if found {
				return coreMQTTMountedFile{}, managedMQTTConsumerBlocker(deploymentplan.ID(object), "MQTT service Root file is ambiguous")
			}
			found, result = true, file
		}
	}
	if !found {
		return coreMQTTMountedFile{}, managedMQTTConsumerBlocker(deploymentplan.ID(object), "MQTT service Root file is unavailable")
	}
	return result, nil
}
