# Provider-neutral DNS Adapter Architecture

## Contract

DNS is selected independently from the deployment adapter. An environment may
use LKE with GoDaddy or Route53, and changing Kubernetes/cloud providers does
not require changing the DNS vendor.

```env
DEPLOYMENT_ARCHITECTURE=kubernetes
DEPLOYMENT_ADAPTER=lke
DNS_ADAPTER=godaddy
```

The environment owns `CLOUD_DNS_ROOT_DOMAIN` and the DNS adapter selection.
Shared DNS orchestration owns public hostname intent, record plans, DNS
convergence, ACME DNS-01 lifecycle, and sanitized evidence. A DNS adapter owns
credential validation, zone discovery, record-set mutation, and
provider-private evidence. Deployment adapters only return normalized public
targets; they do not call DNS vendor APIs.

The [environment DNS naming source](environment-dns-naming.md) owns the
environment mappings, public hostname templates, endpoint roles, and explicit
exceptions. Shared DNS orchestration resolves names from that policy and the
selected environment's configuration. DNS adapters apply the resulting record
intent within the root zone; provider selection does not define service names.

## Configuration and runtime

Tracked defaults live in `cloud_deploy/dns_adapters/<adapter>/`. Optional,
reviewed provider escape hatches live in
`cloud_env/<environment>/overrides/dns.env`. Zone IDs, API keys, AWS profile
sessions, change IDs, and credentials are never tracked configuration.

The generic configuration plan remains at
`cloud_env/<environment>/runtime/resolved/dns-plan.json`; deterministic root and
adapter metadata uses `runtime/resolved/dns.env`. Every operational DNS
file uses `~/.config/rtk_cloud/<environment>/deployment/dns/<adapter>/`:

| File | Producer and consumer |
| --- | --- |
| `ownership.json` | Shared record synchronization and controlled removal; binds the selected environment, adapter, zone and exact recorded values. Explicit current-state reconciliation adds `reconciliation_sha256` to bind the reviewed receipt. |
| `challenges.json` | Shared Go public-HTTPS ACME issuance and recovery, Certbot's shared `dns-hook present/cleanup`, and interrupted-challenge cleanup; retains only outstanding domain/value pairs and removes only each recorded challenge value. |
| `state.json` | Adapter zone discovery evidence, including provider-private hosted-zone ID. |
| `dns.env` | Shared synchronization metadata, including the original applied-record count and update time. |
| `reconciliation.json` | Confirmed `deployment dns-reconcile-state` publishes reviewed current-state Linode/DNS mappings and hashes; ordinary removal validates and retains the receipt for retries. It is not original deployment history or an ACME cleanup receipt. |

Directories are real `0700` directories and files are regular `0600` files.
Writers, readers, cleanup and the unified plan input digest use the same path
resolver. Credentials require explicit configured `CLOUD_ENV_NAME`; a missing
environment never selects staging. Switching checkouts does not change authority.
Operational files are not desired Kubernetes templates, release intent or GO
evidence. Their exact digest fences input drift between deployment phases.

Generic defaults are `DNS_RECORD_TTL=600`,
`DNS_PROPAGATION_TIMEOUT_SECONDS=900`, and
`DNS_PROPAGATION_INTERVAL_SECONDS=10`. Provider-specific limits are validated
by the selected adapter. Unknown keys, cross-layer duplicates, invalid record
types, hostnames outside the root zone, and missing credentials fail before
DNS mutation.

## Adapter lifecycle

Every DNS adapter implements `Name`, `Validate`, `DiscoverZone`,
`GetRecordSet`, `UpsertRecordSet`, `DeleteRecordValues`,
`PresentDNS01Challenge`, `CleanupDNS01Challenge`, and `CollectEvidence`.

```text
resolve environment and adapters
  -> deployment adapter returns normalized public targets
  -> shared DNS planner creates desired A/TXT record sets
  -> selected DNS adapter mutates records
  -> shared authoritative/public resolver convergence checks
  -> shared certbot lifecycle requests the certificate
  -> selected DNS adapter presents and cleans DNS-01 values
  -> shared Kubernetes runtime installs the TLS Secret
```

The shared Go public-HTTPS producer publishes the complete pending DNS-01 set
before requesting CA validation. It verifies every authoritative nameserver and
at least two of three independent recursive resolver views, so one public
resolver outage does not stop an otherwise converged batch. It allows the SOA negative-cache
lifetime to pass once for that batch. The saved per-order propagation horizon
survives interruption; retries recheck current values. Any observed authoritative
negative answer invalidates the earlier horizon in the same durable DNS status
before another wait; renewed convergence extends that status monotonically.
The deployment admission clock stays unchanged. A positive TXT answer from
one resolver cannot prove that the CA has expired a previously cached NXDOMAIN.
The configured propagation budget covers convergence; the required SOA lifetime
is accounted for separately within the original deployment deadline.

The generated certbot hooks invoke the shared DNS command and contain no
vendor HTTP calls. Multiple validations for one `_acme-challenge` name are
submitted as one record set. Cleanup removes only the matching validation
value and preserves unrelated TXT values.

Public record ownership is recorded in the selected environment private store. Removal deletes
only records owned by the environment whose current values still equal the
recorded target. External drift stops removal instead of overwriting or
deleting operator-owned data.

## GoDaddy adapter

