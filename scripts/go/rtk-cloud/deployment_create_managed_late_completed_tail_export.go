package main

import (
	"context"
	"crypto/tls"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Re-export only the already registered serving identity from its original
// permanent owner. The completed, retired exporter and its receipt stay intact.
func readManagedLateCompletedAccountServer(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, namespace, role, claim, stateRoot, clientState, serverState, serverName string) (environmentCreateOwnedServerMaterial, error) {
	var empty environmentCreateOwnedServerMaterial
	bad := func() (environmentCreateOwnedServerMaterial, error) {
		return empty, environmentCreateBootstrapBlocker("completed account export requires its original registered owner and certificate")
	}
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if r == nil || r.completedTail == nil || verifyManagedLateCompletedTailSource(ctx, cfg, e.Store, e, r) != nil || !environmentCreateExportOwnerMatches(p, namespace, "account-manager", role, claim, stateRoot, clientState, serverState, serverName) {
		return bad()
	}
	var receipt struct {
		Certificate string `json:"certificate_sha256"`
		Claim       string `json:"claim_uid"`
		Pod         string `json:"pod_uid"`
		Reader      string `json:"reader_uid"`
		ReplicaSet  string `json:"replicaset_uid"`
		Server      string `json:"server_name"`
	}
	if decodeEnvironmentJSON(e.Journal.Entries[425].Receipt, &receipt) != nil {
		return bad()
	}
	definitions, err := managedLateCurrentCoreDefinitions(runtime)
	if err != nil {
		return empty, err
	}
	id := "Deployment/" + namespace + "/account-manager"
	var desired deploymentplan.Object
	for _, d := range definitions {
		m := deploymentplan.Map(d.Object["metadata"])
		if d.Object["kind"] == "Deployment" && m["namespace"] == namespace && m["name"] == "account-manager" {
			if desired != nil {
				return bad()
			}
			desired = d.Object
		}
	}
	owner := r.ManagedLateSession.Owners[id]
	if desired == nil || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || !managedLateCompletedAccountExportRecipe(desired, claim, clientState, serverState, serverName, service.Root.RootFingerprint) {
		return bad()
	}
	reader, cj, err := managedTransportPostAcceptedStage(e.Store, e, e.Journal.Entries[424].Key)
	if err != nil || len(reader.Resources) != 1 || cj.Owners[reader.Resources[0].ID] != receipt.Reader || !managedLateCompletedExportReaderMatches(reader.Resources[0].Desired, p, service, namespace, claim, stateRoot, clientState, serverName) {
		return bad()
	}
	claimID := "PersistentVolumeClaim/" + namespace + "/" + claim
	current, err := runtime.Backend.Read(ctx, id)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != owner.UID || deploymentplan.Map(current["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(desired), deploymentplan.Projection(current)) {
		return bad()
	}
	pod, err := selectEnvironmentCreateExportPod(ctx, runtime.Backend, desired, owner.UID)
	if err != nil {
		return empty, err
	}
	verify := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		o, oe := runtime.Backend.Read(ctx, id)
		v, ve := runtime.Backend.Read(ctx, claimID)
		if oe != nil || ve != nil || o == nil || v == nil || deploymentplan.Map(o["metadata"])["uid"] != owner.UID || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(desired), deploymentplan.Projection(o)) || deploymentplan.Map(v["metadata"])["uid"] != receipt.Claim || deploymentplan.Map(v["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(deploymentplan.Map(v["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack {
			return environmentCreateBootstrapBlocker("completed account original owner or claim changed")
		}
		return pod.Verify(ctx, runtime.Backend, desired, owner.UID)
	}
	if err := verify(); err != nil {
		return empty, err
	}
	args := []string{"--namespace", namespace, "exec", "pod/" + pod.Name, "--container", "pkimanagement", "--", "env", "PKI_ENVIRONMENT=" + p.Environment, "PKI_SERVER_NAME=" + serverName, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE=" + clientState, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT=service:" + role, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256=" + service.Root.RootFingerprint, "/app/serviceidentity-bootstrap", "export-server", serverState, "service", "service:" + role, service.Root.RootFingerprint, serverName}
	raw, err := runtime.Backend.command(ctx, nil, args...)
	var material environmentCreateOwnedServerMaterial
	if err != nil || len(raw) > 256<<10 || decodeEnvironmentJSON(raw, &material) != nil || material.Environment != cfg.Environment || material.Root != service.Root.RootFingerprint || material.Name != serverName || environmentPreflightBytesSHA256([]byte(material.Certificate)) != receipt.Certificate || material.Key == "" {
		return bad()
	}
	identity, err := tls.X509KeyPair([]byte(material.Certificate), []byte(material.Key))
	if err != nil || len(identity.Certificate) < 2 {
		return bad()
	}
	if err := verify(); err != nil {
		return empty, err
	}
	return material, nil
}

func managedLateCompletedAccountExportRecipe(owner deploymentplan.Object, claim, client, server, name, root string) bool {
	pod := environmentPKIPod(owner)
	var management deploymentplan.Object
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		if c["name"] == "pkimanagement" {
			if management != nil {
				return false
			}
			management = c
		}
	}
	if management == nil || !immutableEnvironmentImage.MatchString(deploymentplan.String(management["image"])) || deploymentplan.Digest(management["command"]) != deploymentplan.Digest([]any{"/app/pkimanagement"}) {
		return false
	}
	settings := map[string]string{"PKI_MANAGEMENT_IDENTITY_STATE": client, "PKI_MANAGEMENT_IDENTITY_ROOT_SHA256": root, "PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE": server, "PKI_MANAGEMENT_ACCOUNT_HOST_NAME": name, "PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256": root}
	for key, value := range settings {
		if mqttFoundationEnv(management, key) != value {
			return false
		}
	}
	return managedLateCompletedClaimMount(pod, management, "service-state", claim, environmentCreateServiceState)
}

func managedLateCompletedClaimMount(pod, container deploymentplan.Object, volume, claim, path string) bool {
	mounts, volumes := 0, 0
	for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if m["name"] == volume {
			if m["mountPath"] != path || m["readOnly"] == true || m["subPath"] != nil || m["subPathExpr"] != nil {
				return false
			}
			mounts++
		}
	}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		if v["name"] == volume {
			pvc := deploymentplan.Map(v["persistentVolumeClaim"])
			if pvc["claimName"] != claim || pvc["readOnly"] == true {
				return false
			}
			volumes++
		}
	}
	return mounts == 1 && volumes == 1
}

func managedLateCompletedExportReaderMatches(owner deploymentplan.Object, p environmentCreateBootstrapPlan, service environmentCreateServiceCeremony, namespace, claim, state, client, name string) bool {
	pod := environmentPKIPod(owner)
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 1 {
		return false
	}
	c := deploymentplan.Map(containers[0])
	m := deploymentplan.Map(owner["metadata"])
	if owner["kind"] != "Deployment" || m["namespace"] != namespace || c["name"] != "custody" || c["image"] != p.Image || deploymentplan.Digest(c["command"]) != deploymentplan.Digest([]any{"/bin/sh", "-ec", "exec sleep 3600"}) {
		return false
	}
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_SERVER_NAME": name, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": client, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:account-manager", "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint}
	for key, value := range settings {
		if mqttFoundationEnv(c, key) != value {
			return false
		}
	}
	return managedLateCompletedClaimMount(pod, c, "owner-state", claim, state)
}
