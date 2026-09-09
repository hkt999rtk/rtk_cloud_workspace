# Dev managed listener renewal

This run qualifies operator-requested early renewal for each managed listener.
It upgrades one listener at a time to a committed, immutable dev image, records
the current public client/server state and registry rows, writes a durable local
intent, then sends exactly one `SIGHUP` to PID 1. The process renews its managed
client first and its server identity second, using the replacement client for
the host request. No private key leaves the listener PVC.

Use successful managed-egress verification and fresh listener CRL qualification
as prerequisites. Run certissuer first, then pki-controller, with a new private
evidence directory for each phase:

```sh
python3 scripts/pki-service-dev/listener_renewal.py --phase certissuer-renew \
  --authority ROOT --intermediate INTERMEDIATE \
  --egress EGRESS_VERIFY --crl CRL_QUALIFICATION --image VERIFIED_DEV_IMAGE \
  --output CERTISSUER_RENEWAL
python3 scripts/pki-service-dev/listener_renewal.py --phase pki-controller-renew \
  --authority ROOT --intermediate INTERMEDIATE \
  --egress EGRESS_VERIFY --crl CRL_QUALIFICATION --image VERIFIED_DEV_IMAGE \
  --output CONTROLLER_RENEWAL
python3 scripts/pki-service-dev/listener_renewal.py --phase verify \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --image VERIFIED_DEV_IMAGE \
  --certissuer CERTISSUER_RENEWAL --controller CONTROLLER_RENEWAL \
  --output FINAL_AUDIT
```

Each phase requires one new client issuance and one new server issuance, changed
public keys and fingerprints, no pending request, the renewed server leaf on the
wire, persistence across a bootstrap-free restart, and the Device mTLS/MQTT
baseline. It updates only the selected dev listener image and its persisted image
pin. Staging is excluded.

Use the matching `resume-LISTENER-renew` phase with `--failed FAILED_EVIDENCE`
after an interrupted phase. If `renewal-intent.json` exists, it never sends
another signal and waits for the durable in-flight requests and normal retry
timer. If rollout completed before the intent was written, it first requires the
exact saved pod template plus unchanged client/host state and registry rows, then
writes the first intent and sends the one signal. Both paths refuse owner, PVC or
image drift.
