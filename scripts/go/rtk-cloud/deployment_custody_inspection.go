package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

// Public metadata only. Admission uses the selected original Root and native
// registry/CRL policy; mutable KV versions and bootstrap mirror keys are absent.
type environmentCustodyPublicState struct {
	Subject         string `json:"subject"`
	Fingerprint     string `json:"fingerprint"`
	PublicKeySHA256 string `json:"public_key_sha256"`
	RootSHA256      string `json:"root_sha256"`
	StateSHA256     string `json:"state_sha256"`
	Pending         bool   `json:"pending"`
}

// A one-shot native reader uses the original workload auth scope and public
// transport projection. It does not create identity, Leases, RBAC or private
// storage, and its selected source need not be running.
func environmentCustodyInspectionJob(cfg deploymentConfig, binding *environmentCustodyBinding, ref *environmentCustodyStateReference, source deploymentplan.Object, image, bootstrapRoot, domain, subject string, names []string) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("custody inspection requires its original selected workload scope, native image and execution profile")
	}
	if binding == nil || ref == nil || !immutableEnvironmentImage.MatchString(image) || source == nil {
		return bad()
	}
	alias := ""
	for _, owner := range binding.Owners {
		if owner.ID == ref.Owner && owner.Namespace == ref.Namespace && len(owner.StorageAliases) > 0 {
			alias = owner.StorageAliases[0]
		}
	}
	if alias == "" || validateEnvironmentCustodyStateReference(ref, cfg, binding, ref.Namespace, alias, ref.StatePath, ref.RootSHA256) != nil {
		return bad()
	}
	if (domain == "" || domain == "service") && binding.ServiceRootSHA256 != "" && binding.ServiceRootSHA256 != ref.RootSHA256 {
		return bad()
	}
	if subject == "" || domain == "" && (!strings.HasPrefix(subject, "service:") || len(names) != 0) || domain != "" && len(names) == 0 {
		return bad()
	}
	wireDomain, wireNames := domain, strings.Join(names, ",")
	if wireDomain == "" {
		wireDomain = "-"
	}
	if len(names) == 0 {
		wireNames = "-"
	}
	return environmentCustodyScopedJob(cfg, binding, ref.Namespace, alias, ref.StatePath, source, image, bootstrapRoot, []any{"/app/serviceidentity-bootstrap", "inspect-admitted", ref.StatePath, wireDomain, subject, ref.RootSHA256, wireNames}, false)
}

func environmentCustodyScopedJob(cfg deploymentConfig, binding *environmentCustodyBinding, namespace, alias, state string, source deploymentplan.Object, image, bootstrapRoot string, command []any, projection bool) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("custody helper requires its original selected scope and native execution profile")
	}
	if !immutableEnvironmentImage.MatchString(image) || source == nil {
		return bad()
	}
	if _, err := environmentCustodyStateOwner(cfg, binding, namespace, alias, state); err != nil {
		return bad()
	}
	timeout, err := environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return nil, err
	}
	sourceContainers := certIssuerObjectList(source["containers"])
	if len(sourceContainers) == 0 || source["securityContext"] == nil {
		return bad()
	}
	sourceContainer := deploymentplan.Map(sourceContainers[0])
	container := deploymentplan.Object{"name": "inspect", "image": image, "command": command,
		"env":          append(environmentCreateEnv(map[string]string{"PKI_INSPECTION_IDENTITY_STATE": state}), environmentCreateServiceDatabaseEnv("pki-verifier-database")),
		"volumeMounts": []any{environmentCreateServiceMount("identity", filepath.Dir(state), !projection)}}
	if sourceContainer["securityContext"] != nil {
		container["securityContext"] = deploymentplan.Clone(deploymentplan.Map(sourceContainer["securityContext"]))
	}
	pod := deploymentplan.Object{"restartPolicy": "Never", "containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", alias)}}
	for _, key := range []string{"securityContext", "imagePullSecrets", "nodeSelector", "tolerations"} {
		if value, ok := source[key]; ok {
			pod[key] = value
		}
	}
	object := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-custody-inspection", "namespace": namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"]}},
		"spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(timeout / time.Second), "ttlSecondsAfterFinished": float64(timeout / time.Second), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{}}, "spec": pod}}}
	projected, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, bootstrapRoot, []deploymentplan.Definition{{Object: object, Phase: "bootstrap"}})
	if err != nil {
		return nil, err
	}
	for _, d := range projected {
		if d.Object["kind"] == "Job" {
			// Scope derives through the same projector as the writer. The
			// read-only command needs only JWT/public trust, not a projection.
			pod := environmentPKIPod(d.Object)
			container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			if !projection {
				pod["volumes"] = environmentCreateRemoveNamed(pod["volumes"], "identity")
				container["volumeMounts"] = environmentCreateRemoveNamed(container["volumeMounts"], "identity")
			}
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_INSPECTION_IDENTITY_STATE")
			return d.Object, nil
		}
	}
	return bad()
}

