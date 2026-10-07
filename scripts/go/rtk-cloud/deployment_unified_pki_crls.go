package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func crlInspectionBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.crls", Resource: resource, Cause: cause, Next: "restore the maintained complete domain consumer policy, original owned manifest mounts and private durable CRL state; reconcile current signed publication and real consumer receipts through managed lifecycle, then repeat full preflight; do not fabricate acknowledgments"}
}

// Observe every configured manifest through the same original-Pod/PVC/source
// fences as managed identity inspection. A required receipt ID cannot be dropped
// because its workload or manifest is missing.
func (b environmentKubernetesBackend) CheckInstalledCRLs(ctx context.Context, plan *deploymentplan.Plan, values map[string]string) error {
	return b.checkInstalledConsumerTrust(ctx, plan, values, false)
}
func (b environmentKubernetesBackend) CheckInstalledRootTrust(ctx context.Context, plan *deploymentplan.Plan, values map[string]string) error {
	return b.checkInstalledConsumerTrust(ctx, plan, values, true)
}
func (b environmentKubernetesBackend) checkInstalledConsumerTrust(ctx context.Context, plan *deploymentplan.Plan, values map[string]string, rootTrust bool) error {
	block := crlInspectionBlocker
	if rootTrust {
		block = rootInspectionBlocker
	}
	window, err := environmentPKIValidityWindow(values)
	if err != nil {
		return err
	}
	required, seen := map[string]bool{}, map[string]bool{}
	for _, domain := range managedPKIDomains {
		ids := strings.Split(values["PKI_REQUIRED_CONSUMERS_"+strings.ToUpper(domain)], ",")
		if len(ids) == 0 || len(ids) > 128 {
			return block("", "complete domain consumer policy is unavailable")
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			key := domain + ":" + id
			if id == "" || len(id) > 128 || required[key] || strings.HasPrefix(id, "service:") || strings.ContainsAny(id, ",/\\") || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return block("", "complete domain consumer policy is invalid")
			}
			required[key] = true
		}
	}
	err = b.inspectOriginalManagedStatePods(ctx, plan, block, func(object, container deploymentplan.Object) ([]originalManagedStateProbe, error) {
		settings, err := environmentPKINamedFields(container["env"])
		if err != nil {
			return nil, err
		}
		probes := []originalManagedStateProbe{}
		names := []string{}
		for name := range settings {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !strings.HasSuffix(name, "_CRL_MANIFEST") {
				continue
			}
			domains, consumer, database := environmentCRLConsumerContract(object, container, name)
			if len(domains) == 0 {
				return nil, block(deploymentplan.ID(object), "configured CRL consumer has no maintained workload/domain contract")
			}
			path := deploymentplan.String(settings[name]["value"])
			manifest, reference, fence, err := b.bindOriginalPublicManifest(ctx, plan, object, container, path)
			if err != nil {
				return nil, err
			}
			var entries []struct {
				Issuer map[string]any `json:"issuer"`
				State  string         `json:"state_path"`
			}
			if json.Unmarshal(manifest, &entries) != nil || len(entries) == 0 || len(entries) > 128 {
				return nil, block(reference.Name, "planned CRL manifest is invalid")
			}
			for _, domain := range domains {
				key := domain + ":" + consumer
				if !required[key] {
					return nil, block(deploymentplan.ID(object), "enabled CRL consumer is absent from the maintained required domain policy")
				}
				paths := []string{}
				for _, entry := range entries {
					if deploymentplan.String(entry.Issuer["environment"]) != plan.Environment {
						return nil, block(deploymentplan.ID(object), "CRL manifest belongs to another environment")
					}
					entryDomain := deploymentplan.String(entry.Issuer["domain"])
					if entryDomain == "" {
						entryDomain = deploymentplan.String(entry.Issuer["trust_domain"])
					}
					if entryDomain != domain {
						continue
					}
					if !filepath.IsAbs(entry.State) || filepath.Clean(entry.State) != entry.State {
						return nil, block(deploymentplan.ID(object), "CRL state requires a canonical original durable path")
					}
					paths = append(paths, entry.State)
				}
				if len(paths) == 0 {
					return nil, block(deploymentplan.ID(object), "required domain has no installed CRL authority inventory")
				}
				sort.Strings(paths)
				rootID, rootState, provisioned := "", "", ""
				var rootsData []byte
				var rootReference environmentPublicFileRef
				var rootFence func(context.Context) error
				crlCount := len(paths)
				if rootTrust {
					idSetting, stateSetting, rootsSetting := environmentRootTrustSettings(name, domain)
					rootID = deploymentplan.String(settings[idSetting]["value"])
					rootState = deploymentplan.String(settings[stateSetting]["value"])
					provisioned = deploymentplan.String(settings[rootsSetting]["value"])
					found := false
					for _, entry := range entries {
						if entry.Issuer["issuer_id"] == rootID && entry.Issuer["kind"] == "root" {
							found = true
						}
					}
					if rootID == "" || !found || !filepath.IsAbs(rootState) || filepath.Clean(rootState) != rootState || rootState == provisioned {
						return nil, block(deploymentplan.ID(object), "Root policy authority/state/provisioned roots binding is incomplete or absent from the reviewed manifest")
					}
					rootsData, rootReference, rootFence, err = b.bindOriginalPublicManifest(ctx, plan, object, container, provisioned)
					if err != nil {
						return nil, block(deploymentplan.ID(object), "original provisioned Root file is not bound to its planned owner")
					}
					paths = append(paths, rootState)
				}
				command := "inspect-crls"
				if name == "VIDEO_CLOUD_AUTH_CRL_MANIFEST" {
					command = "inspect-auth-crls"
				}
				probes = append(probes, originalManagedStateProbe{Paths: paths, Fence: func(ctx context.Context) error {
					if err := fence(ctx); err != nil {
						return err
					}
					if rootFence != nil {
						return rootFence(ctx)
					}
					return nil
				}, CheckPod: func(pod deploymentplan.Object) error {
					actualContainer := deploymentplan.Object(nil)
					for _, item := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
						if deploymentplan.Map(item)["name"] == container["name"] {
							actualContainer = deploymentplan.Map(item)
						}
					}
					actual, err := environmentPublicFileReference(deploymentplan.Map(pod["spec"]), actualContainer, path)
					if err != nil || actual != reference {
						return errors.New("original Pod manifest source differs from planned owner")
					}
					if rootTrust {
						rootActual, err := environmentPublicFileReference(deploymentplan.Map(pod["spec"]), actualContainer, provisioned)
						if err != nil || rootActual != rootReference {
							return errors.New("original Pod Root file differs from planned owner")
						}
					}
					return nil
				}, Inspect: func(ctx context.Context, args []string) error {
					if rootTrust {
						args = append(args, "inspect-roots", plan.Environment, domain, consumer, rootID, rootState, provisioned, strconv.FormatInt(window, 10), database)
						raw, err := b.command(ctx, nil, args...)
						var report struct {
							Environment string `json:"environment"`
							Domain      string `json:"domain"`
							Consumer    string `json:"consumer"`
							RootID      string `json:"root_id"`
							State       string `json:"state_sha256"`
							Policy      string `json:"policy_sha256"`
							Roots       string `json:"loaded_roots_sha256"`
							Provisioned string `json:"provisioned_roots_sha256"`
							Verified    bool   `json:"roots_verified"`
							Consumers   bool   `json:"consumers_verified"`
							Window      int64  `json:"minimum_validity_seconds"`
						}
						hash := sha256.Sum256(rootsData)
						if err != nil || decodeEnvironmentJSON(raw, &report) != nil || report.Environment != plan.Environment || report.Domain != domain || report.Consumer != consumer || report.RootID != rootID || !deploymentImageDigestPattern.MatchString("sha256:"+report.State) || !deploymentImageDigestPattern.MatchString("sha256:"+report.Policy) || !deploymentImageDigestPattern.MatchString("sha256:"+report.Roots) || report.Provisioned != hex.EncodeToString(hash[:]) || !report.Verified || !report.Consumers || report.Window != window {
							return errors.New("native Root policy/pool/receipt qualification incomplete")
						}
						seen[key] = true
						return nil
					}
					args = append(args, command, plan.Environment, domain, consumer, path, strconv.FormatInt(window, 10), database)
					raw, err := b.command(ctx, nil, args...)
					var report struct {
						Environment string `json:"environment"`
						Domain      string `json:"domain"`
						Consumer    string `json:"consumer"`
						Manifest    string `json:"manifest_sha256"`
						Authorities int    `json:"authorities"`
						CRLs        bool   `json:"crls_verified"`
						Consumers   bool   `json:"consumers_verified"`
						Window      int64  `json:"minimum_validity_seconds"`
					}
					if err != nil || decodeEnvironmentJSON(raw, &report) != nil || report.Environment != plan.Environment || report.Domain != domain || report.Consumer != consumer || report.Manifest != func() string { hash := sha256.Sum256(manifest); return hex.EncodeToString(hash[:]) }() || report.Authorities != crlCount || !report.CRLs || !report.Consumers || report.Window != window {
						return errors.New("native CRL inventory/receipt qualification incomplete")
					}
					seen[key] = true
					return nil
				}})
			}
		}
		return probes, nil
	})
	if err != nil {
		return err
	}
	for key := range required {
		if !seen[key] {
			return block("", "a required domain consumer has no qualified original owned workload/manifest")
		}
	}
	return nil
}

