#!/usr/bin/env node
// G117 — convert tests/e2e/fixtures/mock-blueprints.yaml -> public/mock-fixture.json
// so the in-browser mock harness can fetch it without a YAML parser at runtime.
// Run pre-dev + pre-build; no-op when the YAML hasn't changed.

import { readFileSync, writeFileSync, mkdirSync, existsSync, statSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import yaml from 'js-yaml';

const here = dirname(fileURLToPath(import.meta.url));
const src = resolve(here, '..', 'tests', 'e2e', 'fixtures', 'mock-blueprints.yaml');
const dst = resolve(here, '..', 'public', 'mock-fixture.json');

if (!existsSync(src)) {
  console.error('mock fixture source missing:', src);
  process.exit(1);
}

const srcMtime = statSync(src).mtimeMs;
if (existsSync(dst) && statSync(dst).mtimeMs >= srcMtime) {
  console.info('mock fixture up-to-date — skipping build');
  process.exit(0);
}

const raw = readFileSync(src, 'utf8');
const parsed = yaml.load(raw);
mkdirSync(dirname(dst), { recursive: true });
writeFileSync(dst, JSON.stringify(parsed, null, 2));
console.info('wrote mock fixture →', dst);
