package main

import (
	"errors"
	"reflect"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Ordinary updates preserve lifecycle inputs, not an old desired workload spec.
// Release images, resources, probes and maintained controller policies can change.
// Identity, trust and durable-state remapping requires a separate PKI migration.
func validateEnvironmentPKITransition(definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	targets := map[string]deploymentplan.Definition{}
	for _, definition := range definitions {
		targets[deploymentplan.ID(definition.Object)] = definition
	}
	for _, current := range live {
		if current["kind"] != "Deployment" && current["kind"] != "StatefulSet" && current["kind"] != "DaemonSet" {
			continue
		}
		id := deploymentplan.ID(current)
		target, exists := targets[id]
		// The lifecycle compiler already verifies this exact original owner,
		// drained Pods, replacement and core cutover before adding retirement.
		// Check it before parsing a deliberately retired consumer.
		if exists && target.Retire && target.Phase == "retirement" && deploymentplan.Map(current["metadata"])["name"] == "video-cloud-otaregistrar" && deploymentplan.Map(current["spec"])["replicas"] == float64(0) && nonnegativeNumber(deploymentplan.Map(current["status"])["replicas"]) == 0 {
			continue
		}
		before := environmentPKIPod(current)
		component := environmentManagedComponents[deploymentplan.String(deploymentplan.Map(current["metadata"])["name"])]
		oldContainers, err := environmentPKIPodContainers(before)
		if err != nil || component != "" && len(oldContainers) == 0 {
			return environmentPKITransitionBlocker(id, "original container identities are ambiguous")
		}
		for name, old := range oldContainers {
			settings, err := environmentPKISettings(old["env"])
			if err != nil {
				return environmentPKITransitionBlocker(id, "original identity settings are ambiguous")
			}
			containerName := deploymentplan.String(old["name"])
			if len(settings) == 0 && containerName != "pkimanagement" && containerName != "pkibroker" && component == "" {
				continue
			}
			if !exists || target.Retire {
				return environmentPKITransitionBlocker(id, "existing PKI consumer cannot be removed through an ordinary update")
			}
			if target.Retain {
				continue
			}
			after := environmentPKIPod(target.Object)
			containers, err := environmentPKIPodContainers(after)
			if err != nil || containers[name] == nil {
				return environmentPKITransitionBlocker(id, "target removes or ambiguously replaces an existing PKI container")
			}
			wanted := containers[name]
			if containerName == "app" || component == "certissuer" {
				if err := validateEnvironmentAuthTransition(component, old, wanted, id); err != nil {
					return err
				}
			}
			wantedSettings, err := environmentPKISettings(wanted["env"])
			if environmentCoreMQTTConsumer(current, old) {
				if e := validateEnvironmentCoreMQTTTransport(definitions); e != nil {
					return e
				}
				for key := range settings {
					if derivedCoreMQTTSetting(key) {
						delete(settings, key)
					}
				}
				for key := range wantedSettings {
					if derivedCoreMQTTSetting(key) {
						delete(wantedSettings, key)
					}
				}
			}
			if err != nil || !reflect.DeepEqual(settings, wantedSettings) {
				keys := map[string]string{}
				for key := range settings {
					keys[key] = ""
				}
				for key := range wantedSettings {
					keys[key] = ""
				}
				var changed []string
				for _, key := range sortedMapKeys(keys) {
					if !reflect.DeepEqual(settings[key], wantedSettings[key]) {
						changed = append(changed, key)
					}
				}
				return environmentPKITransitionBlocker(id, "target changes identity or trust settings in container "+containerName+": "+strings.Join(changed, ", "))
			}
			oldSources, err := environmentPKIInheritedSources(old["envFrom"])
			newSources, wantedErr := environmentPKIInheritedSources(wanted["envFrom"])
			if err != nil || wantedErr != nil || !reflect.DeepEqual(oldSources, newSources) {
				return environmentPKITransitionBlocker(id, "target changes a PKI container's inherited configuration sources")
			}
			oldMounts, err := environmentPKINamedFields(old["volumeMounts"])
			if err != nil {
				return environmentPKITransitionBlocker(id, "original identity mounts are ambiguous")
			}
			newMounts, err := environmentPKINamedFields(wanted["volumeMounts"])
			if err != nil {
				return environmentPKITransitionBlocker(id, "target identity mounts are ambiguous")
			}
			oldVolumes, err := environmentPKIVolumeSources(current)
			if err != nil {
				return environmentPKITransitionBlocker(id, "original identity volume sources are ambiguous")
			}
			newVolumes, err := environmentPKIVolumeSources(target.Object)
			if err != nil {
				return environmentPKITransitionBlocker(id, "target identity volume sources are ambiguous")
			}
			for volume, mount := range oldMounts {
				newMount := newMounts[volume]
				if newMount == nil || oldVolumes[volume] == nil || newVolumes[volume] == nil {
					return environmentPKITransitionBlocker(id, "target removes an original identity or state mount")
				}
				for _, field := range []string{"mountPath", "subPath", "subPathExpr"} {
					if deploymentplan.String(mount[field]) != deploymentplan.String(newMount[field]) {
						return environmentPKITransitionBlocker(id, "target remaps an original identity or state path")
					}
				}
				if (mount["readOnly"] == true) != (newMount["readOnly"] == true) {
					return environmentPKITransitionBlocker(id, "target changes identity or state mount write access")
				}
				if !reflect.DeepEqual(environmentPKIVolumeSource(oldVolumes[volume]), environmentPKIVolumeSource(newVolumes[volume])) {
					return environmentPKITransitionBlocker(id, "target changes an original private-key, trust, CRL or durable-state volume source")
				}
			}
		}
	}
	return nil
}

func environmentPKITransitionBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.bindings", Resource: resource, Cause: cause, Next: "restore the original managed identity, issuer/trust settings, configuration references and state mounts; qualify any necessary remapping through a separate managed PKI migration before regenerating the complete plan; do not replace keys or reinitialize identity state"}
}

