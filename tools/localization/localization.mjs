#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { basename, extname, join, resolve } from 'node:path';

const REQUIRED_LOCALES = ['en', 'zh-TW', 'zh-CN'];

export function sourceHash(entry) {
  const canonical = JSON.stringify({
    key: entry.key,
    source: entry.source,
    context: entry.context || '',
    placeholders: [...(entry.placeholders || [])].sort(),
  });
  return createHash('sha256').update(canonical).digest('hex');
}

export function fingerprint(entry, locale, policyVersion) {
  return createHash('sha256').update(JSON.stringify({
    key: entry.key,
    sourceHash: sourceHash(entry),
    locale,
    policyVersion,
  })).digest('hex');
}

export function placeholders(text) {
  return [...new Set(String(text).match(/{{\s*[-\w.]+\s*}}/g) || [])].sort();
}

export function validateCatalog(catalog) {
  const errors = [];
  if (!catalog || catalog.schemaVersion !== 1) errors.push('catalog schemaVersion must be 1');
  if (catalog?.sourceLocale !== 'en') errors.push('sourceLocale must be en');
  if (!Array.isArray(catalog?.strings)) errors.push('strings must be an array');
  const seen = new Set();
  for (const entry of catalog?.strings || []) {
    if (!entry.key || !entry.source) errors.push('each string needs key and source');
    if (seen.has(entry.key)) errors.push(`duplicate key: ${entry.key}`);
    seen.add(entry.key);
    const declared = [...(entry.placeholders || [])].sort();
    const found = placeholders(entry.source);
    if (JSON.stringify(declared) !== JSON.stringify(found)) errors.push(`${entry.key}: placeholders do not match source`);
  }
  return errors;
}

export function validateTranslations(catalog, translation) {
  const errors = [];
  if (translation?.schemaVersion !== 1) errors.push('translation schemaVersion must be 1');
  if (!REQUIRED_LOCALES.includes(translation?.locale) || translation.locale === 'en') errors.push('translation locale must be zh-TW or zh-CN');
  const byKey = new Map((catalog.strings || []).map(entry => [entry.key, entry]));
  for (const [key, value] of Object.entries(translation?.entries || {})) {
    const entry = byKey.get(key);
    if (!entry) { errors.push(`${key}: no source string`); continue; }
    if (!['draft', 'approved'].includes(value.status)) errors.push(`${key}: invalid status`);
    if (value.sourceHash !== sourceHash(entry)) errors.push(`${key}: source checksum is stale`);
    if (JSON.stringify(placeholders(value.text)) !== JSON.stringify(placeholders(entry.source))) errors.push(`${key}: placeholders do not match source`);
  }
  return errors;
}

async function readJSON(path) { return JSON.parse(await readFile(path, 'utf8')); }
async function writeJSON(path, data) { await mkdir(resolve(path, '..'), { recursive: true }); await writeFile(path, `${JSON.stringify(data, null, 2)}\n`); }

function option(args, name) { const index = args.indexOf(name); return index === -1 ? '' : args[index + 1] || ''; }

async function translationFiles(directory) {
  return (await readdir(directory)).filter(file => extname(file) === '.json').map(file => join(directory, file));
}

async function status(catalogPath, translationsDir, requireApproved = false) {
  const catalog = await readJSON(catalogPath);
  const catalogErrors = validateCatalog(catalog);
  const files = await translationFiles(translationsDir);
  const translations = await Promise.all(files.map(readJSON));
  const errors = [...catalogErrors];
  for (const translation of translations) errors.push(...validateTranslations(catalog, translation).map(error => `${translation.locale}: ${error}`));
  const summary = Object.fromEntries(REQUIRED_LOCALES.slice(1).map(locale => [locale, { approved: 0, draft: 0, missing: catalog.strings.length, stale: 0 }]));
  for (const translation of translations) {
    if (!summary[translation.locale]) continue;
    for (const entry of catalog.strings) {
      const value = translation.entries?.[entry.key];
      if (!value) continue;
      summary[translation.locale].missing -= 1;
      if (value.sourceHash !== sourceHash(entry)) summary[translation.locale].stale += 1;
      if (value.status === 'approved') summary[translation.locale].approved += 1;
      if (value.status === 'draft') summary[translation.locale].draft += 1;
    }
  }
  if (requireApproved) {
    for (const [locale, values] of Object.entries(summary)) {
      if (values.missing || values.stale || values.draft || values.approved !== catalog.strings.length) {
        errors.push(`${locale}: translations are not fully approved`);
      }
    }
  }
  console.log(JSON.stringify({ catalog: basename(catalogPath), strings: catalog.strings.length, summary, errors }, null, 2));
  if (errors.length) process.exitCode = 1;
}

