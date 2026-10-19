package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentMediaObservationPath = "deployment/storage/media-render-observation.json"
const environmentVideoStorageObjectStorageSecret = "video-storage-object-storage"

func videoStorageStartupBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.video-storage-startup", Resource: id, Cause: cause, Next: "restore the enabled independent VideoStorage role's original media allocation and credential/path references; derive its required runtime settings from genuine existing-provider metadata, preserve Core settings, and repeat preflight; do not create a bucket, rotate keys, perform cutover, or require future workload readiness"}
}

// Media and OTA observations use the same public allocated-provider shape.
// These records never represent object-write, migration or activation receipts.
func validateMediaStorageObservation(cfg deploymentConfig, o environmentOTAStorageObservation) error {
	t := cfg.Storage.RuntimeMedia
	if o.Version != 1 || o.Environment != cfg.Environment || o.Provider != "linode" || t.Bucket == "" || t.Region == "" || t.Prefix == "" || o.Bucket != t.Bucket || o.Region != t.Region || o.Prefix != t.Prefix || !validOTAHTTPSURL(o.Endpoint) {
		return videoStorageStartupBlocker("", "existing media allocation observation does not match the selected maintained target")
	}
	return nil
}

func readMediaStorageObservation(cfg deploymentConfig, store secretStore) (environmentOTAStorageObservation, error) {
	var record environmentOTAStorageRecord
	path, err := privateDeploymentStatePath(store.Environment, environmentMediaObservationPath)
	if err != nil || store.Environment != cfg.Environment {
		return record.Observation, videoStorageStartupBlocker("", "canonical media allocation observation path is unsafe")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		if _, original := os.Lstat(path); errors.Is(original, os.ErrNotExist) {
			return record.Observation, nil
		}
		return record.Observation, videoStorageStartupBlocker("", "canonical media allocation observation is not a bounded private regular file")
	}
	if decodeEnvironmentJSON(raw, &record) != nil || record.SHA256 != deploymentplan.Digest(record.Observation) {
		return record.Observation, videoStorageStartupBlocker("", "canonical media allocation observation is malformed or its projection hash differs")
	}
	if _, err := time.Parse(time.RFC3339, record.ObservedAt); err != nil {
		return record.Observation, videoStorageStartupBlocker("", "canonical media allocation observation lacks a genuine observation time")
	}
	return record.Observation, validateMediaStorageObservation(cfg, record.Observation)
}

func getMediaStorageObservation(ctx context.Context, cfg deploymentConfig, operator map[string]string, checker deploymentCredentialChecker) (environmentOTAStorageObservation, error) {
	var o environmentOTAStorageObservation
	if operator["LINODE_TOKEN"] == "" || ctx.Err() != nil {
		return o, videoStorageStartupBlocker("", "original provider metadata access is unavailable or interrupted")
	}
	checker = checker.withCheckContext(ctx)
	checker.readOnly, checker.out, checker.session = true, io.Discard, nil
	bucket, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], cfg.Storage.RuntimeMedia)
	if err != nil {
		return o, videoStorageStartupBlocker("", "the original selected media bucket cannot be read; preflight cannot create or replace it")
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return o, videoStorageStartupBlocker("", "original media allocation has no valid provider-derived HTTPS endpoint")
	}
	o = environmentOTAStorageObservation{Version: 1, Environment: cfg.Environment, Provider: "linode", Bucket: bucket.Label, Region: firstNonEmpty(bucket.Region, bucket.Cluster), Prefix: cfg.Storage.RuntimeMedia.Prefix, Endpoint: endpoint, EndpointType: bucket.EndpointType}
	return o, validateMediaStorageObservation(cfg, o)
}

func observeMediaStorageForRender(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string, checker deploymentCredentialChecker) (environmentOTAStorageObservation, error) {
	o, err := getMediaStorageObservation(ctx, cfg, operator, checker)
	if err != nil {
		return o, err
	}
	previous, err := readMediaStorageObservation(cfg, store)
	if err != nil {
		return o, err
	}
	if previous.Version != 0 {
		if previous != o {
			return o, videoStorageStartupBlocker("", "original recorded media allocation changed; explicit storage lifecycle qualification is required")
		}
		return o, nil // Fresh GET; retain the stable original record and timestamp.
	}
	record := environmentOTAStorageRecord{Observation: o, SHA256: deploymentplan.Digest(o), ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(record)
	if err := writePrivateDeploymentState(store.Environment, environmentMediaObservationPath, append(raw, '\n')); err != nil {
		return o, videoStorageStartupBlocker("", "original media allocation observation cannot be recorded in the canonical private store")
	}
	return o, nil
}

func projectVideoStorageRenderInputs(cfg deploymentConfig, o environmentOTAStorageObservation, values map[string]string) error {
	if cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return nil
	}
	if err := validateMediaStorageObservation(cfg, o); err != nil {
		return err
	}
	for key, value := range map[string]string{"BUCKET": o.Bucket, "REGION": o.Region, "PREFIX": o.Prefix, "ENDPOINT": o.Endpoint} {
		values["VIDEO_CLOUD_VIDEO_STORAGE_BLOB_"+key] = value
	}
	return nil
}

