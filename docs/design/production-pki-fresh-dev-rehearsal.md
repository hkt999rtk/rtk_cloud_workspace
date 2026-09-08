# Fresh dev PKI rehearsal

## Scope

The user authorizes temporary accounts/devices and dev database resets when
useful. Existing dev issuance data is not a migration requirement. Staging is
untouched. MFA remains disabled and is optional future human-login functionality
only; devices never use it. Legacy fleet migration is deferred.

## Verified on 2026-09-08

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

1. Install the new Root in the actual API TLS runtime and acknowledge the exact
   installed bundle. Then activate it through the governed API.
2. Create a fresh Cloud/Product and their Brand/Product issuers. Keep Brand keys
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

The next concrete bootstrap issue: the API's automatic root-policy synchronizer
expects an already active/retiring Root, while the first Root needs actual
installation acknowledgment before activation. Complete a real initial
installation path before starting automatic synchronization. Do not bypass
activation checks or pretend that writing a file is a runtime reload.

## Retained configuration and artifacts

The canonical dev SecretStore contains:

- `pki/controller-bootstrap/rollout/pki-controller.json`: controller overlay,
  including scoped NetworkPolicy; `controller-auth-probe.json`: public TLS results.
- `pki/controller-bootstrap/rollout/account-manager-pki-migrate.json` and
  `account-manager-pki.json`: exact scoped schema/runtime manifests.
  `account-manager-before.json` retains the prior Deployment for review/rollback.
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
