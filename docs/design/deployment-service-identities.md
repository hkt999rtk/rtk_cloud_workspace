# Deployment service identities

Status: accepted design (operator decisions of 2026-09-29). Implementation is
under verification; the evidence table distinguishes code paths from live rollout.
No live environment is changed by this document.

Owner: rtk_cloud_workspace. Authority: `platform_pki.md` owns CA hierarchy and
runtime issuance; this document owns deployment credential persistence and reuse.

## Decisions and boundaries

- The environment-local SecretStore is the authoritative deployment input:
  `~/.config/rtk_cloud/<environment>/` (or the explicit configuration base plus
  that environment). Plaintext credential files are an accepted operator policy.
  Directories are 0700 and files 0600. SecretStore is a file store, not an encrypted
  vault. Secrets never enter tracked configuration, reports, command arguments,
  build artifacts or logs.
- Operator custody covers workstation/account access, sharing, backups and
  incident reporting. Deployment software owns permissions, environment binding,
  safe persistence, redaction and correct distribution. Operations management owns
  access, handover, revocation and incident-response procedures. Responsibility
  for an incident follows its actual cause.
- A deployment identity comprises environment, service subject, purpose, trusted
  issuer lineage and key/certificate material. Installing a new application image
  or changing capacity, pricing, service features or ordinary deployment settings
  does not authorize replacing that identity.
- An absent initial identity may be enrolled automatically during an operator-
  initiated deployment using the configured environment signer. Persist it before
  installing it into a workload. Repeat deployments reuse the persisted identity.
- Partial, corrupt, expired, revoked, wrong-environment or mismatched material is
  an existing identity requiring reconciliation or explicit renewal/rotation. It
  must never be silently treated as absent and replaced.
- Device/App/user private keys and CA signing keys already held inside OpenBao
  remain there. This policy concerns deployment-owned service leaves and their
  initial trust inputs; it does not export user or CA signing keys.
- Runtime-managed renewal and initial deployment enrollment are distinct actions.
  Existing runtime owners may already have a successor leaf for the same identity.
  Ordinary deployment must not overwrite their durable state with an initial seed.
  Operator-triggered polling/renewal orchestration is a separate lifecycle change;
  this change does not silently disable existing runtime renewal.

## Identity catalogue

Each service has a separate key. Replicas with separately registered identities
also need separate stable instance records; they must not concurrently own one
managed identity state file. Only services selected for a deployment are enrolled.

| Service | Canonical subject | Purpose |
| --- | --- | --- |
| Account Manager | service:account-manager | internal service client |
| Certissuer | service:certissuer | signing service management client |
| PKI Controller | service:pki-controller | controller management client |
| Factory Enroll | service:factory-enroll | factory registration client |
| Video Cloud API | service:video-cloud-api | core API management client |
| MQTT foundation | service:mqtt | platform service registration |
| Shadow | service:shadow | platform service registration |
| WebRTC | service:webrtc | platform registration / management |
| Video Storage | service:video-storage | platform registration / management |
| OTA | service:ota | platform registration / management |
| Logger | service:logger | platform service registration |
| Log Ingester | service:video-cloud-logingester | log-ingestion management client |
| EMQX PKI owner | service:emqx-pki | MQTT server certificate management |
| PKI Broker | service:pkibroker | broker trust and session management |
| OpenBao PKI owner | service:openbao | OpenBao transport certificate management |

`service:deployment-bootstrap` is a temporary, session-scoped enrollment caller.
It is not a permanent workload identity and is not reused after the session seals.
Server-auth certificates for private listeners are separate from the client-auth
identities above. DNS/EKU differences must not be handled by sharing a leaf/key.
Public HTTPS, MQTT server TLS and OpenBao TLS retain their separate trust domains.
Legacy `account-manager` and `factoryenroll` subjects are retained where already
installed; moving them to canonical subjects is an explicit migration.

## Signing a new environment

1. Resolve the selected environment and exact stack. Read its SecretStore and
   environment configuration before provisioning consumers. Obtain the external
   cloud/cluster access required by the deployment; never borrow another
   environment's credentials.
