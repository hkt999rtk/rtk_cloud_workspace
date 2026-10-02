package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Image replacement is a separate active-service operation. The original
// ota-service-rollout continues to refuse an enabled edge or core cutover.
func requireActiveOTAServiceImageUpdateFlags(env, operator map[string]string) error {
	for key, want := range map[string]string{
		"LKE_OTA_SERVICE_REGISTRATION_ENABLED":   "true",
		"LKE_OTA_SERVICE_EDGE_ENABLED":           "true",
		"LKE_OTA_CORE_CUTOVER_ENABLED":           "true",
		"LKE_OTA_REGISTRAR_REGISTRATION_ENABLED": "false",
		"VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED":  "true",
	} {
		if operator[key] != want || lkeEnvValue(env, key) != want {
			return fmt.Errorf("active OTA image update requires canonical operator %s=%s", key, want)
		}
	}
	image := strings.TrimSpace(operator["LKE_VIDEO_CLOUD_IMAGE"])
	if !rolloutImagePattern.MatchString(image) || lkeEnvValue(env, "LKE_VIDEO_CLOUD_IMAGE") != image {
		return errors.New("active OTA image update requires the selected canonical immutable CI image pin")
	}
	return nil
}

func lkeUpdateActiveOTAServiceImage(store secretStore, env map[string]string, readOnly bool) error {
	if err := lkeRequireCanonicalOTAServiceIdentity(store, env); err != nil {
		return err
	}
	if err := lkeRequireCurrentOTACoreForImageUpdate(env, !readOnly); err != nil {
		return err
	}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		return err
	}
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err != nil {
		return err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	beforeSecrets, err := otaServiceRuntimeSecretState(env)
	if err != nil {
		return err
	}
	// Revalidate after the snapshot: validation must describe the same Secrets
	// whose UID and data are checked again after the rollout.
	if err := lkeRequireExistingOTAServiceInputs(env); err != nil {
		return err
	}
	deployment, err := kubectlResourceJSON(namespace, "deployment", otaServiceWorkloadName)
	if err != nil {
		return fmt.Errorf("inspect current OTA Service Deployment: %w", err)
	}
	image := env["LKE_VIDEO_CLOUD_IMAGE"]
	patch, already, oldImage, err := otaServiceImagePatch(deployment, image)
	if err != nil {
		return err
	}
	beforePods, err := otaServicePods(namespace)
	if err != nil {
		return err
	}
	if err := otaServiceCurrentReady(deployment, beforePods, oldImage, env); err != nil {
		return fmt.Errorf("current OTA Service: %w", err)
	}
	if readOnly {
		afterSecrets, err := otaServiceRuntimeSecretState(env)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(beforeSecrets, afterSecrets) {
			return errors.New("OTA Service runtime Secret UID or data changed during verification")
		}
		if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
			return err
		}
		if err := lkeRequireActiveOTADeviceEdgeRoute(env); err != nil {
			return err
		}
		if err := lkeRequireCurrentOTACoreForImageUpdate(env, false); err != nil {
			return err
		}
		if err := lkeRequireCanonicalOTAServiceIdentity(store, env); err != nil {
			return err
		}
		fmt.Printf("PASS active OTA Service currently ready on %s; selected immutable update needed=%t\n", oldImage, !already)
		return nil
	}
	expectedSpec, err := otaServiceSpecWithImage(deployment, image)
	if err != nil {
		return err
	}
	if !already {
		if err := runKubectl("-n", namespace, "patch", "deployment", otaServiceWorkloadName, "--type=json", "-p", patch); err != nil {
			return fmt.Errorf("replace OTA Service image: %w", err)
		}
	}
	if err := runKubectl("-n", namespace, "rollout", "status", "deployment/"+otaServiceWorkloadName, "--timeout", "5m"); err != nil {
		return err
	}
	updated, err := kubectlResourceJSON(namespace, "deployment", otaServiceWorkloadName)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedSpec, updated["spec"]) {
		return errors.New("OTA Service Deployment changed beyond the selected image")
	}
	updatedPods, err := otaServicePods(namespace)
	if err != nil {
		return err
	}
	if err := otaServiceCurrentReady(updated, updatedPods, image, env); err != nil {
		return fmt.Errorf("updated OTA Service: %w", err)
	}
	afterSecrets, err := otaServiceRuntimeSecretState(env)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(beforeSecrets, afterSecrets) {
		return errors.New("OTA Service runtime Secret UID or data changed during image update")
	}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		return err
	}
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err != nil {
		return err
	}
	if err := lkeRequireCanonicalOTAServiceIdentity(store, env); err != nil {
		return err
	}
	return lkeRequireCurrentOTACoreForImageUpdate(env, true)
}

