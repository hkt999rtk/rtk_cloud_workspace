package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The create plan freezes native requests before allocating anything. Secret
// material is supplied separately from the durable selected create session.
type environmentCreateProviderResource struct {
	Key    string         `json:"key"`
	Kind   string         `json:"kind"`
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body"`
	Name   string         `json:"name"`
	Region string         `json:"region"`
}

type environmentCreateProviderPlan struct {
	Environment                string                              `json:"environment"`
	Stack                      string                              `json:"stack"`
	Resources                  []environmentCreateProviderResource `json:"resources"`
	AdditionalActiveServices   int                                 `json:"additional_active_services"`
	WorkloadAllocations        int                                 `json:"workload_allocations"`
	SSHKeySHA256               string                              `json:"ssh_key_sha256"`
	HostIdentityPolicy         string                              `json:"host_identity_policy"`
	HostIdentityTemplateSHA256 string                              `json:"host_identity_template_sha256"`
	TurnRegistrarImage         string                              `json:"turn_registrar_image,omitempty"`
	TurnRegistrarSHA256        string                              `json:"turn_registrar_sha256,omitempty"`
}

type environmentCreateProviderReceipt struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	NativeID   string `json:"native_id"`
	Name       string `json:"name"`
	Region     string `json:"region"`
	PublicIP   string `json:"public_ip,omitempty"`
	PrivateIP  string `json:"private_ip,omitempty"`
	S3Endpoint string `json:"s3_endpoint,omitempty"`
	Status     string `json:"status,omitempty"`
}

type environmentCreateProviderHTTPError struct{ Status int }

func (e *environmentCreateProviderHTTPError) Error() string {
	return fmt.Sprintf("native provider request returned HTTP %d", e.Status)
}

// The default transport and API root remain fixed in real runs. Test servers
// use the existing explicit isolated test-mode API override.
func environmentCreateProviderRequest(ctx context.Context, token, method, path string, body any) (output []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
		return nil, errors.New("selected provider credential and bounded native path required")
	}
	root := "https://api.linode.com/v4"
	if rtkCloudTestMode() {
		root = firstNonEmpty(strings.TrimRight(os.Getenv("RTK_CLOUD_LINODE_API_ROOT"), "/"), root)
	}
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, errors.New("native request cannot be encoded")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, root+path, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("native request cannot be initialized")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	measured := beginDeploymentQuery(ctx, "provider-http")
	defer func() { measured(resultErr) }()
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("native provider response unavailable; mutation outcome requires journal reconciliation")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &environmentCreateProviderHTTPError{response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, errors.New("bounded native provider response cannot be read; mutation outcome requires journal reconciliation")
	}
	return raw, nil
}

