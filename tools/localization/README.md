# RTK localization tool

English is the source locale. The tool stores SHA-256 checksums with every translation so only changed strings are sent to the translation provider. `translate` writes drafts only; approved translations remain a Git-reviewed decision.

```sh
node tools/localization/localization.mjs check --catalog path/to/catalog.json --translations path/to/translations
OPENAI_API_KEY=... node tools/localization/localization.mjs translate --catalog path/to/catalog.json --translations path/to/translations --locale zh-TW
```

`status` reports progress; `check` fails unless every configured locale has an approved, checksum-current value. Both are intentionally offline. Do not run `translate` from ordinary PR CI.
