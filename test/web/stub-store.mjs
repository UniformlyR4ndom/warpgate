// Stand-in for web/app/store.js for the Node tests. The loader redirects
// store.js imports here so the real store (deepSignal, preact, browser globals)
// need not load. Only the fields the modules under test read are present.
export const API_BASE = '';

export const settings = {
  backend: 'http',
  wsFrameSizeKB: 4096,
  paramCrypt: { enabled: false },
  contentType: { preset: 'auto', custom: '' },
  transform: {
    type: 'id',
    xor:    { key: '' },
    aesCtr: { key: '', iv: '' },
    pdf:    { filename: '', password: { enabled: false } },
  },
};

export const state = {
  clipItems: [],
  clipCursor: 0,
  clipImages: {},
  pendingAttachments: [],
  clipRemovedAttachments: [],
  clipCaptionEdits: {},
  clipText: '',
  clipTitle: '',
  clipEditId: null,
  clipError: null,
  clipSubmitting: false,
};

export const authKeyHash = { value: '' };
