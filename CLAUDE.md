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
| GET      | `/api/public/settings`  | No            | Returns `{"wsFrameMin":...,"wsFrameMax":...}`, the server's configured WS frame size bounds, for UI clamping |
| POST     | `/api/files/list`       | Yes           | List files in the download root; body: `{"path":"subdir"}` |
| POST     | `/api/files/download`   | Yes           | Download a (chunk of a) file with a transform applied |
| POST     | `/api/files/upload`     | Yes           | Upload a file (requires `-ul-root`); multipart form |
| GET      | `/api/files/ul-list`    | Yes           | List files in the upload directory |
| GET/Upgrade | `/api/files/ws/download`  | Yes (in-band) | WebSocket alternative to `/api/files/download`; see below |
| GET/Upgrade | `/api/files/ws/upload`    | Yes (in-band) | WebSocket alternative to `/api/files/upload` (requires `-ul-root`); see below |
| GET      | `/api/clip/list`        | Yes           | List all clipboard notes |
| POST     | `/api/clip/new`         | Yes           | Create a clipboard note; body: `{"data":"<base64>"}` |
| POST     | `/api/clip/edit`        | Yes           | Update a note's content and timestamp; body: `{"id":<id>,"data":"<base64>"}` |
| DELETE   | `/api/clip/delete`      | Yes           | Delete a note by ID; body: `{"id":<id>}` |
| GET      | `/static/...`           | No            | Embedded frontend (HTML, JS, CSS) |

`/` and `/static` redirect to `/static/`.

The full API is documented in `doc/openapi.yaml`.

### `/api/files/download` request body

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

## WebSocket Transport

`/api/files/ws/download` and `/api/files/ws/upload` are a second, user-selectable transport for the same
operations as `/api/files/download` and `/api/files/upload`, implemented in `ws.go`, `ws_download.go`, and
`ws_upload.go` (using `github.com/gorilla/websocket`). They exist alongside the HTTP transport, not
instead of it — the client picks per transfer. One WebSocket connection handles exactly one file
end to end; chunking within that connection is optional and orthogonal to the transport choice (see
"Transform System" below for why chunking matters independent of transport).

Browsers cannot set the `X-Auth` header on a WebSocket handshake request, so these two routes are
registered without the `withAuth` middleware; instead, auth is checked in-band against the `auth`
field of the first control message sent after upgrade. An invalid key gets a
`{"ok":false,"error":"unauthorized"}` message followed by a close frame with code 1008 (policy
violation).

Control messages are JSON text frames; file data is sent as binary frames. Because the transform
functions are stateless per call (a fresh cipher/reader is constructed each time), a single
`Transformer` is built once per connection from the first message's `transform`/`args` and reused
across chunks.

**Frame size is a compatibility knob, not just a performance one.** Intercepting/inspecting proxies
that buffer a whole WebSocket frame before forwarding it — Burp Suite among them — can choke on very
large single frames, closing the connection abnormally (no close frame, no reason) partway through a
transfer. Since bypassing exactly this kind of proxy is the point of the tool, the frame size used
for a given transfer is a **per-request value the client picks** (the frontend's "WS frame size"
setting in the General panel section, default 4 MB, used for both Download and Upload), not a fixed
server constant.

`-ws-frame-max` (`main.go`, default `16M`, range 1 KiB-256 MiB via `parseByteSize` in
`bytesize.go`) is the server-operator-controlled *ceiling* on that per-request value; it also sizes
`Server.wsUpgrader`'s `ReadBufferSize`/`WriteBufferSize` (`server.go`), which just needs to be large
enough to hold the biggest frame any request is allowed to produce — `NewServer` clamps a
misconfigured/zero `Config.WSFrameMax` up to `wsFrameSizeMin` (1 KiB) so a zero-size frame buffer can
never occur (that would make the frame-writing loop in `ws_download.go` spin forever: `io.ReadFull` on an
empty buffer returns `(0, nil)` without ever blocking). `GET /api/public/settings` reports the
current `wsFrameMin`/`wsFrameMax` so the frontend can clamp its input instead of letting the user
pick a value the server will reject.

