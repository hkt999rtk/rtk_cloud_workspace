package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func zammadBlocker(id, resource, cause string) error {
	return &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: "repair the shared Zammad chart/values and immutable ZAMMAD_*_IMAGE settings; retain original Secrets, PVCs and database versions, then repeat full preflight"}
}

func zammadImageRole(image string) string {
	roles := map[string]string{"ghcr.io/zammad/zammad": "IMAGE", "library/memcached": "MEMCACHED_IMAGE", "library/redis": "REDIS_IMAGE", "library/postgres": "POSTGRES_IMAGE", "bitnamilegacy/elasticsearch": "ELASTICSEARCH_IMAGE", "bitnamilegacy/os-shell": "OS_SHELL_IMAGE", "library/alpine": "ALPINE_IMAGE"}
	if role := roles[environmentImageRepository(image)]; role != "" {
		return "LKE_ZAMMAD_" + role
	}
	return ""
}

func environmentZammadPod(object deploymentplan.Object) deploymentplan.Object {
	spec := deploymentplan.Map(object["spec"])
	if object["kind"] == "CronJob" {
		spec = deploymentplan.Map(deploymentplan.Map(spec["jobTemplate"])["spec"])
	}
	return deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
}

func canonicalZammadPodSecurityContext(object deploymentplan.Object) {
	security := deploymentplan.Map(environmentZammadPod(object)["securityContext"])
	for _, field := range []string{"supplementalGroups", "sysctls"} {
		if values, ok := security[field].([]any); ok && len(values) == 0 {
			// The typed API omits empty optional lists. Keep an explicit null in
			// desired intent so admission still rejects added groups or sysctls.
			security[field] = nil
		}
	}
}

func validateZammadInventoryImages(object deploymentplan.Object, release environmentRelease) error {
	if !strings.HasPrefix(deploymentplan.String(deploymentplan.Map(object["metadata"])["name"]), "zammad-") {
		return zammadBlocker("deployment.inventory.support", deploymentplan.ID(object), "support namespace contains an unknown workload owner")
	}
	for _, group := range []string{"containers", "initContainers"} {
		for _, v := range certIssuerObjectList(environmentZammadPod(object)[group]) {
			container := deploymentplan.Map(v)
			role := zammadImageRole(deploymentplan.String(container["image"]))
			if role == "" || !immutableEnvironmentImage.MatchString(release.Images[role].Reference) {
				return zammadBlocker("deployment.inventory.image-role", deploymentplan.ID(object), "enabled Zammad container has no maintained image role: "+deploymentplan.String(container["name"]))
			}
		}
	}
	return nil
}

func readEnvironmentZammadInputs(cfg deploymentConfig) ([]byte, []byte, error) {
	version := cfg.Values["ZAMMAD_CHART_VERSION"]
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return nil, nil, zammadBlocker("deployment.configuration.support", "", "an exact shared ZAMMAD_CHART_VERSION is required")
	}
	root := filepath.Join(cfg.Workspace, "cloud_deploy", "architectures", "kubernetes")
	values, ve := readBoundedEnvironmentFile(filepath.Join(root, "zammad-values.yaml"), false)
	chart, ce := readBoundedEnvironmentFile(filepath.Join(root, "charts", "zammad-"+version+".tgz"), false)
	if ve != nil || ce != nil || fmt.Sprintf("%x", sha256.Sum256(chart)) != cfg.Values["ZAMMAD_CHART_SHA256"] {
		return nil, nil, zammadBlocker("deployment.configuration.support", "", "maintained Zammad values or SHA-256-pinned official chart is unavailable or changed")
	}
	return values, chart, nil
}

