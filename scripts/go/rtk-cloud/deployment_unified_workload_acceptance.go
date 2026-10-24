package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Use the execution's frozen API identity and complete native inventory. This
// verifies runtime workloads, not PKI or representative business acceptance.
func (b environmentKubernetesBackend) CheckTargetWorkloads(ctx context.Context, plan *deploymentplan.Plan) error {
	if plan == nil || plan.Environment != b.Store.Environment || plan.Operation == "scoped" {
		return targetWorkloadBlocker("", "complete selected environment plan required")
	}
	objects, err := b.Inventory(ctx, plan.Stack)
	if err != nil {
		return targetWorkloadBlocker("", "complete native workload inventory is unavailable")
	}
	return verifyEnvironmentTargetWorkloads(ctx, plan, objects, func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, b.Store, image)
	})
}

func targetWorkloadBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Resource: resource, Cause: cause, Next: "restore canonical registry access and installed Docker/Buildx; repair the planned controller, actual owned Pods, immutable image, init/sidecar startup and replica readiness; retain execution fencing and the private journal, then repeat complete target and functional acceptance"}
}

// Only maintained literal causes may cross the private OCI client boundary.
func environmentRuntimeImageFailureCause(message string) string {
	switch message {
	case "immutable image required", "selected private environment store required",
		"private image metadata directory unavailable", "canonical image credentials unavailable",
		"incomplete canonical registry credentials", "installed Docker client plugin inventory unavailable",
		"ambiguous installed Buildx plugin", "installed Buildx plugin unavailable",
		"digest-bound OCI metadata unavailable", "invalid OCI metadata", "image config digest missing",
		"ambiguous image platform", "required image platform missing", "OCI index nesting exceeds its bound",
		"empty approved image digest evidence":
		return message
	case "":
		return "empty approved image digest evidence"
	default:
		return "runtime image lookup failed"
	}
}

func environmentPodEvidenceFailureCause(err error) string {
	if err == nil {
		return "container proof unavailable"
	}
	message := err.Error()
	switch message {
	case "incomplete container inventory", "invalid declared container", "duplicate container",
		"duplicate container status", "missing current container", "init not successfully completed",
		"container not running and ready", "running digest differs from the approved image":
		return message
	}
	const prefix = "immutable runtime image evidence unavailable: "
	if strings.HasPrefix(message, prefix) {
		return prefix + environmentRuntimeImageFailureCause(strings.TrimPrefix(message, prefix))
	}
	return "unclassified container proof failure"
}

// A scoped lifecycle may intentionally quiesce one controller while the other
// selected owners remain available. Validate every selected controller against
// its own desired state; a whole-stack summary cannot represent that mixture.
func verifyScopedControllerStatus(controller deploymentplan.Object, desired float64) error {
	kind := deploymentplan.String(controller["kind"])
	if kind == "Pod" {
		return nil
	}
	metadata, status := deploymentplan.Map(controller["metadata"]), deploymentplan.Map(controller["status"])
	generation, generationOK := metadata["generation"].(float64)
	observed, observedOK := status["observedGeneration"].(float64)
	if !generationOK || !observedOK || observed < generation {
		return errors.New("controller generation is not observed")
	}
	exact := func(key string, want float64) bool {
		value, ok := status[key].(float64)
		return ok && value == want
	}
	zero := func(key string) bool {
		value, ok := status[key].(float64)
		return !ok || value == 0
	}
	switch kind {
	case "Deployment":
		if desired == 0 {
			if !zero("replicas") || !zero("readyReplicas") || !zero("updatedReplicas") || !zero("availableReplicas") || !zero("unavailableReplicas") {
				return errors.New("scaled-down deployment still reports replicas")
			}
			return nil
		}
		if !exact("readyReplicas", desired) || !exact("updatedReplicas", desired) || !exact("availableReplicas", desired) || !zero("unavailableReplicas") {
			return errors.New("deployment rollout status differs from desired")
		}
	case "StatefulSet":
		if desired == 0 {
			if !zero("replicas") || !zero("readyReplicas") || !zero("currentReplicas") || !zero("updatedReplicas") || !zero("availableReplicas") {
				return errors.New("scaled-down statefulset still reports replicas")
			}
			return nil
		}
		if !exact("readyReplicas", desired) {
			return errors.New("statefulset ready replicas differ from desired")
		}
		strategy := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(controller["spec"])["updateStrategy"])["type"])
		if strategy != "OnDelete" {
			current, update := deploymentplan.String(status["currentRevision"]), deploymentplan.String(status["updateRevision"])
			if !exact("updatedReplicas", desired) || current == "" || current != update {
				return errors.New("statefulset rollout revision is not current")
			}
		}
	case "DaemonSet":
		if !exact("desiredNumberScheduled", desired) || !exact("numberReady", desired) || !exact("updatedNumberScheduled", desired) || !zero("numberUnavailable") {
			return errors.New("daemonset status differs from desired")
		}
	default:
		return errors.New("unsupported scoped controller")
	}
	return nil
}

