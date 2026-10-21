package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"debug/elf"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCreateHostIdentityPolicy = "cloud-init-ed25519-host-key/v1"
const environmentCreateHostIdentityTemplate = "#cloud-config\nssh_keys:\n  ed25519_private: <persisted-selected-VM-key>\n  ed25519_public: <matching-public-key>\n"

// Only a genuinely new selected VM permits host-key generation. Interrupted
// material is retained and a missing derived public file can be reconstructed
// from its existing private key without replacing the identity.
func prepareEnvironmentCreateProviderMaterial(ctx context.Context, cfg deploymentConfig, planSHA string, resource environmentCreateProviderResource, store secretStore) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource.Kind != "instance" {
		return nil, nil
	}
	if store.Environment != cfg.Environment || !environmentPreflightDigest.MatchString(planSHA) || !secretEnvironmentPattern.MatchString(resource.Key) || !strings.HasPrefix(resource.Name, cfg.Values["CLOUD_STACK_NAME"]+"-") {
		return nil, createProviderBlocker("exact selected VM and creation plan are required for host identity material")
	}
	privateRel := filepath.Join("deployment", "create", "material", planSHA, "host-identity", resource.Key, "ssh_host_ed25519_key")
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(privateRel)); err != nil {
		return nil, err
	}
	private, err := store.read(privateRel)
	if errors.Is(err, os.ErrNotExist) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, createProviderBlocker("new selected VM host identity cannot be generated")
		}
		block, err := ssh.MarshalPrivateKey(key, resource.Name)
		if err != nil {
			return nil, createProviderBlocker("new selected VM host identity cannot be encoded")
		}
		private = string(pem.EncodeToMemory(block))
		if err := store.write(privateRel, []byte(private), false); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, createProviderBlocker("original selected VM host private key cannot be read")
	}
	private = strings.TrimSpace(private) + "\n"
	key, err := ssh.ParseRawPrivateKey([]byte(private))
	if err != nil {
		return nil, createProviderBlocker("original selected VM host private key is invalid")
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, createProviderBlocker("original selected VM host identity is not Ed25519")
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err := writeEnvironmentCreateDerivedPrivateFile(store, privateRel+".pub", []byte(public+"\n")); err != nil {
		return nil, err
	}
	cloud, err := yaml.Marshal(map[string]any{"ssh_keys": map[string]string{"ed25519_private": private, "ed25519_public": public}})
	if err != nil {
		return nil, createProviderBlocker("persisted selected VM host bootstrap cannot be encoded")
	}
	cloud = append([]byte("#cloud-config\n"), cloud...)
	if err := validateEnvironmentCreateHostIdentityMaterial(cloud, public); err != nil {
		return nil, err
	}
	return map[string]string{"user_data": base64.StdEncoding.EncodeToString(cloud), "host_public_key": public, "host_key_sha256": environmentPreflightBytesSHA256([]byte(public))}, nil
}

func writeEnvironmentCreateDerivedPrivateFile(store secretStore, relative string, expected []byte) error {
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) || len(expected) == 0 || int64(len(expected)) > environmentDerivedArtifactMaxBytes {
		return createProviderBlocker("derived private creation state is not bound to the canonical selected environment")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	current, err := readBoundedDerivedArtifact(path)
	if err == nil {
		if !bytes.Equal(current, expected) && strings.TrimSpace(string(current)) != strings.TrimSpace(string(expected)) {
			return createProviderBlocker("retained selected creation material differs from its original identity")
		}
		return nil
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return createProviderBlocker("retained selected creation material cannot be inspected")
	}
	return store.write(relative, expected, false)
}

