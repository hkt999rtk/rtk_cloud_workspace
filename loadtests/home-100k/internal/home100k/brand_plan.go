package home100k

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type BrandPlan struct {
	TotalDevices   int              `json:"total_devices"`
	DevicesPerUser int              `json:"devices_per_user"`
	Brands         []BrandPlanBrand `json:"brands"`
}

type BrandPlanBrand struct {
	Brandname      string         `json:"brandname"`
	Devices        int            `json:"devices"`
	NormalUsers    int            `json:"normal_users"`
	DeveloperUsers map[string]int `json:"developer_users"`
	DeviceMix      map[string]int `json:"device_mix,omitempty"`
}

type BrandDistributionEntry struct {
	Brandname      string         `json:"brandname"`
	Devices        int            `json:"devices"`
	NormalUsers    int            `json:"normal_users"`
	DeveloperUsers map[string]int `json:"developer_users"`
}

func loadBrandPlan(path string) (BrandPlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return BrandPlan{}, err
	}
	var plan BrandPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return BrandPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return BrandPlan{}, fmt.Errorf("%s: %w", path, err)
	}
	return plan, nil
}

func (p BrandPlan) Validate() error {
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
		if want := ceilDiv(brand.Devices, p.DevicesPerUser); brand.NormalUsers != want {
			return fmt.Errorf("%s normal_users=%d, want ceil(devices/devices_per_user)=%d", name, brand.NormalUsers, want)
		}
		totalDevices += brand.Devices
		totalUsers += brand.NormalUsers
	}
	if totalDevices != p.TotalDevices {
		return fmt.Errorf("brand devices sum to %d, want total_devices %d", totalDevices, p.TotalDevices)
	}
	if totalUsers != ceilDiv(p.TotalDevices, p.DevicesPerUser) {
		return fmt.Errorf("brand normal_users sum to %d, want %d", totalUsers, ceilDiv(p.TotalDevices, p.DevicesPerUser))
	}
	return nil
}

func (p BrandPlan) NormalUsers() int {
	total := 0
	for _, brand := range p.Brands {
		total += brand.NormalUsers
	}
	return total
}

func (p BrandPlan) DeveloperUsers() int {
	total := 0
	for _, brand := range p.Brands {
		for _, count := range brand.DeveloperUsers {
			total += count
		}
	}
	return total
}

func (p BrandPlan) Distribution() []BrandDistributionEntry {
	out := make([]BrandDistributionEntry, 0, len(p.Brands))
	for _, brand := range p.Brands {
		developers := map[string]int{}
		for role, count := range brand.DeveloperUsers {
			developers[role] = count
		}
		out = append(out, BrandDistributionEntry{
			Brandname:      brand.Brandname,
			Devices:        brand.Devices,
			NormalUsers:    brand.NormalUsers,
			DeveloperUsers: developers,
		})
	}
	return out
}
