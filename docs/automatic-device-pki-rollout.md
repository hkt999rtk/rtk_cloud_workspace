# Automatic Device PKI: non-production rollout and evidence

Authority: [platform_pki.md](../repos/rtk_cloud_contracts_doc/platform_pki.md),
especially §§8–9. This runbook implements that specification; it does not authorize
production, offline key import, dual-chain migration, or unrelated data deletion.

## Current qualification (2026-09-19)

The contracts, Account Manager, Cloud Admin and Video Cloud changes are merged.
The Cloud Admin and Video Cloud main-branch images are published; the Account
Manager main-branch release is still running. They are **not yet deployed** to
dev or staging. No live Device Root was changed.
The following are separate evidence scopes; local PASS is not live acceptance:

| Scope | Evidence | Result |
| --- | --- | --- |
| Durable creation | Account Manager migration 078 creates Cloud/Product outbox rows in the creation transaction; store tests cover stable retry identity, stale lease rejection and late disabled-Product receipts | PASS, disposable PostgreSQL 16 |
| Machine authorization | Signed request binds exact machine subject/role, active context, environment, body and durable job ID; controller rejects human roles and rebound requests | PASS |
| Internal CA keys | Real OpenBao 2.5.5 creates Device Root → Cloud → Product; runtime policy is exercised, not just text-checked | PASS |
| Ambiguous signing | Test discards an actual successful sign response, reconciles its original CSR and proves sign called once | PASS |
| Trust publication | Real mTLS controller and registry consumer discover new Cloud/Product descendants and persist/acknowledge CRL/bundle state; restarted consumer recovers | PASS |
| CA constraints | Exact hierarchy, signature checks and path lengths 2/1/0; pinned Root mismatch rejected | PASS |
| Persistence/restore | A non-dev-mode, encrypted file-backend OpenBao is stopped, cold-copied, restarted, then separately restored; all three public keys/certificates are unchanged and both recovered instances issue valid Device leaves | PASS, isolated Docker only |
| Scoped rebuild | Dry-run has no mutations; apply retains history, invalidates only selected Device subtree/bindings; App and other environment untouched; explicit replacement epoch is idempotent | PASS, disposable database |
| Business requeue | Explicit non-production, one-Cloud inventory/apply; new operation IDs, audit, no disabled Product revival, repeated apply leaves pending jobs unchanged | PASS, disposable database |
| Readiness UI | Chromium desktop and mobile exercise pending → ready Cloud polling, pending → failed → ready Product polling, and metadata management while PKI is pending | PASS, isolated BFF fixture plus mocked public readiness responses |
| Owner transfer | Real Account Manager handoff commit/finalization with synthetic Billing receipts preserves Cloud/Product issuer and operation IDs, does not add CA jobs, and removes source owner's management access | PASS, disposable database; live certificate continuity still requires environment acceptance |
| Regression checks | Account Manager store/API/database/OpenAPI/auth suite; Video Cloud PKI/provider/controller and trust-consumer race tests; Cloud Admin 196 tests and Vite build; controller renderer 7 tests | PASS locally and in merged service PR CI; repeat workspace integration gate after pin update |
| dev live | Public inventory only | NOT COMPLETE |
| staging live | No mutation and no protected-environment qualification for this release yet | NOT COMPLETE |