func installEnvironmentCreateKubeconfig(ctx context.Context, cfg deploymentConfig, store secretStore, cluster environmentCreateProviderReceipt, operator map[string]string, execution *environmentCreateExecution) error {
	if execution == nil || execution.Store.Root != store.Root {
		return createProviderBlocker("original selected create journal is required for Kubernetes access publication")
	}
	intent, _ := json.Marshal(map[string]any{"cluster": cluster, "destination": "kube/kubeconfig.yaml"})
	if err := execution.BeginEntry("provider-kube-access", "provider", intent, time.Now().UTC()); err != nil {
		return err
	}
	var kubeconfig []byte
	for {
		var err error
		kubeconfig, err = fetchEnvironmentCreateKubeconfig(ctx, cfg, cluster, operator)
		if err == nil {
			break
		}
		var native *environmentCreateProviderHTTPError
		if !errors.As(err, &native) || native.Status != 503 {
			return err
		}
		if err := waitEnvironmentCreateProviderPoll(ctx); err != nil {
			return err
		}
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, "kube/kubeconfig.yaml", kubeconfig); err != nil {
		return err
	}
	state := map[string]string{"LKE_CLUSTER_ID": cluster.NativeID, "LKE_CLUSTER_LABEL": cluster.Name, "LKE_CLUSTER_REGION": cluster.Region}
	// The allocated version is read from the actual exact cluster, never chosen
	// again from a changing version catalog after the reviewed plan was consumed.
	raw, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], "GET", "/lke/clusters/"+cluster.NativeID, nil)
	var actual lkeCluster
	if err != nil || json.Unmarshal(raw, &actual) != nil || strconv.Itoa(actual.ID) != cluster.NativeID || actual.Label != cluster.Name || actual.Region != cluster.Region || !validEnvironmentCreateKubernetesVersion(actual.K8sVersion) {
		return createProviderBlocker("exact allocated cluster metadata cannot be verified before provider state publication")
	}
	state["LKE_CLUSTER_VERSION"] = actual.K8sVersion
	previous, err := readLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	if len(previous) > 0 && deploymentplan.Digest(previous) != deploymentplan.Digest(appendMap(state, map[string]string{"CLOUD_ENV_NAME": cfg.Environment, "CLOUD_STACK_NAME": cfg.Values["CLOUD_STACK_NAME"]})) {
		return createProviderBlocker("original provider state belongs to another allocation; new creation cannot overwrite it")
	}
	if err := writeLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"], state); err != nil {
		return err
	}
	backend, err := newEnvironmentKubernetesBackend(string(kubeconfig), cfg.Environment)
	if err != nil {
		return err
	}
	for {
		ready, err := backend.command(ctx, nil, "get", "--raw=/readyz")
		if err == nil && strings.TrimSpace(string(ready)) == "ok" {
			break
		}
		if err := waitEnvironmentCreateProviderPoll(ctx); err != nil {
			return err
		}
	}
	proof, _ := json.Marshal(map[string]string{"cluster_id": cluster.NativeID, "kubeconfig_sha256": environmentPreflightBytesSHA256(kubeconfig), "provider_state_sha256": deploymentplan.Digest(state)})
	if err := execution.RecordReceipt("provider-kube-access", proof); err != nil {
		return err
	}
	return execution.CompleteEntry("provider-kube-access", time.Now().UTC())
}

func waitEnvironmentCreateProviderPoll(ctx context.Context) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func environmentCreateSSHIdentity(store secretStore, plan environmentCreateProviderPlan) (string, error) {
	privatePath, err := privateDeploymentStatePath(store.Environment, "operator/ssh/id_ed25519")
	if err != nil {
		return "", err
	}
	privateRaw, err := readBoundedEnvironmentFile(privatePath, true)
	if err != nil {
		return "", createProviderBlocker("canonical selected deployment SSH private key is unavailable")
	}
	signer, err := ssh.ParsePrivateKey(privateRaw)
	if err != nil {
		return "", createProviderBlocker("canonical selected deployment SSH private key is invalid")
	}
	publicPath, err := privateDeploymentStatePath(store.Environment, "operator/ssh/id_ed25519.pub")
	if err != nil {
		return "", err
	}
	publicRaw, err := readBoundedEnvironmentFile(publicPath, true)
	if err != nil {
		return "", createProviderBlocker("canonical selected deployment SSH public key is unavailable")
	}
	parsed, _, _, trailing, err := ssh.ParseAuthorizedKey(publicRaw)
	if err != nil || len(bytes.TrimSpace(trailing)) != 0 || !bytes.Equal(parsed.Marshal(), signer.PublicKey().Marshal()) || environmentPreflightBytesSHA256([]byte(strings.TrimSpace(string(publicRaw)))) != plan.SSHKeySHA256 {
		return "", createProviderBlocker("canonical selected SSH key pair differs from the qualified deployment identity")
	}
	return privatePath, nil
}

