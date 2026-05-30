package bulkbindvalidation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateArtifactAcceptsOneHundredDevicesAcrossTenUsers(t *testing.T) {
	artifact := Artifact{
		Schema:       ArtifactSchema,
		BrandName:    "RTK",
		BrandCloudID: "org-rtk",
		Count:        100,
	}
	for i := 0; i < 100; i++ {
		user := (i % 10) + 1
		serviceOptions := []string{"mqtt"}
		category := "mqtt_device"
		if i < 40 {
			serviceOptions = []string{"mqtt", "video_streaming", "video_storage"}
			category = "ip_camera"
		}
		artifact.Assignments = append(artifact.Assignments, Assignment{
			AssignmentIndex: i,
			AssignedEmail:   padUser(user),
			DeviceID:        padDevice(i + 1),
			DeviceType:      category,
			Category:        category,
			ServiceOptions:  serviceOptions,
			ClaimID:         "claim-" + padDevice(i+1),
			AccountDeviceID: "account-" + padDevice(i+1),
			OperationID:     "op-" + padDevice(i+1),
			Status:          "provision_requested",
		})
	}

	result := Validate(artifact, Config{ExpectedCount: 100, ExpectedDevicesPerUser: 10})

	if result.Overall != StatusPass {
		t.Fatalf("expected pass, got %s: %+v", result.Overall, result.Checks)
	}
	if result.Summary.TotalDevices != 100 {
		t.Fatalf("unexpected total devices: %d", result.Summary.TotalDevices)
	}
	if len(result.UserCounts) != 10 {
		t.Fatalf("expected 10 users, got %d", len(result.UserCounts))
	}
	for _, user := range result.UserCounts {
		if user.DeviceCount != 10 {
			t.Fatalf("expected user %s to have 10 devices, got %d", user.Email, user.DeviceCount)
		}
	}
}

func TestValidateArtifactRejectsMissingProvisionOperation(t *testing.T) {
	artifact := minimalArtifact()
	artifact.Assignments[0].OperationID = ""

	result := Validate(artifact, Config{ExpectedCount: 1, ExpectedDevicesPerUser: 1})

	if result.Overall != StatusFail {
		t.Fatalf("expected fail")
	}
	if !hasFailedCheck(result, "operation_ids_present") {
		t.Fatalf("expected operation_ids_present failure: %+v", result.Checks)
	}
}

func TestValidateArtifactRejectsUnevenUserDistribution(t *testing.T) {
	artifact := minimalArtifact()
	artifact.Assignments = append(artifact.Assignments, Assignment{
		AssignmentIndex: 1,
		AssignedEmail:   "rtk+001@users.local",
		DeviceID:        "load-device-0002",
		DeviceType:      "light",
		Category:        "mqtt_device",
		ServiceOptions:  []string{"mqtt"},
		ClaimID:         "claim-load-device-0002",
		AccountDeviceID: "account-load-device-0002",
		OperationID:     "op-load-device-0002",
		Status:          "provision_requested",
	})
	artifact.Count = 2

	result := Validate(artifact, Config{ExpectedCount: 2, ExpectedDevicesPerUser: 1})

	if result.Overall != StatusFail {
		t.Fatalf("expected fail")
	}
	if !hasFailedCheck(result, "devices_per_user") {
		t.Fatalf("expected devices_per_user failure: %+v", result.Checks)
	}
}

func TestValidateArtifactRejectsMQTTOnlyVideoClaims(t *testing.T) {
	artifact := minimalArtifact()
	artifact.Assignments[0].Category = "mqtt_device"
	artifact.Assignments[0].ServiceOptions = []string{"mqtt", "video_streaming"}

	result := Validate(artifact, Config{ExpectedCount: 1, ExpectedDevicesPerUser: 1})

	if result.Overall != StatusFail {
		t.Fatalf("expected fail")
	}
	if !hasFailedCheck(result, "mqtt_only_service_options") {
		t.Fatalf("expected mqtt_only_service_options failure: %+v", result.Checks)
	}
}

func TestReportsDoNotRenderSecrets(t *testing.T) {
	artifact := minimalArtifact()
	result := Validate(artifact, Config{ExpectedCount: 1, ExpectedDevicesPerUser: 1})

	js, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	markdown := RenderMarkdown(result)
	combined := string(js) + markdown
	for _, secretWord := range []string{"password", "bearer", "raw-token", "device.key"} {
		if strings.Contains(strings.ToLower(combined), secretWord) {
			t.Fatalf("report leaked secret marker %q: %s", secretWord, combined)
		}
	}
}

func minimalArtifact() Artifact {
	return Artifact{
		Schema:       ArtifactSchema,
		BrandName:    "RTK",
		BrandCloudID: "org-rtk",
		Count:        1,
		Assignments: []Assignment{{
			AssignmentIndex: 0,
			AssignedEmail:   "rtk+001@users.local",
			DeviceID:        "load-device-0001",
			DeviceType:      "light",
			Category:        "mqtt_device",
			ServiceOptions:  []string{"mqtt"},
			ClaimID:         "claim-load-device-0001",
			AccountDeviceID: "account-load-device-0001",
			OperationID:     "op-load-device-0001",
			Status:          "provision_requested",
		}},
	}
}

func hasFailedCheck(result Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name && check.Status == StatusFail {
			return true
		}
	}
	return false
}

func padUser(i int) string {
	if i < 10 {
		return "rtk+00" + string(rune('0'+i)) + "@users.local"
	}
	return "rtk+010@users.local"
}

func padDevice(i int) string {
	b, _ := json.Marshal(i)
	s := string(b)
	for len(s) < 4 {
		s = "0" + s
	}
	return "load-device-" + s
}
