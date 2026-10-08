# Unified Deployment Readiness Standard

Status: accepted deployment policy; existing-update script implementation delivered locally; environment qualification is operator-run.

Owner: `rtk_cloud_workspace`; service owners supply candidate and recovery checks.

Last reviewed: 2026-10-08.

Authority: deployment scope and readiness policy. The [deployment architecture](cloud-deployment-architecture.md)
owns configuration/provider boundaries, and [Platform PKI](../repos/rtk_cloud_contracts_doc/platform_pki.md)
owns identity, signing and recovery policy. [ADR 0003](adr/0003-unified-environment-deployment.md)
records the replacement decision. Dated observations and local fixtures are evidence,
not permission to relax this policy.

## Fixed script delivery scope — 2026-10-07

The current delivery is limited to `scripts/check-deployment-preflight.sh` and
`scripts/deploy-environment.sh`, their necessary existing Go implementation,
local regression tests and operator instructions. The operator runs the actual
environment checks and deployment. Implementation work does not deploy or run
live qualification on the operator's behalf.

- All environments use one workflow for updating an existing environment's
  enabled workloads and maintained configuration. This is not an image-only
  restriction and does not select an environment-specific renderer.
- Preflight verifies and may create a missing configured media bucket when no
  storage cutover is requested; other provider probes remain read-only. It
  resolves one release and target, checks required configuration,
  images, tools/access, capacity/admission, PKI/Secret bindings and dependencies,
  then reports GO or NO-GO with the plan digest and actionable failures.
- Deployment rechecks that same target and ownership, executes the checked
  resource bodies, waits for rollout and verifies required workload, PKI and
  endpoint health before reporting completion. Partial failure is not success.
