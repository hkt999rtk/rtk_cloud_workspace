# Deployment service identities

Status: accepted design (operator decisions of 2026-09-29 and 2026-10-05). The operator-authority
path is deployed and verified in dev; staging and production remain separate
cutovers. The identity enrollment steps below remain gated by each environment's
issuer, bootstrap session and workload evidence.

Owner: rtk_cloud_workspace. Authority: `platform_pki.md` owns CA hierarchy and
runtime issuance; this document owns deployment credential persistence and reuse.

## Operator authority and implementation boundary

The selected environment's operator is the one human authority for initial PKI
creation and subsequent Service issuer or identity updates. The operator reads
the same environment configuration and SecretStore on every deployment, reviews
the exact issuer request digest and subject/DNS policy, invokes the configured
signer, and records the result. A separate `pki_admin` account or second human
signature is not part of this design. A dedicated PKI WebUI may remain for
inspection and operations, but its location does not change who has authority.

Record `PKI_OPERATOR_USER_ID` and `PKI_OPERATOR_SIGNER_REF` as separate files in
the selected environment's `operator/env/` SecretStore directory. Set the same
user ID on Account Manager and both values on the PKI controller. Pin both in
`pki/services/issuer.json` as `operator_user_id` and `signer_reference` before
enrolling a Service leaf. Deployment checks their exact match and rejects a
partial cutover or an identity copied from another environment. Changing the
operator or signer is an explicit handover, not a routine deployment update.

Dev completed the operator-authority cutover on 2026-09-29. The configured
Platform Admin explicitly authorized the preserved OTA Service intermediate
operation, then provisioned, imported and activated it through the authenticated
Account Manager proxy. The same operator ID and signer reference are pinned in the
dev SecretStore and live controller; Account Manager enforces that user on mutating
PKI calls. The append-only database authorization and audit rows, and the
searchable secret-free Loki event, were verified independently. This confirms the
operator path in dev, not enrollment of the later OTA workload identities. A
`requested` operation in another environment stays pending until that environment
is configured and reviewed; no database edit, fabricated approval or borrowed
account may substitute for this path. See [PKI operator authority tests](pki-operator-authority-test-plan.md).

## Persistence and release qualification

Use the selected environment SecretStore at
`~/.config/rtk_cloud/<environment>/`, with private directories 0700 and files
0600. Reuse the same registered Root, issuer bindings and workload-owned managed
identity state on subsequent deployments. An operator machine change does not
create a new environment. Do not copy a workload private key to another owner or
substitute an ephemeral bootstrap identity for an admitted runtime caller.

## Operator-held Root key custody

Custody records who controls an operator-held signing key, where its encrypted
file is stored, and how a ceremony verifies that it belongs to the registered
Root. It is a storage and recovery responsibility, not an additional approval
role. The configured environment operator may hold the key directly; a separate
custodian is optional under `platform_pki.md` section 7.

For file-backed Service and MQTT Root signers, use the selected environment's
canonical SecretStore. With the default configuration, the layout is:

```text
~/.config/rtk_cloud/<environment>/
  operator/env/PKI_OPERATOR_USER_ID
  operator/env/PKI_OPERATOR_SIGNER_REF
  pki/custody/
    <domain>-root-<version>-key-passphrase
    roots/<immutable-root-issuer-id>/
      ca-key.encrypted.pem
      certificate.pem
      csr.pem
      manifest.json
```

The encrypted key is PKCS#8 PEM, usable by the maintained OpenSSL-based
`pkiceremony` tool across operating systems. The custody manifest is UTF-8 JSON.
An OS-specific disk image such as DMG is not the canonical record or a required
storage format. Do not select an external disk directory as the primary location
without an explicit operator override of the SecretStore base. Directory modes
are `0700`, file modes are `0600`, and paths must remain within that environment
root without symlinks. Store passphrase values only in their separate private
files, never inside the manifest or reports. Keeping both files under the
operator's store does not establish independent escrow or physical offline
custody.

Each `rtk-root-custody/v1` manifest contains metadata only: environment, trust
domain, immutable Root issuer ID, registered certificate SHA-256 fingerprint,
operator user ID, configured signer reference, key format, environment-relative
key/certificate/CSR/passphrase paths, file checksums, creation time, verification
time, and the ceremony operation ID. Require the manifest's operator and signer
reference to match `operator/env/` and the issuer binding. Resolve relative paths
against the selected SecretStore root, reject escaping paths or symlinks, and
verify the decrypted key's **public** key against the registered Root certificate
before signing. Never emit the private key or passphrase during verification.

`PKI_OPERATOR_SIGNER_REF` identifies the authorized signer logically; it does not
encode a filesystem location. The manifest binds that reference and an exact
Root issuer ID to its custody files. The current `pkiceremony` CLI accepts
explicit `--key`, `--passphrase-file`, issuer, and digest arguments; it does not
automatically discover this manifest. A ceremony must resolve and verify the
record before supplying those arguments. Do not add an unsupported deployment
environment variable and claim that it configures custody.

Retain this record on operator-machine handoff. Reuse the same encrypted key and
registered Root; moving storage is not a CA rotation. For an explicitly authorized
storage migration, verify the destination's permissions, file checksums and
public-key match before retiring the former copy. A missing or mismatched key
stops the ceremony and requires an explicit recovery or rotation decision.

This convention covers operator-held Service/MQTT Root keys only. Device,
Cloud/Brand and Product CA keys remain in their approved OpenBao PKI engine;
online intermediate keys remain with their registered signer; workload leaf keys
remain in their own managed state/PVC. Never copy those private keys into operator
custody. Production's independent escrow, offline/HSM and recovery qualification
requirements still apply; a local custody manifest does not satisfy them.

## Release qualification

Accepted policy, deployed implementation and release source are separate
checks. A historical Dev authorization receipt does not prove that a newly
selected release retains the operator route. Qualify the Account Manager proxy
and PKI controller candidate together, including exact operator/signer bindings,
append-only authorization/audit, digest and policy checks, and genuine trust
consumer acknowledgments. Other environments require their own cutover.

The [Dev PKI runbook](../product-services-dev-pki.md) describes the public
CertIssuer repair and the image-only update boundary for an existing managed
PKI stack. Initial enrollment and auxiliary PKI upgrades are separate operations.

## Dev/Staging full workload overwrite policy

An environment operator may authorize complete managed Deployment-spec
replacement in Dev/Staging through the reviewed schema-1 plan at
`~/.config/rtk_cloud/<environment>/deployment/managed-upgrade.json`. The
`replace-managed-workloads` policy is environment-local, excludes production,
and is consumed by both default full preflight and full deployment. This
explicit desired-state input supersedes static workload templates for the
selected complete environment inventory; it does not change CA custody,
operator authority or the accepted certificate lifecycle.

The plan permits declared nonidentity workload configuration changes while
retaining and checking issuer/identity PVCs, mounted trust/CRLs, Secret bindings,
bootstrap/credential plumbing and StatefulSet/DaemonSet provider owners. It
requires exact-release CI/pulls, schemas/startup and server-side dry-run before
mutation, fences plan and owner drift, writes private rollback specs, and
requires actual desired specs plus PKI/public mTLS after rollout. Whole-plan
replacement must never call the legacy static CertIssuer Secret/material
reconciler. See [Full managed workload upgrade](../deployment-operations.md#full-managed-workload-upgrade)
for the normative operational schema, gates and limits. A future root/identity
replacement is an explicit accepted lifecycle operation, followed by a fresh
workload plan; a permissive boolean alone is insufficient deployment evidence.
