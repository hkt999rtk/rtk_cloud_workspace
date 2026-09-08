# Milestone 1 completion audit: fresh dev PKI

**Status: complete — 100%, zero remaining items in the authorized dev milestone.**
The audit follows the five requirements in the
[accepted dev scope](production-pki-legacy-rollout.md). The user explicitly removed
legacy-data migration from this dev exercise, authorized temporary accounts and
devices, and kept MFA disabled. Legacy fleet migration, staging, independent
custody, other trust consumers, recovery/SDK and hardware qualification remain
outside this completed dev milestone.

## Requirements and authoritative evidence

| Requirement | Evidence inspected | Result |
| --- | --- | --- |
| 1. Deploy controller/Account Manager with management mTLS and RS256 login; use distinct temporary approval accounts | Final-run preflight verified six Ready, digest-pinned deployments and running images. Ordinary RS256 logins with MFA false successfully invoked the real PKI APIs. The separate foundation audit read active Root/Brand operations and their database approvals: requester, administrator and custodian are distinct, and both approval digests match the operation. Actual API/broker management receipts are present. | Complete |
| 2. Create fresh Cloud/Product and governed Root/Brand/Product hierarchy; verify separate key custody and actual trust installation | Initial Cloud/Root/Brand creation is retained foundation evidence and was independently revalidated. Encrypted offline Root/Brand keys match the reviewed active certificates and remain mode 0600. The final run created a new Product profile and issuer, provisioned exactly one internal OpenBao key, checked actual controller key-read/export/signing denials, signed/imported its CSR, and proved API-only activation fails until the broker installs and acknowledges the exact bundle. | Complete |
| 3. Enroll a fresh device with its own key; prove direct mTLS and certificate renewal | The final run created a one-device production run, generated a P-256 key locally, enrolled through factory enrollment, checked key/certificate and Product bindings, and authenticated with direct mTLS. Renewal used another local key; replay returned the identical result, predecessor ACK returned 403, and successor ACK returned 204 idempotently. | Complete |
| 4. Reject revoked credentials and close active sessions, including MQTT; exercise restart and interrupted renewal | Actual MQTT ACL/QoS1 and exact tenant-topic delivery passed. Replacement closed the predecessor before lease expiry while preserving its successor. The controlled worker fault closed a valid session while the separate API remained usable. API-only CRL receipt could not complete revocation; recovery supplied the missing broker receipt. Explicit revoked-client TLS alerts, unexpired-token MQTT rejection, terminal state/PVC retention and valid baseline traffic passed across restart. | Complete |
| 5. Retain repeatable dev setup and explicit pass/fail evidence | Committed runner/probe/README/tests in `4ee0c2f1756bae845c793c1b29ef6cb1c488d195`. A new `--run` invocation completed all 12 named checks without resumption; recorded tool-source hashes match the committed files. Failed development reports remain separate. Independent post-run inspection confirmed terminal operations, retained keys/audits, scoped cleanup, private state permissions and persisted/live manifests. | Complete |

The repeated runner deliberately uses the existing reviewed Root/Brand and dev
services as its baseline. It creates a new Product and device on every run.
Initial Cloud/Root/Brand creation was performed earlier in this same dev exercise;
it is not mislabeled as newly executed by the repeated runner. Existing bootstrap
commands, scoped manifests and foundation evidence are retained separately.

## Final uninterrupted run

Command executed from the isolated worktree:

```sh
python3 scripts/pki-dev-acceptance/run.py --run \
  --output /Users/kevinhuang/.config/rtk_cloud/dev/pki/acceptance-run-20260908-2
```

- **12/12 named checks passed**, with no resume checkpoint or failed phase.
- Product issuer: `22fd739c-e41f-4468-8ca7-cefff7836a7e`.
- Device: `pki-dev-787cbb36ea5a4a98af10dba42e14de36`.
- Predecessor disconnected **8.643133 seconds** after ACK, at connection age
  **20.763719 seconds**. Successor traffic remained valid for the required
  additional 12 seconds; old API authentication and MQTT reconnect were denied.
- Worker fault disconnected a valid baseline session after **9.214932 seconds**,
  at connection age **19.898092 seconds**. The independent API remained usable.
- Revocation operation `7f07effd-652f-4b3c-85e8-381b3475ed6b` is **completed**.
  Completion was denied while the broker lacked the new CRL receipt, then allowed
  after exact Root-state restoration and both actual receipts.
- Brand CRL **5**:
  `cfc2cf2b41d4b1520cfd88829fdaff3c35016e1945ee20bb44efc87f233231b4`.
  Prior revocations were preserved. Both `pkibroker` and `video-cloud-api`
  acknowledged that exact current digest.
- The first post-rollout positive MQTT readiness check needed four attempts;
  subsequent rollout checks and fresh-device readiness needed one each. These
  bounded startup retries are recorded; denial/cutoff tests were not retried.

## Post-run state and retained artifacts

Independent inspection confirmed both test-run Product issuers are revoked and
both revocation operations completed. Each provider mount still holds its one
internal key, and each terminal-state file still matches the saved evidence.
Controller and signer roles grant only the baseline v4 Product policy; the test
policy objects are absent. Test Product references were removed from active
manifests. Baseline v4 direct mTLS and MQTT remain healthy.

The same trust PVC `bfb15b95-9c66-4bb3-93ed-e0affec06192` remains. All **seven**
worker JSON state files are mode **0600**, owned by UID **10001**. Root state
matches the exact pre-fault backup; no fault remains. Scoped API/broker manifests
match live configuration, and all six checked service deployments remain Ready
with their original images.

Protected evidence under the canonical `~/.config/rtk_cloud/dev/pki` directory:

- `acceptance-run-20260908-2/report.json` and its request, operation, certificate,
  CRL, policy and terminal-state artifacts.
- `acceptance-foundation-audit-20260908.json`: active Root/Brand, digest-bound
  distinct approvals, real consumer receipts and encrypted-key permissions.
- `acceptance-completion-audit-20260908.json`: independently inspected final
  runtime/registry/provider state and tool-source correspondence.
- `acceptance-run-20260908-1`: reconciled tool-development run and explicit
  preserved failure reports; it is separate from the uninterrupted final run.
- `fresh-rehearsal` and `controller-bootstrap/rollout`: original foundation
  evidence and the preserved, scoped desired manifests.

Credential/key files remain private and untracked. No Git push, PR, remote CI,
staging change or database reset was performed for this final work package.
Local verification passed: 11 Python tests, Python static checks, Go probe race
tests including actual TLS 1.3 certificate rejection, `go vet`, and diff checks.

## Remaining overall plan: four milestones

1. Other trust consumers and live sessions.
2. Backup/recovery and SDK integration.
3. Provider/hardware compatibility.
4. Staging/custody/recovery qualification — deferred; legacy migration also remains
   deferred to environments that need it.

The next priority is the second original reporting area, other trust consumers
and live sessions. Optional future MFA applies only to human login, never devices.
