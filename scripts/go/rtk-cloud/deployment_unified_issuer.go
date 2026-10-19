package main

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func environmentIssuerBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.issuer-runtime", Resource: id, Cause: cause, Next: "restore the original issuer runtime Secret references and unambiguous read-only mounts; the shared renderer derives paths and startup settings, then repeat full preflight"}
}

// Select an existing referenced Secret by its role keys, never by a guessed
// filename or a credential value. The plan separately fences its original UID.
func observeEnvironmentIssuerRuntimeSecret(object deploymentplan.Object, live []deploymentplan.Object) (string, error) {
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	if name == "certissuer" && mqttFoundationEnv(mqttFoundationPrimary(object), "CERT_ISSUER_REGISTRY_ONLY_ENABLED") == "true" {
		if err := validateEnvironmentRegistryOnlyIssuerStartup(object); err != nil {
			return "", err
		}
		container := mqttFoundationPrimary(object)
		keys := []string{"CERT_ISSUER_DB_DSN"}
		if mqttFoundationEnv(container, "CERT_ISSUER_PRODUCT_PKI_ENABLED") == "true" {
			keys = append(keys, "CERT_ISSUER_PRODUCTION_JWT_SECRET", "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE")
		}
		for _, key := range keys {
			reference, err := environmentRegistryIssuerSecretReference(object, container, key)
			if err != nil {
				return "", err
			}
			found := 0
			for _, secret := range live {
				if secret["kind"] != "Secret" || deploymentplan.Map(secret["metadata"])["namespace"] != deploymentplan.Map(object["metadata"])["namespace"] || deploymentplan.Map(secret["metadata"])["name"] != reference["name"] {
					continue
				}
				if deploymentplan.String(deploymentplan.Map(secret["data"])[deploymentplan.String(reference["key"])]) != "" {
					found++
				}
			}
			if found != 1 {
				return "", environmentIssuerBlocker(deploymentplan.ID(object), "registry-only issuer requires each original database and Product JWT Secret key")
			}
		}
		reference, _ := environmentRegistryIssuerSecretReference(object, container, "CERT_ISSUER_DB_DSN")
		return deploymentplan.String(reference["name"]), nil
	}
	keys := []string{"POSTGRES_PASSWORD", "device-ca.crt", "client-ca.crt"}
	if name == "factoryenroll" {
		keys = []string{"POSTGRES_PASSWORD", "FACTORY_ENROLL_AUTH_KEY"}
	}
	references := environmentResourceReferences(object)
	candidates := map[string]string{}
	for _, reference := range references {
		if reference.Kind != "Secret" {
			continue
		}
		for _, secret := range live {
			if deploymentplan.ID(secret) != reference.ID {
				continue
			}
			data, found := deploymentplan.Map(secret["data"]), true
			for _, key := range keys {
				found = found && deploymentplan.String(data[key]) != ""
			}
			if found {
				candidates[reference.Name] = reference.Name
			}
		}
	}
	if len(candidates) != 1 {
		return "", environmentIssuerBlocker(deploymentplan.ID(object), "one original referenced Secret with the issuer runtime role keys is required")
	}
	return sortedMapKeys(candidates)[0], nil
}

