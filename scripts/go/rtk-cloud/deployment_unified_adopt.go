package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Adoption extracts lifecycle references and exact owners, never a desired spec.
// The result is reviewable input to the common renderer, not readiness evidence.
func adoptEnvironmentBindings(environment string, release environmentRelease, live []deploymentplan.Object) (environmentIdentityBindings, error) {
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
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		component := environmentManagedComponents[name]
		if component == "" {
			component = "existing-workload"
		}
		binding := environmentPodBindings{Kind: kind, NamespaceSuffix: strings.TrimPrefix(ns, b.Stack+"-"), Name: name, Component: component, PodFields: map[string]any{}}
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
		for _, field := range []string{"volumes", "securityContext", "serviceAccountName", "automountServiceAccountToken", "imagePullSecrets"} {
			if value, ok := pod[field]; ok {
				binding.PodFields[field] = value
			}
		}
		for _, value := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(value)
			bound := environmentContainerBindings{Name: deploymentplan.String(container["name"]), Fields: map[string]any{}}
			for key, image := range release.Images {
				if environmentImageRepository(image.Reference) == environmentImageRepository(deploymentplan.String(container["image"])) {
					if bound.ImageKey != "" {
						return b, errors.New("consumer image has ambiguous release roles")
					}
					bound.ImageKey = key
				}
			}
			if bound.ImageKey == "" {
				return b, errors.New("consumer has no maintained immutable release image role")
			}
			for _, field := range []string{"volumeMounts", "securityContext", "envFrom"} {
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
				return b, errors.New("OpenBao adoption requires existing shared Raft configuration; file storage needs a separate matched migration and original-seal qualification")
			}
			binding.PodFields = map[string]any{}
			binding.Containers[0].Fields = map[string]any{}
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
	fs := flag.NewFlagSet("deployment adopt-managed-state", flag.ContinueOnError)
	environment := fs.String("environment", "", "maintained environment")
	workspace := fs.String("workspace", "", "workspace root")
	root := fs.String("environment-root", "", "explicit maintained environment root")
	output := fs.String("output", "identity-bindings.candidate.json", "new basename in the environment SecretStore deployment directory")
	timeout := fs.Duration("timeout", 10*time.Minute, "read-only inventory deadline")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return exitCode(2)
	}
	if fs.NArg() != 0 || *environment == "" || *timeout <= 0 || filepath.Base(*output) != *output || !strings.HasSuffix(*output, ".json") {
		fmt.Fprintln(os.Stderr, "error: environment, positive timeout and a JSON output basename are required")
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *root)
	if err != nil {
		return exitCode(2)
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return exitCode(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	release, err := loadEnvironmentCIImages(ctx, cfg, environmentCIImageCommand(cfg))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: selected immutable release unavailable")
		return exitCode(1)
	}
	kubeconfig, err := readSensitiveFile(store.KubeconfigPath(), "selected environment kubeconfig")
	if err != nil {
		return exitCode(1)
	}
	backend, err := newEnvironmentKubernetesBackend(kubeconfig, cfg.Environment)
	if err != nil {
		return exitCode(1)
	}
	live, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: exact environment inventory unavailable")
		return exitCode(1)
	}
	bindings, err := adoptEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitCode(1)
	}
	// Confirm all recorded owners and lifecycle content immediately before
	// publication; a concurrently changed installation is not adoption evidence.
	fresh, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return exitCode(1)
	}
	verified, err := adoptEnvironmentBindings(cfg.Environment, release, fresh)
	if err != nil || deploymentplan.Digest(verified) != deploymentplan.Digest(bindings) {
		fmt.Fprintln(os.Stderr, "error: ownership or lifecycle bindings changed; repeat adoption")
		return exitCode(1)
	}
	directory := filepath.Join(store.Root, "deployment")
	info, err := os.Lstat(store.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return exitCode(1)
	}
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return exitCode(1)
	}
	info, err = os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return exitCode(1)
	}
	raw, _ := json.MarshalIndent(bindings, "", "  ")
	if err := archiveLegacyEnvironmentPlan(store); err != nil {
		fmt.Fprintln(os.Stderr, "error: legacy plan cannot be preserved in the private environment archive")
		return exitCode(1)
	}
	if err := writeExclusivePrivateDeploymentJSON(filepath.Join(directory, *output), raw); err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot publish new private bindings; existing files are never overwritten")
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "ADOPTION CANDIDATE environment=%s bindings_sha256=%s; review lifecycle references and maintained configuration differences, then run full preflight; this is not GO\n", cfg.Environment, deploymentplan.Digest(bindings))
	return nil
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
