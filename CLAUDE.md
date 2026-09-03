# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Comment Policy

Default to no comment at all; add one only when skipping it would leave a reader
surprised or misled. When a comment is warranted, it must:

1. Be brief — no more than the fact and, if needed, a short reason why.
2. State only what is true and non-obvious about the code it annotates — if the code
   already shows it, the comment isn't earning its place.
3. Never narrate history — no past bugs, no design alternatives once tried.
4. Never narrate relationships to other code — no "only called from X," no "same as Y
   does it," no caller/sibling enumeration. Describe this code, not the rest of the
   codebase.

## Duplication Policy

Default to sharing logic that two or more call sites must always change together;
default to leaving logic separate when it only resembles another spot today. When
consolidating:

1. Extract only when a bug fix or behavior change would have to be applied
   identically everywhere it currently appears — that's one piece of logic, not
   several accidental copies of it.
2. Don't force a shared abstraction to swallow a genuine per-caller difference (e.g.
   a different data shape) via an extra parameter or branch — two separate copies are
   usually clearer than one that needs a flag to behave differently per caller.
3. When refactoring several files at once, judge each small pure helper on its own —
   don't bundle a genuinely shared one in with a neighbor whose reason for staying
   separate doesn't actually apply to it.
4. Prefer a few similar lines over a new shared file/hook for something used in
   exactly one place today; extract once a second real use exists.

## Project Overview

Warpgate is a file server designed to bypass restrictive web proxies that inspect traffic content.
The server serves files through a transform that disguises the content (e.g. as encrypted bytes or as a PDF).
The browser-based frontend reverses the transform to recover the original file.
Files can also be uploaded: the frontend applies the transform before sending, the server reverses it on receipt.

The server also provides a clipboard feature for storing short text notes in a local SQLite database,
and a benchmark tab for probing which transport (HTTP POST vs. WebSocket) and frame/chunk size a
given proxy tolerates, independent of any transform.

The Go module (`warpgate`, Go 1.25.0) lives at the repo root. The frontend is embedded into the binary from the `web/` subdirectory.
`cmd/wgcli` is a same-module Go CLI client that exercises the HTTP/WS API from the command line
(mainly for testing); see the `wgcli` skill for usage details.

## Architecture

### API endpoints

