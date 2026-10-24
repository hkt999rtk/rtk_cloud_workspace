package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var otaStartupCredentialSettings = []string{"POSTGRES_PASSWORD", "VIDEO_CLOUD_AUTH_SECRET", "VIDEO_CLOUD_OTA_BFF_TOKEN", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "VIDEO_CLOUD_BILLING_USAGE_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}

// HTTP Services commonly forward port 80 to a different maintained container
// port. The MQTT helper's equal-port restriction does not apply to this role.
func otaDependencyService(definitions []deploymentplan.Definition, namespace, name, portName string) (string, int, error) {
	var service deploymentplan.Object
	for _, d := range definitions {
		if !d.Retire && deploymentplan.ID(d.Object) == "Service/"+namespace+"/"+name {
			if service != nil {
				return "", 0, fmt.Errorf("ambiguous Service")
			}
			service = d.Object
		}
	}
	if service == nil {
		return "", 0, fmt.Errorf("required Service unavailable")
	}
	ports, err := environmentPKINamedFields(deploymentplan.Map(service["spec"])["ports"])
	if err != nil {
		return "", 0, err
	}
	port := ports[portName]
	n, err := strconv.Atoi(fmt.Sprint(port["port"]))
	if err != nil || n < 1 || n > 65535 || port["protocol"] != nil && port["protocol"] != "TCP" {
		return "", 0, fmt.Errorf("required Service port unavailable")
	}
	return name + "." + namespace + ".svc", n, nil
}

func otaStartupContainer(object deploymentplan.Object) deploymentplan.Object {
	if object["kind"] != "Deployment" || deploymentplan.Map(object["metadata"])["name"] != otaServiceWorkloadName {
		return nil
	}
	for _, raw := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
		if c := deploymentplan.Map(raw); c["name"] == "otaservice" {
			return c
		}
	}
	return nil
}

func deriveEnvironmentOTAStartup(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		c := otaStartupContainer(d.Object)
		if c == nil {
			continue
		}
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		stack := strings.TrimSuffix(ns, "-video-cloud")
		am, ap, err := otaDependencyService(definitions, stack+"-account-manager", "account-manager", "http")
		if err != nil {
			return otaStartupBlocker(deploymentplan.ID(d.Object), "selected Account Manager Product grant Service is unavailable or ambiguous")
		}
		billing, bp, err := otaDependencyService(definitions, stack+"-billing", "billing", "http")
		if err != nil {
			return otaStartupBlocker(deploymentplan.ID(d.Object), "selected Billing fact-delivery Service is unavailable or ambiguous")
		}
		values := []any{deploymentplan.Object{"name": "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL", "value": fmt.Sprintf("http://%s.cluster.local:%d", am, ap)}, deploymentplan.Object{"name": "VIDEO_CLOUD_BILLING_USAGE_ENDPOINT", "value": fmt.Sprintf("http://%s.cluster.local:%d/v1/internal/billing/usage-facts", billing, bp)}}
		for _, pair := range [][2]string{{"VIDEO_CLOUD_OTA_SERVICE_CLIENT_CERT", "client.crt"}, {"VIDEO_CLOUD_OTA_SERVICE_CLIENT_KEY", "client.key"}, {"VIDEO_CLOUD_OTA_SERVICE_SERVER_CA", "server-ca.crt"}} {
			file, err := coreMQTTFile(d.Object, c, "platform-service-identity", pair[1])
			if err != nil || file.kind != "Secret" {
				return otaStartupBlocker(deploymentplan.ID(d.Object), "original OTA registration identity role has no unique read-only Secret file mapping")
			}
			values = append(values, deploymentplan.Object{"name": pair[0], "value": file.file})
		}
		c["env"] = mergeEnvironmentNamedFields(c["env"], values)
	}
	return validateEnvironmentOTAStartup(definitions)
}

