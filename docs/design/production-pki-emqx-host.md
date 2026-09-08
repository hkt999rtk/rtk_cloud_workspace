# Managed EMQX host identity

## Authority and responsibility

The source of truth is `repos/rtk_cloud_contracts_doc/platform_pki.md` sections
3 and 10: MQTT server TLS uses a public CA or dedicated MQTT Root; leaf keys are
generated at the subject boundary. This clarification changes no trust domain.
For the private-CA deployment, EMQX terminates MQTT TLS and uses a serverAuth-only
leaf under the dedicated `mqtt` domain. Its local supervisor owns protected
leaf key/CSR state and renewal. Neither holds a Device/Product CA private key.
`pkibroker` is an outbound session-management worker, not a TLS listener.

## Fixed implementation checklist (four items)

Completed 4/4: documentation in workspace `95bbc11`, implementation and
verification in Video Cloud `0387086`. Full tests, race checks, vet and the
PKCS#11-enabled release check passed. Disposable EMQX 5.9.0 verified actual
certificate replacement and MQTT session termination. The acceptance boundaries
below remain; this is not a deployed cluster or recovery qualification.

1. Clarify responsibilities and commit this plan before implementation.
2. Add a host supervisor using the existing durable server identity manager,
   an independent MQTT root/name policy and a separate management mTLS credential
   for the renewal issuer. Import a registered initial chain/key once.
3. Install private local certificate files and supervise EMQX foreground.
   Stop the owned broker process before switching identity. Stop on registry/CRL
   denial; retry only after current identity verification. Restart uses durable
   state and never silently reseeds it. Cancel/shutdown stops the owned process.
4. Add lifecycle, process, renewal/restart and denial tests, deployment example
   and runbook, then commit locally.

## Runtime contract

This slice supports a single native EMQX 5.9 foreground node under a dedicated
systemd unit and the same OS account as its supervisor. It does not take control
of an already running Docker or independently managed broker. The unit owns the
whole control group so a supervisor crash cannot leave the broker running.
Stop-before-start replaces the full process and closes all connections, including
non-MQTT listeners on this dedicated node. Renewal therefore causes an outage.
Cluster orchestration and zero-downtime rolling replacement remain separate work.

The supervisor pins exact MQTT DNS names and the MQTT root independently of the
Service root used to verify the renewal endpoint. Leaf private keys stay in a
0700 local state directory and 0600 files readable by the EMQX account. They are
never uploaded to the management API. Renewal uses current server-key proof and
a separate authorized management client certificate. No cloud/product key is
copied to the host. Public-CA MQTT remains a separate deployment option.

A periodic local registry check bounds trust-loss detection; process shutdown
has a bounded termination deadline and escalates to kill. No successful reload,
readiness or CRL acknowledgment is inferred merely from writing files or starting
a process. The existing consumer protocol and deployed readiness acceptance remain
separate. Existing plaintext/WebSocket listeners and authentication must be
explicitly configured by the operator; this milestone controls the default SSL
listener's server certificate only.

## Acceptance boundaries

Local fixture tests must prove durable renewal replay, startup without seed files,
stop-before-replacement, retry after failed start, revoked/restored-state denial
and owned-process cleanup. Pin the upstream foreground/config interface used in
the runbook. Real EMQX compatibility, deployment availability, client reconnect
behavior, custody, matched backup/provider restoration and post-backup security
history reconciliation must be reported separately from fixture evidence.

The original five broader acceptance milestones remain unchanged:
legacy migration/device replacement; trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification.