func environmentPKIPod(object deploymentplan.Object) deploymentplan.Object {
	return deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
}

func environmentPKINamedFields(value any) (map[string]deploymentplan.Object, error) {
	fields := map[string]deploymentplan.Object{}
	if value == nil {
		return fields, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("invalid named fields")
	}
	for _, value := range values {
		item := deploymentplan.Map(value)
		name := deploymentplan.String(item["name"])
		if name == "" || fields[name] != nil {
			return nil, errors.New("ambiguous named fields")
		}
		fields[name] = item
	}
	return fields, nil
}

func environmentPKISettings(value any) (map[string]deploymentplan.Object, error) {
	fields, err := environmentPKINamedFields(value)
	if err != nil {
		return nil, err
	}
	settings := map[string]deploymentplan.Object{}
	for name, setting := range fields {
		if !environmentIdentitySetting(name) {
			continue
		}
		if setting["valueFrom"] != nil {
			if deploymentplan.String(setting["value"]) != "" {
				return nil, errors.New("ambiguous identity source")
			}
			source := deploymentplan.Map(setting["valueFrom"])
			if source == nil {
				return nil, errors.New("invalid identity reference")
			}
			settings[name] = deploymentplan.Object{"valueFrom": environmentPKIReferenceDefaults(source)}
		} else {
			settings[name] = deploymentplan.Object{"value": deploymentplan.String(setting["value"])}
		}
	}
	return settings, nil
}

func environmentPKIVolumeSource(volume deploymentplan.Object) deploymentplan.Object {
	copy := deploymentplan.Clone(volume)
	delete(copy, "name")
	// API defaults for permissions and absent optional flags do not change the
	// original source. Key readability is independently qualified at startup.
	for _, kind := range []string{"secret", "configMap", "projected"} {
		source := deploymentplan.Map(copy[kind])
		delete(source, "defaultMode")
		if source["optional"] == false {
			delete(source, "optional")
		}
	}
	if source := deploymentplan.Map(copy["persistentVolumeClaim"]); source["readOnly"] == false {
		delete(source, "readOnly")
	}
	return copy
}

func environmentPKIReferenceDefaults(source deploymentplan.Object) deploymentplan.Object {
	copy := deploymentplan.Clone(source)
	for _, kind := range []string{"secretRef", "configMapRef", "secretKeyRef", "configMapKeyRef"} {
		ref := deploymentplan.Map(copy[kind])
		if ref["optional"] == false {
			delete(ref, "optional")
		}
	}
	if ref := deploymentplan.Map(copy["fieldRef"]); ref["apiVersion"] == "v1" {
		delete(ref, "apiVersion")
	}
	if deploymentplan.String(copy["prefix"]) == "" {
		delete(copy, "prefix")
	}
	return copy
}

func environmentPKIInheritedSources(value any) ([]deploymentplan.Object, error) {
	sources := []deploymentplan.Object{}
	if value == nil {
		return sources, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("invalid inherited identity sources")
	}
	for _, value := range values {
		source := deploymentplan.Map(value)
		if source == nil {
			return nil, errors.New("invalid inherited identity source")
		}
		sources = append(sources, environmentPKIReferenceDefaults(source))
	}
	return sources, nil
}

func environmentPKIPodContainers(pod deploymentplan.Object) (map[string]deploymentplan.Object, error) {
	containers := map[string]deploymentplan.Object{}
	for _, group := range []string{"containers", "initContainers"} {
		fields, err := environmentPKINamedFields(pod[group])
		if err != nil || group == "containers" && len(fields) == 0 {
			return nil, errors.New("invalid Pod container identities")
		}
		for name, container := range fields {
			containers[group+"/"+name] = container
		}
	}
	return containers, nil
}

func environmentPKIVolumeSources(object deploymentplan.Object) (map[string]deploymentplan.Object, error) {
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return nil, err
	}
	if object["kind"] == "StatefulSet" {
		for _, value := range certIssuerObjectList(deploymentplan.Map(object["spec"])["volumeClaimTemplates"]) {
			name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["metadata"])["name"])
			if name == "" || volumes[name] != nil {
				return nil, errors.New("ambiguous StatefulSet identity claim template")
			}
			// The same controller ID, ordinal and template name select the same
			// original claim. Native PVC/PV ownership is a separate storage proof.
			volumes[name] = deploymentplan.Object{"name": name, "persistentVolumeClaimTemplate": name}
		}
	}
	return volumes, nil
}
