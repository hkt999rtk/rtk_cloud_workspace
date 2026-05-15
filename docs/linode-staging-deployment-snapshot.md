# Linode Staging Deployment Snapshot

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Snapshot time: 2026-05-15 11:03 UTC.

## Purpose

This document records the current cross-service Linode staging deployment after
bringing up the main Realtek Connect+ backend systems. It is a workspace-level
snapshot for coordination and deployment handoff. It does not replace the
service-owned deployment runbooks, release reports, or production monitors.

## Deployment Order Used

The staging deployment follows the workspace orchestration order in
`docs/private-cloud-deployment.md`:

1. Platform prerequisites: Linode API access, DNS, SSH key, operator CIDR,
   service secrets, nginx.org `>=1.30` package source.
2. Video Cloud runtime.
3. Account Manager API.
4. Admin dashboard.
5. Public frontend / promotion site.
6. Product-level evidence.

Admin is intentionally deployed after Video Cloud and Account Manager because it
is an aggregator/BFF. It must not be treated as proof that upstream services are
ready until `/api/service-health` reports selected upstreams as `ok`.

## Live Components

| Component | Owner repo | Endpoint | Deployed branch / PR | Current status |
| --- | --- | --- | --- | --- |
| Video Cloud runtime | `rtk_video_cloud` | `https://video-cloud-staging.realtekconnect.com` | `codex/linode-nginx-1-30`, PR `hkt999rtk/rtk_video_cloud#467` | `PASS` `/healthz`, `PASS` `/version` |
| Account Manager API | `rtk_account_manager` | `https://account-manager.video-cloud-staging.realtekconnect.com` | `codex/account-manager-linode-public-vm`, PR `hkt999rtk/rtk_account_manager#160` | `PASS` `/v1/health`; register/login/`/v1/me` smoke previously passed |
| Admin dashboard | `rtk_cloud_admin` | `https://admin.video-cloud-staging.realtekconnect.com` | `codex/admin-linode-deploy`, PR `hkt999rtk/rtk_cloud_admin#116` | `PASS` `/healthz`; `/api/service-health` reports Account Manager, Video Cloud, and SQLite as `ok` |

## Current Linode Runtime Shape

| Component | VM / placement | Runtime shape | Notes |
| --- | --- | --- | --- |
| Video Cloud edge | `video-cloud-staging-edge` | nginx `1.30.1` public TLS gateway | Proxies public Video Cloud API traffic. `/version` currently reports `ApiVersion=0.28.2`, `AppVersion=debug`. |
| Account Manager | `rtk-account-manager-staging` | dedicated public VM, nginx `1.30.1`, app on `18081`, local PostgreSQL on `127.0.0.1:5432` | Public integration endpoint is the HTTPS domain, not the raw VM IP or app port. |
| Admin dashboard | `rtk-cloud-admin-staging` | dedicated public VM, nginx `1.30.1`, Dockerized admin app, local SQLite cache | Uses Account Manager and Video Cloud public HTTPS domains as upstreams. |

## Required Admin Upstream Configuration

`rtk_cloud_admin` must use domain-based HTTPS upstreams:

```env
ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_BASE_URL=https://video-cloud-staging.realtekconnect.com
```

Do not configure Admin with `http://<account-manager-ip>:18081`. The app port is
behind nginx, the TLS certificate is issued for the domain, and future VM/IP
changes should be absorbed by DNS.

## Verification Evidence

Commands executed from the operator machine:

```sh
curl -fsS https://video-cloud-staging.realtekconnect.com/healthz
curl -fsS https://video-cloud-staging.realtekconnect.com/version
curl -fsS https://account-manager.video-cloud-staging.realtekconnect.com/v1/health
curl -fsS https://admin.video-cloud-staging.realtekconnect.com/healthz
curl -fsS https://admin.video-cloud-staging.realtekconnect.com/api/service-health
```

Observed results:

```text
Video Cloud /healthz: {"status":"ok"}
Video Cloud /version: {"ApiVersion":"0.28.2","AppVersion":"debug"}
Account Manager /v1/health: {"status":"ok"}
Admin /healthz: ok
Admin /api/service-health: Account Manager ok, Video Cloud ok, SQLite ok
```

## PR And Snapshot State

| Repository | PR | State at snapshot | Notes |
| --- | --- | --- | --- |
| `rtk_video_cloud` | `hkt999rtk/rtk_video_cloud#467` | open | Solidifies nginx.org `>=1.30` edge deploy and nginx 1.30 `http2 on;` template syntax. |
| `rtk_account_manager` | `hkt999rtk/rtk_account_manager#160` | open, CI `test` passed at last check | Adds dedicated public VM deployment, nginx TLS, local PostgreSQL, verify script, and nginx.org `>=1.30` deploy path. |
| `rtk_cloud_admin` | `hkt999rtk/rtk_cloud_admin#116` | open, mergeable at last check | Adds dedicated public VM deployment, nginx.org `>=1.30`, and fixes Account Manager health probe to `/v1/health`. |
| `rtk_cloud_workspace` | `hkt999rtk/rtk_cloud_workspace#11` | open | Contains workspace docs updates, including deployment order and this snapshot. |

## Remaining Work Before Calling This Production-Ready

- Merge the service PRs and update the workspace submodule pointer snapshot on
  the merged commits.
- Produce a product-level evidence bundle with
  `scripts/collect-private-cloud-evidence.sh` after the workspace points at the
  merged service commits.
- Decide whether `AppVersion=debug` on Video Cloud staging is acceptable for the
  current staging label or should be replaced by an explicit release version.
- Deploy or intentionally mark `rtk_cloud_frontend` public promotion site as
  `SKIP` for this staging milestone.
- Add backup/restore evidence references for Account Manager PostgreSQL, Video
  Cloud PostgreSQL/object storage, Admin SQLite, EMQX, and any enabled broker
  state before production-like sign-off.