| Method   | Path                    | Auth required | Description |
|----------|-------------------------|---------------|-------------|
| GET      | `/api/public/checkin`   | No            | Logs client IP; called once on page load |
| GET      | `/api/public/settings`  | No            | Returns `{"wsFrameMin":...,"wsFrameMax":...}`, the server's configured WS frame size bounds, for UI clamping |
| POST     | `/api/files/list`       | Yes           | List files in the download root; body: `{"path":"subdir"}` |
| POST     | `/api/files/download`   | Yes           | Download a (chunk of a) file with a transform applied |
| POST     | `/api/files/upload`     | Yes           | Upload a (chunk of a) file (requires `-ul-root`); multipart form |
| GET      | `/api/files/ul-list`    | Yes           | List files in the upload directory |
| GET/Upgrade | `/api/files/ws/download`  | Yes (in-band) | WebSocket alternative to `/api/files/download`; see below |
| GET/Upgrade | `/api/files/ws/upload`    | Yes (in-band) | WebSocket alternative to `/api/files/upload` (requires `-ul-root`); see below |
| POST     | `/api/sim/download`     | Yes           | Download a chunk of a synthesized file, no transform applied; see "Benchmark/Simulation Endpoints" |
| POST     | `/api/sim/upload`       | Yes           | Upload a (chunk of a) file for SHA-256 hashing (requires `-ul-root`), nothing written to disk; see below |
| GET/Upgrade | `/api/sim/ws/download`  | Yes (in-band) | WebSocket alternative to `/api/sim/download`; see below |
| GET/Upgrade | `/api/sim/ws/upload`    | Yes (in-band) | WebSocket alternative to `/api/sim/upload` (requires `-ul-root`); see below |
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
  "path":        "relative/path/to/file",
  "start":       0,
  "size":        1048576,
  "transform":   "pdf",
  "args":        { "filename": "attachment", "password": "abc123" },
  "contentType": "image/jpeg"
}
```

`contentType` is optional and overrides the response's `Content-Type` header in place of the
transform's own default (see the `contentTyper` interface above). `validContentType` (`download.go`)
rejects control characters and anything over 255 bytes with a 400; an empty/omitted value keeps the
transform's default. Only meaningful for the HTTP backend — WS downloads have no HTTP response
headers, so the frontend's Content-Type control (General panel section) is hidden whenever
`settings.backend === 'ws'`.

This body may optionally be an encrypted blob instead of plain JSON — see "Parameter Encryption" below.

### `/api/files/upload` multipart form

Two parts:
- `transform` (optional file part, JSON): `{"transform":"pdf","args":{"filename":"myfile.txt"},"continuation-key":"<hex>","ul-done":true}`.
  May optionally be an encrypted blob instead of plain JSON — see "Parameter Encryption" below. Only
  this part is ever encrypted; the `file` part below never is.
- `file`: this request's file data (already encoded by the frontend; for a chunked upload, this
  chunk's independently-transformed bytes, not the whole file)

`continuation-key` and `ul-done` are optional and enable chunked upload — splitting one file across
several requests instead of sending it all in a single POST. `ul-done` defaults to `true` when
omitted entirely — not just when falsy — via a `*bool` in the decode struct, specifically so a
legacy client that has never heard of chunking (no `transform` part at all, or one that omits
`ul-done`) gets the historical one-shot behavior automatically: the whole file in one request,
written immediately, no session created. Sending `"ul-done":false` explicitly — even with no
`continuation-key`, i.e. on what would otherwise be a one-shot request — opts into chunking instead;
a plain `bool` can't make this distinction, since `false` and "absent" decode identically.

**Chunked upload protocol** (HTTP POST only — no WebSocket equivalent yet; `wgcli upload`'s
`-chunk-size` flag exercises it):
1. First chunk: `continuation-key` omitted. Behaves like a one-shot upload — creates/overwrites the
   target file unconditionally, discarding any prior state (even an in-progress or completed
   session) recorded for that filename. `transform`/`args` apply to *this chunk only*: each chunk is
   transformed independently (its own `Untransform` call), not as one transform spanning the whole
   file, so `pdf`/`html-b64` chunks are self-contained per chunk, the same way download chunks
   already are.
2. If the resolved `ul-done` (see the defaulting rule above) is not `true`, the response includes a
   `continuation-key`: more chunks are expected.
3. Subsequent chunks: `continuation-key` set to the value from the previous response; the decoded
   bytes are appended to the target file. Each successful non-final chunk's response carries a
   freshly-rotated `continuation-key` for the next chunk.
4. Final chunk: `ul-done: true`. Response omits `continuation-key`; the upload is complete.

Implemented in `ul_session.go` (session/locking state, generic over any session payload type — see
below) and `upload.go` (`handleUpload`, `handleUploadFirstChunk`, `handleUploadContinuation`). The
identical protocol, sharing this same `ul_session.go` machinery, also powers `/api/sim/upload`'s
chunked benchmark upload — see "Benchmark/Simulation Endpoints" below.

- **Locking**: a per-filename claim (`sessionStore.tryClaim`/`release`, called via
  `Server.uploadSessions`, a `*sessionStore[*ulSession]`) rejects a request outright (`409`) rather
  than queuing it if another request for the same filename is already being processed —
  deliberately simple given chunks are expected to be sent strictly one at a time, each awaited
  before the next (`web/app.js`'s `uploadFileHTTPChunked` does this).
- **Continuation-key rotation**: the server remembers both the current and immediately-previous key
  for an in-progress session. A request bearing the previous key (an ack that was lost in transit,
  so the client retried) is answered idempotently with the current key, without reprocessing — this
  is why chunks must never be sent concurrently or out of order, since only the single
  immediately-prior key is remembered. A key matching neither is rejected *without* touching
  whatever, if anything, is actually on record for that filename, so a stale or superseded request
  can never disturb a live or completed session it no longer corresponds to.
- **Completion**: once `ul-done: true` succeeds, the session becomes a "completed" marker (keeping
  the key that was just used) rather than being deleted outright, so a late retry of that exact
  final request still gets an idempotent "already done" response instead of an error.
- **Failure**: any real processing failure (a transform error only discoverable at decode time, a
  write error, or the request exceeding `ulChunkProcessTimeout`) deletes both the partial file and
  the entire session — there is no partial-failure recovery; the client must restart the upload from
  scratch.
- **Expiry**: `ulSweepExpired` purges sessions idle longer than `ulSessionIdleExpiry` (both in
  `ul_session.go`; currently the same value as `ulChunkProcessTimeout` but an independent constant —
  one bounds a single request's own processing time, the other bounds the gap between successive
  chunks). An active (incomplete) session's partial file is deleted on expiry; a completed marker's
  file is left alone, since it's already finished and correct.
- **Listing**: `GET /api/files/ul-list` hides any filename with a non-completed session on record
  (`Server.uploadSessions.lockedNames()`), so an in-progress upload never appears as if it were a
  finished, correctly-sized file.
- **Download-side locking**: `handleDownload` (`download.go`) also consults `lockedNames()` via
  `Server.downloadBlockedByUpload`, which compares *resolved absolute paths* — `filepath.Join(s.absUlRoot,
  name)` against the download's own resolved target — rather than special-casing "`-root` equals
  `-ul-root`". This is what actually matters: it's correct whether the two roots are identical,
  nested, or unrelated, since a download into a subdirectory can never collide (uploads always land
  flat under `-ul-root`). A blocked download gets `409 Conflict`, distinct from the plain `404` a
  never-uploaded filename gets.

## WebSocket Transport

`/api/files/ws/download` and `/api/files/ws/upload` are a second, user-selectable transport for the same
operations as `/api/files/download` and `/api/files/upload`, implemented in `ws.go`, `ws_download.go`, and
`ws_upload.go` (using `github.com/gorilla/websocket`). They exist alongside the HTTP transport, not
instead of it — the client picks per transfer. One WebSocket connection handles exactly one file
end to end; chunking within that connection is optional and orthogonal to the transport choice (see
"Transform System" below for why chunking matters independent of transport).

Browsers cannot set the `X-Auth` header on a WebSocket handshake request, so these two routes are
registered without the `withAuth` middleware; instead, auth is checked in-band against the `auth`
field of the first control message sent after upgrade — like `X-Auth`, this carries the derived
token (see "Authentication" below), not the raw key. An invalid token gets a
`{"ok":false,"error":"unauthorized"}` message followed by a close frame with code 1008 (policy
violation).

Control messages are JSON text frames; file data is sent as binary frames. Because the transform
functions are stateless per call (a fresh cipher/reader is constructed each time), a single
`Transformer` is built once per connection from the first message's `transform`/`args` and reused
across chunks. Every client→server control message described below (here and in the equivalent
sim/download and sim/upload protocols) — not just the handshake — may optionally be an encrypted blob
instead of plain JSON; see "Parameter Encryption" below. Server→client acks mirror that same choice
for the whole connection, decided once at the handshake — see "Parameter Encryption" → "Response
encryption".

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
1. Client → Server (text): `{"auth":"<authToken>","path":"...","transform":"xor","args":{...},"frameSize":1048576}`
2. Server → Client (text): `{"ok":true,"size":12345}` or `{"ok":false,"error":"..."}` — `size` is the
   original (plaintext) file size
3. Client → Server (text, repeated, one at a time — no pipelining): `{"start":0,"size":1048576}` to
   request a chunk, or `{"done":true}` to end the session
4. Server → Client (text): `{"ok":true,"chunkSize":N}` — `N` is the *transformed* byte length of this
   chunk, which is not always equal to the requested plaintext `size` (`pdf`/`html-b64` both expand
   it; see "Why `chunkSize` is announced separately" below)
5. Server → Client (binary, one or more messages, each ≤ `frameSize`, totaling `chunkSize` bytes): the
   transformed chunk for the requested range
6. On any error: server sends `{"ok":false,"error":"..."}` then closes

**Why `chunkSize` is announced separately (step 4).** Historically the client just assumed the
transformed chunk was exactly as long as the requested plaintext `size` and read frames until that
many bytes arrived — true for `id`/`xor`/`aes-ctr`, but false for `pdf`/`html-b64` (both expand their
input), which broke chunked WS download for those two transforms (verified: a 22-byte file with
`transform: pdf` produced a 1290-byte frame while the old ack still said `size: 22`; in the browser,
`received.set(piece, offset)` would throw `RangeError: offset is out of bounds`). The fix:
- `internal/transform.SizedTransformer` — `Transformer` plus `EncodedLen(plaintextLen int64) int64` —
  is implemented by `id`/`xor`/`aes-ctr` (`return plaintextLen`) and `html-b64`
  (`4*ceil(n/3) + len(htmlPrefix) + len(htmlSuffix)`, sharing the same prefix/suffix constants
  `Transform` writes, via `base64.StdEncoding.EncodedLen`). `pdf` does not implement it — its output
  size depends on internal PDF library overhead and can't be predicted without materializing.
- `handleWSDownload` (`ws_download.go`) computes `chunkSize` from `EncodedLen` when the transformer
  implements `SizedTransformer` (streaming preserved), otherwise fully buffers `Transform`'s output via
  `io.ReadAll` to measure it directly (pdf only — which already buffers internally, so this adds no
  new cost there).
- Not applicable to upload: `handleWSUpload` accumulates all frames into one buffer and calls
  `Untransform` once at `{"done":true}`, so there's no per-chunk length ambiguity on that path.
- Not applicable to `/api/sim/ws/download`: no transform is applied there, so requested size always
  equals returned size.

**Upload protocol:**
1. Client → Server (text): `{"auth":"<authToken>","filename":"...","transform":"xor","args":{...},"frameSize":1048576}`
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

## Benchmark/Simulation Endpoints

`/api/sim/download`, `/api/sim/ws/download`, `/api/sim/upload`, and `/api/sim/ws/upload`
(`sim.go`, `sim_download.go`, `sim_upload.go`, `sim_ws_download.go`, `sim_ws_upload.go`) exist
purely to probe which transport (HTTP POST vs. WebSocket) and frame/chunk size work through a given
proxy, independent of any transform. **No transform is applied and no filesystem I/O happens** on
either side — these endpoints exist to isolate transport behavior from everything else, so unlike
`/api/files/*` they take no `transform`/`args` fields at all.

**Download** synthesizes a file server-side instead of reading one from `-root`: the content is
`<totalSize-as-decimal><space><0xFF padding>`, out to `totalSize` bytes total, generated on the fly
by `simReader` (`sim.go`) for an arbitrary `[start, …)` window so ranged/chunked requests never
materialize the whole file. `validateSimTotalSize` enforces `1 <= totalSize <= simMaxTotalSize` (1024
MiB) and that `totalSize` is large enough for its own decimal representation plus the trailing space
to fit (e.g. `totalSize: 1` is rejected). The frontend never persists the downloaded bytes to disk —
it reassembles them in memory only long enough to verify a SHA-256 hash against a locally-computed
expected value (see "Benchmark tab" below), then discards them.

**Upload** accepts a file, or one chunk of one, the same way `/api/files/upload` does (gated behind
`-ul-root`, even though nothing is written to disk — matching the operator's intent to disable
upload-shaped features entirely via that flag) but never writes it: the received bytes are streamed
straight into a running SHA-256 hash, capped cumulatively at `simMaxTotalSize` across all chunks of
one run, and the hex-encoded hash is returned once the upload completes, instead of a filename, so a
client can verify a benchmark upload arrived intact without server-side storage.

The HTTP upload's `simMaxTotalSize` cap applies to the file *content*, not the raw request body: the
`http.MaxBytesReader` guarding `r.Body` is set to `simMaxTotalSize + multipartOverheadSlack` (64 KiB)
because multipart/form-data framing (boundary markers, part headers) adds bytes on top of the file
itself, and a file at exactly `simMaxTotalSize` — the UI's own max, so exactly what a user
stress-testing the benchmark would pick — would otherwise always overflow a same-sized cap once that
framing is counted, and get rejected as if it were too large. This is a *per-request* cap; a chunked
run additionally enforces the same `simMaxTotalSize` ceiling *cumulatively* via
`simUlSession.totalBytes` (`sim_upload.go`), checked after every chunk.

Because there's no transform step requiring the full contents assembled in one place first, the WS
upload variant hashes frames incrementally as they arrive rather than buffering them like
`handleWSUpload` does.

**Chunked sim upload** (`sim_upload.go`, HTTP only) reuses the exact same `ul_session.go` machinery
as `/api/files/upload` — see that endpoint's "Chunked upload protocol" above for the full
`continuation-key`/`ul-done` mechanics (rotation, idempotent replay of the previous/completed key,
locking, `ul-done` defaulting to `true` when omitted, teardown on failure, expiry) — with these
differences:
- No `transform`/`args` fields: the `transform` part, when present, carries only
  `{"continuation-key":"<hex>","ul-done":true}`.
- The `file` part's filename is a session key only — nothing is ever written to disk under it, and
  it lives in its own namespace (`Server.simUploadSessions`, a separate `*sessionStore[*simUlSession]`
  from `Server.uploadSessions`), so it's safe to reuse a name like `bench.bin` across runs without
  colliding with a real upload or a previous benchmark run.
- The session's payload is a running `hash.Hash` (`simUlSession.hasher`) plus a cumulative byte count
  (`simUlSession.totalBytes`, checked against `simMaxTotalSize` after every chunk — see above), not a
  reopened file: there's nothing to "append to" between requests, so the hash itself must be the
  thing kept alive.
- A first chunk (`continuation-key` omitted) always resets `hasher`/`totalBytes`, discarding any
  prior session for that name — mirroring how a real first chunk unconditionally overwrites its
  target file.
- Every successful response is `200`, never `/api/files/upload`'s `201`-for-first-chunk — nothing is
  ever "created" here. `hash` is present only on the response that completes the upload (a one-shot
  request, the final chunk, or a completed-session replay); a non-final chunk's response carries
  `continuation-key` instead, exactly like `/api/files/upload`'s `UploadResponse.ContinuationKey`.

This also drives the Benchmark tab's HTTP-backend "Run Upload Benchmark" chunking — see "Benchmark
tab" below.

**HTTP request/response shapes** (see `doc/openapi.yaml` for the authoritative schema):
- `POST /api/sim/download`: `{"totalSize":1048576,"start":0,"size":1048576}` → raw bytes
  (`application/octet-stream`)
- `POST /api/sim/upload`: multipart form, `file` part required, `transform` part optional
  (`{"continuation-key":"<hex>","ul-done":true}`) → `{"hash":"<hex>"}` on a one-shot/final/replay
  response, `{"continuation-key":"<hex>"}` on an intermediate chunk's response — see "Chunked sim
  upload" above.

**WS download protocol:**
1. Client → Server (text): `{"auth":"<authToken>","totalSize":1048576,"frameSize":1048576}`
2. Server → Client (text): `{"ok":true,"size":1048576}` (`size` echoes `totalSize`) or
   `{"ok":false,"error":"..."}`
3. Client → Server (text, repeated): `{"start":0,"size":1048576}` (reuses `wsChunkRequest`, the same
   type `/api/files/ws/download` uses) or `{"done":true}` to end the session
4. Server → Client (binary, one or more messages, each ≤ `frameSize`): the requested chunk of the
   synthesized file
5. On any error: server sends `{"ok":false,"error":"..."}` then closes

**WS upload protocol:**
1. Client → Server (text): `{"auth":"<authToken>","frameSize":1048576}` (no filename)
2. Server → Client (text): `{"ok":true}` or `{"ok":false,"error":"..."}`
3. Client → Server (binary, one or more messages, each ≤ `frameSize`): raw bytes to hash; the running
   total is checked against `simMaxTotalSize` on every message, aborting the connection if exceeded
4. Client → Server (text, final): `{"done":true}`
5. Server → Client (text, final): `{"ok":true,"hash":"<hex>"}`, then a normal close
6. On any error: server sends `{"ok":false,"error":"..."}` then closes

## Authentication

All API endpoints except `/api/public/checkin` and `/api/public/settings` require an `X-Auth` header
matching the server's derived auth token (the two WebSocket endpoints are also unauthenticated at
the HTTP/upgrade level, but check the token in-band — see WebSocket Transport below).

The raw key configured by the operator is never sent over the wire. `internal/authhash.Hash(key)`
derives the actual token from it: a labeled HMAC-SHA256 round — `HMAC-SHA256(key, "warpgate-auth-token-v1")`
— seeds 1000 rounds of plain SHA-256 chaining the digest, hex-encoded at the end. The label domain-
separates this from `internal/paramcrypt.DeriveKeys`'s two labeled subkeys (see "Parameter Encryption"
below) — knowing one gives no information about, and can't be confused with, the other. Both the
server and every client (frontend, `wgcli`) compute this independently from the same raw key rather
than one side transmitting a derived value to the other. `internal/authhash` is imported by both the
server and `cmd/wgcli`, the same pattern `internal/transform` uses, so the two can't drift apart;
`web/app.js`'s `hashAuthKey()` is a separate, independently-verified JS reimplementation (frontend
JS can't import the Go package), reusing the same `hmacSha256Bytes()` helper `paramCrypt`'s key
derivation uses for its own labeled HMAC round.

- Set the key with `-auth-key <key>`. If omitted and `-no-auth` is not set, a random 32-byte hex key is auto-generated and printed to the log at startup. `Server.authHash` (`server.go`) is computed once from this key at startup, not per-request.
- `-no-auth` disables authentication entirely (no header check).
- The frontend persists the raw key in `sessionStorage`, separately from the rest of the unified UI settings (see "UI settings persistence" below), and restores it on reload within the same tab/session; the derived token is cached separately in the non-persisted `authKeyHash` property, recomputed via `updateAuthKeyHash()` whenever the key changes (including once at `init()`, awaited before the first request goes out) rather than on every request.
- The side panel has an **Auth** section where the user enters the raw key (displayed masked; a toggle reveals it). Changing it immediately refreshes the file listing.
- `wgcli`'s `-auth-key` flag likewise takes the raw key; `globalFlags.authToken()` derives the token sent on the wire.

Auth is enforced via `withAuth(h http.HandlerFunc) http.Handler` in `server.go`, applied at route registration time. The dispatch loop in `handler()` is unconditional.

## Parameter Encryption

Request control parameters (file paths, transform keys/passwords, filenames) are normally sent as
plaintext JSON — including the very transform keys that disguise the response body — so a proxy that
terminates TLS and inspects request bodies learns everything it needs to defeat the content disguise
too. Parameter encryption is an opt-in mechanism, independent of the transform system, that encrypts
these JSON blobs client-side using a key derived from the same shared `-auth-key`.

**Wire format**: `base64(nonce[16] || AES-CTR-ciphertext || HMAC-SHA256[32])`. The nonce is drawn
fresh per message by the client (`window.crypto.getRandomValues` — not `crypto.subtle`, which requires
a secure context; the raw-getRandomValues call has no such requirement). The HMAC-SHA256 covers
everything preceding it in the blob (`nonce || ciphertext`) — i.e. everything except the MAC itself —
using Encrypt-then-MAC, so any tampering is detected before the ciphertext is ever decrypted.

**Key derivation** (`internal/paramcrypt.DeriveKeys`): two independent subkeys, one for AES-CTR
encryption and one for the HMAC, each derived from the raw auth key via a distinctly labeled
HMAC-SHA256 (`warpgate-param-enc-v1` / `warpgate-param-mac-v1`). This is deliberately unrelated to
`internal/authhash.Hash`'s derivation of the X-Auth token, which uses its own distinct label
(`warpgate-auth-token-v1`, see "Authentication" above) — knowing one gives no information about the
other.

**Server auto-detect, no negotiation**: `Server.decodeParams` (`paramdecode.go`) tries `json.Unmarshal`
on the raw body first; if that fails and the server has derived param-crypt keys (i.e. auth is
enabled), it treats the body as the base64 blob above, decrypts, and parses the result as JSON
instead. Either wire format is accepted from any client at all times — there is no server flag to
require or reject one or the other. Any decode failure (malformed input, bad base64, or a MAC
mismatch) is reported as `401 Unauthorized`, the same as a bad `X-Auth` value, rather than `400`; the
client can't reliably tell which occurred, and the frontend already denies the user access on a key
mismatch regardless.

**Scope**: every client→server JSON request body (`/api/files/list`, `/api/files/download`,
`/api/clip/new`, `/api/clip/edit`, `/api/clip/delete`, `/api/sim/download`); only the JSON `transform`
part of the multipart upload endpoints (`/api/files/upload`, `/api/sim/upload`) — the binary `file`
part is untouched, since it's already independently protected by the transform system; and every
client→server WebSocket control message (not just the handshake) for all four WS endpoints — the
handshake, `{"start":...,"size":...}` chunk requests, and `{"done":true}`. Unavailable entirely under
`-no-auth` (there's no shared secret to derive keys from); `decodeParams` doesn't attempt the decrypt
fallback in that mode.

**Frontend**: a checkbox in the side panel's **Auth** section ("Parameter obfuscation",
`settings.paramCrypt.enabled`, disabled when no auth key is set) toggles this per-client — purely a
local preference, not negotiated with the server. `paramEncKeyBytes`/`paramMacKeyBytes` are derived
from `settings.auth.key` and cached the same way `authKeyHash` is (recomputed via
`updateParamCryptKeys()` in `init()` and the `settings.auth.key` watcher, not per-request).
`encodeBody(obj)` (returns either `JSON.stringify(obj)` or the encrypted blob) and `sendWSControl(ws,
obj)` are the two choke points every request body and WS control send routes through, so the toggle
only needs to be checked in one place per transport. AES-CTR uses the same `aes-js`
(`web/aes.js`) construction as the AES-CTR file-content transform; HMAC-SHA256 uses a vendored
`js-sha256` (`web/sha256.js`) rather than `crypto.subtle`, for the same secure-context reason as the
nonce generation above — this feature must work over plain HTTP, which is the whole point of the tool.

**`wgcli` implements parameter encryption** via its own `-param-crypt` flag (`cmd/wgcli/client.go`),
importing `internal/paramcrypt` directly — the same pattern `internal/authhash`/`internal/transform`
already use to keep the server and `wgcli` from drifting apart. `-param-crypt` alone isn't enough to
enable it: `globalFlags.paramKeys()` also requires `-auth-key` to be set, mirroring the frontend
checkbox being disabled with no auth key. `cmd/wgcli/paramcrypt.go` is the client-side equivalent of
the frontend's `encodeBody`/`decodeResponseJSON` choke points: `encodeBody` marshals-then-optionally-
encrypts every JSON request body, multipart `transform` part, and WS control message; `decodeJSON`
reverses it for every in-scope response. Unlike the server's auto-detecting `decodeParams`, `wgcli`
doesn't need to guess whether a response is encrypted — it already knows its own `-param-crypt`
setting for the whole invocation, the same fixed choice for every request. `maybeEncryptedQuery`
appends `?e=1` for the two GET endpoints (`ul-list`, `clip/list`) with no request body to key a
response off of. WS handshakes/chunk-requests/`done` messages route through `writeWSJSON`; acks
(including the payload materialized in the `websocket.BinaryMessage`-mismatch/mid-transfer error
path) are decoded via the updated `readWSAck(conn, g)`.

### Response encryption

The server encrypts a JSON response back to the client under the exact same wire format and keys —
but **only when the specific request/control-message it's answering itself arrived encrypted**. There
is no independent server-side setting for this and no separate opt-in: it is purely a mirror of
whatever the request already was.

- `Server.decodeParams` (`paramdecode.go`) returns `(encrypted bool, err error)` instead of just
  `error` — `encrypted` reports whether the decrypt-fallback path was used for that particular call.
  The sibling `Server.encodeParamsResponse(w, encrypted, status, v)` writes `v` as either an encrypted
  blob (`Content-Type: text/plain`) or plain JSON (`Content-Type: application/json`), based on that
  same bool threaded in from the request's own decode.
- In scope: the JSON response bodies of `/api/files/list`, `/api/files/upload`, `/api/clip/new`,
  `/api/sim/upload` (all four have both a request body to key off of and a JSON response worth
  protecting), and `/api/files/ul-list`/`/api/clip/list` via the `?e` query parameter below. Out of
  scope: `/api/clip/edit`/`/api/clip/delete` (204, no body to encrypt), and
  `/api/files/download`/`/api/sim/download` (raw file bytes, not JSON — already covered by the
  transform system).
- **`GET /api/files/ul-list` and `GET /api/clip/list` have no request body to mirror**, so they use a
  separate signal: the query parameter `?e=<anything>` (presence-only, value ignored, deliberately
  terse rather than descriptive — see `handleUlList`/`handleClipList`, which compute
  `encrypted := !s.cfg.NoAuth && r.URL.Query().Has("e")` and call `encodeParamsResponse` the same way
  every other in-scope handler does). Unlike every other signal in this feature, `?e` is **not
  cryptographically bound to anything** — it's a bare client-controlled hint the server trusts at face
  value, not something derived from a successful decrypt. An on-path party could strip it from the
  URL, causing a silent downgrade to a plaintext response with no error and nothing for the client to
  detect; this is an accepted, deliberate tradeoff (no client-side enforcement was added to reject an
  unexpectedly-plaintext response), unlike every other case in this feature where tampering causes a
  hard failure (401) rather than a silent one. Ignored entirely under `-no-auth`, same as everywhere
  else. `loadUploadDir()`/`loadClipboard()` (`web/app.js`) append `?e=1` to the URL when
  `paramEncryptEnabled()` and use the same `decodeResponseJSON` as every other GET-with-encryption call
  site.
- **WebSocket connections decide once, at the handshake, for the whole connection.** `readWSControl`
  (`ws.go`) returns `(T, encrypted bool, error)`; each handler captures `encrypted` from its handshake
  read and reuses that same value for every ack it sends for the rest of that connection — later
  per-message reads (per-chunk requests, the `{"done":true}` message) have their own returned bool
  discarded, since a legitimate client never changes the setting mid-session. `writeWSOK`/
  `writeWSError` (`ws.go`, both methods on `*Server`) take that `encrypted` bool directly and either
  `conn.WriteJSON(v)` or marshal+`paramcrypt.Encrypt`+send as a text message.
- **Frontend**: `decryptParams(blob)` mirrors `encryptParams` in reverse (verify the HMAC via a
  constant-time `constantTimeEqualBytes`, then AES-CTR decrypt, then `JSON.parse`).
  `decodeResponseJSON(res, encrypted)` is the HTTP-side counterpart to `decodeParams` — but note the
  client doesn't auto-detect; it already knows whether *it* encrypted the request, so it just passes
  that same `encrypted` flag through to know how to read the response. `wsNextJSON(ws, decrypt)` takes
  an optional decrypt callback (`this.decryptParams.bind(this)` or `null`) instead of always calling
  `JSON.parse` directly. For the two WS flows with a persistent `wsSession` object reused across
  multiple calls (`fetchChunkWS`, `fetchSimChunkWS`), the encrypted flag is captured once into
  `wsSession.encrypted` alongside `wsSession.password`/`wsSession.htmlB64Key`, the same
  established pattern for connection-lifetime state; the two single-call WS flows (`uploadFileWS`,
  `uploadSimWS`) just use a local variable.

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

Implemented in the `warpgate/internal/transform` package (`internal/transform/`), imported by both
the server (`download.go`, `upload.go`, `ws_download.go`, `ws_upload.go`) and `cmd/wgcli` — this
guarantees the CLI test client's encode/decode behavior can't drift from the server's, since there's
no separate reimplementation.

### Interface (`internal/transform/transform.go`)

```go
type Transformer interface {
    Transform(r io.Reader) (io.Reader, error)   // encode (download path)
    Untransform(r io.Reader) (io.Reader, error) // decode (upload path)
}
```

`Transform` is called server-side when serving a download chunk; the browser reverses it.
`Untransform` is called server-side when receiving an upload; the browser pre-encoded the file.
For symmetric transforms (XOR, AES-CTR) `Untransform` delegates to `Transform`.
For asymmetric transforms (PDF) they are separate operations.

A transformer may optionally implement `ContentType() string`; `handleDownload` (`download.go`)
checks for this via a local `contentTyper` interface and uses the returned value as the HTTP
response's `Content-Type`, falling back to `application/octet-stream` for transforms that don't
implement it. `html-b64` and `pdf` both implement it (`text/html; charset=utf-8` and
`application/pdf` respectively), matching the disguised content they actually produce.

New transforms are registered in the `transformers` map in `internal/transform/transform.go`:
```go
var transformers = map[string]TransformerFactory{
    "id":       newIDTransformer,
    "xor":      newXORTransformer,
    "aes-ctr":  newAESCTRTransformer,
    "pdf":      newPDFTransformer,
    "html-b64": newHTMLB64Transformer,
}
```

### id (`internal/transform/transform_id.go`)

No-op. Both methods return the reader unchanged. Args: none.

### XOR (`internal/transform/transform_xor.go`)

Streams data XOR-ed with a repeating key. Symmetric.
Implemented as a streaming `xorReader` wrapping an `io.Reader`.
Args: `{"key": "<hex>"}` — key repeats cyclically, any length.

Frontend: byte loop using `hexToBytes`; same loop for upload encode and download decode.

### AES-CTR (`internal/transform/transform_aes.go`)

Streams data through AES in CTR mode via Go's `crypto/cipher`. Symmetric.
Args: `{"key": "<hex>", "iv": "<hex>"}`.
Key must be 16, 24, or 32 bytes; IV must be exactly 16 bytes (one AES block).

Frontend: uses `aes-js` (`web/aes.js`) — `ModeOfOperation.ctr`. Key and IV auto-generated randomly when "auto" is ticked.

### PDF (`internal/transform/transform_pdf.go` / frontend)

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

### HTML/Base64 (`internal/transform/transform_html_b64.go` / frontend)

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

- `index.html` — Alpine.js v3 SPA with **Download**, **Upload**, **Clipboard**, and **Benchmark** tabs
- `app.js` — single `filesApp()` factory function registered with Alpine; no build step required
- `style.css` — dark theme
- `aes.js`, `alpine.min.js`, `pdf.min.js`, `pdf.worker.min.js`, `pdf-lib.min.js` — vendored libraries

All JS libraries are served locally (embedded in the binary); no CDN requests at runtime.

### Key frontend logic

- `authHeaders()` — returns `{"X-Auth": key}` if key is non-empty, else `{}`. Merged into every API request.
- `encodeBytesForUpload(bytes, filename)` — async; the transform-type switch shared by the whole-file
  and per-chunk upload paths, resolving to `{data: Blob, args: {}}` for whatever `bytes` it's given.
- `encodeForUpload(file)` — async; reads the whole file and delegates to `encodeBytesForUpload`.
- `decrypt(buffer, password, htmlB64Key)` — reverses the transform on a downloaded chunk's `ArrayBuffer`.
- `fetchChunk(wsSession, path, start, size)` — dispatches to `fetchChunkHTTP` (POSTs to `/api/files/download`) or `fetchChunkWS` based on the `settings.backend` setting, calling `decrypt` on the (re)assembled response either way. `wsSession` is a plain `{ws, password, htmlB64Key}` object owned by the caller (`downloadFile`), threaded through repeated calls for the same file so the WS backend reuses one connection per file instead of opening one per chunk; `closeWSSession` ends it. Ignored for the HTTP backend.
- Chunked downloads with optional reassembly via the File System Access API (`showSaveFilePicker`). Requires a secure context (HTTPS or localhost); the checkbox is disabled otherwise.
- `uploadFiles(files)` — dispatches each file to `uploadFileHTTP` or `uploadFileWS` based on `settings.backend`, in parallel, then refreshes the upload listing. `uploadFileHTTP` further dispatches to `uploadFileHTTPChunked` when `settings.uploadChunking.enabled` — HTTP backend only, see "`/api/files/upload` multipart form" above for the protocol.
- `load()` / `loadUploadDir()` / `loadClipboard()` — each compares the fetched data against the current state with `JSON.stringify` and only updates the reactive array on a change, preserving scroll position during auto-refresh.
- `textToBase64` / `base64ToText` — UTF-8–safe helpers used by the clipboard tab.
- `sha256Hex(buffer)` — wraps `crypto.subtle.digest('SHA-256', ...)`, hex-encoding via the shared `bytesToHex` helper (also used by `randomHex`). Used by the benchmark tab (see below); requires a secure context (HTTPS or localhost).
- `runDownloadBenchmark()` / `runUploadBenchmark()` — drive the benchmark tab; see "Benchmark tab" below.

### UI settings persistence

Everything under Alpine's `settings` object (backend choice, auto-refresh, confirm-delete, WS frame
size, transform config, chunking config, benchmark size/chunk-size config, and side-panel
open/collapsed state) is persisted as one JSON blob in `localStorage["warpgate-settings"]`, restored
on the next page load. Deliberately **not** persisted there: `tab`/`pathSegments` (navigation, not
configuration — a stale restored path could point at a since-deleted directory), `wsFrameMinKB`/
`wsFrameMaxKB` (fetched fresh from `/api/public/settings` every load, not user configuration),
`authShowKey` (whether the auth key is currently unmasked — always starts masked for basic
shoulder-surf hygiene, kept as a standalone top-level property outside `settings` specifically so it
can never end up in the persisted blob), and `benchmarkResults` (a run history, not configuration —
kept as a standalone top-level property for the same reason).

- **`settings.auth.key` is the one field of `settings` carved out of that blob.** It stays in the
  reactive tree (so every existing `x-model="settings.auth.key"` binding and watcher is unaffected),
  but is persisted separately in `sessionStorage["warpgate-auth-key"]` instead of `localStorage`, so
  it doesn't outlive the browser tab/session the way the rest of the settings do — closing the tab (or
  browser) clears it, and it isn't shared with a different tab either, since `sessionStorage` is
  per-tab. `saveSettings()` zeroes `auth.key` out of the object before serializing it to
  `localStorage` (which also scrubs a key persisted there by a pre-this-change client on the next
  save) and separately `sessionStorage.setItem`/`removeItem`s it; `loadSettings()` restores the rest
  of `settings` from `localStorage` as usual, then unconditionally overwrites `settings.auth.key` from
  `sessionStorage` (or `''` if absent) — so even a stale `localStorage` blob that still has a key in
  it from before this change can never leak the key back in.
- Shape: `{"version":1,"settings":{...}}` — mirrors the `settings` object exactly (auth key aside), so
  persistence is just "serialize `settings`, merge back into `settings`" with no separate allowlist to
  maintain. Any new UI-configurable value should be added under `settings`, not as a new top-level
  Alpine property, or it won't be persisted.
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
| Benchmark | Benchmark tab only; Size (MB, 1-1024) and Chunk size (MB, ≤ Size) for the synthetic transfer — see "Benchmark tab" below |
| Chunking  | Download tab: enable chunked download, set chunk size, enable File System Access API reassembly. Upload tab: enable chunked upload and set chunk size (HTTP POST backend only — hidden when Backend is Websocket) |
| General   | Auto-refresh (polls the active tab every second; re-renders only on changes); Confirm note delete; Backend (HTTP POST/Websocket, download+upload+benchmark tabs); WS frame size in KB (download+upload+benchmark tabs, only when Backend is Websocket; default 4M, clamped to the server's `-ws-frame-max` via `/api/public/settings`); Content-Type (download tab only, only when Backend is HTTP POST — a preset dropdown of common MIME types plus "Custom…", overriding the download response's `Content-Type` header; see `/api/files/download`'s `contentType` field above) |
| Transform | Select transform type and configure its parameters (auto-generate keys/IVs) |

Every setting in the side panel has a `title` attribute (on both its `<label>` and its input/select)
giving a native browser tooltip on hover — kept short (1-2 sentences) and factual, not restating the
label text.

### Clipboard tab

- Large textarea for composing a note; Submit button creates a new note via `/api/clip/new`.
- Clicking ✏ on a note pre-fills the textarea for editing; the button becomes "Update" / "Cancel". Pressing Escape or clicking "Cancel" discards the edit.
- Each note shows its timestamp and decoded text, with footer action buttons: ⬇ (download as `.txt`), 📋 (copy to system clipboard), ✏ (edit).
- ✕ delete button in the note header; optionally prompts for confirmation (controlled by "Confirm note delete" in the General panel section).

### Benchmark tab

Drives `/api/sim/*` (see "Benchmark/Simulation Endpoints" above) to probe which transport/frame size
gets through a given proxy, independent of any transform.

- "Run Download Benchmark" / "Run Upload Benchmark" / "Clear results" buttons, and a results table
  (newest run first): Direction, Backend, Size, Chunk/Frame, Duration, Throughput, Status.
- Both directions verify a SHA-256 hash end-to-end, not just a byte count, so a proxy that corrupts
  even a single byte in transit shows up as a failed run rather than a silently-wrong-but-"successful"
  one:
  - **Download**: the synthesized content is a pure function of `totalSize` (see `simReader`), so
    `buildExpectedSimFile(totalSize)` reproduces it client-side and `sha256Hex` hashes it — no network
    round trip needed to know the expected hash. Chunks are written into a preallocated
    `Uint8Array(totalSize)` at the correct offset (`fetchSimChunkHTTP`/`fetchSimChunkWS` return the
    chunk's bytes instead of just validating its length) and hashed again after the transfer; a
    mismatch is reported as `"hash mismatch (data corrupted in transit)"`.
  - **Upload**: `generateRandomBuffer` fills a buffer via `crypto.getRandomValues` (called in ≤64 KiB
    slices — its per-call limit) and hashes it locally before sending; the hash the server reports
    back (see `simUploadResponse`/`wsAck.Hash`) must match. On the HTTP backend, when the configured
    chunk size is smaller than the total size, `uploadSimHTTPChunked` splits the buffer across
    several `/api/sim/upload` requests using the same continuation-key protocol as the real Upload
    tab's `uploadFileHTTPChunked` (see "Chunked sim upload" under "Benchmark/Simulation Endpoints"
    above) — this exercises the server's chunked-upload session machinery itself, not just the
    transform/HTTP transport. The
    final chunk's response carries the hash; intermediate chunks don't (`data.hash` is only set once
    `ul-done: true` succeeds). The WS backend has no continuation-key equivalent (see "WebSocket
    Transport" above), so `uploadSimWS` always sends the whole buffer as one connection's frames
    regardless of the configured chunk size — only frame size (see below) varies its request shape.
  - Both directions time only the network portion — the expected/local hash is computed *before* the
    timer starts (download) or before the upload call (upload), so local CPU hashing time doesn't
    pollute the reported duration/throughput.
  - `crypto.subtle` (used by `sha256Hex`) requires a secure context (HTTPS or localhost); if
    unavailable, a run immediately fails with a message saying so rather than throwing.
- Size (1-1024 MB) and Chunk size (≤ Size) are configured in the Benchmark side-panel section; Chunk
  size splits both download (always) and HTTP-backend upload (see above) into multiple requests.
  Backend and (when Websocket) WS frame size are the same settings the Download/Upload tabs use.

## Go Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/pdfcpu/pdfcpu` | v0.13.0 | Create PDFs, add/extract attachments, AES-256 encryption |
| `modernc.org/sqlite` | v1.53.0 | Pure-Go SQLite driver (no CGO); clipboard storage |
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket transport for `/api/files/ws/{download,upload}` and `/api/sim/ws/{download,upload}` |

## Development

```bash
# Run (cmd/wgcli is a second main package, so ./... is ambiguous for `go run`)
go run .

# Build
go build ./...

# Test
go test ./...

# Lint (install golangci-lint if needed)
golangci-lint run

# End-to-end verification (builds warpgate + wgcli, exercises every transform/backend
# combo plus clipboard and edge cases against a throwaway server instance)
python3 test/e2e_suite.py --quick   # fast sanity run
python3 test/e2e_suite.py           # full run, up to 1G files; see test/README.md
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
