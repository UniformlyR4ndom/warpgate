// Stand-in for web/app/store.js: a plain, mutable `settings` object with just
// the fields transform.js reads. The loader redirects store.js imports here so
// the real store (deepSignal, preact, browser globals) need not load in Node.
export const settings = {
  backend: 'http',
  wsFrameSizeKB: 4096,
  contentType: { preset: 'auto', custom: '' },
  transform: {
    type: 'id',
    xor:    { key: '' },
    aesCtr: { key: '', iv: '' },
    pdf:    { filename: '', password: { enabled: false } },
  },
};
