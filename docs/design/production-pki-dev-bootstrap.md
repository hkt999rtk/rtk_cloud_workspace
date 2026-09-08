# Dev controller bootstrap

This preparation supports the legacy migration canary on `video-cloud-dev`.
It does not enable staging/production or create Device, Brand or Product CAs.
Human MFA remains optional and disabled; authenticated identities, live roles
and independent approvals remain required.

## Prepare dedicated credentials

From the workspace's `scripts/go` directory:

```sh
GOWORK=off go run ./rtk-cloud pki-dev-prepare --environment dev
```

The command uses the canonical SecretStore base (`RTK_CLOUD_CONFIG_ROOT`, or
`~/.config/rtk_cloud`) and writes only `dev/pki/controller-bootstrap`. The
directory is mode 0700; every material file is mode 0600. A repeat validates and
reuses the complete bundle. Partial, expired, mismatched, unsafe-permission or
symlinked material fails for review; it is never silently replaced. Generation
is serialized and the validated directory is installed atomically. An interrupted
process may leave a `.lock` directory; establish that the process ended before
removing that lock. No live deployment, database, OpenBao policy or operator
configuration is modified by this command.

| File | Intended use |
| --- | --- |
| `database-password` | New dedicated dev controller database login; no login is created yet |
| `jwt-access.key`, `jwt-access.pub` | Account Manager RS256 access/assertion signer; only the public key goes to the controller |
| `jwt-refresh.key`, `jwt-refresh.pub` | Separate Account Manager RS256 refresh signer; never mounted by the controller |
| `management-ca.crt` | Independent dev management TLS trust, not device or OpenBao trust |
| `controller.crt`, `controller.key` | Server-only identity for `pki-controller.video-cloud-dev-video-cloud.svc` and its cluster-local FQDN |
| `account-manager.crt`, `account-manager.key` | Client-only management identity with CN `account-manager` |

This is the static dev management transport needed to bootstrap the controller,
separate from governed Device/Brand/Product authority. Its CA private key exists
only during generation and is not retained. The two leaf certificates last
90 days. Later managed Service identity adoption and renewal remain separate
work; do not treat these static bootstrap identities as production custody or
renewal qualification. Existing certissuer, MQTT and OpenBao CA material is not
read, replaced or repurposed.

## Bind and roll out separately

Use the existing phase renderer in
`repos/rtk_video_cloud/deploy/pki/render-staging-controller.py` with explicit
`--environment dev`. Bind the prepared material only after checking existing
objects and recording their resource versions. Create absent objects; never
overwrite conflicting Secrets or silently rotate an already deployed identity.

- `pki-controller-tls`: `tls.crt` from `controller.crt`, `tls.key` from
  `controller.key`, and `ca.crt` from `management-ca.crt`.
- `pki-account-manager-public-key`: `public-key.pem` from `jwt-access.pub`.
- Account Manager needs its own protected client identity and both JWT key pairs,
  with explicit file mounts and the corresponding `JWT_*_KEY_PATH` settings.
  Set `JWT_SIGNER_PROVIDER=pem`, the dev controller HTTPS URL, management client
  cert/key/CA paths, `PKI_ENVIRONMENT=dev` and `PKI_REQUIRE_USER_MFA=false`.
- `pki-openbao-transport-ca` must come from the verified existing dev OpenBao
  transport CA, not this management bundle.
- Create the dedicated runtime database login with the prepared password and
  only the reviewed controller group membership. Keep migration-owner credentials
  in the migration Job Secret. Verify effective permissions with the runtime login.
- Configure exact OpenBao service-account/namespace/audience binding and reviewed
  issuer-scoped policies; do not attach legacy root or broad signing policies.

The command creates neither the database login nor Kubernetes objects. Validate
the complete set of mounts, credentials, pinned images and persisted dev settings
before the scoped rollout. Account Manager's RS256 cutover invalidates existing
HS256 access and refresh tokens: plan fresh dev logins and test all affected token
consumers. Include Account Manager's migration-alias fix in its migration image.
Retain database backup/rehearsal evidence and apply migration, grants and runtime
phases in order. Roles are assigned through authenticated administration to actual
operators; prepared signer keys are never used to manufacture their approvals.

The current dev database and credential-binding execution is recorded in the
[dev preflight](production-pki-legacy-dev-preflight.md). `PKI_CONTROLLER_IMAGE` in
the dev operator directory holds the verified digest for this separate overlay;
read it explicitly as the renderer's `--image` argument. It does not replace the
existing API's `LKE_VIDEO_CLOUD_IMAGE` or trigger a platform-wide update. Persisted
phase manifests and Job evidence live beside the prepared material in `rollout/`.

The dev OpenBao Kubernetes auth mount and exact `pki-controller-dev` role are now
installed and live-tested. The role deliberately has no issuer policies and no
default policy. Add only reviewed per-issuer policies after reserving the actual
issuer; do not reuse legacy AppRole privileges to make provisioning succeed.
The existing controller service account has automatic token mounting disabled;
retain the renderer's projected `audience=openbao` token volume for the workload.

Before rendering the controller, finish the actual trust-consumer CN inventory
and provision each required consumer's independent management identity. The
prepared `account-manager` client is for human-request proxying; it does not
authorize fabricated installation acknowledgments for the API, broker or other
workloads. Consumer credentials are not generated by `pki-dev-prepare` yet.