func lkeRequireExistingOTAServiceInputs(env map[string]string) error {
	// Active image updates must validate existing material, never issue or
	// install a service identity as the registration rollout may do.
	root := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = ""
	defer func() { activeSecretEnvironmentRoot = root }()
	return lkeRequireOTAServiceInputs(env)
}

// A valid live service:ota certificate is insufficient if this environment's
// saved identity is different. Compare the existing record and Secret only;
// this image command never enrolls, adopts, installs or rotates an identity.
func lkeRequireCanonicalOTAServiceIdentity(store secretStore, env map[string]string) error {
	raw, err := store.read("pki/services/ota/identity.json")
	if err != nil {
		return errors.New("selected OTA service identity record is unavailable; restore it before image update")
	}
	var record deploymentServiceIdentity
	if json.Unmarshal([]byte(raw), &record) != nil || record.Version != 1 ||
		record.Environment != env["CLOUD_ENV_NAME"] || record.Stack != env["CLOUD_STACK_NAME"] ||
		record.Subject != "service:ota" || (record.Source != "enrolled" && record.Source != "adopted") ||
		record.RootSHA256 == "" || record.CertificateChain == "" || record.PrivateKey == "" || record.ServerCA == "" {
		return errors.New("selected OTA service identity record has the wrong scope or is incomplete")
	}
	chain, err := pemCertificates([]byte(record.CertificateChain))
	if err != nil || record.Fingerprint == "" || certificateSHA256(chain[0]) != record.Fingerprint {
		return errors.New("selected OTA service identity record fingerprint is invalid")
	}
	live, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", otaRegistrarIdentitySecretName)
	if err != nil {
		return errors.New("live OTA service identity Secret is unavailable")
	}
	for key, wanted := range map[string]string{
		"client.crt":    record.CertificateChain,
		"client.key":    record.PrivateKey,
		"server-ca.crt": record.ServerCA,
	} {
		have, err := kubernetesSecretBytes(live, key)
		if err != nil || !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace([]byte(wanted))) {
			return errors.New("live OTA service identity differs from the selected environment record; reconcile before image update")
		}
	}
	return nil
}

// Before updating an active service, the core must already forward device OTA
// requests to that service. Read-only qualification accepts the current pinned
// core image; apply requires the newly selected core image to be Ready first.
func lkeRequireCurrentOTACoreForImageUpdate(env map[string]string, requireSelected bool) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	deployment, err := kubectlResourceJSON(namespace, "deployment", "video-cloud-api")
	if err != nil {
		return fmt.Errorf("inspect live OTA core API: %w", err)
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	image := ""
	for _, raw := range anySlice(podSpec["containers"]) {
		container, _ := raw.(map[string]any)
		if container["name"] == "app" {
			if image != "" {
				return errors.New("live OTA core API has duplicate app containers")
			}
			image, _ = container["image"].(string)
		}
	}
	selected := env["LKE_VIDEO_CLOUD_IMAGE"]
	if !rolloutImagePattern.MatchString(image) || strings.SplitN(image, "@sha256:", 2)[0] != strings.SplitN(selected, "@sha256:", 2)[0] {
		return errors.New("live OTA core API is not a pinned release from the selected Video Cloud repository")
	}
	if requireSelected && image != selected {
		return errors.New("live OTA core API must reach the selected immutable image before OTA Service image update")
	}
	upstream := "http://" + otaServiceWorkloadName + "." + namespace + ".svc.cluster.local:18084"
	if _, ready, err := otaCoreCutoverPatch(deployment, image, upstream); err != nil || !ready {
		return fmt.Errorf("live OTA core API has not cut over to the independent service: %v", err)
	}
	pods, err := otaCoreCutoverPods(namespace)
	if err != nil {
		return err
	}
	if err := otaCoreCutoverCurrentReady(deployment, pods, image, upstream); err != nil {
		return fmt.Errorf("live OTA core API: %w", err)
	}
	return nil
}

type otaServiceSecretState struct {
	UID    string
	Digest [sha256.Size]byte
}

func otaServiceRuntimeSecretState(env map[string]string) (map[string]otaServiceSecretState, error) {
	names := []string{"video-cloud-runtime", "video-cloud-workers-runtime", otaRegistrarIdentitySecretName}
	if lkeOTADeliveryMode(env) == "cdn" {
		names = append(names, "ota-cdn-runtime")
	}
	states := make(map[string]otaServiceSecretState, len(names))
	for _, name := range names {
		secret, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", name)
		if err != nil {
			return nil, fmt.Errorf("inspect OTA runtime Secret %s: %w", name, err)
		}
		metadata, _ := secret["metadata"].(map[string]any)
		uid, _ := metadata["uid"].(string)
		data, ok := secret["data"].(map[string]any)
		if uid == "" || !ok {
			return nil, fmt.Errorf("OTA runtime Secret %s lacks UID or data", name)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("inspect OTA runtime Secret %s data: %w", name, err)
		}
		states[name] = otaServiceSecretState{UID: uid, Digest: sha256.Sum256(encoded)}
	}
	return states, nil
}