Each control message carries a `frameSize` (bytes); `Server.resolveFrameSize` (`ws.go`) validates it
against `[wsFrameSizeMin, cfg.WSFrameMax]`, rejecting out-of-range requests, or — if omitted/zero —
falls back to the server ceiling. **Frame splitting is purely transport-level and must stay invisible
to `Transformer.Transform`/`Untransform`**: XOR and AES-CTR reset their cipher state (key phase / CTR
counter) at the start of every call, and PDF/html-b64 chunks are self-contained documents, so a
chunk's bytes must be fully assembled before a single Transform/Untransform call — never split across
independent calls. Concretely:
- **Download** (`ws_download.go`): `Transform` is still called once per `{start,size}` chunk request; only
  *its output* is split, by manually looping `Conn.WriteMessage` over `frameSize`-sized pieces read
  via `io.ReadFull` (instead of one `Conn.NextWriter`+`io.Copy` call left to gorilla's own,
  connection-wide auto-fragmentation).
- **Upload** (`ws_upload.go`): the server accumulates *all* binary frames for the file into one
  buffer, and only calls `Untransform` once, when the `{"done":true}` message arrives. (Earlier this
  called `Untransform` per received message, which was only safe because there was always exactly one
  message per upload; now that uploads can be split into multiple frames, that assumption no longer
  holds and would otherwise silently corrupt anything but the `id` transform.)

**Download protocol:**
1. Client → Server (text): `{"auth":"<key>","path":"...","transform":"xor","args":{...},"frameSize":1048576}`
2. Server → Client (text): `{"ok":true,"size":12345}` or `{"ok":false,"error":"..."}`
3. Client → Server (text, repeated, one at a time — no pipelining): `{"start":0,"size":1048576}` to
   request a chunk, or `{"done":true}` to end the session
4. Server → Client (binary, one or more messages, each ≤ `frameSize`, totaling `size` bytes): the
   transformed chunk for the requested range
5. On any error: server sends `{"ok":false,"error":"..."}` then closes

**Upload protocol:**
1. Client → Server (text): `{"auth":"<key>","filename":"...","transform":"xor","args":{...},"frameSize":1048576}`
2. Server → Client (text): `{"ok":true}` or `{"ok":false,"error":"..."}`
3. Client → Server (binary, one or more messages, each ≤ `frameSize`): the transformed file's bytes,
   split purely for transport — unlike before, an individual message is *not* required to be
   independently decodable
4. Client → Server (text, final): `{"done":true}`
5. Server → Client (text, final): `{"ok":true,"name":"<filename>"}`, then a normal close
6. On any error: server sends `{"ok":false,"error":"..."}` then closes; a partial output file may be
   left behind, matching the existing HTTP upload path's behavior on a mid-copy failure

`doc/openapi.yaml` is REST/HTTP-only and does not model these two endpoints; this section is their
source of truth.

## Authentication

All API endpoints except `/api/public/checkin` and `/api/public/settings` require an `X-Auth` header
matching the server's key (the two WebSocket endpoints are also unauthenticated at the HTTP/upgrade
level, but check the key in-band — see WebSocket Transport below).

- Set the key with `-auth-key <key>`. If omitted and `-no-auth` is not set, a random 32-byte hex key is auto-generated and printed to the log at startup.
- `-no-auth` disables authentication entirely (no header check).
- The frontend persists the key as part of the unified UI settings (see "UI settings persistence" below) and restores it on reload.
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
- `fetchChunk(wsSession, path, start, size)` — dispatches to `fetchChunkHTTP` (POSTs to `/api/files/download`) or `fetchChunkWS` based on the `settings.backend` setting, calling `decrypt` on the (re)assembled response either way. `wsSession` is a plain `{ws, password, htmlB64Key}` object owned by the caller (`downloadFile`), threaded through repeated calls for the same file so the WS backend reuses one connection per file instead of opening one per chunk; `closeWSSession` ends it. Ignored for the HTTP backend.
- Chunked downloads with optional reassembly via the File System Access API (`showSaveFilePicker`). Requires a secure context (HTTPS or localhost); the checkbox is disabled otherwise.
- `uploadFiles(files)` — dispatches each file to `uploadFileHTTP` or `uploadFileWS` based on `settings.backend`, in parallel, then refreshes the upload listing.
- `load()` / `loadUploadDir()` / `loadClipboard()` — each compares the fetched data against the current state with `JSON.stringify` and only updates the reactive array on a change, preserving scroll position during auto-refresh.
- `textToBase64` / `base64ToText` — UTF-8–safe helpers used by the clipboard tab.

