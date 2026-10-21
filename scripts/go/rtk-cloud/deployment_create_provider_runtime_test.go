package main

import (
	"context"
	"crypto/ed25519"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createProviderRuntimeFixture(t *testing.T) (deploymentConfig, secretStore, environmentCreateProviderPlan, environmentCreateProviderResource, map[string]string) {
	t.Helper()
	cfg, operator := createProviderTestFixture(t, "dev")
	createProviderTestAPI(t, nil)
	plan, err := buildEnvironmentCreateProviderPlan(context.Background(), cfg, operator)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	block, err := ssh.MarshalPrivateKey(key, "selected-operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(block), true); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519.pub", []byte(operator["DEPLOYMENT_SSH_PUBLIC_KEY"]+"\n"), true); err != nil {
		t.Fatal(err)
	}
	return cfg, store, plan, plan.Resources[1], operator
}

func TestFreshCreateProviderMaterialPersistsExactHostIdentityOnce(t *testing.T) {
	cfg, store, _, resource, _ := createProviderRuntimeFixture(t)
	sha := strings.Repeat("a", 64)
	first, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(second) {
		t.Fatalf("host identity changed on preparation repeat: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(first["user_data"])
	if err != nil || validateEnvironmentCreateHostIdentityMaterial(raw, first["host_public_key"]) != nil {
		t.Fatal("invalid constrained host key payload")
	}
	if first["root_pass"] != "" || first["host_key_sha256"] != environmentPreflightBytesSHA256([]byte(first["host_public_key"])) {
		t.Fatal("unneeded password or absent host proof")
	}
	path := filepath.Join(store.Root, "deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key")
	for _, name := range []string{path, path + ".pub"} {
		info, err := os.Lstat(name)
		if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
			t.Fatalf("unsafe host identity state: %s %v", name, err)
		}
	}
	if err := os.Remove(path + ".pub"); err != nil {
		t.Fatal(err)
	}
	derived, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(derived) {
		t.Fatal("deriving missing public half replaced private host identity", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store); err == nil {
		t.Fatal("unsafe original host private key was regenerated or accepted")
	}
}

func TestDerivedPrivateArtifactRoundTripsBeyondOrdinaryInputLimit(t *testing.T) {
	_, store, _, _, _ := createProviderRuntimeFixture(t)
	relative := filepath.Join("deployment/create/openbao-recovery", strings.Repeat("a", 64), "managed-late-session-source.json")
	payload := map[string]string{"schema": "rtk-cloud.test-large-derived-artifact/v1", "body": strings.Repeat("x", int(environmentInputMaxBytes)+1024)}
	raw := mustProviderRecoveryJSON(payload)
	if int64(len(raw)) <= environmentInputMaxBytes || int64(len(raw)) > environmentDerivedArtifactMaxBytes {
		t.Fatal("large derived fixture does not exercise the separate bound")
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, raw); err != nil {
		t.Fatal("large derived artifact could not be written", err)
	}
	path := filepath.Join(store.Root, relative)
	if _, err := readBoundedEnvironmentFile(path, true); err == nil {
		t.Fatal("ordinary environment input reader accepted a large deployment artifact")
	}
	var decoded map[string]string
	if err := createOpenBaoACKCompletionRead(store, relative, &decoded); err != nil || deploymentplan.Digest(decoded) != deploymentplan.Digest(payload) {
		t.Fatal("large derived artifact could not round-trip through its producer/consumer", err)
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, raw); err != nil {
		t.Fatal("idempotent large derived publication failed", err)
	}
}

func TestDerivedPrivateArtifactReadReportsExactFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  string
		mutate func(*testing.T, string)
	}{
		{name: "missing", stage: "managed-late-session-source.json-missing", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", stage: "managed-late-session-source.json-oversized", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Truncate(path, environmentDerivedArtifactMaxBytes+1); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permissions", stage: "completed-private-path", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, _, _, _ := createProviderRuntimeFixture(t)
			relative := filepath.Join("deployment/create/openbao-recovery", strings.Repeat("a", 64), "managed-late-session-source.json")
			if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(map[string]string{"schema": "fixture"})); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, filepath.Join(store.Root, relative))
			var decoded map[string]string
			err := createOpenBaoACKCompletionRead(store, relative, &decoded)
			if err == nil || !strings.Contains(err.Error(), tc.stage) {
				t.Fatalf("wrong exact read failure: %v", err)
			}
		})
	}
}