func runEnvironmentCustodyInspection(ctx context.Context, b environmentKubernetesBackend, job deploymentplan.Object, ref *environmentCustodyStateReference) (environmentCustodyPublicState, error) {
	var result environmentCustodyPublicState
	if ref == nil {
		return result, environmentCreateBootstrapBlocker("original identity inspection reference is required")
	}
	containers := certIssuerObjectList(environmentPKIPod(job)["containers"])
	if len(containers) != 1 {
		return result, environmentCreateBootstrapBlocker("one original native inspection container is required")
	}
	command := certIssuerObjectList(deploymentplan.Map(containers[0])["command"])
	if len(command) != 7 || command[0] != "/app/serviceidentity-bootstrap" || command[1] != "inspect-admitted" || command[2] != ref.StatePath || command[5] != ref.RootSHA256 {
		return result, environmentCreateBootstrapBlocker("native inspection command differs from original identity")
	}
	raw, err := runEnvironmentCustodyJob(ctx, b, job, ref.Namespace, nil, nil)
	if err != nil {
		return result, err
	}
	if len(raw) > 64<<10 || decodeEnvironmentJSON(raw, &result) != nil || result.Subject != command[4] || result.RootSHA256 != ref.RootSHA256 || !environmentPreflightDigest.MatchString(result.Fingerprint) || !environmentPreflightDigest.MatchString(result.PublicKeySHA256) || !environmentPreflightDigest.MatchString(result.StateSHA256) {
		return environmentCustodyPublicState{}, environmentCreateBootstrapBlocker("native public admission proof differs from the original logical identity")
	}
	return result, nil
}

