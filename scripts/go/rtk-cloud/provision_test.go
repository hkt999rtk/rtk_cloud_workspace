package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteProvisionArtifactsIncludesCloudAdminPrivateIP(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
		ArtifactsDir:        filepath.Join(root, "artifacts"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{
  "stack":"video-cloud-test",
  "vpc_id":1,
  "subnet_id":2,
  "firewalls":{"edge":10,"api":11,"infra":12,"mqtt":13,"coturn":14},
  "instances":{
    "edge":{"id":101,"role":"edge","label":"vc-edge","public_ipv4":"203.0.113.5","private_ip":"10.42.1.5"},
    "api":{"id":102,"role":"api","label":"vc-api","public_ipv4":"203.0.113.6","private_ip":"10.42.1.10"},
    "infra":{"id":103,"role":"infra","label":"vc-infra","public_ipv4":"203.0.113.7","private_ip":"10.42.1.30"},
    "mqtt":{"id":104,"role":"mqtt","label":"vc-mqtt","public_ipv4":"203.0.113.8","private_ip":"10.42.1.40"},
    "coturn":{"id":105,"role":"coturn","label":"vc-coturn","public_ipv4":"203.0.113.9","private_ip":""}
  }
}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID=201\nACCOUNT_MANAGER_LINODE_LABEL=am\nACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\nACCOUNT_MANAGER_LINODE_PRIVATE_IPV4=10.42.1.50\nACCOUNT_MANAGER_LINODE_FIREWALL_ID=301\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_ID=202\nADMIN_LINODE_LABEL=admin\nADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\nADMIN_LINODE_PRIVATE_IPV4=10.42.1.60\nADMIN_LINODE_FIREWALL_ID=302\n")

	dir, err := writeProvisionArtifacts(paths, "video-cloud-test")
	if err != nil {
		t.Fatalf("writeProvisionArtifacts returned error: %v", err)
	}

	var inventory struct {
		CloudAdmin struct {
			PrivateIP string `json:"private_ip"`
		} `json:"cloud_admin"`
	}
	readJSON(t, filepath.Join(dir, "inventory.json"), &inventory)
	if inventory.CloudAdmin.PrivateIP != "10.42.1.60" {
		t.Fatalf("cloud_admin.private_ip = %q", inventory.CloudAdmin.PrivateIP)
	}

	var targets struct {
		Targets map[string]struct {
			PrivateIP string `json:"private_ip"`
		} `json:"targets"`
	}
	readJSON(t, filepath.Join(dir, "deployment-targets.json"), &targets)
	if targets.Targets["cloud_admin"].PrivateIP != "10.42.1.60" {
		t.Fatalf("targets.cloud_admin.private_ip = %q", targets.Targets["cloud_admin"].PrivateIP)
	}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
