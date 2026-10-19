package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Observe only existing lifecycle references and owners. Target workload settings
// and UIDs for resources that have not been created are never inferred.
func observeEnvironmentBindings(environment string, release environmentRelease, live []deploymentplan.Object) (environmentIdentityBindings, error) {
	b := environmentIdentityBindings{Version: 1, Environment: environment, Stack: "video-cloud-" + environment, Owners: map[string]string{}}
	if release.Environment != environment || release.Stack != b.Stack {
		return b, errors.New("selected release and adoption environment differ")
	}
	for _, object := range live {
		meta := deploymentplan.Map(object["metadata"])
		id, kind := deploymentplan.ID(object), deploymentplan.String(object["kind"])
		ns, name := deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"])
		if ns == "" && !strings.HasPrefix(name, b.Stack+"-") && !environmentIngressAdmissionOwner(object, b.Stack) || ns != "" && !strings.HasPrefix(ns, b.Stack+"-") {
			return b, errors.New("adoption inventory contains an owner outside the selected environment")
		}
		if kind == "Pod" || kind == "ReplicaSet" {
			continue // Controller children are verified by the planner's lineage checks.
		}
		uid := deploymentplan.String(meta["uid"])
		label := deploymentplan.String(deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"])
		if uid == "" || meta["deletionTimestamp"] != nil || b.Owners[id] != "" || label != "" && label != b.Stack {
			return b, errors.New("adoption requires unique, active, exact environment owners")
		}
		b.Owners[id] = uid
		if kind == "ConfigMap" && name == environmentAppRoutingDeclaration {
			if err := observeEnvironmentAppRouting(&b, object, live); err != nil {
				return b, err
			}
		}
		if ns == b.Stack+"-video-cloud" && name == "video-cloud-otaregistrar" && (kind == "Deployment" || kind == "Service") {
			continue
		}
		if kind == "ConfigMap" && name == "pki-bootstrap-cleanup-binding" {
			if ns != b.Stack+"-video-cloud" || object["immutable"] != true || b.BootstrapCleanup != nil {
				return b, errors.New("bootstrap cleanup adoption requires one immutable environment owner record")
			}
			data := deploymentplan.Map(object["data"])
			var recorded environmentBootstrapCleanupBinding
			if decodeEnvironmentJSON([]byte(deploymentplan.String(data["binding.json"])), &recorded) != nil {
				return b, errors.New("bootstrap cleanup ownership record is invalid")
			}
			recorded.KubernetesCA = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(data["ca.crt"])))
			if validateEnvironmentBootstrapCleanupBinding(&recorded, environment) != nil {
				return b, errors.New("bootstrap cleanup adoption has foreign or invalid lifecycle references")
			}
			b.BootstrapCleanup = &recorded
		}
		if environmentIngressAdmissionOwner(object, b.Stack) {
			if b.IngressAdmission != nil && b.IngressAdmission.WebhookName != "" && b.IngressAdmission.WebhookName != name {
				return b, ingressAdmissionBlocker(b.Stack, "multiple webhook owners target the same admission Service")
			}
			for _, value := range certIssuerObjectList(object["webhooks"]) {
				client := deploymentplan.Map(deploymentplan.Map(value)["clientConfig"])
				service := deploymentplan.Map(client["service"])
				if service["namespace"] == b.Stack+"-ingress" && service["name"] == "ingress-nginx-controller-admission" {
					if b.IngressAdmission == nil {
						b.IngressAdmission = &environmentAdmissionBinding{}
					}
					ca := deploymentplan.String(client["caBundle"])
					if ca == "" || b.IngressAdmission.CABundle != "" && b.IngressAdmission.CABundle != ca {
						return b, errors.New("existing ingress admission trust is missing or ambiguous")
					}
					b.IngressAdmission.WebhookName = name
					b.IngressAdmission.CABundle = ca
				}
			}
		}
		if kind == "Deployment" && ns == b.Stack+"-ingress" && name == "ingress-nginx-controller" {
			pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
			mounted := map[string]bool{}
			for _, item := range certIssuerObjectList(pod["containers"]) {
				for _, value := range certIssuerObjectList(deploymentplan.Map(item)["volumeMounts"]) {
					mount := deploymentplan.Map(value)
					if path.Clean(deploymentplan.String(mount["mountPath"])) == "/usr/local/certificates" && mount["readOnly"] == true {
						mounted[deploymentplan.String(mount["name"])] = true
					}
				}
			}
			for _, value := range certIssuerObjectList(pod["volumes"]) {
				volume := deploymentplan.Map(value)
				if mounted[deploymentplan.String(volume["name"])] {
					if b.IngressAdmission == nil {
						b.IngressAdmission = &environmentAdmissionBinding{}
					}
					b.IngressAdmission.SecretName = deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"])
				}
			}
			continue
		}
		if kind == "Ingress" {
			if err := observeEnvironmentAppIngress(&b, object); err != nil {
				return b, err
			}
		}
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		if ns == b.Stack+"-support" {
			if err := validateZammadInventoryImages(object, release); err != nil {
				return b, err
			}
			continue // Chart owns support settings; observation records owners only.
		}
		component := environmentManagedComponents[name]
		if component == "" {
			component = "existing-workload"
		}
		if component == "managed-mqtt-foundation" {
			containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
			if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != "mqttfoundation" || len(certIssuerObjectList(environmentPKIPod(object)["initContainers"])) != 0 {
				return b, mqttFoundationBlocker("deployment.mqtt-foundation.inventory", id, "existing registrar has an unsupported consumer or initializer")
			}
		}
		binding := environmentPodBindings{Kind: kind, NamespaceSuffix: strings.TrimPrefix(ns, b.Stack+"-"), Name: name, Component: component, PodFields: map[string]any{}}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		if name == "certissuer" || name == "factoryenroll" {
			var err error
			binding.IssuerRuntimeSecret, err = observeEnvironmentIssuerRuntimeSecret(object, live)
			if err != nil {
				return b, err
			}
		}
		for _, field := range []string{"volumes", "serviceAccountName", "imagePullSecrets"} {
			if value, ok := pod[field]; ok {
				binding.PodFields[field] = value
			}
		}
		if accountManagerSocketConsumer(component, name) || name == "certissuer" && mqttFoundationEnv(mqttFoundationPrimary(object), "CERT_ISSUER_REGISTRY_ONLY_ENABLED") == "true" {
			if value, ok := pod["securityContext"]; ok {
				binding.PodFields["securityContext"] = value
			}
		}
		groups := []string{"containers"}
		if component == "managed-openbao" || component == "managed-turn" || component == "managed-app-api" || component == "managed-mqtt" || component == "managed-api" || existingMQTTStateConsumer(component, name) || accountManagerSocketConsumer(component, name) || component == "pki-controller" {
			groups = append(groups, "initContainers")
		}
		for _, group := range groups {
			for _, value := range certIssuerObjectList(pod[group]) {
				container := deploymentplan.Map(value)
				bound := environmentContainerBindings{Name: deploymentplan.String(container["name"]), Fields: map[string]any{}}
				if name == "video-cloud-api" && group == "initContainers" && bound.Name == coreIdentityInitializerName {
					image := deploymentplan.String(container["image"])
					if environmentImageRepository(image) != environmentImageRepository(release.Images["LKE_ALPINE_IMAGE"].Reference) {
						return b, coreIdentityPermissionBlocker(object, "original permission initializer has an unsupported image role")
					}
					if err := validateCoreIdentityInitializer(object, image); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_ALPINE_IMAGE"
					bound.Fields = controllerIdentityInitializerBindingFields(container)
					binding.InitContainers = append(binding.InitContainers, bound)
					continue
				}
				if component == "pki-controller" && group == "initContainers" {
					if err := validateObservedControllerIdentityInitializer(object, container, release); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_ALPINE_IMAGE"
					bound.Fields = controllerIdentityInitializerBindingFields(container)
					binding.InitContainers = append(binding.InitContainers, bound)
					continue
				}
				if selected, ok := release.Images["LKE_CERTISSUER_IMAGE"]; ok && component == "certissuer" && name == "certissuer" && group == "containers" && bound.Name == "certissuer" {
					if deploymentplan.String(container["image"]) != selected.Reference {
						return b, certIssuerPrepareCompletionBlock("current-native-role-image")
					}
					bound.ImageKey = "LKE_CERTISSUER_IMAGE"
				}
				if selected, ok := release.Images["LKE_PKI_CONTROLLER_IMAGE"]; ok && component == "pki-controller" && name == "pki-controller" && group == "containers" && bound.Name == "pki-controller" {
					if deploymentplan.String(container["image"]) != selected.Reference && deploymentplan.String(container["image"]) != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
						return b, postCleanupCorrectionBlock("current controller image differs from accepted correction")
					}
					bound.ImageKey = "LKE_PKI_CONTROLLER_IMAGE"
				}
				if selected, ok := release.Images["LKE_PKI_MANAGEMENT_IMAGE"]; ok && component == "existing-workload" && name == "account-manager" && group == "containers" && bound.Name == "pkimanagement" {
					image := deploymentplan.String(container["image"])
					if image != selected.Reference && image != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
						return b, errors.New("Account Manager sidecar is neither the formal old image nor selected management image")
					}
					bound.ImageKey = "LKE_PKI_MANAGEMENT_IMAGE"
				}
				for key, image := range release.Images {
					if key == "LKE_PKI_MANAGEMENT_IMAGE" || bound.ImageKey == "LKE_PKI_MANAGEMENT_IMAGE" {
						continue
					}
					if key == "LKE_CERTISSUER_IMAGE" || bound.ImageKey == "LKE_CERTISSUER_IMAGE" || key == "LKE_PKI_CONTROLLER_IMAGE" || bound.ImageKey == "LKE_PKI_CONTROLLER_IMAGE" {
						continue
					}
					if strings.HasPrefix(key, "LKE_ZAMMAD_") {
						continue
					}
					if environmentImageRepository(image.Reference) == environmentImageRepository(deploymentplan.String(container["image"])) {
						if bound.ImageKey != "" && release.Images[bound.ImageKey].Reference != image.Reference {
							return b, errors.New("consumer image has ambiguous release roles")
						}
						// Identical images may serve multiple catalog roles (Redis/Fleet).
						// Choose a stable alias; different target digests remain ambiguous.
						if bound.ImageKey == "" || key < bound.ImageKey {
							bound.ImageKey = key
						}
					}
				}
				if component == "managed-openbao" {
					switch bound.Name {
					case "openbao":
						bound.ImageKey = "LKE_OPENBAO_IMAGE"
					case "openbao-pki", "openbao-pki-install":
						bound.ImageKey = "LKE_OPENBAO_PKI_IMAGE"
					default:
						return b, openBaoCompatibilityBlocker(id, "existing OpenBao contains an unsupported container")
					}
				}
				if component == "managed-api" && group == "initContainers" {
					if err := observeDeviceInitializer(&binding, container, release, id); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
				}
				if existingMQTTStateConsumer(component, name) && group == "initContainers" {
					if err := validateObservedCoreMQTTInitializer(id, container, release); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
				}
				if accountManagerSocketConsumer(component, name) && group == "initContainers" {
					if err := validateObservedAccountManagerSocketInitializer(id, container, release); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_ALPINE_IMAGE"
				}
				if component == "managed-mqtt" && group == "initContainers" {
					if err := validateObservedMQTTInitializer(id, container, release); err != nil {
						return b, err
					}
					bound.ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
				}
				if component == "managed-app-api" && group == "initContainers" && bound.Name == "initialize-app-trust-state" {
					command := certIssuerObjectList(container["command"])
					if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-ec" || command[2] != environmentAppTrustInitialize && command[2] != environmentAppNativeTrustInitialize || len(certIssuerObjectList(container["args"])) != 0 {
						return b, environmentAppBlocker(id, "trust initializer differs from the supported preserve-on-retry command")
					}
					binding.NativeTrustInitialization = command[2] == environmentAppNativeTrustInitialize
					repository := environmentImageRepository(deploymentplan.String(container["image"]))
					if repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && repository != "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api" {
						return b, environmentAppBlocker(id, "trust initializer has an unknown image source")
					}
					bound.ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
				}
				if component == "managed-turn" {
					bound.ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
				}
				if bound.ImageKey == "" {
					return b, &deploymentplan.Blocker{ID: "deployment.inventory.image-role", Resource: id, Cause: "an existing container has no maintained release image role", Next: "maintain this enabled component and its image role in the common catalog; do not copy its live workload spec or hand-edit generated bindings"}
				}
				// Record absence too: an upgrade must not introduce a new inherited
				// credential source over the existing runtime configuration.
				bound.Fields["envFrom"] = container["envFrom"]
				for _, field := range []string{"volumeMounts"} {
					if value, ok := container[field]; ok {
						bound.Fields[field] = value
					}
				}
				var settings []any
				for _, value := range certIssuerObjectList(container["env"]) {
					setting := deploymentplan.Map(value)
					if environmentCoreMQTTConsumer(object, container) && mqttFoundationEnv(container, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != "" && derivedCoreMQTTSetting(deploymentplan.String(setting["name"])) {
						continue
					}
					if environmentControllerPolicySetting(deploymentplan.String(setting["name"])) {
						continue
					}
					if setting["valueFrom"] != nil || environmentIdentitySetting(deploymentplan.String(setting["name"])) {
						settings = append(settings, value)
					}
				}
				if len(settings) > 0 {
					bound.Fields["env"] = settings
				}
				if component == "managed-openbao" || accountManagerSocketConsumer(component, name) || name == "certissuer" && mqttFoundationEnv(container, "CERT_ISSUER_REGISTRY_ONLY_ENABLED") == "true" {
					if value, ok := container["securityContext"]; ok {
						bound.Fields["securityContext"] = value
					}
				}
				if group == "initContainers" {
					binding.InitContainers = append(binding.InitContainers, bound)
				} else {
					binding.Containers = append(binding.Containers, bound)
				}
			}
		}
		if component == "managed-turn" {
			profile, err := observeEnvironmentTurnProfile(object, release)
			if err != nil {
				return b, err
			}
			binding.Turn = profile
		}
		if component == "managed-openbao" {
			profile, err := observeOpenBaoProfile(object, live)
			if err != nil {
				return b, err
			}
			binding.OpenBao = profile
			if profile.Backend == "raft" {
				// The maintained Raft renderer owns its launch and mounts.
				binding.PodFields = map[string]any{}
				var identity []any
				for _, value := range certIssuerObjectList(binding.Containers[0].Fields["env"]) {
					name := deploymentplan.String(deploymentplan.Map(value)["name"])
					if environmentIdentitySetting(name) && !strings.HasPrefix(name, "BAO_") {
						identity = append(identity, value)
					}
				}
				binding.Containers[0].Fields = map[string]any{}
				if len(identity) != 0 {
					binding.Containers[0].Fields["env"] = identity
				}
			} else {
				for _, field := range []string{"securityContext", "shareProcessNamespace"} {
					if value, ok := pod[field]; ok {
						binding.PodFields[field] = value
					}
				}
			}
		}
		b.Pods = append(b.Pods, binding)
	}
	sort.Slice(b.Pods, func(i, j int) bool {
		return b.Pods[i].NamespaceSuffix+"/"+b.Pods[i].Name < b.Pods[j].NamespaceSuffix+"/"+b.Pods[j].Name
	})
	if len(b.Pods) == 0 {
		return b, errors.New("no existing environment workloads available for adoption")
	}
	if b.BootstrapCleanup != nil {
		if b.Owners["Namespace//"+b.Stack+"-video-cloud"] != b.BootstrapCleanup.NamespaceUID {
			return b, errors.New("bootstrap cleanup adoption namespace UID changed")
		}
		if uid := b.Owners["Secret/"+b.Stack+"-video-cloud/service-bootstrap-"+b.BootstrapCleanup.SessionID]; uid != "" && uid != b.BootstrapCleanup.SecretUID {
			return b, errors.New("bootstrap cleanup adoption Secret was replaced")
		}
	}
	return b, validateEnvironmentIdentityBindings(b, environment)
}

