package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var otaStartupTables = []string{"billing_usage_fact_outbox", "ota_task_receipts", "ota_download_receipts", "ota_artifact_grants", "ota_artifact_upload_attempts", "ota_artifact_objects", "ota_artifact_deletion_attempts", "ota_period_barriers", "ota_cdn_period_reviews", "ota_delivery_period_reviews", "ota_producer_period_seals", "ota_releases", "ota_campaigns", "ota_campaign_targets", "ota_deployments"}

func otaSchemaQuery() string {
	var literals []string
	for _, name := range otaStartupTables {
		literals = append(literals, "('"+name+"')")
	}
	return "BEGIN TRANSACTION READ ONLY; SET LOCAL statement_timeout='10s'; SELECT json_build_object('schema',current_schema(),'tables',(SELECT COALESCE(json_agg(required.name),'[]'::json) FROM (VALUES " + strings.Join(literals, ",") + ") AS required(name) WHERE to_regclass('public.'||required.name) IS NULL),'columns',(SELECT COALESCE(json_agg(required.table_name||'.'||required.column_name),'[]'::json) FROM (VALUES ('billing_usage_fact_outbox','fact_usage_id'),('ota_artifact_grants','delivery_channel')) AS required(table_name,column_name) WHERE NOT EXISTS (SELECT 1 FROM information_schema.columns c WHERE c.table_schema='public' AND c.table_name=required.table_name AND c.column_name=required.column_name))); COMMIT;"
}

func checkEnvironmentOTASchema(ctx context.Context, p *deploymentplan.Plan, store secretStore, read func(context.Context, string) (deploymentplan.Object, error), command func(context.Context, ...string) ([]byte, error)) error {
	// This gate covers existing-environment updates. New database creation and
	// owner migration qualification remain independent bootstrap prerequisites.
	if p.Operation != "upgrade" {
		return nil
	}
	otaID, otaNamespace := "", ""
	for _, r := range p.Resources {
		if r.Action != "retire" && otaStartupContainer(r.Desired) != nil {
			otaID = r.ID
			otaNamespace = deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["namespace"])
			break
		}
	}
	if otaID == "" {
		return nil
	}
	fail := func(cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.compatibility.ota-schema", Resource: otaID, Cause: cause, Next: "qualify the original PostgreSQL database and complete any owner schema migration before updating OTA; preserve original data and verify these prerequisites without requiring the future OTA Pod to run"}
	}
	if err := ctx.Err(); err != nil {
		return fail("original OTA schema qualification was interrupted")
	}
	if store.Environment != p.Environment {
		return fail("selected environment database identity is unavailable")
	}
	for _, r := range p.Resources {
		if (r.Phase == "migration" || r.Phase == "migrations") && r.Action != "retain" && r.Action != "retire" && r.Desired["kind"] == "Job" {
			name := deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["name"])
			if deploymentplan.Map(r.Desired["metadata"])["namespace"] == otaNamespace && (strings.Contains(name, "video-cloud") || strings.Contains(name, "ota")) {
				return fail("candidate declares an OTA/database owner migration; its prerequisite boundary needs explicit qualification review before checking future schema objects")
			}
		}
	}
	stack := strings.TrimSuffix(otaNamespace, "-video-cloud")
	if stack != "video-cloud-"+p.Environment {
		return fail("candidate OTA schema target belongs to another environment")
	}
	ns := stack + "-platform"
	stsID := "StatefulSet/" + ns + "/postgresql"
	uid := ""
	for _, r := range p.Resources {
		if r.ID == stsID && r.Action != "retire" {
			uid = r.UID
		}
	}
	if uid == "" {
		return fail("original PostgreSQL owner is unavailable for existing OTA schema qualification")
	}
	owner, err := read(ctx, stsID)
	if err != nil || owner == nil || deploymentplan.Map(owner["metadata"])["uid"] != uid {
		return fail("original PostgreSQL owner cannot be verified")
	}
	podID := "Pod/" + ns + "/postgresql-0"
	pod, err := read(ctx, podID)
	if err != nil || pod == nil || !identityPermissionOwnedBy(deploymentplan.Map(pod["metadata"]), "StatefulSet", uid) || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil {
		return fail("original PostgreSQL query target cannot be verified")
	}
	queryContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := command(queryContext, "--namespace", ns, "exec", "postgresql-0", "--", "env", "PGOPTIONS=-c statement_timeout=10000", "psql", "-U", "postgres", "-d", "video_cloud", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", otaSchemaQuery())
	if err != nil {
		return fail(secretCheckFailure(err, "original OTA schema prerequisites cannot be read").Error())
	}
	var missing struct {
		Schema  string   `json:"schema"`
		Tables  []string `json:"tables"`
		Columns []string `json:"columns"`
	}
	if json.Unmarshal(raw, &missing) != nil || missing.Tables == nil || missing.Columns == nil {
		return fail("original OTA schema prerequisite response is invalid")
	}
	if missing.Schema != "public" {
		return fail("original PostgreSQL session does not prove the candidate's default public schema")
	}
	if len(missing.Tables) > 0 || len(missing.Columns) > 0 {
		return fail(fmt.Sprintf("original PostgreSQL lacks %d required OTA table(s) and %d required column(s)", len(missing.Tables), len(missing.Columns)))
	}
	after, err := read(ctx, podID)
	if err != nil || after == nil || deploymentplan.Map(after["metadata"])["uid"] != deploymentplan.Map(pod["metadata"])["uid"] || deploymentplan.Map(after["metadata"])["deletionTimestamp"] != nil {
		return fail("original PostgreSQL query target changed during schema qualification")
	}
	return nil
}
