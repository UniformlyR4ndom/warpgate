// Stand-in for web/pdf.min.mjs: the PDF transform isn't exercised by these
// tests, so getDocument only needs to exist as an import target.
export function getDocument() {
  throw new Error('stub-pdf: getDocument is not available in the Node test harness');
}

export const GlobalWorkerOptions = {};
export class PDFWorker {}