func verifyEnvironmentTargetWorkloads(ctx context.Context, plan *deploymentplan.Plan, objects []deploymentplan.Object, imageDigests func(context.Context, string) (map[string]bool, error), recordedOwners ...map[string]string) error {
	if plan == nil || imageDigests == nil {
		return targetWorkloadBlocker("", "complete plan and runtime image verification required")
	}
	standaloneRedis := false
	redisTarget := environmentRedisTargetPodPlan(plan)
	if redisTarget || len(recordedOwners) > 0 {
		if len(recordedOwners) != 1 || !redisTarget || len(objects) != 1 || len(recordedOwners[0]) != 1 {
			return targetWorkloadBlocker("", "standalone Redis helper requires its exact staged recipe and recorded Pod owner")
		}
		resource, pod := plan.Resources[0], objects[0]
		meta := deploymentplan.Map(pod["metadata"])
		uid := recordedOwners[0][resource.ID]
		if deploymentplan.ID(pod) != resource.ID || !environmentBootstrapOwnerUUID.MatchString(uid) || meta["uid"] != uid || len(certIssuerObjectList(meta["ownerReferences"])) != 0 || len(certIssuerObjectList(deploymentplan.Map(pod["spec"])["ephemeralContainers"])) != 0 || len(certIssuerObjectList(deploymentplan.Map(pod["status"])["ephemeralContainerStatuses"])) != 0 || !environmentRedisTargetPodDataMatches(pod) {
			return targetWorkloadBlocker(resource.ID, "standalone Redis helper differs from its original Pod owner")
		}
		standaloneRedis = true
	}
	if err := plan.CheckWorkloadInventory(objects); err != nil {
		return err
	}
	live := map[string]deploymentplan.Object{}
	for _, object := range objects {
		live[deploymentplan.ID(object)] = object
	}
	controllers, counts := map[string]deploymentplan.Object{}, map[string]int{}
	completedJobs, verifiedJobs := map[string]deploymentplan.Object{}, map[string]bool{}
	var health []deploymentplan.Object
	runtimeRequired := false
	for _, resource := range plan.Resources {
		kind := deploymentplan.String(resource.Desired["kind"])
		if resource.Action == "retire" {
			continue
		}
		isWorkload := keySet("Deployment", "StatefulSet", "DaemonSet", "Pod", "Job", "CronJob")[kind]
		if !isWorkload && plan.Operation != "scoped" {
			continue
		}
		actual := live[resource.ID]
		meta := deploymentplan.Map(actual["metadata"])
		if actual == nil || !deploymentplan.Matches(resource.Desired, actual) || deploymentplan.String(meta["uid"]) == "" || meta["deletionTimestamp"] != nil || resource.UID != "" && resource.UID != meta["uid"] {
			return targetWorkloadBlocker(resource.ID, "actual workload does not match its planned owner and target")
		}
		if !isWorkload {
			continue
		}
		if kind == "CronJob" {
			continue
		} // Scheduled templates must match; no future execution is invented.
		runtimeRequired = true
		if kind == "Job" {
			complete := false
			for _, value := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
				condition := deploymentplan.Map(value)
				if condition["type"] == "Complete" && condition["status"] == "True" {
					complete = true
				}
				if condition["type"] == "Failed" && condition["status"] == "True" {
					return targetWorkloadBlocker(resource.ID, "planned one-time Job failed")
				}
			}
			if !complete {
				return targetWorkloadBlocker(resource.ID, "planned one-time Job has not completed")
			}
			completedJobs[resource.ID] = actual
			continue
		}
		controllers[resource.ID] = actual
		if kind != "Pod" {
			health = append(health, actual)
		}
	}
	if plan.Operation == "scoped" && !runtimeRequired {
		return ctx.Err()
	}
	cache := map[string]map[string]bool{}
	for _, pod := range objects {
		meta := deploymentplan.Map(pod["metadata"])
		if pod["kind"] != "Pod" {
			continue
		}
		if meta["deletionTimestamp"] != nil {
			owner, err := environmentPodController(pod, live)
			if controller := controllers[owner]; err == nil && controller != nil && deploymentplan.Map(controller["spec"])["replicas"] == float64(0) {
				return targetWorkloadBlocker(owner, "zero-replica owner still has a terminating Pod")
			}
			continue
		}
		namespace := deploymentplan.String(meta["namespace"])
		if namespace != plan.Stack && !strings.HasPrefix(namespace, plan.Stack+"-") {
			continue
		}
		owner := deploymentplan.ID(pod)
		var err error
		if controllers[owner] == nil {
			// Existing terminal probes are retained by preflight's same strict
			// classifier. Planned Pods and Job-owned Pods still require runtime proof.
			if len(certIssuerObjectList(meta["ownerReferences"])) == 0 && deploymentplan.HistoricalTerminalWorkload(owner, live, plan.Stack) {
				continue
			}
			owner, err = environmentPodController(pod, live)
		}
		if err != nil {
			return targetWorkloadBlocker(deploymentplan.ID(pod), "Pod controller UID lineage cannot be verified")
		}
		controller := controllers[owner]
		if job := completedJobs[owner]; job != nil {
			if deploymentplan.Map(pod["status"])["phase"] != "Succeeded" {
				continue
			}
			if err := verifyEnvironmentPodContainers(ctx, job, pod, cache, imageDigests); err != nil {
				return targetWorkloadBlocker(owner, "completed Job startup and immutable image evidence could not be verified: "+environmentPodEvidenceFailureCause(err))
			}
			verifiedJobs[owner] = true
			continue
		}
		if controller == nil {
			continue
		} // Completed/periodic Job Pods are not application replicas.
		if deploymentplan.Map(pod["status"])["phase"] != "Running" {
			return targetWorkloadBlocker(owner, "owned application Pod is not Running")
		}
		if err := verifyEnvironmentPodContainers(ctx, controller, pod, cache, imageDigests); err != nil {
			return targetWorkloadBlocker(owner, "owned Pod startup and approved immutable runtime image evidence could not be verified: "+environmentPodEvidenceFailureCause(err))
		}
		counts[owner]++
		health = append(health, pod)
	}
	for id := range completedJobs {
		if !verifiedJobs[id] {
			return targetWorkloadBlocker(id, "completed Job has no successful Pod runtime image evidence")
		}
	}
	for id, controller := range controllers {
		spec, status := deploymentplan.Map(controller["spec"]), deploymentplan.Map(controller["status"])
		desired := float64(1)
		if value, exists := spec["replicas"]; exists {
			var ok bool
			desired, ok = value.(float64)
			if !ok {
				return targetWorkloadBlocker(id, "replica intent is not normalized native JSON")
			}
		}
		if controller["kind"] == "DaemonSet" {
			var ok bool
			desired, ok = status["desiredNumberScheduled"].(float64)
			if !ok || desired <= 0 {
				return targetWorkloadBlocker(id, "DaemonSet has no verified eligible node placement")
			}
		}
		if desired < 0 || desired > 10000 || desired != float64(int(desired)) || counts[id] != int(desired) {
			return targetWorkloadBlocker(id, "actual owned Pod count differs from required replicas")
		}
		if plan.Operation == "scoped" {
			if err := verifyScopedControllerStatus(controller, desired); err != nil {
				return targetWorkloadBlocker(id, err.Error())
			}
		}
	}
	// This one-time isolated data helper has no workload controller. Its exact
	// maintained create recipe and original child-journal UID are bound above;
	// the ordinary owner/body, Running, containers and OCI proofs still run.
	if standaloneRedis {
		ready := 0
		for _, value := range certIssuerObjectList(deploymentplan.Map(objects[0]["status"])["conditions"]) {
			condition := deploymentplan.Map(value)
			if condition["type"] == "Ready" {
				if condition["status"] != "True" {
					return targetWorkloadBlocker(plan.Resources[0].ID, "standalone Redis helper Pod is not Ready")
				}
				ready++
			}
		}
		if ready != 1 {
			return targetWorkloadBlocker(plan.Resources[0].ID, "standalone Redis helper Pod has no unique Ready proof")
		}
		return ctx.Err()
	}
	// A staged native migration has terminal successful Job Pods rather than
	// running application replicas. Every planned Job and its immutable Pod
	// image was verified above. This never substitutes full environment health.
	if plan.Operation == "scoped" && len(controllers) == 0 && len(completedJobs) > 0 && len(verifiedJobs) == len(completedJobs) {
		return ctx.Err()
	}
	if plan.Operation == "scoped" && len(controllers) > 0 && len(completedJobs) == 0 {
		return ctx.Err()
	}
	raw, err := json.Marshal(map[string]any{"items": health})
	if err != nil || validateDeploymentWorkloadHealthForStack(plan.Stack, raw) != nil {
		return targetWorkloadBlocker("", "complete target controllers and Pods are not observed, Running and Ready")
	}
	return ctx.Err()
}

