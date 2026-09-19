# Automatic Device PKI: non-production rollout and evidence

Authority: [platform_pki.md](../repos/rtk_cloud_contracts_doc/platform_pki.md),
especially §§8–9. This runbook implements that specification; it does not authorize
production, offline key import, dual-chain migration, or unrelated data deletion.
The current dev/staging rebuild has no backup or restore-drill prerequisite by
explicit non-production decision. Old Device CA identities and affected test
certificates are not recoverable through this rollout; production keeps the
separate backup and recovery gates in the Platform PKI contract.

## Current qualification (2026-09-20)

The contracts, Account Manager, Cloud Admin and Video Cloud implementation is
merged, including the dev-only Root CRL recovery command. Live acceptance is
**not complete**. Account Manager migration 078 is applied in dev; no live
Device Root was changed.
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
| dev live | Migration 078 applied. Seven Cloud and 21 Product CA outbox jobs remain pending with zero attempts; PKI controller and certissuer have zero ready replicas while offline Service/OpenBao TLS Root CRLs are expired. The active offline keys were found in the `m1.local` dev SecretStore, so refresh the governed CRLs rather than rebuilding those Roots. | NOT COMPLETE |
| staging live | No mutation. Read-only qualification gave a NO-GO verdict; full protected-environment gates remain incomplete. | NOT COMPLETE |

Dev image publication attempt: the selected environment's existing registry
credential successfully authenticates and pulls, but the actual push to
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api:dev-auto-device-pki-20260919-r1`
was rejected with insufficient token scopes. The Linux/amd64 image build itself
completed. No manifest was delivered and no live workload was patched. Use the
canonical CI publication path; do not silently widen or borrow credentials.
The subsequent canonical main-branch image releases succeeded. Candidate
linux/amd64 digests, still subject to environment-specific pull/preflight checks:

| Service | Merged commit | Published image digest |
| --- | --- | --- |
| Account Manager | `44defddccc1cb91d574e2ae158052c404e1d7d0d` | `ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:a986bb3b3df32992aeedf43c1ec712a906eff920ae9b42d5bb7ee841bde39ab9` |
| Cloud Admin | `155750061445c405b39ff5c4b8731194bcb03d96` | `ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:ea3e46d8dbf506d102e62060710ecec88396c8f068d0ac61d169f815fa35ff6c` |
| Video Cloud API/controller | `ed215e7ad5f586312395d29be0a3da2475140905` | `ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:5b4da7617f59b7db1bdfe63b6f48dd9207a944399641c3df92f98fb6fac087d3` |

The Video Cloud digest was published by the successful main-branch release
workflow for `ed215e7`; it contains the dev recovery command. Confirm the
environment can pull this exact digest before using it in dev maintenance.

The failed dev push is not a deployment artifact.

The Account Manager integration harness now resets its bootstrap-seal fixture,
matching the store harness; otherwise another test package's sealed state could
spuriously trigger the last-administrator protection. Production safeguards are
unchanged. The managed Service client test now waits for its background worker
before directly invoking a normally serialized callback; runtime locking is unchanged.

## dev impact inventory (read-only, 2026-09-19)

Source: canonical dev kubeconfig, `video-cloud-dev-platform/postgresql-0`,
`video_cloud.pki_issuers` and `pki_certificate_bindings`. Re-read immediately before
maintenance; this is not a concurrency precondition or a current backup.

- Root: `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` (legacy offline key
  verified on `m1.local`; see the contract inventory).
- Cloud CA: `b4da0b7b-42ff-4966-95cc-50eb77c706f0` (legacy offline key
  likewise verified on `m1.local`).
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

### dev non-Device Root CRL recovery (read-only inventory, 2026-09-20)

The active Service Root `697e8e86-5af6-4580-8456-7f91d17634f2` has public
SHA-256 `32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb`.
The active OpenBao TLS Root `ffd5f2b4-c675-47da-9534-e2e86820d8a1` has
public SHA-256 `711e3a9f722acab6111a1bbed610693cf6b9e11fdd47a153727ec7805aa681b6`.
Their encrypted offline keys and separate passphrase references were found in
the `m1.local` dev SecretStore and their public keys match the registered
certificates; exact custody paths are in `platform_pki.md` §8.4. Neither key nor
passphrase is copied into this workspace. This is dev rehearsal custody, not a
production backup or restore qualification. Their registered Root CRLs expired
at 2026-09-17 07:17:30 UTC and 2026-09-18 02:08:42 UTC, respectively. The
conditional authorization to rebuild these non-Device chains does not apply
while the old keys remain available.

The normal controller cannot import the first replacement Root CRL while its
own Service/OpenBao transport admission rejects expired CRLs. The dev-only
`pkicontroller recovery-import-dev-root-crl` command bridges that initial
registry recovery. It accepts only an existing active dev Service or OpenBao
TLS Root, checks the pinned certificate and the exact independently reviewed
request digest against the signed CRL, verifies signature and freshness, and
preserves prior revocations. By default it validates without changing the
database; `--confirm dev` imports with a distinct audit actor. It never reads
a private key, impersonates a human approver, or runs against staging or
production. The offline signing request must be independently reviewed before
the existing Root key is used.

```sh
# Bind PKI_DATABASE_URL privately to the selected dev controller database.
PKI_ENVIRONMENT=dev pkicontroller recovery-import-dev-root-crl ROOT_UUID ROOT_SHA256 \
    REVIEWED_REQUEST_JSON REVIEWED_REQUEST_SHA256 SIGNED_CRL_PEM