The workspace adapter directly implements GoDaddy record APIs; active
deployment code does not call the service submodule's `godaddy-dns` tool.
Credentials are resolved as `GODADDY_KEY` and `GODADDY_SECRET` from
`~/.config/rtk_cloud/<environment>/operator/env/`. The CLI does not use
process-environment overrides, shared profiles, or legacy home env files, and
does not copy credentials into tracked environment config or runtime evidence.
`GODADDY_ENV` selects production or OTE and remains adapter-private.
GoDaddy's record TTL constraints are validated only by this adapter.

## Route53 adapter

The Route53 adapter uses AWS SDK for Go v2 and its default credential chain.
It discovers the unique public hosted zone matching
`CLOUD_DNS_ROOT_DOMAIN`; no hosted-zone ID is stored in environment config.
Zero matches, multiple public matches, or private-zone-only matches fail before
mutation. The control-plane region defaults to `us-east-1` and standard AWS
runtime configuration may override it.

Route53 hosted-zone IDs and change IDs are provider-private runtime evidence.
The adapter uses `ChangeResourceRecordSets` for UPSERT/DELETE and waits for the
change plus shared public DNS convergence.

CI uses a mocked Route53 client. An approved disposable public hosted zone can
run the opt-in SDK smoke with
`RTK_CLOUD_ROUTE53_LIVE_ROOT_DOMAIN=<zone> go test ./rtk-cloud -run TestRoute53LiveSmoke` from `scripts/go`; the test creates and removes only its `_rtk-dns-adapter-smoke` TXT record and must never target a production zone.

## Migration

- Add `DNS_ADAPTER=godaddy` to existing environment `deployment.env` files.
- Do not copy GoDaddy credentials into environment config or runtime evidence.
- Remove `--godaddy-env` and GoDaddy-specific TTL flags from active commands;
  use the generic DNS timing keys.
- Legacy operational files are never read by normal execution as a fallback.
  An existing old file with a missing private counterpart blocks adapter mutation
  with `deployment.dns.state`. The diagnostic identifies the environment,
  adapter, legacy relative filename and required canonical relative filename,
  and distinguishes an existing source from an inspection failure. Restore the
  original private store, or explicitly import a selected original runtime:

  ```sh
  go run ./scripts/go/rtk-cloud -- deployment dns-import-state \
    --environment staging --source-runtime /absolute/original/runtime \
    --confirm video-cloud-staging
  ```

  The importer requires matching original environment, stack and root zone,
  authenticated current zone reads, exact owned record values and outstanding
  challenge values; Route53 additionally verifies the original hosted-zone ID.
  It changes no DNS records or credentials. Bounded original `0600` source files
  remain intact and are archived byte-for-byte under
  `migration-backup/dns-state/<source-sha256>/` in that same environment store.
  Ownership gains an explicit environment binding. Source changes, active
  execution fences, malformed files, DNS drift and conflicting destinations
  reject import. Repeating the same verified source is idempotent.
  Imported metadata alone does not establish record ownership: removal still
  requires the original ownership file and completed ACME cleanup. A removal
  retry does not require full preflight; run full preflight before any subsequent
  deployment.
- Missing or no-longer-visible original records require a separately reviewed
  DNS reconciliation; do not fabricate ownership or silently discard cleanup
  evidence to pass import. Import success is not full deployment GO.
- GoDaddy read-then-delete has no provider CAS primitive. The existing remote
  value drift check is required but does not exclude an external edit between
  that read and mutation; perform controlled removal without concurrent DNS
  writers. This local-state migration does not claim atomic remote transactions.

## Reviewed current-state reconciliation

When original DNS ownership cannot be restored or imported, the explicit
`deployment dns-reconcile-state` command can review the current managed A
records against the selected Linode environment. This is a declared recovery
authority, not reconstruction of missing historical evidence. It never changes
DNS records, keys or credentials.

```sh
./scripts/deploy-environment.sh dns-reconcile-state \
  --environment staging \
  --report "$HOME/.config/rtk_cloud/staging/deployment/reports/dns-reconciliation-review.json"
```

Default execution is a read-only review. It requires the exact configured
HAProxy edge and TURN role labels, required `rtk-cloud`, stack and role tags,
matching region, numeric Linode IDs and public IPv4 addresses. Selected managed
A names must resolve to those exact targets. A configured factory-enrollment
name can be reviewed as a retained managed record even when its current feature
is disabled. Old unknown aliases, name-only matches, unsupported records and
ambiguous targets are excluded; outstanding ACME evidence blocks reconciliation
until original challenge cleanup is resolved.

The review prints `review-sha256`. An optional absolute `--report` path must be
inside that environment's canonical `deployment/reports/` directory. After the
operator approves the displayed mappings, repeating the command with
`--confirm video-cloud-staging --review-sha256 "$DNS_REVIEW_SHA256"` publishes
only private `reconciliation.json` and ownership bound to its digest. Publication
rechecks current mappings and rejects a changed review or conflicting existing
evidence. Original canonical and legacy evidence is never overwritten or
fabricated.

Ordinary `remove` verifies receipt and ownership hashes, selected configuration
and current numeric provider mappings before deleting the exact reviewed DNS
values, and rechecks the same identity bindings after Kubernetes cleanup,
immediately before provider deletion. A replacement cluster or same-label VM
with a different numeric ID blocks removal. The receipt remains available for
partial-removal retries; an exact provider ID returning 404 is accepted only
when its reviewed DNS values are already absent. Permission or transport failure
does not prove absence. It grants no ownership of excluded aliases or
records and no fresh deployment GO. See the [removal recovery procedure](deployment-operations.md#dns-ownership-recovery).
