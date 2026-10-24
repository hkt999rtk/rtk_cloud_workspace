package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateOwnedServerMaterial struct {
	Certificate string `json:"certificate_pem"`
	Key         string `json:"private_key_pem"`
	Root        string `json:"root_sha256"`
	Name        string `json:"server_name"`
	Environment string `json:"environment"`
}

// Native keys remain on their original owner PVC. The adapter captures the
// registry-verified exporter in memory and publishes only to that owner's TLS
// Secret. Export receipts/logs contain no key; ordinary admitted Secret plans
// remain private recovery data in the canonical 0600 child plan store.
func publishEnvironmentCreateApplicationServers(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) error {
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Name != "video-cloud-api-pki" && owner.Name != "video-cloud-api-app-pki" {
			continue
		}
		namespace := p.Stack + "-video-cloud"
		name := owner.Name + "." + namespace + ".svc"
		serverState := filepath.Join(filepath.Dir(owner.ClientState), "server.json")
		issuer := "certissuer." + namespace + ".svc"
		settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_SERVER_DOMAIN": "service", "PKI_SERVER_NAME": name, "PKI_SERVER_DNS_NAMES": name, "PKI_SERVER_ROOT_SHA256": service.Root.RootFingerprint, "PKI_SERVER_IDENTITY_STATE": serverState, "PKI_SERVER_BOOTSTRAP_REQUEST_ID": service.BootstrapSessionID + ":" + owner.Name + ":server", "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": owner.ClientState, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:" + owner.Role, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint, "PKI_CLIENT_ISSUER_URL": "https://" + issuer + ":18445", "PKI_CLIENT_ISSUER_SERVER_PKI_NAME": issuer, "PKI_CLIENT_ISSUER_TLS_CA": environmentCreateServiceTrust + "/root.pem"}
		job := environmentCreateServiceEnrollmentJob(p, namespace, owner.Name+"-server", owner.Claim, service, []any{"/app/serviceidentity-bootstrap", "server-bootstrap"}, settings, false)
		environmentCreateTransportJobOwner(job.Object, p.Image, owner.StateRoot, serverState, 10001)
		pod := environmentPKIPod(job.Object)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		container["volumeMounts"] = environmentCreateRemoveNamed(container["volumeMounts"], "bootstrap")
		pod["volumes"] = environmentCreateRemoveNamed(pod["volumes"], "bootstrap")
		stage := "server-enrollment-" + owner.Name
		retained, err := verifyManagedTransportAcceptedExportStage(ctx, runtime, stage, job)
		if err != nil {
			return err
		}
		if !retained {
			if err := apply(ctx, stage, []deploymentplan.Definition{job}); err != nil {
				return err
			}
		}
		material, err := readEnvironmentCreateOwnedServer(ctx, cfg, p, runtime, service, namespace, owner.Name, owner.Role, owner.Claim, owner.StateRoot, owner.ClientState, serverState, name, apply)
		if err != nil {
			return err
		}
		if err := apply(ctx, "server-secret-"+owner.Name, []deploymentplan.Definition{environmentCreateSecret(namespace, owner.Name+"-tls", map[string]string{"tls.crt": material.Certificate, "tls.key": material.Key, "ca.crt": service.Root.Certificate})}); err != nil {
			return err
		}
	}
	return nil
}

