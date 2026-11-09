package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCustodyCommand func(context.Context, []byte, ...string) ([]byte, error)

// The ConfigMap write is not proof that an existing TLS client has received its
// projection. Check only active original clients; a stopped process holds no old
// TLS connection. This never waits for a failed Controller's HTTP endpoint.
func checkEnvironmentCustodyTransportProjection(ctx context.Context, binding *environmentCustodyBinding, snapshot environmentCustodyTransportTrust, command environmentCustodyCommand) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("original running custody clients have not installed the published OpenBao transport snapshot")
	}
	if binding == nil || binding.Generation != snapshot.Generation || snapshot.Environment == "" || command == nil {
		return bad()
	}
	owners := map[string]environmentCustodyOwnerBinding{}
	namespaces := map[string]string{}
	for _, owner := range binding.Owners {
		owners[snapshot.Environment+"/"+binding.Generation+"/"+owner.ID] = owner
		namespaces[owner.Namespace] = owner.Namespace
	}
	if len(owners) == 0 {
		return bad()
	}
	wanted, _ := json.Marshal(snapshot)
	for _, namespace := range sortedMapKeys(namespaces) {
		raw, err := command(ctx, nil, "--namespace", namespace, "get", "pods", "-o", "json")
		var list struct {
			Items []deploymentplan.Object `json:"items"`
		}
		if err != nil || len(raw) > 8<<20 || json.Unmarshal(raw, &list) != nil {
			return bad()
		}
		for _, pod := range list.Items {
			meta := deploymentplan.Map(pod["metadata"])
			status := deploymentplan.Map(pod["status"])
			if meta["namespace"] != namespace {
				return bad()
			}
			if meta["deletionTimestamp"] != nil || status["phase"] != "Running" {
				continue
			}
			spec := deploymentplan.Map(pod["spec"])
			for _, cv := range certIssuerObjectList(spec["containers"]) {
				c := deploymentplan.Map(cv)
				if mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" || mqttFoundationEnv(c, "PKI_STATE_TRANSPORT_TRUST") == "" {
					continue
				}
				running := false
				for _, sv := range certIssuerObjectList(status["containerStatuses"]) {
					s := deploymentplan.Map(sv)
					running = running || (s["name"] == c["name"] && deploymentplan.Map(s["state"])["running"] != nil)
				}
				if !running {
					continue
				}
				owner, ok := owners[mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")]
				if !ok || owner.Namespace != namespace || spec["serviceAccountName"] != owner.ServiceAccount || meta["uid"] == "" || meta["name"] == "" {
					return bad()
				}
				mount := managedNamedField(c["volumeMounts"], "pki-custody-trust")
				volume := managedNamedField(spec["volumes"], "pki-custody-trust")
				file := environmentCustodyTrustPath + "/transport.json"
				if mqttFoundationEnv(c, "PKI_STATE_TRANSPORT_TRUST") != file || mount["readOnly"] != true || mount["mountPath"] != environmentCustodyTrustPath || mount["subPath"] != nil || mount["subPathExpr"] != nil || deploymentplan.Map(volume["configMap"])["name"] != environmentCustodyTrustName {
					return bad()
				}
				mounted, err := command(ctx, nil, "--namespace", namespace, "exec", "pod/"+deploymentplan.String(meta["name"]), "--container", deploymentplan.String(c["name"]), "--", "cat", file)
				if err != nil || len(mounted) > 256<<10 || deploymentplan.Digest(jsonRawObject(mounted)) != deploymentplan.Digest(jsonRawObject(wanted)) {
					return bad()
				}
				after, err := command(ctx, nil, "--namespace", namespace, "get", "pod", deploymentplan.String(meta["name"]), "-o", "json")
				var checked deploymentplan.Object
				if err != nil || len(after) > 2<<20 || json.Unmarshal(after, &checked) != nil || deploymentplan.Map(checked["metadata"])["uid"] != meta["uid"] || deploymentplan.Map(checked["metadata"])["deletionTimestamp"] != nil || deploymentplan.Digest(checked["spec"]) != deploymentplan.Digest(spec) {
					return bad()
				}
			}
		}
	}
	return nil
}

func publishAndWaitEnvironmentCreateCustodyTrust(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, managed environmentCreateRegisteredIssuer, managedOnly bool, apply environmentCreateBootstrapApply) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	if err := publishEnvironmentCreateCustodyTrust(ctx, cfg, runtime, &managed, managedOnly, apply); err != nil {
		return err
	}
	snapshot, _, err := readEnvironmentCustodyTransportState(cfg, runtime.Execution.Store, runtime.ExecutionSHA256)
	if err != nil {
		return err
	}
	timeout, err := environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := runtime.Execution.Check(bounded); err != nil {
			return err
		}
		if err := checkEnvironmentCustodyTransportProjection(bounded, runtime.Custody, snapshot, runtime.Backend.command); err == nil {
			return nil
		}
		select {
		case <-bounded.Done():
			return environmentCreateBootstrapBlocker("published OpenBao transport snapshot has not reached original running clients within the selected rollout deadline")
		case <-timer.C:
		}
	}
}