func managedLateRedisTargetPod(namespace, name, image string) deploymentplan.Object {
	return deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "terminationGracePeriodSeconds": float64(30), "containers": []any{deploymentplan.Object{"name": "redis-migration", "image": image, "imagePullPolicy": "IfNotPresent", "command": []any{"/bin/sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 10; done"}, "volumeMounts": []any{deploymentplan.Object{"name": "data", "mountPath": "/data"}}}}, "volumes": []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "redis-data"}}}}}
}

func environmentRedisTargetPodDataMatches(pod deploymentplan.Object) bool {
	spec := deploymentplan.Map(pod["spec"])
	containers, volumes := certIssuerObjectList(spec["containers"]), certIssuerObjectList(spec["volumes"])
	if len(containers) != 1 || len(volumes) != 1 {
		return false
	}
	mounts := certIssuerObjectList(deploymentplan.Map(containers[0])["volumeMounts"])
	if len(mounts) != 1 {
		return false
	}
	volume, mount := deploymentplan.Map(volumes[0]), deploymentplan.Map(mounts[0])
	claim := deploymentplan.Map(volume["persistentVolumeClaim"])
	return volume["name"] == "data" && claim["claimName"] == "redis-data" && (claim["readOnly"] == nil || claim["readOnly"] == false) && mount["name"] == "data" && mount["mountPath"] == "/data" && (mount["readOnly"] == nil || mount["readOnly"] == false) && (mount["subPath"] == nil || mount["subPath"] == "") && (mount["subPathExpr"] == nil || mount["subPathExpr"] == "")
}

