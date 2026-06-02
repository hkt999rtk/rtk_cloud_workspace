# Structured Prometheus Targets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Structure central Prometheus scrape targets in `rtk_video_cloud/linode_deploy`, then add the missing app-level `/metrics/prometheus` surfaces for Account Manager, Cloud Admin, and Cloud Frontend so staging observability can scrape all deployed RTK cloud services over private/VPC addresses.

**Architecture:** `rtk_video_cloud/linode_deploy` remains the central Prometheus owner. Static, ad hoc optional target fields are replaced by a manifest-driven external target list with explicit job, service, address, path, and labels. Each app repo owns its own metrics endpoint; central Prometheus only scrapes endpoints declared in the Video Cloud deployment manifest.

**Tech Stack:** Go, `net/http`, Gin, Prometheus text exposition format, Linode private/VPC networking, existing `linode_deploy` manifest/render/test framework.

---

## Current Baseline

- `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer.go` already renders Prometheus config and has test coverage in `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer_test.go`.
- `repos/rtk_video_cloud/linode_deploy/configs/video-cloud-staging-5vm.yaml.example` currently has a single optional `deploy.admin_metrics_private_ip` field.
- `repos/rtk_account_manager/internal/api/metrics.go` already exposes authenticated JSON admin metrics at `GET /v1/admin/metrics`; it does not expose Prometheus text metrics.
- `repos/rtk_cloud_admin/internal/app/app.go` exposes `/healthz` but no `/metrics/prometheus`.
- `repos/rtk_cloud_frontend/internal/web/server.go` exposes `/healthz` but no `/metrics/prometheus`.
- `repos/rtk_cloud_logger` is a library package, not a deployed HTTP service. Do not create a Prometheus target for it until a real logger service/forwarder exists.
- Worktree is dirty for `repos/rtk_cloud_frontend`, `repos/rtk_cloud_logger`, and `repos/rtk_video_cloud`; execution should start in an isolated branch or worktree and must preserve existing user changes.

## Target Schema

Add this manifest shape under `deploy`:

```yaml
deploy:
  prometheus_targets:
    - job: account_manager_app
      service: account-manager
      address: 10.42.1.20:18081
      metrics_path: /metrics/prometheus
      labels:
        repo: rtk_account_manager
        env: staging
    - job: cloud_admin_app
      service: cloud-admin
      address: 10.42.1.60:8080
      metrics_path: /metrics/prometheus
      labels:
        repo: rtk_cloud_admin
        env: staging
    - job: cloud_frontend_app
      service: cloud-frontend
      address: 10.42.1.70:8080
      metrics_path: /metrics/prometheus
      labels:
        repo: rtk_cloud_frontend
        env: staging
```

Keep `admin_metrics_private_ip` temporarily for backward compatibility during rollout, but mark it legacy in docs and tests. Do not add `rtk_cloud_logger` here yet.

---

### Task 1: Isolate Work And Freeze Baseline

**Files:**
- Read only: workspace root and submodule statuses

- [ ] **Step 1: Create or switch to an isolated branch/worktree**

Run:

```bash
git status --short
git switch -c codex/structured-prometheus-targets
```

Expected: branch exists and current dirty submodule state is visible. If branch creation fails because the branch exists, use:

```bash
git switch codex/structured-prometheus-targets
```

- [ ] **Step 2: Record submodule dirtiness before edits**

Run:

```bash
git status --short
git -C repos/rtk_video_cloud status --short
git -C repos/rtk_account_manager status --short
git -C repos/rtk_cloud_admin status --short
git -C repos/rtk_cloud_frontend status --short
git -C repos/rtk_cloud_logger status --short
```

Expected: existing unrelated changes are understood before touching files. Do not revert them.

---

### Task 2: Structure External Prometheus Targets In Video Cloud

**Files:**
- Modify: `repos/rtk_video_cloud/linode_deploy/internal/manifest/manifest.go`
- Modify: `repos/rtk_video_cloud/linode_deploy/internal/manifest/manifest_test.go`
- Modify: `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer.go`
- Modify: `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer_test.go`
- Modify: `repos/rtk_video_cloud/linode_deploy/configs/video-cloud-staging-5vm.yaml.example`
- Modify: `repos/rtk_video_cloud/linode_deploy/docs/RUNBOOK.md`

- [ ] **Step 1: Add failing manifest test for structured targets**