func readEnvironmentCreateOwnedServer(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, namespace, owner, role, claim, stateRoot, clientState, serverState, serverName string, apply environmentCreateBootstrapApply) (environmentCreateOwnedServerMaterial, error) {
	var empty environmentCreateOwnedServerMaterial
	if runtime.Execution == nil || runtime.Execution.Store.Environment != cfg.Environment || runtime.Backend.Store.Root != runtime.Execution.Store.Root || runtime.Backend.Store.Environment != cfg.Environment || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || !environmentPreflightDigest.MatchString(runtime.ExecutionSHA256) || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Stack != runtime.Execution.Journal.Stack || !immutableEnvironmentImage.MatchString(p.Image) || service.Root.Environment != cfg.Environment || service.Root.Domain != "service" || service.Root.Status != "active" || !environmentPreflightDigest.MatchString(service.Root.RootFingerprint) || apply == nil || !environmentCreateExportOwnerMatches(p, namespace, owner, role, claim, stateRoot, clientState, serverState, serverName) {
		return empty, environmentCreateBootstrapBlocker("native server export requires its exact original selected private owner")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return empty, err
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.completedTail != nil && owner == "account-manager" {
		return readManagedLateCompletedAccountServer(ctx, cfg, p, runtime, service, namespace, role, claim, stateRoot, clientState, serverState, serverName)
	}
	claimID := "PersistentVolumeClaim/" + namespace + "/" + claim
	originalClaim, err := runtime.Backend.Read(ctx, claimID)
	if err != nil || originalClaim == nil || deploymentplan.String(deploymentplan.Map(originalClaim["metadata"])["uid"]) == "" || deploymentplan.Map(originalClaim["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(deploymentplan.Map(originalClaim["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack {
		return empty, environmentCreateBootstrapBlocker("native serving key owner claim cannot be read before export")
	}
	claimUID := deploymentplan.Map(originalClaim["metadata"])["uid"]
	readerName := "pki-export-" + owner + "-" + runtime.ExecutionSHA256[:12]
	labels := deploymentplan.Object{"app.kubernetes.io/name": readerName, "rtk.realtek.com/stack": p.Stack}
	readerLabels := deploymentplan.Clone(labels)
	readerLabels["app.kubernetes.io/managed-by"] = "rtk-cloud"
	settings := map[string]string{"PKI_ENVIRONMENT": p.Environment, "PKI_SERVER_NAME": serverName, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": clientState, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:" + role, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint}
	container := deploymentplan.Object{"name": "custody", "image": p.Image, "command": []any{"/bin/sh", "-ec", "exec sleep 3600"}, "env": append(environmentCreateEnv(settings), environmentCreateServiceDatabaseEnv("pki-verifier-database")), "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": []any{environmentCreateServiceMount("owner-state", stateRoot, false)}}
	reader := deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": readerName, "namespace": namespace, "labels": readerLabels}, "spec": deploymentplan.Object{"replicas": float64(1), "strategy": deploymentplan.Object{"type": "Recreate"}, "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": readerName}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{"automountServiceAccountToken": false, "imagePullSecrets": []any{deploymentplan.Object{"name": "ghcr-pull"}}, "securityContext": environmentCreateServicePodSecurity(), "containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("owner-state", "persistentVolumeClaim", claim)}}}}}}
	stage := "server-custody-" + owner
	retained, err := verifyManagedTransportAcceptedExportStage(ctx, runtime, stage, reader)
	if err != nil {
		return empty, err
	}
	if !retained {
		if err := apply(ctx, stage, []deploymentplan.Definition{reader}); err != nil {
			return empty, err
		}
	}
	readerID := "Deployment/" + namespace + "/" + readerName
	actualReader, err := runtime.Backend.Read(ctx, readerID)
	if err != nil || actualReader == nil || deploymentplan.String(deploymentplan.Map(actualReader["metadata"])["uid"]) == "" {
		return empty, environmentCreateBootstrapBlocker("native export reader owner cannot be verified")
	}
	readerUID := deploymentplan.Map(actualReader["metadata"])["uid"]
	ownedPod, err := selectEnvironmentCreateExportPod(ctx, runtime.Backend, reader.Object, deploymentplan.String(readerUID))
	if err != nil {
		return empty, err
	}
	verify := func() error {
		if err := runtime.Execution.Check(ctx); err != nil {
			return err
		}
		claimNow, err := runtime.Backend.Read(ctx, claimID)
		readerNow, readerErr := runtime.Backend.Read(ctx, readerID)
		if err != nil || readerErr != nil || claimNow == nil || readerNow == nil || deploymentplan.Map(claimNow["metadata"])["uid"] != claimUID || deploymentplan.Map(claimNow["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(deploymentplan.Map(claimNow["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack || deploymentplan.Map(readerNow["metadata"])["uid"] != readerUID || deploymentplan.Map(readerNow["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(reader.Object), deploymentplan.Projection(readerNow)) {
			return environmentCreateBootstrapBlocker("original native server export PVC or reader changed")
		}
		return ownedPod.Verify(ctx, runtime.Backend, reader.Object, deploymentplan.String(readerUID))
	}
	if err := verify(); err != nil {
		return empty, err
	}
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", namespace, "exec", "pod/"+ownedPod.Name, "--container", "custody", "--", "/app/serviceidentity-bootstrap", "export-server", serverState, "service", "service:"+role, service.Root.RootFingerprint, serverName)
	var material environmentCreateOwnedServerMaterial
	if err != nil || len(raw) > 256<<10 || decodeEnvironmentJSON(raw, &material) != nil || material.Environment != cfg.Environment || material.Root != service.Root.RootFingerprint || material.Name != serverName || material.Certificate == "" || material.Key == "" {
		return empty, environmentCreateBootstrapBlocker("original native serving credential cannot be exported with exact registry and owner verification")
	}
	identity, err := tls.X509KeyPair([]byte(material.Certificate), []byte(material.Key))
	if err != nil || len(identity.Certificate) < 2 {
		return empty, environmentCreateBootstrapBlocker("original native serving certificate and key do not form its registered chain")
	}
	if err := verify(); err != nil {
		return empty, err
	}
	intent, _ := json.Marshal(deploymentplan.Object{"owner": owner, "namespace": namespace, "claim_uid": claimUID, "reader_uid": readerUID, "pod_uid": ownedPod.UID, "replicaset_uid": ownedPod.ReplicaSetUID, "server_name": serverName, "root_sha256": material.Root})
	key := "server-export-" + owner
	proof, _ := json.Marshal(deploymentplan.Object{"certificate_sha256": environmentPreflightBytesSHA256([]byte(material.Certificate)), "claim_uid": claimUID, "reader_uid": readerUID, "pod_uid": ownedPod.UID, "replicaset_uid": ownedPod.ReplicaSetUID, "server_name": serverName})
	retained, err = verifyManagedTransportRetainedServerExport(runtime, owner, intent, proof)
	if err != nil {
		return empty, err
	}
	if !retained {
		if err := runtime.Execution.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
			return empty, err
		}
		if err := runtime.Execution.RecordReceipt(key, proof); err != nil {
			return empty, err
		}
		if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return empty, err
		}
	}
	if err := retireEnvironmentCreateOwnedDeploymentInNamespace(ctx, cfg, runtime, namespace, readerName, apply); err != nil {
		return empty, err
	}
	return material, nil
}

