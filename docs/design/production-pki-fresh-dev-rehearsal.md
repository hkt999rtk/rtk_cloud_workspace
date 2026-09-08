# Fresh dev PKI rehearsal

## Scope

The user authorizes temporary accounts/devices and dev database resets when
useful. Existing dev issuance data is not a migration requirement. Staging is
untouched. MFA remains disabled and is optional future human-login functionality
only; devices never use it. Legacy fleet migration is deferred.

## Current live checkpoint: initial trust complete

Video Cloud `ad08eef`, workspace `0e12b30`. Deployed the separate dev API listener
`video-cloud-api-pki` with verified image and running image ID
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:c79a651d2807db0bad26b5872784fc435f2833f9143ce042ade710b5f8d2fea7`.
The existing API Deployment and plaintext Service were preserved.

The API verified the reviewed Root bundle against its loaded TLS client pool
and acknowledged bundle
`b72ea85670db35f6a8edada250a1dc66fd4cf4581863d9e21be23133fc32e07b`
as consumer `video-cloud-api`. This acknowledgment came from the API process,
not the operator script. The requester then activated Root
`c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` through Account Manager. The Root is active.
The direct TLS listener rejected missing client identity, a management identity
used as a Device identity, and an incorrect server name. These are negative TLS
checks; a real Product device connection is still pending.

Continuous root-policy synchronization is now enabled. Its state is stored on
PVC `video-cloud-api-pki-trust`, class `linode-block-storage-retain` (requested
5Gi, CSI provisioned 10Gi). The init container copies the authenticated initial
state only if no state exists, then the API fetches and installs the current
policy before listening. Policy acknowledgment digest:
`96381e5ae666aa055fa02db383fdadeb5b581a13e234f26406050da6c7b5b455`.

A controlled Recreate restart changed the API pod UID from
`33ef644d-965e-4a0b-8c7b-065aeb2bee50` to
`7763f25e-6316-4d2e-905e-46ce23c588ee`. The exact image ID and persisted state
SHA-256 `e849ecce2ef73602e133ec69ee3fc181bb4bd1af9c9d355bcb81475f64b152d1`
were unchanged; the replacement became Ready with current policy acknowledgment.
This verifies initial trust persistence/reload, not device renewal recovery.

Fresh Cloud `5382d0cf-0966-45e9-ad5e-9955f4f6360d` and Product
`773aa199-16e3-4d59-94c6-5cdb5801c02f` are active in Account Manager. They were
created through the authenticated administration API with the temporary requester
as designated owner. Brand issuer `b4da0b7b-42ff-4966-95cc-50eb77c706f0` is active after distinct
approvals, an encrypted local key ceremony, Root signing, import and actual API
bundle acknowledgment. Activation before that acknowledgment returned 409.
Cloud and Product creation alone does not generate private CA keys.

Product v1 issuer `2a3f529b-a663-418b-bc62-da4092fe6cb1`, operation
`b09930b0-a32f-4151-83e5-b74cda10d352`, received distinct approvals but provisioning
failed before key generation. OpenBao rejected its nested mount with HTTP 400:
`path is already in use at pki/device/`. Provider inventory confirms the target
mount is absent. Existing legacy mounts remain unchanged. Its provisioning record
is retained as evidence; do not retry key generation or rewrite that reference.

The corrected design reserves new issuers at
`pki-issuers/<domain>/<issuer_id>/v<version>`. Existing governed references retain
their exact old layout. Next, deploy the namespace correction, remove the unused
v1 controller policy and reserve an approved Product v2. This is a fresh dev
provisioning repair, not migration work.

## Earlier bootstrap verification on 2026-09-08

- Dev context `lke649805-ctx`; namespaces `video-cloud-dev-video-cloud` and
  `video-cloud-dev-account-manager`.
- Controller image and running image ID:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:be8d897147d3aa297a2f58a1c3f284d44c403d4ece7b1d60d099e94643f83d35`
  (service source `0dca4d9`). Deployment is Ready.