// Secret item paths and container mounts define the container-visible path.
// This does not open or create any certificate, and cannot select another CA.
func environmentIssuerSecretFile(object deploymentplan.Object, container deploymentplan.Object, secretName, key string) (string, error) {
	var files []string
	for _, value := range certIssuerObjectList(environmentPKIPod(object)["volumes"]) {
		volume := deploymentplan.Map(value)
		secret := deploymentplan.Map(volume["secret"])
		if secret["secretName"] != secretName || secret["optional"] == true {
			continue
		}
		file := key
		if items, declared := secret["items"]; declared {
			file = ""
			for _, value := range certIssuerObjectList(items) {
				item := deploymentplan.Map(value)
				if item["key"] == key {
					if file != "" {
						return "", environmentIssuerBlocker(deploymentplan.ID(object), "issuer certificate key has duplicate file mappings")
					}
					file = deploymentplan.String(item["path"])
				}
			}
		}
		if file == "" || path.IsAbs(file) || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") || strings.ContainsAny(file, "\x00\r\n\\") {
			continue
		}
		for _, value := range certIssuerObjectList(container["volumeMounts"]) {
			mount := deploymentplan.Map(value)
			root := deploymentplan.String(mount["mountPath"])
			if mount["name"] != volume["name"] || mount["readOnly"] != true || !path.IsAbs(root) || path.Clean(root) != root || mount["subPathExpr"] != nil || strings.ContainsAny(root, "\x00\r\n\\") {
				continue
			}
			sub := deploymentplan.String(mount["subPath"])
			if sub == "" {
				files = append(files, path.Join(root, file))
			} else if sub == file {
				files = append(files, root)
			}
		}
	}
	if len(files) != 1 {
		return "", environmentIssuerBlocker(deploymentplan.ID(object), "issuer CA key requires one unambiguous read-only Secret file mount")
	}
	return files[0], nil
}

