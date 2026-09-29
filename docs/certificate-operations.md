# Certificate operations

## Scope and operator workflow

The Go command `deployment certificate-check` performs a read-only inspection of
one environment. It does not enroll, renew, revoke, repair permissions, create a
Secret, change a deployment or install a timer. Scheduling is intentionally a
separate decision. Admin/operator invokes it when an inspection is needed.

The environment inventory identifies expected certificates, independent trust
anchors and their current sources. This prevents a missing certificate from
silently disappearing from an otherwise green report. Repository baseline
entries are in `cloud_deploy/certificate-check.json`; environment-specific
entries/overrides are in `cloud_env/<environment>/certificate-check.json`.
Overrides match by ID. No private material belongs in either file.

Sources:

- `file`: PEM certificate/key under the selected local SecretStore.
- `deployment`: `pki/services/<service>/identity.json`, the saved initial identity.
- `secret`: the current named Kubernetes Secret in the selected stack namespace.
- `managed`: each running Pod selected by the inventory; invoke the Go
  `pkitrust inspect-identity` helper inside its identity-owner container. Only
  public certificate material and validation results leave the container.
  The helper reads current state without creating/locking/updating it.

A managed current leaf may legitimately differ from its initial seed. Compare
current validity, subject, purpose and pinned trust, not equality to that seed.
An initial record is provenance once an explicitly configured managed source
supersedes it; its expiry alone is not a runtime certificate failure.

## Command and results

```sh
go build -o /tmp/rtk-cloud ./scripts/go/rtk-cloud
/tmp/rtk-cloud deployment certificate-check --environment dev
/tmp/rtk-cloud deployment certificate-check --environment staging --format json
```

Use `--local-only` to inspect local sources without Kubernetes access; it reports
runtime targets as unchecked and cannot return a fully healthy result. Feature flags resolve from tracked environment/adapter configuration and the
selected environment's operator files. Shell overrides are not used. No cluster
context is inferred from the shell: the selected environment kubeconfig is used
explicitly. A bounded request timeout applies; API/access failures are UNKNOWN,
not evidence that a certificate is absent or healthy.

Default warning threshold: 7 days. Default critical threshold: 3 days. Both are
options (`--warn-days`, `--critical-days`); scheduling is not inferred from them.
The earliest expiry in the verified chain determines the warning. Reports
include source, subject, fingerprint, UTC expiry, remaining days and findings.
No PEM, private key, bootstrap token, kubeconfig or raw Kubernetes errors appear
in the report. JSON is suitable for a future operator-chosen scheduler.

Exit codes: 0 = checked targets healthy (or explicitly disabled); 1 = warnings;
2 = critical failures; 3 = incomplete checks/configuration errors. A report can
contain both critical and unknown rows; inspect all rows, not just the exit code.
When using `go run`, Go wraps a nonzero child exit; build the binary for exact codes.

Checks cover key/certificate match, exact expected subject and EKU, DNS where
configured, chain signatures/validity against an independent configured root,
root fingerprint where configured, expiry of every chain member, missing/partial
state, pending enrollment, and optional signed CRL freshness/revocation. Static
Secret targets can require agreement with a local initial credential. Managed
sources never overwrite or compare key equality with an old seed.

X.509 validity does not prove current registry authorization or that a process
has reloaded an on-disk certificate. The report explicitly states revocation as
unchecked when no CRL is configured. A certificate inspection must not be
reported as successful end-to-end mTLS or registry admission.

## Inventory and lifecycle changes

The inventory schema and a managed-owner example are documented alongside the
implementation below. During a new environment deployment, select each enabled
service's current source and independent root before considering handover
complete. Add every managed owner instance and its server identity as separate
entries. Do not retain a baseline Secret as the claimed current source after
migrating its owner to durable managed state.

For an abnormal result, preserve the current identity and pending request. Check
environment/source selection, root pins, registry status and owner readiness.
Renewal, revocation and rotation use their existing explicit lifecycle workflows;
rerun this check afterward. Never delete local identity state to make a check
pass. The deployment identity policy is
[deployment-service-identities.md](design/deployment-service-identities.md).

### Inventory fields

