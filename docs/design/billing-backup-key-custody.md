# Billing Backup Key Custody Design

Status: guarded implementation; custody provisioning, role isolation and Billing backup activation require owner approval and environment qualification.

Classification: supporting-note.

Owner: `rtk_cloud_workspace` (recovery integration); environment recovery owner (custody and drills); security owner (access, rotation and incident approval).

Last reviewed: 2026-10-03.

Applies to: encryption and signing identities for Logger `billing_usage` raw
archives and inbox recovery snapshots. PKI signing keys, OpenBao seal keys,
core-backup identities and Object Storage credentials remain separate materials.

## Recommended First Approach

Use the existing age X25519 public-recipient model. Keep the private decryption
identity on an operator-controlled recovery controller outside the workload LKE
cluster, under its environment-specific SecretStore on an encrypted disk. Keep
an independently encrypted offline escrow copy accessible after loss of both
that controller and the workload cluster. Backup writers receive public
recipients only, not private identities.

This is a proposed custody location, not a claim that a key or controller exists.
Start with the file-based interface the current recovery code supports. An
external recovery vault is a later qualified option, not a new dependency added
to the deployed system by this document.

The [billing lifecycle design](billing-raw-data-lifecycle.md) owns export,
verification and local-retirement gates. [Object Storage Policy](../object-storage-policy.md)
owns buckets, namespaces and data retention. [SecretStore](../secret-store.md),
[secret governance](../deployment-secrets-governance.md) and
[core recovery](../backup-restore.md) remain authoritative for current operator
files and recovery procedures. This document supplies the proposed Billing
custody boundary. Billing-specific guarded implementations are distinct from the
existing core format and remain unqualified for live activation.

## Current Implementation and Limits

| Evidence | Implemented behavior and limit |
| --- | --- |
| [Recovery config](../../scripts/go/rtk-cloud/internal/recovery/types.go), `Config.Validate` | `recipients` accepts age X25519 public recipients. Unknown config fields are rejected. No key registry, automatic recipient selection or custody connector exists. |
| [Archive implementation](../../scripts/go/rtk-cloud/internal/recovery/archive.go), `Pack` / `Unpack` | Encryption uses public recipients through the Go age library. Decryption reads an explicit identity file and authenticates the complete encrypted stream and archive hashes. The format is `core` tar, not the proposed Billing NDJSON/bbolt archives. |
| [Recovery CLI](../../scripts/go/rtk-cloud/recovery.go), `runRecovery` | `restore inspect`, `apply` and `retry` accept `--identity` as a file path. No key is fetched from OpenBao/KMS or generated when missing. The reader does not directly unwrap a passphrase-encrypted escrow file. |
| [SecretStore implementation](../../scripts/go/rtk-cloud/secret_store.go) | The root defaults to `~/.config/rtk_cloud/<environment>/`, with a configurable parent. `operator/env` holds environment-variable credentials; the runtime catalog has no Billing backup identity entry. |
| `AllowedSecretPath` in recovery config | Core secret capture permits selected `runtime/`, `pki/` and OpenBao TLS material, not `operator/`. The proposed operator custody subtree is outside core archive selection; restore preserves unselected operator material. |
| [Core archive tests](../../scripts/go/rtk-cloud/internal/recovery/archive_test.go) | Cover round trips, tampered/truncated ciphertext, unsafe identity-file permissions and operator-material rejection in core config. These are not escrow, controller-loss or Billing automation qualification. |
| [Billing controller](../../scripts/go/rtk-cloud/internal/billingbackup/controller.go) | Explicit registered age recipients and Ed25519 public keys, owner/private-ancestor custody checks, bounded private scratch, immutable signed receipts and exact raw/snapshot binding. Keys are supplied explicitly; no vault fetch, replacement or escrow unwrapping is automatic. |
| [Billing recovery admission](../../repos/rtk_cloud_logger/billingarchive/recovery.go) | Distinct recovery public registry and maximum-15-minute domain-signed state admission. An ordinary verifier signature cannot unlock a restored Logger. Independent owner reconciliation is still required. |