func environmentCreateExportOwnerMatches(p environmentCreateBootstrapPlan, namespace, name, role, claim, root, client, server, serverName string) bool {
	if name == "account-manager" {
		return namespace == p.Stack+"-account-manager" && role == name && claim == "account-manager-service-state" && root == environmentCreateServiceState && client == root+"/private/client.json" && server == root+"/private/server.json" && serverName == name+"."+namespace+".svc.cluster.local"
	}
	for _, owner := range environmentCreateApplicationOwners() {
		if name == owner.Name && (name == "video-cloud-api-pki" || name == "video-cloud-api-app-pki") {
			return namespace == p.Stack+"-video-cloud" && role == owner.Role && claim == owner.Claim && root == owner.StateRoot && client == owner.ClientState && server == filepath.Join(filepath.Dir(client), "server.json") && serverName == name+"."+namespace+".svc"
		}
	}
	return false
}

type environmentCreateExportPod struct {
	Namespace, Name, UID, ReplicaSetName, ReplicaSetUID string
}

func selectEnvironmentCreateExportPod(ctx context.Context, backend environmentKubernetesBackend, reader deploymentplan.Object, readerUID string) (environmentCreateExportPod, error) {
	var selected environmentCreateExportPod
	meta := deploymentplan.Map(reader["metadata"])
	namespace, name := deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"])
	raw, err := backend.command(ctx, nil, "get", "pods", "--namespace", namespace, "--selector", "app.kubernetes.io/name="+name, "-o", "json")
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	// Native Kubernetes List envelopes include apiVersion/kind/metadata.
	// Their API defaults are not a strict deployment-document schema; every
	// selected item still passes the original Pod/ReplicaSet/reader UID fences.
	if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return selected, environmentCreateBootstrapBlocker("native export requires one exact original owned reader Pod")
	}
	pod := list.Items[0]
	podMeta := deploymentplan.Map(pod["metadata"])
	refs := certIssuerObjectList(podMeta["ownerReferences"])
	if len(refs) != 1 {
		return selected, environmentCreateBootstrapBlocker("native export reader Pod ownership is absent or ambiguous")
	}
	ref := deploymentplan.Map(refs[0])
	selected = environmentCreateExportPod{namespace, deploymentplan.String(podMeta["name"]), deploymentplan.String(podMeta["uid"]), deploymentplan.String(ref["name"]), deploymentplan.String(ref["uid"])}
	if !secretEnvironmentPattern.MatchString(selected.Name) || !secretEnvironmentPattern.MatchString(selected.ReplicaSetName) || selected.UID == "" || selected.ReplicaSetUID == "" || ref["controller"] != true || ref["kind"] != "ReplicaSet" {
		return environmentCreateExportPod{}, environmentCreateBootstrapBlocker("native export reader Pod has no exact native ReplicaSet owner")
	}
	if err := selected.Verify(ctx, backend, reader, readerUID); err != nil {
		return environmentCreateExportPod{}, err
	}
	return selected, nil
}

