package cloudvalidation

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var scenarioIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)

func LoadScenarios(paths []string) ([]Scenario, error) {
	var scenarios []Scenario
	seen := map[string]bool{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read scenario manifest %s: %w", path, err)
		}
		var manifest ScenarioManifest
		if err := yaml.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("parse scenario manifest %s: %w", path, err)
		}
		if manifest.SchemaVersion != 1 {
			return nil, fmt.Errorf("scenario manifest %s schema_version=%d, want 1", path, manifest.SchemaVersion)
		}
		for _, scenario := range manifest.Scenarios {
			if err := validateScenario(scenario); err != nil {
				return nil, fmt.Errorf("scenario manifest %s: %w", path, err)
			}
			if seen[scenario.ID] {
				return nil, fmt.Errorf("duplicate scenario id %q", scenario.ID)
			}
			seen[scenario.ID] = true
			scenarios = append(scenarios, scenario)
		}
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios configured")
	}
	return scenarios, nil
}

func validateScenario(s Scenario) error {
	if !scenarioIDPattern.MatchString(s.ID) {
		return fmt.Errorf("scenario id %q must match %s", s.ID, scenarioIDPattern)
	}
	if strings.TrimSpace(s.DeviceProfile) == "" || strings.TrimSpace(s.AppAction) == "" || strings.TrimSpace(s.ExpectedSDKResult) == "" {
		return fmt.Errorf("scenario %s requires device_profile, app_action, and expected_sdk_result", s.ID)
	}
	if strings.TrimSpace(s.Cleanup) == "" {
		return fmt.Errorf("scenario %s cleanup is required", s.ID)
	}
	validExpected := map[string]bool{
		"success": true, "forbidden": true, "transport_failure": true,
		"auth_failure": true, "cancelled_and_timeout": true,
		"capability_not_implemented": true, "delta_cleared": true,
		"destroyed_without_callback": true,
	}
	if !validExpected[s.ExpectedSDKResult] {
		return fmt.Errorf("scenario %s expected_sdk_result %q is unsupported", s.ID, s.ExpectedSDKResult)
	}
	timeout, err := time.ParseDuration(s.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("scenario %s timeout %q must be a positive duration", s.ID, s.Timeout)
	}
	return nil
}
