# Realtek Cloud Status Report Framework

Status: source.

Owner: `rtk_cloud_workspace`.

This directory defines the reusable weekly status report framework for Realtek
Video / IoT Cloud and Connect+ work. The framework is tracked in git; generated
Word files, rendered pages, and copied figure assets stay under `.artifacts/`.

## Output Model

Use the builder from the workspace root:

```sh
/Users/kevinhuang/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
  tools/status-report/build_cloud_status_report.py
```

The builder writes:

```text
.artifacts/status-reports/YYYY-MM-DD/
  realtek_video_iot_cloud_status_report.docx
  figures/
  rendered/
```

Do not commit generated report output. Commit only framework changes, source
material indexes, and builder changes.

## Report Shape

The current report structure is documented in
[`templates/cloud-status-report-outline.md`](templates/cloud-status-report-outline.md).
It is intentionally stable so the same skeleton can be reused for weekly
management reports:

- Cover / core management message
- Part 1: executive summary
- Part 2: cloud, product, and KPI detail
- Part 3: operation screenshots and usage flows
- Part 4: Linode staging deployment and configuration
- Review checklist
- Appendix: material and source index

## Material Policy

Use existing submodule design assets before creating new diagrams. The report
body should contain only the screenshots needed to explain current operations;
the appendix should carry the complete source index so future weekly reports can
reuse the same materials.

Tracked source indexes:

- [`materials.md`](materials.md)
- `repos/rtk_cloud_admin/docs/assets/webui-design/`
- `repos/rtk_cloud_client/docs/mockups/`
- `repos/rtk_cloud_frontend/static/assets/`

## Evidence Policy

Live Linode health checks are acceptable for a status report, but they are not a
replacement for formal sign-off. Production or private-cloud readiness must use
the workspace evidence wrapper in
[`../product-level-evidence.md`](../product-level-evidence.md).

Never include secrets, DSNs, bearer tokens, Linode tokens, DNS credentials,
object storage keys, private keys, or raw customer data in a status report.