// Both one-shot observers and streamed public maintenance retain the same exact
// Job/Pod ownership, bounded output and UID-preconditioned cleanup contract.
func runEnvironmentCustodyJob(ctx context.Context, b environmentKubernetesBackend, job deploymentplan.Object, namespace string, arguments []string, input []byte) (result []byte, resultErr error) {
	blocked := func(cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.pki.custody-inspection", Cause: cause, Next: "verify original selected OpenBao scope, registry admission and public transport; preserve identities and repeat qualification"}
	}
	if b.Store.Environment == "" || !strings.HasPrefix(namespace, "video-cloud-"+b.Store.Environment+"-") || job["kind"] != "Job" || deploymentplan.Map(job["metadata"])["namespace"] != namespace || b.RolloutTimeout <= 0 {
		return nil, blocked("original environment and bounded custody helper are required")
	}
	interactive := len(arguments) > 0
	if interactive && (len(arguments) != 4 || (arguments[0] != "inspect-public-crl" && arguments[0] != "apply-public-crl")) {
		return nil, blocked("unsupported public custody operation")
	}
	job = deploymentplan.Clone(job)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	name := "pki-custody-inspect-" + hex.EncodeToString(nonce)
	deploymentplan.Map(job["metadata"])["name"] = name
	id := "Job/" + namespace + "/" + name
	body, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, b.RolloutTimeout)
	defer cancel()
	raw, createErr := b.command(bounded, body, "create", "-f", "-", "-o", "json")
	var actual deploymentplan.Object
	if createErr != nil || json.Unmarshal(raw, &actual) != nil {
		actual, err = b.Read(bounded, id)
		if err != nil {
			return nil, blocked("custody helper creation could not be reconciled")
		}
	}
	uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
	if uid == "" || !deploymentplan.Matches(job, actual) {
		return nil, blocked("created custody helper differs from its exact requested scope")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		request, _ := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": uid}, "gracePeriodSeconds": float64(0), "propagationPolicy": "Foreground"})
		endpoint := "/apis/batch/v1/namespaces/" + url.PathEscape(namespace) + "/jobs/" + url.PathEscape(name)
		_, cleanupErr := b.command(cleanup, request, "delete", "--raw", endpoint, "-f", "-")
		if cleanupErr != nil {
			observed, readErr := b.Read(cleanup, id)
			if readErr == nil && observed == nil {
				cleanupErr = nil
			}
		} else {
			_, cleanupErr = b.command(cleanup, nil, "--namespace", namespace, "wait", "--for=delete", "Job/"+name, "--timeout=30s")
		}
		if cleanupErr != nil {
			result = nil
			resultErr = errors.Join(resultErr, blocked("original custody helper cleanup could not be verified; replacement owners are protected"))
		}
	}()
	verifyJob := func() error {
		observed, err := b.Read(bounded, id)
		if err != nil || observed == nil || deploymentplan.Map(observed["metadata"])["uid"] != uid || deploymentplan.Map(observed["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(job, observed) {
			return blocked("original custody helper Job changed")
		}
		return nil
	}
	if err := verifyJob(); err != nil {
		return nil, err
	}
	if !interactive {
		if err := b.Wait(bounded, deploymentplan.Resource{ID: id, UID: uid, Phase: "bootstrap", Action: "create", Desired: job}); err != nil {
			return nil, blocked("native custody inspection did not complete admission")
		}
	}
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	for {
		raw, err = b.command(bounded, nil, "--namespace", namespace, "get", "pods", "--selector", "batch.kubernetes.io/controller-uid="+uid, "-o", "json")
		if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Items) > 1 {
			return nil, blocked("native custody helper has no unique original Pod")
		}
		if len(list.Items) == 1 {
			break
		}
		if !interactive {
			return nil, blocked("native custody inspection has no unique original Pod")
		}
		if err := verifyJob(); err != nil {
			return nil, err
		}
		select {
		case <-bounded.Done():
			return nil, blocked("native custody helper Pod allocation exceeded selected deadline")
		case <-time.After(250 * time.Millisecond):
		}
	}
	pod := list.Items[0]
	meta := deploymentplan.Map(pod["metadata"])
	podName, podUID := deploymentplan.String(meta["name"]), deploymentplan.String(meta["uid"])
	phase := "Succeeded"
	if interactive {
		phase = "Running"
	}
	verifyPod := func(object deploymentplan.Object) error {
		metadata := deploymentplan.Map(object["metadata"])
		owners := certIssuerObjectList(metadata["ownerReferences"])
		if metadata["namespace"] != namespace || metadata["name"] != podName || podName == "" || metadata["uid"] != podUID || podUID == "" || metadata["deletionTimestamp"] != nil || len(owners) != 1 || deploymentplan.Map(owners[0])["kind"] != "Job" || deploymentplan.Map(owners[0])["name"] != name || deploymentplan.Map(owners[0])["uid"] != uid || deploymentplan.Map(owners[0])["controller"] != true || deploymentplan.Map(object["status"])["phase"] != phase || !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": environmentPKIPod(job)}, deploymentplan.Object{"kind": "Pod", "spec": object["spec"]}) {
			return blocked("native custody helper Pod differs from its original Job and scope")
		}
		return nil
	}
	if interactive {
		if _, err := b.command(bounded, nil, "--namespace", namespace, "wait", "--for=condition=Ready", "pod/"+podName, "--timeout="+b.RolloutTimeout.String()); err != nil {
			return nil, blocked("native public custody helper did not become ready")
		}
		pod, err = b.Read(bounded, "Pod/"+namespace+"/"+podName)
		if err != nil {
			return nil, blocked("original public custody helper Pod is unavailable")
		}
	}
	if err := verifyPod(pod); err != nil {
		return nil, err
	}
	if err := verifyJob(); err != nil {
		return nil, err
	}
	if interactive {
		command := []string{"--namespace", namespace, "exec"}
		if len(input) > 0 {
			command = append(command, "-i")
		}
		command = append(command, "Pod/"+podName, "--container", "inspect", "--", "/app/serviceidentity-bootstrap")
		command = append(command, arguments...)
		result, err = b.command(bounded, input, command...)
	} else {
		result, err = b.command(bounded, nil, "--namespace", namespace, "logs", "Pod/"+podName, "--container", "inspect", "--limit-bytes=65536")
	}
	if err != nil || len(result) > 2<<20 {
		return nil, blocked("native custody helper returned no bounded public proof")
	}
	if err := verifyJob(); err != nil {
		return nil, err
	}
	pod, err = b.Read(bounded, "Pod/"+namespace+"/"+podName)
	if err != nil {
		return nil, blocked("original custody helper Pod could not be rechecked")
	}
	if err := verifyPod(pod); err != nil {
		return nil, err
	}
	return result, nil
}

