# Admin BFF E2E

Admin dashboard live BFF E2E is currently service-owned by `rtk_cloud_admin`, but
it is product-facing and should be indexed from the workspace E2E tree.

Current service-owned files:

```text
repos/rtk_cloud_admin/scripts/local_video_cloud_e2e.sh
repos/rtk_cloud_admin/web/scripts/live-bff-e2e.mjs
repos/rtk_cloud_admin/docs/video-cloud-staging-e2e.md
```

## Migration Rule

Keep the implementation in `rtk_cloud_admin` while it validates Admin BFF
internals or WebUI behavior. Move a wrapper or runner into this directory if the
flow becomes a cross-repo product E2E that coordinates Account Manager, Video
Cloud, Admin Dashboard, and shared fixture material.

Artifacts should use:

```text
.artifacts/e2e_test/admin_bff/<run_id>/
```
