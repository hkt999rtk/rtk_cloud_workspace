# Dev managed listener egress adoption

This procedure replaces the two listeners' **outgoing** static management
credentials. It targets only canonical dev (`lke649805-ctx`) and namespace
`video-cloud-dev-video-cloud`. It does not touch staging, Device trust, database
contents, or any private key outside the workload-owned PVC.

Run it only after the successful Service retirement evidence and after building
the committed Video Cloud source with the canonical dev image builder. The image
must be a verified linux/amd64 digest under
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:...`. Every phase takes a
new private output directory. Retain both successful and failed evidence.

```sh
python3 scripts/pki-service-dev/egress.py --phase certissuer-enroll \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --image VERIFIED_DEV_IMAGE --output RUN/certissuer-enroll
python3 scripts/pki-service-dev/egress.py --phase certissuer-adopt \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --enrollment RUN/certissuer-enroll --image VERIFIED_DEV_IMAGE \
  --output RUN/certissuer-adopt
python3 scripts/pki-service-dev/egress.py --phase controller-enroll \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --certissuer RUN/certissuer-adopt --image VERIFIED_DEV_IMAGE \
  --output RUN/controller-enroll
python3 scripts/pki-service-dev/egress.py --phase controller-adopt \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --certissuer RUN/certissuer-adopt --enrollment RUN/controller-enroll \
  --image VERIFIED_DEV_IMAGE --output RUN/controller-adopt
python3 scripts/pki-service-dev/egress.py --phase verify \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --certissuer RUN/certissuer-adopt --controller RUN/controller-adopt \
  --image VERIFIED_DEV_IMAGE --output RUN/verification
```

`certissuer-enroll` first records the exact current Deployment, PVC and static
consumer identity, narrows the issuer's provisioner authorization to exactly
`^certissuer$`, and runs `/app/serviceidentity-bootstrap service:certissuer`
inside the existing certissuer Pod. The helper generates the new P-256 key and
writes `/var/lib/pki-host/identity/client.json` with mode 0600 in the existing
PVC. It reports only the subject and public certificate fingerprint. It cannot
print or export a private key. The static credential is used only for this
recorded initial request; the policy is then narrowed to `^pki-controller$` for
the next phase.

The runner maps the already-mounted certissuer-only `CERT_ISSUER_DB_DSN` into
the helper's process as `PKI_DATABASE_URL`; the DSN never leaves the Pod or
appears in evidence. Controller enrollment requires its pre-existing
`PKI_DATABASE_URL` directly and fails closed if it is absent.

If that first phase reaches the saved Deployment template but fails before a
client state file or Service-client issuance exists (for example, the selected
image omitted the helper), use a new private directory and the narrowly guarded
recovery phase:

```sh
python3 scripts/pki-service-dev/egress.py --phase resume-certissuer-enroll \
  --authority ROOT --intermediate INTERMEDIATE --retirement RETIREMENT \
  --failed FAILED_CERTISSUER_ENROLL --image VERIFIED_REPLACEMENT_IMAGE \
  --output RUN/certissuer-enroll-recovery
```

It requires the saved Deployment/PVC UID and exact accepted template, the
temporary `^certissuer$` policy, no client state file and zero issuance rows for
`service:certissuer`. It changes only the certissuer image to the verified
replacement, then performs the original single enrollment. Any state, issuance
or template drift stops recovery; it never creates a second request or deletes a
private state file.

`certissuer-adopt` switches only certissuer to the supplied image. It sets the
managed client state path and continues to use the independently pinned issuer
origin, controller URL, manifests and public management CA. It removes the
static management certificate/key environment values and the host-renewal
certificate/key environment values, replaces the Secret mount with a ConfigMap
containing only the public management CA, and removes the static certissuer CA
from controller inbound trust. It permits only the managed Service identities
needed during the staggered transition. The runner requires the client state to
survive restart and verifies that no runtime static credential path remains.

If certissuer has reached its saved dynamic template and public CA ConfigMap but
stops before legacy inbound trust is removed, use `resume-certissuer-adopt` with
the failed adoption and successful enrollment directories. It requires the exact
saved live template, immutable public ConfigMap, current managed client state and
matching enrollment evidence. It removes exactly one certificate block from the
paired listener's inbound bundle using the saved legacy consumer CA at
`dev/pki/consumers/certissuer/ca.crt`; it never uses the egress Secret's
management-server CA for this operation.

`controller-enroll` repeats the recorded initial request for
`service:pki-controller`, using only its own still-mounted static credential and
the exact `^pki-controller$` provisioner policy. `controller-adopt` performs its
equivalent dynamic switch, strips controller's static CA from certissuer inbound
trust, sets the provisioner policy to `^$`, and changes host-renewal authorization
to exactly `^service:(certissuer|pki-controller)$`. Both listeners restart, and
the changed image/settings are checked against the persisted listener renderer.
Legacy consumer Secret deletion and removal of residual legacy CA blocks from
both inbound bundles are still separate acceptance work; this runner does not
implement that cleanup.

The verification phase checks both managed client state files, matching unrevoked
registry rows, no static credential paths or mounts, disabled bootstrap policy,
managed host-renewal authorization, and the existing Device baseline checks.
Separate server/client key comparison, actual managed host renewal, rejection of
old credentials and residual trust/Secret cleanup still require recorded evidence.
A fresh signed Service
CRL and new dynamic receipts are a later, separately recorded acceptance phase;
this procedure does not claim them merely because historical receipt rows exist.

`resume-controller-adopt` uses the original failed `controller-adopt` directory
and successful controller enrollment evidence. Recovery requires exact live
Deployment/PVC ownership, the saved template (including its old image), immutable
public CA data and unchanged private-state hash **before** applying the requested
replacement image. Evidence of an attempted trust mutation blocks this recovery;
it is only for a failure before that boundary. Unrecorded image drift is not
accepted. The built-in inspector validates a single existing state snapshot and
reports its actual pending flag without creating state/lock files or exporting
private material. All Service rollout phases share the same local lock.

Do not use `--phase` again to work around an uncertain mutation. Inspect the
saved live object, state fingerprint, registry row and report first. The runner
never creates a second client state, regenerates a key, broadens a provisioner
policy, restores the legacy Secret, changes staging, resets the database, pushes
Git, opens a PR or dispatches CI.