// These are actual runtime loader contracts. Generic suffix guessing cannot
// assign a database, trust domain or receipt identity to an unknown workload.
func environmentCRLConsumerContract(object, container deploymentplan.Object, setting string) ([]string, string, string) {
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	consumer, database := "", "PKI_DATABASE_URL"
	switch name {
	case "pki-controller":
		consumer = "pki-controller"
	case "certissuer", "certissuer-service-pki":
		consumer, database = "certissuer", "CERT_ISSUER_DB_DSN"
	case "video-cloud-api-pki":
		consumer, database = "video-cloud-api", "VIDEO_CLOUD_DB_DSN"
	case "video-cloud-logingester":
		consumer, database = "video-cloud-logingester", "VIDEO_CLOUD_DB_DSN"
	case "mqtt-pki":
		if container["name"] == "pkibroker" {
			consumer = "pkibroker"
		} else if container["name"] == "mqtt" {
			consumer = "emqx-pki"
		}
	case "openbao":
		consumer = "openbao"
	}
	if consumer == "" {
		return nil, "", ""
	}
	domain := ""
	switch setting {
	case "PKI_BROKER_DEVICE_CRL_MANIFEST":
		if consumer == "pkibroker" {
			domain = "device"
		}
	case "PKI_BROKER_APP_CRL_MANIFEST":
		if consumer == "pkibroker" {
			domain = "app"
		}
	case "VIDEO_CLOUD_AUTH_CRL_MANIFEST":
		if consumer == "video-cloud-api" {
			settings, err := environmentPKINamedFields(container["env"])
			if err != nil {
				return nil, "", ""
			}
			domains := []string{}
			for _, flag := range []struct{ name, domain string }{{"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED", "device"}, {"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED", "app"}} {
				if settings[flag.name]["valueFrom"] != nil {
					return nil, "", ""
				}
				enabled, _ := strconv.ParseBool(deploymentplan.String(settings[flag.name]["value"]))
				if enabled {
					domains = append(domains, flag.domain)
				}
			}
			return domains, consumer, database
		}
	case "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST":
		if consumer == "video-cloud-api" || consumer == "video-cloud-logingester" {
			domain = "mqtt"
		}
	case "OPENBAO_SERVER_CRL_MANIFEST":
		if consumer == "pki-controller" || consumer == "certissuer" {
			domain = "openbao_tls"
		}
	default:
		prefixes := map[string][]string{
			"pki-controller":          {"PKI_SERVICE_CLIENT", "PKI_HOST_RENEWAL"},
			"certissuer":              {"CERT_ISSUER_SERVICE_CLIENT", "CERT_ISSUER_HOST_RENEWAL"},
			"video-cloud-api":         {"VIDEO_CLOUD_CONTROLLER", "VIDEO_CLOUD_ACCOUNT_MANAGER"},
			"emqx-pki":                {"EMQX_PKI_SERVICE_CLIENT", "EMQX_PKI_HOST_RENEWAL"},
			"openbao":                 {"OPENBAO_PKI_SERVICE_CLIENT", "OPENBAO_PKI_HOST_RENEWAL"},
			"pkibroker":               {"PKI_BROKER"},
			"video-cloud-logingester": {"VIDEO_CLOUD_LOG_INGESTER_MQTT"},
		}
		for _, prefix := range prefixes[consumer] {
			if setting == prefix+"_SERVER_CRL_MANIFEST" {
				domain = "service"
			}
		}
	}
	if domain == "" {
		return nil, "", ""
	}
	return []string{domain}, consumer, database
}