// Complete, stable pagination is required to claim absence or account capacity.
func environmentCreateProviderList(ctx context.Context, token, path string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	seen := map[string]bool{}
	pages, results := -1, -1
	for page := 1; ; page++ {
		raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, fmt.Sprintf("%s?page_size=500&page=%d", path, page), nil)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data    []json.RawMessage `json:"data"`
			Page    int               `json:"page"`
			Pages   *int              `json:"pages"`
			Results *int              `json:"results"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Data == nil || response.Pages == nil || response.Results == nil || response.Page != page || *response.Pages < 0 || *response.Pages > 1000 || *response.Results < 0 {
			return nil, errors.New("native inventory pagination or result count is incomplete")
		}
		if page == 1 {
			pages, results = *response.Pages, *response.Results
		} else if pages != *response.Pages || results != *response.Results {
			return nil, errors.New("native inventory changed during pagination")
		}
		if pages == 0 && (page != 1 || results != 0 || len(response.Data) != 0) || pages > 0 && page > pages {
			return nil, errors.New("native inventory pagination is inconsistent")
		}
		for _, item := range response.Data {
			var parsed map[string]any
			if json.Unmarshal(item, &parsed) != nil || len(parsed) == 0 {
				return nil, errors.New("native inventory includes an invalid object")
			}
			identity := deploymentplan.Digest(parsed)
			numericResource := path == "/lke/clusters" || path == "/linode/instances" || path == "/networking/firewalls" || path == "/vpcs" || path == "/volumes" || path == "/nodebalancers"
			if numericResource {
				id, ok := parsed["id"].(float64)
				if !ok || id <= 0 || id != float64(int64(id)) || deploymentplan.String(parsed["label"]) == "" {
					return nil, errors.New("native inventory omitted a positive resource ID or label")
				}
			}
			if path == "/object-storage/buckets" && (deploymentplan.String(parsed["label"]) == "" || firstNonEmpty(deploymentplan.String(parsed["region"]), deploymentplan.String(parsed["cluster"])) == "") {
				return nil, errors.New("native bucket inventory omitted exact region and label")
			}
			if id, exists := parsed["id"]; exists {
				identity = fmt.Sprint(id)
				if identity == "" || identity == "0" || identity == "<nil>" {
					return nil, errors.New("native inventory omitted a resource ID")
				}
			}
			if seen[identity] {
				return nil, errors.New("native inventory repeats a resource identity")
			}
			seen[identity] = true
			all = append(all, item)
		}
		if pages == 0 || page == pages {
			if len(all) != results {
				return nil, errors.New("native inventory does not contain all reported resources")
			}
			return all, nil
		}
	}
}

func environmentCreateProviderValues(cfg deploymentConfig) map[string]string {
	v := appendMap(appendMap(cfg.Values, cfg.AdapterValues), cfg.AdapterResolved)
	v = appendMap(v, deploymentLegacyLKEValues(v, cfg.Environment))
	v["CLOUD_ENV_NAME"], v["CLOUD_STACK_NAME"] = cfg.Environment, cfg.Values["CLOUD_STACK_NAME"]
	return v
}

func createProviderBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.create.provider", Cause: cause, Next: "verify the selected environment, exact provider requests, complete native inventory and original create journal before retrying; never adopt by name, replace identity or repeat an uncertain allocation"}
}

func buildEnvironmentCreateProviderPlan(ctx context.Context, cfg deploymentConfig, operator map[string]string) (environmentCreateProviderPlan, error) {
	plan := environmentCreateProviderPlan{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], HostIdentityPolicy: environmentCreateHostIdentityPolicy, HostIdentityTemplateSHA256: environmentPreflightBytesSHA256([]byte(environmentCreateHostIdentityTemplate))}
	if cfg.Adapter != "lke" || plan.Stack != "video-cloud-"+cfg.Environment || operator["LINODE_TOKEN"] == "" {
		return plan, createProviderBlocker("an exact maintained LKE environment and selected provider credentials are required")
	}
	v := environmentCreateProviderValues(cfg)
	region := v["LKE_REGION"]
	if region == "" {
		return plan, createProviderBlocker("the maintained provider region is unavailable")
	}
	publicKey := strings.TrimSpace(operator["DEPLOYMENT_SSH_PUBLIC_KEY"])
	if publicKey == "" {
		return plan, &deploymentplan.Blocker{ID: "deployment.create.inputs", Resource: "operator/ssh/id_ed25519.pub", Cause: "selected canonical deployment SSH key pair is missing; fresh edge and TURN hosts require an explicit initial management identity", Next: "provide the selected environment's private 0600 operator/ssh/id_ed25519 and matching id_ed25519.pub, or explicitly authorize initialization of a new deployment key pair; never recover another environment's key as a fallback"}
	}
	if strings.ContainsAny(publicKey, "\r\n") {
		return plan, createProviderBlocker("the selected deployment SSH public key is unavailable or invalid")
	}
	_, _, _, trailing, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || len(bytes.TrimSpace(trailing)) != 0 {
		return plan, createProviderBlocker("the selected deployment SSH public key is invalid")
	}
	keyHash := sha256.Sum256([]byte(publicKey))
	plan.SSHKeySHA256 = hex.EncodeToString(keyHash[:])
	versions, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], "/lke/versions")
	if err != nil {
		return plan, createProviderBlocker("complete supported Kubernetes versions cannot be read")
	}
	var supported []string
	for _, raw := range versions {
		var item struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &item) != nil || !validEnvironmentCreateKubernetesVersion(item.ID) {
			return plan, createProviderBlocker("supported Kubernetes version inventory is invalid")
		}
		supported = append(supported, item.ID)
	}
	sort.Slice(supported, func(i, j int) bool {
		a, b := strings.Split(supported[i], "."), strings.Split(supported[j], ".")
		for k := 0; k < len(a) && k < len(b); k++ {
			x, _ := strconv.Atoi(a[k])
			y, _ := strconv.Atoi(b[k])
			if x != y {
				return x > y
			}
		}
		return supported[i] > supported[j]
	})
	version := v["LKE_K8S_VERSION"]
	if version == "" && len(supported) > 0 {
		version = supported[0]
	}
	if !createProviderContains(supported, version) {
		return plan, createProviderBlocker("the frozen Kubernetes version is not supported by the provider")
	}
	tags := []string{"rtk-cloud", plan.Stack, cfg.Environment}
	var pools []map[string]any
	for _, class := range []string{"GENERAL", "BROKER", "DATABASE"} {
		count, err := nonNegativeIntValue("NODE_CLASS_"+class+"_EFFECTIVE_COUNT", v["NODE_CLASS_"+class+"_EFFECTIVE_COUNT"])
		if err != nil {
			return plan, createProviderBlocker("maintained node class allocation count is invalid")
		}
		if count == 0 {
			continue
		}
		nodeType := v["LKE_"+class+"_NODE_TYPE"]
		if _, known := lkeNodeTypeShape(nodeType); !known {
			return plan, createProviderBlocker("maintained node class instance type is invalid")
		}
		poolLabel := plan.Stack + "-" + strings.ToLower(class)
		if len(poolLabel) > 32 {
			return plan, createProviderBlocker("environment-bearing LKE pool label exceeds the provider's 32-character limit")
		}
		pool := map[string]any{"type": nodeType, "count": count, "label": poolLabel, "labels": map[string]string{"rtk.io/node-class": strings.ToLower(class)}}
		if class == "DATABASE" {
			pool["taints"] = []map[string]string{{"key": "rtk.io/node-class", "value": "database", "effect": "NoSchedule"}}
		}
		pools = append(pools, pool)
		plan.AdditionalActiveServices += count
	}
	if len(pools) == 0 {
		return plan, createProviderBlocker("fresh LKE creation requires a nonempty exact node pool inventory")
	}
	label := plan.Stack + "-lke"
	plan.Resources = append(plan.Resources, environmentCreateProviderResource{"lke", "lke", http.MethodPost, "/lke/clusters", map[string]any{"label": label, "region": region, "k8s_version": version, "tags": tags, "node_pools": pools}, label, region})
	edgeCount, err := positiveIntValue("EDGE_REPLICAS", v["EDGE_REPLICAS"])
	if err != nil || edgeCount != 1 {
		return plan, createProviderBlocker("external HAProxy creation supports exactly one maintained edge replica")
	}
	turnCount, err := positiveIntValue("TURN_REPLICAS", v["TURN_REPLICAS"])
	if err != nil || turnCount > 5 {
		return plan, createProviderBlocker("maintained TURN replicas must be between one and five")
	}
	for index := 0; index <= turnCount; index++ {
		key, role, vmName, vmType, image := "edge", "edge-haproxy", plan.Stack+"-edge-haproxy-01", firstNonEmpty(v["LKE_EDGE_HAPROXY_TYPE"], "g6-standard-2"), firstNonEmpty(v["LKE_EDGE_HAPROXY_IMAGE"], "linode/ubuntu24.04")
		vmRegion := firstNonEmpty(v["LKE_EDGE_HAPROXY_REGION"], region)
		if index > 0 {
			key, role, vmName = fmt.Sprintf("turn-%02d", index), "coturn-vm", fmt.Sprintf("%s-turn%02d", plan.Stack, index)
			vmType, image = firstNonEmpty(v["LKE_COTURN_VM_TYPE"], "g6-nanode-1"), firstNonEmpty(v["LKE_COTURN_VM_IMAGE"], "linode/ubuntu24.04")
			vmRegion = firstNonEmpty(v["LKE_COTURN_VM_REGION"], region)
		}
		// VM availability is checked against the exact native type endpoint in
		// qualification. The worker capacity catalog does not include Nanodes.
		if !secretEnvironmentPattern.MatchString(vmType) || len(vmType) > 64 {
			return plan, createProviderBlocker("maintained edge or TURN instance type is invalid")
		}
		body := map[string]any{"label": vmName, "region": vmRegion, "type": vmType, "image": image, "authorized_keys": []string{publicKey}, "tags": append(append([]string{}, tags...), role)}
		if index == 0 {
			body["private_ip"] = true
		}
		plan.Resources = append(plan.Resources, environmentCreateProviderResource{key, "instance", http.MethodPost, "/linode/instances", body, vmName, vmRegion})
		plan.AdditionalActiveServices++
	}
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		if target.Bucket == "" || target.Region == "" {
			return plan, createProviderBlocker("configured runtime and OTA bucket identities are required")
		}
		if cfg.Storage.RuntimeMedia.Bucket == cfg.Storage.OTAFirmware.Bucket {
			return plan, createProviderBlocker("fresh creation requires the maintained separate OTA E3 storage lifecycle; legacy shared activation is not qualified")
		}
		body := map[string]any{"label": target.Bucket, "region": target.Region}
		if target.Purpose == "ota-firmware" {
			body["endpoint_type"] = "E3"
		}
		plan.Resources = append(plan.Resources, environmentCreateProviderResource{"bucket-" + target.Purpose, "bucket", http.MethodPost, "/object-storage/buckets", body, target.Bucket, target.Region})
	}
	return plan, nil
}

func validEnvironmentCreateKubernetesVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || strconv.Itoa(n) != part {
			return false
		}
	}
	return true
}

func qualifyEnvironmentCreateProviders(ctx context.Context, cfg deploymentConfig, plan environmentCreateProviderPlan, operator map[string]string) error {
	if err := validateEnvironmentCreateProviderPlan(cfg, plan); err != nil {
		return err
	}
	token := operator["LINODE_TOKEN"]
	if token == "" {
		return createProviderBlocker("selected provider credential is unavailable")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	if _, err := environmentCreateSSHIdentity(store, plan); err != nil {
		return err
	}
	if err := qualifyEnvironmentCreateStorage(ctx, cfg, operator); err != nil {
		return err
	}
	if err := environmentCreateStorageAllocationAbsent(cfg); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, resource := range plan.Resources {
		names[resource.Name] = true
	}
	current := 0
	for _, path := range []string{"/lke/clusters", "/linode/instances", "/networking/firewalls", "/vpcs", "/object-storage/buckets", "/volumes", "/nodebalancers"} {
		items, err := environmentCreateProviderList(ctx, token, path)
		if err != nil {
			return createProviderBlocker("complete native provider inventory cannot be verified")
		}
		if path == "/linode/instances" || path == "/volumes" || path == "/nodebalancers" {
			current += len(items)
		}
		for _, raw := range items {
			var item struct {
				Label string   `json:"label"`
				Tags  []string `json:"tags"`
			}
			if json.Unmarshal(raw, &item) != nil {
				return createProviderBlocker("native resource identity inventory is invalid")
			}
			if names[item.Label] || strings.HasPrefix(item.Label, plan.Stack+"-") || createProviderContains(item.Tags, plan.Stack) {
				return createProviderBlocker("a selected stack provider resource already exists; fresh creation cannot adopt or replace it")
			}
		}
	}
	account, err := readLKEAccountState(cfg.Environment, true)
	if err != nil || validateActiveServiceLimit(account["LKE_ACTIVE_SERVICE_LIMIT"]) != nil {
		return createProviderBlocker("the original selected operator-confirmed account quota is unavailable")
	}
	if account["LKE_ACTIVE_SERVICE_LIMIT"] != "unlimited" {
		limit, _ := strconv.Atoi(account["LKE_ACTIVE_SERVICE_LIMIT"])
		if current+plan.AdditionalActiveServices+plan.WorkloadAllocations > limit {
			return createProviderBlocker(fmt.Sprintf("account capacity cannot cover fresh allocations (current=%d provider=%d workload=%d limit=%d)", current, plan.AdditionalActiveServices, plan.WorkloadAllocations, limit))
		}
	}
	checkedRegions, checkedTypes, checkedImages := map[string]bool{}, map[string]bool{}, map[string]bool{}
	endpoints, err := environmentCreateProviderList(ctx, token, "/object-storage/endpoints")
	if err != nil {
		return createProviderBlocker("complete storage endpoint capability inventory cannot be read")
	}
	for _, resource := range plan.Resources {
		if !checkedRegions[resource.Region+"/"+resource.Kind] {
			raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, "/regions/"+url.PathEscape(resource.Region), nil)
			var region struct {
				ID, Status   string
				Capabilities []string
			}
			if err != nil || json.Unmarshal(raw, &region) != nil || region.ID != resource.Region || region.Status != "ok" {
				return createProviderBlocker("a frozen provider region is unavailable")
			}
			needed := "Linodes"
			if resource.Kind == "lke" {
				needed = "Kubernetes"
			}
			if resource.Kind == "bucket" {
				needed = "Object Storage"
			}
			if !createProviderContains(region.Capabilities, needed) || resource.Kind == "instance" && !createProviderContains(region.Capabilities, "Metadata") {
				return createProviderBlocker("a frozen provider region lacks the required capability")
			}
			checkedRegions[resource.Region+"/"+resource.Kind] = true
		}
		var types []string
		if resource.Kind == "lke" {
			for _, pool := range createProviderObjectList(resource.Body["node_pools"]) {
				types = append(types, deploymentplan.String(pool["type"]))
			}
		}
		if resource.Kind == "instance" {
			types = append(types, deploymentplan.String(resource.Body["type"]))
		}
		for _, kind := range types {
			if checkedTypes[kind] {
				continue
			}
			raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, "/linode/types/"+url.PathEscape(kind), nil)
			var found struct {
				ID string `json:"id"`
			}
			if err != nil || json.Unmarshal(raw, &found) != nil || found.ID != kind {
				return createProviderBlocker("a frozen Linode instance type is unavailable")
			}
			checkedTypes[kind] = true
		}
		if resource.Kind == "instance" {
			image := deploymentplan.String(resource.Body["image"])
			if !checkedImages[image] {
				raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, "/images/"+url.PathEscape(image), nil)
				var found struct {
					ID           string   `json:"id"`
					Capabilities []string `json:"capabilities"`
				}
				if err != nil || json.Unmarshal(raw, &found) != nil || found.ID != image || !createProviderContains(found.Capabilities, "cloud-init") {
					return createProviderBlocker("a frozen VM image is unavailable")
				}
				checkedImages[image] = true
			}
		}
		if resource.Kind == "bucket" {
			available := false
			for _, raw := range endpoints {
				var endpoint struct {
					Region       string `json:"region"`
					EndpointType string `json:"endpoint_type"`
				}
				if json.Unmarshal(raw, &endpoint) != nil {
					return createProviderBlocker("storage endpoint capability inventory is invalid")
				}
				if endpoint.Region == resource.Region && (resource.Body["endpoint_type"] == nil || resource.Body["endpoint_type"] == endpoint.EndpointType) {
					available = true
				}
			}
			if !available {
				return createProviderBlocker("a frozen storage region or required OTA E3 endpoint type is unavailable")
			}
		}
	}
	return ctx.Err()
}

func validateEnvironmentCreateProviderPlan(cfg deploymentConfig, plan environmentCreateProviderPlan) error {
	if cfg.Adapter != "lke" || plan.Environment != cfg.Environment || plan.Stack != cfg.Values["CLOUD_STACK_NAME"] || plan.Stack != "video-cloud-"+cfg.Environment || len(plan.Resources) < 5 || plan.AdditionalActiveServices < 3 || plan.WorkloadAllocations < 0 || len(plan.SSHKeySHA256) != 64 || plan.HostIdentityPolicy != environmentCreateHostIdentityPolicy || plan.HostIdentityTemplateSHA256 != environmentPreflightBytesSHA256([]byte(environmentCreateHostIdentityTemplate)) {
		return createProviderBlocker("the frozen native provider plan is not bound to the selected environment")
	}
	seen := map[string]bool{}
	allocationCount := 0
	for _, resource := range plan.Resources {
		if seen[resource.Key] || resource.Key == "" || resource.Name == "" || resource.Region == "" || resource.Method != http.MethodPost || resource.Body["label"] != resource.Name || resource.Body["region"] != resource.Region || resource.Body["root_pass"] != nil {
			return createProviderBlocker("a frozen provider request is duplicated, invalid or contains private execution material")
		}
		seen[resource.Key] = true
		switch resource.Kind {
		case "lke":
			if resource.Key != "lke" || resource.Path != "/lke/clusters" || resource.Name != plan.Stack+"-lke" || !validEnvironmentCreateKubernetesVersion(deploymentplan.String(resource.Body["k8s_version"])) {
				return createProviderBlocker("an invalid native cluster request is present")
			}
			pools := createProviderObjectList(resource.Body["node_pools"])
			if len(pools) == 0 {
				return createProviderBlocker("the native cluster node pool inventory is invalid")
			}
			for _, pool := range pools {
				count, err := strconv.Atoi(fmt.Sprint(pool["count"]))
				if err != nil || count <= 0 || deploymentplan.String(pool["type"]) == "" {
					return createProviderBlocker("the native cluster node pool allocation is invalid")
				}
				allocationCount += count
			}
		case "instance":
			if resource.Path != "/linode/instances" || !strings.HasPrefix(resource.Name, plan.Stack+"-") || deploymentplan.String(resource.Body["type"]) == "" || deploymentplan.String(resource.Body["image"]) == "" || len(createProviderStringList(resource.Body["authorized_keys"])) != 1 {
				return createProviderBlocker("an invalid native instance request is present")
			}
			allocationCount++
		case "bucket":
			if resource.Path != "/object-storage/buckets" {
				return createProviderBlocker("an invalid native bucket request is present")
			}
		default:
			return createProviderBlocker("a native provider request kind is unsupported")
		}
		if resource.Kind != "bucket" && (!createProviderContains(createProviderStringList(resource.Body["tags"]), plan.Stack) || !createProviderContains(createProviderStringList(resource.Body["tags"]), plan.Environment)) {
			return createProviderBlocker("native provider ownership tags are incomplete")
		}
	}
	if !seen["lke"] || !seen["edge"] || !seen["turn-01"] || !seen["bucket-runtime-media"] || !seen["bucket-ota-firmware"] || allocationCount != plan.AdditionalActiveServices {
		return createProviderBlocker("the complete fresh provider inventory or exact allocation count is unavailable")
	}
	return nil
}

// Callers must durably record pending intent before this function and persist
// its returned native receipt before polling. It never discovers by label,
// retries a POST, waits for boot, writes state or invents omitted response IDs.
func executeEnvironmentCreateProviderResource(ctx context.Context, cfg deploymentConfig, resource environmentCreateProviderResource, operator, material map[string]string) (environmentCreateProviderReceipt, error) {
	if resource.Method != http.MethodPost || resource.Body["label"] != resource.Name || resource.Body["region"] != resource.Region || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return environmentCreateProviderReceipt{}, createProviderBlocker("the exact frozen provider request is unavailable")
	}
	if (resource.Kind != "lke" || resource.Path != "/lke/clusters") && (resource.Kind != "instance" || resource.Path != "/linode/instances") && (resource.Kind != "bucket" || resource.Path != "/object-storage/buckets") {
		return environmentCreateProviderReceipt{}, createProviderBlocker("unsupported native create endpoint")
	}
	body := map[string]any{}
	for key, value := range resource.Body {
		body[key] = value
	}
	if resource.Kind == "instance" {
		cloudInit, err := base64.StdEncoding.DecodeString(material["user_data"])
		if err != nil || validateEnvironmentCreateHostIdentityMaterial(cloudInit, material["host_public_key"]) != nil {
			return environmentCreateProviderReceipt{}, createProviderBlocker("persisted selected create-session SSH host identity material is unavailable or invalid")
		}
		body["metadata"] = map[string]string{"user_data": material["user_data"]}
		password := material["root_pass"]
		if password != "" {
			if len(password) < 32 || strings.ContainsAny(password, "\r\n") {
				return environmentCreateProviderReceipt{}, createProviderBlocker("persisted selected create-session VM bootstrap material is invalid")
			}
			body["root_pass"] = password
		} else if len(createProviderStringList(body["authorized_keys"])) != 1 {
			return environmentCreateProviderReceipt{}, createProviderBlocker("selected create-session VM authentication is unavailable")
		}
	}
	raw, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], resource.Method, resource.Path, body)
	if err != nil {
		return environmentCreateProviderReceipt{}, err
	}
	return decodeEnvironmentCreateProviderReceipt(resource, raw)
}

func validateEnvironmentCreateHostIdentityMaterial(raw []byte, public string) error {
	var cloud struct {
		SSHKeys struct {
			Private string `yaml:"ed25519_private"`
			Public  string `yaml:"ed25519_public"`
		} `yaml:"ssh_keys"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if len(raw) == 0 || len(raw) > 32<<10 || !bytes.HasPrefix(raw, []byte("#cloud-config\n")) || decoder.Decode(&cloud) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid bounded host-key cloud-init material")
	}
	private, err := ssh.ParseRawPrivateKey([]byte(cloud.SSHKeys.Private))
	if err != nil {
		return errors.New("invalid cloud-init host private key")
	}
	var signer ssh.Signer
	if signer, err = ssh.NewSignerFromKey(private); err != nil {
		return errors.New("invalid cloud-init host signer")
	}
	actual := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 || actual != cloud.SSHKeys.Public || actual != public {
		return errors.New("cloud-init host key does not match its recorded public identity")
	}
	return nil
}