func TestFreshCreatePinnedSSHBindsCanonicalPairNativeIPAndOriginalHostKey(t *testing.T) {
	for _, mode := range []string{"valid", "missing-host", "changed-host", "changed-operator", "unsafe-key", "private-ip", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, plan, resource, _ := createProviderRuntimeFixture(t)
			sha := strings.Repeat("a", 64)
			if _, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			captured := filepath.Join(directory, "arguments")
			binary := filepath.Join(directory, "ssh")
			writeTestFile(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$RTK_CREATE_SSH_ARGUMENTS\"\ncat >/dev/null\nprintf 'verified\\n'\n")
			if err := os.Chmod(binary, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CREATE_SSH_ARGUMENTS", captured)
			t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.99")
			receipt := environmentCreateProviderReceipt{Key: resource.Key, Kind: "instance", NativeID: "123", Name: resource.Name, Region: resource.Region, PublicIP: "198.51.100.12"}
			host := filepath.Join(store.Root, "deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key")
			switch mode {
			case "missing-host":
				os.Remove(host)
			case "changed-host":
				store.write(filepath.Join("deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key.pub"), []byte("ssh-ed25519 changed"), true)
			case "changed-operator":
				store.write("operator/ssh/id_ed25519.pub", []byte("ssh-ed25519 changed"), true)
			case "unsafe-key":
				os.Chmod(filepath.Join(store.Root, "operator", "ssh", "id_ed25519"), 0644)
			case "private-ip":
				receipt.PublicIP = "192.168.1.12"
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			output, err := environmentCreatePinnedSSH(ctx, store, plan, sha, resource, receipt, "echo private-runtime-credential\n")
			if mode == "valid" {
				if err != nil || string(output) != "verified\n" {
					t.Fatal("pinned SSH failed", err)
				}
				args := readTestFile(t, captured)
				if !strings.Contains(args, "StrictHostKeyChecking=yes") || !strings.Contains(args, "root@198.51.100.12") || !strings.Contains(args, filepath.Join(store.Root, "operator", "ssh", "id_ed25519")) || strings.Contains(args, "198.51.100.99") || strings.Contains(args, "private-runtime-credential") {
					t.Fatal("SSH target/key/secret boundary incorrect", args)
				}
				known := readTestFile(t, filepath.Join(filepath.Dir(host), "known_hosts"))
				if !strings.HasPrefix(known, "198.51.100.12 ssh-ed25519 ") {
					t.Fatal("host key pin does not bind original native IP")
				}
			} else {
				if err == nil {
					t.Fatal("SSH accepted invalid original target identity")
				}
				if _, err := os.Lstat(captured); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid target reached SSH", err)
				}
				if mode == "missing-host" {
					if _, err := os.Lstat(host); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("missing allocated host identity was regenerated")
					}
				}
			}
		})
	}
}

func TestFreshCreateCloudInitCannotCarryArbitraryHostCode(t *testing.T) {
	cfg, store, _, resource, _ := createProviderRuntimeFixture(t)
	material, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, strings.Repeat("a", 64), resource, store)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(material["user_data"])
	for _, malformed := range [][]byte{append(append([]byte{}, raw...), []byte("runcmd:\n  - echo unsupported\n")...), []byte("#cloud-config\nssh_keys: {}\n"), []byte("echo not-cloud-init")} {
		if err := validateEnvironmentCreateHostIdentityMaterial(malformed, material["host_public_key"]); err == nil {
			t.Fatal("unsupported private bootstrap payload accepted")
		}
	}
}