func (owner environmentCreateExportPod) Verify(ctx context.Context, backend environmentKubernetesBackend, reader deploymentplan.Object, readerUID string) error {
	pod, podErr := backend.Read(ctx, "Pod/"+owner.Namespace+"/"+owner.Name)
	rs, rsErr := backend.Read(ctx, "ReplicaSet/"+owner.Namespace+"/"+owner.ReplicaSetName)
	if podErr != nil || rsErr != nil || pod == nil || rs == nil {
		return environmentCreateBootstrapBlocker("original native export Pod or ReplicaSet cannot be verified")
	}
	meta, rsMeta := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(rs["metadata"])
	podRefs, rsRefs := certIssuerObjectList(meta["ownerReferences"]), certIssuerObjectList(rsMeta["ownerReferences"])
	if meta["uid"] != owner.UID || rsMeta["uid"] != owner.ReplicaSetUID || meta["deletionTimestamp"] != nil || rsMeta["deletionTimestamp"] != nil || len(podRefs) != 1 || len(rsRefs) != 1 {
		return environmentCreateBootstrapBlocker("original native export Pod or ReplicaSet changed")
	}
	podRef, rsRef := deploymentplan.Map(podRefs[0]), deploymentplan.Map(rsRefs[0])
	readerMeta := deploymentplan.Map(reader["metadata"])
	template := deploymentplan.Map(deploymentplan.Map(reader["spec"])["template"])
	if podRef["controller"] != true || podRef["kind"] != "ReplicaSet" || podRef["name"] != owner.ReplicaSetName || podRef["uid"] != owner.ReplicaSetUID || rsRef["controller"] != true || rsRef["kind"] != "Deployment" || rsRef["name"] != readerMeta["name"] || rsRef["uid"] != readerUID || !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": template["spec"]}, deploymentplan.Object{"kind": "Pod", "spec": pod["spec"]}) || !deploymentplan.Matches(deploymentplan.Map(deploymentplan.Map(template["metadata"])["labels"]), deploymentplan.Map(meta["labels"])) || deploymentplan.Map(pod["status"])["phase"] != "Running" {
		return environmentCreateBootstrapBlocker("native export cannot execute in a foreign reader Pod or changed claim/image recipe")
	}
	return nil
}
