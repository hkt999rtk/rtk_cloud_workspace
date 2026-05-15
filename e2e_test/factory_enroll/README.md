# Factory Enrollment E2E

This workspace-owned suite validates the factory enrollment flow. The local
factory bridge can run against either the repo-local fake certissuer or the
Linode staging certissuer:

```text
rtk-factory-enroll-test -> local cmd/factoryenroll -> certissuer -> device certificate
```

The runner generates per-device ECDSA P-256 keys and CSRs, derives `devid` as
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
- `FACTORY_ENROLL_TEST_WRITE_KEY_FILES=0`

The local script uses `../rtk_video_cloud/examples/factory-enrollment/dev-pki.sh`
to create dev-only mTLS material for `factoryenroll -> fake-certissuer`.
Generated private keys and reports stay outside git-tracked files.

To preserve the generated device material for later integration tests:

```sh
FACTORY_ENROLL_TEST_WRITE_KEY_FILES=1 \
FACTORY_ENROLL_TEST_RUN_ID=factory-local-certset-YYYYMMDDTHHMMSSZ \
e2e_test/factory_enroll/scripts/run_factory_enroll_local.sh
```

The preserved material is written under:

```text
.artifacts/e2e_test/factory_enroll/<run_id>/device-material/device-NNN/
```

Each device directory contains:

- `device.key`
- `device.csr`
- `device.crt`
- `device-chain.crt`

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

## Recorded Linode Certset

On 2026-05-14, the workspace runner generated and enrolled 100 device
identities through the intended factory bridge path:

```text
rtk-factory-enroll-test
-> local cmd/factoryenroll
-> https://certissuer.video-cloud-staging.realtekconnect.com
-> Linode cmd/certissuer
-> staging issuer CA
```

Run details:

- Run ID: `factory-linode-certset-20260514T225802Z`
- Device count: `100`
- Successes: `100`
- Failures: `0`
- Device ID policy: `pk-<sha256-spki-hex>`
- Local artifact source:
  `.artifacts/e2e_test/factory_enroll/factory-linode-certset-20260514T225802Z/`
- Copied test material:
  `../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/`

The older `../rtk_video_cloud/keys/test_device/` directory was removed because
it was not produced by the factory enrollment flow. The earlier local fake
issuer certset was also removed because it was not signed by the Linode staging
issuer. The new certset includes a `device-ca.crt` extracted from the returned
Linode issuer chain so local/staging test configs can reference a single CA
file.

Validation evidence:

- Linode `cert_issue_requests`: `100`
- Linode `cert_issue_events`: `100`
- Linode request status: `succeeded=100`
- Linode event outcome: `succeeded=100`
- Linode `video_cloud-certissuer.service` journal entries matching the run:
  `100`

## Linode Staging Check

The staging certissuer was enabled before the recorded Linode certset was
generated:

- `video_cloud-certissuer.service`: active
- `10.42.1.10:9443`: listening
- `certissuer.video-cloud-staging.realtekconnect.com`: public mTLS ingress
- edge -> API private upstream `10.42.1.10:9443`: reachable
- `certbot` certificate SANs include gateway, device, and certissuer hostnames

`linode-deploy verify` still reported an unrelated TURN registry active-node
failure during this session. Certissuer-specific checks and the 100-device
factory enrollment path passed.

The workspace does not require the Linode `factoryenroll` service for this v1
test because the factory bridge intentionally runs locally:

- Linode `video_cloud-factoryenroll.service`: not required for this test and
  currently not installed/enabled
- `:18443`: not listening
