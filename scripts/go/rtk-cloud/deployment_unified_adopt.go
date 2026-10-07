package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		if ns == "" && !strings.HasPrefix(name, b.Stack+"-") || ns != "" && !strings.HasPrefix(ns, b.Stack+"-") {
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
		if kind == "ValidatingWebhookConfiguration" && name == b.Stack+"-ingress-admission" {
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
					if mount["mountPath"] == "/usr/local/certificates" {
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
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		if name == "openbao" && kind == "StatefulSet" {
			pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
			raft := false
			for _, value := range certIssuerObjectList(pod["volumes"]) {
				volume := deploymentplan.Map(value)
				if volume["name"] == "raft-config" && deploymentplan.Map(volume["configMap"])["name"] == "openbao-raft" {
					raft = true
				}
			}
			if !raft || len(certIssuerObjectList(pod["containers"])) != 1 {
				return b, &deploymentplan.Blocker{ID: "deployment.compatibility.openbao", Resource: id, Cause: "existing OpenBao storage or PKI sidecars differ from the maintained target profile", Next: "qualify an in-place compatible OpenBao target that preserves existing data, seal and PKI references, or a separately verified storage transition; a local bindings file cannot resolve this incompatibility"}
			}
		}
		component := environmentManagedComponents[name]
		if component == "" {
			component = "existing-workload"
		}
		binding := environmentPodBindings{Kind: kind, NamespaceSuffix: strings.TrimPrefix(ns, b.Stack+"-"), Name: name, Component: component, PodFields: map[string]any{}}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		for _, field := range []string{"volumes", "serviceAccountName", "imagePullSecrets"} {
			if value, ok := pod[field]; ok {
				binding.PodFields[field] = value
			}
		}
		for _, value := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(value)
			bound := environmentContainerBindings{Name: deploymentplan.String(container["name"]), Fields: map[string]any{}}
			for key, image := range release.Images {
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
			if bound.ImageKey == "" {
				return b, &deploymentplan.Blocker{ID: "deployment.inventory.image-role", Resource: id, Cause: "an existing container has no maintained release image role", Next: "maintain this enabled component and its image role in the common catalog; do not copy its live workload spec or hand-edit generated bindings"}
			}
			for _, field := range []string{"volumeMounts", "envFrom"} {
				if value, ok := container[field]; ok {
					bound.Fields[field] = value
				}
			}
			var settings []any
			for _, value := range certIssuerObjectList(container["env"]) {
				setting := deploymentplan.Map(value)
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
			binding.Containers = append(binding.Containers, bound)
		}
		// An old launch command is never imported over the Raft renderer. The
		// compiler still requires a separate matched storage/seal migration.
		if component == "managed-openbao" {
			raft := false
			for _, resource := range live {
				if deploymentplan.ID(resource) == "ConfigMap/"+ns+"/openbao-raft" {
					want := environmentOpenBaoRaftDependencies(map[string]string{"CLOUD_STACK_NAME": b.Stack, "OPENBAO_EFFECTIVE_REPLICAS": fmt.Sprint(deploymentplan.Map(object["spec"])["replicas"])})[0]
					raft = deploymentplan.Digest(resource["data"]) == deploymentplan.Digest(want["data"])
				}
			}
			var mount deploymentplan.Object
			for _, value := range certIssuerObjectList(pod["volumes"]) {
				if item := deploymentplan.Map(value); item["name"] == "raft-config" {
					mount = item
				}
			}
			if !raft || deploymentplan.Map(mount["configMap"])["name"] != "openbao-raft" || len(binding.Containers) != 1 || binding.Containers[0].Name != "openbao" {
				return b, &deploymentplan.Blocker{ID: "deployment.compatibility.openbao", Resource: id, Cause: "existing OpenBao configuration differs from the maintained Raft target", Next: "qualify a compatible in-place target or an explicit matched storage/seal transition; preflight observations cannot prove this migration"}
			}
			binding.PodFields = map[string]any{}
			var identity []any
			for _, value := range certIssuerObjectList(binding.Containers[0].Fields["env"]) {
				if environmentIdentitySetting(deploymentplan.String(deploymentplan.Map(value)["name"])) {
					identity = append(identity, value)
				}
			}
			binding.Containers[0].Fields = map[string]any{}
			if len(identity) != 0 {
				binding.Containers[0].Fields["env"] = identity
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
