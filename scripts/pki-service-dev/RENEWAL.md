# Dev Account Manager operator renewal

Requires the managed Account Manager adoption and sealed bootstrap, active Service
hierarchy, and fresh Root/intermediate CRLs. The managed key stays inside the owner
container. Normal renewal at two-thirds of the actual lifetime is unchanged;
this run qualifies operator-requested early renewal through SIGHUP.

Build committed Video Cloud source with the canonical `lke-build-images` command,
`--workloads video-cloud`, `--env-root cloud_env/dev/runtime`, a unique dev tag and
the dev registry. Ensure normalized dev metadata exists at
`cloud_env/dev/runtime/env/stack.env` and includes the dev environment, stack and
region; the builder otherwise falls back to a staging label. Require the output
manifest stack to be `video-cloud-dev`. Retain source/image manifests privately
and verify the registry linux/amd64 digest. Only the Account Manager `pkimanagement` container adopts this
image; API, init-container and worker images remain unchanged. The independent
`PKI_ACCOUNT_MANAGER_OWNER_IMAGE` pin and scoped full Deployment overlay persist it.

Each command requires a new private output directory:

```sh
python3 scripts/pki-service-dev/renewal.py --phase adopt \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --image "$OWNER_IMAGE" \
  --output "$RUN/adoption"
python3 scripts/pki-service-dev/renewal.py --phase renew \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --adoption "$RUN/adoption" \
  --output "$RUN/renewal"
python3 scripts/pki-service-dev/renewal.py --phase verify \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --adoption "$RUN/adoption" \
  --renewal "$RUN/renewal" --output "$RUN/verification"
```

Adoption snapshots the current public identity, registry row, state hash, PVC,
workload/worker images and Deployment UID. It upgrades the existing owner using a
resource-version guarded patch and requires the identity to remain unchanged
before any renewal signal. A temporary Go probe executes inside the owner and
emits public fingerprints, public-key/state hashes and pending-request metadata;
private keys never leave the container. The probe is removed at phase cleanup;
pod replacement discards its ephemeral copy.

Renewal checks PID 1 is `pkimanagement`, saves signal intent, and sends one SIGHUP.
It requires exactly one new successful receipt under the authenticated managed
caller, a different public key and leaf, unchanged Root/subject, no pending request,
working human PKI operations and persistence after a bootstrap-free restart. It
rechecks Device mTLS/MQTT ACL/QoS1. The final audit checks persisted image settings,
original PVC and unchanged API/worker/other workload images.

Do not resend a signal after an uncertain result. Use `--phase resume-renew
--renewal FAILED_RENEWAL --adoption ADOPTION ...` with a new output directory. It
reconciles the original intent and saved baseline, waits for the existing pending
request to retry through the normal owner timer, and never sends another signal.
If the owner never queued or persisted a request, this phase stops without
manufacturing a successful rotation. Keep the failed report. An interrupted image
adoption must be reconciled against its saved Deployment/PVC before proceeding.

These original-baseline phases qualify this first dev early renewal; they are not
a recurring monitor. Historical single-issuance adoption checks remain historical
after renewal. Subsequent lifecycle phases must identify the actually installed
public fingerprint instead of assuming one issuance row per subject.

Retirement/revocation of the previous leaf, durable installed CRL receipts,
live active-session eviction and natural timer execution in dev remain distinct
acceptance items. Operator early renewal does not automatically revoke the old
leaf. Never use clock changes, private-state edits or restored bootstrap trust to
manufacture lifecycle evidence. No staging rollout, PR, Git push or CI dispatch
is part of this procedure.
