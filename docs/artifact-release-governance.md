# Artifact Release Governance

Status: active workspace policy.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-18.

## Purpose

This document defines the cross-repository release artifact policy for Realtek
Connect+ deploy and handoff artifacts. It is a workspace-level governance note:
each service repository still owns its build scripts, release workflow, deploy
workflow, verifier, and service-specific release report.

Use this document when opening release/CD issues that affect where artifacts are
published, how operators find them, and how developers prove an artifact exists.

## Storage Boundary

Linode Object Storage is the durable store for formal deploy and handoff
artifacts. It exposes an S3-compatible API. Workflows may use `aws s3`, `s3cmd`,
`rclone`, or another S3-compatible client, but the backend is Linode Object
Storage and every command must point at the configured Linode endpoint.

Do not describe this as publishing to AWS storage. When using the AWS CLI, write
"AWS CLI as an S3-compatible client for Linode Object Storage" or equivalent.

| Store | Role |
| --- | --- |
| Linode Object Storage | Durable source for formal deploy and handoff artifacts. |
| GitHub Actions artifacts | Short-lived CI/debug artifacts and report candidates. |
| GitHub Releases | Optional human-facing release mirror or legacy fallback, not the default deploy source. |
| Service runtime object/blob stores | Product data such as media, firmware, snapshots, or backups; out of scope for this policy. |

## Required Artifact Pattern

Formal release artifacts must be version-addressed. Deploy and handoff flows must
not infer or deploy a floating `latest` object.

Canonical object prefix:

```text
releases/<artifact-name>-<version>/
```

Required objects:

```text
releases/<artifact-name>-<version>/<version>.tar.gz
releases/<artifact-name>-<version>/<version>.tar.gz.sha256
releases/<artifact-name>-<version>/manifest.json
```

A repository may keep local file names that include the repo name, such as
`rtk_account_manager-v1.2.3.tar.gz`, but the object manifest must record the
exact uploaded object key.

Minimum manifest fields:

```json
{
  "repo": "hkt999rtk/<repo>",
  "artifact_name": "<artifact-name>",
  "version": "<version>",
  "source_commit": "<git-sha>",
  "bundle": "<version>.tar.gz",
  "artifact_path": "releases/<artifact-name>-<version>/<version>.tar.gz",
  "sha256": "<hex-sha256>",
  "created_at": "<rfc3339-utc>"
}
```

## Developer Self-Check

Every implementation issue that adds or changes artifact publishing must include
commands that let the developer verify the published objects directly.

AWS CLI example, using the AWS CLI only as an S3-compatible client:

```bash
aws s3 ls "s3://$LINODE_OBJ_BUCKET/releases/<artifact-name>-<version>/" \
  --endpoint-url "$LINODE_OBJ_ENDPOINT"

aws s3 cp "s3://$LINODE_OBJ_BUCKET/releases/<artifact-name>-<version>/manifest.json" - \
  --endpoint-url "$LINODE_OBJ_ENDPOINT"
```

Equivalent `s3cmd` or `rclone` checks are acceptable if they use the same Linode
Object Storage endpoint and bucket.

Verification must prove:

- bundle, checksum, and manifest objects exist
- manifest `version`, `source_commit`, `artifact_path`, and `sha256` match the
  release candidate
- downloaded bundle checksum matches the published `.sha256`
- repo-owned verifier passes, for example `deploy/check-release.sh` or the SDK
  delivery verifier
- release or readiness report records the object key and checksum

## Adoption Matrix

| Repository | Artifact class | Current status | Required follow-up |
| --- | --- | --- | --- |
| `rtk_video_cloud` | Video cloud release bundle | Done: release workflow publishes to Linode Object Storage. | None for this batch. |
| `rtk_cloud_frontend` | Realtek Connect+ website bundle | Done: CI/release upload and deploy-linode download use Linode Object Storage. | None for this batch. |
| `rtk_cloud_admin` | Admin dashboard release image bundle | Done: release workflow publishes to Linode Object Storage. | None for this batch. |
| `rtk_account_manager` | Account manager release bundle | Done: release workflow publishes bundle, checksum, and manifest to Linode Object Storage; owner issue [hkt999rtk/rtk_account_manager#168](https://github.com/hkt999rtk/rtk_account_manager/issues/168) is closed. | None for publishing. Follow-up PR [hkt999rtk/rtk_account_manager#170](https://github.com/hkt999rtk/rtk_account_manager/pull/170) only tightens deploy verifier/runbook details. |
| `rtk_cloud_client` | SDK delivery bundle | In progress: owner issue [hkt999rtk/rtk_cloud_client#482](https://github.com/hkt999rtk/rtk_cloud_client/issues/482) and implementation PR [hkt999rtk/rtk_cloud_client#484](https://github.com/hkt999rtk/rtk_cloud_client/pull/484) add Linode Object Storage publishing and repo-owned verifier support. CI is currently blocked by GitHub Actions artifact storage quota, not by test failure. | Merge owner PR after CI can upload short-lived GitHub debug artifacts again; then mark done after the SDK package workflow records object key/checksum in `RELEASE_TEST_REPORT` candidate. |
| `rtk_cloud_contracts_doc` | Contract documentation | N/A: docs-only repository, no deployable or SDK handoff artifact. | None. |

## Issue Routing Rules

Open implementation issues only after this workspace policy is merged. Issue
bodies should link to the merged copy of this document and include concise
acceptance criteria. Do not duplicate this full policy in each issue.

Do not open duplicate implementation issues for repositories already marked
`Done` unless a later audit finds a concrete drift between their workflow and the
policy above.

Workspace tracking issue:
[hkt999rtk/rtk_cloud_workspace#13](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/13).