// A JSON patch tests the current version, container identity and exact old
// image. It cannot replace a concurrently changed Deployment or any other
// Pod-template, Service, policy, Secret or claim field.
func otaServiceImagePatch(deployment map[string]any, image string) (string, bool, string, error) {
	metadata, _ := deployment["metadata"].(map[string]any)
	version, _ := metadata["resourceVersion"].(string)
	if metadata["name"] != otaServiceWorkloadName || version == "" {
		return "", false, "", errors.New("OTA Service Deployment identity or resourceVersion changed")
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		return "", false, "", errors.New("OTA Service Deployment container layout changed")
	}
	container, _ := containers[0].(map[string]any)
	current, _ := container["image"].(string)
	if container["name"] != "otaservice" || !rolloutImagePattern.MatchString(current) || !rolloutImagePattern.MatchString(image) {
		return "", false, "", errors.New("OTA Service Deployment container or immutable image changed")
	}
	if strings.SplitN(current, "@sha256:", 2)[0] != strings.SplitN(image, "@sha256:", 2)[0] {
		return "", false, "", errors.New("OTA Service image repository differs from the current Video Cloud release")
	}
	if current == image {
		return "", true, current, nil
	}
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": "/spec/template/spec/containers/0/name", "value": "otaservice"},
		{"op": "test", "path": "/spec/template/spec/containers/0/image", "value": current},
		{"op": "replace", "path": "/spec/template/spec/containers/0/image", "value": image},
	}
	encoded, err := json.Marshal(operations)
	return string(encoded), false, current, err
}

func otaServiceSpecWithImage(deployment map[string]any, image string) (map[string]any, error) {
	encoded, err := json.Marshal(deployment["spec"])
	if err != nil {
		return nil, err
	}
	var spec map[string]any
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return nil, err
	}
	container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["image"] = image
	return spec, nil
}

func otaServicePods(namespace string) ([]map[string]any, error) {
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name="+otaServiceWorkloadName, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("inspect OTA Service Pods: %w", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("OTA Service Pod list is invalid")
	}
	return list.Items, nil
}

func otaServiceCurrentReady(deployment map[string]any, pods []map[string]any, image string, env map[string]string) error {
	metadata, _ := deployment["metadata"].(map[string]any)
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	strategy, _ := spec["strategy"].(map[string]any)
	generation, _ := metadata["generation"].(float64)
	observed, _ := status["observedGeneration"].(float64)
	if strategy["type"] != "Recreate" || generation < 1 || observed < generation || spec["replicas"] != float64(1) {
		return errors.New("OTA Service has not observed its current singleton Recreate revision")
	}
	for _, key := range []string{"replicas", "updatedReplicas", "readyReplicas", "availableReplicas"} {
		if status[key] != float64(1) {
			return fmt.Errorf("OTA Service %s is not current and ready", key)
		}
	}
	for _, key := range []string{"unavailableReplicas", "terminatingReplicas"} {
		if count, _ := status[key].(float64); count != 0 {
			return fmt.Errorf("OTA Service still has %s", key)
		}
	}
	template, _ := spec["template"].(map[string]any)
	if err := otaServiceTemplateMatches(template, image, env); err != nil {
		return fmt.Errorf("Deployment: %w", err)
	}
	live := 0
	for _, pod := range pods {
		podStatus, _ := pod["status"].(map[string]any)
		if podStatus["phase"] == "Succeeded" || podStatus["phase"] == "Failed" {
			continue
		}
		live++
		podMetadata, _ := pod["metadata"].(map[string]any)
		if podMetadata["deletionTimestamp"] != nil {
			return errors.New("OTA Service has a terminating live Pod")
		}
		if err := otaServiceTemplateMatches(pod, image, env); err != nil {
			return fmt.Errorf("live Pod: %w", err)
		}
		ready := false
		for _, raw := range anySlice(podStatus["conditions"]) {
			condition, _ := raw.(map[string]any)
			if condition["type"] == "Ready" && condition["status"] == "True" {
				ready = true
			}
		}
		if podStatus["phase"] != "Running" || !ready {
			return errors.New("OTA Service live Pod is not Running and Ready")
		}
	}
	if live != 1 {
		return errors.New("OTA Service must have exactly one current live Pod")
	}
	return nil
}

