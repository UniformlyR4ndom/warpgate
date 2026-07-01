# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Warpgate is a file server designed to bypass restrictive web proxies that inspect traffic content.
The server serves files through a transform that disguises the content (e.g. as encrypted bytes or as a PDF).
The browser-based frontend reverses the transform to recover the original file.
Files can also be uploaded: the frontend applies the transform before sending, the server reverses it on receipt.

The server also provides a clipboard feature for storing short text notes in a local SQLite database.

The Go module (`warpgate`, Go 1.25.0) lives at the repo root. The frontend is embedded into the binary from the `web/` subdirectory.

## Architecture

### API endpoints

| Method   | Path                    | Auth required | Description |
|----------|-------------------------|---------------|-------------|
| GET      | `/api/public/checkin`   | No            | Logs client IP; called once on page load |
| POST     | `/api/files/list`       | Yes           | List files in the download root; body: `{"path":"subdir"}` |
| POST     | `/api/files/get`        | Yes           | Download a (chunk of a) file with a transform applied |
| POST     | `/api/files/upload`     | Yes           | Upload a file (requires `-ul-root`); multipart form |
| GET      | `/api/files/ul-list`    | Yes           | List files in the upload directory |
| GET      | `/api/clip/list`        | Yes           | List all clipboard notes |
| POST     | `/api/clip/new`         | Yes           | Create a clipboard note; body: `{"data":"<base64>"}` |
| POST     | `/api/clip/edit`        | Yes           | Update a note's content and timestamp; body: `{"id":<id>,"data":"<base64>"}` |
| DELETE   | `/api/clip/delete`      | Yes           | Delete a note by ID; body: `{"id":<id>}` |
| GET      | `/static/...`           | No            | Embedded frontend (HTML, JS, CSS) |

`/` and `/static` redirect to `/static/`.

The full API is documented in `doc/openapi.yaml`.

### `/api/files/get` request body

```json
{
  "path":      "relative/path/to/file",
  "start":     0,
  "size":      1048576,
  "transform": "pdf",
  "args":      { "filename": "attachment", "password": "abc123" }
}
```

### `/api/files/upload` multipart form

Two parts:
- `transform` (optional file part, JSON): `{"transform":"pdf","args":{"filename":"myfile.txt"}}`
- `file`: the file data (already encoded by the frontend)

## Authentication

All API endpoints except `/api/public/checkin` require an `X-Auth` header matching the server's key.

- Set the key with `-auth-key <key>`. If omitted and `-no-auth` is not set, a random 32-byte hex key is auto-generated and printed to the log at startup.
- `-no-auth` disables authentication entirely (no header check).
- The frontend stores the key in `localStorage` under `"auth-key"` and restores it on reload.
- The side panel has an **Auth** section where the user enters the key (displayed masked; a toggle reveals it). Changing it immediately refreshes the file listing.

Auth is enforced via `withAuth(h http.HandlerFunc) http.Handler` in `server.go`, applied at route registration time. The dispatch loop in `handler()` is unconditional.

## TLS

Pass `-cert <certfile>` and `-cert-key <keyfile>` to enable HTTPS. Both flags must be provided together; providing only one is a fatal startup error. Without them the server speaks plain HTTP.

The certificate file may be a full chain (intermediate + leaf). The startup log prints `https://` or `http://` to make the active mode visible.

### TLS client authentication

Two optional flags add mutual TLS; both require `-cert`/`-cert-key` and may be combined:

- `-client-root <pemfile>` — PEM certificate (CA or self-signed) used to validate the client certificate chain. Sets `tls.RequireAndVerifyClientCert`; connections whose certificate chain cannot be verified against this root are rejected.
- `-client-fp <hex>` — hex-encoded SHA-256 fingerprint of the exact client certificate to accept. When used alone (without `-client-root`) sets `tls.RequireAnyClientCert` and rejects any certificate whose fingerprint doesn't match. When combined with `-client-root`, chain validation and fingerprint pinning are both enforced.