func decodeEnvironmentCreateProviderReceipt(resource environmentCreateProviderResource, raw []byte) (environmentCreateProviderReceipt, error) {
	receipt := environmentCreateProviderReceipt{Key: resource.Key, Kind: resource.Kind}
	var found struct {
		ID           int      `json:"id"`
		Label        string   `json:"label"`
		Region       string   `json:"region"`
		Cluster      string   `json:"cluster"`
		IPv4         []string `json:"ipv4"`
		Tags         []string `json:"tags"`
		S3Endpoint   string   `json:"s3_endpoint"`
		EndpointType string   `json:"endpoint_type"`
		Status       string   `json:"status"`
	}
	if json.Unmarshal(raw, &found) != nil {
		return receipt, createProviderBlocker("native create response is invalid; preserve pending intent and inspect the original allocation")
	}
	if found.ID > 0 {
		receipt.NativeID = strconv.Itoa(found.ID)
	}
	if resource.Kind == "bucket" && found.Label == resource.Name && firstNonEmpty(found.Region, found.Cluster) == resource.Region {
		receipt.NativeID = resource.Region + "/" + resource.Name
	}
	if found.Label != resource.Name || firstNonEmpty(found.Region, found.Cluster) != resource.Region {
		return receipt, createProviderBlocker("provider response does not verify the exact created resource identity; preserve pending intent and inspect the original allocation")
	}
	receipt.Name, receipt.Region, receipt.Status = found.Label, resource.Region, found.Status
	if resource.Kind == "bucket" {
		receipt.NativeID = resource.Region + "/" + resource.Name
		endpoint, err := normalizeLinodeS3Endpoint(found.S3Endpoint)
		if err != nil || resource.Body["endpoint_type"] == "E3" && found.EndpointType != "E3" {
			return receipt, createProviderBlocker("created storage endpoint or required OTA E3 capability cannot be verified")
		}
		receipt.S3Endpoint = endpoint
	} else {
		if found.ID <= 0 {
			return receipt, createProviderBlocker("provider create response omitted its native ID; preserve pending intent and never repeat the allocation")
		}
		receipt.NativeID = strconv.Itoa(found.ID)
		for _, tag := range createProviderStringList(resource.Body["tags"]) {
			if !createProviderContains(found.Tags, tag) {
				return receipt, createProviderBlocker("provider response omitted the exact created resource ownership tags")
			}
		}
		for _, address := range found.IPv4 {
			ip := net.ParseIP(address)
			if ip == nil || ip.To4() == nil {
				return receipt, createProviderBlocker("provider response contains an invalid native IPv4 address")
			}
			if isPrivateNetIP(ip) {
				if receipt.PrivateIP == "" {
					receipt.PrivateIP = address
				}
			} else if receipt.PublicIP == "" {
				receipt.PublicIP = address
			}
		}
	}
	return receipt, nil
}

