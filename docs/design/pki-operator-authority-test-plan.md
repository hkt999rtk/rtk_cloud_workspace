# Environment PKI operator authority: qualification

Status: accepted Dev operator policy (2026-09-29), requalified 2026-10-04.
Owner: rtk_cloud_workspace. Applies to: the configured Dev operator mode;
staging and production require their own bindings and qualification.
Normative authority: [Platform PKI](../../repos/rtk_cloud_contracts_doc/platform_pki.md)
§7. Deployment rules: [Deployment service identities](deployment-service-identities.md).

## Authority and evidence

One configured operator may perform all steps. The controller's exact
authorization row and transactional audit determine execution authority;
Loki is a searchable operational log. Missing or delayed Loki delivery never
creates authorization. Required checks follow:

| Case | Evidence required |
| --- | --- |
| Same configured operator | Authenticated Account Manager proxy and controller accept that environment's configured operator without a second `pki_admin` or human signature. |
| Exact request | Immutable parent, issuer, Service subjects, server DNS policy, request digest and signer reference remain bound through authorization, provision, signing/import and activation. Changed content is denied. |
| Durable authorization | PostgreSQL authorization and audit records commit transactionally and reject updates/deletes. Structured Loki delivery is operational evidence; it does not replace the authorization row. |
| Wrong operator/environment | Missing, disabled, mismatched or wrong-environment identity fails closed. No database edit or borrowed account can replace authorization. |
| Trust activation | Registry/provider lineage, full certificate chain, signed current CRLs and real consumer acknowledgments agree before activation. |
| Candidate preservation | Selected Account Manager and controller source retain the operator implementation; prior live success does not qualify a regressed release. |
| Private custody | Workload clients remain in their own managed state; public reports contain only IDs, digests, certificate fingerprints and outcomes. |

Controller integration and HTTP tests live in
`repos/rtk_video_cloud/internal/pki/`; proxy tests live in
`repos/rtk_account_manager/internal/api/`. Legacy distinct-role tests remain
applicable to environments without operator mode. They do not redefine Dev's
accepted authority.

## Dated Dev evidence

Dev completed its configured-operator cutover on 2026-09-29. On 2026-10-04,
the same configured operator continued the preserved public CertIssuer
Service-successor request through authorization, provision, Root signing,
import and activation. The Root and previous issuer trust were preserved.
Public hostname validation, anonymous client-certificate rejection and the
Account Manager owner's deliberately incomplete App request passed.
Detailed operation receipts, signed public manifests, exact live changes and
rollback values are environment-local qualification evidence; they are not
credentials or authorization for another environment.

The [Dev PKI runbook](../product-services-dev-pki.md) distinguishes public
transport qualification from issuance lifecycle, Product activation and
other feature acceptance.

## Existing requested operation

Preserve the operation ID, creator, request digest and history during migration.
The configured operator may explicitly continue it after exact-policy review.
Migration does not silently approve or execute an existing request.
