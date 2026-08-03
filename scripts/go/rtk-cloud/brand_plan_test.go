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

func TestLoadTestBrandPlan1KScenarioUsesOneFormalOwner(t *testing.T) {
	path := filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-1k.json")
	plan, err := loadLoadTestBrandPlan(path)
	if err != nil {
		t.Fatalf("loadLoadTestBrandPlan() error = %v", err)
	}
	if len(plan.Brands) != 1 ||
		plan.TotalDevices != 1000 ||
		plan.normalUserCount() != 50 ||
		plan.developerUserCount() != 1 {
		t.Fatalf("1K totals brands=%d devices=%d normal=%d developer=%d",
			len(plan.Brands), plan.TotalDevices, plan.normalUserCount(), plan.developerUserCount())
	}
	brand := plan.Brands[0]
	if brand.Devices != 1000 ||
		brand.NormalUsers != 50 ||
		brand.DeveloperUsers["owner"] != 1 ||
		brand.DeveloperUsers["admin"] != 0 {
		t.Fatalf("brand %+v, want 1000 devices, 50 normal users, one owner, no admin", brand)
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

func TestResolve1KLoadPlanCreatesRunScopedFormalOwner(t *testing.T) {
	source, err := loadLoadTestBrandPlan(filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-1k.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolveLoadTestBrandPlan(source, "1K", "run-1k-20260727", "imap-test01@realtekconnect.com")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target != "1K" || len(plan.Brands) != 1 {
		t.Fatalf("resolved plan metadata = %+v", plan)
	}
	brand := plan.Brands[0]
	if brand.Brandname != "RTK-LOAD-1K-run-1k-20260727-B01" ||
		brand.OwnerEmail != "imap-test01+load-run-1k-20260727-b01@realtekconnect.com" ||
		brand.OwnerName != "RTK Load 1K run-1k-20260727 Brand 01 Owner" ||
		brand.MemberPrefix != "load-run-1k-20260727-b01" {
		t.Fatalf("resolved 1K brand = %+v", brand)
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

func TestEmailOwnerActivationCompletesBeforeEachBrandSetup(t *testing.T) {
	temp := t.TempDir()
	operatorEnv := filepath.Join(temp, "operator.env")
	if err := os.WriteFile(operatorEnv, []byte("IMAP_EMAIL_ADDR=imap-test01@realtekconnect.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(temp, "commands.log")
	makeCommand := func(name string) string {
		path := filepath.Join(temp, name+".sh")
		body := fmt.Sprintf("#!/bin/sh\nprintf '%s %%s\\n' \"$*\" >> %q\n", name, commandLog)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	outDir := filepath.Join(temp, "evidence")
	err := runStagingE2EMultiBrandDataSetup(stagingE2EMultiBrandConfig{
		Workspace:       filepath.Join("..", "..", ".."),
		EnvRoot:         temp,
		BrandPlanFile:   filepath.Join("..", "..", "..", "loadtests", "home-100k", "scenarios", "brand-plan-email-owner-canary.json"),
		DeviceMix:       "camera=2",
		DevicePrefix:    "canary",
		UserConcurrency: 2, DeviceConcurrency: 3, BindConcurrency: 4,
		OutDir: outDir, RunID: "run-20260726", LoadTarget: "CANARY",
		EmailOwners: true, OperatorEnvFile: operatorEnv, NoResume: true,
		Scripts: map[string]string{
			"create-brand":   makeCommand("create-brand"),
			"activate-owner": makeCommand("activate-owner"),
			"create-users":   makeCommand("create-users"),
			"setup-brand":    makeCommand("setup-brand"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if strings.Count(log, "create-brand ") != 2 ||
		strings.Count(log, "activate-owner ") != 2 ||
		strings.Count(log, "setup-brand ") != 2 ||
		strings.Contains(log, "create-users ") {
		t.Fatalf("unexpected command counts:\n%s", log)
	}
	firstCreate := strings.Index(log, "create-brand ")
	firstActivate := strings.Index(log, "activate-owner ")
	firstSetup := strings.Index(log, "setup-brand ")
	secondCreate := strings.LastIndex(log, "create-brand ")
	secondActivate := strings.LastIndex(log, "activate-owner ")
	secondSetup := strings.LastIndex(log, "setup-brand ")
	if !(firstCreate < firstActivate && firstActivate < firstSetup &&
		firstSetup < secondCreate && secondCreate < secondActivate && secondActivate < secondSetup) {
		t.Fatalf("formal activation was not sequenced before synthetic setup:\n%s", log)
	}
	if !strings.Contains(log, "--user-email-domain users.invalid") ||
		!strings.Contains(log, "--user-role member") ||
		!strings.Contains(log, "--user-email-prefix load-run-20260726-b01") ||
		!strings.Contains(log, "--device-prefix load-run-20260726-b01") ||
		!strings.Contains(log, "--no-resume") {
		t.Fatalf("synthetic setup did not use the resolved test-only identity plan:\n%s", log)
	}

	var summary map[string]any
	if err := readJSONFile(filepath.Join(outDir, "summary.json"), &summary); err != nil {
		t.Fatal(err)
	}
	if summary["overall"] != "pass" || asFloat(summary["activated_owners"]) != 2 || asFloat(summary["synthetic_members"]) != 4 {
		t.Fatalf("summary = %+v", summary)
	}
	var resolved loadTestBrandPlan
	if err := readJSONFile(filepath.Join(outDir, "resolved-brand-plan.json"), &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Brands[0].Brandname != "RTK-LOAD-CANARY-run-20260726-B01" ||
		resolved.Brands[1].OwnerEmail != "imap-test01+load-run-20260726-b02@realtekconnect.com" {
		t.Fatalf("resolved plan = %+v", resolved)
	}
}

func TestWriteBulkProvisioningWorkflowEvidenceRequiresEveryDeviceReady(t *testing.T) {
	plan := loadTestBrandPlan{
		TotalDevices: 3,
		Brands: []loadTestBrandConfig{
			{Brandname: "RTK-LOAD-1K-run-B01", Devices: 2},
			{Brandname: "RTK-LOAD-1K-run-B02", Devices: 1},
		},
	}
	dir := t.TempDir()
	for _, brand := range plan.Brands {
		bindDir := filepath.Join(dir, brandSlug(brand.Brandname), "bind-validation")
		if err := os.MkdirAll(bindDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(bindDir, "bulk-device-bind-validation-results.json"), map[string]any{
			"overall": "pass",
			"provisioning": map[string]any{
				"checked": brand.Devices, "ready": brand.Devices, "pending": 0, "failed": 0,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeBulkProvisioningWorkflowEvidence(dir, plan); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Workflow struct {
			WorkflowID string                       `json:"workflow_id"`
			Steps      map[string]string            `json:"steps"`
			Assertions map[string]map[string]string `json:"assertions"`
		} `json:"workflow"`
	}
	if err := readJSONFile(filepath.Join(dir, "bulk-provisioning-workflow.json"), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Workflow.WorkflowID != "WF-PROV-BULK-001" || len(evidence.Workflow.Steps) != 2 ||
		len(evidence.Workflow.Assertions["wait_for_provisioning"]) == 0 {
		t.Fatalf("unexpected bulk workflow evidence: %+v", evidence.Workflow)
	}

	failedPath := filepath.Join(dir, brandSlug(plan.Brands[1].Brandname), "bind-validation", "bulk-device-bind-validation-results.json")
	if err := writeJSON(failedPath, map[string]any{
		"overall": "fail", "provisioning": map[string]any{"checked": 1, "ready": 0, "pending": 0, "failed": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeBulkProvisioningWorkflowEvidence(dir, plan); err == nil {
		t.Fatal("incomplete bulk provisioning evidence was accepted")
	}
}

func TestLoadEmailDevicePrefixIsRunScopedAndOpenBaoSafe(t *testing.T) {
	prefix, err := loadEmailDevicePrefix("run-20260726", "B01")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "load-run-20260726-b01" || len(prefix+"-0001") > 63 {
		t.Fatalf("unsafe device prefix %q", prefix)
	}
	if _, err := loadEmailDevicePrefix("run-20260726-extraordinarily-long-identifier-that-cannot-fit", "B01"); err == nil {
		t.Fatal("overlong OpenBao device label accepted")
	}
	if _, err := loadEmailDevicePrefix("run-20260726", "brand-1"); err == nil {
		t.Fatal("invalid Brand key accepted")
	}
}

func TestResolveLoadTestBrandPlanRejectsInvalidInputs(t *testing.T) {
	base := loadTestBrandPlan{
		TotalDevices: 1, DevicesPerUser: 1,
		Brands: []loadTestBrandConfig{{
			Brandname: "Brand", Devices: 1, NormalUsers: 1,
			DeveloperUsers: map[string]int{"owner": 1},
		}},
	}
	tests := []struct {
		name, target, runID, mailbox string
	}{
		{"short run", "CANARY", "short", "imap@example.test"},
		{"bad target", "25K", "run-12345", "imap@example.test"},
		{"missing local", "CANARY", "run-12345", "@example.test"},
		{"plus mailbox", "CANARY", "run-12345", "imap+old@example.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveLoadTestBrandPlan(base, test.target, test.runID, test.mailbox); err == nil {
				t.Fatal("invalid plan identity accepted")
			}
		})
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
