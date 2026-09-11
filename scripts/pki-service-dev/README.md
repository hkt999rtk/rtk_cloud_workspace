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
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase install-intermediate --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --output INTERMEDIATE_CLIENT_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase activate-intermediate --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --output INTERMEDIATE_ACTIVATION_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase configure-certissuer --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --image VIDEO_CLOUD_IMAGE_DIGEST \
  --output MQTT_ISSUER_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase prepare-host --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --image EMQX_HOST_IMAGE_DIGEST \
  --output MQTT_HOST_PREPARED_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase adopt-host --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared MQTT_HOST_PREPARED_EVIDENCE \
  --image EMQX_HOST_IMAGE_DIGEST --output MQTT_HOST_ADOPTION_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase renew-host --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --traffic MQTT_TRAFFIC_EVIDENCE \
  --output MQTT_HOST_RENEWAL_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase revoke-host --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --renewal MQTT_HOST_RENEWAL_EVIDENCE \
  --output MQTT_HOST_REVOCATION_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase publish-host-revocation --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --revocation MQTT_HOST_REVOCATION_EVIDENCE \
  --output MQTT_HOST_PUBLICATION_EVIDENCE
python3 scripts/pki-service-dev/mqtt_host.py \
  --phase verify-host-lifecycle --authority MQTT_ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --publication MQTT_HOST_PUBLICATION_EVIDENCE \
  --output MQTT_HOST_LIFECYCLE_EVIDENCE
```

If intermediate activation succeeded but a client failed before recording CRL
receipts, keep the failed evidence and run `--phase
finish-intermediate-activation --failed FAILED_ACTIVATION_EVIDENCE` with the
same Root and intermediate evidence. Recovery accepts only the already-active
reviewed issuer and existing CRLs; it creates the missing private state
directory and waits for both actual clients.

The two clients keep their existing MQTT username/password authorization while
changing transport to TLS with an exact Root pin and DNS name. Their separate
management certificates report trust installation over the existing managed
Service channel. Intermediate activation also installs signed Root/intermediate
CRLs into one retained state volume per client and waits for exact CRL receipts.
The certissuer phase grants only the generated MQTT server signer policy and
enables only the named MQTT route for the `emqx-pki` caller. No MQTT server
private key is created until `prepare-host`; that phase creates it inside the
dedicated retained host PVC and exports only its CSR. `adopt-host` switches the
dedicated broker to the managed foreground supervisor, verifies the exact served
certificate, removes the seed after a successful state import, and proves a
seed-free restart. The live broker no longer mounts the legacy static TLS Secret.
The runner refuses any environment other than the selected dev cluster.

## OpenBao TLS authority preparation

Prepare the independent `openbao_tls` Root before changing the existing OpenBao
listener. This phase is dev-only, leaves the Root `ready`, and does not touch the
StatefulSet, provider authentication, seal state, or provider CA keys:

```sh
python3 scripts/pki-service-dev/openbao_authority.py \
  --output OPENBAO_TLS_ROOT_EVIDENCE
```

Install that exact ready Root into both real provider processes without changing
the current OpenBao listener, then activate it and publish its initial CRL:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase install-root-consumers --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST --output OPENBAO_ROOT_CONSUMER_EVIDENCE
python3 scripts/pki-service-dev/openbao_host.py \
  --phase activate-root --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --output OPENBAO_ROOT_ACTIVATION_EVIDENCE
```

