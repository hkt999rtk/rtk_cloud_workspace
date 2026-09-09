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
Intermediate transition, obtains both ordinary-login approvals, provisions one
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

The following phases will install additive Root/v1/v2 trust, activate v2 with
both real listener receipts, seed the factory-owned PVC, switch factoryenroll,
qualify early renewal, revoke the static bootstrap leaf and remove its unused
Secret. Keep v1 trusted while its still-valid listener leaves remain.