func otaServiceTemplateMatches(template map[string]any, image string, env map[string]string) error {
	metadata, _ := template["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations["rtk.realtek.com/runtime-checksum"] != lkeVideoCloudRuntimeChecksum(env) {
		return errors.New("runtime checksum differs from selected configuration")
	}
	spec, _ := template["spec"].(map[string]any)
	containers := anySlice(spec["containers"])
	if len(containers) != 1 {
		return errors.New("container layout changed")
	}
	container, _ := containers[0].(map[string]any)
	if container["name"] != "otaservice" || container["image"] != image {
		return errors.New("otaservice image differs from selected current release")
	}
	variables := anySlice(container["env"])
	wanted := map[string]string{
		"VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED":      "true",
		"VIDEO_CLOUD_OTA_SERVICE_ENABLED":            "true",
		"VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF":       "ota-service",
		"VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID":        otaRegistrarInstanceID,
		"VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON": env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"],
		"VIDEO_CLOUD_OTA_DELIVERY_MODE":              lkeOTADeliveryMode(env),
		"VIDEO_CLOUD_BLOB_ENDPOINT":                  env["VIDEO_CLOUD_BLOB_ENDPOINT"],
		"VIDEO_CLOUD_BLOB_REGION":                    env["VIDEO_CLOUD_BLOB_REGION"],
		"VIDEO_CLOUD_BLOB_BUCKET":                    env["VIDEO_CLOUD_BLOB_BUCKET"],
		"VIDEO_CLOUD_BLOB_PREFIX":                    env["VIDEO_CLOUD_BLOB_PREFIX"],
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL":   lkeAccountManagerInternalURL(env),
		"VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL":   "https://account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local:8443",
		"VIDEO_CLOUD_BILLING_USAGE_ENDPOINT":         "http://billing." + lkeNamespaceName(env, "billing") + ".svc.cluster.local:80/v1/internal/billing/usage-facts",
	}
	if lkeOTADeliveryMode(env) == "cdn" {
		wanted["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = env["VIDEO_CLOUD_OTA_CDN_BASE_URL"]
	}
	for key, value := range wanted {
		matches := 0
		for _, raw := range variables {
			variable, _ := raw.(map[string]any)
			if variable["name"] == key {
				matches++
				if variable["value"] != value || variable["valueFrom"] != nil {
					return fmt.Errorf("%s differs from selected runtime", key)
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must appear exactly once", key)
		}
	}
	for key, name := range map[string]string{
		"POSTGRES_PASSWORD": "video-cloud-runtime", "VIDEO_CLOUD_AUTH_SECRET": "video-cloud-runtime",
		"VIDEO_CLOUD_OTA_BFF_TOKEN": "video-cloud-runtime", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "video-cloud-runtime",
		"AWS_ACCESS_KEY_ID": "video-cloud-runtime", "AWS_SECRET_ACCESS_KEY": "video-cloud-runtime",
		"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "video-cloud-workers-runtime",
	} {
		if !otaServiceHasSecretRef(variables, key, name) {
			return fmt.Errorf("%s must reference the selected runtime Secret", key)
		}
	}
	if lkeOTADeliveryMode(env) == "cdn" && !otaServiceHasSecretRef(variables, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX", "ota-cdn-runtime") {
		return errors.New("OTA CDN token must reference the selected runtime Secret")
	}
	for _, raw := range anySlice(container["volumeMounts"]) {
		mount, _ := raw.(map[string]any)
		if mount["name"] == "platform-service-identity" && mount["mountPath"] == "/etc/video_cloud/platform-service" && mount["readOnly"] == true {
			for _, raw := range anySlice(spec["volumes"]) {
				volume, _ := raw.(map[string]any)
				secret, _ := volume["secret"].(map[string]any)
				if volume["name"] == "platform-service-identity" && secret["secretName"] == otaRegistrarIdentitySecretName {
					return nil
				}
			}
		}
	}
	return errors.New("OTA Service identity mount differs from selected service:ota Secret")
}

func otaServiceHasSecretRef(variables []any, key, name string) bool {
	matches := 0
	for _, raw := range variables {
		variable, _ := raw.(map[string]any)
		if variable["name"] != key {
			continue
		}
		from, _ := variable["valueFrom"].(map[string]any)
		ref, _ := from["secretKeyRef"].(map[string]any)
		if variable["value"] != nil || ref["name"] != name || ref["key"] != key || ref["optional"] == true {
			return false
		}
		matches++
	}
	return matches == 1
}
