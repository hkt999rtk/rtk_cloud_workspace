package main

import (
	"context"
	"fmt"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Managed EMQX owns its server private key on its enrolled PVC. These two
// dependency objects contain only maintained broker configuration and existing
// process credentials, never legacy TLS copies or regenerated secrets.
func renderManagedMQTTDependencyDefinitions(values map[string]string, secrets map[string]string) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("managed MQTT dependencies require the selected original dashboard, callback and EMQX API credentials; missing values cannot be regenerated")
	}
	environment, stack := values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"]
	if !secretEnvironmentPattern.MatchString(environment) || stack != "video-cloud-"+environment {
		return bad()
	}
	credential := func(name string) string { return secrets[lkeSecretFileName(name)] }
	dashboard, auth, key, password := credential("emqx-dashboard-password"), credential("mqtt-broker-auth"), credential("emqx-handoff-api-key"), credential("emqx-handoff-api-secret")
	for _, value := range []string{dashboard, auth, key, password} {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return bad()
		}
	}
	if strings.ContainsAny(key+password, ":") || strings.ContainsAny(auth, "\"\\") {
		return bad()
	}
	namespace := stack + "-video-cloud"
	meta := func(name string) deploymentplan.Object {
		return deploymentplan.Object{"name": name, "namespace": namespace, "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}
	}
	hocon := lkeEMQXTenantBaseHOCON(values)
	// Native broker sweeps require the maintained pki-session-policy.conf
	// contract: neither authentication nor authorization may cache decisions.
	hocon += "\nauthentication_settings.node_cache.enable = false\nauthorization.cache.enable = false\n"
	const legacyPath = "/opt/emqx/etc/certs/handoff-api-keys.conf"
	const managedPath = "/run/mqtt-pki/api-keys.conf"
	if strings.Contains(hocon, legacyPath) {
		hocon = strings.ReplaceAll(hocon, legacyPath, managedPath)
	} else {
		hocon += "\napi_key {\n  bootstrap_file = \"" + managedPath + "\"\n}\n"
	}
	// The inherited canonical HTTP authenticator remains fail-closed while the
	// API is in trust-only bootstrap. Finalization installs the native host's
	// HTTPS callback using its existing registered Service client.
	authentication := fmt.Sprintf(`[{mechanism=password_based,backend=http,enable=true,method=post,url="http://video-cloud-api.%s.svc.cluster.local/v1/internal/mqtt/authenticate",headers={"content-type"="application/json","authorization"="Bearer %s"},body={listener="${listener}",username="${username}",password="${password}",clientid="${clientid}"},connect_timeout="5s",request_timeout="5s",pool_size=32}]`, namespace, auth)
	cookie := lkeEMQXNodeCookie(values)
	if cookie == "" || strings.ContainsAny(cookie, "\r\n") {
		return bad()
	}
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("mqtt-pki-config"), "data": deploymentplan.Object{"broker": "emqx", "base.hocon": hocon}}
	runtime := environmentCreateSecret(namespace, "mqtt-pki-runtime", map[string]string{"cookie": cookie, "dashboard-password": dashboard, "authentication": authentication, "api-keys.conf": key + ":" + password + ":administrator\n"})
	runtime.Object["metadata"] = meta("mqtt-pki-runtime")
	return []deploymentplan.Definition{{Phase: "configuration", Object: config}, runtime}, nil
}

func renderEnvironmentCreateManagedMQTTDependencies(ctx context.Context, cfg deploymentConfig, release environmentRelease, runtime environmentCreateBootstrapRuntime) ([]deploymentplan.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := runtime.Backend.Store
	if runtime.Execution != nil {
		if store.Root != "" && store.Root != runtime.Execution.Store.Root {
			return nil, environmentCreateBootstrapBlocker("managed MQTT selected runtime store differs from its original execution")
		}
		store = runtime.Execution.Store
	}
	if store.Root == "" || store.Environment != cfg.Environment {
		return nil, environmentCreateBootstrapBlocker("managed MQTT dependency rendering requires the selected canonical original runtime store")
	}
	original, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return nil, err
	}
	if original.Root != store.Root {
		return nil, environmentCreateBootstrapBlocker("managed MQTT operator inputs differ from its original environment store")
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return nil, err
	}
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}
	if err := readEnvironmentCreateStorageRenderSnapshot(cfg, store, &snapshot); err != nil {
		return nil, err
	}
	var definitions []deploymentplan.Definition
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
		var err error
		definitions, err = renderManagedMQTTDependencyDefinitions(values, secrets)
		return err
	})
	return definitions, err
}
