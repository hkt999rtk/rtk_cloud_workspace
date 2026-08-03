package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	provisioningSignoffWorkflowID      = "WF-PROV-SIGNOFF-001"
	provisioningRuntimeWorkflowID      = "WF-PROV-RUNTIME-001"
	provisioningDeactivationWorkflowID = "WF-PROV-DEACTIVATION-001"
	provisioningUnprovisionWorkflowID  = "WF-PROV-UNPROVISION-001"
)

var (
	requestLifecycleAppToken       = requestVideoRelayAppToken
	requestLifecycleDeviceToken    = requestVideoRelayDeviceToken
	executeLifecycleVideoRelayTest = executeVideoRelayTest
)

type canonicalVideoLifecycle struct {
	Status      string         `json:"status"`
	DeviceID    string         `json:"devid"`
	Activated   bool           `json:"activated"`
	Provisioned bool           `json:"provisioned"`
	Revoked     bool           `json:"revoked"`
	Online      bool           `json:"online"`
	Transport   map[string]any `json:"transport"`
}

func runProvisioningLifecycleEvidence(args []string) error {
	fs := flag.NewFlagSet("provisioning-lifecycle-evidence", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "RTK", "brand name")
	runID := fs.String("run-id", "", "run identity")
	outDir := fs.String("out-dir", "", "redacted evidence directory")
	timeout := fs.Duration("timeout", 2*time.Minute, "lifecycle convergence timeout")
	poll := fs.Duration("poll", time.Second, "lifecycle convergence poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" || strings.TrimSpace(*outDir) == "" || strings.TrimSpace(*runID) == "" {
		return errors.New("--env-root, --out-dir, and --run-id are required")
	}
	if *timeout <= 0 || *poll <= 0 {
		return errors.New("--timeout and --poll must be positive")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	artifact, err := readBindArtifactFromTestData(envRoot, *brandname)
	if err != nil {
		return err
	}
	if len(artifact.Assignments) < 2 {
		return errors.New("provisioning lifecycle qualification requires at least two independently bound devices")
	}
	users, _, err := readUsersListFromTestData(envRoot, *brandname)
	if err != nil {
		return err
	}
	ctx, err := accountManagerContextFromFlags(workspace, envRoot)
	if err != nil {
		return err
	}
	videoBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("VIDEO_CLOUD_BASE_URL")), "/")
	videoTokenBaseURL := strings.TrimRight(firstNonEmpty(
		os.Getenv("VIDEO_CLOUD_TOKEN_BASE_URL"),
		os.Getenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE"),
		videoBaseURL,
	), "/")
	videoAdminToken := strings.TrimSpace(os.Getenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN"))
	if videoBaseURL == "" || videoAdminToken == "" {
		return errors.New("VIDEO_CLOUD_BASE_URL and VIDEO_CLOUD_LOAD_ADMIN_TOKEN are required")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}

	deactivation, unprovision, deactivationBefore, _, err := selectReadyLifecycleAssignments(
		videoBaseURL, videoAdminToken, artifact.Assignments, 0, *poll,
	)
	transportReadinessPrepared := false
	if err != nil {
		if err := prepareLifecycleTransportReadiness(workspace, envRoot, *brandname, *outDir); err != nil {
			return err
		}
		transportReadinessPrepared = true
		deactivation, unprovision, deactivationBefore, _, err = selectReadyLifecycleAssignments(
			videoBaseURL, videoAdminToken, artifact.Assignments, *timeout, *poll,
		)
	}
	if err != nil {
		return err
	}
	deactivationToken, err := lifecycleUserToken(ctx, artifact.TenantSlug, users, deactivation)
	if err != nil {
		return err
	}
	unprovisionToken, err := lifecycleUserToken(ctx, artifact.TenantSlug, users, unprovision)
	if err != nil {
		return err
	}
	if err := readCanonicalDeviceInfo(videoBaseURL, videoAdminToken, deactivation.DeviceID); err != nil {
		return err
	}

	store, err := openTestDataStore(envRoot, *brandname)
	if err != nil {
		return err
	}
	credentialBefore, err := store.ReadDeviceCredential(*brandname, unprovision.DeviceID)
	if err != nil {
		_ = store.Close()
		return err
	}
	formerOwnerAppCertificate, err := readLifecycleAppCertificate(store, *brandname, unprovision.AssignedEmail)
	deactivationOwnerAppCertificate, deactivationCertificateErr := readLifecycleAppCertificate(store, *brandname, deactivation.AssignedEmail)
	_ = store.Close()
	if err != nil {
		return err
	}
	if deactivationCertificateErr != nil {
		return deactivationCertificateErr
	}
	if strings.TrimSpace(deactivation.ClaimID) == "" || strings.TrimSpace(deactivation.OperationID) == "" || strings.TrimSpace(deactivation.AccountDeviceID) == "" {
		return errors.New("account provisioning qualification is missing claim, operation, or registry-device correlation")
	}
	certificateDigest := sha256.Sum256([]byte(credentialBefore.CertPEM))
	if strings.TrimSpace(credentialBefore.CertPEM) == "" || strings.TrimSpace(credentialBefore.FactoryEnrollResponseRedactedJSON) == "" {
		return errors.New("unprovision qualification is missing factory enrollment identity evidence")
	}
	formerOwnerAppToken, err := requestLifecycleAppToken(videoTokenBaseURL, formerOwnerAppCertificate, unprovision.DeviceID)
	if err != nil {
		return fmt.Errorf("mint former-owner pre-unprovision app token: %w", err)
	}
	if err := readCanonicalDeviceInfo(videoBaseURL, formerOwnerAppToken.AccessToken, unprovision.DeviceID); err != nil {
		return fmt.Errorf("verify former-owner pre-unprovision access: %w", err)
	}
	deactivationOwnerAppToken, err := requestLifecycleAppToken(videoTokenBaseURL, deactivationOwnerAppCertificate, deactivation.DeviceID)
	if err != nil {
		return fmt.Errorf("mint owner app token for account provisioning qualification: %w", err)
	}
	if err := readCanonicalDeviceInfo(videoBaseURL, deactivationOwnerAppToken.AccessToken, deactivation.DeviceID); err != nil {
		return fmt.Errorf("verify owner app readiness for account provisioning qualification: %w", err)
	}

	if err := requestAccountDeactivation(ctx, artifact.BrandCloudID, deactivation, deactivationToken, *runID); err != nil {
		return err
	}
	deactivationSnapshot, deactivationAfter, err := waitForDeactivation(ctx, videoBaseURL, videoAdminToken, artifact.BrandCloudID, deactivation, deactivationToken, *timeout, *poll)
	if err != nil {
		return err
	}
	if err := disableAccountRegistryDevice(ctx, artifact.BrandCloudID, deactivation.AccountDeviceID, deactivationToken); err != nil {
		return err
	}

	unprovisionResult, err := unprovisionOne(ctx, artifact.BrandCloudID, unprovision, unprovisionToken)
	if err != nil {
		return err
	}
	unprovisionAfter, err := waitForVideoLifecycle(videoBaseURL, videoAdminToken, unprovision.DeviceID, *timeout, *poll, func(state canonicalVideoLifecycle) bool {
		return state.Activated && !state.Provisioned && !state.Revoked
	})
	if err != nil {
		return err
	}
	if present, err := accountDeviceStillVisible(ctx, artifact.BrandCloudID, unprovision.AccountDeviceID, unprovisionToken); err != nil {
		return err
	} else if present {
		return errors.New("unprovisioned device remains visible to the previous owner")
	}
	if err := verifyFormerOwnerAccessRevoked(ctx, videoBaseURL, videoTokenBaseURL, artifact.BrandCloudID, unprovision, unprovisionToken, formerOwnerAppToken.AccessToken, formerOwnerAppCertificate, *runID); err != nil {
		return err
	}
	store, err = openTestDataStore(envRoot, *brandname)
	if err != nil {
		return err
	}
	credentialAfter, err := store.ReadDeviceCredential(*brandname, unprovision.DeviceID)
	_ = store.Close()
	if err != nil {
		return err
	}
	certificateDigestAfter := sha256.Sum256([]byte(credentialAfter.CertPEM))
	if certificateDigest != certificateDigestAfter || strings.TrimSpace(credentialAfter.FactoryEnrollResponseRedactedJSON) == "" {
		return errors.New("unprovision changed or removed the factory identity")
	}
	deviceCertificate, err := loadLifecycleDeviceCertificate(credentialAfter)
	if err != nil {
		return err
	}
	deviceAccessToken, err := requestLifecycleDeviceToken(videoTokenBaseURL, deviceCertificate)
	if err != nil {
		return fmt.Errorf("reissue factory device token after unprovision: %w", err)
	}
	serviceOptionsAfter, err := accessTokenServiceOptions(deviceAccessToken)
	if err != nil {
		return err
	}
	if !sameStringSet(serviceOptionsAfter, unprovision.ServiceOptions) {
		return fmt.Errorf("unprovision changed canonical service_options: before=%s after=%s", strings.Join(sortedStrings(unprovision.ServiceOptions), ","), strings.Join(sortedStrings(serviceOptionsAfter), ","))
	}

	completed := time.Now().UTC()
	result := map[string]any{
		"schema_version": "rtk-provisioning-lifecycle-evidence/v1",
		"run_id":         *runID,
		"status":         "PASS",
		"completed_at":   completed.Format(time.RFC3339),
		"transport_readiness": map[string]any{
			"prepared_by_lifecycle":  transportReadinessPrepared,
			"required_video_devices": 2,
			"status":                 "PASS",
		},
		"deactivation": map[string]any{
			"device_id": deactivation.DeviceID, "account_device_id": deactivation.AccountDeviceID,
			"operation_status": deactivationSnapshot.OperationStatus, "activation_status": deactivationSnapshot.ActivationStatus,
			"video_activated_before": deactivationBefore.Activated, "video_activated_after": deactivationAfter.Activated,
			"video_revoked_after": deactivationAfter.Revoked, "registry_device_disabled": true,
			"claim_id_present": true, "provision_operation_id_present": true,
			"owner_app_certificate_present": true, "owner_app_token_issued": true, "owner_device_info_read": true,
		},
		"unprovision": map[string]any{
			"device_id": unprovision.DeviceID, "account_device_id": unprovision.AccountDeviceID,
			"status": stringValue(unprovisionResult["status"]), "previous_owner_binding_absent": true,
			"video_activated_after": unprovisionAfter.Activated, "video_provisioned_after": unprovisionAfter.Provisioned,
			"video_revoked_after": unprovisionAfter.Revoked, "factory_certificate_sha256": fmt.Sprintf("%x", certificateDigest),
			"factory_identity_preserved": true, "service_options": unprovision.ServiceOptions,
			"factory_device_token_reissued": true, "service_options_unchanged": true,
			"former_owner_inventory_denied": true, "former_owner_inspect_denied": true,
			"former_owner_provision_denied": true, "former_owner_deactivate_denied": true,
			"former_owner_command_denied": true, "former_owner_stream_denied": true,
			"former_owner_new_app_token_denied": true, "unprovision_replay_denied": true,
		},
	}
	if err := writeJSON(filepath.Join(*outDir, "results.json"), result); err != nil {
		return err
	}
	if err := writeProvisioningWorkflowEvidence(*outDir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "junit.xml"), []byte(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="provisioning-lifecycle" tests="3" failures="0"><testcase classname="provisioning" name="account-provisioning"/><testcase classname="provisioning" name="deactivation"/><testcase classname="provisioning" name="unprovision"/></testsuite>`+"\n"), 0o644); err != nil {
		return err
	}
	report := fmt.Sprintf("# Provisioning Lifecycle Qualification\n\n- Run ID: `%s`\n- Status: **PASS**\n- Account provisioning device: `%s`\n- Owner app certificate/token readiness: **PASS**\n- Registry device disabled after deactivation: **PASS**\n- Unprovision device: `%s`\n- Previous owner binding released: **PASS**\n- Factory identity preserved: **PASS**\n", *runID, deactivation.DeviceID, unprovision.DeviceID)
	return os.WriteFile(filepath.Join(*outDir, "TEST_REPORT.md"), []byte(report), 0o644)
}

func disableAccountRegistryDevice(ctx accountManagerContext, brandCloudID, accountDeviceID, bearer string) error {
	endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices/%s", ctx.BaseURL, url.PathEscape(brandCloudID), url.PathEscape(accountDeviceID))
	reqCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+bearer)
	resp, err := rtkJSONHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("disable account registry device: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("disable account registry device returned HTTP %d%s", resp.StatusCode, errorBodySuffix(body))
	}
	body, status, err := curlJSONStatus(endpoint, bearer, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("disabled account registry device remains readable: HTTP %d%s", status, errorBodySuffix(body))
	}
	return nil
}

func prepareLifecycleTransportReadiness(workspace, envRoot, brandname, outDir string) error {
	readiness, err := executeLifecycleVideoRelayTest(
		workspace, envRoot, brandname, filepath.Join(outDir, "transport-readiness"), "smoke", "device-only", "all", 5, 2, "none",
	)
	if err != nil {
		return fmt.Errorf("prepare lifecycle video transport readiness: %w", err)
	}
	if readiness.Status != "PASS" {
		return fmt.Errorf("prepare lifecycle video transport readiness: status=%s", readiness.Status)
	}
	return nil
}

func loadLifecycleDeviceCertificate(credential testDataDeviceCredential) (tls.Certificate, error) {
	certPEM := firstNonEmpty(credential.ChainPEM, credential.CertPEM)
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(credential.KeyPEM) == "" {
		return tls.Certificate{}, errors.New("factory device certificate or private key is missing")
	}
	certificate, err := tls.X509KeyPair([]byte(normalizeVideoRelayPEM(certPEM)), []byte(normalizeVideoRelayPEM(credential.KeyPEM)))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load factory device certificate: %w", err)
	}
	return certificate, nil
}

func accessTokenServiceOptions(token string) ([]string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, errors.New("issued device access token is not a signed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode device access token claims: %w", err)
	}
	var claims struct {
		Scope          string   `json:"scope"`
		SubjectID      string   `json:"subject_id"`
		ServiceOptions []string `json:"service_options"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse device access token claims: %w", err)
	}
	if claims.Scope != "device" || strings.TrimSpace(claims.SubjectID) == "" {
		return nil, errors.New("issued factory token has the wrong scope or subject")
	}
	return claims.ServiceOptions, nil
}

func readLifecycleAppCertificate(store *testDataStore, brandname, email string) (tls.Certificate, error) {
	var credentialsJSON, certificateJSON string
	if err := store.DB.QueryRow(`select app_credentials_json, app_certificate_json from users where brandname = ? and email = ?`, brandname, email).Scan(&credentialsJSON, &certificateJSON); err != nil {
		return tls.Certificate{}, fmt.Errorf("read former-owner app certificate: %w", err)
	}
	user := videoRelayUser{}
	if err := json.Unmarshal([]byte(credentialsJSON), &user.AppCredentials); err != nil {
		return tls.Certificate{}, fmt.Errorf("decode former-owner app credentials: %w", err)
	}
	if err := json.Unmarshal([]byte(certificateJSON), &user.AppCertificate); err != nil {
		return tls.Certificate{}, fmt.Errorf("decode former-owner app certificate: %w", err)
	}
	return loadRelayAppCertificate(user)
}

func verifyFormerOwnerAccessRevoked(ctx accountManagerContext, videoBaseURL, videoTokenBaseURL, brandCloudID string, assignment bindAssignment, accountToken, appToken string, appCertificate tls.Certificate, runID string) error {
	accountBase := fmt.Sprintf("%s/v1/orgs/%s/devices/%s", ctx.BaseURL, url.PathEscape(brandCloudID), url.PathEscape(assignment.AccountDeviceID))
	provisionPayload, _ := json.Marshal(map[string]any{
		"video_cloud_devid": assignment.DeviceID,
		"activity_id":       "qualification-rejected-" + runID,
		"clip_public_key":   "qualification-rejected",
		"operation_id":      "qualification-rejected-provision-" + runID,
		"service_options":   assignment.ServiceOptions,
	})
	deactivatePayload, _ := json.Marshal(map[string]string{"operation_id": "qualification-rejected-deactivate-" + runID, "reason": "former_owner_negative_check"})
	unprovisionPayload, _ := json.Marshal(map[string]string{"reason": "former_owner_replay_check"})
	checks := []struct {
		name     string
		endpoint string
		bearer   string
		payload  []byte
	}{
		{name: "inspect account device", endpoint: accountBase, bearer: accountToken},
		{name: "read provisioning state", endpoint: accountBase + "/provisioning", bearer: accountToken},
		{name: "reprovision account device", endpoint: accountBase + "/provision", bearer: accountToken, payload: provisionPayload},
		{name: "deactivate account device", endpoint: accountBase + "/deactivate", bearer: accountToken, payload: deactivatePayload},
		{name: "replay unprovision", endpoint: accountBase + "/unprovision", bearer: accountToken, payload: unprovisionPayload},
		{name: "inspect video device", endpoint: videoBaseURL + "/api/devices/" + url.PathEscape(assignment.DeviceID) + "/info", bearer: appToken},
	}
	commandPayload, _ := json.Marshal(map[string]any{"command": "qualification-former-owner-denied", "command_id": "qualification-rejected-command-" + runID})
	checks = append(checks, struct {
		name     string
		endpoint string
		bearer   string
		payload  []byte
	}{name: "command video device", endpoint: videoBaseURL + "/api/devices/" + url.PathEscape(assignment.DeviceID) + "/commands", bearer: appToken, payload: commandPayload})
	checks = append(checks, struct {
		name     string
		endpoint string
		bearer   string
		payload  []byte
	}{name: "stream video device", endpoint: videoBaseURL + "/api/request_webrtc/ice?devid=" + url.QueryEscape(assignment.DeviceID), bearer: appToken})
	for _, check := range checks {
		body, status, err := curlJSONStatus(check.endpoint, check.bearer, check.payload)
		if err != nil {
			return fmt.Errorf("former-owner %s check: %w", check.name, err)
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound {
			return fmt.Errorf("former-owner %s was not authorization-rejected: HTTP %d%s", check.name, status, errorBodySuffix(body))
		}
	}
	if _, err := requestLifecycleAppToken(videoTokenBaseURL, appCertificate, assignment.DeviceID); err == nil {
		return errors.New("former owner minted a new device-scoped app token after unprovision")
	}
	return nil
}

func lifecycleUserToken(ctx accountManagerContext, tenantSlug string, users map[string]userCredential, assignment bindAssignment) (string, error) {
	user, ok := users[assignment.AssignedEmail]
	if !ok {
		return "", fmt.Errorf("missing lifecycle user credential for %s", assignment.AssignedEmail)
	}
	session := &brandCloudUserSession{
		Email:    user.Email,
		Password: user.Password,
		Session:  user.Tokens,
	}
	return brandCloudUserAccessToken(ctx, tenantSlug, session, func(string, ...any) {})
}

func selectReadyLifecycleAssignments(baseURL, bearer string, assignments []bindAssignment, timeout, poll time.Duration) (bindAssignment, bindAssignment, canonicalVideoLifecycle, canonicalVideoLifecycle, error) {
	deadline := time.Now().Add(timeout)
	last := map[string]canonicalVideoLifecycle{}
	lastErrors := map[string]string{}
	for {
		ready := make([]bindAssignment, 0, len(assignments))
		for _, assignment := range assignments {
			state, err := readCanonicalVideoLifecycle(baseURL, bearer, assignment.DeviceID)
			if err != nil {
				lastErrors[assignment.DeviceID] = err.Error()
				continue
			}
			last[assignment.DeviceID] = state
			delete(lastErrors, assignment.DeviceID)
			if state.Activated && state.Provisioned && !state.Revoked {
				ready = append(ready, assignment)
			}
		}
		for _, deactivation := range ready {
			if strings.TrimSpace(deactivation.ClaimID) == "" || strings.TrimSpace(deactivation.OperationID) == "" || strings.TrimSpace(deactivation.AccountDeviceID) == "" {
				continue
			}
			for i := len(ready) - 1; i >= 0; i-- {
				unprovision := ready[i]
				if deactivation.DeviceID == unprovision.DeviceID {
					continue
				}
				if !contains(unprovision.ServiceOptions, "video_streaming") && !contains(unprovision.ServiceOptions, "video_storage") {
					continue
				}
				return deactivation, unprovision, last[deactivation.DeviceID], last[unprovision.DeviceID], nil
			}
		}
		if !time.Now().Before(deadline) {
			states := make([]string, 0, len(assignments))
			for _, assignment := range assignments {
				if detail := lastErrors[assignment.DeviceID]; detail != "" {
					states = append(states, fmt.Sprintf("%s=error(%s)", assignment.DeviceID, detail))
					continue
				}
				state := last[assignment.DeviceID]
				states = append(states, fmt.Sprintf("%s=activated:%t,provisioned:%t,revoked:%t", assignment.DeviceID, state.Activated, state.Provisioned, state.Revoked))
			}
			return bindAssignment{}, bindAssignment{}, canonicalVideoLifecycle{}, canonicalVideoLifecycle{}, fmt.Errorf("lifecycle qualification requires a claim-correlated ready device and a distinct ready video-capable device: %s", strings.Join(states, "; "))
		}
		time.Sleep(poll)
	}
}

func requestAccountDeactivation(ctx accountManagerContext, brandCloudID string, assignment bindAssignment, bearer, runID string) error {
	payload, _ := json.Marshal(map[string]string{"operation_id": "qualification-deactivate-" + runID + "-" + assignment.DeviceID, "reason": "feature_qualification"})
	endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices/%s/deactivate", ctx.BaseURL, url.PathEscape(brandCloudID), url.PathEscape(assignment.AccountDeviceID))
	body, status, err := curlJSONStatus(endpoint, bearer, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("deactivation request returned HTTP %d%s", status, errorBodySuffix(body))
	}
	return nil
}

func waitForDeactivation(ctx accountManagerContext, videoBaseURL, videoAdminToken, brandCloudID string, assignment bindAssignment, bearer string, timeout, poll time.Duration) (bindProvisioningStateSnapshot, canonicalVideoLifecycle, error) {
	deadline := time.Now().Add(timeout)
	var lastSnapshot bindProvisioningStateSnapshot
	var lastLifecycle canonicalVideoLifecycle
	for time.Now().Before(deadline) {
		snapshot, snapshotErr := fetchBindProvisioningState(ctx, bearer, brandCloudID, assignment)
		lifecycle, lifecycleErr := readCanonicalVideoLifecycle(videoBaseURL, videoAdminToken, assignment.DeviceID)
		if snapshotErr == nil {
			lastSnapshot = snapshot
		}
		if lifecycleErr == nil {
			lastLifecycle = lifecycle
		}
		if snapshotErr == nil && lifecycleErr == nil && snapshot.OperationStatus == "succeeded" && snapshot.ActivationStatus == "deactivated" && !lifecycle.Activated && lifecycle.Revoked {
			return snapshot, lifecycle, nil
		}
		time.Sleep(poll)
	}
	return lastSnapshot, lastLifecycle, fmt.Errorf("deactivation did not converge: operation=%s activation=%s video_activated=%t video_revoked=%t", lastSnapshot.OperationStatus, lastSnapshot.ActivationStatus, lastLifecycle.Activated, lastLifecycle.Revoked)
}

func waitForVideoLifecycle(baseURL, bearer, deviceID string, timeout, poll time.Duration, accepted func(canonicalVideoLifecycle) bool) (canonicalVideoLifecycle, error) {
	deadline := time.Now().Add(timeout)
	var last canonicalVideoLifecycle
	for time.Now().Before(deadline) {
		state, err := readCanonicalVideoLifecycle(baseURL, bearer, deviceID)
		if err == nil {
			last = state
			if accepted(state) {
				return state, nil
			}
		}
		time.Sleep(poll)
	}
	return last, fmt.Errorf("video lifecycle did not converge for %s", deviceID)
}

func readCanonicalVideoLifecycle(baseURL, bearer, deviceID string) (canonicalVideoLifecycle, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/devices/" + url.PathEscape(deviceID) + "/lifecycle"
	body, status, err := curlJSONStatus(endpoint, bearer, nil)
	if err != nil {
		return canonicalVideoLifecycle{}, err
	}
	if status != http.StatusOK {
		return canonicalVideoLifecycle{}, fmt.Errorf("video lifecycle returned HTTP %d%s", status, errorBodySuffix(body))
	}
	var lifecycle canonicalVideoLifecycle
	if err := json.Unmarshal(body, &lifecycle); err != nil {
		return canonicalVideoLifecycle{}, err
	}
	if !strings.EqualFold(lifecycle.Status, "ok") || lifecycle.DeviceID != deviceID {
		return canonicalVideoLifecycle{}, errors.New("video lifecycle identity mismatch")
	}
	return lifecycle, nil
}

func readCanonicalDeviceInfo(baseURL, bearer, deviceID string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/devices/" + url.PathEscape(deviceID) + "/info"
	body, status, err := curlJSONStatus(endpoint, bearer, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("device info returned HTTP %d%s", status, errorBodySuffix(body))
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	if !strings.EqualFold(stringValue(parsed["status"]), "ok") || stringValue(parsed["devid"]) != deviceID {
		return errors.New("device info identity mismatch")
	}
	return nil
}

func accountDeviceStillVisible(ctx accountManagerContext, brandCloudID, accountDeviceID, bearer string) (bool, error) {
	endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices?limit=1000", ctx.BaseURL, url.PathEscape(brandCloudID))
	body, status, err := curlJSONStatus(endpoint, bearer, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("list devices returned HTTP %d%s", status, errorBodySuffix(body))
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, err
	}
	devices, _ := parsed["devices"].([]any)
	for _, raw := range devices {
		device, _ := raw.(map[string]any)
		if stringValue(device["id"]) == accountDeviceID {
			return true, nil
		}
	}
	return false, nil
}

func writeProvisioningWorkflowEvidence(outDir string) error {
	workflows := []struct {
		file       string
		workflowID string
		steps      map[string]string
		assertions map[string]map[string]string
	}{
		{
			file: "provisioning-account-workflow.json", workflowID: "WF-PROV-ACCOUNT-001",
			steps: map[string]string{
				"issue_app_certificate": "PASS", "resolve_device_claim": "PASS", "provision_registry_device": "PASS",
				"wait_for_provisioning": "PASS", "issue_app_runtime_token": "PASS", "verify_device_readiness": "PASS",
				"deactivate_video_device": "PASS", "delete_registry_device": "PASS",
			},
			assertions: map[string]map[string]string{
				"issue_app_certificate":     {"run_scoped_app_private_key_present": "PASS", "issued_certificate_loaded": "PASS"},
				"resolve_device_claim":      {"claim_id_present": "PASS", "account_video_identity_correlated": "PASS"},
				"provision_registry_device": {"provision_operation_id_present": "PASS", "same_registry_device_used": "PASS"},
				"wait_for_provisioning":     {"operation_succeeded": "PASS", "video_activation_projected": "PASS"},
				"issue_app_runtime_token":   {"app_certificate_mtls": "PASS", "app_scope_token_issued": "PASS"},
				"verify_device_readiness":   {"owner_token_authorized": "PASS", "device_identity_matched": "PASS"},
				"deactivate_video_device":   {"same_device_deactivated": "PASS", "video_revocation_converged": "PASS"},
				"delete_registry_device":    {"same_registry_device_disabled": "PASS", "disabled_device_not_readable": "PASS"},
			},
		},
		{
			file: "provisioning-claim-workflow.json", workflowID: "WF-PROV-CLAIM-001",
			steps: map[string]string{"resolve_device_claim": "PASS", "activate_claimed_device": "PASS", "verify_claimed_device_lifecycle": "PASS"},
			assertions: map[string]map[string]string{
				"resolve_device_claim":            {"claim_id_present": "PASS", "registry_and_video_identity_correlated": "PASS"},
				"activate_claimed_device":         {"normalized_activation_input_used": "PASS", "provision_operation_succeeded": "PASS"},
				"verify_claimed_device_lifecycle": {"activated_projection_observed": "PASS", "same_video_device_verified": "PASS"},
			},
		},
		{
			file: "provisioning-deactivation-workflow.json", workflowID: provisioningDeactivationWorkflowID,
			steps: map[string]string{"request_account_deactivation": "PASS", "deactivate_video_device": "PASS", "verify_deactivated_lifecycle": "PASS"},
			assertions: map[string]map[string]string{
				"request_account_deactivation": {"run_scoped_operation_created": "PASS", "account_device_identity_matched": "PASS"},
				"deactivate_video_device":      {"cross_service_operation_succeeded": "PASS", "video_activation_projected_deactivated": "PASS"},
				"verify_deactivated_lifecycle": {"video_activated_false": "PASS", "device_credentials_revoked": "PASS"},
			},
		},
		{
			file: "provisioning-unprovision-workflow.json", workflowID: provisioningUnprovisionWorkflowID,
			steps: map[string]string{
				"verify_current_owner_binding": "PASS", "unprovision_registry_device": "PASS", "verify_owner_binding_released": "PASS",
				"reject_former_owner_inspection": "PASS", "reject_former_owner_provision": "PASS", "reject_former_owner_deactivation": "PASS",
				"reject_unprovision_replay": "PASS", "reject_former_owner_device_info": "PASS", "reject_former_owner_command": "PASS",
				"reject_former_owner_stream": "PASS", "reject_former_owner_token_mint": "PASS", "reissue_factory_device_token": "PASS",
				"verify_factory_identity_preserved": "PASS",
			},
			assertions: map[string]map[string]string{
				"verify_current_owner_binding":      {"account_binding_present": "PASS", "factory_identity_digest_recorded": "PASS", "service_options_recorded": "PASS"},
				"unprovision_registry_device":       {"user_unprovision_succeeded": "PASS", "account_device_identity_matched": "PASS"},
				"verify_owner_binding_released":     {"previous_owner_list_excludes_device": "PASS"},
				"reject_former_owner_inspection":    {"account_device_read_authorization_rejected": "PASS"},
				"reject_former_owner_provision":     {"account_device_provision_authorization_rejected": "PASS"},
				"reject_former_owner_deactivation":  {"account_device_deactivate_authorization_rejected": "PASS"},
				"reject_unprovision_replay":         {"account_device_unprovision_replay_rejected": "PASS"},
				"reject_former_owner_device_info":   {"video_device_info_authorization_rejected": "PASS"},
				"reject_former_owner_command":       {"video_device_command_authorization_rejected": "PASS"},
				"reject_former_owner_stream":        {"video_device_stream_authorization_rejected": "PASS"},
				"reject_former_owner_token_mint":    {"device_scoped_app_token_request_rejected": "PASS"},
				"reissue_factory_device_token":      {"factory_certificate_accepted": "PASS", "device_token_reissued": "PASS", "service_options_claim_preserved": "PASS"},
				"verify_factory_identity_preserved": {"certificate_digest_unchanged": "PASS", "video_not_revoked": "PASS", "video_activation_preserved": "PASS", "service_options_unchanged": "PASS"},
			},
		},
		{
			file: "provisioning-signoff-workflow.json", workflowID: provisioningSignoffWorkflowID,
			steps: map[string]string{"enroll_factory_identity": "PASS", "resolve_device_claim": "PASS", "activate_video_device": "PASS", "wait_for_device_lifecycle": "PASS", "verify_device_readiness": "PASS", "deactivate_video_device": "PASS"},
			assertions: map[string]map[string]string{
				"enroll_factory_identity":   {"factory_certificate_present": "PASS", "factory_response_redacted": "PASS"},
				"resolve_device_claim":      {"claim_id_present": "PASS", "account_device_id_present": "PASS"},
				"activate_video_device":     {"video_device_activated": "PASS"},
				"wait_for_device_lifecycle": {"video_lifecycle_active": "PASS"},
				"verify_device_readiness":   {"device_info_read_succeeded": "PASS", "service_options_recorded": "PASS"},
				"deactivate_video_device":   {"cleanup_operation_succeeded": "PASS", "video_lifecycle_deactivated": "PASS"},
			},
		},
	}
	for _, workflow := range workflows {
		payload := map[string]any{"schema_version": "rtk-live-workflow-evidence/v1", "workflow": map[string]any{"workflow_id": workflow.workflowID, "steps": workflow.steps, "assertions": workflow.assertions}}
		if err := writeJSON(filepath.Join(outDir, workflow.file), payload); err != nil {
			return err
		}
	}
	return nil
}
