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

// Only public, digest-verified registry bytes are served. Runtime acceptance
// still checks the actual Docker container image ID and native owner.
func installIsolatedFoundationOCIBridge(t *testing.T, bin, privateDir string) {
	t.Helper()
	// Supply unmodified registry responses as <sha256>.json in this directory.
	inputDir := strings.TrimSpace(os.Getenv("RTK_TEST_FOUNDATION_OCI_INPUT_DIR"))
	if inputDir == "" {
		t.Skip("set RTK_TEST_FOUNDATION_OCI_INPUT_DIR to the directory containing digest-named public foundation OCI JSON fixtures")
	}
	if !isolatedScriptTempPath(bin) || !isolatedScriptTempPath(privateDir) {
		t.Fatal("isolated OCI bridge paths required")
	}
	type image struct{ ref, index, child, config string }
	images := []image{
		{"docker.io/library/postgres:16-alpine", "cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685", "075f7ba66bc9b3ce7d6b8b635208ff61cd7cf1a67d71ec530eec5d7ae0cbe571", "75f5a96988cdf694a215073c3e9c001b706b371e2f94df3967f2efdec2787f6b"},
		{"docker.io/valkey/valkey:8-alpine", "081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507", "32627109abf6f741121096b45c732f758876803efd7b2e1018ebc0350d117119", "09f1cebdbddf5d5c6e7521bd66e488a55bc11c47736572c101d9d0cf628121d0"},
		{"ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", "12067783a9e4337f1da4ef31e7576bc71298fca8a63bb841b7f1d8bbdb669e55", "ee4e72cc978df2013c9ae783a441bbc2d9cb4ef3b0444a74e6ec9ccad80ff22a", "823a0857eda7cd4f319fcdc7b322674e9748cd158604d25f3f8a8a8edccb4361"},
		{"ghcr.io/hkt999rtk/rtk_account_manager/account-manager", "1d118e86ec9bbc35c213f0e162b3a974a6ab49adc535491727d12c7e1b8e58a0", "ca46a85f8f98be99bc8ca2e532389f769318ecc4c11e2b022c9a6e57d0bb1f7f", "10441aae65ee84b882e5f3c7f199bc1ee00eeac8d8916ceaf59f7ccb0588e80d"},
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("real Docker client unavailable")
	}
	for _, item := range images {
		for _, digest := range []string{item.index, item.child} {
			raw, err := os.ReadFile(filepath.Join(inputDir, digest+".json"))
			if err != nil || len(raw) == 0 || len(raw) > 4<<20 || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
				t.Fatal("exact public OCI source digest unavailable")
			}
			if err := os.WriteFile(filepath.Join(privateDir, digest), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		index, err := os.ReadFile(filepath.Join(privateDir, item.index))
		var list struct {
			Manifests []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
				} `json:"platform"`
			} `json:"manifests"`
		}
		if err != nil || json.Unmarshal(index, &list) != nil {
			t.Fatal("official OCI index unavailable")
		}
		count := 0
		for _, m := range list.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" && m.Digest == "sha256:"+item.child {
				count++
			}
		}
		if count != 1 {
			t.Fatal("official OCI amd64 child differs")
		}
		child, err := os.ReadFile(filepath.Join(privateDir, item.child))
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if err != nil || json.Unmarshal(child, &manifest) != nil || manifest.Config.Digest != "sha256:"+item.config {
			t.Fatal("official OCI child configuration differs")
		}
		childRef := strings.Split(item.ref, ":")[0] + "@sha256:" + item.child
		actual, err := exec.Command(docker, "image", "inspect", "--format", "{{.Id}} {{.Os}}/{{.Architecture}}", childRef).Output()
		if err != nil || strings.TrimSpace(string(actual)) != "sha256:"+item.config+" linux/amd64" {
			t.Fatal("actual isolated amd64 image does not match its official OCI configuration")
		}
	}
	original, fallback := filepath.Join(bin, "docker"), filepath.Join(bin, "docker-before-foundation-oci")
	if err := os.Rename(original, fallback); err != nil {
		t.Fatal(err)
	}
	shim := `#!/usr/bin/python3
import hashlib,os,sys
args=sys.argv[1:]
if len(args)==7 and args[0]=='--config' and args[2:6]==['buildx','imagetools','inspect','--raw']:
 root=os.path.realpath(os.environ['RTK_CLOUD_CONFIG_ROOT']); config=os.path.realpath(args[1])
 if os.path.commonpath([root,config])!=root or not os.path.isfile(os.path.join(config,'config.json')):sys.exit(71)
 refs={
 'docker.io/library/postgres:16-alpine@sha256:':{'cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685','075f7ba66bc9b3ce7d6b8b635208ff61cd7cf1a67d71ec530eec5d7ae0cbe571'},
 'docker.io/valkey/valkey:8-alpine@sha256:':{'081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507','32627109abf6f741121096b45c732f758876803efd7b2e1018ebc0350d117119'},
 'ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:':{'12067783a9e4337f1da4ef31e7576bc71298fca8a63bb841b7f1d8bbdb669e55','ee4e72cc978df2013c9ae783a441bbc2d9cb4ef3b0444a74e6ec9ccad80ff22a'},
 'ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:':{'1d118e86ec9bbc35c213f0e162b3a974a6ab49adc535491727d12c7e1b8e58a0','ca46a85f8f98be99bc8ca2e532389f769318ecc4c11e2b022c9a6e57d0bb1f7f'}}
 for base,allowed in refs.items():
  if args[6].startswith(base):
   digest=args[6][len(base):]
   if digest not in allowed:sys.exit(72)
   with open(os.path.join(os.environ['RTK_TEST_OPENBAO_OCI_DIR'],digest),'rb') as f:raw=f.read(4194305)
   if len(raw)>4194304 or hashlib.sha256(raw).hexdigest()!=digest:sys.exit(73)
   sys.stdout.buffer.write(raw);sys.exit(0)
os.execv(os.path.join(os.path.dirname(__file__),'docker-before-foundation-oci'),['docker']+args)
`
	if err := os.WriteFile(original, []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedFoundationOCIBridgeRejectsForeignAndTampered(t *testing.T) {
	if os.Getenv("RTK_TEST_OPENBAO_FIRST") != "1" {
		t.Skip("explicit isolated OCI evidence required")
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
	installIsolatedFoundationOCIBridge(t, bin, private)
	run := func(ref string) error {
		c := exec.Command(filepath.Join(bin, "docker"), "--config", config, "buildx", "imagetools", "inspect", "--raw", ref)
		c.Env = []string{"RTK_CLOUD_CONFIG_ROOT=" + config, "RTK_TEST_OPENBAO_OCI_DIR=" + private}
		return c.Run()
	}
	pg := "docker.io/library/postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685"
	valkey := "docker.io/valkey/valkey:8-alpine@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
	for _, ref := range []string{pg, valkey} {
		if err := run(ref); err != nil {
			t.Fatal("verified official OCI index rejected", err)
		}
	}
	if run(strings.Replace(pg, "16-alpine", "foreign", 1)) == nil || run(strings.Replace(valkey, "081c2f5c", "ffffffff", 1)) == nil {
		t.Fatal("foreign image accepted")
	}
	if err := os.WriteFile(filepath.Join(private, "cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if run(pg) == nil {
		t.Fatal("tampered manifest accepted")
	}
}
