package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateOpenBaoBootstrap struct {
	RootTokenPath string
	URL           string
	CACertPath    string
}

func renderEnvironmentCreateOpenBaoFoundation(cfg deploymentConfig, release environmentRelease, tlsSecretName, caConfigName string) ([]deploymentplan.Definition, error) {
	v := environmentCreateProviderValues(cfg)
	if cfg.Environment == "prod" || v["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || v["OPENBAO_STORAGE_BACKEND"] != "file" || v["OPENBAO_EFFECTIVE_REPLICAS"] != "1" || !secretEnvironmentPattern.MatchString(tlsSecretName) || !secretEnvironmentPattern.MatchString(caConfigName) {
		return nil, environmentCreateBlocker("deployment.create.openbao", "initial file OpenBao requires the explicit non-production single-writer profile and exact TLS references")
	}
	v["LKE_OPENBAO_IMAGE"] = release.Images["LKE_OPENBAO_IMAGE"].Reference
	if !immutableEnvironmentImage.MatchString(v["LKE_OPENBAO_IMAGE"]) {
		return nil, environmentCreateBlocker("deployment.create.openbao", "initial OpenBao requires the formally resolved immutable provider image")
	}
	object, err := renderEnvironmentOpenBaoRaft(v, environmentPodBindings{Kind: "StatefulSet", NamespaceSuffix: "secrets", Name: "openbao"})
	if err != nil {
		return nil, err
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	metadata := func(name string) deploymentplan.Object { return deploymentplan.Object{"name": name, "namespace": ns} }
	hcl := `ui = false
disable_mlock = true
listener "tcp" {
  address = "[::]:8200"
  cluster_address = "[::]:8201"
  tls_cert_file = "/openbao/transport/tls.crt"
  tls_key_file = "/openbao/transport/tls.key"
  tls_min_version = "tls12"
}
storage "file" {
  path = "/openbao/data"
}
audit "file" "deployment-audit" {
  options {
    file_path = "/openbao/audit/audit.jsonl"
    log_raw = "false"
  }
}
`
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata("openbao-file"), "data": deploymentplan.Object{"file.hcl": hcl}}
	pod := environmentPKIPod(object)
	delete(pod, "affinity")
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	main["command"] = []any{"bao", "server", "-config=/openbao/config/file.hcl"}
	main["env"] = []any{deploymentplan.Object{"name": "BAO_ADDR", "value": "https://127.0.0.1:8200"}, deploymentplan.Object{"name": "BAO_CACERT", "value": "/openbao/transport/ca.crt"}, deploymentplan.Object{"name": "BAO_TLS_SERVER_NAME", "value": "openbao." + ns + ".svc"}}
	main["volumeMounts"] = []any{deploymentplan.Object{"name": "data", "mountPath": "/openbao/data"}, deploymentplan.Object{"name": "audit", "mountPath": "/openbao/audit"}, deploymentplan.Object{"name": "file-config", "mountPath": "/openbao/config", "readOnly": true}, deploymentplan.Object{"name": "transport-tls", "mountPath": "/openbao/transport", "readOnly": true}, deploymentplan.Object{"name": "openbao-kubernetes-reviewer", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true}}
	// Bootstrap readiness only establishes a reachable initialized-or-sealed API.
	// Actual initialization, unseal/auth and managed PKI acceptance follow below.
	main["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": float64(8200)}, "periodSeconds": float64(5), "timeoutSeconds": float64(3)}
	pod["volumes"] = []any{deploymentplan.Object{"name": "file-config", "configMap": deploymentplan.Object{"name": "openbao-file"}}, deploymentplan.Object{"name": "transport-tls", "secret": deploymentplan.Object{"secretName": tlsSecretName, "defaultMode": float64(288)}}, deploymentplan.Object{"name": "openbao-kubernetes-reviewer", "projected": deploymentplan.Object{"defaultMode": float64(288), "sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(900), "path": "token"}}, deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}}}}}}
	defs := []deploymentplan.Definition{{Object: config, Phase: "configuration"}}
	for _, dependency := range environmentOpenBaoRaftDependencies(v)[1:] {
		if deploymentplan.String(dependency["kind"]) == "NetworkPolicy" {
			deploymentplan.Map(dependency["metadata"])["name"] = "allow-openbao-file"
		}
		defs = append(defs, deploymentplan.Definition{Object: dependency, Phase: "configuration"})
	}
	reviewer := deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": deploymentplan.Object{"name": cfg.Values["CLOUD_STACK_NAME"] + "-openbao-reviewer"}, "roleRef": deploymentplan.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "system:auth-delegator"}, "subjects": []any{deploymentplan.Object{"kind": "ServiceAccount", "name": "openbao", "namespace": ns}}}
	bootstrapAccess := deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": metadata("allow-openbao-bootstrap"), "spec": deploymentplan.Object{"podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "openbao"}}, "policyTypes": []any{"Ingress"}, "ingress": []any{deploymentplan.Object{"from": []any{deploymentplan.Object{"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"kubernetes.io/metadata.name": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}}}}, "ports": []any{deploymentplan.Object{"protocol": "TCP", "port": float64(8200)}}}}}}
	defs = append(defs, deploymentplan.Definition{Object: reviewer, Phase: "configuration"}, deploymentplan.Definition{Object: bootstrapAccess, Phase: "configuration"}, deploymentplan.Definition{Object: object, Phase: "workload"})
	return defs, nil
}

func environmentCreateOpenBaoExec(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, input []byte, script string) ([]byte, error) {
	if err := checkManagedLateNativeGuard(ctx); err != nil {
		return nil, err
	}
	if backend.Store.Environment != cfg.Environment {
		return nil, environmentCreateBlocker("deployment.create.openbao", "selected Kubernetes identity does not match the new OpenBao owner")
	}
	args := []string{"--namespace", cfg.Values["CLOUD_STACK_NAME"] + "-secrets", "exec", "-i", "openbao-0", "--container", "openbao", "--", "env", "BAO_ADDR=https://127.0.0.1:8200", "BAO_TLS_SERVER_NAME=openbao." + cfg.Values["CLOUD_STACK_NAME"] + "-secrets.svc", "/bin/sh", "-ec", script}
	// The checked serving container owns its CA projection: bootstrap transport
	// and managed PKI mount different authorities. Preserve its exact selected
	// path instead of forcing the retired bootstrap mount after cutover.
	args[len(args)-1] = "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n" + script
	raw, err := backend.command(ctx, input, args...)
	if err != nil {
		return nil, environmentCreateBlocker("deployment.create.openbao", "selected exact OpenBao API request failed; original initialization/auth evidence is retained")
	}
	return raw, nil
}

func readEnvironmentCreateOpenBaoStatus(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend) (lkeOpenBaoStatus, error) {
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, backend, nil, "set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]")
	var status struct {
		Initialized *bool `json:"initialized"`
		Sealed      *bool `json:"sealed"`
	}
	if err != nil || json.Unmarshal(raw, &status) != nil || status.Initialized == nil || status.Sealed == nil {
		return lkeOpenBaoStatus{}, environmentCreateBlocker("deployment.create.openbao", "actual initialized/sealed state cannot be verified; no initialization is permitted")
	}
	return lkeOpenBaoStatus{Initialized: *status.Initialized, Sealed: *status.Sealed}, nil
}

func initializeEnvironmentCreateOpenBao(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend, execution *environmentCreateExecution) (environmentCreateOpenBaoBootstrap, error) {
	var result environmentCreateOpenBaoBootstrap
	if execution == nil || execution.Store.Root != store.Root || store.Environment != cfg.Environment || cfg.Environment == "prod" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" {
		return result, environmentCreateBlocker("deployment.create.openbao", "original non-production create execution and exact selected OpenBao owner are required")
	}
	for _, relative := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return result, err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return result, environmentCreateBlocker("deployment.create.openbao", "original OpenBao initialization or operator material exists or cannot be inspected; a fresh ceremony cannot replace it")
		}
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	controller, err := backend.Read(ctx, "StatefulSet/"+ns+"/openbao")
	if err != nil || controller == nil || deploymentplan.Map(deploymentplan.Map(controller["metadata"])["labels"])["rtk.realtek.com/stack"] != cfg.Values["CLOUD_STACK_NAME"] {
		return result, environmentCreateBlocker("deployment.create.openbao", "newly created OpenBao StatefulSet owner cannot be verified")
	}
	pod, err := backend.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil || validateOpenBaoPodOwner(controller, pod) != nil {
		return result, environmentCreateBlocker("deployment.create.openbao", "actual OpenBao Pod does not belong to the exact newly created StatefulSet UID")
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, backend)
	if err != nil {
		return result, err
	}
	if status.Initialized {
		return result, environmentCreateBlocker("deployment.create.openbao", "the selected OpenBao API is already initialized without this creation's original operator receipt")
	}
	intent, _ := json.Marshal(deploymentplan.Object{"pod_uid": deploymentplan.Map(pod["metadata"])["uid"], "statefulset_uid": deploymentplan.Map(controller["metadata"])["uid"], "key_shares": 1, "key_threshold": 1})
	if err := execution.BeginEntry("openbao-initialize", "bootstrap", intent, time.Now().UTC()); err != nil {
		return result, err
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, backend, nil, "bao operator init -key-shares=1 -key-threshold=1 -format=json")
	if err != nil {
		return result, err
	}
	var initialized struct {
		UnsealKeys []string `json:"unseal_keys_b64"`
		RootToken  string   `json:"root_token"`
	}
	if json.Unmarshal(raw, &initialized) != nil || len(initialized.UnsealKeys) != 1 || strings.TrimSpace(initialized.RootToken) == "" || strings.ContainsAny(initialized.RootToken, "\r\n") {
		return result, environmentCreateBlocker("deployment.create.openbao", "initialization response cannot be consumed; retain the pending original journal and reconcile actual OpenBao state")
	}
	decoded, err := base64.StdEncoding.DecodeString(initialized.UnsealKeys[0])
	if err != nil || len(decoded) < 16 {
		return result, environmentCreateBlocker("deployment.create.openbao", "initialization response contains an invalid unseal share; original outcome requires reconciliation")
	}
	// Persist the original complete sensitive response first, then its consumed
	// projections, before any unseal or auth writes can run.
	for _, file := range []struct {
		relative string
		raw      []byte
	}{{"openbao/init.json", raw}, {"openbao/unseal-key", []byte(initialized.UnsealKeys[0] + "\n")}, {"openbao/root-token", []byte(initialized.RootToken + "\n")}} {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, file.relative, file.raw); err != nil {
			return result, err
		}
	}
	proof, _ := json.Marshal(deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(raw), "pod_uid": deploymentplan.Map(pod["metadata"])["uid"], "statefulset_uid": deploymentplan.Map(controller["metadata"])["uid"], "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"})
	if err := execution.RecordReceipt("openbao-initialize", proof); err != nil {
		return result, err
	}
	if err := execution.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
		return result, err
	}
	unsealIntent, _ := json.Marshal(deploymentplan.Object{"pod_uid": deploymentplan.Map(pod["metadata"])["uid"], "unseal_sha256": environmentPreflightBytesSHA256([]byte(initialized.UnsealKeys[0]))})
	if err := execution.BeginEntry("openbao-unseal", "bootstrap", unsealIntent, time.Now().UTC()); err != nil {
		return result, err
	}
	unsealBody, _ := json.Marshal(map[string]string{"key": initialized.UnsealKeys[0]})
	if _, err := environmentCreateOpenBaoExec(ctx, cfg, backend, unsealBody, "bao write -format=json sys/unseal -"); err != nil {
		return result, err
	}
	status, err = readEnvironmentCreateOpenBaoStatus(ctx, cfg, backend)
	if err != nil || !status.Initialized || status.Sealed {
		return result, environmentCreateBlocker("deployment.create.openbao", "actual initialized/unsealed OpenBao state cannot be verified")
	}
	unsealProof, _ := json.Marshal(status)
	if err := execution.RecordReceipt("openbao-unseal", unsealProof); err != nil {
		return result, err
	}
	if err := execution.CompleteEntry("openbao-unseal", time.Now().UTC()); err != nil {
		return result, err
	}
	if err := configureEnvironmentCreateOpenBaoAudit(ctx, cfg, backend, execution, initialized.RootToken); err != nil {
		return result, err
	}
	if err := configureEnvironmentCreateOpenBaoKubernetesAuth(ctx, cfg, backend, execution, initialized.RootToken); err != nil {
		return result, err
	}
	rootPath, err := privateDeploymentStatePath(cfg.Environment, "openbao/root-token")
	if err != nil {
		return result, err
	}
	caPath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("deployment", "create", "material", execution.Journal.PlanSHA256, "openbao-transport", "ca.crt"))
	if err != nil {
		return result, err
	}
	if _, err := readBoundedEnvironmentFile(caPath, true); err != nil {
		return result, environmentCreateBlocker("deployment.create.openbao", "original selected OpenBao transport CA reference is unavailable")
	}
	result = environmentCreateOpenBaoBootstrap{RootTokenPath: rootPath, URL: "https://openbao." + ns + ".svc:8200", CACertPath: caPath}
	return result, nil
}