`Unpack` checks the identity file itself is a private regular file with no
group/other access. It does not establish the expected owner, environment/key-ID
binding, encrypted disk, all ancestor permissions or independent escrow. Those
are required custody checks, not existing CLI guarantees. Permissions do not
isolate processes running as the same operating-system user.

## Where Each Material Is Stored

| Material | Proposed location | Access and purpose |
| --- | --- | --- |
| Public recipient and non-secret key ID | Reviewed environment intent/key registry outside secret material; the current core equivalent is its config's `recipients` array. | Writer may read. Changes require custody-owner approval and exact public-recipient fingerprint checks. Do not add unsupported fields to today's core config. |
| Primary private age identity | `<resolved-environment-secret-root>/operator/recovery/billing-backup/<key-id>.agekey` on the off-cluster recovery controller's encrypted disk. | Recovery custodian owns it. Only isolated, approved verification/recovery may use it; writers, services and ordinary deployment CI receive none. |
| Verifier signing identity | `<resolved-environment-secret-root>/operator/recovery/billing-backup/<verifier-key-id>.ed25519key` on the isolated verification controller. | Independent Ed25519 key; signs exact verification receipts. The writer receives only its reviewed public key. Never reuse age, PKI or SSH keys. |
| Recovery admission signing identity | `<resolved-environment-secret-root>/operator/recovery/billing-backup/<recovery-key-id>.ed25519key` under separate recovery-custodian access. | Distinct from the automatic verifier; attests independently approved exact allocation frontier, decision history, dependencies and Pg checkpoints. Logger receives only its reviewed public key. |
| Independent escrow copy | Encrypted offline recovery medium in separate physical custody; its media/custody ID and retrieval procedure are in the restricted recovery inventory. | Alternate custodian can retrieve it without the original controller, LKE, OpenBao or Linode account. Escrow-unlock access is independently recoverable. |
| Temporary native identity | A dedicated private directory on the isolated recovery controller, on encrypted storage or qualified protected memory-backed storage. | `0700` directory, `0600` regular file. Pass only its absolute path to the existing core CLI; remove the temporary copy after use. |
| Archive ciphertext | Environment-owned private `billing-backup` bucket under Object Storage Policy. | Contains no private identity or escrow-unlock secret. Storage access credentials do not decrypt payloads. |

The default primary path expands to:

```text
~/.config/rtk_cloud/<environment>/operator/recovery/billing-backup/<key-id>.agekey
```

Resolve the configured SecretStore parent, including an approved
`RTK_CLOUD_CONFIG_ROOT` override, before recording the absolute operational path.
Environments never share identities or fall back to another environment. Use a
versioned ID such as `billing-backup-staging-v1`; never replace that identity
with a new key during rotation. This is a new reserved custody convention:
`secrets init`, `ensure`, `verify` and `inventory` do not automatically generate,
rotate or certify these keys. It is not `operator/env` or `runtime/` and has no
Kubernetes binding.

Custody directories must be real, owner-controlled and private (`0700` within
the environment root); reject symlink ancestors, unsafe ownership and scope
mismatches before opening an identity. Restrict the root to the custodian's OS
identity. The public-only writer must run under a separate principal/isolation
boundary without that root readable or mounted. A public-only encryption
argument does not prove the writer lacks filesystem access to private keys.

Do not put identities in Git, `cloud_env/.../runtime`, images, Helm values, LKE
Secrets/PVCs, the workload cluster's OpenBao, the billing bucket, core archives,
logs or ordinary GitHub Actions bundles. Do not copy the full SecretStore to a
writer or runner. The current
[CI materializer](../../scripts/ci/materialize-rtk-cloud-secret-store.sh) rejects
the entire bundle before its first write if a `files` entry selects
`operator/recovery/`, `.agekey`, or `.ed25519key` (case-insensitive). This narrow
exclusion does not establish writer filesystem isolation or inspect secret
contents. Audit bundles without exposing secret values.

