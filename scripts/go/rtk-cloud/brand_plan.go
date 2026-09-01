package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	loadRunIDPattern    = regexp.MustCompile(`^[a-z0-9-]{8,64}$`)
	loadBrandKeyPattern = regexp.MustCompile(`^b[0-9]{2}$`)
)

type loadTestBrandPlan struct {
	TotalDevices   int                   `json:"total_devices"`
	DevicesPerUser int                   `json:"devices_per_user"`
	RunID          string                `json:"run_id,omitempty"`
	Target         string                `json:"target,omitempty"`
	Brands         []loadTestBrandConfig `json:"brands"`
}

type loadTestBrandConfig struct {
	Brandname      string         `json:"brandname"`
	Devices        int            `json:"devices"`
	NormalUsers    int            `json:"normal_users"`
	DeveloperUsers map[string]int `json:"developer_users"`
	DeviceMix      map[string]int `json:"device_mix,omitempty"`
	BrandKey       string         `json:"brand_key,omitempty"`
	OwnerEmail     string         `json:"owner_email,omitempty"`
	OwnerName      string         `json:"owner_display_name,omitempty"`
	MemberPrefix   string         `json:"member_email_prefix,omitempty"`
}

func resolveLoadTestBrandPlan(plan loadTestBrandPlan, target, runID, mailbox string) (loadTestBrandPlan, error) {
	runID = strings.ToLower(strings.TrimSpace(runID))
	if !loadRunIDPattern.MatchString(runID) {
		return loadTestBrandPlan{}, fmt.Errorf("run_id must use lowercase letters, digits, and hyphens")
	}
	target = strings.ToUpper(strings.TrimSpace(target))
	if target == "" {
		target = fmt.Sprintf("%dK", plan.TotalDevices/1000)
	}
	if target != "1K" && target != "50K" && target != "100K" && target != "CANARY" {
		return loadTestBrandPlan{}, fmt.Errorf("target must be 1K, 50K, 100K, or CANARY")
	}
	local, domain, err := loadTestMailboxBase(mailbox)
	if err != nil {
		return loadTestBrandPlan{}, err
	}
	resolved := plan
	resolved.RunID = runID
	resolved.Target = target
	resolved.Brands = make([]loadTestBrandConfig, len(plan.Brands))
	for i, source := range plan.Brands {
		brand := source
		brand.BrandKey = fmt.Sprintf("B%02d", i+1)
		brand.Brandname = fmt.Sprintf("RTK-LOAD-%s-%s-%s", target, runID, brand.BrandKey)
		brand.OwnerEmail = fmt.Sprintf("%s+load-%s-b%02d@%s", local, runID, i+1, domain)
		brand.OwnerName = fmt.Sprintf("RTK Load %s %s Brand %02d Owner", target, runID, i+1)
		brand.MemberPrefix = fmt.Sprintf("load-%s-b%02d", runID, i+1)
		resolved.Brands[i] = brand
	}
	return resolved, resolved.validate()
}

func loadTestMailboxBase(mailbox string) (string, string, error) {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	local, domain, ok := strings.Cut(mailbox, "@")
	local, _, _ = strings.Cut(local, "+")
	if !ok {
		return "", "", fmt.Errorf("operator mailbox is missing the @ separator")
	}
	if local == "" {
		return "", "", fmt.Errorf("operator mailbox is missing the local part")
	}
	if domain == "" {
		return "", "", fmt.Errorf("operator mailbox is missing the domain")
	}
	if strings.Contains(domain, "@") {
		return "", "", fmt.Errorf("operator mailbox contains multiple @ separators")
	}
	if strings.ContainsAny(local+domain, " \t\r\n") {
		return "", "", fmt.Errorf("operator mailbox contains whitespace")
	}
	return local, domain, nil
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