- Device/App API and main issuer role flags come from shared architecture
  settings. Existing App API main/initializer images, retained trust-state
  references and the original TLS passthrough route are included in the same
  plan. Missing references or role incompatibility remain explicit blockers;
  see [the role mapping](cloud-deployment-architecture.md#shared-device-and-app-authentication-roles).
- Existing identity, keys, CRLs, Secrets and persistent data are retained.
  A required incompatible identity/schema/storage change is an explicit migration
  blocker, not an automatic reset or an additional recovery project.
- Output/help remain English with exit codes 0/1/2. Environment operational data
  remains under `~/.config/rtk_cloud/<environment>/` using common path helpers
  and private permissions; `cloud_env/` contains configuration only.

Fresh-environment bootstrap, Production activation, new custody/escrow systems,
backup/PITR implementation and restore/RPO/RTO qualification are outside this
delivery. The broader qualification design is retained as a separate roadmap; building
those capabilities is not a dependency of finishing these two scripts. Necessary
safeguards for the selected update remain in force. Local regression results do
not establish environment GO; the operator must run the scripts against the
selected environment and release.

Preflight and deployment stream `[RUNNING]` stage IDs while resolving provider
and formal CI images, observing Kubernetes, compiling the target and qualifying
required gates. A running stage repeats its elapsed time every 10 seconds.
Progress lines are not PASS/GO evidence and do not enter the plan hash or JSON
report. Errors, cancellation and completion stop the heartbeat before results
are printed; private provider output and credentials are never progress text.

## GO for an existing environment update

The current scripts report `scope=existing-environment-update`. GO authorizes
execution of that exact checked release/configuration/resource plan; it does not
declare deployment complete. Required update gates are the same in all three
environments:

| Gate | Current update evidence |
| --- | --- |
| `deployment.target` | Complete maintained target, original owners, unchanged configuration/release/bindings and durable-volume identities. |
| `deployment.release` | Immutable images, exact clean workspace/service revisions, successful formal CI/publication and available images. |
| `deployment.execution` | Selected Kubernetes API access, native node-class capacity, original storage, execution prerequisites and server dry-run of the exact resource bodies; an OpenBao `OnDelete` update also requires named Pod restart rights and UID/resourceVersion conditional deletion admission in dry-run. Existing K8s upgrades do not require local Linode cluster ID or account-quota files; provider quota belongs to explicit infrastructure operations. |
| `deployment.pki` | Original synchronized runtime Secret bindings and the maintained runtime PKI checks at the start and end of the configured rollout window; identity/mount changes require explicit migration. |
| `deployment.compatibility` | Selected AM/Billing schema prerequisites, required restricted payment-simulator startup and migration setting, with candidate source/CI checked by the release gate. |
| `deployment.network` | Configured public CertIssuer ingress, authenticated mTLS and anonymous TLS denial; planned Kubernetes network objects also pass exact-body admission. |

For the managed Account Manager socket profile, the public probe uses the
original `pkimanagement` owner identity in memory and connects to the explicit
public hostname. It fences the validated identity snapshot, verifies server
trust/SAN and anonymous client-certificate denial, and issues no certificate.
An internal socket response cannot qualify this public route. Probe scratch
belongs to the selected environment's private `deployment/tmp/` and is removed
on completion; private keys are never exported into files, arguments or reports.

Live Service registry evidence must match the selected environment, domain,
issuer and Root. Its timestamp may be at most five minutes old and at most five
seconds ahead of the checking machine. `deployment.pki.clock` identifies a
future timestamp; verify the checking machine and controller node against a
trusted time source and correct the faulty clock. `deployment.pki.registry.stale`
requires a new live inventory. These failures do not authorize replacement
identity or a fabricated report.

Admission comparison permits only the client's refresh of
`kubectl.kubernetes.io/last-applied-configuration` as bookkeeping. It does not
remove that annotation from baseline drift checks, mutate the approved plan,
or relax existing UID, consumer configuration and security-policy checks.

Storage fences use fresh reads with the same frozen Kubernetes identity and
retry a transient API read failure once. `deployment.access.storage` reports a
sanitized read/access failure for the exact PVC or PV; it does not establish
that data or ownership was lost. Successfully observed missing, replaced,
terminating, unbound or changed PVC/PV bindings remain
`deployment.execution.storage` and require verified recovery or migration.

Missing or failed update evidence, changed inputs/owners, cancellation or `--fast`
is NO-GO. Actual scheduling/startup and endpoint health are verified after rollout
before completion is recorded. Ordinary update does not mutate bootstrap
credentials or initialize new backup/identity state. Managed issuer lifecycle,
fresh bootstrap and activation/restore qualification remain separate operations;
the later native inspection tools are not prerequisites imposed on older running
images merely to update their Pods.

`DEPLOYMENT_POSTGRES_BACKUP_ENABLED=false` is the common baseline. A maintained
backup component must explicitly select `true`; then its original configuration,
dedicated credentials and selected runner image remain required. Missing optional
backup configuration never creates a backup project during normal preflight.
Existing undeclared workloads still block planning; the flag cannot silently
remove or reset an already enabled component.

Preflight and deployment use the same common compiler, including selected
adapter defaults/overrides and resolved provider settings. The computed
configuration SHA-256 includes both sets; changing an adapter setting requires
new preflight qualification. It is a plan/report identifier, not a new
configuration file that operators must create.

Expected compiler failures must provide a structured public blocker with a
stable ID, the affected resource/container when known, a specific cause and a
repair/recheck operation. Missing renderers, unknown sidecars, conflicting
definitions and missing image roles must not collapse into a generic target
failure. External-tool failures report their safe compilation stage and access
or configuration repair boundary, without printing raw provider output, YAML,
credentials or private file contents. An unclassified failure is explicitly
`deployment.compilation.internal`, an implementation diagnostic gap rather than
an instruction to select another release.

## Retired static PKI templates

The production binary no longer contains the static CertIssuer or FactoryEnroll
Deployment templates or their PKI initialization/reconciliation block. Historical
Kubernetes samples are test fixtures only. Public plan/preflight/create/upgrade
entries use the maintained managed-PKI engine, with no private-file detection or
static renderer fallback. Scoped legacy video-cloud deployment is rejected
before mutation rather than rebuilding identities through deleted templates.

## CI image selection

Preflight reads formal service `image-manifest.json` artifacts directly for the
selected clean workspace service revisions; the pinned selection is held in
memory and included in the plan digest. Deployment repeats selection and requires
the approved plan SHA-256. Missing or invalid CI publication is NO-GO. Local
`release.json` selection and freeze-release file generation are retired. Provider
image versions remain maintained configuration and are registry-digest pinned.

### Preflight-produced identity observations

An existing-environment preflight does not require a pre-existing private
`identity-bindings.json`. It obtains actual owners and existing identity references
from the selected API and compiles the maintained target in memory. It records
existing UIDs only; newly planned resources have no UID until creation. Target
mounts/configuration are renderer output, and persistent contents or seal state
cannot be inferred from a Pod volume reference.

Preflight publishes the current result beneath the canonical environment store
as `deployment/identity-bindings.json`; complete GO is bound to the exact plan and
observed-bindings hashes. Failed/fast preflight invalidates previous GO. Deployment
must freshly compile the same plan, verify the matching GO record and fence
ownership during execution. An unsupported existing OpenBao profile remains a
compatibility blocker, not an instruction to manufacture a bindings file.

### Historical one-off execution records

A standalone Job with an explicit Complete/Failed terminal condition, zero active
workers and only terminal Pods is historical evidence, not an enabled update
component. Preflight and execution inventory checks preserve it without requiring
a new renderer, old image publication or retired temporary credentials. Pod
lineage must match the original Job UID/API version. Running, partial, scheduled,
foreign or ambiguous owners remain NO-GO; no online cleanup is performed by
preflight and no historical commands are imported into the candidate plan.

A terminal standalone Pod is likewise historical only when it has no owner
references and `restartPolicy=Never`. Running, restartable or ambiguous Pods
remain required inventory blockers; historical inspection Pods are not deleted
or replayed by preflight.