func configureEnvironmentCreateOpenBaoAudit(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution, rootToken string) error {
	intent, _ := json.Marshal(deploymentplan.Object{"path": "sys/audit/deployment-audit", "type": "file", "file_path": "/openbao/audit/audit.jsonl", "log_raw": false})
	if err := execution.BeginEntry("openbao-audit-enable", "bootstrap", intent, time.Now().UTC()); err != nil {
		return err
	}
	script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n"
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, backend, []byte(rootToken+"\n"), script)
	var actual deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &actual) != nil {
		return environmentCreateBlocker("deployment.create.openbao", "OpenBao audit device creation or actual readback failed")
	}
	device := deploymentplan.Map(actual["deployment-audit/"])
	options := deploymentplan.Map(device["options"])
	if device["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
		return environmentCreateBlocker("deployment.create.openbao", "actual OpenBao audit device does not preserve its dedicated PVC and redacted log policy")
	}
	proof, _ := json.Marshal(deploymentplan.Object{"audit_sha256": environmentPreflightBytesSHA256(raw)})
	if err := execution.RecordReceipt("openbao-audit-enable", proof); err != nil {
		return err
	}
	return execution.CompleteEntry("openbao-audit-enable", time.Now().UTC())
}

func configureEnvironmentCreateOpenBaoKubernetesAuth(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution, rootToken string) error {
	request := []struct {
		key, script string
		intent      any
	}{
		{"openbao-auth-enable", "bao auth enable -path=kubernetes kubernetes", deploymentplan.Object{"path": "auth/kubernetes", "type": "kubernetes"}},
		{"openbao-auth-config", "bao write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443 kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt disable_local_ca_jwt=false", deploymentplan.Object{"path": "auth/kubernetes/config", "reviewer": "rotating-local-projection", "host": "https://kubernetes.default.svc:443"}},
	}
	for _, step := range request {
		intent, _ := json.Marshal(step.intent)
		if err := execution.BeginEntry(step.key, "bootstrap", intent, time.Now().UTC()); err != nil {
			return err
		}
		script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n" + step.script + " >/dev/null\n"
		if _, err := environmentCreateOpenBaoExec(ctx, cfg, backend, []byte(rootToken+"\n"), script); err != nil {
			return err
		}
		readCommand := "bao auth list -format=json"
		if step.key == "openbao-auth-config" {
			readCommand = "bao read -format=json auth/kubernetes/config"
		}
		actual, err := environmentCreateOpenBaoExec(ctx, cfg, backend, []byte(rootToken+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"+readCommand+"\n")
		var observed deploymentplan.Object
		if err != nil || json.Unmarshal(actual, &observed) != nil {
			return environmentCreateBlocker("deployment.create.openbao", "configured OpenBao Kubernetes auth cannot be read back")
		}
		if step.key == "openbao-auth-enable" {
			if deploymentplan.Map(observed["kubernetes/"])["type"] != "kubernetes" {
				return environmentCreateBlocker("deployment.create.openbao", "the actual configured OpenBao auth mount is not Kubernetes")
			}
		} else {
			data := deploymentplan.Map(observed["data"])
			if data["kubernetes_host"] != "https://kubernetes.default.svc:443" || data["disable_local_ca_jwt"] != false || data["token_reviewer_jwt_set"] == true {
				return environmentCreateBlocker("deployment.create.openbao", "the actual Kubernetes auth configuration lacks its rotating selected local reviewer")
			}
		}
		proof, _ := json.Marshal(deploymentplan.Object{"configured": step.intent, "readback_sha256": environmentPreflightBytesSHA256(actual)})
		if err := execution.RecordReceipt(step.key, proof); err != nil {
			return err
		}
		if err := execution.CompleteEntry(step.key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}
