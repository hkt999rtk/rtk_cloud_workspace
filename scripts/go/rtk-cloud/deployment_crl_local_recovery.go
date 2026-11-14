package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type maintenanceLocalRecovery struct {
	consumer             crlMaintenanceConsumer
	name, uid, sourceSHA string
	parentID, parentUID  string
	claims               map[string]string
	manifest             deploymentplan.Object
}

// A failed consumer cannot be the prerequisite for inspecting or repairing its
// original CRL state. Use the original mounted claims on the same node, with no
// enrollment/init command, key export, ownership change or provider restart.
func (r *maintenanceRuntime) localConsumerCommand(ctx context.Context, c crlMaintenanceConsumer, pod deploymentplan.Object, input []byte, args ...string) ([]byte, error) {
	if pod == nil {
		var err error
		pod, err = r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
		if err != nil {
			return nil, err
		}
	}
	key := c.Namespace + "/" + c.Pod + "/" + c.Container
	h := r.localRecovery[key]
	execPrefix := []string{"--namespace", c.Namespace, "exec"}
	if input != nil {
		execPrefix = append(execPrefix, "-i")
	}
	if !r.localConsumerUsesHelper(c, pod) {
		return r.command(ctx, input, append(append(execPrefix, "pod/"+c.Pod, "--container", c.Container, "--"), args...)...)
	}
	if h == nil {
		var err error
		h, err = r.createLocalRecovery(ctx, c, pod)
		if err != nil {
			return nil, err
		}
	}
	if err := r.verifyLocalRecovery(ctx, h); err != nil {
		return nil, err
	}
	return r.command(ctx, input, append(append(execPrefix, "pod/"+h.name, "--container", "maintenance", "--"), args...)...)
}

func (r *maintenanceRuntime) localConsumerUsesHelper(c crlMaintenanceConsumer, pod deploymentplan.Object) bool {
	if r.localRecovery[c.Namespace+"/"+c.Pod+"/"+c.Container] != nil {
		return true
	}
	status := managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], c.Container)
	source := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], c.Container)
	runningCustody := mqttFoundationEnv(source, "PKI_STATE_BACKEND") == "openbao" && maintenanceConsumerRunning(pod, c.Container)
	return status["ready"] == false && !runningCustody
}

