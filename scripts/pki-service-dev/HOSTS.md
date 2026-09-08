# Dev managed server adoption

Run `hosts.py` after the Root/intermediate phases have passed. Use the existing
Root/intermediate evidence directories and a **new private output directory per
phase**. All commands target dev only. No image build is needed when both listeners
already run the qualified managed-host image. Read the active design's managed
server rollout contract before applying these phases.

```sh
python3 scripts/pki-service-dev/hosts.py --phase prepare --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --output HOST_PREPARATION
python3 scripts/pki-service-dev/hosts.py --phase callers --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared HOST_PREPARATION --output CALLER_TRUST
python3 scripts/pki-service-dev/hosts.py --phase certissuer --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared HOST_PREPARATION --callers CALLER_TRUST --output ISSUER_ADOPTION
python3 scripts/pki-service-dev/hosts.py --phase controller --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared HOST_PREPARATION --callers CALLER_TRUST --output CONTROLLER_ADOPTION
python3 scripts/pki-service-dev/hosts.py --phase canary --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared HOST_PREPARATION --output FACTORY_CANARY
python3 scripts/pki-service-dev/hosts.py --phase verify --authority ROOT_EVIDENCE \
  --intermediate INTERMEDIATE_EVIDENCE --prepared HOST_PREPARATION --output FINAL_AUDIT
```

Preparation grants only the exact intermediate's server signing policy to the
existing certissuer OpenBao role. It enables governed server issuance/renewal for
the two approved server names and selected management clients. Device issuance
settings and the independent Service-client signer policy remain separate.
Server mode requires `CERT_ISSUER_MAX_TTL_DAYS=365`; the legacy 1,095-day default
is incompatible and must not be carried into this mode.

Each host gets a 10 GiB retained dev PVC and a temporary seed pod using its current
image. OpenSSL generates the P-256 key and CSR there. Only the CSR leaves the pod;
normal authenticated HTTP produces the registered leaf/chain, verified against
the active Service Root, hostname and CSR public key. The public chain is written
back to the PVC. Raw responses, previous objects and pod/PVC IDs are saved privately.
Never copy out the key, recreate a pending request, or regenerate a failed seed.

The callers phase adds the Root to five controller caller trust bundles and both
factory/Account Manager issuer trust bundles. Both issuer URLs use the approved
`.svc` DNS name. Callers restart to load these bundles before either server changes.
Initial management identities remain bootstrap credentials; this does not deploy
managed Account Manager egress. Configured bootstrap CA source files remain intact.

Each adoption phase deletes only its recorded seed pod using Kubernetes UID and
resource-version preconditions, then switches that listener to its dedicated PVC
and existing managed-host implementation. Recreate and one replica prevent two
owners. `fsGroupChangePolicy: OnRootMismatch` preserves private subdirectory/file
permissions on reattachment. Once the actual served fingerprint matches the
registered leaf and managed state exists, seed files are removed. The listener
restarts, preserving its private state hash and serving the same certificate.

Desired objects and per-listener settings are persisted in the existing private
`dev/pki/controller-bootstrap/rollout` directory. The scoped renderer consumes the
saved image/settings and complete volume configuration; full platform provisioning
must reconcile these overlays explicitly. Runtime and persisted state are checked
at final audit. Existing runtime database identities are preserved; this rollout
does not qualify database least privilege or provider/hardware custody.

A failure retains its report and partial state. Do not repeat a phase to bypass
an uncertain mutation. Inspect recorded objects/UIDs, stable issuance request,
provider/registry receipt and PVC before a narrowly scoped recovery. Pod readiness
alone is not a completed phase. Do not delete the private PVC on failure. Finished
seed pods are removed during adoption; failed preparation may leave an owned seed
pod that needs deliberate reconciliation before cleanup.

Evidence covers server issuance, real served certificates, authenticated replay,
human assertion enforcement, restart without seed and Device baseline. The short
MQTT baseline explicitly includes ACL/QoS1 roundtrip after connect readiness.
Natural scheduled renewal, server revocation/stream cutoff, durable CRL adoption,
managed clients and matched backup/recovery remain separate acceptance work.

The canary phase creates one new dev Device through the actual factory service
with a one-device production run valid for one hour. It verifies the active
Product issuer, matching key, Device mTLS and MQTT ACL/QoS1. Its identified Device
and protected credentials remain for later dev checks; it does not alter the
existing baseline Device. The final audit also performs read-only authenticated
CRL GETs from all four consumer pods using their mounted trust/identity files.
These requests verify transport; they never manufacture installation receipts.

If the first preparation fails before any server signing claim (for example,
configuration rejects the TTL), first repair that exact configuration using a
resource-version-guarded change. Then `--phase resume-prepare --prepared
FAILED_PREPARATION ... --output NEW_RECOVERY` requires the saved PVC/pod UID,
unchanged CSR and zero registry signing claims before using the original request.
It never regenerates that key, grants policy again or replaces the failed report.
A pending/succeeded claim stops this narrow recovery and needs the existing
issuance reconciliation workflow. Use the successful recovery directory as
`HOST_PREPARATION` in subsequent phases.


The state-directory check accepts `0700` or `02700`: inherited setgid preserves
the PVC group without granting group access. The state file must remain `0600`;
`0770`, `02770` and group-readable files fail. If the original certissuer adoption
stopped after serving its managed certificate but before seed removal, the narrow
`--phase resume-certissuer --adoption FAILED_ADOPTION --prepared HOST_PREPARATION
... --output NEW_RECOVERY` verifies the unchanged live template/PVC and existing
seed, then finishes seed removal and restart. It never issues another leaf or
changes the managed key. Keep the original failed report alongside recovery.

If controller adoption already removed the seed and restarted, but a later probe
failed, `--phase resume-controller --adoption FAILED_ADOPTION ...` requires the
unchanged Deployment template, original PVC UID, absent seed and the previously
observed private state hash. It rechecks the registered serving certificate,
restarts without seed again, verifies unchanged state and runs Device mTLS/MQTT.
It never replays preparation or issuance. Keep the failed probe report even if
fresh connections pass; an unreproduced transport failure is not an explained
root cause or an uninterrupted successful run.

The provider audit reads the certificate issuer Deployment's actual service
account (`certissuer-pki` in this dev deployment); the Deployment name is not its
service account name. It mints a short-lived workload JWT, tests the existing
OpenBao role's exact capabilities and revokes the resulting audit token. A failed
read-only final audit may be rerun with `verify` into a new evidence directory
after its cause is corrected; retain the original failed report.