# Repeat the same command with --confirm dev only after checking the dry run.
```

Importing the public CRL row alone does not restore controller readiness.
Install the exact signed CRL on each real consumer's private trust state,
reload it, and require its authenticated digest acknowledgment. Only after
Root transport trust recovers should the normal worker refresh stale
intermediate CRLs. Never fabricate receipts, disable CRL checks, or replace an
unrelated Root or OpenBao mount. Re-read live workload volume references and
preserve deployment rollback references immediately before maintenance.

The Service Root CRL consumers below each own a distinct PVC; identical path
strings across workloads do **not** imply shared state. `{service}` denotes
the Service Root issuer ID above.

| Consumer Deployment | PVC | Service Root CRL state path(s) |
| --- | --- | --- |
| `account-manager` | `account-manager-service-identity` | `/var/lib/account-pki/private/account-listener-crl-{service}.json` (listener and egress manifests) |
| `certissuer` | `certissuer-service-identity` | `/var/lib/pki-host/identity/crls/{service}.json` |
| `factoryenroll` | `factoryenroll-service-identity` | `/state/identity/crl-{service}.json`, `/state/identity/account-manager-crl-{service}.json` |
| `pki-controller` | `pki-controller-service-identity` | `/var/lib/pki-host/identity/crls/{service}.json` |
| `video-cloud-api` | `video-cloud-api-service-identity` | `/var/lib/video-cloud-api-pki/account-manager-crl-{service}.json` |
| `video-cloud-logingester` | `video-cloud-logingester-mqtt-pki-state` | `/var/lib/mqtt-pki/identity/service-crls/{service}.json` |

The controller and certissuer also hold the OpenBao TLS Root CRL at
`/var/lib/pki-host/identity/openbao-tls-crl-ffd5f2b4-c675-47da-9534-e2e86820d8a1.json`
in their respective PVCs. The observed public manifests were ConfigMaps
`pki-service-client-crls-fba75443073d` and
`pki-openbao-tls-crls-post-withdraw-0b0e251d-8286959f`.
`pkitrust apply-crl` can atomically replace expired signed CRL state on disk,
reject rollback or changed prior revocations, and tolerate an exact retry. It
requires a runtime reload and never fabricates a consumer acknowledgment.

The dev recovery importer passed a disposable PostgreSQL 16 test covering
dry-run, apply, idempotent retry, and an unreviewed digest rejection. Focused
`pkitrust` tests cover expired-state repair and signed monotonic history. These
are local tests, not evidence that any live dev CRL was signed, installed, or
acknowledged.

## Ordered environment procedure

1. In dev first, inventory the exact target, issuer tree, bound test certificates,
   workloads, container names, image digests, resource versions, trust ConfigMaps,
   PVCs and named public feature settings. Preserve rollback references without
   writing credentials into this document. Record source commits and dirty-input
   fingerprints. Use the workspace scoped `lke-build-images`; its Video Cloud
   build uses the service's canonical Dockerfile so PKI binaries are included.
2. Fence Device enrollment/signing and Account Manager's automatic worker.
   Record the existing Device issuer IDs, public fingerprints, bindings and
   affected test-device chains before replacing trust. This dev/staging rebuild
   intentionally has no matched backup or isolated restore gate: do not claim
   that old Device identities can be recovered after reset. Keep OpenBao seal
   access available for service continuity. Preserve business tables/PVCs and
   all unrelated App, Service and transport-TLS mounts; do not clear the whole
   database or OpenBao merely because test-device certificates will be replaced.
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
   marks bindings revoked, cancels old pending CA operations, and atomically
   publishes the old Root in the cumulative distrust policy. This records the
   removal but does not install it on consumers: keep writers fenced until the
   new consumer manifests, trust pools and root-policy state are installed and
   acknowledged. Never apply this to App/Service roots. Staging's legacy shared
   `pki/root` must remain; an empty Device registry does not require resetting
   a made-up governed Root.
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
11. Record actual public Root identity, mount, seal custody reference, explicit
    absence of a non-production backup, and live acceptance evidence in
    `platform_pki.md`. Only then repeat the qualified process
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