func applyEnvironmentIssuerRuntime(env map[string]string, object deploymentplan.Object, binding environmentPodBindings) error {
	if binding.Name != "certissuer" && binding.Name != "factoryenroll" {
		return nil
	}
	id := deploymentplan.ID(object)
	for _, key := range []string{"CERT_ISSUER_CA_CERT_PATH", "CERT_ISSUER_SIGNER_PROVIDER", "LKE_CERT_ISSUER_PORT"} {
		if env[key] != "" {
			return environmentIssuerBlocker(id, key+" is derived by the shared renderer and must not be configured manually")
		}
	}
	if !secretEnvironmentPattern.MatchString(binding.IssuerRuntimeSecret) || len(binding.Containers) == 0 {
		return environmentIssuerBlocker(id, "original issuer runtime Secret reference is unavailable")
	}
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) == 0 {
		return environmentIssuerBlocker(id, "issuer primary container is unavailable")
	}
	container := deploymentplan.Map(containers[0])
	if binding.Name == "certissuer" && mqttFoundationEnv(container, "CERT_ISSUER_REGISTRY_ONLY_ENABLED") == "true" {
		reference, err := environmentRegistryIssuerSecretReference(object, container, "CERT_ISSUER_DB_DSN")
		if err != nil || reference["name"] != binding.IssuerRuntimeSecret {
			return environmentIssuerBlocker(id, "registry-only issuer lost its original database Secret binding")
		}
		if err := validateEnvironmentRegistryOnlyIssuerStartup(object); err != nil {
			return err
		}
		container["ports"] = []any{deploymentplan.Object{"name": "tls", "containerPort": float64(18445), "protocol": "TCP"}}
		container["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": "tls"}, "periodSeconds": float64(5)}
		container["env"] = mergeEnvironmentNamedFields(container["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_ENV", "value": env["CLOUD_ENV_NAME"]}})
		return nil
	}
	ns, platform := lkeNamespaceName(env, "video-cloud"), lkeNamespaceName(env, "platform")
	dsn := "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql." + platform + ".svc.cluster.local:5432/video_cloud?sslmode=disable"
	settings := map[string]string{"VIDEO_CLOUD_ENV": env["CLOUD_ENV_NAME"]}
	if binding.Name == "factoryenroll" {
		settings["FACTORY_ENROLL_ADDR"] = ":18443"
		settings["VIDEO_CLOUD_DB_DSN"] = dsn
		settings["FACTORY_ENROLL_ACCOUNT_MANAGER_URL"] = "https://account-manager-internal." + lkeNamespaceName(env, "account-manager") + ".svc:8443"
		if state, pin := mqttFoundationEnv(container, "FACTORY_ENROLL_SERVICE_IDENTITY_STATE"), mqttFoundationEnv(container, "FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256"); state != "" || pin != "" {
			accountURL := "https://account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local:18443"
			issuerURL := "https://certissuer." + ns + ".svc:18445"
			if !path.IsAbs(state) || path.Clean(state) != state || !environmentPreflightDigest.MatchString(pin) || mqttFoundationEnv(container, "FACTORY_ENROLL_ACCOUNT_MANAGER_URL") != accountURL || mqttFoundationEnv(container, "FACTORY_ENROLL_CERT_ISSUER_URL") != issuerURL || mqttFoundationEnv(container, "FACTORY_ENROLL_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256") != pin || mqttFoundationEnv(container, "FACTORY_ENROLL_CERT_ISSUER_SERVER_PKI_ROOT_SHA256") != pin {
				return environmentIssuerBlocker(id, "native Factory enrollment must retain its original Service identity and exact managed Account/issuer endpoints")
			}
			settings["FACTORY_ENROLL_ACCOUNT_MANAGER_URL"] = accountURL
		}
	} else {
		ca, err := environmentIssuerSecretFile(object, container, binding.IssuerRuntimeSecret, "device-ca.crt")
		if err != nil {
			return err
		}
		settings["CERT_ISSUER_CA_CERT_PATH"] = ca
		settings["CERT_ISSUER_LISTEN_ADDR"] = fmt.Sprintf(":%d", lkeResolveCertIssuerTLSConfig(env).HTTPSPort)
		settings["CERT_ISSUER_DB_DSN"] = dsn
		settings["CERT_ISSUER_SIGNER_PROVIDER"] = "openbao"
		settings["CERT_ISSUER_OPENBAO_PKI_MOUNT"] = "pki/device"
		settings["CERT_ISSUER_OPENBAO_PKI_ROLE"] = "factory-device"
		settings["CERT_ISSUER_OPENBAO_GATEWAY_ROLE"] = "gateway-server"
		settings["CERT_ISSUER_MAX_TTL_DAYS"] = "365"
		settings["CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN"] = "^service:factory-enroll$"
		settings["CERT_ISSUER_APP_CLIENT_CN_PATTERN"] = "^service:account-manager$"
		settings["CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN"] = "^(?:service:(?:certissuer|pki-controller|account-manager|video-cloud-api))$"
		// Optional issuance domains remain enabled only by their original
		// verified root binding; deriving a DNS name must not enable a domain.
		for _, value := range certIssuerObjectList(container["env"]) {
			e := deploymentplan.Map(value)
			if e["valueFrom"] == nil && deploymentplan.String(e["value"]) == "" {
				continue
			}
			switch e["name"] {
			case "CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256":
				settings["CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN"] = "^service:emqx-pki$"
				settings["CERT_ISSUER_MQTT_SERVER_DNS_NAMES"] = "mqtt-pki." + ns + ".svc"
			case "CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256":
				settings["CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN"] = "^service:openbao$"
				settings["CERT_ISSUER_OPENBAO_HOST_DNS_NAMES"] = "openbao." + lkeNamespaceName(env, "secrets") + ".svc,openbao." + lkeNamespaceName(env, "secrets") + ".svc.cluster.local"
			}
		}
		hosts := []string{"certissuer." + ns + ".svc", "pki-controller." + ns + ".svc", "account-manager-internal." + lkeNamespaceName(env, "account-manager") + ".svc", "account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local"}
		if host := lkeResolveCertIssuerTLSConfig(env).PublicHost; host != "" {
			hosts = append(hosts, host)
		}
		settings["CERT_ISSUER_GATEWAY_DNS_NAMES"] = strings.Join(hosts, ",")
		if env["CERT_ISSUER_APP_PKI_ENABLED"] == "false" {
			appCA, err := environmentIssuerSecretFile(object, container, binding.IssuerRuntimeSecret, "app-ca.crt")
			if err != nil {
				return err
			}
			settings["CERT_ISSUER_APP_CA_CERT_PATH"] = appCA
			settings["CERT_ISSUER_APP_SIGNER_PROVIDER"] = "openbao"
			settings["CERT_ISSUER_APP_OPENBAO_PKI_MOUNT"] = "pki/app"
			settings["CERT_ISSUER_APP_OPENBAO_PKI_ROLE"] = "app-user"
		}
	}
	// Kubernetes expands only preceding environment variables. Preserve Secret
	// values as references and place the password before either derived DSN.
	values := []any{deploymentplan.Object{"name": "POSTGRES_PASSWORD", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": binding.IssuerRuntimeSecret, "key": "POSTGRES_PASSWORD"}}}}
	for _, value := range certIssuerObjectList(container["env"]) {
		name := deploymentplan.String(deploymentplan.Map(value)["name"])
		if _, replaced := settings[name]; !replaced && name != "POSTGRES_PASSWORD" {
			values = append(values, value)
		}
	}
	for _, key := range sortedMapKeys(settings) {
		values = append(values, deploymentplan.Object{"name": key, "value": settings[key]})
	}
	container["env"] = values
	return validateEnvironmentIssuerStartup(object)
}

// Compile-time checks reject an incomplete candidate before any rollout. The
// native runtime/PKI gates still verify actual credentials, lineage and health.
func validateEnvironmentIssuerStartup(object deploymentplan.Object) error {
	if object["kind"] != "Deployment" || deploymentplan.String(deploymentplan.Map(object["metadata"])["name"]) != "certissuer" {
		return nil
	}
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) == 0 {
		return environmentIssuerBlocker(deploymentplan.ID(object), "candidate issuer primary container is unavailable")
	}
	container := deploymentplan.Map(containers[0])
	if mqttFoundationEnv(container, "CERT_ISSUER_REGISTRY_ONLY_ENABLED") == "true" {
		return validateEnvironmentRegistryOnlyIssuerStartup(object)
	}
	settings := map[string]string{}
	for _, value := range certIssuerObjectList(container["env"]) {
		e := deploymentplan.Map(value)
		settings[deploymentplan.String(e["name"])] = deploymentplan.String(e["value"])
	}
	for _, key := range []string{"CERT_ISSUER_CA_CERT_PATH", "CERT_ISSUER_LISTEN_ADDR", "CERT_ISSUER_DB_DSN", "CERT_ISSUER_OPENBAO_PKI_MOUNT", "CERT_ISSUER_OPENBAO_PKI_ROLE", "CERT_ISSUER_GATEWAY_DNS_NAMES"} {
		if settings[key] == "" {
			return environmentIssuerBlocker(deploymentplan.ID(object), "candidate issuer startup setting is missing: "+key)
		}
	}
	if settings["CERT_ISSUER_SIGNER_PROVIDER"] != "openbao" || settings["CERT_ISSUER_MAX_TTL_DAYS"] != "365" {
		return environmentIssuerBlocker(deploymentplan.ID(object), "candidate issuer must use the managed OpenBao signer and bounded server TTL")
	}
	for _, key := range []string{"CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN", "CERT_ISSUER_APP_CLIENT_CN_PATTERN", "CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN"} {
		if _, err := regexp.Compile(settings[key]); err != nil || settings[key] == "" {
			return environmentIssuerBlocker(deploymentplan.ID(object), "candidate issuer caller policy is missing or invalid: "+key)
		}
	}
	return nil
}