func (r *maintenanceRuntime) createLocalRecovery(ctx context.Context, c crlMaintenanceConsumer, source deploymentplan.Object) (*maintenanceLocalRecovery, error) {
	bad := func() (*maintenanceLocalRecovery, error) {
		return nil, errors.New("original CRL PVC recovery requires the same owned Pod, controller, bound claims and selected native helper")
	}
	meta, spec := deploymentplan.Map(source["metadata"]), deploymentplan.Map(source["spec"])
	uid := deploymentplan.String(meta["uid"])
	if uid == "" || c.PodUID != "" && c.PodUID != uid || meta["name"] != c.Pod || meta["namespace"] != c.Namespace || meta["deletionTimestamp"] != nil || !strings.HasPrefix(c.Namespace, r.cfg.Values["CLOUD_STACK_NAME"]+"-") || deploymentplan.String(spec["nodeName"]) == "" {
		return bad()
	}
	c.PodUID = uid
	h := &maintenanceLocalRecovery{consumer: c, sourceSHA: deploymentplan.Digest(spec), claims: map[string]string{}}
	for _, raw := range certIssuerObjectList(meta["ownerReferences"]) {
		ref := deploymentplan.Map(raw)
		if ref["controller"] != true {
			continue
		}
		kind := deploymentplan.String(ref["kind"])
		if h.parentID != "" || kind != "StatefulSet" && kind != "ReplicaSet" && kind != "Deployment" {
			return bad()
		}
		h.parentID, h.parentUID = kind+"/"+c.Namespace+"/"+deploymentplan.String(ref["name"]), deploymentplan.String(ref["uid"])
	}
	parent, err := r.read(ctx, h.parentID)
	if err != nil || h.parentUID == "" || deploymentplan.Map(parent["metadata"])["uid"] != h.parentUID {
		return bad()
	}
	if r.image == "" && r.ensureImage != nil {
		r.image, err = r.ensureImage(ctx)
	}
	if err != nil || !immutableEnvironmentImage.MatchString(r.image) {
		return bad()
	}
	timeout, err := environmentRolloutTimeout(r.cfg.Values)
	if err != nil {
		return nil, err
	}
	lifetime := timeout
	if deadline, ok := ctx.Deadline(); ok {
		lifetime = time.Until(deadline)
		if lifetime <= 0 {
			return nil, context.DeadlineExceeded
		}
	}
	container := deploymentplan.Clone(managedNamedField(spec["containers"], c.Container))
	if container == nil || mqttFoundationEnv(container, "PKI_STATE_BACKEND") == "openbao" {
		return bad()
	}
	for _, name := range []string{"args", "livenessProbe", "startupProbe", "lifecycle", "ports"} {
		delete(container, name)
	}
	container["name"], container["image"] = "maintenance", r.image
	container["command"] = []any{"/bin/sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"}
	container["readinessProbe"] = deploymentplan.Object{"exec": deploymentplan.Object{"command": []any{"/bin/sh", "-ec", "test -x /app/pkitrust"}}, "periodSeconds": float64(2)}
	var volumes []any
	volumeNames := map[string]bool{}
	for _, mv := range certIssuerObjectList(container["volumeMounts"]) {
		mount := deploymentplan.Map(mv)
		volume := deploymentplan.Clone(managedNamedField(spec["volumes"], deploymentplan.String(mount["name"])))
		if volume == nil || mount["subPath"] != nil || mount["subPathExpr"] != nil {
			return bad()
		}
		if pvc := deploymentplan.Map(volume["persistentVolumeClaim"]); pvc != nil {
			id := "PersistentVolumeClaim/" + c.Namespace + "/" + deploymentplan.String(pvc["claimName"])
			claim, err := r.read(ctx, id)
			claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
			if err != nil || claimUID == "" || deploymentplan.Map(claim["status"])["phase"] != "Bound" {
				return bad()
			}
			h.claims[id] = claimUID
			if r.lease == nil {
				pvc["readOnly"], mount["readOnly"] = true, true
			}
		}
		name := deploymentplan.String(volume["name"])
		if !volumeNames[name] {
			volumes = append(volumes, volume)
			volumeNames[name] = true
		}
	}
	if len(h.claims) == 0 {
		return bad()
	}
	security := deploymentplan.Clone(deploymentplan.Map(spec["securityContext"]))
	// Mounting an existing identity for inspection must never chmod its tree.
	delete(security, "fsGroup")
	delete(security, "fsGroupChangePolicy")
	ps := deploymentplan.Object{"nodeName": spec["nodeName"], "restartPolicy": "Never", "activeDeadlineSeconds": float64((lifetime + time.Second - 1) / time.Second), "terminationGracePeriodSeconds": float64(1), "automountServiceAccountToken": false, "securityContext": security, "containers": []any{container}, "volumes": volumes}
	for _, name := range []string{"imagePullSecrets", "tolerations", "dnsPolicy", "dnsConfig"} {
		if value, ok := spec[name]; ok {
			ps[name] = value
		}
	}
	h.manifest = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"generateName": "crl-pvc-recovery-", "namespace": c.Namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": r.cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/name": "crl-maintenance"}, "annotations": deploymentplan.Object{"rtk.realtek.com/original-pod-uid": uid, "rtk.realtek.com/original-state": deploymentplan.Digest(h.claims)}}, "spec": ps}
	body, _ := json.Marshal(h.manifest)
	raw, err := r.command(ctx, body, "--namespace", c.Namespace, "create", "-f", "-", "-o", "json")
	var created deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &created) != nil {
		return bad()
	}
	h.name, h.uid = deploymentplan.String(deploymentplan.Map(created["metadata"])["name"]), deploymentplan.String(deploymentplan.Map(created["metadata"])["uid"])
	if !strings.HasPrefix(h.name, "crl-pvc-recovery-") || h.uid == "" {
		return bad()
	}
	if r.localRecovery == nil {
		r.localRecovery = map[string]*maintenanceLocalRecovery{}
	}
	r.localRecovery[c.Namespace+"/"+c.Pod+"/"+c.Container] = h
	if _, err = r.command(ctx, nil, "--namespace", c.Namespace, "wait", "--for=condition=Ready", "pod/"+h.name, "--timeout="+timeout.String()); err != nil {
		return nil, errors.New("original CRL PVC helper did not become ready; preserve original state")
	}
	return h, nil
}

func (r *maintenanceRuntime) verifyLocalRecovery(ctx context.Context, h *maintenanceLocalRecovery) error {
	bad := errors.New("original CRL PVC inspection owner, helper or claim changed")
	pod, err := r.read(ctx, "Pod/"+h.consumer.Namespace+"/"+h.consumer.Pod)
	if err != nil || deploymentplan.Map(pod["metadata"])["uid"] != h.consumer.PodUID || deploymentplan.Digest(pod["spec"]) != h.sourceSHA {
		return bad
	}
	parent, err := r.read(ctx, h.parentID)
	if err != nil || deploymentplan.Map(parent["metadata"])["uid"] != h.parentUID {
		return bad
	}
	for id, uid := range h.claims {
		claim, err := r.read(ctx, id)
		if err != nil || deploymentplan.Map(claim["metadata"])["uid"] != uid {
			return bad
		}
	}
	helper, err := r.read(ctx, "Pod/"+h.consumer.Namespace+"/"+h.name)
	if err != nil || deploymentplan.Map(helper["metadata"])["uid"] != h.uid || !deploymentplan.Matches(h.manifest, helper) {
		return bad
	}
	return nil
}

