package bulkbindvalidation

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func Validate(artifact Artifact, cfg Config) Result {
	start := time.Now().UTC()
	if cfg.ExpectedCount == 0 {
		cfg.ExpectedCount = artifact.Count
	}
	result := Result{
		Schema:    ResultSchema,
		StartedAt: start,
		Config:    cfg,
		Summary: Summary{
			BrandName:    artifact.BrandName,
			BrandCloudID: artifact.BrandCloudID,
			TotalDevices: len(artifact.Assignments),
		},
	}

	result.addCheck("artifact_schema", artifact.Schema == ArtifactSchema, fmt.Sprintf("schema=%s", artifact.Schema))
	result.addCheck("expected_count", len(artifact.Assignments) == cfg.ExpectedCount && artifact.Count == cfg.ExpectedCount, fmt.Sprintf("artifact_count=%d assignments=%d expected=%d", artifact.Count, len(artifact.Assignments), cfg.ExpectedCount))

	missingAccountDevice := 0
	missingOperation := 0
	missingClaim := 0
	missingEmail := 0
	missingDevice := 0
	mqttVideoClaims := 0
	invalidServiceOptions := 0
	userCounts := map[string]int{}

	for _, assignment := range artifact.Assignments {
		if assignment.AssignedEmail == "" {
			missingEmail++
		} else {
			userCounts[assignment.AssignedEmail]++
		}
		if assignment.DeviceID == "" {
			missingDevice++
		}
		if assignment.AccountDeviceID == "" {
			missingAccountDevice++
		}
		if assignment.OperationID == "" {
			missingOperation++
		}
		if assignment.ClaimID == "" {
			missingClaim++
		}
		if containsOnlyMQTT(assignment.ServiceOptions) {
			result.Summary.MQTTOnlyDevices++
		}
		if hasAnyVideoService(assignment.ServiceOptions) {
			result.Summary.VideoDevices++
		}
		if assignment.Category == "mqtt_device" && hasAnyVideoService(assignment.ServiceOptions) {
			mqttVideoClaims++
		}
		for _, service := range assignment.ServiceOptions {
			if !validServiceOption(service) {
				invalidServiceOptions++
			}
		}
		if assignment.Status != "" && assignment.OperationID != "" {
			result.Summary.ProvisionRequested++
		}
	}

	result.addCheck("assigned_users_present", missingEmail == 0, fmt.Sprintf("missing_assigned_email=%d", missingEmail))
	result.addCheck("device_ids_present", missingDevice == 0, fmt.Sprintf("missing_device_id=%d", missingDevice))
	result.addCheck("claim_ids_present", missingClaim == 0, fmt.Sprintf("missing_claim_id=%d", missingClaim))
	result.addCheck("account_device_ids_present", missingAccountDevice == 0, fmt.Sprintf("missing_account_device_id=%d", missingAccountDevice))
	result.addCheck("operation_ids_present", missingOperation == 0, fmt.Sprintf("missing_operation_id=%d", missingOperation))
	result.addCheck("service_options_valid", invalidServiceOptions == 0, fmt.Sprintf("invalid_service_options=%d", invalidServiceOptions))
	result.addCheck("mqtt_only_service_options", mqttVideoClaims == 0, fmt.Sprintf("mqtt_device_with_video_services=%d", mqttVideoClaims))

	emails := make([]string, 0, len(userCounts))
	for email := range userCounts {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	for _, email := range emails {
		result.UserCounts = append(result.UserCounts, UserCount{Email: email, DeviceCount: userCounts[email]})
	}
	result.Summary.Users = len(result.UserCounts)

	distributionOK := true
	if cfg.ExpectedDevicesPerUser > 0 {
		for _, user := range result.UserCounts {
			if user.DeviceCount != cfg.ExpectedDevicesPerUser {
				distributionOK = false
				break
			}
		}
	}
	expectedUsers := 0
	if cfg.ExpectedDevicesPerUser > 0 && cfg.ExpectedCount > 0 {
		expectedUsers = cfg.ExpectedCount / cfg.ExpectedDevicesPerUser
		distributionOK = distributionOK && expectedUsers == len(result.UserCounts)
	}
	result.addCheck("devices_per_user", distributionOK, fmt.Sprintf("users=%d expected_users=%d expected_devices_per_user=%d", len(result.UserCounts), expectedUsers, cfg.ExpectedDevicesPerUser))

	result.Overall = StatusPass
	for _, check := range result.Checks {
		if check.Status == StatusFail {
			result.Overall = StatusFail
			break
		}
	}
	result.EndedAt = time.Now().UTC()
	return result
}

func (r *Result) addCheck(name string, ok bool, evidence string) {
	status := StatusPass
	if !ok {
		status = StatusFail
	}
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Evidence: evidence})
}

func validServiceOption(service string) bool {
	switch service {
	case "mqtt", "video_streaming", "video_storage":
		return true
	default:
		return false
	}
}

func containsOnlyMQTT(services []string) bool {
	if len(services) != 1 {
		return false
	}
	return services[0] == "mqtt"
}

func hasAnyVideoService(services []string) bool {
	for _, service := range services {
		if strings.HasPrefix(service, "video_") {
			return true
		}
	}
	return false
}