If client installation stops after either immutable ConfigMap or Deployment was
created, retain the failed evidence and resume the exact image and Root:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase finish-root-consumers --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --failed FAILED_ROOT_CONSUMER_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST --output OPENBAO_ROOT_RECOVERY_EVIDENCE
```

Recovery accepts only the saved failed installation phase. It verifies or creates
the two immutable public ConfigMaps, changes only a client that still references
the legacy CA source, and verifies the exact final Deployment state and receipts.

If the command reports failure after writing `root-ready.json`, keep that output
and pass it to `--reconcile`; the runner accepts only the same registered ready
Root and never creates another key or operation.

After the OpenBao TLS intermediate is active, enable its exact named server
issuance route without changing the live OpenBao listener:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase configure-certissuer \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_INTERMEDIATE_READY_EVIDENCE \
  --policy-evidence OPENBAO_TLS_INTERMEDIATE_POLICY_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST \
  --output OPENBAO_HOST_ISSUER_EVIDENCE
```

The route permits only the two reviewed OpenBao Service DNS names and the
managed `service:openbao` caller. Its ingress policy admits only OpenBao-labelled
pods from the dev secrets namespace.

Bootstrap the retained Service client and server identities after building the
dedicated `openbao-pki` image:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase bootstrap-host \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_INTERMEDIATE_READY_EVIDENCE \
  --route OPENBAO_HOST_ISSUER_EVIDENCE \
  --service SERVICE_V5_ACTIVATION_EVIDENCE \
  --openbao-image OPENBAO_PKI_IMAGE_DIGEST \
  --output OPENBAO_HOST_BOOTSTRAP_EVIDENCE
```

This phase creates the retained PVC, copies only the registry connection and
public Service Root into the secrets namespace, and temporarily uses the
existing certissuer management identity to enroll `service:openbao`. It closes
the temporary provisioner and client CA, deletes the bootstrap pod and Secret,
and accepts exactly one Service-client row and one `openbao_tls` server row.
Both generated private keys remain only in the retained PVC.

The bootstrap also installs `allow-openbao-pki-registry` in the dev platform
namespace. It permits PostgreSQL port 5432 only from OpenBao-labelled pods in
the dev secrets namespace. If bootstrap fails before either registry row is
created, retain the failed evidence and resume the same request ID and PVC:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase bootstrap-host \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_INTERMEDIATE_READY_EVIDENCE \
  --route OPENBAO_HOST_ISSUER_EVIDENCE \
  --service SERVICE_V5_ACTIVATION_EVIDENCE \
  --openbao-image OPENBAO_PKI_IMAGE_DIGEST \
  --failed FAILED_OPENBAO_HOST_BOOTSTRAP_EVIDENCE \
  --output OPENBAO_HOST_BOOTSTRAP_RECOVERY_EVIDENCE
```

Recovery verifies the failed pod, temporary Secrets and retained PVC before it
recreates the pod. It refuses recovery if an issuance row already exists.

Adopt the retained identities in the live OpenBao StatefulSet:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase adopt-host \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_INTERMEDIATE_READY_EVIDENCE \
  --service SERVICE_V5_ACTIVATION_EVIDENCE \
  --bootstrap OPENBAO_HOST_BOOTSTRAP_EVIDENCE \
  --openbao-image OPENBAO_PKI_IMAGE_DIGEST \
  --output OPENBAO_HOST_ADOPTION_EVIDENCE
```

The adoption uses an init container for the first retained-state install and a
sidecar for admission, renewal and `SIGHUP` reload. Both share a private runtime
directory and process namespace with OpenBao. The runner verifies the served
leaf, repeats a seed-free restart with the same retained state, and then deletes
the unmounted legacy `openbao-tls` Secret. Saved desired objects make the phase
safe to rerun after an interrupted rollout.

If adoption stops, rerun the same `adopt-host` command with a new output
directory. The runner accepts only its saved exact ConfigMap and StatefulSet,
reuses the registered identities and retained PVC, and never issues another leaf.

After adoption, replace the transitional v1 authority with a server-only v2.
The `service:openbao` renewal identity remains under the Service hierarchy; the
OpenBao TLS hierarchy contains only server names:

```sh
python3 scripts/pki-service-dev/openbao_authority.py \
  --phase prepare-intermediate --server-only \
  --root OPENBAO_TLS_ROOT_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --output OPENBAO_TLS_V2_READY_EVIDENCE