func environmentCreatePinnedSSH(ctx context.Context, store secretStore, plan environmentCreateProviderPlan, planSHA string, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt, script string) ([]byte, error) {
	privatePath, err := environmentCreateSSHIdentity(store, plan)
	if err != nil {
		return nil, err
	}
	if receipt.Key != resource.Key || receipt.Name != resource.Name || receipt.Kind != "instance" || receipt.NativeID == "" || net.ParseIP(receipt.PublicIP) == nil || isPrivateNetIP(net.ParseIP(receipt.PublicIP)) {
		return nil, createProviderBlocker("exact newly allocated public VM identity is required for SSH configuration")
	}
	if !environmentPreflightDigest.MatchString(planSHA) || !secretEnvironmentPattern.MatchString(resource.Key) {
		return nil, createProviderBlocker("original creation and exact VM keys are required for pinned SSH")
	}
	hostRel := filepath.Join("deployment", "create", "material", planSHA, "host-identity", resource.Key, "ssh_host_ed25519_key")
	hostPath, err := privateDeploymentStatePath(store.Environment, hostRel)
	if err != nil {
		return nil, err
	}
	hostRaw, err := readBoundedEnvironmentFile(hostPath, true)
	if err != nil {
		return nil, createProviderBlocker("original allocated VM host identity is unavailable; configuration cannot regenerate it")
	}
	hostSigner, err := ssh.ParsePrivateKey(hostRaw)
	if err != nil || hostSigner.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, createProviderBlocker("original allocated VM host identity is invalid")
	}
	hostPublic := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))
	savedPublic, err := store.read(hostRel + ".pub")
	if err != nil || savedPublic != hostPublic {
		return nil, createProviderBlocker("original allocated VM host public and private identities disagree")
	}
	knownRel := filepath.Join("deployment", "create", "material", planSHA, "host-identity", resource.Key, "known_hosts")
	if err := writeEnvironmentCreateDerivedPrivateFile(store, knownRel, []byte(receipt.PublicIP+" "+hostPublic+"\n")); err != nil {
		return nil, err
	}
	knownPath, err := store.safePath(knownRel)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "ssh", "-T", "-i", privatePath, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+knownPath, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "ControlMaster=no", "-o", "LogLevel=ERROR", "--", "root@"+receipt.PublicIP, "bash", "-s")
	command.WaitDelay = time.Second
	command.Stdin = strings.NewReader(script)
	var output environmentAPIOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, createProviderBlocker("exact pinned VM SSH configuration failed; original private journal and host identity are retained")
	}
	return output.buffer.Bytes(), nil
}

