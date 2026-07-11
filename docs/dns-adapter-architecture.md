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

## Configuration and runtime

Tracked defaults live in `cloud_deploy/dns_adapters/<adapter>/`. Optional,
reviewed provider escape hatches live in
`cloud_env/<environment>/overrides/dns.env`. Zone IDs, API keys, AWS profile
sessions, change IDs, and credentials are never tracked configuration.

The generic plan is written to `runtime/resolved/dns-plan.json`. Normalized
state is written to `runtime/state/dns.env`. Provider-private state is written
to `runtime/dns/<adapter>/state.json`; it is ignored and must not be read by the
shared Kubernetes renderer or load tests.

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

The generated certbot hooks invoke the shared DNS command and contain no
vendor HTTP calls. Multiple validations for one `_acme-challenge` name are
submitted as one record set. Cleanup removes only the matching validation
value and preserves unrelated TXT values.

Public record ownership is recorded in ignored runtime state. Removal deletes
only records owned by the environment whose current values still equal the
recorded target. External drift stops removal instead of overwriting or
deleting operator-owned data.

## GoDaddy adapter

The workspace adapter directly implements GoDaddy record APIs; active
deployment code does not call the service submodule's `godaddy-dns` tool.
Credentials are resolved as `GODADDY_KEY` and `GODADDY_SECRET` in this
order: process environment, the environment runtime operator file, then
`~/.env`. This matches the LKE operator-secret fallback without copying
credentials into tracked environment config or resolved runtime evidence.
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
- Existing historical reports and the ignored `cloud_env/staging/lke` tree are
  evidence only and are not migrated or read.