2. Initialize the selected environment's CA/signing infrastructure once, using
   its explicit bootstrap procedure. Record immutable issuer IDs, public chains,
   fingerprints and signer references. Existing Roots/intermediates are reused.
   A missing service leaf never authorizes creation of a replacement Root.
3. Configure the Service intermediate's exact service-subject/DNS policies and
   Certissuer's restricted OpenBao access. The CA registry and provider must agree
   before leaves can be signed. Bootstrap authority is an audited deployment
   action; never invent human approvers to satisfy a different CA workflow.
4. Establish a short-lived deployment enrollment identity and a recorded session
   scoped to the selected environment, issuer and required service subjects.
   The environment signing configuration pins the actual HTTPS issuer endpoint,
   server DNS name and independent trust anchor. No insecure TLS fallback.
5. For each missing service identity, generate its key and CSR, persist the
   pending request and stable request ID locally, then submit the request through
   the existing authenticated Certissuer API. Certissuer validates the bootstrap
   session/allowlist, reserves the registry issuance, signs through OpenBao and
   records the result. The operator's human identity is not installed in a Pod.
6. Verify the returned subject, purpose, key match, issuer/root, validity and
   response/request identity. Save the complete credential record atomically in
   the environment SecretStore before rendering any Kubernetes Secret.
7. Install the selected credential only into its intended workload. Verify mTLS,
   registry admission and consumer acknowledgements. Seal/revoke the temporary
   bootstrap authority through the existing session procedure.

The ordinary transport bootstrap currently used by baseline LKE Account Manager
and Factory Enroll creates a local transport CA and both client leaves in one
bundle. It is a compatibility path, not a registered Service intermediate. It
must preserve its existing bundle on updates and cannot mint registry-backed
optional service identities. Migrating it is an explicit trust migration.

## Storage and precedence

| Material | Authoritative deployment location | Runtime location |
| --- | --- | --- |
| Baseline Account Manager / Factory Enroll transport bundle | `pki/certissuer/` | named Kubernetes Secrets |
| MQTT transport seed | `pki/mqtt/` | MQTT TLS Secret / explicitly adopted owner state |
| OpenBao transport seed | `openbao/tls-ca.crt`, `tls.crt`, `tls.key` | OpenBao TLS Secret / explicitly adopted owner state |
| Registered service initial identity | `pki/services/<service>/identity.json` | service Secret or initial seed for its managed owner |
| Pending initial enrollment | same identity record, pending state | no consumer installation yet |
| Enrollment signer configuration | `pki/services/issuer.json` plus environment-local referenced files | temporary bootstrap caller only |
| Runtime-renewed identity | runtime owner state; initial record remains provenance | existing owner PVC |

Record environment, stack, service subject, request ID, pinned Root fingerprint,
certificate fingerprint, validity and lifecycle state with the credential. The
private key may be stored in the accepted plaintext SecretStore but never in
public inventory output. Pin/record changes require an explicit lifecycle action.

For an existing environment with live credentials but no local record, reconcile
and adopt verified existing material before redeployment. Do not issue a second
identity merely because the operator changed computers. If local and runtime
material disagree, report the public fingerprints and resolve the mismatch; do
not choose a winner by file modification time.

## Update and failure rules

| Observation | Deployment action |
| --- | --- |
| Complete, valid local identity matches environment and purpose | reuse bytes; no signing call |
| No local identity and no prior installation/issuance | enroll once, persist, then install |
| Pending enrollment | reuse key, CSR and request ID; recover uncertain signing result |
| Only some bundle files exist | stop without replacement |
| Existing key algorithm differs from a new default | reuse valid identity; default applies only to new generation |
| Listener DNS or service subject changed | stop with explicit identity-change guidance |
| Expired or revoked identity | explicit renewal/recovery, never generate a new CA |
| Local pin/record differs from configured environment | stop before signing or installing |
| Existing Kubernetes identity differs | reconcile; no blind Secret overwrite |
| Runtime owner has renewed its leaf | retain owner state; never reseed it during image update |