// Build routes only from selected placement intent and this cluster's actual
// private node addresses. An empty MQTT backend is never an installable target.
func environmentCreateDataPlaneUpstreams(values map[string]string, raw []byte) ([]lkeEdgeHAProxyUpstream, error) {
	mqttClass, classLabel := strings.TrimSpace(values["MQTT_NODE_CLASS"]), strings.TrimSpace(values["NODE_CLASS_LABEL_KEY"])
	if mqttClass == "" || classLabel == "" {
		return nil, createProviderBlocker("selected MQTT node placement is required before external edge configuration")
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes.Items) == 0 {
		return nil, createProviderBlocker("selected Kubernetes node private address inventory is invalid")
	}
	var upstreams []lkeEdgeHAProxyUpstream
	mqttNodes := 0
	for _, node := range nodes.Items {
		address := ""
		for _, candidate := range node.Status.Addresses {
			if candidate.Type == "InternalIP" && net.ParseIP(candidate.Address) != nil && isPrivateNetIP(net.ParseIP(candidate.Address)) {
				address = candidate.Address
				break
			}
		}
		if address == "" {
			return nil, createProviderBlocker("an actual selected Kubernetes node lacks its required private upstream address")
		}
		upstreams = append(upstreams, lkeEdgeHAProxyUpstream{Name: node.Metadata.Name, Address: address, Port: 30443, Kind: "https"})
		if node.Metadata.Labels[classLabel] == mqttClass {
			mqttNodes++
			upstreams = append(upstreams, lkeEdgeHAProxyUpstream{Name: node.Metadata.Name, Address: address, Port: 31883, Kind: "mqtts"})
		}
	}
	if mqttNodes == 0 {
		return nil, createProviderBlocker("actual cluster has no private MQTT upstream in its selected node class")
	}
	sort.Slice(upstreams, func(i, j int) bool {
		if upstreams[i].Kind != upstreams[j].Kind {
			return upstreams[i].Kind < upstreams[j].Kind
		}
		return upstreams[i].Name < upstreams[j].Name
	})
	return upstreams, nil
}

// Recovery's network gate and actual routing consume this identical read-only
// node selection. Execution repeats it before writing any external configuration.
func environmentCreateDataPlaneNodeUpstreams(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend) ([]lkeEdgeHAProxyUpstream, error) {
	raw, err := backend.command(ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, createProviderBlocker("actual selected Kubernetes node private addresses cannot be read for external edge configuration")
	}
	return environmentCreateDataPlaneUpstreams(environmentCreateProviderValues(cfg), raw)
}