// Match the compiler output of this exact maintained stage, not arbitrary
// standalone Pods or the complete environment health contract.
func environmentRedisTargetPodPlan(plan *deploymentplan.Plan) bool {
	if plan == nil || plan.Operation != "scoped" || len(plan.Resources) != 1 {
		return false
	}
	resource := plan.Resources[0]
	meta := deploymentplan.Map(resource.Desired["metadata"])
	name := deploymentplan.String(meta["name"])
	suffix := strings.TrimPrefix(name, "redis-durable-")
	containers := certIssuerObjectList(deploymentplan.Map(resource.Desired["spec"])["containers"])
	if resource.Desired["kind"] != "Pod" || meta["namespace"] != plan.Stack+"-platform" || !strings.HasPrefix(name, "redis-durable-") || len(suffix) != 12 || !environmentPreflightDigest.MatchString(suffix+strings.Repeat("0", 52)) || len(containers) != 1 {
		return false
	}
	image := deploymentplan.String(deploymentplan.Map(containers[0])["image"])
	if !immutableEnvironmentImage.MatchString(image) {
		return false
	}
	definitions := []deploymentplan.Definition{{Object: managedLateRedisTargetPod(plan.Stack+"-platform", name, image), Phase: "workload"}}
	if plan.BindingsSHA256 != environmentCreateStageBindingsSHA("normal-redis-durable-target", definitions) {
		return false
	}
	expected, err := deploymentplan.Compile(plan.Inputs, definitions, nil)
	return err == nil && expected.SHA256() == plan.SHA256()
}

