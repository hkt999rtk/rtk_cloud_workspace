package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test-only draft, not installed. Caller supplies fixture-private dir and
// exact original index / amd64 manifest files; no registry or Docker socket.
func installIsolatedOpenBaoOCIBridge(t *testing.T, bin, privateDir string) {
	t.Helper()
	// Supply unmodified registry responses as <sha256>.json in this directory.
	inputDir := strings.TrimSpace(os.Getenv("RTK_TEST_OPENBAO_OCI_INPUT_DIR"))
	if inputDir == "" {
		t.Skip("set RTK_TEST_OPENBAO_OCI_INPUT_DIR to the directory containing digest-named public OpenBao OCI JSON fixtures")
	}
	digests := []string{
		"436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115",
		"d0424c95859f7b4c1e308abf57c4cd72b9cba835bb946eb397172b799fba9477",
	}
	if !isolatedScriptTempPath(bin) || !isolatedScriptTempPath(privateDir) {
		t.Fatal("private OCI bridge required")
	}
	for _, digest := range digests {
		data, err := os.ReadFile(filepath.Join(inputDir, digest+".json"))
		if err != nil || len(data) == 0 || len(data) > 4<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
			t.Fatal("exact public OCI source digest unavailable")
		}
		if err := os.WriteFile(filepath.Join(privateDir, digest), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := os.ReadFile(filepath.Join(privateDir, "436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(index, &manifest) != nil {
		t.Fatal("invalid official OCI index")
	}
	count := 0
	for _, item := range manifest.Manifests {
		if item.Platform.OS == "linux" && item.Platform.Architecture == "amd64" && item.Digest == "sha256:d0424c95859f7b4c1e308abf57c4cd72b9cba835bb946eb397172b799fba9477" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("official OCI amd64 child mismatch")
	}
	child, err := os.ReadFile(filepath.Join(privateDir, "d0424c95859f7b4c1e308abf57c4cd72b9cba835bb946eb397172b799fba9477"))
	if err != nil {
		t.Fatal(err)
	}
	var leaf struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(child, &leaf) != nil || leaf.Config.Digest != "sha256:5cdbfe3c85e965315cd3220d2836b8d54b55f95715c551a46e233e0e6e6c9b03" {
		t.Fatal("official OCI config mismatch")
	}
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("real Docker client unavailable")
	}
	pluginInfo, err := exec.Command(dockerPath, "info", "--format", "{{json .ClientInfo.Plugins}}").Output()
	if err != nil || len(pluginInfo) > 1<<20 || !json.Valid(pluginInfo) {
		t.Fatal("real Buildx plugin information unavailable")
	}
	pluginDigest := fmt.Sprintf("%x", sha256.Sum256(pluginInfo))
	if err := os.WriteFile(filepath.Join(privateDir, "plugin-info"), pluginInfo, 0600); err != nil {
		t.Fatal(err)
	}
	original, fallback := filepath.Join(bin, "docker"), filepath.Join(bin, "docker-before-oci")
	if err := os.Rename(original, fallback); err != nil {
		t.Fatal(err)
	}
	shim := `#!/usr/bin/python3
import hashlib,os,sys
args=sys.argv[1:]
if args == ['info','--format','{{json .ClientInfo.Plugins}}']:
 with open(os.path.join(os.environ['RTK_TEST_OPENBAO_OCI_DIR'],'plugin-info'),'rb') as f:raw=f.read(1048577)
 if len(raw)>1048576 or hashlib.sha256(raw).hexdigest()!='__PLUGIN_DIGEST__':sys.exit(73)
 sys.stdout.buffer.write(raw);sys.exit(0)
if len(args)==7 and args[0]=='--config' and args[2:6]==['buildx','imagetools','inspect','--raw']:
 root=os.path.realpath(os.environ['RTK_CLOUD_CONFIG_ROOT'])
 config=os.path.realpath(args[1])
 if os.path.commonpath([root,config])!=root or not os.path.isfile(os.path.join(config,'config.json')):sys.exit(71)
 base='quay.io/openbao/openbao:2.5.4@sha256:'
 allowed={'436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115','d0424c95859f7b4c1e308abf57c4cd72b9cba835bb946eb397172b799fba9477'}
 if not args[6].startswith(base) or args[6][len(base):] not in allowed:sys.exit(72)
 digest=args[6][len(base):]
 with open(os.path.join(os.environ['RTK_TEST_OPENBAO_OCI_DIR'],digest),'rb') as f:raw=f.read(4194305)
 if len(raw)>4194304 or hashlib.sha256(raw).hexdigest()!=digest:sys.exit(73)
 sys.stdout.buffer.write(raw);sys.exit(0)
os.execv(os.path.join(os.path.dirname(__file__),'docker-before-oci'),['docker']+args)
`
	shim = strings.ReplaceAll(shim, "__PLUGIN_DIGEST__", pluginDigest)
	if err := os.WriteFile(original, []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedOpenBaoOCIBridgeRejectsForeignOrTamperedManifest(t *testing.T) {
	if os.Getenv("RTK_TEST_OPENBAO_FIRST") != "1" {
		t.Skip("explicit isolated real OCI evidence required")
	}
	bin, private, config := t.TempDir(), t.TempDir(), t.TempDir()
	for _, p := range []string{bin, private, config} {
		if err := os.Chmod(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(config, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 71\n"), 0700); err != nil {
		t.Fatal(err)
	}
	installIsolatedOpenBaoOCIBridge(t, bin, private)
	index := "quay.io/openbao/openbao:2.5.4@sha256:436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115"
	run := func(ref string) ([]byte, error) {
		c := exec.Command(filepath.Join(bin, "docker"), "--config", config, "buildx", "imagetools", "inspect", "--raw", ref)
		c.Env = []string{"RTK_CLOUD_CONFIG_ROOT=" + config, "RTK_TEST_OPENBAO_OCI_DIR=" + private}
		return c.Output()
	}
	if _, err := run(index); err != nil {
		t.Fatal("verified official index rejected", err)
	}
	if _, err := run("quay.io/openbao/openbao:2.5.4@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("foreign image accepted")
	}
	if _, err := run("quay.io/openbao/openbao:foreign@sha256:436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115"); err == nil {
		t.Fatal("foreign tag accepted")
	}
	path := filepath.Join(private, "436eaf9778cad75507ff70ea26ace30dcbe15606e619ac3823495663d7f7c115")
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(index); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