func videoStorageStartupContainer(o deploymentplan.Object) deploymentplan.Object {
	if o["kind"] != "Deployment" || deploymentplan.Map(o["metadata"])["name"] != "video-cloud-videostorage" {
		return nil
	}
	for _, raw := range certIssuerObjectList(environmentPKIPod(o)["containers"]) {
		if c := deploymentplan.Map(raw); c["name"] == "videostorage" {
			return c
		}
	}
	return nil
}

// The independent role's bucket-scoped credentials are a separate mirror.
// Core, clip workers and their shared runtime Secret retain their original
// credential lifecycle. Both the fresh intent and executable catalog use this
// same explicit dependency before native identities or Secret owners exist.
func bindEnvironmentVideoStorageObjectStorage(definitions []deploymentplan.Definition) {
	for _, d := range definitions {
		c := videoStorageStartupContainer(d.Object)
		if c == nil || d.Retire {
			continue
		}
		var settings []any
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			settings = append(settings, deploymentplan.Object{"name": key, "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": environmentVideoStorageObjectStorageSecret, "key": key}}})
		}
		c["env"] = mergeEnvironmentNamedFields(c["env"], settings)
	}
}

func deriveEnvironmentVideoStorageStartup(values map[string]string, definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		c := videoStorageStartupContainer(d.Object)
		if c == nil || d.Retire {
			continue
		}
		var settings []any
		for _, key := range []string{"BUCKET", "REGION", "PREFIX", "ENDPOINT"} {
			settings = append(settings, deploymentplan.Object{"name": "VIDEO_CLOUD_BLOB_" + key, "value": values["VIDEO_CLOUD_VIDEO_STORAGE_BLOB_"+key]})
		}
		settings = append(settings, deploymentplan.Object{"name": "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "value": "true"})
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		stack := strings.TrimSuffix(ns, "-video-cloud")
		host, port, err := otaDependencyService(definitions, stack+"-account-manager", "account-manager", "http")
		if err != nil {
			return videoStorageStartupBlocker(deploymentplan.ID(d.Object), "selected original AM HTTP Service is unavailable")
		}
		settings = append(settings, deploymentplan.Object{"name": "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL", "value": "http://" + host + ".cluster.local:" + strconv.Itoa(port)})
		c["env"] = mergeEnvironmentNamedFields(c["env"], settings)
	}
	return validateEnvironmentVideoStorageStartup(definitions)
}

func validateEnvironmentVideoStorageStartup(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		c := videoStorageStartupContainer(d.Object)
		if c == nil || d.Retire {
			continue
		}
		fail := func(cause string) error { return videoStorageStartupBlocker(deploymentplan.ID(d.Object), cause) }
		settings, err := environmentPKINamedFields(c["env"])
		if err != nil || len(certIssuerObjectList(c["envFrom"])) != 0 {
			return fail("candidate VideoStorage settings are ambiguous")
		}
		if mqttFoundationEnv(c, "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENABLED") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "false" || mqttFoundationEnv(c, "VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED") != "false" || mqttFoundationEnv(c, "VIDEO_CLOUD_DB_ENSURE_SCHEMA") != "false" {
			return fail("candidate independent VideoStorage must retain direct S3 upload without Core MQTT, WebRTC or schema initialization")
		}
		if value := mqttFoundationEnv(c, "VIDEO_CLOUD_WEBRTC_SERVICE_ENABLED"); value != "" && value != "false" {
			return fail("candidate VideoStorage cannot enable the independent WebRTC service role")
		}
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		stack := strings.TrimSuffix(ns, "-video-cloud")
		dbHost, dbPort, dbErr := otaDependencyService(definitions, stack+"-platform", "postgresql", "postgres")
		dsn, dsnErr := url.Parse(mqttFoundationEnv(c, "VIDEO_CLOUD_DB_DSN"))
		password, present := "", false
		if dsnErr == nil && dsn.User != nil {
			password, present = dsn.User.Password()
		}
		if dbErr != nil || dsnErr != nil || dsn.User == nil || dsn.User.Username() != "postgres" || !present || password != "$(POSTGRES_PASSWORD)" || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") || (dsn.Hostname() != dbHost && dsn.Hostname() != dbHost+".cluster.local") || dsn.Port() != strconv.Itoa(dbPort) || dsn.Path != "/video_cloud" || dsn.Fragment != "" {
			return fail("candidate VideoStorage DSN does not select its original PostgreSQL Service/database/password reference")
		}
		query, err := url.ParseQuery(dsn.RawQuery)
		if err != nil {
			return fail("candidate VideoStorage DSN query is invalid")
		}
		for key := range query {
			if key != "sslmode" {
				return fail("candidate VideoStorage DSN cannot override schema/session state")
			}
		}
		seenPassword := false
		for _, raw := range certIssuerObjectList(c["env"]) {
			setting := deploymentplan.Map(raw)
			if setting["name"] == "POSTGRES_PASSWORD" {
				seenPassword = true
			}
			if setting["name"] == "VIDEO_CLOUD_DB_DSN" && !seenPassword {
				return fail("original PostgreSQL password must precede the derived VideoStorage DSN")
			}
		}
		for _, endpoint := range []struct{ key, scheme, port string }{{"VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL", "https", "service-reg"}, {"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL", "http", "http"}} {
			host, port, err := otaDependencyService(definitions, stack+"-account-manager", "account-manager", endpoint.port)
			u, urlErr := url.Parse(mqttFoundationEnv(c, endpoint.key))
			if err != nil || urlErr != nil || u.Scheme != endpoint.scheme || (u.Hostname() != host && u.Hostname() != host+".cluster.local") || u.Port() != strconv.Itoa(port) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return fail("candidate VideoStorage dependency URL differs from the selected original AM Service")
			}
		}
		if !otaRegistrationInstancePattern.MatchString(mqttFoundationEnv(c, "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID")) {
			return fail("candidate VideoStorage registration instance is invalid")
		}
		for _, key := range []string{"VIDEO_CLOUD_ENV", "VIDEO_CLOUD_DB_DSN", "VIDEO_CLOUD_BLOB_BUCKET", "VIDEO_CLOUD_BLOB_REGION", "VIDEO_CLOUD_BLOB_PREFIX", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENDPOINT_REF"} {
			if settings[key]["valueFrom"] != nil || mqttFoundationEnv(c, key) == "" {
				return fail("candidate VideoStorage lacks a required derived startup input: " + key)
			}
		}
		if !validOTAHTTPSURL(mqttFoundationEnv(c, "VIDEO_CLOUD_BLOB_ENDPOINT")) || !validOTAHTTPSURL(mqttFoundationEnv(c, "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL")) {
			return fail("candidate VideoStorage requires original HTTPS storage and registration targets")
		}
		for _, key := range []string{"POSTGRES_PASSWORD", "VIDEO_CLOUD_AUTH_SECRET", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			ref := deploymentplan.Map(deploymentplan.Map(settings[key]["valueFrom"])["secretKeyRef"])
			if settings[key]["value"] != nil || ref["optional"] == true || deploymentplan.String(ref["name"]) == "" || deploymentplan.String(ref["key"]) == "" {
				return fail("candidate VideoStorage lacks a required original Secret reference: " + key)
			}
			if (key == "AWS_ACCESS_KEY_ID" || key == "AWS_SECRET_ACCESS_KEY") && (ref["name"] != environmentVideoStorageObjectStorageSecret || ref["key"] != key) {
				return fail("candidate VideoStorage must use its dedicated media credential mirror: " + key)
			}
		}
		for _, pair := range [][3]string{{"VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_CERT", "platform-service-identity", "client.crt"}, {"VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_KEY", "platform-service-identity", "client.key"}, {"VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_SERVER_CA", "platform-service-identity", "server-ca.crt"}, {"VIDEO_CLOUD_CLIP_PRIVATE_KEY_PATH", "clip-crypto", "clip-private-key.pem"}} {
			file, err := coreMQTTFile(d.Object, c, pair[1], pair[2])
			if err != nil || file.kind != "Secret" || mqttFoundationEnv(c, pair[0]) != file.file {
				return fail("candidate VideoStorage file does not map to its original read-only Secret path")
			}
		}
	}
	return nil
}

