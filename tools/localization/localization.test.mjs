import assert from 'node:assert/strict';
import test from 'node:test';
import { fingerprint, placeholders, sourceHash, validateCatalog, validateTranslations } from './localization.mjs';

const entry = { key: 'devices.count', source: '{{count}} devices', context: 'fleet metric', placeholders: ['{{count}}'] };
const catalog = { schemaVersion: 1, sourceLocale: 'en', policyVersion: 1, strings: [entry] };

test('source hash changes with source context and keeps placeholder order stable', () => {
  assert.equal(sourceHash(entry), sourceHash({ ...entry, placeholders: ['{{count}}'] }));
  assert.notEqual(sourceHash(entry), sourceHash({ ...entry, source: '{{count}} managed devices' }));
  assert.notEqual(fingerprint(entry, 'zh-TW', 1), fingerprint(entry, 'zh-CN', 1));
});

test('translation validation rejects stale checksums and missing placeholders', () => {
  assert.deepEqual(validateCatalog(catalog), []);
  const valid = { schemaVersion: 1, locale: 'zh-TW', entries: { 'devices.count': { sourceHash: sourceHash(entry), text: '{{count}} 台裝置', status: 'approved' } } };
  assert.deepEqual(validateTranslations(catalog, valid), []);
  assert.match(validateTranslations(catalog, { ...valid, entries: { 'devices.count': { ...valid.entries['devices.count'], text: '裝置' } } }).join('\n'), /placeholders/);
  assert.deepEqual(placeholders('Use {{name}} on {{date}}'), ['{{date}}', '{{name}}']);
});
