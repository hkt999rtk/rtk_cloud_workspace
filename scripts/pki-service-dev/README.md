# Dev Service hierarchy rollout

This phased runner extends the completed Device baseline with an independent
Service Root and an approved OpenBao intermediate. It uses the same distinct, ordinary-login approval accounts and
private evidence helpers as `pki-dev-acceptance`. It only targets canonical dev
context `lke649805-ctx` and namespace `video-cloud-dev-video-cloud`. Staging,
legacy migration, hardware and independent human custody are excluded.

## Account Manager caller acceptance

`account_callers.py` proves actual factory admission and public App-token
authorization through the managed Account Manager listener. It uses the existing
reviewed dev Cloud/Product, the rehearsal owner, and a dedicated
`pki-service-caller-…@dev.invalid` member created with `rtk-cloud create-users`.
The owner claims the Device and receives a token; the member without Product
admission and an unassigned Device are denied. Core login is unchanged.

Use an OpenSSL-backed Python (on this workstation,
`/opt/homebrew/bin/python3`); the bundled macOS LibreSSL cannot load Ed25519.
Supply a new private output directory for every invocation. A fresh run creates
one production run, Device and claim, and issues the owner's first App certificate
only if none exists. Existing owner credentials must be supplied explicitly:

```sh
/opt/homebrew/bin/python3 scripts/pki-service-dev/account_callers.py \
  --database PRIVATE_TEST_DATA_SQLITE --email CALLER_TEST_EMAIL \
  --owner-identity RETAINED_OWNER_IDENTITY --output NEW_PRIVATE_OUTPUT
```

To reuse an already enrolled and claimed Device, add `--fixture ENROLLMENT_OUTPUT`.
This repeats the exact factory request and requires the same returned certificate;
it does not create another production run, Device or claim. The original
production JWT must still be valid. The fixture contains `enroll-request.json`,
`enrolled.json` and `production-run.json`; the owner identity directory contains
`owner-chain.pem` and `owner-key.pem`.

Add `--restart video-cloud-api`, `--restart factoryenroll`, or
`--restart account-manager` to that reuse command to replace one Pod with
UID/resourceVersion preconditions, verify unchanged identity hashes and
Deployment specifications, and repeat both caller paths. Restarts are dev-only
and sequential. No configuration rollout or private Service-key export occurs.
Reports retain selected Account Manager internal request logs and explicit
pass/fail checks. A failure requires reconciliation of saved intents/responses;
do not create replacement credentials or replay uncertain mutations blindly.
Renewal, retirement, held connections and trust-outage qualification remain
separate T7 checks.

## Authority rollout

First run Device preflight. Build the selected Video Cloud revision using
`lke-build-images --workloads video-cloud` and the dev registry. Verify the image
manifest's dev stack and immutable registry digest. Prepare independent bootstrap
consumer identities with `rtk-cloud pki-dev-prepare --environment dev --consumer
pki-controller` and the same command for `certissuer`. These are bootstrap
management identities, not governed Service leaves or installation receipts.

Each phase requires a NEW private output directory. Keep all directories, keys,
raw responses and rollback manifests private; never attach the directory to a
PR. Replace the placeholders below with those actual private paths and digest:

```sh
python3 scripts/pki-service-dev/run.py --phase prepare-root --output ROOT_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase controller --authority ROOT_EVIDENCE \
  --image VERIFIED_DEV_IMAGE_DIGEST --output CONTROLLER_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase certissuer --authority ROOT_EVIDENCE \
  --image VERIFIED_DEV_IMAGE_DIGEST --output CERTISSUER_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase activate-root --authority ROOT_EVIDENCE \
  --output ACTIVATION_EVIDENCE
```

The first phase refuses an existing Service authority, records the operation,
obtains distinct approvals, generates an encrypted offline Root key, registers
its CSR and imports the signed public certificate. Its passphrase is stored
separately under dev `pki/rehearsal-passphrases`; this is a dev offline simulation,
not independent custody. Activation without receipts must return 409.

The controller phase preserves Device's exact `video-cloud-api,pkibroker` gate,
sets Service's consumers to `certissuer,pki-controller`, and installs only the
ready Service Root plus the two selected bootstrap client CAs. The actual serving
controller sends its own receipt. Activation must still return 409 until certissuer
installs the bundle. The certissuer phase preserves existing client CAs, adds the
Service Root and uses its own bootstrap management identity to send its receipt.
Both use explicit mounted manifests, registry verification and a private group
readable management Secret. Only these two Deployments are updated. Scoped
network ingress permits the actual two listener pods to reach the controller.