func TestCreateDataPlaneUpstreamsHonorSelectedMQTTPlacement(t *testing.T) {
	t.Setenv("MQTT_NODE_CLASS", "unrelated-ambient-class")
	for _, selected := range []string{"general", "broker"} {
		t.Run(selected, func(t *testing.T) {
			values := map[string]string{"MQTT_NODE_CLASS": selected, "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}
			raw := []byte(`{"items":[{"metadata":{"name":"broker-node","labels":{"rtk.io/node-class":"broker"}},"status":{"addresses":[{"type":"ExternalIP","address":"198.51.100.2"},{"type":"InternalIP","address":"192.168.1.2"}]}},{"metadata":{"name":"general-node","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.1"}]}}]}`)
			upstreams, err := environmentCreateDataPlaneUpstreams(values, raw)
			if err != nil || len(upstreams) != 3 {
				t.Fatal("selected placement did not retain HTTPS and MQTT routes", err)
			}
			mqtt := upstreams[2]
			if mqtt.Kind != "mqtts" || mqtt.Name != selected+"-node" || mqtt.Port != 31883 || mqtt.Address != map[string]string{"general": "192.168.1.1", "broker": "192.168.1.2"}[selected] {
				t.Fatal("MQTT route differs from selected original node class")
			}
			config := renderLKEEdgeHAProxyConfig(lkeEdgeHAProxyPlan{Upstreams: upstreams})
			if !strings.Contains(config, "server "+selected+"-node "+mqtt.Address+":31883 check") {
				t.Fatal("actual edge target has no selected MQTT backend")
			}
		})
	}
	// This original dev generation has one general node and no broker pool.
	upstreams, err := environmentCreateDataPlaneUpstreams(map[string]string{"MQTT_NODE_CLASS": "general", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}, []byte(`{"items":[{"metadata":{"name":"original-dev-general","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.153.10"}]}}]}`))
	if err != nil || len(upstreams) != 2 || upstreams[1].Kind != "mqtts" {
		t.Fatal("original single-general-node dev cannot serve managed MQTT", err)
	}
}

func TestCreateDataPlaneRejectsEmptyOrUnqualifiedMQTTBackend(t *testing.T) {
	for _, mode := range []string{"no-matching-node", "missing-class", "missing-label-key", "public-upstream", "empty-inventory", "invalid-inventory"} {
		t.Run(mode, func(t *testing.T) {
			values := map[string]string{"MQTT_NODE_CLASS": "broker", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}
			raw := []byte(`{"items":[{"metadata":{"name":"general-node","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.1"}]}}]}`)
			switch mode {
			case "missing-class":
				delete(values, "MQTT_NODE_CLASS")
			case "missing-label-key":
				delete(values, "NODE_CLASS_LABEL_KEY")
			case "public-upstream":
				values["MQTT_NODE_CLASS"] = "general"
				raw = []byte(strings.ReplaceAll(string(raw), "192.168.1.1", "198.51.100.1"))
			case "empty-inventory":
				raw = []byte(`{"items":[]}`)
			case "invalid-inventory":
				raw = []byte(`not-json`)
			}
			if upstreams, err := environmentCreateDataPlaneUpstreams(values, raw); err == nil || len(upstreams) != 0 {
				t.Fatal("empty/unqualified MQTT target reached edge installation")
			}
		})
	}
}

func TestCreateDataPlaneNetworkQualificationUsesReadOnlySelectedNodes(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("isolated-original-kubeconfig"), true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "kubectl")
	arguments := filepath.Join(dir, "arguments")
	writeTestFile(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$RTK_CREATE_NODE_ARGUMENTS\"\nprintf '%s\\n' '{\"items\":[{\"metadata\":{\"name\":\"original-dev-general\",\"labels\":{\"rtk.io/node-class\":\"general\"}},\"status\":{\"addresses\":[{\"type\":\"InternalIP\",\"address\":\"192.168.153.10\"}]}}]}'\n")
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CREATE_NODE_ARGUMENTS", arguments)
	t.Setenv("MQTT_NODE_CLASS", "unrelated-ambient-class")
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "MQTT_NODE_CLASS": "general", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}}
	backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-original-kubeconfig")}
	upstreams, err := environmentCreateDataPlaneNodeUpstreams(context.Background(), cfg, backend)
	if err != nil || len(upstreams) != 2 || upstreams[1].Kind != "mqtts" {
		t.Fatal("network qualification differs from original dev routing", err)
	}
	args := readTestFile(t, arguments)
	if !strings.Contains(args, filepath.Join(store.Root, "deployment", "tmp", "kubernetes-api-")) || !strings.Contains(args, "get\nnodes\n-o\njson\n") || strings.Contains(args, "apply") || strings.Contains(args, "patch") {
		t.Fatal("network gate used another identity or performed a mutation")
	}
	cfg.Values["MQTT_NODE_CLASS"] = "broker"
	if _, err := environmentCreateDataPlaneNodeUpstreams(context.Background(), cfg, backend); err == nil {
		t.Fatal("network gate qualified an empty selected MQTT backend")
	}
}

