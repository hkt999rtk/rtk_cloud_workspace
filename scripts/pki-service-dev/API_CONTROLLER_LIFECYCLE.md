# Isolated Device API controller identity lifecycle

This dev-only runner qualifies the managed controller identity used by
`video-cloud-api-pki`. It reads only public identity metadata from the retained
state file; no private key or Secret contents leave the Pod.

Run each phase with a new private output directory. Use the existing active
Service Root evidence for `ROOT`:

```sh
python3 scripts/pki-service-dev/api_controller_lifecycle.py --phase reconcile \
  --authority "$ROOT" --output "$RUN/reconcile"
python3 scripts/pki-service-dev/api_controller_lifecycle.py --phase lifecycle \
  --authority "$ROOT" --reconcile "$RUN/reconcile" --output "$RUN/lifecycle"
```

`reconcile` can retire only active registry leaves for `service:video-cloud-api`
that do not match the retained API state. It records each idempotent revocation,
published CRL and actual receipt from certissuer, factory-enroll and
pki-controller. The isolated API is a Service client whose controller admission
checks revocation; it does not install a Service CRL. The runner stops if the
state lacks exactly one active admission.

If a reconciliation committed its revocations but failed before finalization,
do not repeat it. Recover only the saved run with a new output directory:

```sh
python3 scripts/pki-service-dev/api_controller_lifecycle.py --phase recover-reconcile \
  --authority "$ROOT" --reconcile "$RUN/failed-reconcile" --output "$RUN/reconcile-recovery"
```

Recovery accepts only the saved revoked leaves, the installed identity, and the
current published CRL with real receipts; it creates no key or issuance.

`lifecycle` sends one SIGHUP after persisting its intent. It requires one new
Service issuance with a new public key and leaf, then keeps a held connection
from the old leaf until its explicit revocation closes it. The old identity must
fail a fresh connection, while the successor remains admitted. The runner then
restarts the isolated API without seed/static controller credentials and reruns
the Device mTLS and MQTT authorization canary. Do not rerun a failed lifecycle
phase with another output directory: reconcile its saved intent and live state
first.