func checkEnvironmentVideoStorageStartup(ctx context.Context, p *deploymentplan.Plan, cfg deploymentConfig, store secretStore, read func(context.Context, string) (deploymentplan.Object, error), operator map[string]string, checker deploymentCredentialChecker) error {
	var definitions []deploymentplan.Definition
	resources := map[string]deploymentplan.Resource{}
	active := false
	for _, r := range p.Resources {
		resources[r.ID] = r
		if r.Action != "retire" {
			definitions = append(definitions, deploymentplan.Definition{Object: r.Desired})
			active = active || videoStorageStartupContainer(r.Desired) != nil
		}
	}
	if !active {
		return nil
	}
	if err := validateEnvironmentVideoStorageStartup(definitions); err != nil {
		return err
	}
	if p.Environment != cfg.Environment || p.Stack != "video-cloud-"+cfg.Environment || store.Environment != cfg.Environment {
		return videoStorageStartupBlocker("", "candidate media allocation belongs to another environment")
	}
	o, err := readMediaStorageObservation(cfg, store)
	if err != nil {
		return err
	}
	if err := validateMediaStorageObservation(cfg, o); err != nil {
		return err
	}
	fresh, err := getMediaStorageObservation(ctx, cfg, operator, checker)
	if err != nil {
		return err
	}
	if fresh != o {
		return videoStorageStartupBlocker("", "original media allocation changed after compilation")
	}
	allocationSettings := map[string]string{"VIDEO_CLOUD_BLOB_BUCKET": o.Bucket, "VIDEO_CLOUD_BLOB_REGION": o.Region, "VIDEO_CLOUD_BLOB_PREFIX": o.Prefix, "VIDEO_CLOUD_BLOB_ENDPOINT": o.Endpoint, "VIDEO_CLOUD_ENV": cfg.Environment}
	for _, r := range p.Resources {
		c := videoStorageStartupContainer(r.Desired)
		if c == nil || r.Action == "retire" {
			continue
		}
		for key, expected := range allocationSettings {
			if mqttFoundationEnv(c, key) != expected {
				return videoStorageStartupBlocker(r.ID, "candidate VideoStorage allocation differs from frozen original provider metadata")
			}
		}
	}
	// Bootstrap ownership and credentials are qualified by the existing create
	// flow. Future Secret/workload UIDs cannot be prerequisites for that flow.
	if p.Operation != "upgrade" {
		return nil
	}
	for _, r := range p.Resources {
		c := videoStorageStartupContainer(r.Desired)
		if c == nil || r.Action == "retire" {
			continue
		}
		fail := func(cause string) error { return videoStorageStartupBlocker(r.ID, cause) }
		old := videoStorageStartupContainer(r.Baseline)
		if r.UID != "" {
			for _, key := range []string{"VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENDPOINT_REF"} {
				if old == nil || mqttFoundationEnv(old, key) == "" || mqttFoundationEnv(old, key) != mqttFoundationEnv(c, key) {
					return fail("candidate VideoStorage does not retain its original approved registration instance/endpoint")
				}
			}
		}
		for key, expected := range allocationSettings {
			if key != "VIDEO_CLOUD_ENV" && old != nil {
				previous := mqttFoundationEnv(old, key)
				if previous != "" && strings.TrimRight(previous, "/") != strings.TrimRight(expected, "/") {
					return fail("candidate VideoStorage would switch an original nonempty media allocation; explicit storage lifecycle review is required")
				}
			}
		}
		settings, _ := environmentPKINamedFields(c["env"])
		original, _ := environmentPKINamedFields(old["env"])
		refs := map[string][]string{}
		for _, key := range []string{"POSTGRES_PASSWORD", "VIDEO_CLOUD_AUTH_SECRET", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			if r.UID != "" && deploymentplan.Digest(settings[key]["valueFrom"]) != deploymentplan.Digest(original[key]["valueFrom"]) {
				return fail("candidate VideoStorage changes an original credential reference")
			}
			ref := deploymentplan.Map(deploymentplan.Map(settings[key]["valueFrom"])["secretKeyRef"])
			id := "Secret/" + p.Stack + "-video-cloud/" + deploymentplan.String(ref["name"])
			refs[id] = append(refs[id], deploymentplan.String(ref["key"]))
		}
		for _, pair := range [][2]string{{"platform-service-identity", "client.crt"}, {"platform-service-identity", "client.key"}, {"platform-service-identity", "server-ca.crt"}, {"clip-crypto", "clip-private-key.pem"}} {
			file, _ := coreMQTTFile(r.Desired, c, pair[0], pair[1])
			if old != nil {
				before, e := coreMQTTFile(r.Baseline, old, pair[0], pair[1])
				if e != nil || before != file {
					return fail("candidate VideoStorage changes an original identity or clip-key path/reference")
				}
			}
			id := "Secret/" + p.Stack + "-video-cloud/" + file.reference
			refs[id] = append(refs[id], file.key)
		}
		for id, keys := range refs {
			resource := resources[id]
			if resource.UID == "" {
				return fail("original VideoStorage Secret owner is unavailable")
			}
			actual, err := read(ctx, id)
			if err != nil {
				return fail(secretCheckFailure(err, "original VideoStorage Secret cannot be read").Error())
			}
			if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil {
				return fail("original VideoStorage Secret owner cannot be verified")
			}
			for _, key := range keys {
				value, e := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(actual["data"])[key]))
				if e != nil || len(value) == 0 || deploymentplan.Digest(deploymentplan.Map(actual["data"])[key]) != deploymentplan.Digest(deploymentplan.Map(resource.Baseline["data"])[key]) {
					return fail("original VideoStorage Secret role key is absent or changed")
				}
			}
		}
	}
	return nil
}
