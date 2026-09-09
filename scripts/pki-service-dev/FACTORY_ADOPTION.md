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
to certissuer's existing OpenBao role. The activation phase rechecks exact
workload mounts, receipts, role profiles and provider capabilities, atomically
makes v2 active and v1 retiring, immediately imports v2's signed CRL, waits for
both CRL receipts, restarts both listeners and runs the Device baseline.
