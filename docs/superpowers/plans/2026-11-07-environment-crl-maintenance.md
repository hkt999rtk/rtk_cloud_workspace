# Environment CRL renewal and recovery

User authorization: implement environment-generic renewal/recovery and automatic pre-expiry renewal; test the complete flow, repair staging, and rerun its full health check until PASS. Existing identities, data, and unrelated checkout edits must remain intact.

## Design

Keep encrypted offline Root keys on the environment operator host. Reuse pkiceremony for signing and native registry/consumer validators for publication and installation. Add a maintained `deployment renew-crls` entry and shell facade. Manual and scheduled execution share the same code. The selected canonical `~/.config/rtk_cloud/<environment>` is the only operational store (0700 directories/0600 files).

Support explicit dev, staging, and prod targeting without a dev checkpoint requirement. Read-only status/plan is the default; explicit execution and stack confirmation authorize mutations. Discover original issuers and consumer bindings from their current registry and actual mounted manifests. Pin environment, cluster, issuer fingerprint and predecessor digest. Reuse original signing keys, preserve every revocation, use monotonically increasing CRL numbers, and resume previously signed/published work rather than issue duplicates.

For offline roots, use a six-day signed validity and a 48-hour renewal lead within the existing seven-day verifier ceiling. A host scheduler runs hourly, including after host resume; missing signing material is a reported failure, never permission to create keys. Installation is environment-specific; only staging is enabled live in this task. The host must remain available; expose last-success/next-expiry/failure status and scheduler logs. Online issuers retain their native signer/worker; verify their current evidence and recover a due issuer through its original signing provider when needed.

Expired Service/OpenBao trust must not block the recovery transport itself. Use an explicit operator-controlled Kubernetes/native recovery channel with exact original issuer/cluster pins, guarded import and native PVC installation, never disabled TLS checks, direct ad-hoc SQL writes or fabricated consumer ACKs. Required consumers must actually reload and acknowledge the signed successor. Preserve prior evidence and do not reopen bootstrap.

Alternatives considered: putting offline Root keys in a Kubernetes CronJob would change custody and is rejected. A manual-only recovery or merely extending CRL TTL does not satisfy automatic renewal. The chosen operator-host scheduler preserves custody and shares the tested maintenance engine.

## Implementation and verification

- [x] Native environment-generic guarded publication/installation capability, retaining dev compatibility. Tests: wrong environment/key/issuer/predecessor, dropped revocations, stale/rollback signatures, exact idempotent retry, expired prior CRL, no forged ACK.
- [x] Workspace maintenance facade, discovery, due calculation, per-environment exclusion, resumable private journal, and bounded execution. Tests: healthy no-op, pre-expiry renewal, expired trust recovery, interruption after sign/import/install, cross-environment rejection, missing custody and unchanged permanent identity/data.
- [x] Hourly launchd/systemd scheduler with installation/status/removal and stable executable binding. Tests: correct environment/path/permissions, no secrets in scheduling files, no overlap, failed/missed runs visible, same maintenance entry used.
- [x] Documentation: operational command, state producer/consumer paths, custody and availability requirements, renewal policy, emergency recovery and evidence.
- [x] Complete isolated flow with actual cryptographic CRLs, native registry and consumer behavior; controlled clock for expiry without day-long sleeps. Focused tests before publication; equivalent required CI owns full coverage and integration gates.
- [ ] Publish/merge needed leaf and workspace changes, use canonical CI image for protected native execution. Run scoped read-only staging repair qualification, execute the maintained script, activate staging schedule, and run `./scripts/check-deployment-health.sh --environment staging` to PASS. Verify idempotent second maintenance run and fresh consumer evidence.

## Review focus

Expired CRL versus expired leaf/issuer are separate outcomes. An imported successor without consumer installation/ACK is incomplete. A canceled job cannot return PASS. Scheduler setup is not proof of a successful scheduled run. Never overwrite newer registry/PVC evidence or use another environment's custody. Any live failure remaining outside CRL recovery stays visible in the final health result.

Verification before live recovery: focused maintenance/scheduler/core-permission tests passed (17.370s); CRL implementation statement coverage 75.19%. Native real PostgreSQL recovery/ACK and isolated OpenBao provider smoke passed. Native delivery merged in Video Cloud PRs #768 and #769; canonical helper source is `53508bbedb9a869cf28940b50dd75bda28828356`. Live recovery and scheduler activation remain pending.