python3 scripts/pki-service-dev/openbao_host.py \
  --phase install-intermediate-consumers --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --output OPENBAO_TLS_V2_CONSUMER_EVIDENCE
python3 scripts/pki-service-dev/openbao_host.py \
  --phase activate-intermediate --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --output OPENBAO_TLS_V2_ACTIVATION_EVIDENCE
python3 scripts/pki-service-dev/openbao_host.py \
  --phase configure-certissuer --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --policy-evidence OPENBAO_TLS_V2_READY_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST \
  --output OPENBAO_TLS_V2_SIGNER_EVIDENCE
```

The consumer manifest overlaps Root, v1 and v2 so the currently served v1 leaf
stays available while v2 activates. Activation moves v1 to `retiring`. The v2
certissuer policy permits only `/sign/server`; it does not create or grant a
service-client role.

Rotate the live listener once onto v2 and prove the retained successor survives
an OpenBao Pod replacement:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase renew-host --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --output OPENBAO_HOST_V2_RENEWAL_EVIDENCE
```

The runner sends one `SIGHUP` to the `openbaopkihost` process, requires exactly
one new v2 registry row and a new public key, verifies the served fingerprint,
then replaces and unseals the dev Pod. The same successor and retained PVC must
return. Private keys are never copied into evidence.

Revoke the replaced v1 leaf, publish it through the v1 provider CRL, wait for
both actual clients, finalize it and remove the old certissuer signer:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase retire-host --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --renewal OPENBAO_HOST_V2_RENEWAL_EVIDENCE \
  --output OPENBAO_HOST_V1_RETIREMENT_EVIDENCE
```

If the retirement run stops waiting for CRL receipts, enable registry and CRL
verification in both actual provider clients and resume finalization:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase enable-provider-verification --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --retirement FAILED_OPENBAO_HOST_V1_RETIREMENT_EVIDENCE \
  --image VIDEO_CLOUD_IMAGE_DIGEST \
  --output OPENBAO_PROVIDER_VERIFICATION_EVIDENCE
```

Both workloads move to the same committed image and keep their exact OpenBao
origins. The phase adds the independent Root pin, the origin's DNS name, a full
public CRL manifest and a ten-second registry/CRL sweep. CRL state stays under
each workload's existing retained host-state PVC. Each workload's managed
Service identity performs CRL fetch and acknowledgment; static management keys
are forbidden.

Exercise the now-verified clients through the normal listener lifecycle before
starting failure and recovery tests:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase exercise-provider-clients --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --provider-verification OPENBAO_PROVIDER_VERIFICATION_EVIDENCE \
  --output OPENBAO_PROVIDER_OPERATION_EVIDENCE
```

The owner first renews its Service client and then its server leaf through Cert Issuer. It asks PKI Controller to
revoke and publish the replaced v2 leaf. The phase accepts exactly one successor,
requires both retained CRL receipts, verifies the denied leaf is in the CRL and
checks replay-safe finalization. It does not export private keys.

Run the scoped failure/recovery step with the successful operation evidence:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase exercise-provider-outage --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --provider-operations OPENBAO_PROVIDER_OPERATION_EVIDENCE \
  --output OPENBAO_PROVIDER_OUTAGE_EVIDENCE
```

The runner applies a temporary, zero-egress policy to Cert Issuer. This blocks
all its outbound dependencies, including PostgreSQL; it is not an isolated
OpenBao network fault. One HUP starts the Service-client renewal first. The
runner observes that client's retained request for at least 50 seconds, checks
that the installed identity is unchanged, and removes the exact policy by UID
and resource version. It lets the normal client retry the same request. An
uncertain `issuing` claim requires reconciliation; another HUP cannot repair it.