Dev image publication attempt: the selected environment's existing registry
credential successfully authenticates and pulls, but the actual push to
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api:dev-auto-device-pki-20260919-r1`
was rejected with insufficient token scopes. The Linux/amd64 image build itself
completed. No manifest was delivered and no live workload was patched. Use the
canonical CI publication path; do not silently widen or borrow credentials.
The subsequent Cloud Admin and Video Cloud canonical main-branch image releases
succeeded. Deploy only reviewed immutable digests after every service release
completes and the environment-specific pull/preflight checks pass; the failed
dev push is not a deployment artifact.

The Account Manager integration harness now resets its bootstrap-seal fixture,
matching the store harness; otherwise another test package's sealed state could
spuriously trigger the last-administrator protection. Production safeguards are
unchanged. The managed Service client test now waits for its background worker
before directly invoking a normally serialized callback; runtime locking is unchanged.

## dev impact inventory (read-only, 2026-09-19)

Source: canonical dev kubeconfig, `video-cloud-dev-platform/postgresql-0`,
`video_cloud.pki_issuers` and `pki_certificate_bindings`. Re-read immediately before
maintenance; this is not a concurrency precondition or a current backup.

- Root: `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` (offline custody location unrecorded).
- Cloud CA: `b4da0b7b-42ff-4966-95cc-50eb77c706f0` (offline).
- Active Product: `fab94fc3-0f2c-48ff-ab0f-7b9739bb0e61`, version 4,
  mount `pki-issuers/device/fab94fc3-0f2c-48ff-ab0f-7b9739bb0e61/v4`.
- Other Product history: `760d3f92-c3f2-4676-8a4d-7d754324947d`,
  `49b8cf3f-1179-4f67-a860-3330c31019e2`, `2a3f529b-a663-418b-bc62-da4092fe6cb1`,
  `d522d10d-14eb-4d72-94c9-fd7f16d9c49e`, `22fd739c-e41f-4468-8ca7-cefff7836a7e`,
  `39bf0f29-5dbe-4af1-abfa-738249be8ddd`, `14865afa-c346-4976-b9cb-915d7b21a958`,
  `56df0589-ae15-4fc0-b4a2-b4114c5b95eb`.
- Total: 11 Device issuers. 44 binding rows have no `revoked_at`; 29 are under the
  active Product. The other 15 already have revoked parent issuers and are not
  evidence of 44 currently valid/live devices. Old test devices require reenrollment.
- Required Device receipts: `video-cloud-api,pkibroker`. Existing consumers are
  `video-cloud-api-pki` and the `pkibroker` container of `mqtt-pki`, both with private
  persisted trust state. Read actual prefixes/settings rather than assuming the
  sample environment names match each workload.
- Existing App, Service, MQTT server and OpenBao transport roots are independent
  and excluded. Retain all OpenBao mounts, including historical Device mounts.

## Ordered environment procedure

1. In dev first, inventory the exact target, issuer tree, bound test certificates,
   workloads, container names, image digests, resource versions, trust ConfigMaps,
   PVCs and named public feature settings. Preserve rollback references without
   writing credentials into this document. Record source commits and dirty-input
   fingerprints. Use the workspace scoped `lke-build-images`; its Video Cloud
   build uses the service's canonical Dockerfile so PKI binaries are included.
2. Fence Device enrollment/signing and Account Manager's automatic worker. Take a
   matched, protected backup of Account Manager, PKI registry and OpenBao state
   using the deployed storage backend's supported procedure; preserve seal custody
   separately. Do not substitute a live file copy or public certificate export.
   Verify isolated restore before replacing trust. Preserve business tables/PVCs.
3. Run explicit schema migrations with migration credentials; normal service
   credentials must not gain schema administration. Migration 078 backfills only
   pending metadata/jobs, not keys or old-CA adoption.
4. Render separate OpenBao policies with `pkicontroller`:
   `render-device-bootstrap-policy`, `render-automatic-device-policy`, and
   `render-automatic-device-signer-policy`. Add only the appropriate Device policy
   to each existing environment-local identity; preserve unrelated domain policy.
   Do not replace the ordinary signer identity with the bootstrap credential.
5. For an existing governed Device Root, run `pkicontroller reset-device-pki
   OLD_ROOT_UUID` for the public dry-run, then the same command with `--confirm dev`
   (or `staging`) against the reviewed environment. This retains history/mounts,
   marks bindings revoked and cancels old pending CA operations. It does not itself
   distribute root removal: keep writers fenced until the new consumer manifests,
   trust pools and root-policy state are installed. Never apply this to App/Service
   roots. Staging's legacy shared `pki/root` must remain; an empty Device registry
   does not require resetting a made-up governed Root.
6. Choose and record one stable new Root UUID. Run `pkicontroller
   bootstrap-device-root ROOT_UUID` with the bootstrap identity and explicit
   `PKI_ENVIRONMENT`. Repeating the same UUID resumes/reconciles; another UUID
   cannot replace a non-terminal Root. First result remains pending until real
   consumers install trust. Record its public fingerprint and immutable mount.
7. Set the paired `PKI_DEVICE_ROOT_ID`/`PKI_DEVICE_ROOT_SHA256` on the controller.
   Install the new Root-only Device manifest and corresponding pinned trust pool
   on each required consumer, using new private persistent state paths rather than
   overwriting terminal history. Enable each consumer's bundle acknowledgments and
   `<existing-prefix>_DEVICE_AUTOMATIC_STATE_DIR`. Dynamic members are accepted
   only below that pinned Root, after certificate/scope/CRL validation. Repeat the
   same Root bootstrap to complete activation after all actual receipts arrive.
8. Deploy Account Manager, controller and affected consumers using exact images
   and API-enforced resource-version/old-value preconditions. Persist the new public
   pins, manifests and images in environment-local managed configuration; a live-only
   patch is not complete. Account Manager uses its existing authenticated
   `PKI_CONTROLLER_*` transport for the machine worker. Its new migration causes
   existing/new active scopes to queue automatically.
9. If an earlier automatic epoch already completed or failed, inventory each
   retained Cloud with `rtk-account-manager-device-pki-admin --cloud-id CLOUD_UUID
   --replacement-root-id ROOT_UUID`. After confirming the controller's new pin,
   rerun with `--apply --confirm-environment dev` (or `staging`). The command requires
   matching `PKI_ENVIRONMENT` and an environment-scoped `DATABASE_URL`. Only ready/
   failed active scopes are requeued; cancelled scopes are not revived and pending
   operations are not replaced. It records old operation/issuer IDs in audit.
10. Resume workers, verify every expected active Cloud/Product reaches ready,
    replace old test-device chains by reenrollment, and run a fresh developer
    signup → verified account → Cloud ready → Product ready → Device enrollment.
    Verify owner transfer retains issuer fingerprints and removes old-owner rights,
    cross-Cloud signing is refused, and API/UI show independent PKI readiness.
11. Record actual public Root identity, mount, backup/custody references and live
    acceptance evidence in `platform_pki.md`. Only then repeat the qualified process
    for staging. Staging requires canonical CI-published images, scoped credential/
    mTLS checks and the protected-environment Go/No-Go procedure; do not promote a
    dev local image or interpret local tests as staging approval.

## Failure handling

Jobs retain stable operation IDs across retries and fence late leases. Temporary
provider failures retry; permission errors, malformed receipts, provider/registry
mismatch and unresolved ambiguous signing remain failed/reconciliation-required.
Do not delete keys to unblock a job. The `signing` journal is persisted before the
remote operation: after an uncertain reply, only a unique unrevoked certificate
with the reserved CSR key may be recovered. No second signing is attempted.

Readiness never grants Root generation to the runtime. Bootstrap is explicit and
startup never changes a Root. Provider checks compare current certificate/key
identity with registry metadata; partial restore must fail closed. Generic alerts
and persisted error codes intentionally omit provider response bodies, DSNs,
tokens and private material. Operators inspect the bounded job/issuer and public
provider metadata through their environment-local credentials, not developer CSR
or private-key upload workflows.