func inspectEnvironmentCustodyIdentity(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, binding *environmentCustodyBinding, ref *environmentCustodyStateReference, source deploymentplan.Object, image, domain, subject string, names []string) (environmentCustodyPublicState, error) {
	var empty environmentCustodyPublicState
	if binding == nil || ref == nil || binding.Generation != ref.Generation {
		return empty, environmentCreateBootstrapBlocker("independent custody inspection lacks its original generation binding")
	}
	snapshot, originalBootstrapRoot, err := readEnvironmentCustodyTransportState(cfg, b.Store, binding.Generation)
	if err != nil {
		return empty, err
	}
	selected := *binding
	if snapshot.ManagedRootSHA256 != "" {
		if selected.OpenBaoRootSHA256 != "" && selected.OpenBaoRootSHA256 != snapshot.ManagedRootSHA256 {
			return empty, environmentCreateBootstrapBlocker("inspection transport differs from its original managed OpenBao authority")
		}
		selected.OpenBaoRootSHA256 = snapshot.ManagedRootSHA256
	}
	bootstrapRoot := ""
	if !snapshot.ManagedOnly {
		bootstrapRoot = originalBootstrapRoot
	}
	job, err := environmentCustodyInspectionJob(cfg, &selected, ref, source, image, bootstrapRoot, domain, subject, names)
	if err != nil {
		return empty, err
	}
	b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return empty, err
	}
	return runEnvironmentCustodyInspection(ctx, b, job, ref)
}

// The accepted original enrollment is also available after its Pod retires.
// Use its execution profile, without requiring a finalized bootstrap credential
// or asking a stopped consumer to authenticate an inspection of its own state.
func inspectEnvironmentProductCustody(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, output environmentCreateProductIdentity, receipt environmentProductIdentityReceipt) (environmentCustodyPublicState, error) {
	var empty environmentCustodyPublicState
	if err := validateEnvironmentProductIdentityStorage(cfg, l, output, receipt); err != nil {
		return empty, err
	}
	ref := receipt.Custody
	if runtime.Custody == nil || ref == nil || runtime.Custody.Generation != ref.Generation || runtime.Execution == nil || runtime.Execution.Store != runtime.Backend.Store {
		return empty, environmentCreateBootstrapBlocker("product observation lacks its original workload custody owner")
	}
	role := strings.TrimPrefix(output.Subject, "service:")
	original, journal, err := acceptedCreateStageForProduct(runtime.Backend.Store, runtime.Execution, l, "enroll-"+role)
	if err != nil {
		return empty, err
	}
	if original.Environment != cfg.Environment || original.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return empty, environmentCreateBootstrapBlocker("product inspection source belongs to another environment")
	}
	var source deploymentplan.Object
	for _, resource := range original.Resources {
		if resource.Desired["kind"] != "Deployment" || deploymentplan.Map(resource.Desired["metadata"])["namespace"] != ref.Namespace {
			continue
		}
		pod := environmentPKIPod(resource.Desired)
		if pod["serviceAccountName"] != ref.ServiceAccount || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) {
			continue
		}
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(raw)
			if mqttFoundationEnv(container, "PKI_STATE_BACKEND") == "openbao" && mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX") == cfg.Environment+"/"+ref.Generation+"/"+ref.Owner && mqttFoundationEnv(container, "PKI_STATE_KV_MOUNT") == ref.Mount && mqttFoundationEnv(container, "PKI_CLIENT_IDENTITY_STATE") == ref.StatePath {
				if source != nil {
					return empty, environmentCreateBootstrapBlocker("product inspection has multiple original native execution profiles")
				}
				source = pod
			}
		}
	}
	if source == nil {
		return empty, environmentCreateBootstrapBlocker("product inspection cannot resolve its original completed enrollment profile")
	}
	return inspectEnvironmentCustodyIdentity(ctx, cfg, runtime.Backend, runtime.Custody, ref, source, l.Bootstrap.Image, "", output.Subject, nil)
}