func environmentRegistryIssuerSecretReference(object, container deploymentplan.Object, name string) (deploymentplan.Object, error) {
	var selected deploymentplan.Object
	for _, value := range certIssuerObjectList(container["env"]) {
		setting := deploymentplan.Map(value)
		if setting["name"] != name {
			continue
		}
		reference := deploymentplan.Map(deploymentplan.Map(setting["valueFrom"])["secretKeyRef"])
		if selected != nil || setting["value"] != nil || !secretEnvironmentPattern.MatchString(deploymentplan.String(reference["name"])) || reference["optional"] == true {
			return nil, environmentIssuerBlocker(deploymentplan.ID(object), "registry-only issuer requires one mandatory Secret key reference: "+name)
		}
		selected = reference
	}
	wanted := map[string]string{"CERT_ISSUER_DB_DSN": "url", "CERT_ISSUER_PRODUCTION_JWT_SECRET": "FACTORY_ENROLL_PRODUCTION_JWT_SECRET", "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE": "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE"}[name]
	if selected == nil || wanted == "" || selected["key"] != wanted {
		return nil, environmentIssuerBlocker(deploymentplan.ID(object), "registry-only issuer lost an exact database or Product JWT role key: "+name)
	}
	return selected, nil
}

func applyEnvironmentRegistryIssuerService(object deploymentplan.Object, bindings environmentIdentityBindings) error {
	if object["kind"] != "Service" || deploymentplan.Map(object["metadata"])["name"] != "certissuer" || deploymentplan.Map(object["metadata"])["namespace"] != bindings.Stack+"-video-cloud" {
		return nil
	}
	for _, binding := range bindings.Pods {
		if binding.Name != "certissuer" || binding.NamespaceSuffix != "video-cloud" || len(binding.Containers) == 0 || mqttFoundationEnv(binding.Containers[0].Fields, "CERT_ISSUER_REGISTRY_ONLY_ENABLED") != "true" {
			continue
		}
		if bindings.Owners[deploymentplan.ID(object)] == "" || mqttFoundationEnv(binding.Containers[0].Fields, "CERT_ISSUER_LISTEN_ADDR") != ":18445" {
			return environmentIssuerBlocker(deploymentplan.ID(object), "native issuer Service requires its original owner and exact admitted listener")
		}
		deploymentplan.Map(object["spec"])["ports"] = []any{deploymentplan.Object{"name": "https", "port": float64(18445), "targetPort": float64(18445), "protocol": "TCP"}}
	}
	return nil
}

