// Dev-only lint pass for the frontend ES modules. It has no build step, so this
// is what catches dead references, bad import paths, and imported names that no
// longer exist — the mistakes the Go compiler would catch on the backend.
// Run with `npm run lint`. Vendored and minified libraries are not our code and
// are skipped (but still resolved, so import checks see their exports).
import globals from 'globals';
import importPlugin from 'eslint-plugin-import';

export default [
  {
    ignores: [
      'web/app/vendor/**',
      'web/pdf.min.mjs',
      'web/pdf.worker.min.mjs',
      'web/pdf-lib.min.js',
      'web/aes.js',
      'web/sha256.js',
    ],
  },
  {
    files: ['web/app/**/*.js', 'web/app/**/*.mjs'],
    plugins: { import: importPlugin },
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {
        ...globals.browser,
        // Loaded as global <script>s in index.html, not imported.
        PDFLib: 'readonly',
        aesjs: 'readonly',
        sha256: 'readonly',
        // Not yet in the browser globals set.
        FileSystemObserver: 'readonly',
      },
    },
    settings: {
      'import/resolver': { node: { extensions: ['.js', '.mjs'] } },
    },
    rules: {
      'no-undef': 'error',
      'no-unused-vars': ['error', { args: 'none', caughtErrors: 'none' }],
      'import/no-unresolved': 'error',
      'import/named': 'error',
    },
  },
];
