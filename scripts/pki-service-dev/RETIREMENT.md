# Replaced dev Service credential retirement

Prerequisites: the successful managed Account Manager early-renewal evidence,
original adoption evidence, active Service hierarchy, prepared managed listener
identities and fresh signed CRLs. The target is the original replaced leaf; the
currently installed replacement must have no pending request and remain admitted.
No managed private key is exported. This run cannot prove active-session eviction
for the old key that renewal already discarded.

Build the committed Service-listener changes with the canonical dev builder and
verified `video-cloud-dev` manifest/linux-amd64 registry digest. Only certissuer
and controller adopt that image. Account Manager API/owner, workers, Device API,
broker and factory images stay unchanged. No staging operation, branch push, PR
or CI dispatch is part of this procedure.

Each phase creates a new private evidence directory. Every invocation takes:

```sh
--authority "$ROOT" --intermediate "$INTERMEDIATE" --prepared "$HOSTS" \
--adoption "$MANAGEMENT_ADOPTION" --renewal "$SUCCESSFUL_RENEWAL"
```

Run `python3 scripts/pki-service-dev/retirement.py` with those arguments and:

1. `--phase retire --output "$RUN/retirement"`: verify installed successor;
   revoke only the recorded old fingerprint through the human PKI API; repeat the
   identical request to check idempotence; publish its provider CRL; verify signed
   coverage and preserved prior entries. Finalization must return 409 before and
   after publication while listener receipts are missing. Device traffic and
   managed human PKI operations must remain healthy.
2. `--phase certissuer --retirement "$RUN/retirement" --image "$IMAGE"
   --output "$RUN/certissuer"`: install the public Root/intermediate manifest and
   enable the real certissuer consumer. Require new exact CRL receipts and keep
   finalization at 409 because controller is missing. Restart certissuer and
   verify persistent CRLs, same private identity and working managed traffic.
3. `--phase controller --retirement "$RUN/retirement" --image "$IMAGE"
   --certissuer "$RUN/certissuer" --output "$RUN/controller"`: configure the
   controller's real consumer. Its registry-backed preparation must permit a
   startup that cannot call its own listener yet. Require both consumers' receipts
   before finalization succeeds, then check restart/persistence and Device traffic.
4. `--phase verify --retirement "$RUN/retirement" --image "$IMAGE"
   --output "$RUN/verification"`: verify exact signed coverage, finalization,
   retained CRL files, actual listener identities and unchanged other owners.

The two listeners use distinct retained identity PVCs. Public CRL state lives
under `/var/lib/pki-host/identity/crls/`; private identity files are not edited.
The shared public ConfigMap is `pki-service-client-crls`. Scoped full Deployment
manifests and `PKI_CERTISSUER_IMAGE`/`PKI_CONTROLLER_IMAGE` pins persist adoption.
CRL state is private to the workload (0700 or inherited-setgid 2700 directory,
0600 files). Initial receipts must be newer than the newly configured process.
Acknowledgments are idempotent immutable rows: restart verification uses retained
state, the existing exact receipt, and real admitted traffic rather than claiming
a new acknowledgment timestamp.

Consumer preparation reads through the existing registry connection and keeps
signature, pin, freshness, monotonicity and installer checks. Acknowledgments still
travel through the actual configured mTLS consumer transport after the listener
is serving and its sweep succeeds. Never insert/delete receipt rows to pass a
check. These consumer transport credentials remain separately trusted bootstrap
identities; migrating those callers to managed Service credentials remains open.

On failure, retain the report and reconcile its saved revocation request, provider
CRL and scoped Deployment against current state. Do not repeat signing, blindly
rotate provider CRLs, resend a renewal signal or replay a resource creation.
If a listener rollout reached its saved API-accepted Deployment and failed before
the verification restart, rerun that listener phase with `--resume FAILED_PHASE`
and a new output directory. This checks the exact accepted/live template (including
Kubernetes defaults), unchanged
identity, current published CRL and expected workload images before continuing
receipt checks and the restart. It does not recreate resources or republish the
CRL. A changed template or identity stops recovery. After the verification
restart has changed the template, reconcile that later checkpoint separately.
A revocation already committed remains effective even if CRL publication or
receipt installation fails. The installed replacement must remain unchanged
through recovery. Never restore the old key/bootstrap trust to recover this run.
