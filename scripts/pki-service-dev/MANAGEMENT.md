# Dev Account Manager credential adoption

Prerequisite: the Service Root/intermediate and both managed server hosts are
active and verified. This runbook uses the existing implementation, dev namespace
and retained host state. Read the active design's Account Manager adoption contract.
Every phase requires a **new private output directory**. A failed phase is not
safe to replay: reconcile its exact saved objects, PVC and registry request first.

Build the committed Account Manager source with `lke-build-images --workloads
account-manager`, using a unique dev image tag. Keep the source SHAs, build log,
manifest and registry-verified linux/amd64 digest privately. Reuse the qualified
Video Cloud managed-host image for `pkimanagement`. This updates the API Deployment
only; the shared platform worker image setting must not be advanced implicitly.

```sh
python3 scripts/pki-service-dev/management.py --phase prepare \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE --output PREPARATION
python3 scripts/pki-service-dev/management.py --phase adopt \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE \
  --preparation PREPARATION --image VERIFIED_ACCOUNT_MANAGER_DIGEST --output ADOPTION
python3 scripts/pki-service-dev/management.py --phase seal \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE \
  --preparation PREPARATION --adoption ADOPTION --output BOOTSTRAP_REMOVAL
python3 scripts/pki-service-dev/management.py --phase verify \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE \
  --preparation PREPARATION --adoption ADOPTION --output FINAL_VERIFICATION
python3 scripts/pki-service-dev/management.py --phase issuer-egress \
  --authority ROOT_EVIDENCE --intermediate INTERMEDIATE_EVIDENCE \
  --preparation PREPARATION --image VERIFIED_ACCOUNT_MANAGER_DIGEST \
  --owner-image VERIFIED_VIDEO_CLOUD_DIGEST --output ISSUER_EGRESS
```

Preparation creates a dedicated verifier login inheriting the existing non-login
PKI verifier role, a private Secret for its DSN, public Service Root ConfigMap and
10 GiB retained identity PVC. It verifies PKI write and schema-create denial. It
does not rerun the global SQL grant/migration command. The active intermediate's
reviewed Service-client signing policy is added to certissuer's existing role.
The historical `hosts.py verify` server-only policy check no longer applies after
this explicit client-signing grant.

A one-day provisioner leaf under a separate two-day bootstrap CA permits initial
issuance only at certissuer. Its protected local key and owner-only Secret are
separate from the managed key, which the owner generates and retains in the PVC.
No managed private state leaves the pod. The existing Account Manager JWT keys
remain projected into its API container; old controller TLS key projection is
removed. The owner alone mounts persistent identity and bootstrap files and reads
the verifier DSN. Both containers share only a private socket volume. Initial
socket directory creation runs before either process; Recreate prevents overlap.

Adoption must pass ordinary human-asserted PKI calls for three dev users, exactly
one registered managed issuance, private state permissions/hash, absent API key
mounts and Device mTLS/MQTT ACL/QoS1. Sealing removes bootstrap settings/mount,
restarts from managed state, removes the temporary issuer CA and provisioner
permission, and deletes only the recorded bootstrap Secret using UID/resource
version preconditions. An actual TLS probe must receive an explicit certificate
rejection for the removed bootstrap identity; a timeout is not denial evidence.
The local private evidence retains the old credential for
audit; its runtime trust is gone. Do not delete the managed identity PVC.

The final audit checks runtime against `render_management`, which consumes the
private saved Deployment and API-only `PKI_ACCOUNT_MANAGER_API_IMAGE` pin. The
complete scoped overlays remain in `dev/pki/controller-bootstrap/rollout`; full
platform provisioning must reconcile them explicitly. No worker image changes
are implied by this API pin. Persisted bootstrap Secret overlays are removed on
successful sealing so a later scoped render does not recreate them.

Natural renewal, live revocation and response-stream cutoff, actual installed
CRL receipts, root rotation and failed-registry acceptance remain separate work.
Do not edit identity state or system clocks to manufacture renewal evidence.
Readiness alone does not qualify managed remote transport. A crashed owner may
leave a stale socket; verify the process is stopped before any recovery, and
recreate the pod's ephemeral socket volume rather than deleting identity state.
No staging changes, PRs, CI dispatch or automatic Git push are part of this runbook.

If adoption stopped after the owner successfully issued its identity because an
inspection check failed, `--phase resume-adopt --adoption FAILED_ADOPTION ...`
requires the original Deployment template, PVC UID and a single `succeeded`,
unrevoked registry issuance. It performs verification only; no resource mutation
or new issuance. Use the successful recovery directory as `ADOPTION` for sealing
and final verification, and retain the failed report. `issuing` is pending and
must never be treated as successful installation.

This audit describes initial adoption and expects one successful issuance. After
a later renewal or replacement, use that lifecycle's reviewed evidence rather
than relaxing this phase's original-identity checks or treating it as a monitor.

`issuer-egress` reuses the same private socket and managed identity for the one
App issuance route. It first admits both the legacy and managed caller, updates
only the Account Manager API and owner images, and proves a normal dev user
certificate issuance records caller `service:account-manager`. It then removes
the legacy CA and static credential Secret, narrows the caller policy to the
managed identity, and repeats issuance plus the Device baseline. The canary keys,
credentials and responses remain in the private phase output. This phase does
not change human login or MFA behavior. If the first canary fails after the
image switch, a new phase directory may resume only the exact saved transition;
it does not repeat either rollout.

The current dev App signer remains the existing `pki/app` OpenBao mount until the
separate App hierarchy milestone. This phase gives the certissuer Kubernetes
role only `update` on `pki/app/sign/app-user`; it grants no key, role, mount or CA
administration. The later App hierarchy cutover removes this temporary policy.

If sealing already restarted without bootstrap and removed issuer trust, but its
certificate-denial probe failed, `--phase resume-seal --sealing FAILED_SEAL
--adoption ADOPTION ...` checks the original template/PVC/state and removed trust,
then repeats a bootstrap-free restart, tests explicit certificate rejection and
deletes the recorded bootstrap Secret. It does not reintroduce trust or issue a
new identity. Preserve the failed report. The denial probe explicitly presents
the selected certificate even if its CA is no longer advertised, while verifying
the server's own chain/name; a missing-client alert is not accepted as evidence.