// Helm only renders the fixed bundled chart. The normal plan/admission/executor
// owns every write; no Helm upgrade, dependency download or cluster lookup runs.
func renderEnvironmentZammad(ctx context.Context, cfg deploymentConfig, store secretStore, release environmentRelease, operation string, values, chart []byte) ([]deploymentplan.Definition, error) {
	if cfg.Values["SUPPORT_TICKETS_ENABLED"] != "true" {
		return nil, nil
	}
	if len(values) == 0 || len(chart) == 0 {
		return nil, zammadBlocker("deployment.configuration.support", "", "complete frozen Zammad inputs are required")
	}
	for _, key := range environmentProviderImageKeys {
		if strings.HasPrefix(key, "LKE_ZAMMAD_") && !immutableEnvironmentImage.MatchString(release.Images[key].Reference) {
			return nil, zammadBlocker("deployment.release.support", key, "enabled Zammad image is not pinned")
		}
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "tmp")} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return nil, err
		}
	}
	directory, err := os.MkdirTemp(filepath.Join(store.Root, "deployment", "tmp"), "zammad-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	for name, raw := range map[string][]byte{"chart.tgz": chart, "values.yaml": values, "kubeconfig": []byte("apiVersion: v1\nkind: Config\n")} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
			return nil, err
		}
	}
	args := []string{"template", "zammad", filepath.Join(directory, "chart.tgz"), "--namespace", release.Stack + "-support", "--skip-tests", "--kubeconfig", filepath.Join(directory, "kubeconfig"), "--values", filepath.Join(directory, "values.yaml")}
	if operation == "upgrade" {
		args = append(args, "--is-upgrade")
	}
	command := exec.CommandContext(ctx, "helm", args...)
	command.Env = append(os.Environ(), "HELM_CACHE_HOME="+directory, "HELM_CONFIG_HOME="+directory, "HELM_DATA_HOME="+directory, "HELM_PLUGINS="+filepath.Join(directory, "plugins"), "KUBECONFIG="+filepath.Join(directory, "kubeconfig"), "HELM_NAMESPACE="+release.Stack+"-support")
	var output environmentAPIOutput
	command.Stdout = &output
	if command.Run() != nil {
		return nil, zammadBlocker("deployment.renderer.support", "", "Helm could not render the fixed Zammad chart; verify Helm is installed and maintained values are valid")
	}
	decoder := yaml.NewDecoder(strings.NewReader(output.buffer.String()))
	var definitions []deploymentplan.Definition
	for {
		var object deploymentplan.Object
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			return nil, errors.New("deployment.renderer.support: invalid chart output")
		}
		if len(object) == 0 {
			continue
		}
		raw, _ := json.Marshal(object)
		if json.Unmarshal(raw, &object) != nil {
			return nil, errors.New("invalid chart API shape")
		}
		canonicalZammadPodSecurityContext(object)
		meta := deploymentplan.Map(object["metadata"])
		if meta["namespace"] != release.Stack+"-support" || object["kind"] == "Ingress" || object["kind"] == "Secret" {
			return nil, zammadBlocker("deployment.target.support", deploymentplan.ID(object), "Zammad must use only the support namespace, private Services and existing credential references")
		}
		if object["kind"] == "Service" {
			kind := deploymentplan.String(deploymentplan.Map(object["spec"])["type"])
			if kind != "" && kind != "ClusterIP" {
				return nil, zammadBlocker("deployment.target.support", deploymentplan.ID(object), "Zammad public Service exposure is not permitted")
			}
		}
		phase := environmentResourcePhase(deploymentplan.String(object["kind"]))
		// Data dependencies must be ready before the chart's initialization Job.
		if object["kind"] == "StatefulSet" || meta["name"] == "zammad-redis" || meta["name"] == "zammad-memcached" {
			phase = "configuration"
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: phase})
	}
	if err := pinEnvironmentResourceImages(definitions, release); err != nil {
		return nil, err
	}
	for i := range definitions {
		object := definitions[i].Object
		if object["kind"] == "Job" {
			// Content-bound name permits a reviewed migration without reusing a
			// different immutable Job. Keep it for final acceptance/journal fences.
			spec := deploymentplan.Map(object["spec"])
			delete(spec, "ttlSecondsAfterFinished")
			deploymentplan.Map(object["metadata"])["name"] = "zammad-init-" + deploymentplan.Digest(struct {
				Spec          any
				Values, Chart []byte
			}{spec, values, chart})[:16]
			definitions[i].Dependencies = []string{"StatefulSet/" + release.Stack + "-support/zammad-postgres", "StatefulSet/" + release.Stack + "-support/zammad-elasticsearch-master", "Deployment/" + release.Stack + "-support/zammad-redis", "Deployment/" + release.Stack + "-support/zammad-memcached"}
		}
	}
	return definitions, nil
}

