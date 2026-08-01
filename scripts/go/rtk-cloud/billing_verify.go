package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type e2eStepSelection struct {
	Reset       bool
	Provision   bool
	Data        bool
	MQTT        bool
	RuntimeLogs bool
	BillingLog  bool
	BillingDB   bool
	Lifecycle   bool
}

func parseE2ESteps(raw string, skipRemove, skipProvision bool) (e2eStepSelection, error) {
	selection := e2eStepSelection{}
	items := strings.Split(strings.ToLower(strings.TrimSpace(raw)), ",")
	if len(items) == 0 || (len(items) == 1 && strings.TrimSpace(items[0]) == "") {
		items = []string{"all"}
	}
	for _, item := range items {
		switch strings.TrimSpace(item) {
		case "all":
			selection = e2eStepSelection{Reset: true, Provision: true, Data: true, MQTT: true, RuntimeLogs: true, BillingLog: true, BillingDB: true, Lifecycle: true}
		case "reset":
			selection.Reset = true
		case "provision":
			selection.Provision = true
		case "data", "setup-data":
			selection.Data = true
		case "mqtt":
			selection.MQTT = true
		case "runtime-logs", "logs":
			selection.RuntimeLogs = true
		case "billing", "billing-all":
			selection.BillingLog = true
			selection.BillingDB = true
		case "billing-log":
			selection.BillingLog = true
		case "billing-db", "ledger":
			selection.BillingDB = true
		case "lifecycle", "provisioning-lifecycle":
			selection.Lifecycle = true
		case "":
		default:
			return e2eStepSelection{}, fmt.Errorf("unsupported E2E step %q; use reset,provision,data,mqtt,runtime-logs,billing-log,billing-db,lifecycle", item)
		}
	}
	if skipRemove {
		selection.Reset = false
	}
	if skipProvision {
		selection.Provision = false
	}
	if !selection.Reset && !selection.Provision && !selection.Data && !selection.MQTT && !selection.RuntimeLogs && !selection.BillingLog && !selection.BillingDB && !selection.Lifecycle {
		return e2eStepSelection{}, errors.New("at least one E2E step is required")
	}
	return selection, nil
}

type billingVerifyCheck struct {
	Log bool
	DB  bool
}

type billingUsageLogEvent struct {
	Fields map[string]any `json:"fields"`
}

type billingUsageLogResponse struct {
	Events []billingUsageLogEvent `json:"events"`
}

type billingUsageSummary struct {
	UsageEvents    int    `json:"usage_events"`
	BrandCloudID   string `json:"brand_cloud_id"`
	PublishBytes   uint64 `json:"publish_bytes"`
	DeliveryBytes  uint64 `json:"delivery_bytes"`
	PublishCount   uint64 `json:"publish_count"`
	DeliveryCount  uint64 `json:"delivery_count"`
	UsageFacts     int    `json:"usage_facts"`
	LedgerPubBytes uint64 `json:"ledger_publish_bytes"`
	LedgerDelBytes uint64 `json:"ledger_delivery_bytes"`
}