func validateEnvironmentOTAStartup(definitions []deploymentplan.Definition) error {
	for _, d := range definitions {
		c := otaStartupContainer(d.Object)
		if c == nil {
			continue
		}
		fail := func(cause string) error { return otaStartupBlocker(deploymentplan.ID(d.Object), cause) }
		settings, err := environmentPKINamedFields(c["env"])
		if err != nil || len(certIssuerObjectList(c["envFrom"])) != 0 {
			return fail("candidate OTA startup settings are ambiguous or depend on an unqualified envFrom source")
		}
		if mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_ENABLED") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "false" || mqttFoundationEnv(c, "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED") != "false" {
			return fail("candidate OTA must be enabled without core MQTT or clip-upload runtime")
		}
		for _, key := range []string{"VIDEO_CLOUD_WEBRTC_SERVICE_ENABLED", "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENABLED"} {
			if value := mqttFoundationEnv(c, key); value != "" && value != "false" {
				return fail("candidate OTA cannot enable another independent service role")
			}
		}
		if mqttFoundationEnv(c, "VIDEO_CLOUD_DB_ENSURE_SCHEMA") != "false" {
			return fail("candidate OTA must consume independently qualified PostgreSQL schema without initializing it at startup")
		}
		for _, key := range []string{"VIDEO_CLOUD_DB_DSN", "VIDEO_CLOUD_BLOB_BUCKET", "VIDEO_CLOUD_BLOB_REGION", "VIDEO_CLOUD_BLOB_PREFIX", "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID", "VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF"} {
			if settings[key]["valueFrom"] != nil || strings.TrimSpace(mqttFoundationEnv(c, key)) == "" {
				return fail("candidate OTA requires explicit derived database/storage/registration input: " + key)
			}
		}
		if !validOTAHTTPSURL(mqttFoundationEnv(c, "VIDEO_CLOUD_BLOB_ENDPOINT")) {
			return fail("candidate OTA requires a provider-derived HTTPS object-storage endpoint without embedded credentials or query data")
		}
		if !validOTAHTTPSURL(mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL")) {
			return fail("candidate OTA registration requires an HTTPS target")
		}
		for _, key := range otaStartupCredentialSettings {
			ref := deploymentplan.Map(deploymentplan.Map(settings[key]["valueFrom"])["secretKeyRef"])
			if ref["optional"] == true || deploymentplan.String(ref["name"]) == "" || deploymentplan.String(ref["key"]) == "" || settings[key]["value"] != nil {
				return fail("candidate OTA must retain its required original Secret reference: " + key)
			}
		}
		if !strings.Contains(mqttFoundationEnv(c, "VIDEO_CLOUD_DB_DSN"), "$(POSTGRES_PASSWORD)") {
			return fail("candidate OTA database configuration must reference its original PostgreSQL password")
		}
		seenPassword := false
		for _, raw := range certIssuerObjectList(c["env"]) {
			s := deploymentplan.Map(raw)
			if s["name"] == "POSTGRES_PASSWORD" {
				seenPassword = true
			}
			if s["name"] == "VIDEO_CLOUD_DB_DSN" && !seenPassword {
				return fail("candidate OTA PostgreSQL password must be defined before its derived DSN")
			}
		}
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		stack := strings.TrimSuffix(ns, "-video-cloud")
		pg, pp, pgErr := otaDependencyService(definitions, stack+"-platform", "postgresql", "postgres")
		dsn, dsnErr := url.Parse(mqttFoundationEnv(c, "VIDEO_CLOUD_DB_DSN"))
		password, present := "", false
		if dsnErr == nil && dsn.User != nil {
			password, present = dsn.User.Password()
		}
		if pgErr != nil || dsnErr != nil || dsn.User == nil || dsn.User.Username() != "postgres" || !present || password != "$(POSTGRES_PASSWORD)" || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") || (dsn.Hostname() != pg && dsn.Hostname() != pg+".cluster.local") || dsn.Port() != fmt.Sprint(pp) || dsn.Path != "/video_cloud" || dsn.Fragment != "" {
			return fail("candidate OTA DSN does not select the original PostgreSQL Service, user and video_cloud database qualified by the schema check")
		}
		for key := range dsn.Query() {
			if key != "sslmode" {
				return fail("candidate OTA DSN has unsupported schema or connection overrides")
			}
		}
		for _, endpoint := range []struct{ key, namespace, name, port, path string }{{"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL", stack + "-account-manager", "account-manager", "http", ""}, {"VIDEO_CLOUD_BILLING_USAGE_ENDPOINT", stack + "-billing", "billing", "http", "/v1/internal/billing/usage-facts"}} {
			host, port, err := otaDependencyService(definitions, endpoint.namespace, endpoint.name, endpoint.port)
			u, urlErr := url.Parse(mqttFoundationEnv(c, endpoint.key))
			if err != nil || urlErr != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" || (u.Hostname() != host && u.Hostname() != host+".cluster.local") || u.Port() != fmt.Sprint(port) || u.Path != endpoint.path {
				return fail("candidate OTA dependency endpoint differs from its selected maintained Service: " + endpoint.key)
			}
		}
		for _, pair := range [][2]string{{"VIDEO_CLOUD_OTA_SERVICE_CLIENT_CERT", "client.crt"}, {"VIDEO_CLOUD_OTA_SERVICE_CLIENT_KEY", "client.key"}, {"VIDEO_CLOUD_OTA_SERVICE_SERVER_CA", "server-ca.crt"}} {
			file, err := coreMQTTFile(d.Object, c, "platform-service-identity", pair[1])
			if err != nil || file.kind != "Secret" || mqttFoundationEnv(c, pair[0]) != file.file {
				return fail("candidate OTA registration path differs from its original read-only identity mount")
			}
		}
		if raw := mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_REPORT_LATENESS"); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 || value > 48*time.Hour {
				return fail("candidate OTA report lateness must be positive and at most 48 hours")
			}
		}
		cdn, key := mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_CDN_BASE_URL"), settings["VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX"]
		if cdn != "" && (!validOTAHTTPSURL(cdn) || deploymentplan.Map(deploymentplan.Map(key["valueFrom"])["secretKeyRef"])["name"] == nil) || cdn == "" && (key["valueFrom"] != nil || mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX") != "") {
			return fail("candidate OTA CDN endpoint and original token-key Secret must be configured together")
		}
	}
	return nil
}

