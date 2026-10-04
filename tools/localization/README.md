# RTK localization tool

English is the source locale. The tool stores SHA-256 source checksums and a fingerprint of the source, locale, policy version, and glossary with every translation. A glossary or policy change makes an existing translation stale even when its English text is unchanged. `translate` writes drafts only; approved translations remain a Git-reviewed decision.

```sh
node tools/localization/localization.mjs check --catalog path/to/catalog.json --translations path/to/translations
OPENAI_API_KEY=... node tools/localization/localization.mjs translate --catalog path/to/catalog.json --translations path/to/translations --locale zh-TW
```

`status` reports progress; `check` fails unless every configured locale has an approved, checksum-current value. Both are intentionally offline. Do not run `translate` from ordinary PR CI.

Translation requests use each entry's caller context and the locale-specific
glossary. Keep Taiwan and Mainland terminology distinct, preserve executable
identifiers and technical units, and review negation, authorization boundaries,
and completion conditions against English before approving a draft. A current
checksum establishes source alignment; it does not establish language quality.

Cloud Admin also runs terminology and identifier checks in
`web/localization/quality.mjs`. Its UI/API catalog and Developer Docs Markdown,
navigation, and diagrams are separate authoring sources. Review the complete
user flow across both sources when changing terminology.