The fingerprint check is implemented via `tls.Config.VerifyPeerCertificate`: `sha256.Sum256(rawCerts[0])` compared to the stored fingerprint. Both flags are validated at startup (`NewServer` returns an error for an unreadable/invalid root PEM or a fingerprint that doesn't decode to exactly 32 bytes).

## Transport Security and Proxy Bypass

The primary use-case is bypassing deep-packet inspection proxies. Recommended deployment:
- Use HTTPS (`-cert` / `-cert-key`) so the proxy cannot inspect payloads.
- Enable a transform (XOR, AES-CTR, PDF, or HTML/base64) so file content is further disguised even if TLS is terminated by the proxy.
- The File System Access API (chunk reassembly) requires a secure context (HTTPS or localhost).

## Transform System

### Interface (`transform.go`)

```go
type Transformer interface {
    Transform(r io.Reader) (io.Reader, error)   // encode — used on download
    Untransform(r io.Reader) (io.Reader, error) // decode — used on upload
}
```

`Transform` is called server-side when serving a download chunk; the browser reverses it.
`Untransform` is called server-side when receiving an upload; the browser pre-encoded the file.
For symmetric transforms (XOR, AES-CTR) `Untransform` delegates to `Transform`.
For asymmetric transforms (PDF) they are separate operations.

New transforms are registered in the `transformers` map in `transform.go`:
```go
var transformers = map[string]TransformerFactory{
    "id":       newIDTransformer,
    "xor":      newXORTransformer,
    "aes-ctr":  newAESCTRTransformer,
    "pdf":      newPDFTransformer,
    "html-b64": newHTMLB64Transformer,
}
```

### id (`transform_id.go`)

No-op. Both methods return the reader unchanged. Args: none.

### XOR (`transform_xor.go`)

Streams data XOR-ed with a repeating key. Symmetric.
Implemented as a streaming `xorReader` wrapping an `io.Reader`.
Args: `{"key": "<hex>"}` — key repeats cyclically, any length.

Frontend: byte loop using `hexToBytes`; same loop for upload encode and download decode.

### AES-CTR (`transform_aes.go`)

Streams data through AES in CTR mode via Go's `crypto/cipher`. Symmetric.
Args: `{"key": "<hex>", "iv": "<hex>"}`.
Key must be 16, 24, or 32 bytes; IV must be exactly 16 bytes (one AES block).

Frontend: uses `aes-js` (`web/aes.js`) — `ModeOfOperation.ctr`. Key and IV auto-generated randomly when "auto" is ticked.

### PDF (`transform_pdf.go` / frontend)

**Asymmetric** — encode and decode are different operations.

**Download (encode, server-side `Transform`):**
1. Uses a cached base PDF (`basePDFBytes`, built once at startup via `api.Create` from `basePDFJSON`).
2. Parses the base PDF with `api.ReadValidateAndOptimize`, embeds the file data as an attachment via `ctx.AddAttachment`.
3. Optionally encrypts with AES-256 (`model.NewAESConfiguration(password, password, 256)`).
4. The per-request password (20 hex chars) is generated in the browser and passed in args; never stored server-side.

**Download (decode, browser-side):**
`pdfjsLib.getDocument` opens the PDF (with password if set), then `pdf.getAttachments()` extracts the file bytes.

**Upload (encode, browser-side):**
`pdf-lib` (`PDFLib.PDFDocument.create()`) builds a PDF with "Hello there!" text and the file embedded via `pdfDoc.attach()`. `pdf-lib` 1.x does not support PDF encryption, so uploaded PDFs are unencrypted.

**Upload (decode, server-side `Untransform`):**
Reads the PDF into memory, calls `api.ExtractAttachmentsRaw` and returns `attachments[0]`.

Args: `{"filename": "<name>", "password": "<hex>"}`. Password applies to downloads only.

### HTML/Base64 (`transform_html_b64.go` / frontend)

**Symmetric** — XOR is applied before base64 encoding; the same XOR reverses it.

**Download (encode, server-side `Transform`):**
1. Buffers the chunk, XORs with the key (repeating, 16 bytes).
2. Base64-encodes the result.
3. Wraps in a minimal HTML page: `<!DOCTYPE html><html><body>\n<img src="data:image/jpeg;base64,<data>">\n</body></html>`.

**Download (decode, browser-side):**
`TextDecoder` converts the arraybuffer to a string, the base64 payload is extracted from the `src` attribute by string search, `atob()` decodes it, then `applyXOR` recovers the original bytes.

**Upload (encode, browser-side):**
XORs the file bytes, base64-encodes via a byte loop into `btoa()` (avoids `apply(null, largeArray)` stack overflow), wraps in the same HTML template.

**Upload (decode, server-side `Untransform`):**
Reads the HTML, extracts the base64 payload by string search between the fixed delimiters, base64-decodes, then XORs.

Args: `{"key": "<hex>"}` — exactly 16 bytes. The key is generated randomly per request by the frontend (not shown to or configurable by the user). The frontend label for this transform is "HTML (base64)".

## Clipboard (`db.go`, `clip.go`)

Notes are stored in a SQLite database (default `warpgate.sqlite`, configurable with `-db`).

### Schema

```sql
CREATE TABLE IF NOT EXISTS clipboard (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    data      TEXT    NOT NULL
)
```

### `db.go`

- `OpenDB(path string) (*DB, error)` — opens/creates the SQLite file and initialises the schema.
- `DB.InsertClipboard(data []byte) (id, ts int64, err error)` — inserts a new note with the current Unix timestamp.
- `DB.UpdateClipboard(id int64, data []byte) (bool, error)` — replaces note content and refreshes timestamp; returns false if ID not found.
- `DB.DeleteClipboard(id int64) (bool, error)` — deletes a note; returns false if ID not found.
- `DB.ListClipboard() ([]ClipItem, error)` — returns all notes ordered by `id`; `data` is base64-encoded in the returned struct.

The `ClipItem` struct is `{ID int64, TS int64, Data string}` and serialises to `{"id":…,"ts":…,"data":"<base64>"}`.

### `clip.go`

Handlers `handleClipList`, `handleClipNew`, `handleClipEdit`, `handleClipDelete` — all registered via `withAuth`. Base64 decoding of the incoming `data` field is done in the handler before passing raw bytes to the DB layer.

## Frontend (`web/`)

- `index.html` — Alpine.js v3 SPA with **Download**, **Upload**, and **Clipboard** tabs
- `app.js` — single `filesApp()` factory function registered with Alpine; no build step required
- `style.css` — dark theme
- `aes.js`, `alpine.min.js`, `pdf.min.js`, `pdf.worker.min.js`, `pdf-lib.min.js` — vendored libraries

All JS libraries are served locally (embedded in the binary); no CDN requests at runtime.

### Key frontend logic

- `authHeaders()` — returns `{"X-Auth": key}` if key is non-empty, else `{}`. Merged into every API request.
- `encodeForUpload(file)` — async; resolves to `{data: Blob, args: {}}`.
- `decrypt(buffer, password, htmlB64Key)` — reverses the transform on a downloaded chunk's `ArrayBuffer`.
- `fetchChunk(path, start, size)` — POSTs to `/api/files/get`, calls `decrypt` on the response.
- Chunked downloads with optional reassembly via the File System Access API (`showSaveFilePicker`). Requires a secure context (HTTPS or localhost); the checkbox is disabled otherwise.
- `uploadFiles(files)` — encodes and uploads all files in parallel, then refreshes the upload listing.
- `load()` / `loadUploadDir()` / `loadClipboard()` — each compares the fetched data against the current state with `JSON.stringify` and only updates the reactive array on a change, preserving scroll position during auto-refresh.
- `textToBase64` / `base64ToText` — UTF-8–safe helpers used by the clipboard tab.

### Side panel sections (alphabetical order)

| Section   | Purpose |
|-----------|---------|
| Auth      | `X-Auth` key input; masked by default with a 👁 reveal toggle; persisted in `localStorage["auth-key"]`; change triggers listing refresh |
| Chunking  | Enable chunked download (download tab only), set chunk size, enable File System Access API reassembly |
| General   | Auto-refresh (polls the active tab every second; re-renders only on changes); Confirm note delete |
| Transform | Select transform type and configure its parameters (auto-generate keys/IVs) |

### Clipboard tab

- Large textarea for composing a note; Submit button creates a new note via `/api/clip/new`.
- Clicking ✏ on a note pre-fills the textarea for editing; the button becomes "Update" / "Cancel". Pressing Escape or clicking "Cancel" discards the edit.
- Each note shows its timestamp and decoded text, with footer action buttons: ⬇ (download as `.txt`), 📋 (copy to system clipboard), ✏ (edit).
- ✕ delete button in the note header; optionally prompts for confirmation (controlled by "Confirm note delete" in the General panel section).

## Go Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/pdfcpu/pdfcpu` | v0.13.0 | Create PDFs, add/extract attachments, AES-256 encryption |
| `modernc.org/sqlite` | v1.53.0 | Pure-Go SQLite driver (no CGO); clipboard storage |

## Development

```bash
# Run
go run ./...

# Build
go build ./...

# Test
go test ./...

# Lint (install golangci-lint if needed)
golangci-lint run
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-root` | `./files` | Directory to serve for downloads |
| `-ul-root` | — | Upload directory (upload endpoints disabled if omitted) |
| `-db` | `warpgate.sqlite` | Path to the SQLite database file |
| `-listen` | `0.0.0.0:8080` | Listen address (`host:port`) |
| `-auth-key` | — | `X-Auth` key (auto-generated and logged if omitted) |
| `-no-auth` | false | Disable authentication |
| `-cert` | — | TLS certificate (chain) file; must be paired with `-cert-key` |
| `-cert-key` | — | TLS private key file; must be paired with `-cert` |
| `-client-root` | — | PEM certificate for validating client certificates; requires `-cert`/`-cert-key` |
| `-client-fp` | — | Hex SHA-256 fingerprint to pin a specific client certificate; requires `-cert`/`-cert-key` |