type environmentPublicFileRef struct{ Kind, Name, Key string }

func environmentPublicFileReference(pod, container deploymentplan.Object, path string) (environmentPublicFileRef, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return environmentPublicFileRef{}, errors.New("canonical public manifest path required")
	}
	var selected deploymentplan.Object
	relative := ""
	length := -1
	for _, item := range certIssuerObjectList(container["volumeMounts"]) {
		mount := deploymentplan.Map(item)
		base := deploymentplan.String(mount["mountPath"])
		if base == "" || !filepath.IsAbs(base) || filepath.Clean(base) != base {
			continue
		}
		if path != base && !strings.HasPrefix(path, strings.TrimRight(base, "/")+"/") {
			continue
		}
		if len(base) > length {
			selected = mount
			relative = strings.TrimPrefix(strings.TrimPrefix(path, base), "/")
			length = len(base)
		}
	}
	if selected == nil || selected["subPathExpr"] != nil {
		return environmentPublicFileRef{}, errors.New("explicit public manifest mount required")
	}
	if sub := deploymentplan.String(selected["subPath"]); sub != "" {
		if relative != "" {
			return environmentPublicFileRef{}, errors.New("manifest subPath file mapping is ambiguous")
		}
		relative = sub
	}
	for _, item := range certIssuerObjectList(pod["volumes"]) {
		volume := deploymentplan.Map(item)
		if volume["name"] != selected["name"] {
			continue
		}
		kind, source := "ConfigMap", deploymentplan.Map(volume["configMap"])
		if source == nil {
			kind, source = "Secret", deploymentplan.Map(volume["secret"])
		}
		if source == nil {
			break
		}
		name := deploymentplan.String(source["name"])
		if kind == "Secret" {
			name = deploymentplan.String(source["secretName"])
		}
		key := relative
		if source["items"] != nil {
			key = ""
			for _, raw := range certIssuerObjectList(source["items"]) {
				entry := deploymentplan.Map(raw)
				if entry["path"] == relative {
					if key != "" {
						return environmentPublicFileRef{}, errors.New("duplicate public manifest projection")
					}
					key = deploymentplan.String(entry["key"])
				}
			}
		}
		if name != "" && key != "" && !strings.Contains(key, "/") {
			return environmentPublicFileRef{kind, name, key}, nil
		}
	}
	return environmentPublicFileRef{}, errors.New("manifest must have an explicit planned Secret/ConfigMap file owner")
}

