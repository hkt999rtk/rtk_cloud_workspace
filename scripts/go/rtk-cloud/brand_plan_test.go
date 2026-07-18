package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTestBrandPlanValidatesTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brand-plan.json")
	if err := os.WriteFile(path, []byte(`{
		"total_devices": 3,
		"devices_per_user": 1,
		"brands": [
			{"brandname":"RTK-A","devices":2,"normal_users":2,"developer_users":{"owner":1,"admin":1}},
			{"brandname":"RTK-B","devices":1,"normal_users":1,"developer_users":{"owner":1,"admin":1}}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := loadLoadTestBrandPlan(path)
	if err != nil {
		t.Fatalf("loadLoadTestBrandPlan() error = %v", err)
	}
	if plan.normalUserCount() != 3 || plan.developerUserCount() != 4 {
		t.Fatalf("counts normal=%d developer=%d", plan.normalUserCount(), plan.developerUserCount())
	}
}

func TestLoadTestBrandPlan100KScenarioUsesTenBrandClouds(t *testing.T) {
	path := filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-100k.json")
	plan, err := loadLoadTestBrandPlan(path)
	if err != nil {
		t.Fatalf("loadLoadTestBrandPlan() error = %v", err)
	}
	if len(plan.Brands) != 10 {
		t.Fatalf("brand count = %d, want 10", len(plan.Brands))
	}
	if plan.TotalDevices != 100000 || plan.normalUserCount() != 5000 || plan.developerUserCount() != 10 {
		t.Fatalf("totals devices=%d normal=%d developer=%d", plan.TotalDevices, plan.normalUserCount(), plan.developerUserCount())
	}
	for _, brand := range plan.Brands {
		if brand.Devices != 10000 || brand.NormalUsers != 500 || brand.DeveloperUsers["owner"] != 1 || brand.DeveloperUsers["admin"] != 0 {
			t.Fatalf("brand %+v, want 10000 devices, 500 normal users, one owner, no admin", brand)
		}
	}
}

func TestPlannedUsersKeepsMemberEmailAndSeparatesDeveloperRoles(t *testing.T) {
	member := plannedUsers("RTK Primary", "rtk-primary", "member", 1)[0]["email"].(string)
	owner := plannedUsers("RTK Primary", "rtk-primary", "owner", 1)[0]["email"].(string)
	admin := plannedUsers("RTK Primary", "rtk-primary", "admin", 1)[0]["email"].(string)
	if member != "rtk-primary+001@users.local" {
		t.Fatalf("member email = %q", member)
	}
	if !strings.Contains(owner, "+owner-001@") || !strings.Contains(admin, "+admin-001@") {
		t.Fatalf("developer emails owner=%q admin=%q", owner, admin)
	}
	if owner == admin || owner == member || admin == member {
		t.Fatalf("role emails should be unique: member=%q owner=%q admin=%q", member, owner, admin)
	}
}

func TestPlannedUsersWithPrefixUsesRunScopedAddress(t *testing.T) {
	user := plannedUsersWithPrefix("SDK E2E iOS", "sdk-e2e-ios", "member", 1, "sdk-ios-run-123")[0]
	if got, want := user["email"], "sdk-ios-run-123+001@users.local"; got != want {
		t.Fatalf("email = %v, want %s", got, want)
	}
}
