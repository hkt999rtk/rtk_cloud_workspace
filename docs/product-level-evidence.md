# Realtek Connect+ Product-Level Evidence Wrapper

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-09.

## Purpose

This document defines the workspace-owned evidence wrapper for private-cloud
readiness sign-off. The wrapper gathers service-local evidence into one redacted
artifact. It does not replace service-owned smoke tests, deployment runbooks, or
production monitors.

The implementation is `scripts/collect-private-cloud-evidence.sh`.

## Evidence Boundary

The workspace wrapper owns product-level aggregation:

- pinned workspace and submodule commits
- canonical tracked test report inventory by submodule commit
- selected service version metadata when available
- health and metrics snapshots from configured URLs
- broker status references for EMQX and the cross-service broker
- backup evidence references
- service-local collector output when explicitly enabled
- explicit `PASS`, `FAIL`, `SKIP`, and `BLOCKED` markers

Service repositories still own service-local evidence:

| Evidence | Owner repository | Workspace behavior |
| --- | --- | --- |
| Account auth/org/device/provisioning smoke | `rtk_account_manager` | Record the repo-owned canonical report path and commit; invoke a configured collector command once that repo provides one. |
| Video cloud runtime/deploy readiness | `rtk_video_cloud` | Record the repo-owned canonical report path and commit; can invoke `deploy/collect-readiness-evidence.sh`. |
| Admin dashboard production-mode readiness | `rtk_cloud_admin` | Record the repo-owned canonical report path and commit; invoke a configured collector command once that repo provides one. |
| Frontend website/lead portal readiness | `rtk_cloud_frontend` | Record the repo-owned canonical report path and commit; invoke a configured collector command once that repo provides one. |
| SDK release validation, load, and lab evidence | `rtk_cloud_client` | Record the repo-owned canonical report path and commit; refer to release/load/lab artifacts rather than synthesizing them. |

The workspace summary is an aggregation layer only. It must not create a
replacement test report for a service repo, rewrite service-owned results, or
turn raw logs into committed report content. Repo-owned canonical reports remain
authoritative for their repository and are governed by the common contracts
format in `rtk_cloud_contracts_doc`.

## Canonical Report Aggregation

The wrapper inventories these canonical tracked report filenames for every
submodule that participates in product evidence:

| Filename | Expected source |
| --- | --- |
| `docs/TEST_REPORT.md` | PR validation and normal CI evidence. |
| `docs/RELEASE_TEST_REPORT.md` | Package, binary, SDK, image, or deployment artifact release validation. |
| `docs/READINESS_TEST_REPORT.md` | Deployed service readiness evidence. |
| `docs/LOAD_TEST_REPORT.md` | Load or performance validation evidence. |
| `docs/HARDWARE_TEST_REPORT.md` | Hardware, lab, mobile-device, or runner-specific evidence. |

The generated evidence bundle writes the inventory to
`reports/canonical-reports.tsv` with:

- submodule path
- submodule commit
- canonical report filename
- result marker
- source path
- reason

Result semantics:

| Result | Meaning |
| --- | --- |
| `PASS` | The repo-owned canonical report exists at the pinned submodule commit. |
| `SKIP` | The report is intentionally not present or not applicable for this repo/profile. |
| `BLOCKED` | The report is expected for the run but unavailable. |

Use `RTK_EVIDENCE_REQUIRED_REPORTS` to mark expected reports for a specific
evidence run. It accepts space-separated selectors:

- `repos/rtk_cloud_client:docs/LOAD_TEST_REPORT.md`
- `repos/rtk_video_cloud:*`
- `*:docs/TEST_REPORT.md`
- `*:*`

If a missing report matches a required selector, the wrapper records `BLOCKED`
instead of `SKIP`. With `RTK_EVIDENCE_STRICT=1`, any `FAIL` or `BLOCKED` marker
causes a non-zero exit.

Remote CI/CD, staging, load, or hardware runners may also produce raw artifact
references. Provide a sanitized file through
`RTK_EVIDENCE_REPORT_ARTIFACT_REFS_FILE`; the wrapper copies it to
`reports/artifact-references.txt` after redaction. Raw logs remain artifact-only
and are not committed by the workspace.

## Command

Run from the workspace root:

```sh
scripts/collect-private-cloud-evidence.sh
```

The command writes a deterministic directory layout under `evidence/` and, by
default, a tarball next to it. The generated files are local deployment artifacts
and should not be committed.

Recommended evaluation command:

```sh
RTK_EVIDENCE_ENVIRONMENT=evaluation \
RTK_EVIDENCE_FRONTEND_HEALTH_URL=http://127.0.0.1:8080/healthz \
RTK_EVIDENCE_ADMIN_HEALTH_URL=http://127.0.0.1:18090/healthz \
RTK_EVIDENCE_ACCOUNT_MANAGER_HEALTH_URL=http://127.0.0.1:18070/healthz \
RTK_EVIDENCE_VIDEO_CLOUD_HEALTH_URL=http://127.0.0.1:18080/health \
RTK_EVIDENCE_EMQX_STATUS_URL=http://127.0.0.1:18083/status \
RTK_EVIDENCE_METRICS_URLS="http://127.0.0.1:18080/metrics/prometheus" \
  scripts/collect-private-cloud-evidence.sh
```

