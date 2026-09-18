package main

import (
	"strings"
	"testing"
)

func TestStagingProductAccessGrantsAreLimitedToBoundUserProducts(t *testing.T) {
	users := map[string]userCredential{
		"one@example.test": {UserID: "11111111-1111-4111-8111-111111111111"},
		"two@example.test": {UserID: "22222222-2222-4222-8222-222222222222"},
	}
	assignments := []bindAssignment{
		{AssignedEmail: "one@example.test", DeviceID: "one-a", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{AssignedEmail: "one@example.test", DeviceID: "one-b", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{AssignedEmail: "two@example.test", DeviceID: "two-a", ProductID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
	}
	grants, err := stagingProductAccessGrants(assignments, users)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("grant count = %d, want 2: %#v", len(grants), grants)
	}
	if grants[0].UserID != users["one@example.test"].UserID || grants[0].ProductID != assignments[0].ProductID {
		t.Fatalf("first grant = %#v", grants[0])
	}
	if grants[1].UserID != users["two@example.test"].UserID || grants[1].ProductID != assignments[2].ProductID {
		t.Fatalf("second grant = %#v", grants[1])
	}
}

func TestStagingProductAccessGrantsRejectIncompleteBindingData(t *testing.T) {
	users := map[string]userCredential{"one@example.test": {UserID: "11111111-1111-4111-8111-111111111111"}}
	for name, assignments := range map[string][]bindAssignment{
		"unknown user":    {{AssignedEmail: "missing@example.test", DeviceID: "one-a", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}},
		"missing Product": {{AssignedEmail: "one@example.test", DeviceID: "one-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := stagingProductAccessGrants(assignments, users); err == nil {
				t.Fatal("expected incomplete staging binding data to fail")
			}
		})
	}
	if _, err := databaseNameFromPostgresURL("postgres://host/not-valid-name!"); err == nil {
		t.Fatal("expected unsafe database name to fail")
	}
}

func TestBuildStagingProductAccessSQLIsTransactionalAndOwnerScoped(t *testing.T) {
	cloud := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	owner := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	grants := []stagingProductAccessGrant{{
		UserID: "11111111-1111-4111-8111-111111111111", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}}
	sql, err := buildStagingProductAccessSQL(cloud, owner, grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"BEGIN;", "ON COMMIT DROP", "role='owner'", "role IN ('admin','member')",
		"brand_cloud_product_admissions", "'owner_invitation'", "ON CONFLICT", "COMMIT;",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("SQL missing %q:\n%s", expected, sql)
		}
	}
	if strings.Contains(strings.ToLower(sql), "password") || strings.Contains(strings.ToLower(sql), "token") {
		t.Fatalf("SQL unexpectedly contains credential material: %s", sql)
	}
}

func TestBuildStagingProductAccessSQLRejectsInvalidIDs(t *testing.T) {
	_, err := buildStagingProductAccessSQL("not-a-uuid", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", []stagingProductAccessGrant{{
		UserID: "11111111-1111-4111-8111-111111111111", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}})
	if err == nil {
		t.Fatal("expected invalid Brand Cloud ID to fail")
	}
}

func TestStagingAccountManagerDatabaseNameParsing(t *testing.T) {
	for name, rawURL := range map[string]string{
		"plain":   "postgres://user:secret@postgres.example/rtk_account_manager?sslmode=disable",
		"escaped": "postgres://user:secret@postgres.example/rtk%5Faccount%5Fmanager",
	} {
		t.Run(name, func(t *testing.T) {
			databaseName, err := databaseNameFromPostgresURL(rawURL)
			if err != nil || databaseName != "rtk_account_manager" {
				t.Fatalf("database name = %q, err=%v", databaseName, err)
			}
		})
	}
	if _, err := databaseNameFromPostgresURL("postgres://host/not-valid-name!"); err == nil {
		t.Fatal("expected unsafe database name to fail")
	}
}
