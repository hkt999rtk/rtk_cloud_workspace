package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type loadTestBrandPlan struct {
	TotalDevices   int                   `json:"total_devices"`
	DevicesPerUser int                   `json:"devices_per_user"`
	Brands         []loadTestBrandConfig `json:"brands"`
}

type loadTestBrandConfig struct {
	Brandname      string         `json:"brandname"`
	Devices        int            `json:"devices"`
	NormalUsers    int            `json:"normal_users"`
	DeveloperUsers map[string]int `json:"developer_users"`
	DeviceMix      map[string]int `json:"device_mix,omitempty"`
}

func loadLoadTestBrandPlan(path string) (loadTestBrandPlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return loadTestBrandPlan{}, err
	}
	var plan loadTestBrandPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return loadTestBrandPlan{}, err
	}
	if err := plan.validate(); err != nil {
		return loadTestBrandPlan{}, fmt.Errorf("%s: %w", path, err)
	}
	return plan, nil
}

func (p loadTestBrandPlan) validate() error {
	if p.TotalDevices <= 0 {
		return fmt.Errorf("total_devices must be positive")
	}
	if p.DevicesPerUser <= 0 {
		return fmt.Errorf("devices_per_user must be positive")
	}
	if len(p.Brands) == 0 {
		return fmt.Errorf("brands must not be empty")
	}
	seen := map[string]bool{}
	totalDevices := 0
	totalUsers := 0
	for _, brand := range p.Brands {
		name := strings.TrimSpace(brand.Brandname)
		if name == "" {
			return fmt.Errorf("brandname must not be empty")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("duplicate brandname %q", name)
		}
		seen[key] = true
		if brand.Devices <= 0 {
			return fmt.Errorf("%s devices must be positive", name)
		}
		if brand.NormalUsers <= 0 {
			return fmt.Errorf("%s normal_users must be positive", name)
		}
		if want := (brand.Devices + p.DevicesPerUser - 1) / p.DevicesPerUser; brand.NormalUsers != want {
			return fmt.Errorf("%s normal_users=%d, want %d", name, brand.NormalUsers, want)
		}
		for role, count := range brand.DeveloperUsers {
			if role != "owner" && role != "admin" {
				return fmt.Errorf("%s developer_users role %q must be owner or admin", name, role)
			}
			if count < 0 {
				return fmt.Errorf("%s developer_users.%s must not be negative", name, role)
			}
		}
		totalDevices += brand.Devices
		totalUsers += brand.NormalUsers
	}
	if totalDevices != p.TotalDevices {
		return fmt.Errorf("brand devices sum to %d, want total_devices %d", totalDevices, p.TotalDevices)
	}
	if totalUsers != (p.TotalDevices+p.DevicesPerUser-1)/p.DevicesPerUser {
		return fmt.Errorf("brand normal_users sum to %d, want %d", totalUsers, (p.TotalDevices+p.DevicesPerUser-1)/p.DevicesPerUser)
	}
	return nil
}

func (p loadTestBrandPlan) developerUserCount() int {
	total := 0
	for _, brand := range p.Brands {
		for _, count := range brand.DeveloperUsers {
			total += count
		}
	}
	return total
}

func (p loadTestBrandPlan) normalUserCount() int {
	total := 0
	for _, brand := range p.Brands {
		total += brand.NormalUsers
	}
	return total
}
