package main

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the synthetic fixture uses this reader. It derives process inputs from
// the same admitted parent and written child resource owners; it never creates
// Kubernetes objects, journal entries, images, credentials, or acknowledgements.
type isolatedFoundationAuthority struct {
	PostgresUID, RedisUID                 string
	PostgresImage, RedisImage             string
	PostgresPassword, PostgresInitSQL     string
	PostgresArgs, RedisCommand, RedisArgs []string
}

func isolatedFoundationProcessAuthority(cfg deploymentConfig, store secretStore, objects map[string]deploymentplan.Object) (isolatedFoundationAuthority, error) {
	var out isolatedFoundationAuthority
	bad := func(stage string) error { return errors.New("authority-" + stage) }
	if cfg.Environment != "dev" || store.Environment != cfg.Environment || !isolatedScriptTempPath(store.Root) {
		return out, bad("environment")
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return out, bad("checkpoint")
	}
	original, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, checkpoint.Journal.PlanSHA256, checkpoint.Journal.ArtifactSHA256)
	if err != nil || !original.OpenBaoFirst || configSHA != deploymentplan.Digest(cfg) || original.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return out, bad("admitted-plan")
	}
	var entry *environmentCreateJournalEntry
	for i := range checkpoint.Journal.Entries {
		item := &checkpoint.Journal.Entries[i]
		if item.Key == "kubernetes/foundation-services" {
			if entry != nil || (item.State != "pending" && item.State != "allocated" && item.State != "completed") {
				return out, bad("duplicate-entry")
			}
			entry = item
		}
	}
	if entry == nil {
		return out, bad("missing-entry")
	}
	parent := &environmentCreateExecution{Store: store, owner: checkpoint.Owner, Journal: checkpoint.Journal}
	child, journal, err := originalCreateChildAtIntent(store, parent, *entry)
	if err != nil || child.Environment != cfg.Environment || child.Stack != original.Stack || child.ConfigurationSHA256 != original.ConfigurationSHA256 || child.ReleaseSHA256 != deploymentplan.Digest(original.Release) || journal.PlanSHA256 != child.SHA256() || journal.InFlight != "" || (journal.Failed != "" && !isolatedRedisAcceptedWriteFailure(child, journal)) {
		return out, bad("child-journal")
	}
	ns := original.Stack + "-platform"
	ids := []string{"StatefulSet/" + ns + "/postgresql", "Deployment/" + ns + "/redis", "Secret/" + ns + "/postgresql-runtime", "ConfigMap/" + ns + "/postgresql-initdb"}
	for _, id := range ids {
		actual := objects[id]
		uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
		if uid == "" || uid != journal.Owners[id] {
			return out, bad("resource-owner")
		}
		match := false
		for _, resource := range child.Resources {
			if resource.ID == id && resource.Action == "create" && deploymentplan.Matches(resource.Desired, actual) {
				match = true
				break
			}
		}
		if !match {
			return out, bad("resource-desired")
		}
	}
	postgres, redis := objects[ids[0]], objects[ids[1]]
	image := func(obj deploymentplan.Object, containerName string) string {
		found := ""
		for _, raw := range certIssuerObjectList(environmentPKIPod(obj)["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] == containerName {
				if found != "" {
					return ""
				}
				found = deploymentplan.String(c["image"])
			}
		}
		return found
	}
	out.PostgresImage, out.RedisImage = image(postgres, "postgres"), image(redis, "redis")
	args := func(obj deploymentplan.Object, field string) ([]string, bool) {
		pod := environmentPKIPod(obj)
		containers := certIssuerObjectList(pod["containers"])
		if len(containers) != 1 || len(certIssuerObjectList(pod["initContainers"])) != 0 {
			return nil, false
		}
		var result []string
		for _, value := range certIssuerObjectList(deploymentplan.Map(containers[0])[field]) {
			v, ok := value.(string)
			if !ok || v == "" {
				return nil, false
			}
			result = append(result, v)
		}
		return result, true
	}
	var ok bool
	if out.PostgresArgs, ok = args(postgres, "args"); !ok {
		return isolatedFoundationAuthority{}, bad("postgres-args")
	}
	if out.RedisCommand, ok = args(redis, "command"); !ok {
		return isolatedFoundationAuthority{}, bad("redis-command")
	}
	if out.RedisArgs, ok = args(redis, "args"); !ok {
		return isolatedFoundationAuthority{}, bad("redis-args")
	}
	if len(out.PostgresArgs) != 2 || out.PostgresArgs[0] != "-c" || !strings.HasPrefix(out.PostgresArgs[1], "max_connections=") || strings.Join(out.RedisCommand, " ") != "redis-server" || strings.Join(out.RedisArgs, " ") != "--dir /data --appendonly yes --appendfsync everysec" {
		return isolatedFoundationAuthority{}, bad("command-recipe")
	}
	if out.PostgresImage == "" || out.RedisImage == "" || out.PostgresImage != original.Release.Images["LKE_POSTGRES_IMAGE"].Reference || out.RedisImage != original.Release.Images["LKE_REDIS_IMAGE"].Reference || !immutableEnvironmentImage.MatchString(out.PostgresImage) || !immutableEnvironmentImage.MatchString(out.RedisImage) {
		return isolatedFoundationAuthority{}, bad("image")
	}
	secret := objects[ids[2]]
	out.PostgresPassword = deploymentplan.String(deploymentplan.Map(secret["stringData"])["POSTGRES_PASSWORD"])
	if out.PostgresPassword == "" {
		decoded, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])["POSTGRES_PASSWORD"]))
		if err != nil {
			return isolatedFoundationAuthority{}, bad("secret-data")
		}
		out.PostgresPassword = string(decoded)
	}
	out.PostgresInitSQL = deploymentplan.String(deploymentplan.Map(objects[ids[3]]["data"])["001-create-databases.sql"])
	if out.PostgresPassword == "" || out.PostgresInitSQL == "" {
		return isolatedFoundationAuthority{}, bad("required-input")
	}
	out.PostgresUID = deploymentplan.String(deploymentplan.Map(postgres["metadata"])["uid"])
	out.RedisUID = deploymentplan.String(deploymentplan.Map(redis["metadata"])["uid"])
	return out, nil
}

func TestIsolatedFoundationAuthorityRejectsMissingParent(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	if _, err := isolatedFoundationProcessAuthority(cfg, store, nil); err == nil {
		t.Fatal("missing parent was accepted")
	}
}