Use `RTK_EVIDENCE_STRICT=1` when CI or release sign-off should fail on any
`FAIL` marker. The default is non-strict so an operator can still inspect partial
evidence bundles from a half-configured environment.

## Configuration

| Variable | Purpose | Default |
| --- | --- | --- |
| `RTK_EVIDENCE_ENVIRONMENT` | Evidence environment name used in artifact naming. | `evaluation` |
| `RTK_EVIDENCE_OUTPUT_DIR` | Parent output directory. | `./evidence` |
| `RTK_EVIDENCE_TIMESTAMP` | Override timestamp for repeatable tests. | current UTC time |
| `RTK_EVIDENCE_TARBALL` | Create a `.tar.gz` bundle. | `1` |
| `RTK_EVIDENCE_STRICT` | Exit non-zero if any `FAIL` or `BLOCKED` is recorded. | `0` |
| `RTK_EVIDENCE_RUN_SERVICE_COLLECTORS` | Run service-local collectors. | `0` |
| `RTK_EVIDENCE_REQUIRED_REPORTS` | Space-separated selectors for canonical reports that must exist. | unset, missing reports recorded as `SKIP` |
| `RTK_EVIDENCE_REPORT_ARTIFACT_REFS_FILE` | Sanitized file containing links/paths to remote report artifacts. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_FRONTEND_HEALTH_URL` | Frontend health URL. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_ADMIN_HEALTH_URL` | Admin health URL. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_ACCOUNT_MANAGER_HEALTH_URL` | Account manager health URL. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_VIDEO_CLOUD_HEALTH_URL` | Video cloud health URL. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_METRICS_URLS` | Space-separated metrics snapshot URLs. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_EMQX_STATUS_URL` | EMQX status endpoint or operator proxy URL. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_NATS_URL` | Redacted NATS JetStream endpoint presence marker. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_BROKER_SMOKE_REF` | Link/path to broker smoke evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_POSTGRES_BACKUP_REF` | Link/path to database backup evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_OBJECT_STORAGE_BACKUP_REF` | Link/path to object storage backup evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_FRONTEND_BACKUP_REF` | Link/path to frontend lead DB backup evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_EMQX_BACKUP_REF` | Link/path to EMQX config/state backup evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_NATS_BACKUP_REF` | Link/path to JetStream backup evidence. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_ACCOUNT_MANAGER_COLLECTOR_CMD` | Account manager collector command. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_ADMIN_COLLECTOR_CMD` | Admin dashboard collector command. | unset, recorded as `SKIP` |
| `RTK_EVIDENCE_FRONTEND_COLLECTOR_CMD` | Frontend collector command. | unset, recorded as `SKIP` |

When `RTK_EVIDENCE_RUN_SERVICE_COLLECTORS=1`, the wrapper invokes video cloud's
existing collector if present and invokes other service collector commands only
when their command variables are set.

## Artifact Layout

```text
realtek-connect-plus-evidence-<environment>-<timestamp>/
  manifest.txt
  summary.md
  status.txt
  services/
    commits.tsv
    versions.txt
    <service collector outputs>
  health/
    <health probe outputs>
  metrics/
    <metrics snapshots>
  reports/
    canonical-reports.tsv
    artifact-references.txt
  brokers/
    config.txt
    smoke-reference.txt
  backups/
    references.txt
```

`status.txt` is tab-separated and every row begins with `PASS`, `FAIL`, `SKIP`,
or `BLOCKED`. `summary.md` is intended for deployment sign-off review.

## Redaction Rules

The wrapper redacts common credential forms before writing command output:

- URL userinfo such as `user:password@host`
- query parameters containing token, secret, password, passwd, key, dsn, or auth
- `Bearer` tokens
- environment-style key/value pairs containing token, secret, password, passwd,
  or dsn
- PEM private-key body lines

Operators must still avoid pointing the wrapper at raw customer data endpoints.
The wrapper is for readiness evidence, not data export.

## Acceptance Checklist

Before a private-cloud deployment is considered evidence-ready:

- `services/commits.tsv` lists clean pinned commits for workspace and selected submodules.
- Required health endpoints are configured and report `PASS`.
- Metrics snapshots or links are present for selected runtime services.
- EMQX status is present when MQTT transport is enabled.
- NATS JetStream configuration and broker smoke reference are present when the
  cross-service lifecycle channel is enabled.
- Backup references cover Postgres, object storage, frontend lead storage, EMQX,
  and JetStream where those components are deployed.
- Disabled optional components appear as `SKIP` with an intentional reason.
- Missing required canonical reports appear as `BLOCKED` with the expected
  repo-owned path and pinned submodule commit.
- Existing canonical reports are referenced by path and commit; the workspace
  does not synthesize replacement service reports.
- The final bundle contains no tokens, DSNs, private keys, or raw customer data.
