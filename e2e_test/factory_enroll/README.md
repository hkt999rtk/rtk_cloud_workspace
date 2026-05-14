# Factory Enrollment E2E

This workspace-owned suite validates the local factory enrollment flow:

```text
rtk-factory-enroll-test -> local cmd/factoryenroll -> local fake-certissuer -> device certificate
```

The v1 target is factory-environment validation only. It does not require Linode,
production issuer keys, or cert issuer private material. The runner generates
per-device ECDSA P-256 keys and CSRs, derives `devid` as
`pk-<sha256-spki-hex>`, signs `POST /v1/factory/enroll` with the factory HMAC
headers, and validates the returned certificate.

## Local Run

```sh
e2e_test/factory_enroll/scripts/run_factory_enroll_local.sh
```

Defaults:

- `FACTORY_ENROLL_TEST_COUNT=100`
- `FACTORY_ENROLL_TEST_CONCURRENCY=8`
- `RTK_VIDEO_CLOUD_DIR=../rtk_video_cloud`
- artifacts under `.artifacts/e2e_test/factory_enroll/<run_id>/`

The local script uses `../rtk_video_cloud/examples/factory-enrollment/dev-pki.sh`
to create dev-only mTLS material for `factoryenroll -> fake-certissuer`.
Generated private keys and reports stay outside git-tracked files.

## CLI

```sh
cd e2e_test
go run ./factory_enroll/cmd/rtk-factory-enroll-test run \
  --factory-url http://127.0.0.1:18443 \
  --auth-key factory-secret \
  --count 100 \
  --concurrency 8
```

Outputs:

- `factory-enroll-results.json`
- `factory-enroll-report.md`

Device private keys are held in memory and are not written unless
`--write-key-files` is explicitly set for a local debug run.