func (b environmentKubernetesBackend) bindOriginalPublicManifest(ctx context.Context, plan *deploymentplan.Plan, object, container deploymentplan.Object, path string) ([]byte, environmentPublicFileRef, func(context.Context) error, error) {
	ref, err := environmentPublicFileReference(environmentPKIPod(object), container, path)
	if err != nil {
		return nil, ref, nil, crlInspectionBlocker(deploymentplan.ID(object), "CRL manifest mount is unresolved or unsupported")
	}
	id := ref.Kind + "/" + deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"]) + "/" + ref.Name
	for _, resource := range plan.Resources {
		if resource.ID != id {
			continue
		}
		if resource.UID == "" || resource.Action == "create" || resource.Action == "retire" {
			break
		}
		current, err := b.Read(ctx, id)
		if err != nil || current == nil {
			break
		}
		if _, err := resource.Body(current); err != nil {
			break
		}
		wanted := deploymentplan.String(deploymentplan.Map(resource.Desired["data"])[ref.Key])
		original := deploymentplan.String(deploymentplan.Map(current["data"])[ref.Key])
		if wanted == "" || wanted != original || len(wanted) > 2<<20 {
			break
		}
		raw := []byte(wanted)
		if ref.Kind == "Secret" {
			raw, err = base64.StdEncoding.Strict().DecodeString(wanted)
			if err != nil {
				break
			}
		}
		version := deploymentplan.String(deploymentplan.Map(current["metadata"])["resourceVersion"])
		if version == "" {
			break
		}
		return raw, ref, func(ctx context.Context) error {
			fresh, err := b.Read(ctx, id)
			if err != nil || fresh == nil || deploymentplan.Map(fresh["metadata"])["resourceVersion"] != version {
				return crlInspectionBlocker(id, "original CRL manifest changed during inspection")
			}
			if _, err := resource.Body(fresh); err != nil {
				return crlInspectionBlocker(id, "original CRL manifest owner drifted")
			}
			return nil
		}, nil
	}
	return nil, ref, nil, crlInspectionBlocker(id, "original owned CRL manifest is unavailable, changed or differs from the target plan")
}

func rootInspectionBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.roots", Resource: resource, Cause: cause, Next: "restore the original durable Root-removal state, reviewed provisioned Root-only bundle and configured Root authority; reconcile the current authenticated policy and actual consumer receipt through managed lifecycle, then repeat complete preflight; do not add trust anchors or create receipts to pass"}
}

func environmentRootTrustSettings(manifest, domain string) (string, string, string) {
	if manifest == "VIDEO_CLOUD_AUTH_CRL_MANIFEST" {
		prefix := "VIDEO_CLOUD_AUTH_" + strings.ToUpper(domain) + "_ROOT_TRUST"
		roots := prefix + "_ROOTS"
		if domain == "device" {
			roots = "VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"
		}
		return prefix + "_ROOT_ID", prefix + "_STATE", roots
	}
	prefix := strings.TrimSuffix(manifest, "_SERVER_CRL_MANIFEST")
	switch domain {
	case "device", "app":
		prefix = "PKI_BROKER_" + strings.ToUpper(domain) + "_ROOT"
	case "service":
		prefix += "_SERVICE_ROOT"
	case "mqtt":
		prefix += "_ROOT"
	case "openbao_tls":
		prefix += "_SERVER_ROOT"
	default:
		return "", "", ""
	}
	return prefix + "_ID", prefix + "_STATE", prefix + "S"
}