func (r *maintenanceRuntime) closeLocalRecovery(ctx context.Context) error {
	var result error
	for key, h := range r.localRecovery {
		body, _ := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": h.uid}, "gracePeriodSeconds": float64(0)})
		endpoint := "/api/v1/namespaces/" + url.PathEscape(h.consumer.Namespace) + "/pods/" + url.PathEscape(h.name)
		_, err := r.command(ctx, body, "delete", "--raw", endpoint, "-f", "-")
		if err == nil {
			_, err = r.command(ctx, nil, "--namespace", h.consumer.Namespace, "wait", "--for=delete", "pod/"+h.name, "--timeout=30s")
		}
		if err != nil {
			result = errors.Join(result, errors.New("original CRL PVC helper cleanup could not be confirmed"))
		} else {
			delete(r.localRecovery, key)
		}
	}
	return result
}

// The root transport's retained signed startup snapshot must advance before
// any online signer or KV reader depends on OpenBao becoming available.
func (r *maintenanceRuntime) refreshOpenBaoStartup(ctx context.Context, inventory crlMaintenanceInventory) error {
	seen := map[string]bool{}
	for _, c := range inventory.Consumers {
		key := c.Namespace + "/" + c.PodUID + "/" + c.Container
		if seen[key] || c.Custody || c.Namespace != r.cfg.Values["CLOUD_STACK_NAME"]+"-secrets" || c.Pod != "openbao-0" || c.Container != "openbao-pki" {
			continue
		}
		seen[key] = true
		pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
		meta, spec := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(pod["spec"])
		source := managedNamedField(spec["containers"], c.Container)
		startup := mqttFoundationEnv(source, "OPENBAO_PKI_STARTUP_TRUST")
		if startup == "" {
			continue
		}
		if err != nil || meta["uid"] != c.PodUID || !filepath.IsAbs(startup) || filepath.Clean(startup) != startup || r.lease == nil {
			return errors.New("original OpenBao startup trust recovery scope is unavailable")
		}
		owned := false
		for _, raw := range certIssuerObjectList(meta["ownerReferences"]) {
			ref := deploymentplan.Map(raw)
			if ref["controller"] == true && ref["kind"] == "StatefulSet" && ref["name"] == "openbao" && deploymentplan.String(ref["uid"]) != "" {
				parent, err := r.read(ctx, "StatefulSet/"+c.Namespace+"/openbao")
				owned = err == nil && deploymentplan.Map(parent["metadata"])["uid"] == ref["uid"]
			}
		}
		provider := managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "openbao")
		providerID := deploymentplan.String(provider["containerID"])
		if !owned || providerID == "" || r.lease.Check(ctx) != nil {
			return errors.New("original OpenBao provider owner is unavailable for startup trust recovery")
		}
		binary := "/app/openbaopkihost" // The qualified native helper image contract.
		if !r.localConsumerUsesHelper(c, pod) {
			command := certIssuerObjectList(source["command"])
			if len(command) != 1 {
				return errors.New("original OpenBao host startup command is unavailable")
			}
			binary = deploymentplan.String(command[0])
			if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || filepath.Base(binary) != "openbaopkihost" {
				return errors.New("original OpenBao host startup command is invalid")
			}
		}
		if _, err := r.localConsumerCommand(ctx, c, pod, nil, binary, "prepare-startup"); err != nil {
			return secretCheckFailure(err, "original OpenBao signed startup trust could not be refreshed from the current registry")
		}
		// Restoring the same process is maintenance, not a workload rollout.
		// Its caller deadline also covers native kubelet restart backoff.
		bounded := ctx
		err = func() error {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				current, err := r.read(bounded, "Pod/"+c.Namespace+"/"+c.Pod)
				if bounded.Err() != nil {
					return errors.New("original OpenBao did not resume within the maintenance deadline")
				}
				status := deploymentplan.Map(current["status"])
				main := managedNamedField(status["containerStatuses"], "openbao")
				host := managedNamedField(status["containerStatuses"], c.Container)
				if err != nil || deploymentplan.Map(current["metadata"])["uid"] != c.PodUID || deploymentplan.Digest(current["spec"]) != deploymentplan.Digest(spec) || main["containerID"] != providerID || r.lease.Check(bounded) != nil {
					return errors.New("original OpenBao Pod, provider or execution ownership changed during trust recovery")
				}
				if main["ready"] == true && host["ready"] == true {
					return nil
				}
				select {
				case <-bounded.Done():
					return errors.New("original OpenBao did not resume after signed startup trust recovery")
				case <-ticker.C:
				}
			}
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