func runStagingE2EBillingVerify(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-billing-verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	stackFlag := fs.String("stack", "", "staging stack name")
	testDataDB := fs.String("test-data-db", "", "load-test SQLite data database")
	mqttResults := fs.String("mqtt-results", "", "load-test results.json used to bound evidence time")
	outDir := fs.String("out-dir", "", "output directory")
	checks := fs.String("checks", "log,db", "comma-separated checks: log,db")
	timeout := fs.Duration("timeout", 2*time.Minute, "maximum wait for billing evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	if strings.TrimSpace(*testDataDB) == "" {
		return errors.New("--test-data-db is required")
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stack := firstNonEmpty(*stackFlag, envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	check, err := parseBillingVerifyChecks(*checks)
	if err != nil {
		return err
	}
	brandCloudID, err := billingBrandCloudID(*testDataDB)
	if err != nil {
		return err
	}
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	evidenceSince := time.Now().Add(-2 * time.Hour)
	if strings.TrimSpace(*mqttResults) != "" {
		if generatedAt, readErr := billingResultsGeneratedAt(*mqttResults); readErr == nil && !generatedAt.IsZero() {
			evidenceSince = generatedAt.Add(-2 * time.Hour)
		}
	}
	result := billingUsageSummary{BrandCloudID: brandCloudID}
	if check.Log {
		loggerEndpoint := strings.TrimRight(firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOGGER_ENDPOINT"), os.Getenv("CLOUD_LOGGER_ENDPOINT")), "/")
		if loggerEndpoint == "" {
			return errors.New("logger endpoint is required for billing log verification")
		}
		secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-logger", "cloud-logger-runtime", "RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN")
		if err != nil {
			return fmt.Errorf("read billing logger credential: %w", err)
		}
		billingToken, err := envValue(secretEnv, "RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN")
		if err != nil {
			return err
		}
		for {
			result, err = queryBillingUsageLogs(loggerEndpoint, billingToken, brandCloudID, evidenceSince, time.Now().Add(2*time.Minute), result)
			if err != nil {
				return err
			}
			if result.PublishBytes > 0 && result.DeliveryBytes > 0 && result.PublishCount > 0 && result.DeliveryCount > 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("billing log evidence not found for brand cloud %s", brandCloudID)
			}
			time.Sleep(2 * time.Second)
		}
	}
	if check.DB {
		for {
			facts, err := queryBillingUsageFacts(kubeconfig, stack, brandCloudID, evidenceSince)
			if err != nil {
				return err
			}
			result.UsageFacts = facts.Count
			result.LedgerPubBytes = facts.PublishBytes
			result.LedgerDelBytes = facts.DeliveryBytes
			if facts.Count > 0 && facts.PublishBytes > 0 && facts.DeliveryBytes > 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("billing ledger evidence not found for brand cloud %s", brandCloudID)
			}
			time.Sleep(2 * time.Second)
		}
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "staging-e2e", "billing-verify")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	resultFile := filepath.Join(*outDir, "summary.json")
	if err := writeJSON(resultFile, map[string]any{"overall": "pass", "checks": check, "generated_at": time.Now().UTC().Format(time.RFC3339), "summary": result}); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": "pass", "summary_file": resultFile})
}

func parseBillingVerifyChecks(raw string) (billingVerifyCheck, error) {
	check := billingVerifyCheck{}
	for _, item := range strings.Split(strings.ToLower(strings.TrimSpace(raw)), ",") {
		switch strings.TrimSpace(item) {
		case "", "all":
			check.Log = true
			check.DB = true
		case "log", "billing-log":
			check.Log = true
		case "db", "billing-db", "ledger":
			check.DB = true
		default:
			return billingVerifyCheck{}, fmt.Errorf("unsupported billing check %q; use log,db", item)
		}
	}
	if !check.Log && !check.DB {
		return billingVerifyCheck{}, errors.New("at least one billing check is required")
	}
	return check, nil
}

func billingBrandCloudID(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRow(`SELECT coalesce(brand_cloud_id, '') FROM device_bindings WHERE trim(brand_cloud_id) <> '' ORDER BY assignment_index, device_id LIMIT 1`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("read brand_cloud_id from %s: %w", path, err)
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("test data database %s has no brand_cloud_id", path)
	}
	return strings.TrimSpace(id), nil
}

func envValue(values []string, key string) (string, error) {
	prefix := key + "="
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			out := strings.TrimPrefix(value, prefix)
			if strings.TrimSpace(out) == "" {
				return "", fmt.Errorf("secret value %s is empty", key)
			}
			return out, nil
		}
	}
	return "", fmt.Errorf("secret value %s is missing", key)
}