In `repos/rtk_video_cloud/linode_deploy/internal/manifest/manifest_test.go`, add assertions that load `video-cloud-staging-5vm.yaml.example` and expect three `Deploy.PrometheusTargets` entries:

```go
want := []string{
	"account_manager_app|account-manager|10.42.1.20:18081|/metrics/prometheus",
	"cloud_admin_app|cloud-admin|10.42.1.60:8080|/metrics/prometheus",
	"cloud_frontend_app|cloud-frontend|10.42.1.70:8080|/metrics/prometheus",
}
```

Run:

```bash
cd repos/rtk_video_cloud/linode_deploy
go test ./internal/manifest -run TestLoadFiveVMExample -v
```

Expected: FAIL because `PrometheusTargets` is not defined yet.

- [ ] **Step 2: Add manifest structs and validation**

In `repos/rtk_video_cloud/linode_deploy/internal/manifest/manifest.go`, add:

```go
type PrometheusTarget struct {
	Job         string            `yaml:"job"`
	Service     string            `yaml:"service"`
	Address     string            `yaml:"address"`
	MetricsPath string            `yaml:"metrics_path"`
	Labels      map[string]string `yaml:"labels"`
}
```

Add `PrometheusTargets []PrometheusTarget `yaml:"prometheus_targets"` to the existing deploy config struct.

Validation rules:

- `job`, `service`, `address`, and `metrics_path` are required.
- `metrics_path` must start with `/`.
- `address` must be `host:port`.
- Private IP addresses must be inside the configured VPC CIDR when the host parses as an IP.
- Duplicate `job + service + address + metrics_path` entries are invalid.

- [ ] **Step 3: Render structured targets into Prometheus YAML**

In `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer.go`, extend the infra config passed into `RenderPrometheusConfig` to carry structured external targets. Render each target as an independent `scrape_configs` entry:

```yaml
- job_name: "account_manager_app"
  metrics_path: /metrics/prometheus
  static_configs:
    - targets:
        - "10.42.1.20:18081"
      labels:
        service: "account-manager"
        repo: "rtk_account_manager"
        env: "staging"
