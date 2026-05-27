package accountvideosmoke

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func LoadAccountFixture(dir string) (AccountFixture, error) {
	if dir == "" {
		return AccountFixture{}, errors.New("E2E_ACCOUNT_USERS_DIR is not configured")
	}
	path := filepath.Join(dir, "credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return AccountFixture{}, fmt.Errorf("read credentials.json: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return AccountFixture{}, fmt.Errorf("parse credentials.json: %w", err)
	}
	fixture := AccountFixture{
		Email:          stringField(raw, "email"),
		Password:       stringField(raw, "password"),
		UserID:         firstString(raw, "user_id", "userId", "id"),
		OrganizationID: firstString(raw, "organization_id", "organizationId", "org_id", "orgId"),
	}
	if fixture.Email == "" || fixture.Password == "" {
		if users, ok := raw["users"].([]any); ok && len(users) > 0 {
			if user, ok := users[0].(map[string]any); ok {
				fixture.Email = stringField(user, "email")
				fixture.Password = stringField(user, "password")
				fixture.UserID = firstString(user, "user_id", "userId", "id")
				fixture.OrganizationID = firstString(user, "organization_id", "organizationId", "org_id", "orgId")
			}
		}
	}
	if fixture.OrganizationID == "" {
		if org, ok := raw["organization"].(map[string]any); ok {
			fixture.OrganizationID = firstString(org, "id", "organization_id", "organizationId")
		}
	}
	if fixture.Email == "" || fixture.Password == "" {
		return AccountFixture{}, errors.New("credentials.json must include email and password")
	}
	if fixture.OrganizationID == "" {
		return AccountFixture{}, errors.New("credentials.json must include organization_id")
	}
	return fixture, nil
}

func LoadDeviceCertset(dir, requestedDeviceID string) (DeviceCertset, error) {
	if dir == "" {
		return DeviceCertset{}, errors.New("E2E_DEVICE_CERTSET_DIR is not configured")
	}
	resultPath := filepath.Join(dir, "factory-enroll-results.json")
	b, err := os.ReadFile(resultPath)
	if err != nil {
		return DeviceCertset{}, fmt.Errorf("read factory-enroll-results.json: %w", err)
	}
	var raw struct {
		Devices []struct {
			Index    int    `json:"index"`
			DeviceID string `json:"devid"`
			Success  bool   `json:"success"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return DeviceCertset{}, fmt.Errorf("parse factory-enroll-results.json: %w", err)
	}
	deviceID := requestedDeviceID
	deviceIndex := 0
	if deviceID == "" {
		for _, device := range raw.Devices {
			if device.Success && device.DeviceID != "" {
				deviceID = device.DeviceID
				deviceIndex = device.Index
				break
			}
		}
	} else {
		for _, device := range raw.Devices {
			if device.DeviceID == deviceID {
				deviceIndex = device.Index
				break
			}
		}
	}
	if deviceID == "" {
		return DeviceCertset{}, errors.New("no successful factory-enrolled device found")
	}
	deviceDir, err := findDeviceMaterialDir(filepath.Join(dir, "device-material"), deviceID, deviceIndex)
	if err != nil {
		return DeviceCertset{}, err
	}
	certset := DeviceCertset{
		RootDir:         dir,
		DeviceDir:       deviceDir,
		DeviceID:        deviceID,
		DeviceKeyPath:   filepath.Join(deviceDir, "device.key"),
		DeviceCertPath:  filepath.Join(deviceDir, "device.crt"),
		DeviceChainPath: filepath.Join(deviceDir, "device-chain.crt"),
		DeviceCAPath:    filepath.Join(dir, "device-ca.crt"),
	}
	if _, err := os.Stat(certset.DeviceKeyPath); err != nil {
		return DeviceCertset{}, fmt.Errorf("device key missing: %w", err)
	}
	if _, err := os.Stat(certset.DeviceChainPath); err != nil {
		return DeviceCertset{}, fmt.Errorf("device chain missing: %w", err)
	}
	return certset, nil
}

func findDeviceMaterialDir(root, deviceID string, deviceIndex int) (string, error) {
	if deviceIndex > 0 {
		candidate := filepath.Join(root, fmt.Sprintf("device-%03d", deviceIndex))
		if _, err := os.Stat(filepath.Join(candidate, "device.key")); err == nil {
			return candidate, nil
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read device-material: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		candidate := filepath.Join(root, name)
		if _, err := os.Stat(filepath.Join(candidate, "device.key")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no device material directory found for %s", deviceID)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringField(m, key); v != "" {
			return v
		}
	}
	return ""
}