A signing timeout is not evidence that no certificate was issued. Keep the exact
pending request and ask the existing issuance/recovery API for its outcome.
Deployment serialization protects each local identity record. Atomic writes
preserve either the prior complete record or the new complete record. No new PVC
is needed for operator-side credential storage.

## Implementation and verification

- Existing baseline material loaders and Secret manifests:
  `scripts/go/rtk-cloud/lke.go`, `secret_store.go`.
- Registry-backed issuance and receipt/replay:
  `repos/rtk_video_cloud/internal/certissuer/service_client_renewal.go`,
  `service.go`, `service_client_registry.go`,
  `internal/pki/service_client_issuance.go`.
- Runtime owner and first-install protection:
  `repos/rtk_video_cloud/internal/serviceidentity/`,
  `cmd/serviceidentity-bootstrap/`.
- Deployment reconciliation: `deployment_service_identity.go`,
  `deployment_identity_store.go`, `deployment_identity_cluster.go`. Presence of
  a catalogue entry does not prove live enrollment.

Acceptance must cover fresh enrollment, unchanged second deployment (same key and
certificate with zero signing calls), configuration drift rejection without file
changes, partial-file preservation, restart after uncertain issuance, wrong
subject/root/environment rejection, and runtime identity preservation. Test keys
and issuers are disposable local fixtures; shared environments are not fixtures.

## Operator configuration and commands

The same rules apply to dev, staging and prod. Resolve the environment with
`deployment --environment`; do not copy another environment's issuer file or
credential bundle. `secrets init --environment <name>` establishes the local
store. Complete the environment's Service Root/intermediate ceremony in
`platform_pki.md` §8.1.1 before enrolling Service leaves. The existing
`repos/rtk_video_cloud/deploy/pki/render-staging-controller.py --phase
service-bootstrap` prepares the restricted bootstrap Job/session; its README
lists issuer, root pin, subject allowlist and session arguments. It does not
create an approved Service issuer merely because an argument is present.

Save the public issuer references and the short-lived bootstrap leaf/key under
that environment's SecretStore. The required `pki/services/issuer.json` shape
is below; every file reference is relative to the selected environment root.
Values are examples/placeholders, not live credentials:

```json
{
  "environment": "dev",
  "stack": "video-cloud-dev",
  "endpoint": "https://certissuer.dev.example.invalid",
  "server_name": "certissuer.dev.example.invalid",
  "server_ca_file": "pki/services/issuer-server-ca.crt",
  "registration_server_ca_file": "pki/services/registration-server-ca.crt",
  "root_ca_file": "pki/services/service-root.crt",
  "root_sha256": "<actual lowercase SHA-256 of the Service Root DER>",
  "bootstrap_cert_file": "pki/services/bootstrap/client.crt",
  "bootstrap_key_file": "pki/services/bootstrap/client.key"
}
```

The issuer endpoint must be reachable from the deployment process, HTTPS with
verified DNS and independent server CA; redirects are refused. The endpoint
server CA and registration server CA may differ from each other and from the
Service **client** signing root. Do not substitute one merely because it is
available. Bootstrap authorizations remain server-side: an active session,
exact subject allowlist, issuer policy and receipt are required. A copied PEM or
matching subject alone cannot grant signing authority.

### Registered platform services

During canonical LKE deployment, the existing registration preflight now
reconciles `mqtt`, `shadow`, `webrtc`, `video-storage`, `logger` and `ota` when
their deployment flags select them:

1. Verify the Account Manager registration listener's TLS/trust/CRL inputs.
2. Read the environment identity and the selected Kubernetes identity Secret.
3. If only a live identity exists, verify it with independent listener trust,
   CRL and configured Service Root, then adopt its exact material locally.
4. If genuinely absent, persist a P-256 key/CSR and stable initial request ID,
   request a 30-day leaf through Certissuer, and verify/persist the result.
5. Create a missing Secret. If it already exists, require agreement with the
   saved identity. Creation uses Kubernetes `create`, so a concurrent install
   cannot be overwritten. A failed installation retains the saved identity.

The root pin and environment/stack/subject bind the deterministic initial
request ID. Losing a saved key cannot authorize another initial issuance:
Certissuer rejects a changed CSR under that request ID. Restore/reconcile the
original request rather than deleting it to retry.