func createProviderStringList(value any) []string {
	if list, ok := value.([]string); ok {
		return list
	}
	var list []string
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if s, ok := item.(string); ok {
				list = append(list, s)
			}
		}
	}
	return list
}

func createProviderObjectList(value any) []map[string]any {
	if list, ok := value.([]map[string]any); ok {
		return list
	}
	if values, ok := value.([]any); ok {
		list := make([]map[string]any, 0, len(values))
		for _, item := range values {
			object, ok := item.(map[string]any)
			if !ok {
				return nil
			}
			list = append(list, object)
		}
		return list
	}
	return nil
}

func createProviderContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func verifyEnvironmentCreateProviderResource(ctx context.Context, cfg deploymentConfig, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt, operator map[string]string) (environmentCreateProviderReceipt, bool, error) {
	if receipt.Key != resource.Key || receipt.Kind != resource.Kind || receipt.Name != resource.Name || receipt.Region != resource.Region || receipt.NativeID == "" || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return receipt, false, createProviderBlocker("original exact resource receipt does not match the selected creation session")
	}
	path := resource.Path + "/" + url.PathEscape(receipt.NativeID)
	if resource.Kind == "bucket" {
		path = resource.Path + "/" + url.PathEscape(resource.Region) + "/" + url.PathEscape(resource.Name)
	}
	if resource.Kind != "bucket" {
		if id, err := strconv.Atoi(receipt.NativeID); err != nil || id <= 0 || strconv.Itoa(id) != receipt.NativeID {
			return receipt, false, createProviderBlocker("original provider receipt native ID is invalid")
		}
	}
	raw, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], http.MethodGet, path, nil)
	if err != nil {
		return receipt, false, err
	}
	actual, err := decodeEnvironmentCreateProviderReceipt(resource, raw)
	if err != nil {
		return receipt, false, err
	}
	if actual.NativeID != receipt.NativeID {
		return receipt, false, createProviderBlocker("native readback no longer matches the original creation allocation")
	}
	ready := resource.Kind != "instance" || actual.Status == "running" && actual.PublicIP != "" && (resource.Key != "edge" || actual.PrivateIP != "")
	return actual, ready, nil
}