func checkEnvironmentOTAStartup(ctx context.Context, p *deploymentplan.Plan, read func(context.Context, string) (deploymentplan.Object, error), operator map[string]string) error {
	var definitions []deploymentplan.Definition
	resources := map[string]deploymentplan.Resource{}
	for _, r := range p.Resources {
		if r.Action != "retire" {
			definitions = append(definitions, deploymentplan.Definition{Object: r.Desired})
			resources[r.ID] = r
		}
	}
	if err := validateEnvironmentOTAStartup(definitions); err != nil {
		return err
	}
	checked := map[string]deploymentplan.Object{}
	for _, r := range p.Resources {
		c := otaStartupContainer(r.Desired)
		if c == nil || r.Action == "retire" {
			continue
		}
		settings, _ := environmentPKINamedFields(c["env"])
		old := otaStartupContainer(r.Baseline)
		original, _ := environmentPKINamedFields(old["env"])
		fail := func(cause string) error { return otaStartupBlocker(r.ID, cause) }
		refs := map[string][]string{}
		for _, key := range otaStartupCredentialSettings {
			if r.UID != "" && deploymentplan.Digest(settings[key]["valueFrom"]) != deploymentplan.Digest(original[key]["valueFrom"]) {
				return fail("candidate OTA changes an original PostgreSQL/storage/runtime credential reference")
			}
			ref := deploymentplan.Map(deploymentplan.Map(settings[key]["valueFrom"])["secretKeyRef"])
			id := "Secret/" + deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["namespace"]) + "/" + deploymentplan.String(ref["name"])
			refs[id] = append(refs[id], deploymentplan.String(ref["key"]))
		}
		for _, fileKey := range []string{"client.crt", "client.key", "server-ca.crt"} {
			file, _ := coreMQTTFile(r.Desired, c, "platform-service-identity", fileKey)
			id := "Secret/" + deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["namespace"]) + "/" + file.reference
			refs[id] = append(refs[id], file.key)
		}
		ids := make([]string, 0, len(refs))
		for id := range refs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			source := resources[id]
			if source.UID == "" {
				return fail("original OTA startup Secret source owner is unavailable")
			}
			actual := checked[id]
			if actual == nil {
				var err error
				actual, err = read(ctx, id)
				if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != source.UID {
					return fail("original OTA startup Secret source owner cannot be verified")
				}
				checked[id] = actual
			}
			for _, key := range refs[id] {
				value, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(actual["data"])[key]))
				if err != nil || len(strings.TrimSpace(string(value))) == 0 {
					return fail("original OTA startup Secret source lacks a required nonempty role key")
				}
				if (key == "AWS_ACCESS_KEY_ID" || key == "AWS_SECRET_ACCESS_KEY") && operator[map[string]string{"AWS_ACCESS_KEY_ID": "LINODE_OTA_OBJ_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY": "LINODE_OTA_OBJ_SECRET_ACCESS_KEY"}[key]] != string(value) {
					return fail("original OTA storage Secret differs from the selected canonical dedicated credentials")
				}
			}
		}
	}
	return nil
}
