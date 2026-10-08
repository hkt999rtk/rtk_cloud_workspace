package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentRelease struct {
	Schema            string                             `json:"schema"`
	Environment       string                             `json:"environment"`
	Stack             string                             `json:"stack"`
	WorkspaceRevision string                             `json:"workspace_revision"`
	Images            map[string]environmentReleaseImage `json:"images"`
}
type environmentReleaseImage struct {
	Reference      string `json:"reference"`
	SourceRevision string `json:"source_revision,omitempty"`
}

var immutableEnvironmentImage = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

func decodeEnvironmentJSON(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid environment JSON: unknown fields, invalid values or trailing input")
	}
	return nil
}

func readBoundedEnvironmentFile(path string, private bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4<<20 || (private && info.Mode().Perm() != 0600) {
		return nil, errors.New("bounded regular environment input with required permissions is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open bounded environment input")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("environment input owner changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("cannot read bounded environment input")
	}
	return raw, nil
}

// Identity bindings are lifecycle-owned mounts, references and public settings.
// They cannot carry image, replicas, resources, probes or a desired Deployment.
type environmentIdentityBindings struct {
	Version          int                                 `json:"schema_version"`
	Environment      string                              `json:"environment"`
	Stack            string                              `json:"stack"`
	Owners           map[string]string                   `json:"owners"`
	Pods             []environmentPodBindings            `json:"pods"`
	IngressAdmission *environmentAdmissionBinding        `json:"ingress_admission,omitempty"`
	BootstrapCleanup *environmentBootstrapCleanupBinding `json:"bootstrap_cleanup,omitempty"`
	AppIngress       *environmentAppIngressBinding       `json:"app_ingress,omitempty"`
}

type environmentAdmissionBinding struct {
	WebhookName string `json:"webhook_name,omitempty"`
	SecretName  string `json:"secret_name"`
	CABundle    string `json:"ca_bundle"`
}
type environmentPodBindings struct {
	Kind            string                         `json:"kind"`
	NamespaceSuffix string                         `json:"namespace_suffix"`
	Name            string                         `json:"name"`
	Component       string                         `json:"component"`
	PodFields       map[string]any                 `json:"pod_fields"`
	Containers      []environmentContainerBindings `json:"containers"`
	InitContainers  []environmentContainerBindings `json:"init_containers,omitempty"`
	OpenBao         *environmentOpenBaoProfile     `json:"openbao,omitempty"`
	Turn            *environmentTurnProfile        `json:"turn,omitempty"`
}
type environmentContainerBindings struct {
	Name     string         `json:"name"`
	ImageKey string         `json:"image_key"`
	Fields   map[string]any `json:"fields"`
}

func validateEnvironmentIdentityBindings(b environmentIdentityBindings, environment string) error {
	if b.Version != 1 || b.Environment != environment || b.Stack != "video-cloud-"+environment {
		return errors.New("deployment.bindings: exact environment and stack bindings required")
	}
	if err := validateEnvironmentBootstrapCleanupBinding(b.BootstrapCleanup, environment); err != nil {
		return err
	}
	seen := map[string]bool{}
	for id, uid := range b.Owners {
		parts := strings.Split(id, "/")
		if len(parts) != 3 || uid == "" || (parts[1] != "" && !strings.HasPrefix(parts[1], b.Stack+"-")) || (parts[1] == "" && !strings.HasPrefix(parts[2], b.Stack+"-") && !(parts[0] == "ValidatingWebhookConfiguration" && b.IngressAdmission != nil && parts[2] == b.IngressAdmission.WebhookName)) {
			return errors.New("deployment.bindings: invalid environment ownership adoption")
		}
	}
	podFields := keySet("volumes", "securityContext", "serviceAccountName", "automountServiceAccountToken", "imagePullSecrets")
	containerFields := keySet("volumeMounts", "securityContext", "envFrom", "env")
	for _, pod := range b.Pods {
		if (pod.Kind != "Deployment" && pod.Kind != "StatefulSet" && pod.Kind != "DaemonSet") || !secretEnvironmentPattern.MatchString(pod.NamespaceSuffix) || !secretEnvironmentPattern.MatchString(pod.Name) || !keySet("existing-workload", "certissuer", "pki-controller", "managed-api", "managed-app-api", "managed-mqtt-foundation", "managed-mqtt", "managed-openbao", "managed-turn", "service-issuer")[pod.Component] {
			return errors.New("deployment.bindings: unsupported managed component")
		}
		id := pod.Kind + "/" + b.Stack + "-" + pod.NamespaceSuffix + "/" + pod.Name
		if seen[id] || len(pod.Containers) == 0 {
			return errors.New("deployment.bindings: duplicate or empty managed component")
		}
		seen[id] = true
		if pod.Component != "managed-openbao" && pod.OpenBao != nil || pod.Component != "managed-openbao" && pod.Component != "managed-turn" && pod.Component != "managed-app-api" && len(pod.InitContainers) != 0 {
			return errors.New("deployment.bindings: OpenBao lifecycle references require the managed OpenBao component")
		}
		if pod.Component == "managed-app-api" {
			if err := validateEnvironmentAppBinding(pod); err != nil {
				return err
			}
		}
		if pod.Component == "managed-turn" {
			if err := validateEnvironmentTurnBinding(pod); err != nil {
				return err
			}
		} else if pod.Turn != nil {
			return errors.New("deployment.bindings: TURN endpoint references require the managed TURN component")
		}
		for field := range pod.PodFields {
			if !podFields[field] && !(pod.Component == "managed-openbao" && field == "shareProcessNamespace") {
				return errors.New("deployment.bindings cannot override workload settings")
			}
		}
		containers := map[string]bool{}
		for i, container := range append(append([]environmentContainerBindings{}, pod.Containers...), pod.InitContainers...) {
			if container.Name == "" || containers[container.Name] || !strings.HasSuffix(container.ImageKey, "_IMAGE") {
				return errors.New("deployment.bindings: exact container identity and release image key required")
			}
			containers[container.Name] = true
			if expected := environmentManagedImageKey(pod.Component, container.Name, i); expected != "" && container.ImageKey != expected {
				return errors.New("deployment.bindings: a managed consumer cannot exchange its maintained API, broker or provider image role")
			}
			for field := range container.Fields {
				if !containerFields[field] {
					return errors.New("deployment.bindings cannot carry images, commands, arguments, ports, replicas, resources or probes; move workload behavior into the maintained catalog")
				}
			}
			for _, value := range certIssuerObjectList(container.Fields["env"]) {
				setting := certIssuerObjectMap(value)
				name := certIssuerObjectString(setting["name"])
				if name == "" {
					return errors.New("deployment.bindings: invalid managed environment setting")
				}
				if environmentControllerPolicySetting(name) {
					return errors.New("deployment.bindings: API/issuer authentication, controller consumer and CRL worker policies belong in maintained configuration, not identity references")
				}
				if _, literal := setting["value"]; literal && !environmentIdentitySetting(name) {
					return errors.New("deployment.bindings: ordinary environment settings belong in maintained configuration, not private identity bindings")
				}
				if literal, ok := setting["value"].(string); ok && name == "PKI_TURN_CLI_PASSWORD_FILE" {
					// This is a container file reference, never the CLI password or
					// a controller-local file. Do not open it during preflight.
					if !environmentPrivateFileMount(pod, container, literal) {
						return environmentCredentialBindingBlocker(id, container.Name, name, "password file must be an absolute path on a read-only Secret or private staging volume")
					}
					continue
				}
				if literal, ok := setting["value"].(string); ok && literal != "" && (strings.Contains(name, "PASSWORD") || strings.Contains(name, "DSN") || strings.Contains(name, "DATABASE_URL") || strings.HasSuffix(name, "_TOKEN") || strings.HasSuffix(name, "_SECRET")) {
					return environmentCredentialBindingBlocker(id, container.Name, name, "credentials require Secret references, not inline values")
				}
				if name == "PKI_ENVIRONMENT" && setting["value"] != environment {
					return errors.New("deployment.bindings: managed identity belongs to another environment")
				}
			}
		}
	}
	return nil
}

func environmentCredentialBindingBlocker(id, container, setting, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.credential-reference", Resource: id, Cause: container + "/" + setting + ": " + cause, Next: "retain the original credential Secret and its private file mount; configuration contains references only; deployment initializes new credentials, while upgrades preserve existing credentials"}
}

func environmentPrivateFileMount(pod environmentPodBindings, container environmentContainerBindings, file string) bool {
	if !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n") {
		return false
	}
	for _, value := range certIssuerObjectList(container.Fields["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		root := deploymentplan.String(mount["mountPath"])
		if mount["readOnly"] != true || !path.IsAbs(root) || path.Clean(root) != root || (file != root && !strings.HasPrefix(file, strings.TrimSuffix(root, "/")+"/")) {
			continue
		}
		for _, value := range certIssuerObjectList(pod.PodFields["volumes"]) {
			volume := deploymentplan.Map(value)
			if volume["name"] == mount["name"] && (volume["secret"] != nil || volume["emptyDir"] != nil) {
				// emptyDir is a runtime private copy made by the init container
				// to enforce 0600; source Secret/admission checks remain separate.
				return true
			}
		}
	}
	return false
}

func environmentManagedImageKey(component, name string, index int) string {
	if component == "managed-turn" || component == "managed-app-api" {
		return "LKE_VIDEO_CLOUD_IMAGE"
	}
	if component == "managed-openbao" {
		if name == "openbao" {
			return "LKE_OPENBAO_IMAGE"
		}
		if name == "openbao-pki" || name == "openbao-pki-install" {
			return "LKE_OPENBAO_PKI_IMAGE"
		}
	}
	if index > 0 || component == "existing-workload" {
		if name == "pkibroker" || name == "pkimanagement" {
			return "LKE_VIDEO_CLOUD_IMAGE"
		}
		return ""
	}
	switch component {
	case "certissuer", "service-issuer", "pki-controller", "managed-api", "managed-mqtt-foundation":
		return "LKE_VIDEO_CLOUD_IMAGE"
	case "managed-mqtt":
		return "LKE_MQTT_PKI_IMAGE"
	case "managed-openbao":
		return "LKE_OPENBAO_IMAGE"
	}
	return ""
}

func applyEnvironmentPodBindings(object deploymentplan.Object, b environmentIdentityBindings, release environmentRelease) error {
	id := deploymentplan.ID(object)
	for _, binding := range b.Pods {
		if binding.Kind+"/"+b.Stack+"-"+binding.NamespaceSuffix+"/"+binding.Name != id {
			continue
		}
		pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(object["spec"])["template"])["spec"])
		if pod == nil {
			return errors.New("managed component has no Pod template")
		}
		template := deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])
		metadata := deploymentplan.Map(template["metadata"])
		if metadata == nil {
			metadata = map[string]any{}
			template["metadata"] = metadata
		}
		labels := deploymentplan.Map(metadata["labels"])
		if labels == nil {
			labels = map[string]any{}
			metadata["labels"] = labels
		}
		labels["rtk.cloud/pki-consumer"] = "true"
		for field, value := range binding.PodFields {
			if field == "volumes" || field == "imagePullSecrets" {
				pod[field] = mergeEnvironmentNamedFields(pod[field], value)
			} else {
				pod[field] = value
			}
		}
		boundNames := map[string]bool{}
		for _, bound := range binding.Containers {
			boundNames[bound.Name] = true
		}
		for _, value := range certIssuerObjectList(pod["containers"]) {
			if !boundNames[deploymentplan.String(deploymentplan.Map(value)["name"])] {
				return errors.New("deployment.bindings: binding would remove a rendered consumer")
			}
		}
		var containers []any
		for index, bound := range binding.Containers {
			var container map[string]any
			for _, value := range certIssuerObjectList(pod["containers"]) {
				c := certIssuerObjectMap(value)
				if c["name"] == bound.Name {
					container = c
				}
			}
			if container == nil {
				var err error
				container, err = environmentManagedSidecar(bound.Name)
				if err != nil {
					return &deploymentplan.Blocker{ID: "deployment.inventory.sidecar", Resource: id, Cause: "container " + bound.Name + " has no maintained sidecar definition", Next: "add its launch, health and image role to the shared renderer; retain existing credential/state references and repeat preflight"}
				}
			}
			image := release.Images[bound.ImageKey].Reference
			if image == "" {
				return &deploymentplan.Blocker{ID: "deployment.release.image-role", Resource: id, Cause: "container " + bound.Name + " lacks selected role " + bound.ImageKey, Next: "publish and select the exact formal CI image for this maintained container role; repeat preflight"}
			}
			if expected := environmentManagedImageKey(binding.Component, bound.Name, index); expected != "" && bound.ImageKey != expected {
				return errors.New("deployment.release: consumer image role differs from the maintained catalog")
			}
			if binding.Component == "existing-workload" {
				if original := deploymentplan.String(container["image"]); original != "" && environmentImageRepository(original) != environmentImageRepository(image) {
					return errors.New("deployment.release: identity bindings cannot replace an existing workload's application image role")
				}
			}
			container["image"] = image
			for field, value := range bound.Fields {
				if field == "env" || field == "volumeMounts" || field == "ports" {
					container[field] = mergeEnvironmentNamedFields(container[field], value)
				} else {
					container[field] = value
				}
			}
			containers = append(containers, container)
		}
		pod["containers"] = containers
		if binding.Component == "managed-openbao" || binding.Component == "managed-turn" || binding.Component == "managed-app-api" {
			for _, bound := range binding.InitContainers {
				var container map[string]any
				for _, v := range certIssuerObjectList(pod["initContainers"]) {
					if c := deploymentplan.Map(v); c["name"] == bound.Name {
						container = c
					}
				}
				image := release.Images[bound.ImageKey].Reference
				if container == nil || !immutableEnvironmentImage.MatchString(image) {
					if binding.Component == "managed-turn" {
						return turnDeploymentBlocker(id, "private-file initializer lacks its selected formal CI image")
					}
					return errors.New("deployment.release: managed OpenBao installer lacks its formal CI image")
				}
				container["image"] = image
				for field, value := range bound.Fields {
					container[field] = value
				}
			}
		}
		return nil
	}
	return nil
}