Independent escrow must survive controller/disk loss, custodian absence and
Linode account loss. A second folder on the same disk/controller, or a key
encrypted only to itself, is not independent recovery. Encrypted disk protects
powered-off storage, not a compromised logged-in host.

## Ownership, Access and Registration

- The environment recovery owner records key ID, scope/purpose, public recipient
  and fingerprint, creation/activation dates, custody references, state,
  authorized users, last escrow/restore test and archive dependencies. Public
  intent contains non-secret metadata only; exact private locations, custody
  contacts and unlock procedures stay in the restricted recovery inventory.
- Primary and alternate custodians control private-key/escrow retrieval.
  Production activation, private-key release, custody transfer and destruction
  require a second designated person's approval and an independently retained
  audit receipt. Today's CLI does not enforce this; an approved operator
  procedure or future controller must do so.
- The backup writer receives approved public recipients and its scoped storage
  credential. Separate verification/recovery can decrypt in isolation and must
  not silently mutate production or erase evidence.
- The financial owner approves data retention and holds, not key deletion merely
  because local hot data reached 90 days.

With age multiple recipients, any matching identity can decrypt. This is not
two-person cryptographic control or a threshold scheme. The initial primary and
escrow copies preserve the same versioned identity. Use distinct keys for each
environment and for Billing versus general core recovery; never reuse a device
CA, OpenBao seal key or SSH login identity.

## Generation, Verification and Recovery

1. Generate a fresh age X25519 identity on the approved recovery controller
   using the reviewed library or a vetted tool. Current SecretStore commands do
   not implement this workflow. Disable secret-bearing terminal capture; never
   put private contents in argv, environment variables or logs.
2. Install it at the reserved path with required ownership/permissions. Create
   independently encrypted escrow, with separate unlock custody. A missing or
   unreadable identity must never trigger automatic replacement.
3. Derive its public recipient; match the registered key ID, environment and
   approved fingerprint. Retrieve/decrypt the escrow copy on a clean isolated
   controller and test a representative encrypted set before activation.
   Public-recipient replacement is security-sensitive despite being non-secret.
4. Activate the approved recipient for new exports. Proposed Billing manifests
   and catalog entries record key ID and recipient fingerprint per set. These
   are not new fields supported by today's core manifest/config. Keep mappings
   and verification receipts independently recoverable, not only inside an
   encrypted file requiring an unidentified key.
5. The writer captures/encrypts/uploads without the identity. Ciphertext SHA-256
   readback proves uploaded bytes, not recoverability. A separately authorized
   verifier decrypts the exact set and checks receipt bindings, precision,
   sequence coverage and snapshot dependencies under the lifecycle design
   before publication of verified completion can unlock retirement.
6. For recovery, approve the environment, key ID, archive set and operation.
   Retrieve the original matching identity and unwrap escrow outside the current
   CLI when necessary. Supply a private native identity file, not the encrypted
   escrow wrapper. Existing core CLI commands read core archives only; Billing
   formats require their own implemented reader and recovery qualification.

An unattended daily verifier needs a separately approved key-delivery path,
isolation and audit, not a permanent key mounted into the uploader or workload
Pod. Until qualified, use controlled verification and leave unverified sets
incomplete; upload success alone cannot certify daily protection or retirement.

Remove temporary identities and plaintext outputs after success/handled failure.
After a crash, inspect only the operation's recorded private work directory;
never remove the primary identity or unrelated files. Qualify core-dump, swap,
host-backup and snapshot controls against key leakage. Ordinary file deletion
is not a secure-erase guarantee on SSD/snapshotting storage; use the approved
storage lifecycle.

