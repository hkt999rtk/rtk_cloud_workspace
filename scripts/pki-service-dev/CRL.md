# Dev Service CRL refresh

This flow refreshes the existing Service Root/intermediate before expiry. It
requires the currently working human-authenticated management plane, existing
managed hosts and Account Manager, and fresh Device baseline CRLs. It does not
qualify recovery after the management plane loses trust. Keep that recovery path,
automatic maintenance and installed-consumer CRL receipts open in the plan.

Use the existing encrypted offline Root key and separate passphrase reference.
Only public CRLs go to the normal API. Intermediate rotation uses the controller
workload's Kubernetes/OpenBao identity and existing scoped permissions. It does
not change policies, generate a key, restart workloads or modify staging.

Each phase creates a new private evidence directory. Substitute existing reviewed
paths for `ROOT`, `INTERMEDIATE` and `HOSTS`; `HOSTS` is the successful managed-host
preparation (including seed issuance), not the Account Manager preparation.

```sh
python3 scripts/pki-service-dev/crl.py --phase prepare-root \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --output "$RUN/root"
python3 scripts/pki-service-dev/crl.py --phase lose-response \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --source "$RUN/root" --output "$RUN/response-loss"
```

The second command **must fail intentionally** after a single loopback proxy
forwards the import and discards its response. Inspect the saved response-loss
report: exactly one forward, upstream 200 and a real client EOF are required.
Do not repeat signing or the fault phase. Retain the failed report and reconcile:

```sh
python3 scripts/pki-service-dev/crl.py --phase publish \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --source "$RUN/root" --output "$RUN/root-publication"
python3 scripts/pki-service-dev/crl.py --phase prepare-intermediate \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --output "$RUN/intermediate"
python3 scripts/pki-service-dev/crl.py --phase publish \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --source "$RUN/intermediate" --output "$RUN/intermediate-publication"
python3 scripts/pki-service-dev/crl.py --phase verify \
  --authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
  --source "$RUN/root" --root-publication "$RUN/root-publication" \
  --intermediate-publication "$RUN/intermediate-publication" --output "$RUN/verification"
```

Publication verifies the issuer and signed artifact, rejects an unrelated current
CRL, imports only if the saved prior CRL remains current, then replays the same
artifact. Require one CRL row and one matching audit event. The prior signed CRL
must receive HTTP 409 and leave the refreshed record unchanged. No database writes
or trust bypass are used. The routine Root manifest preserves all entries and
extends validity to 72 hours, within the importer's seven-day bound.

If intermediate rotation's response/read/save is uncertain, do not rerun
`prepare-intermediate`. Use `--phase resume-intermediate --source FAILED_PREPARE`
with a new output directory. It requires the saved rotation intent, same issuer
and registry baseline, and a changed provider CRL; it reads and validates that
public CRL without rotating. Publish from the successful recovery directory. An
unchanged provider CRL or unrelated registry change requires manual reconciliation.
No automatic rotation retry is allowed.

Verification compares managed state hashes, registered Account Manager issuance,
actual server fingerprints and workload/worker images, then tests human PKI
operations and Device mTLS/MQTT ACL/QoS1. It records every active issuer's latest
CRL deadline and the earliest deadline. These commands create no scheduler, Git
push, PR or CI dispatch. Refresh must be repeated before the earliest applicable
expiry; this rehearsal is not an automatic Root signing service.