func environmentPodController(pod deploymentplan.Object, live map[string]deploymentplan.Object) (string, error) {
	object := pod
	for depth := 0; depth < 4; depth++ {
		meta := deploymentplan.Map(object["metadata"])
		var ref deploymentplan.Object
		for _, value := range certIssuerObjectList(meta["ownerReferences"]) {
			candidate := deploymentplan.Map(value)
			if candidate["controller"] == true {
				if ref != nil {
					return "", errors.New("ambiguous controller")
				}
				ref = candidate
			}
		}
		if ref == nil {
			return "", errors.New("missing controller")
		}
		id := deploymentplan.String(ref["kind"]) + "/" + deploymentplan.String(meta["namespace"]) + "/" + deploymentplan.String(ref["name"])
		parent := live[id]
		if parent == nil || ref["uid"] != deploymentplan.Map(parent["metadata"])["uid"] || ref["uid"] == "" || ref["apiVersion"] != parent["apiVersion"] {
			return "", errors.New("controller identity changed")
		}
		if parent["kind"] != "ReplicaSet" {
			return id, nil
		}
		object = parent
	}
	return "", errors.New("controller lineage exceeds its bound")
}

func verifyEnvironmentPodContainers(ctx context.Context, controller, pod deploymentplan.Object, cache map[string]map[string]bool, imageDigests func(context.Context, string) (map[string]bool, error)) error {
	expected := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(controller["spec"])["template"])["spec"])
	if controller["kind"] == "Pod" {
		expected = deploymentplan.Map(controller["spec"])
	}
	spec, status := deploymentplan.Map(pod["spec"]), deploymentplan.Map(pod["status"])
	for _, field := range []string{"containers", "initContainers"} {
		containers := certIssuerObjectList(expected[field])
		actual := certIssuerObjectList(spec[field])
		statuses := certIssuerObjectList(status[map[string]string{"containers": "containerStatuses", "initContainers": "initContainerStatuses"}[field]])
		if len(containers) != len(actual) || len(containers) != len(statuses) || field == "containers" && len(containers) == 0 {
			return errors.New("incomplete container inventory")
		}
		seen := map[string]bool{}
		for _, value := range containers {
			container := deploymentplan.Map(value)
			name, image := deploymentplan.String(container["name"]), deploymentplan.String(container["image"])
			if name == "" || seen[name] || !immutableEnvironmentImage.MatchString(image) {
				return errors.New("invalid declared container")
			}
			seen[name] = true
			var observed, running deploymentplan.Object
			for _, v := range actual {
				c := deploymentplan.Map(v)
				if c["name"] == name {
					if observed != nil {
						return errors.New("duplicate container")
					}
					observed = c
				}
			}
			for _, v := range statuses {
				c := deploymentplan.Map(v)
				if c["name"] == name {
					if running != nil {
						return errors.New("duplicate container status")
					}
					running = c
				}
			}
			if observed["image"] != image || running == nil || deploymentplan.String(running["containerID"]) == "" {
				return errors.New("missing current container")
			}
			state := deploymentplan.Map(running["state"])
			if field == "initContainers" && container["restartPolicy"] != "Always" || controller["kind"] == "Job" {
				terminated := deploymentplan.Map(state["terminated"])
				if len(terminated) == 0 || terminated["exitCode"] != float64(0) || state["waiting"] != nil || state["running"] != nil {
					return errors.New("init not successfully completed")
				}
			} else if len(deploymentplan.Map(state["running"])) == 0 || running["ready"] != true || running["started"] == false || container["startupProbe"] != nil && running["started"] != true || state["waiting"] != nil || state["terminated"] != nil {
				return errors.New("container not running and ready")
			}
			if cache[image] == nil {
				accepted, err := imageDigests(ctx, image)
				if err != nil || len(accepted) == 0 {
					message := ""
					if err != nil {
						message = err.Error()
					}
					return errors.New("immutable runtime image evidence unavailable: " + environmentRuntimeImageFailureCause(message))
				}
				cache[image] = accepted
			}
			imageID := deploymentplan.String(running["imageID"])
			_, digest, found := strings.Cut(imageID, "@")
			if !found {
				_, digest, found = strings.Cut(imageID, "://")
				if !found {
					digest = imageID
				}
			}
			if !deploymentImageDigestPattern.MatchString(digest) || !cache[image][digest] {
				return errors.New("running digest differs from the approved image")
			}
		}
	}
	return nil
}

