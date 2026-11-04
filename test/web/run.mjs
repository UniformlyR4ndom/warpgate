// Entry point for the frontend Node tests. Installs the browser globals the
// first-party modules expect, registers the import loader (which stubs the
// browser-only store/pdf imports), then runs each test module.
import { register } from 'node:module';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

globalThis.window = { crypto: globalThis.crypto };
globalThis.aesjs = require(join(here, '..', '..', 'web', 'aes.js'));

register('./loader.mjs', import.meta.url);

await import('./transform.test.mjs');
await import('./loadguard.test.mjs');
await import('./ws.test.mjs');
await import('./chunksize.test.mjs');
await import('./clipboard.test.mjs');