// The shared chart determines settings. Existing lifecycle allocations and
// Secret payloads stay under their original owners; unknown transitions block.
func bindZammadLifecycle(definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	for i := range definitions {
		object := definitions[i].Object
		for _, current := range live {
			if deploymentplan.ID(current) != deploymentplan.ID(object) {
				continue
			}
			if object["kind"] != "StatefulSet" {
				continue
			}
			want, old := deploymentplan.Map(object["spec"]), deploymentplan.Map(current["spec"])
			if want["serviceName"] != old["serviceName"] || deploymentplan.Digest(want["selector"]) != deploymentplan.Digest(old["selector"]) {
				return zammadBlocker("deployment.compatibility.support", deploymentplan.ID(object), "Zammad immutable workload identity differs from the chart target")
			}
			claims := certIssuerObjectList(old["volumeClaimTemplates"])
			target := certIssuerObjectList(want["volumeClaimTemplates"])
			if len(claims) != len(target) {
				return zammadBlocker("deployment.compatibility.support", deploymentplan.ID(object), "Zammad durable claim inventory differs")
			}
			for n, v := range claims {
				a, b := deploymentplan.Map(v), deploymentplan.Map(target[n])
				sa, sb := deploymentplan.Map(a["spec"]), deploymentplan.Map(b["spec"])
				if deploymentplan.Map(a["metadata"])["name"] != deploymentplan.Map(b["metadata"])["name"] || deploymentplan.Digest(sa["accessModes"]) != deploymentplan.Digest(sb["accessModes"]) || deploymentplan.Digest(deploymentplan.Map(sa["resources"])["requests"]) != deploymentplan.Digest(deploymentplan.Map(sb["resources"])["requests"]) {
					return zammadBlocker("deployment.compatibility.support", deploymentplan.ID(object), "Zammad durable claim layout or size changes require a separate migration")
				}
			}
			want["volumeClaimTemplates"] = deploymentplan.Clone(current)["spec"].(map[string]any)["volumeClaimTemplates"]
			if old["podManagementPolicy"] != nil {
				want["podManagementPolicy"] = old["podManagementPolicy"]
			}
			if deploymentplan.Map(object["metadata"])["name"] == "zammad-postgres" {
				before := deploymentplan.String(deploymentplan.Map(certIssuerObjectList(environmentZammadPod(current)["containers"])[0])["image"])
				after := deploymentplan.String(deploymentplan.Map(certIssuerObjectList(environmentZammadPod(object)["containers"])[0])["image"])
				major := regexp.MustCompile(`:([0-9]+)[.\-@]`)
				a, b := major.FindStringSubmatch(before), major.FindStringSubmatch(after)
				if len(a) != 2 || len(b) != 2 || a[1] != b[1] {
					return zammadBlocker("deployment.compatibility.support", deploymentplan.ID(object), "Zammad PostgreSQL major version change requires a verified database migration")
				}
			}
		}
	}
	return nil
}

func retireCompletedZammadInitJobs(definitions []deploymentplan.Definition, live []deploymentplan.Object, release environmentRelease) ([]deploymentplan.Definition, error) {
	if len(definitions) == 0 {
		return definitions, nil
	}
	selected := map[string]bool{}
	for _, d := range definitions {
		selected[deploymentplan.ID(d.Object)] = true
	}
	for _, object := range live {
		meta := deploymentplan.Map(object["metadata"])
		name := deploymentplan.String(meta["name"])
		if object["kind"] != "Job" || meta["namespace"] != release.Stack+"-support" || !strings.HasPrefix(name, "zammad-init-") || selected[deploymentplan.ID(object)] {
			continue
		}
		if err := validateZammadInventoryImages(object, release); err != nil {
			return nil, err
		}
		complete := false
		for _, v := range certIssuerObjectList(deploymentplan.Map(object["status"])["conditions"]) {
			c := deploymentplan.Map(v)
			if c["type"] == "Complete" && c["status"] == "True" {
				complete = true
			}
		}
		if !complete {
			return nil, zammadBlocker("deployment.compatibility.support-job", deploymentplan.ID(object), "an original Zammad initialization Job is pending or failed; resolve it before another migration")
		}
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "namespace": meta["namespace"]}}, Phase: "retirement", Retire: true})
	}
	return definitions, nil
}