func environmentIdentitySetting(name string) bool {
	switch name {
	case "VIDEO_CLOUD_WEBRTC_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_WEBRTC_SERVICE_ENDPOINT_REF", "VIDEO_CLOUD_WEBRTC_SERVICE_REGISTRATION_URL", "VIDEO_CLOUD_WEBRTC_SERVICE_CLIENT_CERT", "VIDEO_CLOUD_WEBRTC_SERVICE_CLIENT_KEY", "VIDEO_CLOUD_WEBRTC_SERVICE_SERVER_CA":
		return true
	case "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENDPOINT_REF", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_CERT", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_KEY", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_SERVER_CA":
		return true
	case "VIDEO_CLOUD_SHADOW_WORKER_INSTANCE_ID", "VIDEO_CLOUD_SHADOW_WORKER_ENDPOINT_REF", "VIDEO_CLOUD_SHADOW_WORKER_REGISTRATION_URL", "VIDEO_CLOUD_SHADOW_WORKER_CLIENT_CERT", "VIDEO_CLOUD_SHADOW_WORKER_CLIENT_KEY", "VIDEO_CLOUD_SHADOW_WORKER_SERVER_CA":
		return true
	case "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF", "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL", "VIDEO_CLOUD_OTA_SERVICE_CLIENT_CERT", "VIDEO_CLOUD_OTA_SERVICE_CLIENT_KEY", "VIDEO_CLOUD_OTA_SERVICE_SERVER_CA":
		return true
	case "VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID", "VIDEO_CLOUD_MQTT_FOUNDATION_ENDPOINT_REF", "VIDEO_CLOUD_MQTT_FOUNDATION_REGISTRATION_URL", "VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_CERT", "VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_KEY", "VIDEO_CLOUD_MQTT_FOUNDATION_SERVER_CA":
		return true
	case "BAO_ADDR", "BAO_API_ADDR", "BAO_CLUSTER_ADDR", "BAO_CACERT", "BAO_TLS_SERVER_NAME", "CERT_ISSUER_SERVER_CERT", "CERT_ISSUER_SERVER_KEY", "CERT_ISSUER_CLIENT_CA", "CERT_ISSUER_CA_PROVIDER":
		return true
	}
	if environmentControllerPolicySetting(name) {
		return false
	}
	for _, prefix := range []string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_", "PKI_", "OPENBAO_", "EMQX_PKI_", "CERT_ISSUER_SERVICE_", "CERT_ISSUER_HOST_", "CERT_ISSUER_SERVER_PKI_", "CERT_ISSUER_MQTT_SERVER_PKI_", "FACTORY_ENROLL_CERT_ISSUER_", "VIDEO_CLOUD_API_TLS_", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_", "VIDEO_CLOUD_AUTH_APP_", "VIDEO_CLOUD_SERVICE_PKI_", "VIDEO_CLOUD_SERVER_PKI_", "VIDEO_CLOUD_CONTROLLER_PKI_", "VIDEO_CLOUD_AUTH_DEVICE_CA_", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_", "VIDEO_CLOUD_MQTT_SERVER_PKI_", "VIDEO_CLOUD_MQTT_BUNDLE_", "APP_PKI_", "SERVER_PKI_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func environmentManagedSidecar(name string) (map[string]any, error) {
	container := map[string]any{"name": name, "resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]any{"memory": "512Mi"}}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
	switch name {
	case "pkibroker":
		container["command"] = []any{"/bin/sh", "-ec", "i=0; until /app/pkibroker prepare; do i=$((i+1)); [ \"$i\" -lt 30 ] || exit 1; sleep 2; done; exec /app/pkibroker watch"}
	case "pkimanagement":
		container["command"] = []any{"/app/pkimanagement"}
	default:
		return nil, &deploymentplan.Blocker{ID: "deployment.inventory.sidecar", Resource: name, Cause: "container has no maintained sidecar definition", Next: "add its launch, health and formal image role to the shared renderer; retain existing credential/state references and repeat preflight"}
	}
	return container, nil
}

func mergeEnvironmentNamedFields(base, overrides any) []any {
	result := append([]any{}, certIssuerObjectList(base)...)
	for _, value := range certIssuerObjectList(overrides) {
		name := deploymentplan.String(deploymentplan.Map(value)["name"])
		replaced := false
		for i, existing := range result {
			if deploymentplan.Map(existing)["name"] == name && name != "" {
				result[i] = value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, value)
		}
	}
	return result
}