async function translate(catalogPath, translationsDir, locale) {
  if (!['zh-TW', 'zh-CN'].includes(locale)) throw new Error('--locale must be zh-TW or zh-CN');
  const apiKey = process.env.OPENAI_API_KEY;
  if (!apiKey) throw new Error('OPENAI_API_KEY is required for translate');
  const catalog = await readJSON(catalogPath);
  const errors = validateCatalog(catalog);
  if (errors.length) throw new Error(errors.join('\n'));
  const outputPath = join(translationsDir, `${locale}.json`);
  let translation;
  try { translation = await readJSON(outputPath); } catch { translation = { schemaVersion: 1, locale, entries: {} }; }
  const needed = catalog.strings.filter(entry => {
    const current = translation.entries[entry.key];
    return !current || current.sourceHash !== sourceHash(entry);
  });
  if (!needed.length) { console.log(`No ${locale} strings need translation.`); return; }
  const response = await fetch('https://api.openai.com/v1/responses', {
    method: 'POST',
    headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model: process.env.TRANSLATION_MODEL || 'gpt-5.2',
      store: false,
      instructions: `Translate English product UI strings into ${locale}. Preserve every {{placeholder}}, URL, product name, and markup exactly. Use concise professional language. Return only the requested JSON schema.`,
      input: JSON.stringify({ glossary: catalog.glossary || {}, strings: needed }),
      text: {
        format: {
          type: 'json_schema', name: 'translations', strict: true,
          schema: {
            type: 'object', additionalProperties: false, required: ['translations'],
            properties: {
              translations: {
                type: 'array',
                items: {
                  type: 'object', additionalProperties: false, required: ['key', 'text'],
                  properties: { key: { type: 'string' }, text: { type: 'string' } },
                },
              },
            },
          },
        },
      },
    }),
  });
  if (!response.ok) throw new Error(`OpenAI translation request failed: ${await response.text()}`);
  const body = await response.json();
  const generated = JSON.parse(body.output_text || '{}').translations || [];
  const byKey = new Map(generated.map(item => [item.key, item.text]));
  for (const entry of needed) {
    const text = byKey.get(entry.key);
    if (!text) throw new Error(`OpenAI response omitted ${entry.key}`);
    if (JSON.stringify(placeholders(text)) !== JSON.stringify(placeholders(entry.source))) throw new Error(`${entry.key}: translated placeholders do not match source`);
    translation.entries[entry.key] = { sourceHash: sourceHash(entry), fingerprint: fingerprint(entry, locale, catalog.policyVersion || 1), text, status: 'draft' };
  }
  await writeJSON(outputPath, translation);
  console.log(`Wrote ${needed.length} ${locale} draft translations to ${outputPath}`);
}

async function main() {
  const [command, ...args] = process.argv.slice(2);
  const catalog = option(args, '--catalog');
  const translations = option(args, '--translations');
  if (!['status', 'check', 'translate'].includes(command) || !catalog || !translations) throw new Error('Usage: localization.mjs <status|check|translate> --catalog catalog.json --translations directory [--locale zh-TW]');
  if (command === 'translate') return translate(catalog, translations, option(args, '--locale'));
  return status(catalog, translations, command === 'check');
}

if (import.meta.url === `file://${process.argv[1]}`) main().catch(error => { console.error(error.message); process.exitCode = 1; });
