package main

import (
	"fmt"
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

func TestResolveLoadTestBrandPlanUsesFormalOwnerAliasesAndSyntheticDomain(t *testing.T) {
	source, err := loadLoadTestBrandPlan(filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-50k.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolveLoadTestBrandPlan(source, "50K", "run-20260726", "imap-test01@realtekconnect.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Brands) != 5 || plan.RunID != "run-20260726" || plan.Target != "50K" {
		t.Fatalf("resolved plan metadata = %+v", plan)
	}
	seen := map[string]bool{}
	for i, brand := range plan.Brands {
		wantKey := fmt.Sprintf("B%02d", i+1)
		if brand.BrandKey != wantKey ||
			brand.Brandname != fmt.Sprintf("RTK-LOAD-50K-run-20260726-%s", wantKey) ||
			brand.OwnerEmail != fmt.Sprintf("imap-test01+load-run-20260726-b%02d@realtekconnect.com", i+1) ||
			!strings.Contains(brand.OwnerName, "run-20260726") {
			t.Fatalf("resolved brand %d = %+v", i, brand)
		}
		if seen[brand.OwnerEmail] {
			t.Fatalf("duplicate owner alias %q", brand.OwnerEmail)
		}
		seen[brand.OwnerEmail] = true
		member := plannedUsersWithPrefixAndDomain(brand.Brandname, brandSlug(brand.Brandname), "member", 1, brand.MemberPrefix, "users.invalid")[0]
		if got := member["email"]; got != fmt.Sprintf("load-run-20260726-b%02d+001@users.invalid", i+1) {
			t.Fatalf("member email = %v", got)
		}
	}
}

func TestResolveLoadTestBrandPlanRejectsForeignResumeIdentity(t *testing.T) {
	source, err := loadLoadTestBrandPlan(filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-50k.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := resolveLoadTestBrandPlan(source, "50K", "run-20260726", "imap-test01@realtekconnect.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveLoadTestBrandPlan(source, "50K", "run-20260727", "imap-test01@realtekconnect.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.Brands[0].Brandname == second.Brands[0].Brandname || first.Brands[0].OwnerEmail == second.Brands[0].OwnerEmail {
		t.Fatal("different run IDs resolved to reusable brand/owner identities")
	}
}

func TestResolve100KLoadPlanCreatesTenUniqueFormalOwners(t *testing.T) {
	source, err := loadLoadTestBrandPlan(filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-100k.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolveLoadTestBrandPlan(source, "100K", "run-100k-20260726", "imap-test01@realtekconnect.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Brands) != 10 {
		t.Fatalf("brand count = %d, want 10", len(plan.Brands))
	}
	seen := map[string]bool{}
	for _, brand := range plan.Brands {
		if seen[brand.OwnerEmail] {
			t.Fatalf("duplicate formal owner alias %q", brand.OwnerEmail)
		}
		seen[brand.OwnerEmail] = true
	}
}

func TestEmailOwnerActivationFailureStopsBeforeSyntheticMembersAndDevices(t *testing.T) {
	temp := t.TempDir()
	operatorEnv := filepath.Join(temp, "operator.env")
	if err := os.WriteFile(operatorEnv, []byte("IMAP_EMAIL_ADDR=imap-test01@realtekconnect.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(temp, "commands.log")
	makeCommand := func(name string, exitCode int) string {
		path := filepath.Join(temp, name+".sh")
		body := fmt.Sprintf("#!/bin/sh\nprintf '%s\\n' >> %q\nexit %d\n", name, commandLog, exitCode)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	err := runStagingE2EMultiBrandDataSetup(stagingE2EMultiBrandConfig{
		Workspace:       filepath.Join("..", "..", ".."),
		EnvRoot:         temp,
		BrandPlanFile:   filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-email-owner-canary.json"),
		DeviceMix:       "camera=2",
		DevicePrefix:    "canary",
		UserConcurrency: 1, DeviceConcurrency: 1, BindConcurrency: 1,
		OutDir: temp, RunID: "run-20260726", LoadTarget: "CANARY",
		EmailOwners: true, OperatorEnvFile: operatorEnv,
		Scripts: map[string]string{
			"create-brand":   makeCommand("create-brand", 0),
			"activate-owner": makeCommand("activate-owner", 1),
			"create-users":   makeCommand("create-users", 0),
		},
	})
	if err == nil {
		t.Fatal("owner activation failure unexpectedly allowed data setup")
	}
	raw, readErr := os.ReadFile(commandLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	got := string(raw)
	if !strings.Contains(got, "create-brand\nactivate-owner\n") || strings.Contains(got, "create-users") {
		t.Fatalf("command order did not fail before synthetic provisioning:\n%s", got)
	}
}

func TestLoadOwnerEvidenceSchemaDoesNotPersistCredentialsOrActivationURL(t *testing.T) {
	path := filepath.Join("..", "..", "..", "repos", "rtk_cloud_admin", "web", "scripts", "load-owner-activation-live-e2e.mjs")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "schema: 'rtk.load-owner-activation.evidence.v1'")
	if start < 0 {
		t.Fatal("could not locate load-owner evidence object")
	}
	end := strings.Index(source[start:], "})}\\n")
	if end < 0 {
		t.Fatal("could not locate end of load-owner evidence object")
	}
	evidence := source[start : start+end]
	for _, forbidden := range []string{"activationURL", "password", "access_token", "refresh_token", "session_token", "bearer"} {
		if strings.Contains(evidence, forbidden) {
			t.Fatalf("evidence object persists forbidden field %q:\n%s", forbidden, evidence)
		}
	}
	for _, required := range []string{"run_id", "recipient_alias", "imap_uid", "activation_origin", "replay"} {
		if !strings.Contains(evidence, required) {
			t.Fatalf("evidence object is missing %q", required)
		}
	}
}