// An OCI index digest is not necessarily the kubelet's platform imageID. Verify
// every inspected manifest against its requested digest, then accept only its
// selected linux/amd64 manifest or content-addressed config, never a tag.
func environmentRuntimeImageDigests(ctx context.Context, store secretStore, image string) (map[string]bool, error) {
	if !immutableEnvironmentImage.MatchString(image) {
		return nil, errors.New("immutable image required")
	}
	directory, cleanup, err := environmentPrivateImageAccess(ctx, store)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	repository, digest, _ := strings.Cut(image, "@")
	accepted := map[string]bool{}
	for depth := 0; depth < 3; depth++ {
		readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		command := exec.CommandContext(readCtx, "docker", "--config", directory, "buildx", "imagetools", "inspect", "--raw", repository+"@"+digest)
		command.Env = append(os.Environ(), "DOCKER_CONFIG="+directory, "BUILDX_CONFIG="+filepath.Join(directory, "buildx"), "DOCKER_CONTEXT=", "DOCKER_HOST=")
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error {
			if command.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		command.WaitDelay = time.Second
		var output environmentAPIOutput
		command.Stdout = &output
		err := command.Run()
		raw := output.buffer.Bytes()
		cancel()
		if err != nil || len(raw) > 4<<20 || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != digest {
			return nil, errors.New("digest-bound OCI metadata unavailable")
		}
		var manifest struct {
			SchemaVersion int `json:"schemaVersion"`
			Manifests     []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
				} `json:"platform"`
			} `json:"manifests"`
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
			return nil, errors.New("invalid OCI metadata")
		}
		accepted[digest] = true
		if len(manifest.Manifests) == 0 {
			if !deploymentImageDigestPattern.MatchString(manifest.Config.Digest) {
				return nil, errors.New("image config digest missing")
			}
			accepted[manifest.Config.Digest] = true
			return accepted, nil
		}
		digest = ""
		for _, child := range manifest.Manifests {
			if child.Platform.OS == "linux" && child.Platform.Architecture == "amd64" {
				if digest != "" || !deploymentImageDigestPattern.MatchString(child.Digest) {
					return nil, errors.New("ambiguous image platform")
				}
				digest = child.Digest
			}
		}
		if digest == "" {
			return nil, errors.New("required image platform missing")
		}
	}
	return nil, errors.New("OCI index nesting exceeds its bound")
}

// Metadata clients may create Docker/Buildx state. Isolate both alongside the
// frozen Kubernetes client and supply only canonical environment credentials.
func environmentPrivateImageAccess(ctx context.Context, store secretStore) (string, func(), error) {
	if !filepath.IsAbs(store.Root) || !secretEnvironmentPattern.MatchString(store.Environment) {
		return "", nil, errors.New("selected private environment store required")
	}
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "tmp")} {
		if err := ensurePrivateDirectory(path); err != nil {
			return "", nil, errors.New("private image metadata directory unavailable")
		}
	}
	directory, err := os.MkdirTemp(filepath.Join(store.Root, "deployment", "tmp"), "image-metadata-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(directory) }
	operator, err := store.readOperator()
	if err != nil {
		cleanup()
		return "", nil, errors.New("canonical image credentials unavailable")
	}
	auths := map[string]any{}
	username, token := operator["GHCR_PULL_USERNAME"], operator["GHCR_PULL_TOKEN"]
	if (username == "") != (token == "") {
		cleanup()
		return "", nil, errors.New("incomplete canonical registry credentials")
	}
	if username != "" {
		auths["ghcr.io"] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + token))}
	}
	toolCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	metadata, err := exec.CommandContext(toolCtx, "docker", "info", "--format", "{{json .ClientInfo.Plugins}}").Output()
	cancel()
	var plugins []struct {
		Name string
		Path string
	}
	if err != nil || len(metadata) > 1<<20 || json.Unmarshal(metadata, &plugins) != nil {
		cleanup()
		return "", nil, errors.New("installed Docker client plugin inventory unavailable")
	}
	pluginDirectory := ""
	for _, plugin := range plugins {
		if plugin.Name == "buildx" && filepath.IsAbs(plugin.Path) {
			if pluginDirectory != "" {
				cleanup()
				return "", nil, errors.New("ambiguous installed Buildx plugin")
			}
			pluginDirectory = filepath.Dir(plugin.Path)
		}
	}
	if pluginDirectory == "" {
		cleanup()
		return "", nil, errors.New("installed Buildx plugin unavailable")
	}
	raw, err := json.Marshal(map[string]any{"auths": auths, "cliPluginsExtraDirs": []string{pluginDirectory}})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), raw, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return directory, cleanup, nil
}
