# Fresh dev PKI acceptance

This runner exercises a new disposable Product CA and device against the existing
isolated `video-cloud-dev` PKI stack. It uses the reviewed Root/Brand and three
temporary human accounts from the [fresh dev rehearsal](../../docs/design/production-pki-fresh-dev-rehearsal.md).
It does not recreate the initial Cloud, Root, Brand, accounts or deployments.
Those setup results remain separate evidence. It does not qualify legacy
migration, staging, independent human custody, hardware or SDK integration.

The runner uses Python's standard library and builds the checked-out Go
`pki-dev-probe` and Video Cloud `pkiceremony` commands. No `/private/tmp` scripts,
third-party Python packages, Git push, PR, or CI job are required.

## Prerequisites

- Python 3.9+, Go matching the checked-out modules, kubectl and OpenSSL 3 at
  `/opt/homebrew/opt/openssl@3/bin/openssl`.
- Canonical `<config-root>/dev/kube/kubeconfig.yaml`, default context
  `lke649805-ctx`. `--config-root` is the parent of `dev`; it is never a staging path.
- Existing Ready, digest-pinned `pki-controller`, `video-cloud-api-pki`,
  `mqtt-pki`, `certissuer`, `factoryenroll` and Account Manager deployments.
  The controller must require both `video-cloud-api` and `pkibroker` receipts.
- The environment-local `pki/fresh-rehearsal` foundation contains `accounts.json`,
  `cloud.json`, `brand-active.json`, `device-root-active.json`, encrypted offline
  keys in `{brand,device-root}-offline-simulation/ca-key.encrypted.pem`, and the
  healthy baseline `device-2/{enroll-request.json,v4-key.pem,v4-chain.pem}`.
  Passphrases live in `pki/rehearsal-passphrases/{brand,device-root}`. These are
  protected dev fixture files, never repository content.
- Current Root and Brand CRLs have at least one hour remaining. A stale baseline
  fails preflight; renew it through the reviewed ceremony instead of disabling
  certificate or CRL validation.
- Dev-only OpenBao and Kubernetes access is already configured. The runner uses
  the existing OpenBao root token privately to add/remove the run's exact Product
  policies. It verifies actual controller role denial of key reads, export, and
  legacy signing. Product private keys remain inside OpenBao.

## Commands

Use a new output directory for every invocation. Directories and generated keys,
credentials and raw responses are private. Do not attach the entire directory to
a PR or paste its contents into a conversation.

```sh
python3 scripts/pki-dev-acceptance/run.py \
  --output "$HOME/.config/rtk_cloud/dev/pki/acceptance-preflight-$(date +%Y%m%d-%H%M%S)"

python3 scripts/pki-dev-acceptance/run.py --run \
  --output "$HOME/.config/rtk_cloud/dev/pki/acceptance-run-$(date +%Y%m%d-%H%M%S)"
```

Without `--run`, the runner performs preflight, builds local probes and opens/closes
its own loopback port forwards. `preflight_passed` does not mean lifecycle
acceptance passed. With `--run`, it executes these checks in order:

1. Validate live baseline, ordinary RS256 logins with MFA disabled, distinct
   identities, Root/Brand key correspondence, images and required consumers.
2. Create a unique Product profile, obtain distinct approvals, provision exactly
   one internal provider key, and sign/import its CSR with the offline Brand key.
3. Prove API receipt alone cannot activate it, install the broker bundle, then
   activate only after both actual runtime receipts. Import its Product CRL.
4. Create a one-device production run, generate its key locally, enroll through
   factory enrollment, and verify direct mTLS plus MQTT ACL/QoS 1 round trips. The delivered topic must
   match the reviewed `_bc/<cloud>/…` broker rewrite exactly; payload equality
   alone is insufficient. Positive startup checks may retry CONNACK 5 for up to
   30 seconds while Service/callback routing settles and record the attempt count.
   Revocation/replacement denial and cutoff checks do not use that retry.
