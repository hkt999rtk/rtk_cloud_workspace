package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Registration is logged once; its current native lease, not the age of that
// startup log, proves an already-started registrar. No activation is replayed.
func resumeEnvironmentCreateTURNActivation(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, e *environmentCreateExecution, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt, key string, intent, proof []byte) (bool, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return false, nil
	}
	verify := func(ctx context.Context) error {
		return verifyEnvironmentCreateTURNActivation(ctx, cfg, plan, store, e, resource, receipt, entry.StartedAt)
	}
	if entry.State != "pending" {
		return createNormalTailRecordedStage(ctx, e, key, "routing", intent, proof, verify)
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.Phase != "routing" || entry.IntentSHA256 != sha || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
		return true, createProviderRecoveryBlock("TURN activation differs from its original pending intent")
	}
	if err := e.Check(ctx); err != nil {
		return true, err
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	if err := e.RecordReceipt(key, proof); err != nil {
		return true, err
	}
	return true, e.CompleteEntry(key, time.Now().UTC())
}

func verifyEnvironmentCreateTURNActivation(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, e *environmentCreateExecution, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt, started time.Time) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	configured := createOpenBaoACKCompletionEntry(e.Journal, "configure-"+resource.Key)
	var original struct {
		Native           environmentCreateProviderReceipt `json:"native"`
		ConfigurationSHA string                           `json:"configuration_sha256"`
		RuntimeSHA       string                           `json:"runtime_sha256"`
	}
	if configured == nil || !environmentPreflightDigest.MatchString(configured.IntentSHA256) || configured.State != "completed" || configured.Phase != "routing" || configured.CompletedAt.After(started) || decodeEnvironmentJSON(configured.Receipt, &original) != nil || !environmentPreflightDigest.MatchString(original.ConfigurationSHA) || !environmentPreflightDigest.MatchString(original.RuntimeSHA) || deploymentplan.Digest(original.Native) != deploymentplan.Digest(receipt) {
		return createProviderRecoveryBlock("TURN activation lacks its original completed VM configuration and immutable registrar evidence")
	}
	actual, err := createNormalTailDataPlaneReadback(ctx, store, plan, e, resource, receipt)
	if err != nil {
		return err
	}
	if actual != original.RuntimeSHA {
		return createProviderRecoveryBlock("original TURN configuration or registrar binary changed")
	}
	if _, err := environmentCreatePinnedSSH(ctx, store, plan.Provider, environmentCreateHostMaterialPlan(e), resource, receipt, "set -eu\nsystemctl is-active coturn >/dev/null\nsystemctl is-active video-cloud-turnregistrar >/dev/null\n"); err != nil {
		return err
	}
	values := environmentCreateProviderValues(cfg)
	index, err := strconv.Atoi(strings.TrimPrefix(resource.Key, "turn-"))
	if err != nil || index < 1 || index > 5 {
		return createProviderRecoveryBlock("original TURN node index is invalid")
	}
	values = appendMap(values, map[string]string{"LKE_COTURN_VM_NAME": "turn0" + strconv.Itoa(index), "LKE_COTURN_VM_INDEX": strconv.Itoa(index), "LKE_COTURN_VM_REGION": receipt.Region})
	// Match the selected legacy installation scope; ambient LKE overrides are
	// neither the installed node identity nor authority to select another server.
	var endpoint, node, host string
	var version int
	environmentRenderMutex.Lock()
	clear := map[string]string{}
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if strings.HasPrefix(key, "LKE_") {
			clear[key] = ""
		}
	}
	restore := installAllCredentialEnvironment(appendMap(clear, values))
	endpoint, node, host = lkeTurnRegistryPublicURL(values), lkeCoturnVMName(values), lkeCoturnDomain(values)
	version, err = positiveIntValue("LKE_COTURN_SECRET_VERSION", firstNonEmpty(values["LKE_COTURN_SECRET_VERSION"], "1"))
	restore()
	environmentRenderMutex.Unlock()
	if err != nil {
		return createProviderRecoveryBlock("original TURN secret version is invalid")
	}
	auth, err := store.readRuntime("turn-registry-node-auth")
	if err != nil || auth == "" {
		return createProviderRecoveryBlock("original TURN registry authentication cannot be read")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return createProviderRecoveryBlock("selected TURN registry endpoint is invalid")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/turn/nodes/active"
	u.RawQuery = url.Values{"count": {"100"}, "region": {receipt.Region}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return createProviderRecoveryBlock("selected TURN registry request cannot be prepared")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(auth))
	io.WriteString(mac, "GET\n"+req.URL.RequestURI()+"\napi\n"+timestamp+"\n")
	req.Header.Set("X-Video-Cloud-Registry-Node-ID", "api")
	req.Header.Set("X-Video-Cloud-Registry-Timestamp", timestamp)
	req.Header.Set("X-Video-Cloud-Registry-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return createProviderRecoveryBlock("current TURN registry lease cannot be read; activation outcome remains unchanged")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return createProviderRecoveryBlock("current TURN registry lease authentication or read failed; activation outcome remains unchanged")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	var result struct {
		Nodes []struct {
			NodeID        string    `json:"node_id"`
			Region        string    `json:"region"`
			PublicHost    string    `json:"public_host"`
			UDPPort       int       `json:"udp_port"`
			TCPPort       int       `json:"tcp_port"`
			TLSPort       int       `json:"tls_port"`
			State         string    `json:"state"`
			SecretVersion int       `json:"secret_version"`
			LastError     string    `json:"last_error"`
			RegisteredAt  time.Time `json:"registered_at"`
			LastSeenAt    time.Time `json:"last_seen_at"`
		} `json:"nodes"`
	}
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil || result.Nodes == nil {
		return createProviderRecoveryBlock("current TURN registry lease response is unavailable or incomplete")
	}
	matches := 0
	for _, actual := range result.Nodes {
		if actual.NodeID != node {
			continue
		}
		matches++
		if actual.Region != receipt.Region || actual.PublicHost != host || actual.UDPPort != 3478 || actual.TCPPort != 3478 || actual.TLSPort != 0 || actual.SecretVersion != version || actual.State != "ACTIVE" || actual.LastError != "" || actual.RegisteredAt.IsZero() || actual.LastSeenAt.Before(actual.RegisteredAt) || actual.LastSeenAt.Before(started) {
			return createProviderRecoveryBlock("current TURN registration differs from its original node identity, endpoint or healthy lease")
		}
	}
	if matches != 1 {
		return createProviderRecoveryBlock("the original TURN node has no unique active registration; no activation was repeated")
	}
	return e.Check(ctx)
}