// Only the exact freshly recorded cluster ID may supply the new canonical
// kubeconfig. The caller controls deadline/polling and privately persists it.
func fetchEnvironmentCreateKubeconfig(ctx context.Context, cfg deploymentConfig, receipt environmentCreateProviderReceipt, operator map[string]string) ([]byte, error) {
	if receipt.Kind != "lke" || receipt.Key != "lke" || receipt.Name != cfg.Values["CLOUD_STACK_NAME"]+"-lke" || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return nil, createProviderBlocker("the original newly created cluster receipt is required for Kubernetes access")
	}
	id, err := strconv.Atoi(receipt.NativeID)
	if err != nil || id <= 0 || strconv.Itoa(id) != receipt.NativeID {
		return nil, createProviderBlocker("the original new cluster native ID is invalid")
	}
	raw, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], http.MethodGet, "/lke/clusters/"+receipt.NativeID+"/kubeconfig", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, createProviderBlocker("the exact new cluster kubeconfig response is invalid")
	}
	kubeconfig, err := base64.StdEncoding.DecodeString(response.Kubeconfig)
	if err != nil || len(kubeconfig) == 0 || len(kubeconfig) > 1<<20 {
		return nil, createProviderBlocker("the exact new cluster embedded kubeconfig is unavailable")
	}
	if _, err := newEnvironmentKubernetesBackend(string(kubeconfig), cfg.Environment); err != nil {
		return nil, createProviderBlocker("the exact new cluster kubeconfig cannot initialize the selected Kubernetes identity")
	}
	return kubeconfig, nil
}
