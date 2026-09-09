# Dev replaced listener leaf retirement

This procedure retires the two client leaves and two server leaves replaced by
the managed listener renewal. It targets only canonical dev. Private keys never
leave the existing listener PVCs, and staging is excluded.

Run each phase with a new private evidence directory. Supply the successful
renewal reports and final renewal audit to every phase:

```sh
python3 scripts/pki-service-dev/listener_retirement.py --phase revoke \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --certissuer-renewal CERTISSUER_RENEWAL \
  --controller-renewal CONTROLLER_RENEWAL \
  --renewal-verification RENEWAL_VERIFY --output REVOCATION
python3 scripts/pki-service-dev/listener_retirement.py --phase publish \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --certissuer-renewal CERTISSUER_RENEWAL \
  --controller-renewal CONTROLLER_RENEWAL \
  --renewal-verification RENEWAL_VERIFY --revocation REVOCATION \
  --output PUBLICATION
python3 scripts/pki-service-dev/listener_retirement.py --phase verify \
  --authority ROOT --intermediate INTERMEDIATE --egress EGRESS_VERIFY \
  --crl CRL_QUALIFICATION --certissuer-renewal CERTISSUER_RENEWAL \
  --controller-renewal CONTROLLER_RENEWAL \
  --renewal-verification RENEWAL_VERIFY --revocation REVOCATION \
  --publication PUBLICATION --output FINAL_AUDIT
```

`revoke` binds all four old fingerprints to their exact successful replacement
records, requires the current successors on the PVC and wire, and commits online
registry denial without changing the provider CRL. Each revocation uses a stable
fingerprint and reason, so exact replay returns the existing record.

`publish` invokes the matching Service-client or server provider revocation for
each leaf. After each cumulative CRL update it waits for both listener receipts
and verifies the PVC-installed CRL before making the next authenticated request.
This preserves the controller's fail-closed CRL floor. The final CRL must retain
all earlier entries and contain all four old serials. Finalization requires both
consumer receipts and binds every revocation to the final digest.

`verify` rechecks the final CRL, receipts, revoked rows and admitted successors,
then restarts both listeners and reruns the Device mTLS/MQTT baseline. The old
private keys were destroyed by the preceding renewal, so this retrospective run
cannot establish cutoff of a connection opened with an old key. A future renewal
acceptance must hold that session before rotation and revocation.