All patches test resource versions and retain prior objects. Normalized desired
objects are persisted alongside prior dev PKI overlays in
`dev/pki/controller-bootstrap/rollout`; these are the desired full overlays, not
just source renderer defaults. `PKI_CONTROLLER_IMAGE` and `PKI_CERTISSUER_IMAGE`
record the selected digest; per-listener `*-service-settings.json` records the
runtime settings. `--phase verify --authority ROOT_EVIDENCE --output NEW_AUDIT`
re-renders the scoped desired Deployments using those operator image files and
saved settings, compares them with live values, checks running digests/readiness,
Root key correspondence, both receipts and current signed CRL. It saves the
rendered objects privately for a reviewed restore. The generic platform
renderer does not manage the Service overlay; a full platform provision must
explicitly reconcile these overlays rather than silently reverting their settings.
`LKE_VIDEO_CLOUD_IMAGE` for unrelated API/workers is deliberately preserved.

Only both actual receipts release Root activation. Then the final phase signs
and imports Root CRL 1. The initial Root CRL lasts one day; refresh it with the
reviewed offline ceremony before it expires. This runner does not refresh or
replace existing Root keys. Successful Root setup does not provision the online
Service intermediate, server leaves or Account Manager managed caller, and is
not completion of the trust-consumer milestone.

After the Root phase, the intermediate sequence uses a NEW private directory for
each phase. Reuse the original Root evidence path; do not create another Root.

```sh
python3 scripts/pki-service-dev/run.py --phase prepare-intermediate \
  --authority ROOT_EVIDENCE --output INTERMEDIATE_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase intermediate-controller \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE --output INT_CONTROLLER_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase intermediate-certissuer \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE --output INT_CERTISSUER_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase activate-intermediate \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE --output INT_ACTIVATION_EVIDENCE
python3 scripts/pki-service-dev/run.py --phase verify \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE --output NEW_FINAL_AUDIT
```

The reviewed intermediate policy permits exactly the dev controller and certissuer
service DNS names, plus `service:account-manager`, `service:certissuer` and
`service:pki-controller`. The first phase refuses an existing Service intermediate,
requires a fresh Root CRL, and records normal approvals before OpenBao internal key
generation and offline Root signing. Controller workload capabilities must deny
key read/export and both server/client leaf signing. Generated exact-mount policy
is added to the existing dev controller role; the original role is saved privately.
No leaf signer grant is installed by these phases.

A new immutable Root/intermediate ConfigMap is adopted by the controller first,
then certissuer. Both changes preserve image/settings and unrelated volumes;
resource-version and old-volume tests guard the rollout. Activation must fail
before receipts and with only controller's receipt. The final phase activates
with both actual receipts, imports the provider-signed CRL, rechecks custody and
runs Device mTLS/MQTT baseline. Verification checks both manifests, persisted
volumes/settings, active intermediate/receipt/CRL evidence, and both OpenBao roles'
exact identity policies and selected issuer certificates. It does not deploy
managed server/client leaves or install durable CRL consumers. Failed intermediate
phases require explicit reconciliation from their saved operation/issuer/provider
state; they have no automatic retry or key regeneration mode.

Any error leaves a failed phase report and saved prior/current observations.
Do not rerun a phase against a new output directory to work around an uncertain
mutation. Inspect the saved operation, issuer, receipts, desired objects and live
resource versions first. Existing objects are never adopted by `create`, and an
already configured listener or changed ready Root is rejected. Recover the exact
phase deliberately; do not fabricate receipts, regenerate keys, reprovision an
uncertain authority or restore an obsolete image over a concurrent change.
If activation succeeded and signing produced `root-crl/revocations.pem` but the
phase failed before import, `--phase finish-root-crl --authority ROOT_EVIDENCE
--activation FAILED_ACTIVATION_EVIDENCE --output NEW_RECOVERY_EVIDENCE` validates
the exact active Root, both receipts, signed CRL hash and approved request. It
imports only when no CRL exists, otherwise requires the current digest to match.
It never signs again or rewrites the failed report. A changed/newer CRL stops
recovery. The recovered phase then checks the existing Device mTLS/MQTT baseline.

