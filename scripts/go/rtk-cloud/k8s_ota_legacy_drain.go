package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const otaLegacyReportLateness = 48 * time.Hour

// The core API and the independent OTA process use the same video_cloud DB.
// Match the complete historical object key, including the firmware filename,
// and fail closed if a legacy-prefix row does not have that canonical key.
// A single SELECT gives the gate one database snapshot without changing data.
const otaLegacyDrainQuery = `WITH legacy_releases AS (
  SELECT r.id, r.brand_cloud_id, r.product_id, r.payload->>'object_key' AS object_key
  FROM ota_releases r
  WHERE r.payload->>'object_key' LIKE 'ota/%'
), legacy_campaigns AS (
  SELECT c.* FROM ota_campaigns c JOIN legacy_releases r ON r.id=c.release_id
), legacy_deployments AS (
  SELECT d.* FROM ota_deployments d JOIN legacy_releases r ON r.id=d.release_id
), legacy_grants AS (
  SELECT g.* FROM ota_artifact_grants g JOIN legacy_releases r ON r.id=g.release_id
), activity AS (
  SELECT MAX(updated_at) AS activity_at FROM legacy_campaigns
  UNION ALL SELECT MAX(updated_at) FROM legacy_deployments
  UNION ALL SELECT MAX(expires_at) FROM legacy_grants
  UNION ALL SELECT MAX(e.received_at) FROM ota_deployment_events e JOIN legacy_deployments d ON d.id=e.deployment_id
  UNION ALL SELECT MAX(r.reported_at) FROM ota_download_receipts r JOIN legacy_deployments d ON d.id=r.deployment_id
)
SELECT json_build_object(
  'checked_at', CURRENT_TIMESTAMP,
  'legacy_releases', (SELECT COUNT(*) FROM legacy_releases),
  'invalid_keys', (SELECT COUNT(*) FROM legacy_releases r WHERE r.object_key <> 'ota/' ||
    replace(replace(replace(btrim(r.brand_cloud_id), '/', '-'), chr(92), '-'), '..', '-') || '/' ||
    replace(replace(replace(btrim(r.product_id), '/', '-'), chr(92), '-'), '..', '-') || '/' ||
    r.id || '/firmware.bin'),
  'open_campaigns', (SELECT COUNT(*) FROM legacy_campaigns WHERE state NOT IN ('completed','canceled','archived')),
  'open_deployments', (SELECT COUNT(*) FROM legacy_deployments WHERE status NOT IN
    ('succeeded','failed','canceled','skipped','rolled_back','timed_out')),
  'unexpired_grants', (SELECT COUNT(*) FROM legacy_grants WHERE expires_at > CURRENT_TIMESTAMP),
  'last_activity_at', (SELECT MAX(activity_at) FROM activity)
)::text`

type otaLegacyDrainSnapshot struct {
	CheckedAt       time.Time  `json:"checked_at"`
	LegacyReleases  int64      `json:"legacy_releases"`
	InvalidKeys     int64      `json:"invalid_keys"`
	OpenCampaigns   int64      `json:"open_campaigns"`
	OpenDeployments int64      `json:"open_deployments"`
	UnexpiredGrants int64      `json:"unexpired_grants"`
	LastActivityAt  *time.Time `json:"last_activity_at"`
}

func lkeLegacyOTADrainSnapshot(env map[string]string) (otaLegacyDrainSnapshot, error) {
	out, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "platform"), "exec", "postgresql-0", "--",
		"psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "video_cloud", "-c", otaLegacyDrainQuery)
	if err != nil {
		return otaLegacyDrainSnapshot{}, fmt.Errorf("query live legacy OTA drain state: %w: %s", err, truncateForLog(string(out), 300))
	}
	var snapshot otaLegacyDrainSnapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &snapshot); err != nil {
		return otaLegacyDrainSnapshot{}, fmt.Errorf("decode live legacy OTA drain state: %w", err)
	}
	return snapshot, nil
}

func lkeEvaluateLegacyOTADrain(snapshot otaLegacyDrainSnapshot) error {
	if snapshot.CheckedAt.IsZero() ||
		snapshot.LegacyReleases < 0 || snapshot.InvalidKeys < 0 || snapshot.OpenCampaigns < 0 ||
		snapshot.OpenDeployments < 0 || snapshot.UnexpiredGrants < 0 {
		return fmt.Errorf("legacy OTA drain query returned incomplete state")
	}
	if snapshot.InvalidKeys > 0 {
		return fmt.Errorf("legacy OTA drain has %d noncanonical ota/ object keys", snapshot.InvalidKeys)
	}
	if snapshot.OpenCampaigns > 0 || snapshot.OpenDeployments > 0 {
		return fmt.Errorf("legacy OTA drain requires all ota/ campaigns and deployments closed or canceled: campaigns=%d deployments=%d", snapshot.OpenCampaigns, snapshot.OpenDeployments)
	}
	if snapshot.UnexpiredGrants > 0 {
		return fmt.Errorf("legacy OTA drain has %d unexpired artifact grants", snapshot.UnexpiredGrants)
	}
	if snapshot.LastActivityAt != nil {
		quietUntil := snapshot.LastActivityAt.Add(otaLegacyReportLateness)
		if snapshot.CheckedAt.Before(quietUntil) {
			return fmt.Errorf("legacy OTA drain requires a 48h report-lateness window after the last activity; retry after %s", quietUntil.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// Called after the observed core cutover has finished and before the device
// ingress is changed. Core returns 503 for old device mutations in this interval.
func lkeRequireLegacyOTADrain(env map[string]string) error {
	snapshot, err := lkeLegacyOTADrainSnapshot(env)
	if err != nil {
		return err
	}
	if err := lkeEvaluateLegacyOTADrain(snapshot); err != nil {
		return err
	}
	lastActivity := "none"
	if snapshot.LastActivityAt != nil {
		lastActivity = snapshot.LastActivityAt.UTC().Format(time.RFC3339Nano)
	}
	fmt.Printf("OTA legacy drain verified: legacy_releases=%d invalid_keys=%d open_campaigns=%d open_deployments=%d unexpired_grants=%d checked_at=%s last_activity_at=%s\n",
		snapshot.LegacyReleases, snapshot.InvalidKeys, snapshot.OpenCampaigns, snapshot.OpenDeployments,
		snapshot.UnexpiredGrants, snapshot.CheckedAt.UTC().Format(time.RFC3339Nano), lastActivity)
	return nil
}