5. Renew onto a new local key, replay the same request as if its response was
   lost, reject predecessor ACK, and acknowledge with the successor. Prove the
   predecessor session closes within 20 seconds and before lease expiry while
   the successor remains connected. Verify rejection and successor use on restart.
6. Approve revocation of this run's Product. Inject a deliberately invalid broker
   Root state under the existing lock and exact-byte precondition. Prove a healthy
   baseline MQTT session closes before lease expiry while its API remains usable.
7. Execute revocation and sign the next Brand CRL, preserving existing signed
   entries. Require completion to remain blocked while the broker lacks the new
   receipt. Restore the exact Root state in `finally` after checking the live Root
   policy has not changed, then require both receipts and completed revocation.
8. Verify terminal issuer state, unexpired revoked-token denial, healthy baseline
   traffic, retained PVC and private state permissions after a broker restart.
9. Remove only this run's provider policies and manifest entries. Retain its
   revoked Product profile, internal provider key and terminal audit files.

The run briefly interrupts the isolated dev API/broker during scoped Recreate
rollouts and deliberately causes the isolated broker to fail closed. It does not
modify the original dev API/EMQX or staging. K8s updates test resource versions;
changed manifests are persisted in `pki/controller-bootstrap/rollout`. OpenBao
roles are read/compared before mutation and verified afterward; OpenBao role writes
have no CAS primitive, so do not run another role writer concurrently. A local
file lock prevents overlapping copies of this runner.

## Results and interrupted runs

`report.json` records each named check as `not_run` or `passed`, the active phase,
source commit, live desired images, identities and measured cutoff times. Failure
leaves the top-level status `failed` and preserves completed checks; it never
turns existing evidence files into a new pass. Console output contains sanitized
phase/check status only. A complete successful run requires all named checks to
pass, including cleanup. Preflight results alone cannot close the milestone.

On any failure, inspect the private report and the corresponding durable request,
operation and result files before proceeding. Do not automatically rerun signing,
provisioning, activation or revocation after an ambiguous response. Query the
recorded operation/issuer through the normal authenticated PKI API and reconcile
its actual status first. The narrowly scoped `--run --resume provision --output <failed-directory>` path
is available only when the same operation still has the exact recorded
`provisioning` issuer/CSR and signing has never started. It verifies the live
operation, Product and parent bindings and preserves the previous failed report;
it does not replay key generation or approval. `--resume ready` additionally supports an exactly matching signed/imported issuer
that is still ready. `--resume enrolled` supports an exactly matching active issuer
and enrolled device before any renewal request exists. `--resume revocation` requires the original operation still approved, no CRL
signing attempt and exact restoration of the prior Root-state backup. `--resume recovery` requires the exact imported Brand CRL, revoked issuer and
restored Root state; it never re-executes revocation or signing. `--resume`
accepts one checkpoint and requires `--run`. Later or ambiguous lifecycle states require manual
reconciliation. Completed checks remain attributed to their actual execution,
and each previous failure report is retained. A new output directory starts a *different* disposable
Product; it does not resume a failed Product or clean up its policy.

SIGINT/SIGTERM unwind the fault restoration and close owned child processes.
SIGKILL, workstation loss, a changed Root policy or unavailable control plane can
prevent restoration. If `fault_restored` is not passed after a fault attempt,
compare the current worker file and Root policy to `root-state-before-fault.json`.
Restore only after exact-byte/current-policy checks under the existing state lock;
do not overwrite a concurrent legitimate policy update. Preserve failed reports
and provider audit/key state. The file is
`/run/pki-state/device/root-policy.json` in the isolated `mqtt-pki` worker.

Local verification:

```sh
python3 -m unittest discover -s scripts/pki-dev-acceptance -v
(cd scripts/go && GOWORK=off go test -race ./pki-dev-probe)
```