func archiveLegacyEnvironmentPlan(store secretStore) error {
	path := filepath.Join(store.Root, "deployment", "managed-upgrade.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return errors.New("legacy plan must be a bounded private regular file before archiving")
	}
	var legacy struct {
		Version     int    `json:"schema_version"`
		Environment string `json:"environment"`
		Stack       string `json:"stack"`
	}
	if json.Unmarshal(raw, &legacy) != nil || legacy.Version != 1 || legacy.Environment != store.Environment || legacy.Stack != "video-cloud-"+store.Environment {
		return errors.New("legacy archive must match schema 1 and the exact environment and stack")
	}
	directory := filepath.Join(filepath.Dir(path), "archive")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("real private environment archive directory required")
	}
	archive := filepath.Join(directory, fmt.Sprintf("managed-upgrade-%x.json", sha256.Sum256(raw)))
	if old, err := readBoundedEnvironmentFile(archive, true); err == nil && string(old) == string(raw) {
		return nil
	}
	return writeExclusivePrivateDeploymentJSON(archive, raw)
}

func runEnvironmentManagedAdoption(args []string) error {
	fmt.Fprintln(os.Stderr, "error: adopt-managed-state is retired; run check-deployment-preflight.sh --environment NAME to observe existing ownership and identity references and produce checked bindings")
	return exitCode(2)
}

