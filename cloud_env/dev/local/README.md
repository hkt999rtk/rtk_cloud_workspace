# Dev local object storage

This compose file is only for local P1 report validation. It starts a
S3-compatible MinIO service and does not change the dev/staging deployment
workflow.

Set `REPORT_OBJECT_STORAGE_ACCESS_KEY` and
`REPORT_OBJECT_STORAGE_SECRET_KEY` in the shell (or an ignored local env
file), then start `minio.compose.yaml`. Configure Cloud Admin with:

```env
REPORT_OBJECT_STORAGE_ENDPOINT=http://127.0.0.1:9000
REPORT_OBJECT_STORAGE_BUCKET=reports
REPORT_OBJECT_STORAGE_REGION=us-east-1
```

Create the bucket once before running report E2E. Credentials must remain in
the operator environment and must not be committed.