Age authenticates ciphertext integrity, not the encrypting party. Anyone with
a public recipient can create ciphertext. Validate provenance against approved,
independently retained catalog/verification receipts, not just a replaceable
manifest/marker that decrypts. The lifecycle implementation adds an independent
Ed25519 verification identity: sign domain-separated exact payload bytes binding
environment, stack, StoreID, set ID, manifest and ordered object hashes, horizon,
key references and verification policy. Verification public keys come only from
the reviewed registry; a key embedded in a receipt is never trusted. The
uploader cannot certify its own backup with an unsigned completion marker.

Signing identities follow the same private-controller/independent-escrow
boundary, but are separate from encryption identities. Register rotation and
revocation independently and preserve historical public verification keys.
After suspected signer compromise, do not rely on a receipt's self-declared
timestamp: independently anchored audit evidence or fresh verification must
qualify affected sets before retirement resumes.

## Rotation, Loss and Destruction

Generate/test a new version before switching new writes to its recipient. Keep
old identities decrypt-only while any retained archive, snapshot dependency or
hold needs them. Normal rotation does not replace completed objects or discard
old decryption capability. Approve the review period per environment; 90-day
local hot retention is not a key expiry or destruction deadline.

For controller/key loss, recover the original identity from independent escrow
and test it on a clean controller. Without a valid original identity, declare
affected data unrecoverable, block completeness/retirement claims and follow
the incident process. A new identity cannot decrypt old ciphertexts.

For suspected compromise, stop new writes to that recipient, restrict access,
activate a tested replacement and inventory exposed archive versions/copies.
Changing recipients or re-encrypting new copies does not undo exposure of
ciphertexts already available to the attacker. Replacement archives require
approved mapping, provenance and dependency reconciliation under Object Storage
Policy; never silently overwrite historical evidence or held data.

Destroy a version and all escrow copies only after financial/recovery owners
confirm no retained object/version, snapshot, hold or recovery dependency needs
it and approve a disposition receipt. Inventory controller, media and host-backup
copies and preserve non-secret dependency history. This is a data-loss action,
not routine rotation.

## Rollout and Qualification

Record the actual recovery controller, encrypted-storage controls, primary and
alternate custodians, escrow medium/retrieval access, key IDs/recipients,
environment separation and approved retention before activation. No live key
location has been inspected or certified by this document.

Qualification must demonstrate:

- independent escrow recovery after simulated controller loss and unavailability
  of LKE/OpenBao/Linode account access;
- wrong/missing identity, substituted recipient, scope mismatch, unsafe file or
  ancestor ownership/permissions and symlink paths fail closed;
- writer identity access is denied, ordinary CI cannot carry identities, and
  core capture cannot include the operator custody subtree;
- old/new versions decrypt their approved dependency sets, including holds,
  rollback and explicit loss/compromise handling;
- interrupted verification/recovery leaves no unrecorded key/plaintext copies,
  and approval/audit receipts remain independently available;
- Billing verification and coordinated recovery work at representative size;
  existing core crypto tests alone are not evidence of these new controls.

First deliver registration/custody checks, CI exclusion and isolated verification
without local deletion. Implement Billing archive/readers under the lifecycle
plan, then qualify daily operations/recovery before enabling retirement. This
documentation change creates no key, escrow medium, credential, controller,
scheduled job, vault or deployment.

An approved independent external vault may later replace primary file custody.
It must remain accessible without the workload cluster, have tested seal/auth/
backup recovery and preserve key IDs/escrow dependencies. Today's reader would
still need approved native-identity-file materialization or a separately
implemented crypto adapter. OpenBao Transit/KMS, HSM plugins and encrypted
identity files are not assumed drop-in replacements. Keep one writable custody
authority per version and record a reviewed cutover, not parallel key sources.

## External Design References

The [age documentation](https://github.com/FiloSottile/age#usage) distinguishes
public recipients from identities and explains any-recipient decryption. Its
full CLI supports features beyond this workspace's narrower reader; current
code above determines compatibility.
[OWASP key/data separation](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#separation-of-keys-and-data)
supports custody separate from encrypted backups. Neither reference proves
deployment or qualification of the proposed controls.