func queryBillingUsageLogs(endpoint, token, brandCloudID string, since, until time.Time, summary billingUsageSummary) (billingUsageSummary, error) {
	u, err := url.Parse(endpoint + "/v1/logs")
	if err != nil {
		return summary, err
	}
	query := u.Query()
	query.Set("stream", "billing_usage")
	query.Set("source", "billing_usage")
	query.Set("limit", "1000")
	query.Set("order", "asc")
	query.Set("since", since.UTC().Format(time.RFC3339Nano))
	query.Set("until", until.UTC().Format(time.RFC3339Nano))
	u.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return summary, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return summary, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return summary, fmt.Errorf("billing logger query returned HTTP %d", resp.StatusCode)
	}
	var body billingUsageLogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return summary, err
	}
	for _, item := range body.Events {
		raw, ok := item.Fields["usage_event"].(map[string]any)
		if !ok || strings.TrimSpace(stringValue(raw["brand_cloud_id"])) != brandCloudID || stringValue(raw["service_code"]) != "mqtt" {
			continue
		}
		summary.UsageEvents++
		for _, measurement := range anySlice(raw["measurements"]) {
			m, ok := measurement.(map[string]any)
			if !ok {
				continue
			}
			quantity := uint64Number(m["quantity"])
			switch stringValue(m["metric_code"]) {
			case "publish_bytes":
				summary.PublishBytes += quantity
			case "delivery_bytes":
				summary.DeliveryBytes += quantity
			case "publish_count":
				summary.PublishCount += quantity
			case "delivery_count":
				summary.DeliveryCount += quantity
			}
		}
	}
	return summary, nil
}

type billingFacts struct {
	Count         int
	PublishBytes  uint64
	DeliveryBytes uint64
}

func queryBillingUsageFacts(kubeconfig, stack, brandCloudID string, since time.Time) (billingFacts, error) {
	secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-platform", "postgresql-runtime", "POSTGRES_PASSWORD")
	if err != nil {
		return billingFacts{}, fmt.Errorf("read postgres credential: %w", err)
	}
	password, err := envValue(secretEnv, "POSTGRES_PASSWORD")
	if err != nil {
		return billingFacts{}, err
	}
	query := fmt.Sprintf(`SELECT count(*), coalesce(sum(CASE WHEN metric_code='publish_bytes' THEN quantity ELSE 0 END),0), coalesce(sum(CASE WHEN metric_code='delivery_bytes' THEN quantity ELSE 0 END),0) FROM usage_facts WHERE service_code='mqtt' AND brand_cloud_id=%s AND event_time >= %s`, sqlLiteral(brandCloudID), sqlLiteral(since.UTC().Format(time.RFC3339Nano)))
	cmd := exec.Command("kubectl", "-n", stack+"-platform", "exec", "postgresql-0", "--", "sh", "-c", "PGPASSWORD=\"$1\" psql -h 127.0.0.1 -U postgres -d video_cloud -At -F '\t' -c \"$2\"", "sh", password, query)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.Output()
	if err != nil {
		return billingFacts{}, fmt.Errorf("query usage_facts: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(fields) != 3 {
		return billingFacts{}, fmt.Errorf("unexpected usage_facts query result")
	}
	count, err := strconv.Atoi(fields[0])
	if err != nil {
		return billingFacts{}, err
	}
	pub, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return billingFacts{}, err
	}
	del, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return billingFacts{}, err
	}
	return billingFacts{Count: count, PublishBytes: pub, DeliveryBytes: del}, nil
}

func billingResultsGeneratedAt(path string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	var result struct {
		GeneratedAt string `json:"generated_at"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, result.GeneratedAt)
}

func uint64Number(value any) uint64 {
	switch v := value.(type) {
	case float64:
		return uint64(v)
	case json.Number:
		n, _ := strconv.ParseUint(string(v), 10, 64)
		return n
	case int:
		return uint64(v)
	case int64:
		return uint64(v)
	case uint64:
		return v
	default:
		return 0
	}
}
