# PRO2 examples: Dev and Staging release operations

The source repository owns firmware, release packaging and the canonical guide. Cloud Admin serves the guide and examples UI; the frontend Portal provides the separate catalog and records evaluation-term acceptance before issuing download URLs. The original SDK catalog is unchanged.

Build from a clean committed examples checkout:

```sh
python3 tools/package_release.py --version VERSION --sdk /absolute/sdk-ameba-v9.6e --toolchain /absolute/gcc-arm-none-eabi-10.3-2021.10/bin
```

The command creates fresh isolated test credentials and three complete flash images. `build/releases/VERSION/publish/` is the only publication directory. Never upload the sibling `private/` or firmware build directories. The source archive excludes local config, credentials, private keys and build output. Real user credentials must never enter this pipeline.

Before releasing changed guide content, run the examples repository's `tools/sync_website_docs.py --admin /absolute/rtk_cloud_admin`, build Developer Docs, and review the website copy. The canonical English document participates in the existing Developer Docs translation pipeline; do not separately edit its generated website copy.

From the workspace, with Python boto3 and requests installed:

```sh
python3 tools/pro2/publish_examples.py --release-dir repos/amebapro2_cloud_examples/build/releases/VERSION
```

This command defaults to **Dev**, obtains runtime bucket/endpoint information from the canonical Dev kubeconfig, and uses the Dev operator's `LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID` / `LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY` for writing. Runtime SDK credentials are read-only and must not be used for publication. It verifies artifact hashes before upload and after download, refuses conflicting immutable objects, and preserves existing CORS rules while adding the Dev browser origin. Previous CORS settings are saved alongside the private local build evidence.

Set `PRO2_EXAMPLES_PREFIX=pro2-examples/dev/` in the Dev operator store and frontend SDK Secret. The workspace renderer persists this setting. Roll out only Cloud Admin and frontend with their validated images, using Recreate to preserve single-writer SQLite semantics. Preserve old image references, prefix values and Deployment resource versions for rollback; use guarded updates.

After those services are ready, activate the previously uploaded version:

```sh
python3 tools/pro2/publish_examples.py --release-dir repos/amebapro2_cloud_examples/build/releases/VERSION --activate
```

Activation uses the old latest object's ETag (or creates it only if absent). The prior latest pointer is refreshed locally for every actual transition, including reactivation, in `previous-latest-<environment>.json`. Rollback restores that pointer with the current ETag and restores affected image/configuration values using resource-version preconditions. Immutable release objects remain available; never overwrite an old version.

Verify the authenticated Dev examples page, all three download hashes, the URL-to-burner handoff and local file selection. The public Portal catalog is `/api/pro2-examples/catalog`; the authenticated BFF catalog is `/api/developer/pro2-examples/catalog`. Both accept an optional version. The download POST accepts `accepted`, `version`, `artifact`, `terms_version` and returns a URL, artifact metadata and expiration. Signed URLs are never permanent release links. Keep deployment, browser, mock Serial and physical-board results separate.

Physical flash/boot, camera and real user Cloud acceptance are pending until performed with hardware/user credentials. No production protected-zone support is implied by this release.

### Runtime read access

The Portal runtime credential must read both the existing `sdk/` prefix and
`PRO2_EXAMPLES_PREFIX` (`pro2-examples/dev/` in Dev). A key restricted to SDK files
will return an unavailable examples catalog until its permissions are updated.
Verify catalog and signed artifact reads using the runtime identity before
activating the release; retain write credentials only in the publishing operator
configuration. The current Dev runtime identity passed these reads during the
0.1.0-dev.2 acceptance preparation.

## Staging promotion

Use `--environment staging` for the same verified isolated evaluation release.
The publisher reads only the canonical Staging kubeconfig and operator writer
credentials, writes under `pro2-examples/staging/`, and allows both Staging Admin
and Portal origins in CORS. Runtime credentials must read that prefix and `sdk/`.
Backups are named per environment. First upload and verify without `--activate`,
then deploy CI-published service images with the Staging prefix before activating.
Never promote locally built Dev service images to Staging. Preserve existing
CORS rules, data and image/settings rollback records.
