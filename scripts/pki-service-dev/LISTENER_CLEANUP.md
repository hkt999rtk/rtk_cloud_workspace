# Dev retired listener bootstrap cleanup

This procedure removes the two retired listener bootstrap CA blocks and deletes
their two unmounted credential Secrets after managed renewal and CRL retirement
have passed. It targets only canonical dev; staging is excluded.

Run cleanup and its independent audit in separate private evidence directories:

```sh
python3 scripts/pki-service-dev/listener_cleanup.py --phase clean \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --certissuer-renewal CERTISSUER_RENEWAL \
  --controller-renewal CONTROLLER_RENEWAL \
  --renewal-verification RENEWAL_VERIFY --revocation REVOCATION \
  --publication PUBLICATION --retirement-verification RETIREMENT_VERIFY \
  --output CLEANUP
python3 scripts/pki-service-dev/listener_cleanup.py --phase verify \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --certissuer-renewal CERTISSUER_RENEWAL \
  --controller-renewal CONTROLLER_RENEWAL \
  --renewal-verification RENEWAL_VERIFY --revocation REVOCATION \
  --publication PUBLICATION --retirement-verification RETIREMENT_VERIFY \
  --output CLEANUP_VERIFY
```

The runner inventories every Deployment, StatefulSet, DaemonSet, Job, CronJob and
Pod in the dev namespace and requires zero references to either bootstrap Secret.
It removes exactly one certissuer legacy CA from certissuer's self-trust bundle
and exactly one controller legacy CA from the controller's self-trust bundle.
Peer-side copies must already be absent. Each Secret patch tests its observed
resource version and exact data field.

Before deletion, the complete Secret object is retained only in private evidence.
Deletion uses the observed UID as a Kubernetes API precondition. Both listeners
then restart without bootstrap credentials, retain their four managed successor
identities and final CRL, and rerun the Device mTLS/MQTT baseline.

If cleanup is interrupted, use `--phase resume-clean --failed FAILED_CLEANUP` in
a new evidence directory. It accepts an already completed patch or deletion only
when the failed evidence proves the exact prior/next object or deleted UID. It
otherwise refuses drift and does not recreate credentials.