| Field | Meaning |
| --- | --- |
| `version` | Inventory version, currently `1` |
| `targets[].id` | Stable unique target ID; environment override replaces the entire matching entry |
| `source` | `file`, `deployment`, `secret`, or `managed` |
| `enabled_by` / `enabled_by_any` / `disabled` | Optional environment feature flag / any enabled flag / explicit topology exclusion; excluded entries remain visible as DISABLED |
| `subject`, `purpose`, `dns` | Exact expected common name, `client`/`server`/`ca`, optional server DNS |
| `root_file`, `root_sha256` | Independent SecretStore-relative PEM trust bundle and optional exact single-root pin |
| `service_root` | Obtain root path/pin from this environment's `pki/services/issuer.json` instead |
| `crl_file` | Optional independent PEM CRL bundle covering every non-root member of the verified chain |
| `cert_file`, `key_file` | SecretStore-relative files for a `file` target; CA targets need no key |
| `record_file` | SecretStore-relative initial record for a `deployment` target |
| `namespace_suffix`, `secret`, `cert_key`, `key_key` | Current Secret source in `<stack>-<namespace_suffix>` |
| `compare_to` | Optional local PEM/initial-record comparison for a static current credential |
| `selector`, `container`, `state_file` | Managed-owner Pod selector, owner container, and absolute current state path |
| `superseded_by` | Initial record's managed current target ID; only verified current state makes time-only initial expiry informational |

Example **topology override**, for an environment already migrated to the
managed Account Manager owner. Confirm the actual container/selector/state path
from that environment's deployment before saving this as its
`cloud_env/<environment>/certificate-check.json`:

```json
{
  "version": 1,
  "targets": [
    {"id": "account-manager-local", "disabled": true},
    {
      "id": "account-manager-current",
      "source": "managed",
      "namespace_suffix": "account-manager",
      "selector": "app.kubernetes.io/name=account-manager",
      "container": "pkimanagement",
      "state_file": "/var/lib/account-pki/private/identity.json",
      "subject": "service:account-manager",
      "purpose": "client",
      "service_root": true
    },
    {
      "id": "account-manager-initial",
      "source": "deployment",
      "record_file": "pki/services/account-manager/identity.json",
      "subject": "service:account-manager",
      "purpose": "client",
      "service_root": true,
      "superseded_by": "account-manager-current"
    }
  ]
}
```

Add a separate managed target for the Account Manager server-auth state and its
own independent trust domain; a client target does not cover that listener.
Likewise add PKI Controller, Certissuer, Factory Enroll, Video Cloud API,
Log Ingester, PKI Broker, EMQX and OpenBao managed owners when selected. The same
schema supports each catalogue identity, CA trust certificate and listener.
Legacy `account-manager` subjects need an explicit matching inventory override;
the checker does not silently accept alternative service subjects.

The shared inventory covers baseline Account Manager/Factory Enroll/Certissuer,
MQTT and OpenBao local/current transport credentials, plus enabled MQTT, Shadow,
WebRTC, Video Storage, Logger and OTA initial/current registration identities.
It applies to dev, staging and prod. Live managed owner coverage is additionally
checked in the video-cloud, account-manager and secrets namespaces, plus every
namespace named by enabled inventory entries. Any observed `_IDENTITY_STATE`
container setting without a successful corresponding inspection is UNKNOWN.
Namespaces outside this scope and certificates not exposed through those owner
settings require explicit inventory entries. The report is never a discovery
claim for arbitrary external certificates or user/device certificates in OpenBao.

Managed inspection requires the image containing `/usr/local/bin/pkitrust`
(`inspect-identity` support), read access to the owner's state, and Kubernetes
permission to list Pods and execute that fixed helper. An older image or denied
access yields UNKNOWN. Do not replace this with `cat` of a private state file.
The command uses only `get` and the fixed inspection helper; Kubernetes `exec`
permissions are broader than the operation, so grant/use them deliberately.

## Delivery verification

- Tooling tests exercise expiry boundaries, independent trust/key/purpose checks,
  signed revocation, missing/runtime failures, unmanaged inventory gaps, environment
  overrides, output redaction and unchanged local files/permissions.
- Runtime tests prove public-only inspection, missing-state non-creation,
  pending-state visibility, key mismatch detection and unchanged owner bytes.
- Live environment readiness and registry authorization are separate rollout
  evidence; a fixture test does not assert that an existing image has this helper.