A local lock prevents overlapping Service rollout phases. SIGINT/SIGTERM close
owned port forwards and preserve the failed report. Termination may leave a
partial rollout; private rollback data remains available.

Run local helper checks with:

```sh
python3 -m unittest discover -s scripts/pki-service-dev -v
python3 -m unittest discover -s scripts/pki-dev-acceptance -v
```

The report records only checks actually executed. Preflight is not a new complete
Device lifecycle run; live Root receipts are not Service client renewal/revocation
qualification. Retain all failed phase reports separately from successful recovery.

For the following managed controller/certissuer server rollout, see [HOSTS.md](HOSTS.md).

For managed Account Manager controller credentials after host adoption, follow [MANAGEMENT.md](MANAGEMENT.md).

For the isolated Device API controller credential reconciliation, renewal,
predecessor retirement and Device/MQTT canary, follow
[API_CONTROLLER_LIFECYCLE.md](API_CONTROLLER_LIFECYCLE.md).

Service CRL maintenance and publication-response-loss recovery are documented in
[CRL.md](CRL.md). Run before the earliest applicable signed CRL expires; post-expiry
management recovery remains unqualified.

Managed Account Manager early renewal is documented in [RENEWAL.md](RENEWAL.md).
It uses operator SIGHUP and the existing guarded renewal path; no clock or private
state edits are used. Follow [RETIREMENT.md](RETIREMENT.md) to retire the replaced
leaf, publish its signed CRL and require actual certissuer/controller receipts.
Active-session revocation and managed consumer transport adoption remain open.

## PKI broker identity authority

`pkibroker_authority.py` advances the Service intermediate from V3 to V4 for
the managed PKI broker. V4 keeps the V3 DNS policy unchanged and adds exactly
`service:pkibroker` to the Service client policy. It first prepares and signs a
ready intermediate, then installs the immutable trust bundle in `pki-controller`
and `certissuer`, and finally activates only after both real bundle receipts.
Activation publishes the V4 CRL, moves V3 to retiring, and requires both CRL
receipts. Every phase writes private evidence and uses the shared Service
rollout lock.

Use a separate private output directory for every phase. `PREPARED` must be
the successful preparation evidence; do not rerun preparation with a new
directory after an uncertain mutation.

```sh
python3 scripts/pki-service-dev/pkibroker_authority.py \
  --phase prepare-intermediate-v4 --authority SERVICE_ROOT_EVIDENCE \
  --output PREPARED
python3 scripts/pki-service-dev/pkibroker_authority.py \
  --phase controller --authority SERVICE_ROOT_EVIDENCE --prepared PREPARED \
  --output CONTROLLER_EVIDENCE
python3 scripts/pki-service-dev/pkibroker_authority.py \
  --phase certissuer --authority SERVICE_ROOT_EVIDENCE --prepared PREPARED \
  --output CERTISSUER_EVIDENCE
python3 scripts/pki-service-dev/pkibroker_authority.py \
  --phase activate --authority SERVICE_ROOT_EVIDENCE --prepared PREPARED \
  --output ACTIVATION_EVIDENCE
```

The runner is dev-only. It does not provision the broker leaf, remove the
legacy broker management key, or modify staging.

## MQTT server authority and actual clients

`mqtt_host.py` continues from the prepared MQTT Root. It installs the exact
Root in the real `video-cloud-api` and `video-cloud-logingester` connection
owners, configures those two identities as the MQTT activation gate, and then
activates the Root with a signed initial CRL. The next phase prepares one
OpenBao-backed MQTT intermediate limited to
`mqtt-pki.video-cloud-dev-video-cloud.svc`.

Use a new private evidence directory for each command and the pinned dev image
digest produced from the reviewed source:

```sh
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase install-root-consumers --authority MQTT_ROOT_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST --output ROOT_CONSUMER_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase activate-root --authority MQTT_ROOT_EVIDENCE \
  --output ROOT_ACTIVATION_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase prepare-intermediate --authority MQTT_ROOT_EVIDENCE \
  --output INTERMEDIATE_EVIDENCE
```

The two clients keep their existing MQTT username/password authorization while
changing transport to TLS with an exact Root pin and DNS name. Their separate
management certificates report trust installation over the existing managed
Service channel. No MQTT server private key is created by these phases, and the
runner refuses any environment other than the selected dev cluster.