### UI settings persistence

Everything under Alpine's `settings` object (auth key, backend choice, auto-refresh, confirm-delete,
WS frame size, transform config, chunking config, and side-panel open/collapsed state) is persisted
as one JSON blob in `localStorage["warpgate-settings"]`, restored on the next page load. Deliberately
**not** persisted: `tab`/`pathSegments` (navigation, not configuration — a stale restored path could
point at a since-deleted directory), `wsFrameMinKB`/`wsFrameMaxKB` (fetched fresh from
`/api/public/settings` every load, not user configuration), and `authShowKey` (whether the auth key
is currently unmasked — always starts masked for basic shoulder-surf hygiene, kept as a standalone
top-level property outside `settings` specifically so it can never end up in the persisted blob).

- Shape: `{"version":1,"settings":{...}}` — mirrors the `settings` object exactly, so persistence is
  just "serialize `settings`, merge back into `settings`" with no separate allowlist to maintain. Any
  new UI-configurable value should be added under `settings`, not as a new top-level Alpine property,
  or it won't be persisted.
- `mergeSettings(target, source)` (module-level in `app.js`) recursively copies fields from the parsed
  stored blob onto the live `settings` object, but only for keys that already exist on the target —
  this ignores stale/unknown fields from an older schema version rather than grafting them on
  (relevant if `SETTINGS_VERSION` is ever bumped and old/new fields briefly coexist).
- `loadSettings()` runs synchronously as the very first line of `init()`, before `applyAuto()` — that
  order matters: it lets a restored `auto: false` + custom key survive untouched, while `auto: true`
  still gets a fresh key generated every load (`applyAuto()` only overwrites when the auto flag is
  true), matching the pre-existing "never reuse an old auto-generated key" behavior.
- `saveSettings()` is triggered by `this.$watch('JSON.stringify(settings)', ...)` — evaluating
  `JSON.stringify` reads every nested field, which is what makes the watcher fire on a change
  anywhere in the tree; a plain `$watch('settings', ...)` only fires when the `settings` reference
  itself is reassigned, not on nested mutations, since almost every field here is edited via a plain
  `x-model` binding with no dedicated change handler to hook a manual save into.
- **`<select x-model="settings.backend">` and `<select x-model="settings.transform.type">` need an
  extra nudge.** Their `<option>`s are rendered by a child `x-for`; Alpine's tree walk processes a
  parent's directives (including `x-model`'s initial DOM sync) before recursing into that child, so
  `x-model` tries to apply the restored value before the matching `<option>` exists. A native
  `<select>.value` assignment to a not-yet-existing option value is a silent no-op with no automatic
  retry once the option later appears — so without a fix, these two selects visually stick on their
  default option after a restore even though the underlying `settings` value is correct.
  `$nextTick` fires too early to reliably fix this; `init()` instead force-resyncs both via
  `x-ref="backendSelect"`/`x-ref="transformTypeSelect"` inside a `setTimeout(fn, 0)` (a full macrotask
  later, after Alpine's render has definitely settled).

### Side panel sections (alphabetical order)

| Section   | Purpose |
|-----------|---------|
| Auth      | `X-Auth` key input; masked by default with a 👁 reveal toggle (reveal state itself is not persisted); change triggers listing refresh |
| Chunking  | Enable chunked download (download tab only), set chunk size, enable File System Access API reassembly |
| General   | Auto-refresh (polls the active tab every second; re-renders only on changes); Confirm note delete; Backend (HTTP POST/Websocket, download+upload tabs); WS frame size in KB (download+upload tabs, only when Backend is Websocket; default 4M, clamped to the server's `-ws-frame-max` via `/api/public/settings`) |
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
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket transport for `/api/files/ws/download` and `/api/files/ws/upload` |

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
| `-ws-frame-max` | `16M` | Ceiling on the WS frame size a client may request; byte count or `K`/`M` suffix (case-insensitive), range `1K`-`256M` |