### Managed service owners, including Account Manager

For a selected managed client, the deployment preparation command enrolls or
reuses its local initial credential and optionally installs an immutable seed
Secret. This command supports the 15 catalogue subjects:

```sh
go run ./scripts/go/rtk-cloud -- deployment service-identity \
  --environment dev --subject service:account-manager \
  --confirm video-cloud-dev --install-seed
```

The command prints only environment, subject and public certificate fingerprint.
`--install-seed` uses the selected environment's kubeconfig and creates
`<service>-deployment-identity` with key `identity.json`. Account Manager goes
into `<stack>-account-manager`, OpenBao into `<stack>-secrets`, and other managed
clients into `<stack>-video-cloud`. Existing differing seeds are not overwritten.
Without the flag, the command only prepares the environment-local credential.

Mount the seed read-only in the **identity owner** at initial deployment. For
Account Manager's `pkimanagement` container, set
`PKI_MANAGEMENT_IDENTITY_SEED_FILE=/run/deployment-identity/identity.json`.
Clients using the common settings loader use `<PREFIX>_IDENTITY_SEED_FILE`
alongside their existing state path/root/endpoint settings. The seed cannot be
combined with bootstrap caller credentials or a root transition. Each instance
retains its existing distinct persistent state path.

The owner verifies environment, subject, Root, key/chain and current registry
admission before a one-time install. Existing current or pending state is never
replaced. An owner with current state does not read the original seed again;
it can continue after the seed is removed or its original leaf expires. Ordinary
image/configuration updates therefore retain the existing owner and PVC rather
than re-running first-enrollment preparation. The bootstrap/session ACK and seal
remain after real consumer verification, not after merely writing a Secret.

Baseline LKE rendering cannot replace an already managed Account Manager,
Video Cloud API or Certissuer Deployment: preflight refuses that downgrade before
mutation. Use the managed
renderer/patch path for such updates and retain its identity volumes. This
change provides initial credential enrollment/delivery/consumption; it does not
migrate an existing baseline deployment to the managed topology automatically.

### Compatibility transport bundles

Existing `pki/certissuer/`, `pki/mqtt/` and `openbao/` bundles are loaded and
validated in place. A changed algorithm generation default does not change an
existing valid identity. First-generation defaults still must be explicitly
configured. DNS, key-pair, purpose, issuer and validity failures require explicit
repair. Missing files within an existing bundle are not a fresh environment.
For existing live baseline Secrets, deployment compares local and live material
before changing workloads. Restore the selected environment's bundle if its
local record was lost; a different operator machine is not a new environment.

## Delivery evidence and rollout boundaries

| Implementation | Verification |
| --- | --- |
| Environment signing/pending record and local reuse | `deployment_service_identity_test.go`: HTTPS mTLS signing fixture, identical retry, no signing after completion, wrong response/key/stack/root rejection and verified adoption |
| Legacy transport identity stability | `lke_test.go`: generation-default changes reuse bytes; DNS changes and partial bundles preserve original material |
| Managed initial client install | Video Cloud `serviceidentity/deployment_seed_test.go`: one-time install, wrong subject, current/pending protection |
| Registry gate and owner preservation | Video Cloud `pkitrust/deployment_seed_test.go`: environment binding, registry denial before install, existing state bypasses the original seed |

Code verification is distinct from environment rollout. Installing a new Service
Root/intermediate, opening a bootstrap session, mounting seed Secrets and
recording consumer ACKs must appear in the selected environment rollout evidence.
No claim of live migration or successful deployment follows merely from merging
this design or passing isolated fixture tests.

## Operator inspection

[Certificate operations](../certificate-operations.md) defines the Go
`deployment certificate-check` command, expected-source inventory, current-owner
inspection, expiry thresholds and report/exit semantics. It is an operator-
initiated read-only operation. No timer, automatic renewal or new scheduling
policy is introduced. Environment handover includes checking every selected
identity's current source; unknown/uncovered managed owners cannot be reported
as healthy. Initial seeds remain provenance after successful managed enrollment.
