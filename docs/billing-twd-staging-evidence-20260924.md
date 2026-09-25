# TWD Billing staging qualification — 2026-09-24

Scope: `video-cloud-staging` only. This release changed Billing, Account
Manager and Cloud Admin in place. Video Cloud, Logger, environment Root CA,
OpenBao/HSM material, credentials and persistent volumes were preserved.
Production was not changed. The protected-environment preflight recorded a GO
for this three-service currency rollout after exact-image credential checks,
SecretStore verification, Account Manager→certissuer mTLS, a no-growth Linode
provider projection (`current=54`, `additional_required=0`, `projected=54`),
storage/DNS canaries, and a read-only data inventory. The catalog's recorded
limit of 20 is below current usage; this rollout requested no new provider
services. The separate Product Device Root pin is incomplete, so no
Product-scoped device was created.

## Release provenance and schema

| Service | Merged source | Live staging image digest |
| --- | --- | --- |
| Billing | [`f1614c0`](https://github.com/hkt999rtk/rtk_billing/commit/f1614c0bb59d46d382f41ec7836cf7c5bc4a4067) | `ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:c308cb8987d18dde893178e06e31301178683c8e2fe91c872f1816bcd3affcfa` |
| Account Manager | [`a8f6bf5`](https://github.com/hkt999rtk/rtk_account_manager/commit/a8f6bf5b5de9bdedc380041d66d1ae1462631efa) | `ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:a04ce671d0c15ee46aa961d0ad4e765873dc0d4128edafa11521a9184512379a` |
| Cloud Admin | [`62e10df`](https://github.com/hkt999rtk/rtk_cloud_admin/commit/62e10df9a3a9634d14d8e3c9e674ccda8a601c29) | `ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:32374368d4949127134d23d3a207d6c59a214ba0ea7885c286cd5bf198c2fe74` |

All three CI-published digests passed a complete linux/amd64 pull with staging
credentials. [Workspace PR #496](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/496)
merged at `c3ffd0a3bc97799944ad2dafc28bb63951ef7dc9`, with required CI and
the final clean local pre-PR gate passing. Billing migrations 060/061 and
Account Manager migration 085 are recorded in the live staging databases.
Fresh and upgraded Billing schemas matched in the isolated PostgreSQL rehearsal;
the currency change adds no business table or non-TWD settlement permission.

Before mutation, custom PostgreSQL archives of both staging databases were
saved locally with mode 0600 and parsed by PostgreSQL 16 `pg_restore`.
Billing SHA-256: `0eb7f44bb102c5e458b3dcb084ad5d3df029f4ad41944831c94f0686276de93f`;
Account Manager SHA-256: `3289f4c34c7ada8cba0c6ff462e86f74af9a4b4e97b581f87f4e3aa5ab3bddbb`.
Both archives restored into an isolated local PostgreSQL server; AM migration
085 applied there before live rollout. Three pre-cutover invoice rows and their
three line rows matched the restored original data after live migration and
qualification. The new nullable `product_id` column was excluded only from the
old-line comparison; all three old values in that column are null. With the
same ordered JSON comparison on backup and live data, invoice hashes both equal
`5993716f81ad40a53295947b3f39a5f4` and original-column line hashes both
equal `f8389e9b069af370a86725a8f87c3bf3`.

## Active TWD rate and invoice

The read-only pre-activation check found zero non-TWD accounts, pricing
versions, periods or invoices, and zero issued invoices or periods at/after the
reviewed cutover. The affected staging Cloud had an active TWD account and
responsibility from 2026-09-02 11:07 UTC. Through the Billing internal API, a
`qualification` TWD rate version `2026092401` was created and activated from
2026-09-02 11:00 UTC. The prior active version retired exactly at that point.
The new version preserves the existing `staging_units` rate (NT$2/unit) and
adds all four MQTT metrics: `publish_count` and `delivery_count` at NT$32 per
million requests (minor price 32, scale 6); `publish_bytes` and
`delivery_bytes` at zero per byte. No other draft retail service was activated.

The existing 12 MQTT usage facts produced a reviewed staging invoice for
2026-09-02 11:08 to 2026-09-19 00:00 UTC, using the current owner-authorized,
explicitly labeled `RTK Staging MQTT Qualification` billing profile. The four
lines contain 45 publishes, 5 deliveries, 18,454 published bytes and 2,110
delivered bytes. Each line references the new rate version. Integer invoice
rounding yields a TWD total of NT$0 for this small fixture; the presence of
four usage/rate lines, rather than the zero total alone, verifies pricing.
Invoice `INV-2026-000004` is settled. Repeating the same close call returned
the same invoice with `duplicate=true`. A current-owner Billing summary returned
HTTP 200 with TWD account, current period and latest invoice; the old invoices
remained unchanged.

After a deliberate restart of Billing API/payment worker/simulator, Account
Manager API/email/outbox workers and Cloud Admin, all seven Deployments were
1/1 ready on the exact digests above with zero container restarts. Public
Billing `/healthz`, Account Manager `/v1/health`, and Admin `/healthz` returned
HTTP 200. The TWD rate, four-line invoice and old-invoice comparisons persisted.
The staging SecretStore verification and Account Manager→certissuer mTLS check
passed again; the three affected namespaces had no Warning events.

## Qualification boundary

The maintained Console check passed social provider visibility, platform and
developer login, published chipset/Board assets, SDK release catalog, and
Billing reads/ownership (six checks). Its Test Lab read route returned 404
because the only existing staging Test Lab account had expired at 2026-09-24
06:11 UTC; no active Test Lab account was present. The checker correctly
returned a nonzero aggregate result. This limits full Console/Test Lab release
sign-off, but does not invalidate the independently observed TWD Billing
summary and MQTT invoice. A later Test Lab acceptance run needs a fresh account
and separate Product PKI qualification. Browser-only layout, external OAuth,
WebGL/video playback and production behavior were not claimed by this run.