// This starts at the declared provider-allocated stage. The original receipt
// IDs and selected SSH keys are held by a real execution; the provider, image
// extraction and VM processes are isolated native protocols. No public GO or
// real machine configuration is asserted.
type createDataPlaneContract struct {
	cfg            deploymentConfig
	store          secretStore
	e              *environmentCreateExecution
	plan           environmentCreatePlan
	operator       map[string]string
	receipts       map[string]environmentCreateProviderReceipt
	native         map[string]map[string]any
	state          string
	providerWrites *int
}
type createDataPlaneVMState struct {
	Files    map[string]string `json:"files"`
	Installs int               `json:"installs"`
	Reads    int               `json:"reads"`
}
type createDataPlaneNativeState struct {
	Hosts                                      map[string]createDataPlaneVMState `json:"hosts"`
	Mode                                       string                            `json:"mode"`
	DockerCreates, DockerCopies, DockerRemoves int
}

func newCreateDataPlaneContract(t *testing.T, mode string) *createDataPlaneContract {
	t.Helper()
	cfg, err := resolveDeploymentConfig(writeDeploymentFixture(t, "dev", "lke"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	// All values used by the original role scripts are selected before the
	// parent artifact. The test does not rebind the parent after admission.
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	cfg.Values["MQTT_NODE_CLASS"] = "broker"
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	private, err := ssh.MarshalPrivateKey(key, "isolated-dataplane-operator")
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
	operator := map[string]string{"LINODE_TOKEN": "private-provider-token", "DEPLOYMENT_SSH_PUBLIC_KEY": pub, "GHCR_PULL_USERNAME": "isolated-ci-user", "GHCR_PULL_TOKEN": "isolated-ci-token"}
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	kube, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, e, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: operator, kubeconfig: kube, prepare: func(_ *deploymentConfig, p *environmentCreatePlan) []environmentReleasePublicationProof {
		s, err := newSecretStore("", "dev")
		if err != nil {
			t.Fatal(err)
		}
		if s.write("operator/ssh/id_ed25519", pem.EncodeToMemory(private), false) != nil || s.write("operator/ssh/id_ed25519.pub", []byte(pub+"\n"), false) != nil {
			t.Fatal("canonical original SSH pair")
		}
		p.Provider.Environment, p.Provider.Stack = "dev", p.Stack
		p.Provider.SSHKeySHA256 = environmentPreflightBytesSHA256([]byte(pub))
		p.Provider.HostIdentityPolicy = environmentCreateHostIdentityPolicy
		p.Provider.HostIdentityTemplateSHA256 = environmentPreflightBytesSHA256([]byte(environmentCreateHostIdentityTemplate))
		p.Provider.TurnRegistrarImage = p.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
		p.Provider.TurnRegistrarSHA256 = environmentPreflightBytesSHA256(header)
		// Capture the selected synthetic CI publications before freezing the parent.
		return coreContractPublicationProofs(t, &p.Release)
	}})
	f := &createDataPlaneContract{cfg: cfg, store: store, e: e, plan: plan, operator: operator, receipts: map[string]environmentCreateProviderReceipt{}, native: map[string]map[string]any{}, state: filepath.Join(t.TempDir(), "native.json")}
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "provider" || entry.Key == "provider-kube-access" {
			continue
		}
		var receipt environmentCreateProviderReceipt
		if json.Unmarshal(entry.Receipt, &receipt) != nil {
			t.Fatal("original provider receipt")
		}
		f.receipts[entry.Key] = receipt
	}
	hostKeys := map[string]string{}
	for _, resource := range plan.Provider.Resources {
		if resource.Kind != "instance" {
			continue
		}
		receipt := f.receipts[resource.Key]
		id, err := strconv.Atoi(receipt.NativeID)
		if err != nil {
			t.Fatal(err)
		}
		addresses := []string{"198.51.100.12", "192.168.1.12"}
		if resource.Key != "edge" {
			addresses = []string{"198.51.100.13", "192.168.1.13"}
		}
		f.native["/linode/instances/"+receipt.NativeID] = map[string]any{"id": id, "label": receipt.Name, "region": receipt.Region, "status": "running", "ipv4": addresses}
		if _, err := prepareEnvironmentCreateProviderMaterial(t.Context(), cfg, e.Journal.PlanSHA256, resource, store); err != nil {
			t.Fatal(err)
		}
		originalHostKey, err := store.read(filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "host-identity", resource.Key, "ssh_host_ed25519_key.pub"))
		if err != nil {
			t.Fatal(err)
		}
		hostKeys[addresses[0]] = addresses[0] + " " + originalHostKey + "\n"
	}
	f.providerWrites = createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if row := f.native[r.URL.Path]; row != nil {
			if r.Method != http.MethodGet {
				t.Error("data-plane setup tried provider allocation")
				w.WriteHeader(405)
			} else {
				_ = json.NewEncoder(w).Encode(row)
			}
			return true
		}
		return false
	})
	for name, value := range map[string]string{"turn-shared": "isolated-turn-shared", "turn-registry-node-auth": "isolated-turn-registry"} {
		if err := store.write("runtime/"+name, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	native := createDataPlaneNativeState{Hosts: map[string]createDataPlaneVMState{"198.51.100.12": {Files: map[string]string{}}, "198.51.100.13": {Files: map[string]string{}}}, Mode: mode}
	f.save(t, native)
	directory := t.TempDir()
	hostKeyPath := f.state + ".hostkeys"
	if err := os.WriteFile(hostKeyPath, mustProviderRecoveryJSON(hostKeys), 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(directory, "registrar")
	if err := os.WriteFile(artifact, header, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import base64,hashlib,json,os,re,shlex,sys,traceback
def diagnostic(kind,value,tb):
 with open(os.environ['RTK_DATAPLANE_STATE']+'.failure','w') as dst:dst.write(kind.__name__+':'+','.join(str(frame.lineno) for frame in traceback.extract_tb(tb)))
sys.excepthook=diagnostic
p=os.environ['RTK_DATAPLANE_STATE'];root=os.environ['RTK_DATAPLANE_ROOT'];a=sys.argv[1:]
with open(p) as src:d=json.load(src)
def save():
 with open(p,'w') as dst:json.dump(d,dst)
def put(files,path,raw):files[path]=base64.b64encode(raw).decode()
tool=os.path.basename(sys.argv[0])
if tool=='kubectl':
 while a and a[0].startswith('--'):
  a=a[2:] if a[0]=='--kubeconfig' else a[1:]
 assert a==['get','nodes','-o','json']
 print(json.dumps({'items':[{'metadata':{'name':'original-broker','labels':{'rtk.io/node-class':'broker'}},'status':{'addresses':[{'type':'InternalIP','address':'192.168.1.2'}]}},{'metadata':{'name':'original-general','labels':{'rtk.io/node-class':'general'}},'status':{'addresses':[{'type':'InternalIP','address':'192.168.1.1'}]}}]}));sys.exit()
if tool=='docker':
 assert a[0]=='--config';auth=json.load(open(a[1]+'/config.json'));assert base64.b64decode(auth['auths']['ghcr.io']['auth']).decode()=='isolated-ci-user:isolated-ci-token';a=a[2:]
 if a[0]=='create':
  assert a==['create','--platform','linux/amd64','--entrypoint','/app/turnregistrar',os.environ['RTK_DATAPLANE_IMAGE']]
  d['DockerCreates']+=1;save();print('c'*64);sys.exit()
 if a[0]=='cp':
  assert a[1]=='c'*64+':/app/turnregistrar'
  raw=open(os.environ['RTK_DATAPLANE_ARTIFACT'],'rb').read()
  if d['mode']=='artifact-drift':raw=raw+b'changed immutable artifact'
  with open(a[2],'wb') as dst:dst.write(raw)
  d['DockerCopies']+=1;save();sys.exit()
 assert a==['rm','c'*64];d['DockerRemoves']+=1;save();sys.exit()
assert tool=='ssh' and '-T' in a and a[a.index('-i')+1]==root+'/operator/ssh/id_ed25519'
opts=[a[i+1] for i,v in enumerate(a) if v=='-o'];assert 'StrictHostKeyChecking=yes' in opts and 'GlobalKnownHostsFile=/dev/null' in opts and 'IdentitiesOnly=yes' in opts
ip=a[a.index('--')+1].removeprefix('root@');assert ip in d['hosts'] and a[-2:]==['bash','-s']
known=next(x.split('=',1)[1] for x in opts if x.startswith('UserKnownHostsFile='));assert open(known).read()==json.load(open(os.environ['RTK_DATAPLANE_HOST_KEYS']))[ip]
s=sys.stdin.read();host=d['hosts'][ip];files=host['files']
if '\nsha256sum ' in s:
 paths=s.split('\nsha256sum ',1)[1].strip().split();expected=['/etc/haproxy/haproxy.cfg','/etc/sysctl.d/99-rtk-cloud-edge.conf','/etc/systemd/system/haproxy.service.d/10-rtk-cloud-edge.conf'] if ip.endswith('.12') else ['/etc/turnserver.conf','/etc/video-cloud-coturn-cli.env','/etc/video-cloud-turnregistrar.env','/etc/systemd/system/video-cloud-turnregistrar.service','/opt/video_cloud/bin/turnregistrar']
 assert paths==expected and host['installs']==1 and set(files)==set(paths)
 host['reads']+=1;save()
 for path in paths:print(hashlib.sha256(base64.b64decode(files[path])).hexdigest()+'  '+path)
 sys.exit()
assert host['installs']==0
if ip.endswith('.12'):
 for path,tag,body in re.findall(r"cat >(/[^ ]+) <<'([^']+)'\n(.*?)\n\2\n",s,re.S):put(files,path,(body+'\n').encode())
 cfg=base64.b64decode(files['/etc/haproxy/haproxy.cfg']).decode()
 assert '192.168.1.2:31883' in cfg and '192.168.1.1:30443' in cfg and '192.168.1.2:30443' in cfg
 assert 'systemctl is-active haproxy' in s and "[:.]8883" in s
else:
 exports={}
 for line in s.splitlines():
  if line.startswith('export '):
   kv=shlex.split(line)[1].split('=',1);exports[kv[0]]=kv[1]
 assert exports['VIDEO_CLOUD_COTURN_SHARED_SECRET']==open(root+'/runtime/turn-shared').read()
 assert exports['VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY']==open(root+'/runtime/turn-registry-node-auth').read()
 assert exports['COTURN_CLI_PASSWORD']==open(root+'/deployment/turn/turn01/cli-password').read().strip()
 binary=base64.b64decode(s.split("<<'RTK_REGISTRAR_BINARY'\n",1)[1].split('\nRTK_REGISTRAR_BINARY',1)[0]);assert binary==open(os.environ['RTK_DATAPLANE_ARTIFACT'],'rb').read()
 tail=s.split("cat <<'COTURN_TAIL'\n",1)[1].split('\nCOTURN_TAIL',1)[0]+'\n'
 put(files,'/etc/turnserver.conf',('use-auth-secret\nstatic-auth-secret='+exports['VIDEO_CLOUD_COTURN_SHARED_SECRET']+'\n'+tail+'cli-password='+exports['COTURN_CLI_PASSWORD']+'\nlog-file=stdout\n').encode())
 put(files,'/etc/video-cloud-coturn-cli.env',('COTURN_CLI_PASSWORD='+exports['COTURN_CLI_PASSWORD']+'\n').encode())
 block=s.split('} >/etc/video-cloud-turnregistrar.env',1)[0].rsplit('{\n',1)[1]
 env=''
 for line in block.splitlines():
  if not line.strip():continue
  cmd,fmt,arg=shlex.split(line);assert cmd=='printf'
  if arg.startswith('$'):arg=exports[arg[1:]]
  env+=fmt.replace('\\n','\n')%arg
 assert 'VIDEO_CLOUD_ENV=dev\n' in env and 'VIDEO_CLOUD_TURN_NODE_ID=turn01\n' in env and 'VIDEO_CLOUD_TURN_NODE_REGION=us-sea\n' in env
 put(files,'/etc/video-cloud-turnregistrar.env',env.encode())
 unit=s.split("<<'TURNREGISTRAR_UNIT'\n",1)[1].split('\nTURNREGISTRAR_UNIT',1)[0]+'\n';put(files,'/etc/systemd/system/video-cloud-turnregistrar.service',unit.encode());put(files,'/opt/video_cloud/bin/turnregistrar',binary)
 assert s.endswith('systemctl enable video-cloud-turnregistrar\n') and 'systemctl restart video-cloud-turnregistrar' not in s
host['installs']+=1;save()
if d['mode']=='response-lost' and ip.endswith('.13'):sys.exit(1)
`
	for _, name := range []string{"ssh", "docker", "kubectl"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_DATAPLANE_HOST_KEYS", hostKeyPath)
	t.Setenv("RTK_DATAPLANE_STATE", f.state)
	t.Setenv("RTK_DATAPLANE_ROOT", store.Root)
	t.Setenv("RTK_DATAPLANE_ARTIFACT", artifact)
	t.Setenv("RTK_DATAPLANE_IMAGE", plan.Provider.TurnRegistrarImage)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(directory, "kubectl"))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}
func (f *createDataPlaneContract) read(t *testing.T) createDataPlaneNativeState {
	t.Helper()
	raw, err := os.ReadFile(f.state)
	if err != nil {
		t.Fatal(err)
	}
	var n createDataPlaneNativeState
	if json.Unmarshal(raw, &n) != nil {
		t.Fatal("native state")
	}
	return n
}
func (f *createDataPlaneContract) save(t *testing.T, n createDataPlaneNativeState) {
	t.Helper()
	if err := os.WriteFile(f.state, mustProviderRecoveryJSON(n), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *createDataPlaneContract) run(t *testing.T) error {
	t.Helper()
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256})
	return configureEnvironmentCreateExternalDataPlanes(ctx, f.cfg, &f.plan, f.receipts, f.store, f.operator, f.e)
}

func TestCreateDataPlaneCoordinatorInstallsExactTargetsAndRevalidatesWithoutReplay(t *testing.T) {
	f := newCreateDataPlaneContract(t, "normal")
	if err := f.run(t); err != nil {
		raw, _ := os.ReadFile(f.state + ".failure")
		t.Fatal("actual edge and TURN configuration", err, string(raw))
	}
	before := f.read(t)
	receipts := map[string]string{}
	for _, key := range []string{"configure-edge", "configure-turn-01"} {
		entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
		if entry == nil || entry.State != "completed" {
			t.Fatal("missing original configuration receipt", key)
		}
		receipts[key] = string(entry.Receipt)
	}
	if before.Hosts["198.51.100.12"].Installs != 1 || len(before.Hosts["198.51.100.12"].Files) != 3 || before.Hosts["198.51.100.13"].Installs != 1 || len(before.Hosts["198.51.100.13"].Files) != 5 || before.DockerCreates != 1 || before.DockerCopies != 1 || before.DockerRemoves != 1 || *f.providerWrites != 0 {
		t.Fatal("actual selected configuration protocol did not run exactly once")
	}
	password, err := f.store.read("deployment/turn/turn01/cli-password")
	if err != nil || !validCoturnCLIPassword(password) {
		t.Fatal("retained original relay credential", err)
	}
	if err := f.run(t); err != nil {
		t.Fatal("completed configure retry", err)
	}
	after := f.read(t)
	for ip, host := range before.Hosts {
		actual := after.Hosts[ip]
		if actual.Installs != host.Installs || actual.Reads != host.Reads+1 || deploymentplan.Digest(actual.Files) != deploymentplan.Digest(host.Files) {
			t.Fatal("completed retry did not read only original installed files", ip)
		}
	}
	for key, raw := range receipts {
		if string(createOpenBaoACKCompletionEntry(f.e.Journal, key).Receipt) != raw {
			t.Fatal("completed native read changed receipt")
		}
	}
	if current, _ := f.store.read("deployment/turn/turn01/cli-password"); current != password {
		t.Fatal("retry changed CLI identity")
	}
	for _, mode := range []string{"native-config", "provider-owner", "intent", "native-host-key"} {
		t.Run(mode, func(t *testing.T) {
			original := f.read(t)
			key := "/linode/instances/102"
			row := f.native[key]
			oldRow := map[string]any{}
			for k, v := range row {
				oldRow[k] = v
			}
			oldValue := f.plan.Release.WorkspaceRevision
			originalHostKeys, err := os.ReadFile(f.state + ".hostkeys")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "native-config":
				n := f.read(t)
				host := n.Hosts["198.51.100.12"]
				host.Files["/etc/haproxy/haproxy.cfg"] = base64.StdEncoding.EncodeToString([]byte("foreign config"))
				n.Hosts["198.51.100.12"] = host
				f.save(t, n)
			case "provider-owner":
				row["id"] = 999
			case "intent":
				f.plan.Release.WorkspaceRevision = strings.Repeat("9", 40)
			case "native-host-key":
				// The same IP now presents a different remote identity. The local
				// original material stays intact and strict SSH must refuse it.
				seed := make([]byte, ed25519.SeedSize)
				seed[0] = 1
				public, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed).Public())
				if err != nil {
					t.Fatal(err)
				}
				var remote map[string]string
				if json.Unmarshal(originalHostKeys, &remote) != nil {
					t.Fatal("original remote host key state")
				}
				remote["198.51.100.12"] = "198.51.100.12 " + string(ssh.MarshalAuthorizedKey(public))
				if err := os.WriteFile(f.state+".hostkeys", mustProviderRecoveryJSON(remote), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				current := f.read(t)
				for ip, host := range original.Hosts {
					now := current.Hosts[ip]
					now.Files = host.Files
					current.Hosts[ip] = now
				}
				f.save(t, current)
				f.native[key] = oldRow
				f.plan.Release.WorkspaceRevision = oldValue
				if err := os.WriteFile(f.state+".hostkeys", originalHostKeys, 0600); err != nil {
					t.Fatal("restore isolated original remote host identity", err)
				}
			})
			err = f.run(t)
			cause := map[string]string{"native-config": "recorded data-plane configuration changed", "provider-owner": "native owner cannot be verified", "intent": "uncertain request or changed original outcome", "native-host-key": "pinned VM SSH configuration failed"}[mode]
			if err == nil || !strings.Contains(err.Error(), cause) {
				t.Fatal("changed original data-plane accepted", mode, err)
			}
			current := f.read(t)
			for ip, host := range original.Hosts {
				if current.Hosts[ip].Installs != host.Installs {
					t.Fatal("refusal reinstalled native host")
				}
			}
		})
	}
	if err := f.e.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestCreateDataPlaneCoordinatorPreservesUnknownWritesAndRejectsChangedArtifact(t *testing.T) {
	for _, mode := range []string{"response-lost", "artifact-drift", "missing-runtime"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateDataPlaneContract(t, mode)
			if mode == "missing-runtime" {
				if err := os.Remove(filepath.Join(f.store.Root, "runtime/turn-shared")); err != nil {
					t.Fatal(err)
				}
			}
			err := f.run(t)
			cause := map[string]string{"response-lost": "pinned VM SSH configuration failed", "artifact-drift": "differs from the exact create qualification", "missing-runtime": "TURN shared runtime credential is unavailable"}[mode]
			if err == nil || !strings.Contains(err.Error(), cause) {
				t.Fatal("native interruption not enforced", mode, err)
			}
			before := f.read(t)
			expected := 0
			if mode == "response-lost" {
				expected = 1
			}
			if before.Hosts["198.51.100.12"].Installs != 1 || before.Hosts["198.51.100.13"].Installs != expected {
				t.Fatal("failure crossed native install boundary")
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "configure-turn-01")
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
				t.Fatal("unknown result gained receipt")
			}
			if err := f.run(t); err == nil || !strings.Contains(err.Error(), "data-plane outcome is not recorded") {
				t.Fatal("pending native mutation replayed", err)
			}
			after := f.read(t)
			if after.Hosts["198.51.100.13"].Installs != expected || after.DockerCopies != before.DockerCopies || *f.providerWrites != 0 {
				t.Fatal("retry reminted or replayed an uncertain host")
			}
		})
	}
}