func configureEnvironmentCreateExternalDataPlanes(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution) error {
	if plan == nil || execution == nil {
		return createProviderBlocker("complete selected creation plan and original execution journal are required for data-plane configuration")
	}
	kubeconfig, err := store.read("kube/kubeconfig.yaml")
	if err != nil {
		return err
	}
	backend, err := newEnvironmentKubernetesBackend(kubeconfig, cfg.Environment)
	if err != nil {
		return err
	}
	v := environmentCreateProviderValues(cfg)
	upstreams, err := environmentCreateDataPlaneNodeUpstreams(ctx, cfg, backend)
	if err != nil {
		return err
	}
	for _, resource := range plan.Provider.Resources {
		if resource.Kind != "instance" {
			continue
		}
		receipt, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, receipts[resource.Key], operator)
		if err != nil || !ready {
			return createProviderBlocker("the exact newly allocated VM is not ready or its native owner cannot be verified")
		}
		intent, _ := json.Marshal(map[string]any{"resource": resource, "receipt": receipt, "upstreams": upstreams, "release_sha256": deploymentplan.Digest(plan.Release)})
		key := "configure-" + resource.Key
		normalTail, err := createOriginalRoutingContext(ctx, execution)
		if err != nil {
			return err
		}
		if normalTail && createOpenBaoACKCompletionEntry(execution.Journal, key) != nil {
			var recorded struct {
				Native           environmentCreateProviderReceipt `json:"native"`
				ConfigurationSHA string                           `json:"configuration_sha256"`
				RuntimeSHA       string                           `json:"runtime_sha256"`
			}
			entry := createOpenBaoACKCompletionEntry(execution.Journal, key)
			if decodeEnvironmentJSON(entry.Receipt, &recorded) != nil || !environmentPreflightDigest.MatchString(recorded.RuntimeSHA) {
				return createProviderRecoveryBlock("data-plane outcome is not recorded: " + key)
			}
			proof, _ := json.Marshal(map[string]any{"native": receipt, "configuration_sha256": deploymentplan.Digest(intent), "runtime_sha256": recorded.RuntimeSHA})
			done, err := createNormalTailRecordedStage(ctx, execution, key, "routing", intent, proof, func(ctx context.Context) error {
				sha, err := createNormalTailDataPlaneReadback(ctx, store, plan, execution, resource, receipt)
				if err != nil {
					return err
				}
				if sha != recorded.RuntimeSHA {
					return createProviderRecoveryBlock("recorded data-plane configuration changed: " + key)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		if err := execution.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
			return err
		}
		if resource.Key == "edge" {
			maxconn, err := positiveIntValue("EDGE_MAX_CONNECTIONS", v["EDGE_MAX_CONNECTIONS"])
			if err != nil {
				return err
			}
			edgePlan := lkeEdgeHAProxyPlan{Mode: "external-haproxy", EdgeVMs: []lkeEdgeHAProxyVM{{ID: mustCreateProviderID(receipt.NativeID), Label: receipt.Name, PublicIP: receipt.PublicIP, PrivateIP: receipt.PrivateIP, Region: receipt.Region}}, Upstreams: upstreams, HTTPSNodePort: 30443, MQTTSNodePort: 31883, MaxConn: maxconn, ProxyProtocol: false}
			script := renderLKEEdgeHAProxyInstallScript(edgePlan, renderLKEEdgeHAProxyConfig(edgePlan)) + "\nsystemctl is-active haproxy >/dev/null\nss -lnt | grep -E '[:.]443[[:space:]]' >/dev/null\nss -lnt | grep -E '[:.]8883[[:space:]]' >/dev/null\n"
			if _, err := environmentCreatePinnedSSH(ctx, store, plan.Provider, environmentCreateHostMaterialPlan(execution), resource, receipt, script); err != nil {
				return err
			}
		} else {
			if err := configureEnvironmentCreateTURN(ctx, cfg, plan, resource, receipt, store, execution); err != nil {
				return err
			}
		}
		proofBody := map[string]any{"native": receipt, "configuration_sha256": deploymentplan.Digest(intent)}
		if normalTail {
			sha, err := createNormalTailDataPlaneReadback(ctx, store, plan, execution, resource, receipt)
			if err != nil {
				return err
			}
			proofBody["runtime_sha256"] = sha
		}
		proof, _ := json.Marshal(proofBody)
		if err := execution.RecordReceipt(key, proof); err != nil {
			return err
		}
		if err := execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func mustCreateProviderID(raw string) int { id, _ := strconv.Atoi(raw); return id }

// Extract the registrar from the exact formal CI image, never build source on
// the operator. The immutable package must actually contain its required binary.
func extractEnvironmentCreateTURNRegistrar(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore) (string, error) {
	image := plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	if !immutableEnvironmentImage.MatchString(image) || plan.Provider.TurnRegistrarImage != "" && plan.Provider.TurnRegistrarImage != image {
		return "", createProviderBlocker("formal immutable TURN registrar package is unavailable")
	}
	rel := filepath.Join("deployment", "create", "artifacts", environmentPreflightBytesSHA256([]byte(image)), "turnregistrar")
	path, err := store.safePath(rel)
	if err != nil {
		return "", err
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(rel)); err != nil {
		return "", err
	}
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil || operator["GHCR_PULL_USERNAME"] == "" || operator["GHCR_PULL_TOKEN"] == "" || strings.ContainsAny(operator["GHCR_PULL_USERNAME"]+operator["GHCR_PULL_TOKEN"], "\r\n") {
		return "", createProviderBlocker("canonical selected private registry access is required for formal TURN artifact inspection")
	}
	authRel := filepath.Join(filepath.Dir(rel), "docker", "config.json")
	auth, _ := json.Marshal(map[string]any{"auths": map[string]any{"ghcr.io": map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(operator["GHCR_PULL_USERNAME"] + ":" + operator["GHCR_PULL_TOKEN"]))}}})
	if err := writeEnvironmentCreateDerivedPrivateFile(store, authRel, auth); err != nil {
		return "", err
	}
	authPath, _ := store.safePath(filepath.Dir(authRel))
	cmd := exec.CommandContext(ctx, "docker", "--config", authPath, "create", "--platform", "linux/amd64", "--entrypoint", "/app/turnregistrar", image)
	cmd.WaitDelay = time.Second
	raw, err := cmd.Output()
	container := strings.TrimSpace(string(raw))
	if err != nil || len(container) != 64 || strings.Trim(container, "0123456789abcdef") != "" {
		return "", createProviderBlocker("exact formal registrar image cannot be opened; publish the canonical CI image and restore private registry access")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "--config", authPath, "rm", container).Run()
	}()
	file, err := os.CreateTemp(filepath.Dir(path), ".turnregistrar-*")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	defer os.Remove(temporary)
	if err := exec.CommandContext(ctx, "docker", "--config", authPath, "cp", container+":/app/turnregistrar", temporary).Run(); err != nil {
		return "", &deploymentplan.Blocker{ID: "deployment.create.provider", Resource: "/app/turnregistrar", Cause: "exact formal CI image does not contain the required TURN registrar binary", Next: "publish the canonical Video Cloud CI image with its required native bootstrap programs and image-manifest.json, then repeat complete create preflight; local builds and mutable tags cannot replace formal release evidence"}
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		return "", err
	}
	info, err := os.Lstat(temporary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() == 0 || info.Size() > 128<<20 {
		return "", createProviderBlocker("formal registrar binary is not a bounded private regular artifact")
	}
	artifact, err := os.ReadFile(temporary)
	if err != nil {
		return "", err
	}
	if plan.Provider.TurnRegistrarSHA256 != "" && environmentPreflightBytesSHA256(artifact) != plan.Provider.TurnRegistrarSHA256 {
		return "", createProviderBlocker("formal registrar binary differs from the exact create qualification")
	}
	linux, err := elf.Open(temporary)
	if err != nil {
		return "", createProviderBlocker("formal registrar binary is not a Linux ELF package")
	}
	valid := linux.Machine == elf.EM_X86_64 && linux.Class == elf.ELFCLASS64 && (linux.Type == elf.ET_EXEC || linux.Type == elf.ET_DYN)
	linux.Close()
	if !valid {
		return "", createProviderBlocker("formal registrar package does not match the frozen Linux amd64 VM architecture")
	}
	if prior, err := os.Lstat(path); err == nil {
		if !prior.Mode().IsRegular() || prior.Mode().Perm() != 0600 {
			return "", createProviderBlocker("retained formal registrar artifact has invalid private permissions")
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		fresh, err := os.ReadFile(temporary)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(old, fresh) {
			return "", createProviderBlocker("formal registrar artifact changed for the same immutable CI image")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Link(temporary, path); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	return path, nil
}

func qualifyEnvironmentCreateDataPlaneArtifact(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore) (string, error) {
	path, err := extractEnvironmentCreateTURNRegistrar(ctx, cfg, plan, store)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return environmentPreflightBytesSHA256(raw), nil
}

func configureEnvironmentCreateTURN(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt, store secretStore, execution *environmentCreateExecution) error {
	index, err := strconv.Atoi(strings.TrimPrefix(resource.Key, "turn-"))
	if err != nil || index < 1 || index > 5 {
		return createProviderBlocker("frozen TURN relay owner is invalid")
	}
	values := environmentCreateProviderValues(cfg)
	values = appendMap(values, map[string]string{"LKE_COTURN_VM_NAME": fmt.Sprintf("turn%02d", index), "LKE_COTURN_VM_INDEX": strconv.Itoa(index), "LKE_COTURN_VM_REGION": receipt.Region})
	shared, err := store.readRuntime("turn-shared")
	if err != nil {
		return createProviderBlocker("original selected TURN shared runtime credential is unavailable")
	}
	registry, err := store.readRuntime("turn-registry-node-auth")
	if err != nil {
		return createProviderBlocker("original selected TURN registry authorization is unavailable")
	}
	cli, err := retainCoturnCLIPassword(store, values["LKE_COTURN_VM_NAME"], "", true)
	if err != nil {
		return err
	}
	binary, err := extractEnvironmentCreateTURNRegistrar(ctx, cfg, plan, store)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	// Transfer only the formally selected binary and credentials over stdin.
	transfer := "set -eu\nbase64 -d >/tmp/video-cloud-turnregistrar <<'RTK_REGISTRAR_BINARY'\n" + base64.StdEncoding.EncodeToString(raw) + "\nRTK_REGISTRAR_BINARY\nchmod 0755 /tmp/video-cloud-turnregistrar\n"
	var install string
	environmentRenderMutex.Lock()
	clear := map[string]string{}
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if strings.HasPrefix(key, "LKE_") {
			clear[key] = ""
		}
	}
	restore := installAllCredentialEnvironment(appendMap(clear, values))
	install = renderLKECoturnInstallScript(values)
	restore()
	environmentRenderMutex.Unlock()
	install = strings.ReplaceAll(install, "'VIDEO_CLOUD_ENV=staging'", shellQuote("VIDEO_CLOUD_ENV="+cfg.Environment))
	if position := strings.Index(install, "systemctl enable video-cloud-turnregistrar\n"); position >= 0 {
		install = install[:position] + "systemctl enable video-cloud-turnregistrar\n"
	} else {
		return createProviderBlocker("formal TURN install template lacks its reviewed activation boundary")
	}
	script := transfer + "export VIDEO_CLOUD_COTURN_SHARED_SECRET=" + shellQuote(shared) + "\nexport VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=" + shellQuote(registry) + "\nexport COTURN_CLI_PASSWORD=" + shellQuote(cli) + "\n" + install
	_, err = environmentCreatePinnedSSH(ctx, store, plan.Provider, environmentCreateHostMaterialPlan(execution), resource, receipt, script)
	return err
}

func activateEnvironmentCreateTURNRegistrars(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution) error {
	for _, resource := range plan.Provider.Resources {
		if resource.Kind != "instance" || resource.Key == "edge" {
			continue
		}
		receipt, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, receipts[resource.Key], operator)
		if err != nil || !ready {
			return createProviderBlocker("original exact TURN allocation cannot be verified before registrar activation")
		}
		key := "activate-" + resource.Key
		intent, _ := json.Marshal(map[string]any{"native": receipt, "registry": buildGenericDNSPlan(cfg)})
		proof, _ := json.Marshal(map[string]any{"native": receipt, "registration": "verified-runtime-success"})
		normalTail, err := createOriginalRoutingContext(ctx, execution)
		if err != nil {
			return err
		}
		if normalTail {
			done, err := resumeEnvironmentCreateTURNActivation(ctx, cfg, plan, store, execution, resource, receipt, key, intent, proof)
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		if err := execution.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
			return err
		}
		script := "set -eu\nsystemctl enable --now video-cloud-turnregistrar\nfor attempt in $(seq 1 20); do\n  systemctl is-active coturn >/dev/null\n  systemctl is-active video-cloud-turnregistrar >/dev/null\n  if journalctl -u video-cloud-turnregistrar --since '2 minutes ago' --no-pager | grep -q 'turn registry register succeeded'; then exit 0; fi\n  sleep 3\ndone\nexit 1\n"
		if _, err := environmentCreatePinnedSSH(ctx, store, plan.Provider, environmentCreateHostMaterialPlan(execution), resource, receipt, script); err != nil {
			return err
		}
		if err := execution.RecordReceipt(key, proof); err != nil {
			return err
		}
		if err := execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func environmentCreateHostMaterialPlan(execution *environmentCreateExecution) string {
	if execution.Journal.ProviderMaterialPlanSHA256 != "" {
		// Recovery preserves original VM host keys rather than generating keys
		// for an already allocated VM under the new recovery plan digest.
		return execution.Journal.ProviderMaterialPlanSHA256
	}
	return execution.Journal.PlanSHA256
}
