// ESM resolve hook: redirect the two browser-only imports of transform.js to
// local stubs, so the module can be loaded and tested under Node.
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const redirects = {
  'store.js': pathToFileURL(join(here, 'stub-store.mjs')).href,
  'pdf.min.mjs': pathToFileURL(join(here, 'stub-pdf.mjs')).href,
  'api.js': pathToFileURL(join(here, 'stub-api.mjs')).href,
};

export function resolve(specifier, context, nextResolve) {
  for (const [suffix, url] of Object.entries(redirects)) {
    if (specifier.endsWith('/' + suffix) || specifier === suffix) {
      return { url, shortCircuit: true };
    }
  }
  return nextResolve(specifier, context);
}
