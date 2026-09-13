# Localization workflow

English is the source locale for the Portal and Admin Console. Traditional and Simplified Chinese are checked-in, reviewable release artifacts; production pages never wait for a translation API response.

## Commands

Run the shared validation against an approved catalog:

```sh
node tools/localization/localization.mjs check \
  --catalog repos/rtk_cloud_admin/web/localization/catalog.json \
  --translations repos/rtk_cloud_admin/web/localization/translations
```

Generate only missing or checksum-stale drafts locally. The command requires an explicit API key and never marks output approved:

```sh
OPENAI_API_KEY=... node tools/localization/localization.mjs translate \
  --catalog repos/rtk_cloud_admin/web/localization/catalog.json \
  --translations repos/rtk_cloud_admin/web/localization/translations \
  --locale zh-TW
```

Review the generated JSON in a normal pull request. Human reviewers change `status` to `approved` only after verifying product terminology, parameters, destructive actions, billing, permissions, and consent text.

The Portal exposes its source catalog without sending content to a provider:

```sh
(cd repos/rtk_cloud_frontend && GOWORK=off go run ./cmd/localization-catalog)
```

This output has the same checksum field order as the shared tool. Existing Portal Catalog translations remain the approved source during the migration; feature bodies and hand-authored documentation are deliberately excluded from automatic translation.

## Policy

- A checksum includes the key, English text, context, and interpolation tokens.
- A source, context, or parameter change invalidates only that translation entry.
- The translation command writes `draft` entries only and is never run by ordinary PR CI.
- `check` is offline and rejects missing, stale, draft, or malformed approved entries.
- Customer data, API payloads, device data, and runtime logs must not be sent to the translation provider.
