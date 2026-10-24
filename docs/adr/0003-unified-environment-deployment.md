# ADR 0003: Unified Environment Deployment and Production Qualification

Status: accepted design; implementation and environment qualification in progress.

Date: 2026-10-06

Supersedes: private schema-1 managed workload upgrade policy

Superseded by: none

## Context

The former full provisioning path selected a private Deployment snapshot when one exists
and otherwise selected an incompatible static PKI renderer. The snapshot path
excluded production and retained provider controllers and routing. Neither path
implemented the complete environment upgrade promised by a full preflight verdict.

## Decision

Use one environment-independent compiler and executor for create and upgrade.
Compile architecture/environment intent, a selected immutable release and
environment-local managed identity/secret bindings into a versioned resource plan.
Live objects supply ownership and concurrency baselines, not desired templates.
Explicitly inventory Deployments, StatefulSets, DaemonSets, Jobs, Services,
ConfigMaps, RBAC, network policy, routing, persistence and provider operations.
Unknown owners and unsupported lifecycle changes fail closed with repair actions.

Preflight compiles in memory without provisioning, generating credentials or
writing runtime state. Qualification and execution use identical desired bodies,
with a deterministic plan digest and fresh ownership/configuration fences. An
absent private snapshot never selects another renderer. Private snapshot selection and prepare-upgrade are retired at the public complete
deployment entry. Missing replacement capability remains NO-GO during cutover.

GO means that all required pre-deploy gates for the exact selected complete
environment operation passed. Fast/scoped evidence cannot qualify that operation.
Post-deploy health and functional acceptance are separate completion gates.
New-environment infrastructure and admission/bootstrap have explicit stage gates;
nonexistent resources are never reported as verified runtime evidence.

Managed issuers, CRLs, trust pins, workload private state and PVCs retain their
ownership. Root changes and incompatible storage/schema changes use explicit
lifecycle plans, never implicit regeneration or delete/recreate. A single configured
environment operator retains PKI authority; quorum recovery custody is a separate
responsibility, not another PKI approval role.

Production uses a three-node OpenBao Raft profile, five Shamir shares and a
three-share threshold. No single custody location may retain a quorum. Controller
and operator material uses ~/.config/rtk_cloud/<environment>, with 0700 directories
and 0600 files; portable PKCS#8, JSON and age replace OS-specific disk images.
Online issuer private keys remain inside OpenBao. Escrow is independent of the
workload cluster and original controller, with recorded owners and recovery access.

Production activation requires measured RPO <= 15 minutes and RTO <= 4 hours,
continuous PostgreSQL WAL protection, encrypted Raft snapshots, complete durable
data inventory, independently retained security history, issuer/leaf lifecycle,
bootstrap sealing/cleanup, HA failure and isolated restore evidence. Replace the
unconditional production startup rejection only with effective capability checks;
missing qualification must still deny activation. Restore never regenerates a Root
or reopens revoked identities. Security-history gaps keep signing and traffic fenced.

## Consequences

Environment differences are explicit configuration and qualification policy, not
renderer forks. Production activation requires service and recovery changes, not
just removal of a CLI guard. Existing file-storage deployments need an explicit
matched migration before adopting Raft. Release selection, obsolete snapshot
settings and required managed bindings need a documented cutover. Tests must
exercise the same compiler/executor for dev, staging and prod and prove that
required missing evidence cannot produce GO.

An implementation merge is not an environment qualification receipt or permission
to perform a production rollout.
