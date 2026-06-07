package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDeviceBindAllowsMissingClaimIDWhenProvisionIdentifiersExist(t *testing.T) {
	root := t.TempDir()
	bindPath := filepath.Join(root, "bind.json")
	outDir := filepath.Join(root, "out")
	data := `{
  "brandname":"RTK",
  "brand_cloud_id":"brand-1",
  "assignments":[
    {
      "assignment_index":0,
      "assigned_email":"rtk+001@users.local",
      "device_id":"load-device-0001",
      "device_type":"camera",
      "category":"ip_camera",
      "service_options":["mqtt","video_streaming","video_storage"],
      "claim_id":"",
      "account_device_id":"account-device-1",
      "operation_id":"operation-1",
      "status":"provision_requested"
    },
    {
      "assignment_index":1,
      "assigned_email":"rtk+002@users.local",
      "device_id":"load-device-0002",
      "device_type":"light",
      "category":"mqtt_device",
      "service_options":["mqtt"],
      "claim_id":"",
      "account_device_id":"account-device-2",
      "operation_id":"operation-2",
      "status":"provision_requested"
    }
  ]
}`
	if err := os.WriteFile(bindPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runValidateDeviceBind([]string{
		"--bind-artifact", bindPath,
		"--out-dir", outDir,
		"--expected-count", "2",
		"--expected-devices-per-user", "1",
	})
	if err != nil {
		t.Fatalf("runValidateDeviceBind() error = %v", err)
	}
}