func writeExclusivePrivateDeploymentJSON(path string, raw []byte) error {
	if !filepath.IsAbs(path) || len(raw) == 0 || len(raw) > 4<<20 {
		return errors.New("bounded absolute private JSON destination required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(raw) || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("cannot persist complete private JSON")
	}
	return nil
}

// Preflight evidence is generated output. It is never a target configuration input.
type environmentPreflightBindingsRecord struct {
	Schema         string                       `json:"schema"`
	Environment    string                       `json:"environment"`
	Operation      string                       `json:"operation"`
	Decision       string                       `json:"decision"`
	PlanSHA256     string                       `json:"plan_sha256,omitempty"`
	BindingsSHA256 string                       `json:"bindings_sha256,omitempty"`
	Observed       *environmentIdentityBindings `json:"observed,omitempty"`
}

func publishEnvironmentPreflightBindings(cfg deploymentConfig, c *compiledEnvironmentDeployment, decision string) error {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	record := environmentPreflightBindingsRecord{Schema: "rtk-cloud.preflight-bindings/v1", Environment: cfg.Environment, Decision: "NO-GO"}
	if c != nil && c.Plan != nil {
		record.Operation = c.Plan.Operation
		record.PlanSHA256 = c.Plan.SHA256()
		record.BindingsSHA256 = deploymentplan.Digest(c.Bindings)
		record.Observed = &c.Bindings
		if decision == "GO" {
			if record.BindingsSHA256 != c.Plan.BindingsSHA256 || validateEnvironmentIdentityBindings(c.Bindings, cfg.Environment) != nil {
				return errors.New("checked observations differ from the qualified plan")
			}
			record.Decision = "GO"
		}
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return store.write("deployment/identity-bindings.json", raw, true)
}

func verifyEnvironmentPreflightBindings(c *compiledEnvironmentDeployment) error {
	if c == nil || c.Plan == nil {
		return errors.New("checked deployment plan unavailable")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(c.Store.Root, "deployment", "identity-bindings.json"), true)
	var record environmentPreflightBindingsRecord
	if err != nil || decodeEnvironmentJSON(raw, &record) != nil || record.Schema != "rtk-cloud.preflight-bindings/v1" || record.Environment != c.Plan.Environment || record.Operation != c.Plan.Operation || record.Decision != "GO" || record.PlanSHA256 != c.Plan.SHA256() || record.Observed == nil || record.BindingsSHA256 != c.Plan.BindingsSHA256 || deploymentplan.Digest(*record.Observed) != c.Plan.BindingsSHA256 {
		return &deploymentplan.Blocker{ID: "deployment.preflight.bindings", Cause: "no successful preflight bindings match the freshly observed deployment plan", Next: "run full preflight for this environment and review its plan SHA-256; deployment does not produce or reuse stale GO evidence"}
	}
	return nil
}
