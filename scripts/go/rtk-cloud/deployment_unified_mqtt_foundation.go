package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strconv"
	"time"
)

func mqttFoundationBlocker(id, resource, cause string) error {
	return &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: "maintain shared MQTT Foundation and Account Manager registration settings; retain the original registrar instance, identity Secret and listener TLS/CRL references; repair the named dependency and repeat full preflight without replacing credentials"}
}
func validateMQTTFoundationConfiguration(values map[string]string) error {
	for _, key := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"} {
		if values[key] != "true" && values[key] != "false" {
			return mqttFoundationBlocker("deployment.mqtt-foundation.configuration", key, "shared enablement must explicitly be true or false")
		}
	}
	if values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] == "true" && values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return mqttFoundationBlocker("deployment.mqtt-foundation.configuration", "account-manager", "MQTT Foundation requires the Account Manager private registration listener")
	}
	port, err := strconv.Atoi(values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"])
	if err != nil || port < 1 || port > 65535 {
		return mqttFoundationBlocker("deployment.mqtt-foundation.configuration", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT", "maintained registration listener port must be between 1 and 65535")
	}
	return nil
}
func mqttFoundationTarget(definitions []deploymentplan.Definition, kind, name string) deploymentplan.Object {
	for _, d := range definitions {
		if d.Object["kind"] == kind && deploymentplan.Map(d.Object["metadata"])["name"] == name {
			return d.Object
		}
	}
	return nil
}
func mqttFoundationPrimary(object deploymentplan.Object) map[string]any {
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) == 0 {
		return nil
	}
	return deploymentplan.Map(containers[0])
}
func mqttFoundationEnv(container map[string]any, name string) string {
	for _, v := range certIssuerObjectList(container["env"]) {
		setting := deploymentplan.Map(v)
		if setting["name"] == name {
			return deploymentplan.String(setting["value"])
		}
	}
	return ""
}
func mqttFoundationMountedSecret(object deploymentplan.Object, container map[string]any, path string) string {
	for _, v := range certIssuerObjectList(container["volumeMounts"]) {
		mount := deploymentplan.Map(v)
		if mount["mountPath"] == path && mount["readOnly"] == true {
			for _, v := range certIssuerObjectList(environmentPKIPod(object)["volumes"]) {
				volume := deploymentplan.Map(v)
				if volume["name"] == mount["name"] {
					return deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"])
				}
			}
		}
	}
	return ""
}
func validateMQTTFoundationTarget(env map[string]string, definitions []deploymentplan.Definition) error {
	foundation := mqttFoundationTarget(definitions, "Deployment", "video-cloud-mqttfoundation")
	account := mqttFoundationTarget(definitions, "Deployment", "account-manager")
	registrar, listener := mqttFoundationPrimary(foundation), mqttFoundationPrimary(account)
	block := func(cause string) error {
		return mqttFoundationBlocker("deployment.mqtt-foundation.dependencies", "video-cloud-mqttfoundation", cause)
	}
	if registrar == nil || registrar["name"] != "mqttfoundation" || listener == nil {
		return block("registrar or Account Manager target is absent")
	}
	if deploymentplan.Map(foundation["spec"])["replicas"] != float64(1) || deploymentplan.Map(deploymentplan.Map(foundation["spec"])["strategy"])["type"] != "Recreate" {
		return block("one approved registrar instance requires a Recreate rollout")
	}
	if mqttFoundationEnv(registrar, "VIDEO_CLOUD_MQTT_FOUNDATION_ENABLED") != "true" || mqttFoundationEnv(registrar, "VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID") == "" || mqttFoundationEnv(registrar, "VIDEO_CLOUD_MQTT_FOUNDATION_ENDPOINT_REF") != "mqtt" {
		return block("registrar enablement, original instance or MQTT service option is invalid")
	}
	expected := "https://" + lkeRegistrationServerDNS(env) + ":8443"
	if mqttFoundationEnv(registrar, "VIDEO_CLOUD_MQTT_FOUNDATION_REGISTRATION_URL") != expected {
		return block("registration URL does not select this environment's private Account Manager Service")
	}
	if mqttFoundationEnv(listener, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT") != env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] {
		return block("Account Manager listener port differs from shared configuration")
	}
	registrationPort, _ := strconv.Atoi(env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"])
	found := false
	for _, v := range certIssuerObjectList(listener["ports"]) {
		p := deploymentplan.Map(v)
		if p["name"] == "service-reg" && p["containerPort"] == float64(registrationPort) {
			found = true
		}
	}
	if !found {
		return block("Account Manager named registration port is absent")
	}
	service := mqttFoundationTarget(definitions, "Service", "account-manager")
	spec := deploymentplan.Map(service["spec"])
	found = false
	for _, v := range certIssuerObjectList(spec["ports"]) {
		p := deploymentplan.Map(v)
		if p["name"] == "service-reg" && p["port"] == float64(8443) && p["targetPort"] == "service-reg" {
			found = true
		}
	}
	if !found || spec["type"] != "ClusterIP" || len(certIssuerObjectList(spec["externalIPs"])) != 0 || deploymentplan.Map(spec["selector"])["app.kubernetes.io/name"] != "account-manager" {
		return block("Account Manager registration Service must remain private and route 8443 to its named listener")
	}
	for _, dependency := range []struct{ kind, name string }{{"Service", "mqtt"}, {"Service", "postgresql"}, {"NetworkPolicy", "allow-platform-service-registration"}, {"NetworkPolicy", "allow-video-cloud-mqtt-clients"}, {"NetworkPolicy", "allow-postgres-clients"}} {
		if mqttFoundationTarget(definitions, dependency.kind, dependency.name) == nil {
			return block("required target dependency is absent: " + dependency.kind + "/" + dependency.name)
		}
	}
	if mqttFoundationMountedSecret(foundation, registrar, "/etc/video_cloud/platform-service") == "" || mqttFoundationMountedSecret(account, listener, "/etc/rtk-account-manager/service-registration") == "" {
		return block("original registrar identity or listener TLS/CRL Secret mount is absent")
	}
	return nil
}
func (c *compiledEnvironmentDeployment) CheckMQTTFoundationIdentity(ctx context.Context, now time.Time) error {
	if c.Configuration.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] != "true" {
		return nil
	}
	definitions := []deploymentplan.Definition{}
	for _, resource := range c.Plan.Resources {
		if resource.Desired != nil {
			object := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(object["metadata"])["uid"] = resource.UID
			definitions = append(definitions, deploymentplan.Definition{Object: object})
		}
	}
	return checkMQTTFoundationIdentity(ctx, c.Configuration.Values, definitions, now, c.Backend.Read, func(ctx context.Context, account, listener deploymentplan.Object) (deploymentplan.Object, error) {
		return readAccountRegistrationPublicListener(ctx, c.Configuration, c.Store, account, listener, now, c.Backend.Read, func(ctx context.Context, args ...string) ([]byte, error) { return c.Backend.command(ctx, nil, args...) })
	})
}
func checkMQTTFoundationIdentity(ctx context.Context, env map[string]string, definitions []deploymentplan.Definition, now time.Time, read func(context.Context, string) (deploymentplan.Object, error), liveListeners ...accountRegistrationListenerRead) error {
	foundation := mqttFoundationTarget(definitions, "Deployment", "video-cloud-mqttfoundation")
	account := mqttFoundationTarget(definitions, "Deployment", "account-manager")
	identityName := mqttFoundationMountedSecret(foundation, mqttFoundationPrimary(foundation), "/etc/video_cloud/platform-service")
	listenerName := mqttFoundationMountedSecret(account, mqttFoundationPrimary(account), "/etc/rtk-account-manager/service-registration")
	block := func(cause string) error {
		return mqttFoundationBlocker("deployment.mqtt-foundation.identity", "video-cloud-mqttfoundation", cause)
	}
	if identityName == "" || listenerName == "" {
		return block("original registration identity mounts cannot be verified")
	}
	identity, err := read(ctx, "Secret/"+lkeNamespaceName(env, "video-cloud")+"/"+identityName)
	if err != nil || identity == nil {
		return block("original MQTT Foundation identity Secret is unavailable")
	}
	listener, err := read(ctx, "Secret/"+lkeNamespaceName(env, "account-manager")+"/"+listenerName)
	if err != nil || listener == nil {
		return block("original Account Manager listener TLS/CRL Secret is unavailable")
	}
	if accountRegistrationPublicCRLDeclared(account) {
		if len(liveListeners) != 1 || liveListeners[0] == nil {
			return block("managed registration public CRL reader is unavailable")
		}
		listener, err = liveListeners[0](ctx, account, listener)
		if err != nil {
			return block("managed registration public CRL export validation failed")
		}
	}
	if err := validatePlatformServiceIdentityMaterial(identity, listener, "service:mqtt", lkeRegistrationServerDNS(env), now); err != nil {
		// This validator emits only checker-owned material errors. Provider
		// read failures above remain masked; never append tool stderr.
		return block("registration identity validation failed: " + err.Error())
	}
	return nil
}