```

Rendering requirements:

- Stable output order follows manifest order.
- Always include `service`.
- Include optional labels sorted by key for deterministic tests.
- Reject label key `service`; service is controlled by the top-level field.

- [ ] **Step 4: Preserve legacy admin exporter behavior**

Keep existing `admin_metrics_private_ip` rendering for `node` and `nginx` exporter targets while adding the structured app target for Cloud Admin. The old field covers host/nginx exporter ports `9100` and `9113`; the new structured target covers app `/metrics/prometheus`.

- [ ] **Step 5: Update Video Cloud tests**

In `repos/rtk_video_cloud/linode_deploy/internal/deployer/deployer_test.go`, assert rendered Prometheus config contains:

```go
`job_name: "account_manager_app"`
`service: "account-manager"`
`"10.42.1.20:18081"`
`job_name: "cloud_admin_app"`
`service: "cloud-admin"`
`"10.42.1.60:8080"`
`job_name: "cloud_frontend_app"`
`service: "cloud-frontend"`
`"10.42.1.70:8080"`
```

Run:

```bash
cd repos/rtk_video_cloud/linode_deploy
go test ./internal/manifest ./internal/deployer -v
```

Expected: PASS.

- [ ] **Step 6: Update config example and runbook**

Update `repos/rtk_video_cloud/linode_deploy/configs/video-cloud-staging-5vm.yaml.example` with the `prometheus_targets` block shown in this plan.

Update `repos/rtk_video_cloud/linode_deploy/docs/RUNBOOK.md` to state:

- Prometheus scrapes declared external app targets only over private/VPC addresses.
- `admin_metrics_private_ip` is legacy host/nginx exporter support.
- `rtk_cloud_logger` has no target until a deployed service exists.

---

### Task 3: Add Account Manager Prometheus Text Metrics

**Files:**
- Modify: `repos/rtk_account_manager/internal/api/api.go`
- Create or modify: `repos/rtk_account_manager/internal/api/prometheus_metrics.go`
- Create or modify: `repos/rtk_account_manager/internal/api/prometheus_metrics_test.go`
- Modify: `repos/rtk_account_manager/linode_deploy/docs/RUNBOOK.md`

- [ ] **Step 1: Write failing route test**

Add a test that calls:

```go
res := performRaw(env.router, http.MethodGet, "/metrics/prometheus", nil, "")
```

Expected response:

```text
# HELP rtk_account_manager_up Whether the Account Manager app is serving metrics.
# TYPE rtk_account_manager_up gauge
rtk_account_manager_up 1
```

Run:

```bash
cd repos/rtk_account_manager
go test ./internal/api -run TestPrometheusMetrics -v
```

Expected: FAIL with 404.

- [ ] **Step 2: Add unauthenticated private scrape route**

In `repos/rtk_account_manager/internal/api/api.go`, register:

```go
router.GET("/metrics/prometheus", s.prometheusMetrics)
```

This endpoint must not require platform admin auth; Prometheus reaches it on private networking. Do not expose it via public nginx routes unless the deploy topology already binds the app only to private/VPC access.

- [ ] **Step 3: Implement Prometheus text output**

Implement `prometheusMetrics` with content type:

```go
c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
```

Minimum metrics:

```text
rtk_account_manager_up 1
rtk_account_manager_eval_signups_total{tier="evaluation"} <count>
rtk_account_manager_eval_signups_total{tier="commercial"} <count>
rtk_account_manager_email_verification_completed_total <count>
rtk_account_manager_quota_raise_requests{status="pending"} <count>
rtk_account_manager_quota_raise_requests{status="approved"} <count>
rtk_account_manager_quota_raise_requests{status="declined"} <count>
rtk_account_manager_lifecycle_messages{queue="outbox",status="<status>"} <count>
rtk_account_manager_lifecycle_messages{queue="inbox",status="<status>"} <count>
rtk_account_manager_lifecycle_operations{type="<type>",status="<status>"} <count>
```

Reuse the existing store methods already used by `adminMetrics`; do not duplicate SQL.

- [ ] **Step 4: Run Account Manager tests**

Run:

```bash
cd repos/rtk_account_manager
go test ./internal/api ./internal/store ./internal/config -v
```

Expected: PASS.

---

### Task 4: Add Cloud Admin App Prometheus Metrics

**Files:**
- Modify: `repos/rtk_cloud_admin/internal/app/app.go`
- Create or modify: `repos/rtk_cloud_admin/internal/app/metrics_test.go`
- Modify: `repos/rtk_cloud_admin/deploy/linode/README.md`

- [ ] **Step 1: Write failing route test**

In `repos/rtk_cloud_admin/internal/app/metrics_test.go`, create a test server and request:

```go
req := httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil)
res := httptest.NewRecorder()
server.ServeHTTP(res, req)
```

Expected body contains:

```text
rtk_cloud_admin_up 1
```

Run:

```bash
cd repos/rtk_cloud_admin
go test ./internal/app -run TestPrometheusMetrics -v
```

Expected: FAIL with 404.

- [ ] **Step 2: Add route and handler**

In `routes()`, add:

```go
s.mux.HandleFunc("GET /metrics/prometheus", s.metricsPrometheus)
```

Implement `metricsPrometheus` with plain Prometheus text format.

Minimum metrics:

```text
rtk_cloud_admin_up 1
```

Optional low-risk metric if existing store methods make it cheap:

```text
rtk_cloud_admin_service_health_upstream_up{service="account-manager"} 0|1
rtk_cloud_admin_service_health_upstream_up{service="video-cloud"} 0|1
```

Do not call slow upstream APIs from the metrics handler unless current service-health code already has cached facts.

- [ ] **Step 3: Run Cloud Admin tests**

Run:

```bash
cd repos/rtk_cloud_admin
go test ./internal/app ./internal/config ./internal/store -v
```

Expected: PASS.

---

### Task 5: Add Cloud Frontend App Prometheus Metrics

**Files:**
- Modify: `repos/rtk_cloud_frontend/internal/web/server.go`
- Create or modify: `repos/rtk_cloud_frontend/internal/web/metrics_test.go`

- [ ] **Step 1: Write failing route test**

In `repos/rtk_cloud_frontend/internal/web/metrics_test.go`, create a `Server`, call `Routes()`, and request:

```go
req := httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil)
res := httptest.NewRecorder()
handler.ServeHTTP(res, req)
```

Expected body contains:

```text
rtk_cloud_frontend_up 1
```

Run:

```bash
cd repos/rtk_cloud_frontend
go test ./internal/web -run TestPrometheusMetrics -v
```

Expected: FAIL with 404.

- [ ] **Step 2: Add route before catch-all public route**

In `Routes()`, register before `mux.HandleFunc("/", s.handlePublic)`:

```go
mux.HandleFunc("/metrics/prometheus", s.handlePrometheusMetrics)
```

Implement minimum metrics:

```text
rtk_cloud_frontend_up 1
```

If `LeadStore` is configured, also expose:

```text
rtk_cloud_frontend_leads_total <count>
```

Use `LeadStore.Count(ctx, leads.ListFilter{})`; if the store returns an error, keep `up 1` and emit:

```text
rtk_cloud_frontend_leads_query_error 1
```

- [ ] **Step 3: Run Cloud Frontend tests**

Run:

```bash
cd repos/rtk_cloud_frontend
go test ./internal/web ./internal/leads ./internal/analytics -v
```

Expected: PASS.

---

### Task 6: Deployment Exposure Review

**Files:**
- Read/modify only if needed: `repos/rtk_account_manager/linode_deploy/scripts/*.sh`
- Read/modify only if needed: `repos/rtk_cloud_admin/deploy/linode/*.sh`
- Read/modify only if needed: `repos/rtk_cloud_frontend` deployment scripts if present
- Modify docs where endpoint binding is documented

- [ ] **Step 1: Confirm app ports are private-safe**

Run:

```bash
rg -n "18080|18081|metrics|nginx|proxy_pass|listen" \
  repos/rtk_account_manager/linode_deploy \
  repos/rtk_cloud_admin/deploy \
  repos/rtk_cloud_frontend
```

Expected: identify whether `/metrics/prometheus` would be reachable publicly. If public nginx proxies all paths, add an nginx deny rule for `/metrics/prometheus` on public listeners and allow only private/VPC scrape path.

- [ ] **Step 2: Add deploy docs**

Document that app metrics endpoints are intended for private Prometheus scraping only. Public verification should use `/healthz` or existing public health routes, not `/metrics/prometheus`.

---

### Task 7: End-To-End Verification

**Files:**
- No new files expected

- [ ] **Step 1: Run repo-local Go tests**

Run:

```bash
cd repos/rtk_video_cloud/linode_deploy && go test ./...
cd ../../rtk_account_manager && go test ./...
cd ../rtk_cloud_admin && go test ./...
cd ../rtk_cloud_frontend && go test ./...
```

Expected: PASS in all four repos.

- [ ] **Step 2: Render and inspect Prometheus config**

Run:

```bash
cd repos/rtk_video_cloud/linode_deploy
go test ./internal/deployer -run TestRenderInfraConfigsUsePrivateBindings -v
```

Expected: PASS and rendered config includes `account_manager_app`, `cloud_admin_app`, and `cloud_frontend_app`.

- [ ] **Step 3: Live staging verify after deploy**

After deployment, from the Prometheus/infra host or a host with VPC access, run:

```bash
curl -fsS http://10.42.1.20:18081/metrics/prometheus | head
curl -fsS http://10.42.1.60:8080/metrics/prometheus | head
curl -fsS http://10.42.1.70:8080/metrics/prometheus | head
curl -fsS http://10.42.1.30:9090/api/v1/targets
```

Expected:

- Each app endpoint returns Prometheus text.
- Prometheus target API shows the three new app jobs as `up`.
- No metrics endpoint is exposed through a public URL.

---

## Commit Plan

Use separate commits to keep review clean:

```bash
git add repos/rtk_video_cloud/linode_deploy
git commit -m "feat(linode): structure prometheus external targets"

git add repos/rtk_account_manager
git commit -m "feat(account-manager): expose prometheus app metrics"

git add repos/rtk_cloud_admin
git commit -m "feat(admin): expose prometheus app metrics"

git add repos/rtk_cloud_frontend
git commit -m "feat(frontend): expose prometheus app metrics"
```

If the workspace root tracks submodule pointers, update and commit those pointers separately after the submodule commits are finalized.

---

## Open Decisions

- Confirm Cloud Frontend staging private IP and app port. This plan uses `10.42.1.70:8080` as the intended manifest target, matching the frontend Go app default port; confirm the private IP before live deployment.
- Decide whether Account Manager `/metrics/prometheus` should include lifecycle metrics immediately or start with only up/signup/quota counters. The plan includes lifecycle because store methods already exist.
- Decide whether central Prometheus config should support grouped targets under one job or one job per app. This plan chooses one job per app for simpler alerting and ownership.

## Non-Goals

- Do not add a fake target for `rtk_cloud_logger`.
- Do not expose metrics on public interfaces.
- Do not replace the existing Account Manager JSON admin metrics endpoint.
- Do not build dashboards or alert rules in this iteration; add them after scrape targets are stable.