For the dev-only no-result case, replace the original Certissuer signing Pod
with the same desired deployment and wait at least five minutes. Preserve the
failed evidence. Then run:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase recover-provider-outage \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --failed FAILED_OPENBAO_PROVIDER_OUTAGE_EVIDENCE \
  --request-id EXACT_RETAINED_CLIENT_REQUEST_ID \
  --output OPENBAO_PROVIDER_RECOVERY_EVIDENCE
```

Recovery compares the original CSR against the complete public certificate
inventory. An existing unique result goes directly to the authenticated
controller reconciliation endpoint. Only an absent result with all original
signer Pods gone permits a scoped `certissuer-pki` signing attempt. The original
request/CSR remains in the registry and private retained state. A durable,
exclusive `pki/openbao-recovery-<request-id>.json` marker precedes signing;
provider CLI retries are disabled. If the marker exists but discovery finds no
result, stop and investigate rather than deleting the marker or signing again.
Recovered validity cannot exceed the original request deadline or issuer margin.
There is no runtime `abandon-pending` command.

Complete qualification after successful recovery:

```sh
python3 scripts/pki-service-dev/openbao_host.py \
  --phase verify-provider-recovery --server-only \
  --authority OPENBAO_TLS_ROOT_EVIDENCE \
  --intermediate OPENBAO_TLS_V2_READY_EVIDENCE \
  --adoption OPENBAO_HOST_ADOPTION_EVIDENCE \
  --signer OPENBAO_TLS_V2_SIGNER_EVIDENCE \
  --provider-verification OPENBAO_PROVIDER_VERIFICATION_EVIDENCE \
  --recovery OPENBAO_PROVIDER_RECOVERY_EVIDENCE \
  --output OPENBAO_POST_RECOVERY_EVIDENCE
```

This repeats actual provider renewal/revocation and both CRL receipts, then
creates fresh App and factory Device certificates. The Device must pass mTLS
and MQTT ACL/QoS1 checks. Wrong-root/name credential-forwarding, redirect,
plaintext and eviction tests use the real transport code against an isolated
local PostgreSQL/TLS fixture; configure `VIDEO_CLOUD_TEST_DSN` so they do not skip.

If final qualification fails after provider/App success, add `--failed` pointing
to that final report and select a new output directory. The phase revalidates
the qualified host/CRL receipts and skips completed App issuance. If a Device
certificate was already returned, it verifies that same retained certificate;
it does not create another production run or key. Product CRLs must be fresh
for Device mTLS/MQTT acceptance. Refresh only eligible active/retiring authorities;
revoked issuers stay disabled.

The broker's HTTPS authentication callback reuses the separately mounted
`emqx-pki` client identity and mounts its callback CA as public trust. If an older
adoption copied the callback paths from the removed static Secret, run
`--phase repair-host-callback --adoption MQTT_HOST_ADOPTION_EVIDENCE --image
VIDEO_CLOUD_IMAGE_DIGEST` with the same authority and intermediate arguments.
The repair installs the reviewed callback policy, changes the paths, restarts the
broker, waits for both actual service clients, and runs Device MQTT ACL/QoS1.

The four lifecycle phases then rotate the broker-owned key and leaf with SIGHUP,
measure closure of a held MQTT session, verify the served successor and restart
from retained state. They revoke the predecessor in the registry, publish it in
the MQTT intermediate CRL, wait for both actual client receipts, and finalize the
revocation. The last phase temporarily makes only the broker's registry lookup
unavailable, verifies that the Pod and Service fail closed without changing
issuance or acknowledgment state, restores the exact Deployment template, and
repeats authenticated Device traffic. It also rejects the served broker with a
wrong DNS name and an unrelated Root before MQTT credentials are sent.

If renewal stops after `renewal-intent.json` was written and the successor was
registered, retain that failed directory and run `--phase finish-host-renewal
--failed FAILED_RENEWAL_EVIDENCE --traffic MQTT_TRAFFIC_EVIDENCE` with the same
authority and intermediate arguments. Recovery accepts exactly one installed
successor, does not signal renewal again, and proves its retained-state restart.