- Account Manager image:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:4ee5be3c53a666c8fb9df4149000cb6ab8c6311d568b51cfdcf606339a943288`
  (clean service source `63c928f`). Registry digest verified; schema Job
  `account-manager-pki-migrate` completed and the Deployment is Ready.
- Account Manager uses the separate access/refresh RSA keys and management mTLS
  identity prepared earlier. Existing HS256 access/refresh tokens require fresh
  login. Existing workers were not upgraded. No database reset was needed.
- Controller mTLS accepts Account Manager and a separate `video-cloud-api`
  management identity. Missing identity, wrong server name and a server-only
  client identity are rejected. The API identity cannot invoke human endpoints;
  Account Manager without its request-bound human assertion is rejected.
- Live Account Manager-to-controller testing exposed missing ingress under the
  existing default-deny policy. Video Cloud `14ff366` adds the scoped policy to
  the renderer. Dev policy is installed; five renderer tests and the authenticated
  request passed. No staging policy was changed.
- Three ordinary verified fixture users were seeded in the dev database without
  PKI roles or email delivery. All password logins ran through Account Manager,
  with RS256 tokens and no MFA claim. Each was denied PKI access before its role
  assignment. The existing authenticated dev administrator assigned requester
  and approver `pki_admin` and a distinct `security_custodian` through the ACL API.
  These are simulated test actors, not independent human custody evidence.
- Device Root operation `f08aac85-a944-4e7f-a52a-49676ddc78a3`, issuer
  `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8`, reached `ready` after distinct approvals,
  CSR registration and certificate import. Self-approval and a PKI administrator
  claiming the custodian role returned 403. Activation without the runtime trust
  acknowledgment returned 409.
- The existing ceremony CLI generated an encrypted P-256 Root key locally and
  produced the public CSR/certificate. Only public artifacts went to the
  controller. This is a dev simulation on the workstation, not a physically
  offline ceremony or qualified escrow.

## Remaining acceptance work, in order

1. **Done:** API initial trust installation, actual bundle acknowledgment, governed
   Root activation, continuous policy synchronization and persisted-state restart.
2. **In progress:** Brand activation is complete; finish Product provisioning. Keep Brand keys
   in the encrypted local ceremony simulation and generate Product keys inside
   isolated OpenBao mounts with per-issuer policies. Verify key non-export and
   actual consumer installation before activation.
3. Enroll a fresh device with its own key; verify direct-mTLS authentication and
   renewal. Check restart and interrupted renewal.
4. Verify revocation denial and live-session termination, then add the compatible
   MQTT broker and its consumers to the required trust installation set.
5. Retain pass/fail results and a reproducible setup for the full dev lifecycle.

Initial HTTP acceptance currently requires consumer `video-cloud-api`. MQTT is
not included in that phase and is not yet qualified. Do not count missing
acknowledgments as user-input blockers or send fabricated acknowledgments.

The resolved bootstrap issue was: the API's automatic root-policy synchronizer
expects an already active/retiring Root, while the first Root needs actual
installation acknowledgment before activation. The new initial
installation path now resolves that cycle and automatic synchronization is running.
Activation checks remain enforced; disk-only installation is never a runtime ACK.

The initial installation path uses an explicit reviewed manifest of issuer IDs
and exact trust-bundle versions. API startup re-reads those issuers from its
registry, checks environment/domain/status/version and verifies their full chains
against its actual loaded TLS client trust pool before sending acknowledgments
with its own management identity. It never adds trust from the manifest. Any
validation or delivery failure fails startup; a restart safely repeats the exact
acknowledgment. After initial Root activation, configure the existing dynamic
root-policy synchronizer for continuing trust removal. This initial bootstrap
does not itself claim continuing revocation or MQTT acceptance.

## Retained configuration and artifacts

The canonical dev SecretStore contains:

- `pki/controller-bootstrap/rollout/pki-controller.json`: controller overlay,
  including scoped NetworkPolicy; `controller-auth-probe.json`: public TLS results.
- `pki/controller-bootstrap/rollout/account-manager-pki-migrate.json` and
  `account-manager-pki.json`: exact scoped schema/runtime manifests.
  `account-manager-before.json` retains the prior Deployment for review/rollback.
- `pki/controller-bootstrap/rollout/video-cloud-api-pki-deployment.json`, the
  matching Service/ConfigMap/PVC manifests and initial-state manifest retain the
  scoped API overlay. `PKI_API_IMAGE` pins its verified dev image;
  `PKI_CONSUMER_PODS=video-cloud-api-pki` maps its pod name to the existing
  `video-cloud-api` management identity. The restart-check annotation is transient
  operational evidence and does not change the persisted workload configuration.
- `pki/fresh-rehearsal/device-root-active.json`, `cloud.json`, `product.json`,
  `api-restart.json` and `api-tls-negative-probe.json` retain this checkpoint.
- `operator/env/PKI_CONTROLLER_IMAGE`, `PKI_REQUIRED_CONSUMERS`,
  `PKI_ACCOUNT_MANAGER_IMAGE` and the selected JWT/PKI file settings record the
  overlay configuration. These separate PKI overlays are not automatically
  applied by the default full-platform provisioner. Apply the retained overlay
  deliberately with current resource-version checks; do not assume a full
  provision preserves it or point all workers at the new Account Manager image.
- `pki/fresh-rehearsal/`: protected fixture account credentials, public operation
  and issuer snapshots, and encrypted local ceremony outputs.
- `pki/rehearsal-passphrases/`: protected dev simulation passphrase files. They are
  on the same workstation and do not constitute independent custody.

Credential files are not tracked or included in reports. Root/Brand keys never
belong in service database rows, container images or Kubernetes workloads.
No git push, PR, CI run or staging mutation was performed.
