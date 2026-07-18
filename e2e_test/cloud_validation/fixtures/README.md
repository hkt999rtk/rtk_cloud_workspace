# Cloud validation fixtures

Fixture files in this directory contain schemas and sanitized examples only.
Live credentials are generated per run under
`${RUNNER_TEMP:-$TMPDIR}/sdk-cloud-validation-secrets/<run_id>/` with mode
`0600`. That private directory is never an upload root. The artifact tree
contains only reports, redacted logs, and the secret-free resource manifest.

The dedicated Android and iOS Brand Clouds are long-lived. A run owns only the
users, devices, certificates, claims, and bindings listed in its
`resource-manifest.json`; cleanup must never target resources absent from that
manifest.

The fixture provider must resolve both dedicated cloud slugs through the
deployed Account Manager, reject either missing/disabled cloud, create a real
run-scoped foreign device for negative authorization, and emit
`brand_cloud_active: true`. The harness rejects a runtime bundle that does not
carry that verified state. `resources` contains identifiers and fingerprints
only; secret-bearing fields are rejected before the manifest is retained.

If setup fails after creating a subset of remote resources, the provider emits
a recovery bundle with `brand_cloud_active: false`. It is cleanup input only
and is not valid as an App runtime bundle; `setup-fixture.sh` still emits the
secret-free manifest before returning the original setup error.
