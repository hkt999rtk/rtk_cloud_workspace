# Dev factory Service identity adoption

This procedure replaces factoryenroll's static certissuer client key with the
application-owned `service:factory-enroll` identity. It targets only canonical
dev context `lke649805-ctx`. Staging is excluded.

The active Service Intermediate v1 does not authorize that subject. Never edit
its OpenBao role to add the name. First prepare an independently approved v2
under the existing Service Root:

```sh
python3 scripts/pki-service-dev/factory_adoption.py \
  --authority SERVICE_ROOT_EVIDENCE \
  --output NEW_PRIVATE_V2_PREPARATION_EVIDENCE
```

The runner requires the exact active v1 policy, refuses another unfinished
Intermediate transition, obtains an ordinary-login approval from a `pki_admin`
distinct from the requester, provisions one
new internal OpenBao key, verifies that the controller cannot read or sign with
it, and offline-signs its CSR with the existing dev-simulation Root key. The v2
policy retains the three current client subjects, adds only
`service:factory-enroll`, and keeps the two current listener DNS names.

The resulting Intermediate remains `ready`. Activation is deliberately tested
to return 409 because neither listener has installed or acknowledged it. The
runner does not change a Deployment, listener trust bundle, signer role, factory
key, Secret or PVC. Continue only from the successful private preparation
evidence. Never rerun into a new directory after an uncertain failure: inspect
the saved operation, issuer, provider mount and controller role first.

If the first run stopped after the independent admin approval but before saving
provider policy or provisioning the key, resume that exact evidence directory:

```sh
python3 scripts/pki-service-dev/factory_adoption.py \
  --authority SERVICE_ROOT_EVIDENCE \
  --output EXISTING_FAILED_V2_PREPARATION_EVIDENCE --resume
```

Recovery requires the exact saved request, approved operation, unchanged active
v1 and absent provider mount. It cannot create or approve another operation.

The following phases will install additive Root/v1/v2 trust, activate v2 with
both real listener receipts, seed the factory-owned PVC, switch factoryenroll,
qualify early renewal, revoke the static bootstrap leaf and remove its unused
Secret. Keep v1 trusted while its still-valid listener leaves remain.

Install the additive bundle one listener at a time, then activate in a separate
phase. Each command requires a new private output directory:

```sh
python3 scripts/pki-service-dev/factory_adoption.py --phase controller \
  --authority SERVICE_ROOT_EVIDENCE --prepared V2_PREPARATION_EVIDENCE \
  --output NEW_CONTROLLER_EVIDENCE
python3 scripts/pki-service-dev/factory_adoption.py --phase certissuer \
  --authority SERVICE_ROOT_EVIDENCE --prepared V2_PREPARATION_EVIDENCE \
  --output NEW_CERTISSUER_EVIDENCE
python3 scripts/pki-service-dev/factory_adoption.py --phase activate \
  --authority SERVICE_ROOT_EVIDENCE --prepared V2_PREPARATION_EVIDENCE \
  --output NEW_ACTIVATION_EVIDENCE
```

The controller phase creates one immutable manifest containing Root, v1 and v2,
then requires controller's real receipt while certissuer remains absent and v1
remains active. The certissuer phase installs the same object, requires both
receipts and adds only the generated v2 server and Service-client signer policies
to certissuer's existing OpenBao role. It verifies that the operation remains
`ready`; it never probes activation after both receipts exist. The activation phase rechecks exact
workload mounts, receipts, role profiles and provider capabilities, atomically
makes v2 active and v1 retiring, immediately imports v2's signed CRL, waits for
both CRL receipts, restarts both listeners and runs the Device baseline. After
CRL import it adds v2 to the separate Root/v1 server-CRL manifest before those
restarts; bundle membership alone does not configure CRL consumption.

If activation returned success but the caller lost the response or later work
failed, never send activation again. After the exact Root/v1/v2 CRL manifest and
both receipts have been restored, qualify that state with:

```sh
python3 scripts/pki-service-dev/factory_adoption.py --phase recover-activation \
  --authority SERVICE_ROOT_EVIDENCE --prepared V2_PREPARATION_EVIDENCE \
  --output NEW_ACTIVATION_RECOVERY_EVIDENCE
```

This phase requires v1 `retiring`, v2 `active`, exact certificate fingerprints,
the complete CRL manifest, both bundle/CRL receipts and signer boundaries. It
restarts both listeners, runs the Device baseline and never calls activation.

The dev v2 transition completed on 2026-09-09. Successful private evidence is in
`service-factory-adoption-20260909/{v2-prepare,controller,activation-recovery-3,activation-verification-2}`.
The active v2 is `2b98cbae-b116-4064-ab36-060951062d07`; v1 is retained as
`retiring` because its listener leaves are still valid. Continue with the factory
PVC/bootstrap phase. Do not rerun authority preparation or activation.

The factory runtime uses its one application-owned dynamic identity for both
certissuer requests and CRL receipt acknowledgements. Do not configure
`FACTORY_ENROLL_CERT_ISSUER_MANAGEMENT_CERT` or
`FACTORY_ENROLL_CERT_ISSUER_MANAGEMENT_KEY` after adoption. The application
validates this transport with the managed-identity rules before opening its
private state, so a second static management key is rejected at startup.

Seed the retained factory-owned PVC with the verified dev image. This phase
temporarily permits only the existing `factoryenroll` bootstrap certificate,
runs one non-root Pod with no ServiceAccount token, requires exactly one v2
registry issuance and closes the provisioner policy back to `^$` even when the
seed attempt fails:

```sh
python3 scripts/pki-service-dev/factory_identity.py --phase seed \
  --authority SERVICE_ROOT_EVIDENCE --image VERIFIED_DEV_IMAGE_DIGEST \
  --output NEW_PRIVATE_FACTORY_SEED_EVIDENCE
```

The completed seed Pod remains present so the adoption phase can compare its UID
and delete that exact bootstrap owner before mounting the PVC in the Deployment.
It reuses the Deployment's registry pull references. Private state is created at
`/state/identity/client.json` (0600) inside an owner-only directory on the retained PVC.
For a failed seed that never ran, `--failed FAILED_SEED_EVIDENCE` permits reuse
only after recorded cleanup and exact PVC/workload identity checks.

Adopt that successful seed using the same verified image:

```sh
python3 scripts/pki-service-dev/factory_identity.py --phase adopt \
  --authority SERVICE_ROOT_EVIDENCE --image VERIFIED_DEV_IMAGE_DIGEST \
  --enrollment SUCCESSFUL_FACTORY_SEED_EVIDENCE \
  --output NEW_PRIVATE_FACTORY_ADOPTION_EVIDENCE
```

The runner adds controller consumer `factory-enroll` and network workload label
`factoryenroll`, mounts public CA/CRL data and the private PVC, and removes the
static client key mount. It uses a single replica with Recreate, closes the old
factory caller policy, and verifies restart, exact receipts for all three Service
CRLs, a new factory enrollment, Device mTLS and MQTT QoS1. Renewal and old-leaf
retirement are separate exit criteria; adoption alone does not close T6.

CRL files use `/state/identity/crl-ISSUER_ID.json`; their parent directory must
already exist before the CRL consumer opens its lock files. A failed initial
factory rollout can use `--failed FAILED_ADOPTION_EVIDENCE` after reconciling the
manifest. Recovery requires the original seed, exact Deployment/PVC identity,
expected managed template and controller policy. It never seeds another key or
repeats authority activation. The canary reopens its port-forward after restart.