// The explicit native mode retains only admitted settings and original Secret
// references. Legacy fixed CA files and roles remain confined to the old recipe.
func validateEnvironmentRegistryOnlyIssuerStartup(object deploymentplan.Object) error {
	container := mqttFoundationPrimary(object)
	id := deploymentplan.ID(object)
	block := func(cause string) error { return environmentIssuerBlocker(id, cause) }
	settings := map[string]string{}
	seen := map[string]bool{}
	referenced := map[string]bool{}
	for _, value := range certIssuerObjectList(container["env"]) {
		e := deploymentplan.Map(value)
		name := deploymentplan.String(e["name"])
		if seen[name] {
			return block("registry-only issuer has duplicate native settings")
		}
		seen[name] = true
		referenced[name] = e["valueFrom"] != nil
		settings[name] = deploymentplan.String(e["value"])
	}
	if settings["CERT_ISSUER_REGISTRY_ONLY_ENABLED"] != "true" || settings["CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY"] != "" && settings["CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY"] != "false" || referenced["CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY"] || settings["CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED"] != "true" || settings["CERT_ISSUER_SERVER_PKI_DOMAIN"] != "service" || settings["CERT_ISSUER_TRUSTED_HEADER_ENABLED"] == "true" || referenced["CERT_ISSUER_TRUSTED_HEADER_ENABLED"] {
		return block("registry-only issuer requires its managed Service client and host with direct mTLS")
	}
	for _, key := range []string{"CERT_ISSUER_CA_CERT_PATH", "CERT_ISSUER_CA_KEY_PATH", "CERT_ISSUER_APP_CA_CERT_PATH", "CERT_ISSUER_APP_CA_KEY_PATH", "CERT_ISSUER_SERVER_CERT", "CERT_ISSUER_SERVER_KEY", "CERT_ISSUER_PKCS11_MODULE_PATH", "CERT_ISSUER_APP_PKCS11_MODULE_PATH", "CERT_ISSUER_HOST_TRANSITION_FROM_STATE", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECT", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS"} {
		if settings[key] != "" || referenced[key] {
			return block("normal registry-only issuer cannot use static CA/host material, transitions or temporary provisioning authority")
		}
	}
	pin := settings["CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256"]
	if !environmentPreflightDigest.MatchString(pin) {
		return block("registry-only issuer requires its exact current Service Root pin")
	}
	for _, key := range []string{"CERT_ISSUER_SERVICE_CLIENT_ISSUER_ROOT_SHA256", "CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256", "CERT_ISSUER_SERVER_PKI_ROOT_SHA256", "CERT_ISSUER_SERVER_PKI_ISSUER_ROOT_SHA256", "CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256", "CERT_ISSUER_HOST_ROOT_SHA256"} {
		if settings[key] != pin {
			return block("registry-only issuer independent current Service client and host pins differ")
		}
	}
	for _, key := range []string{"CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE", "CERT_ISSUER_HOST_IDENTITY_STATE", "CERT_ISSUER_CLIENT_CA", "OPENBAO_CACERT", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE"} {
		if !path.IsAbs(settings[key]) || path.Clean(settings[key]) != settings[key] || strings.ContainsAny(settings[key], "\x00\r\n\\") {
			return block("registry-only issuer requires canonical original private state and trust paths")
		}
	}
	if settings["CERT_ISSUER_SIGNER_PROVIDER"] != "openbao" || settings["CERT_ISSUER_APP_SIGNER_PROVIDER"] != "openbao" || settings["OPENBAO_AUTH_METHOD"] != "kubernetes" || !secretEnvironmentPattern.MatchString(settings["OPENBAO_KUBERNETES_ROLE"]) {
		return block("registry-only issuer requires the maintained native OpenBao provider and role")
	}
	provider, err := url.Parse(settings["OPENBAO_ADDR"])
	if err != nil || provider.Scheme != "https" || provider.Host == "" || provider.User != nil || provider.RawQuery != "" || provider.Fragment != "" {
		return block("registry-only issuer provider origin must retain explicit HTTPS trust")
	}
	_, port, err := net.SplitHostPort(settings["CERT_ISSUER_LISTEN_ADDR"])
	if err != nil || port != "18445" {
		return block("registry-only issuer must retain its maintained native listener port")
	}
	minimum, errMin := strconv.Atoi(settings["CERT_ISSUER_MIN_TTL_DAYS"])
	maximum, errMax := strconv.Atoi(settings["CERT_ISSUER_MAX_TTL_DAYS"])
	defaultTTL, errDefault := strconv.Atoi(settings["CERT_ISSUER_DEFAULT_TTL_DAYS"])
	if errMin != nil || errMax != nil || errDefault != nil || minimum < 1 || minimum > 90 || maximum > 365 || defaultTTL < minimum || defaultTTL > maximum {
		return block("registry-only issuer requires the bounded native issuance lifetimes")
	}
	for _, policy := range []string{"CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN", "CERT_ISSUER_APP_CLIENT_CN_PATTERN", "CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN", "CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN"} {
		if _, err := regexp.Compile(settings[policy]); err != nil || !strings.HasPrefix(settings[policy], "^") || !strings.HasSuffix(settings[policy], "$") {
			return block("registry-only issuer requires explicit admitted caller policies")
		}
	}
	if settings["CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN"] != "^$" || settings["CERT_ISSUER_GATEWAY_DNS_NAMES"] == "" {
		return block("normal registry-only issuer must retain exact gateway DNS and closed temporary provisioning")
	}
	for _, domain := range []string{"service", "app"} {
		key := "CERT_ISSUER_OPENBAO_PKI_MOUNT"
		if domain == "app" {
			if settings["CERT_ISSUER_APP_PKI_ENABLED"] != "true" {
				continue
			}
			key = "CERT_ISSUER_APP_OPENBAO_PKI_MOUNT"
		}
		if !regexp.MustCompile(`^pki-issuers/` + domain + `/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/v[1-9][0-9]*$`).MatchString(settings[key]) {
			return block("registry-only issuer must retain its exact registered signing references")
		}
	}
	if _, err := environmentRegistryIssuerSecretReference(object, container, "CERT_ISSUER_DB_DSN"); err != nil {
		return err
	}
	if settings["CERT_ISSUER_PRODUCT_PKI_ENABLED"] == "true" {
		secret, err := environmentRegistryIssuerSecretReference(object, container, "CERT_ISSUER_PRODUCTION_JWT_SECRET")
		if err != nil {
			return err
		}
		audience, err := environmentRegistryIssuerSecretReference(object, container, "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE")
		if err != nil || secret["name"] != audience["name"] {
			return block("registry-only Product JWT authority and audience require the same original Secret")
		}
	}
	return nil
}
