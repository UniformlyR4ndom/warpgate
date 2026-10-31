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
(for testing and scripted/agent use: global flags fall back to `WGCLI_*` environment variables,
flags may follow positional arguments, transform keys default to random, `-cert`/`-cert-key` for
TLS client auth, `wgcli register` for the IP firewall, `-json` output, filtered/incremental
`clip list` and `clip get`, image-only notes via `clip new -attach`); see the `wgcli` skill for usage details.

## Architecture

### API endpoints

| Method   | Path                    | Auth required | Description |
|----------|-------------------------|---------------|-------------|
| GET      | `/api/public/checkin`   | No            | Logs client IP (see `clientIP`); called once on page load |
| GET      | `/api/public/settings`  | No            | Returns `{"wsFrameMin":...,"wsFrameMax":...}`, the server's configured WS frame size bounds, for UI clamping |
| POST     | `/api/files/list`       | Yes           | List files in the download root; body: `{"path":"subdir"}` |
| POST     | `/api/files/download`   | Yes           | Download a (chunk of a) file with a transform applied |
| POST     | `/api/files/upload`     | Yes           | Upload a (chunk of a) file (requires `-ul-root`); multipart form |
| GET      | `/api/files/ul-list`    | Yes           | List files in the upload directory |
| POST     | `/api/ul/list`          | Yes           | Like `/api/files/list`, in the upload directory (requires `-ul-root`) |
| POST     | `/api/ul/download`      | Yes           | Like `/api/files/download`, from the upload directory (requires `-ul-root`) |
| GET/Upgrade | `/api/ul/ws/download` | Yes (in-band) | Like `/api/files/ws/download`, from the upload directory (requires `-ul-root`) |
| GET/Upgrade | `/api/files/ws/download`  | Yes (in-band) | WebSocket alternative to `/api/files/download`; see below |
| GET/Upgrade | `/api/files/ws/upload`    | Yes (in-band) | WebSocket alternative to `/api/files/upload` (requires `-ul-root`); see below |
| POST     | `/api/sim/download`     | Yes           | Download a chunk of a synthesized file, no transform applied; see "Benchmark/Simulation Endpoints" |
| POST     | `/api/sim/upload`       | Yes           | Upload a (chunk of a) file for SHA-256 hashing (requires `-ul-root`), nothing written to disk; see below |
| GET/Upgrade | `/api/sim/ws/download`  | Yes (in-band) | WebSocket alternative to `/api/sim/download`; see below |
| GET/Upgrade | `/api/sim/ws/upload`    | Yes (in-band) | WebSocket alternative to `/api/sim/upload` (requires `-ul-root`); see below |
| GET      | `/api/clip/state`       | Yes           | Incremental sync; query `since=<rev>` → `{rev,changed,ids,reset}` (see "Clipboard sync") |
| POST     | `/api/clip/new`         | Yes           | Create a clipboard note; body: `{"title":"...","data":"<base64>","source":"outbox"}` (`title`, `source` optional) |
| POST     | `/api/clip/edit`        | Yes           | Update a note's content and timestamp; body: `{"id":<id>,"title":"...","data":"<base64>"}` (`title` omitted = keep) |
| DELETE   | `/api/clip/delete`      | Yes           | Delete a note by ID; body: `{"id":<id>}` |
| POST     | `/api/clip/attachment`  | Yes           | Attach an image to a note; multipart (`transform` part `{transform,args,note_id,mime,caption}` + `file`), ≤20 MiB |
| POST     | `/api/clip/attachment/download` | Yes   | Fetch an image's bytes, transform-disguised; body: `{id,transform,args,contentType?}` |
| POST     | `/api/clip/attachment/edit`     | Yes   | Set one image's caption; body: `{"id":<id>,"caption":"..."}` |
| DELETE   | `/api/clip/attachment/delete`   | Yes   | Delete one image by ID; body: `{"id":<id>}` |
| GET      | `/static/...`           | No            | Embedded frontend (HTML, JS, CSS) |

`/` and `/static` redirect to `/static/`.

### Path prefix

`-path-prefix <segment>` (`pathprefix.go`) mounts *everything* — UI, `/api/public/*`, and the WS
endpoints — under `/<segment>`, to keep a publicly hosted instance out of plain sight. The value is
one path segment of `[A-Za-z0-9._~-]` (leading `/` optional; `.`/`..` rejected), normalized to
`/<segment>` by `normalizePathPrefix`; empty (the default) disables it and leaves routing untouched.
`withPathPrefix` wraps `Server.handler()`: a path that is exactly `<prefix>` or starts with
`<prefix>/` has the prefix stripped (and `RawPath` cleared) before normal routing; anything else —
any method, including `<prefix>foo` — gets an identical empty `200 text/html; charset=utf-8`
response, with no Go-specific body or redirect, to be hard to fingerprint. The `/` → `/static/`
redirect becomes `<prefix>/static/`.

The frontend needs no configuration: `API_BASE` (`web/app/store.js`) is derived from `location.pathname`
(everything before `/static/`) and prepended to every `fetch` URL and to `wsBaseURL()`. Static assets
already use relative URLs. `wgcli` just takes the prefix as part of `-url`.

### Filesystem confinement

Which root a request uses is fixed by its route, never by a request field: `registerBrowse`
(`server.go`) registers the same list/download handlers under `/api/files/` with `Server.root` and
under `/api/ul/` with `Server.ulRoot`.

All file access goes through `os.Root` handles opened once in `NewServer` (`Server.root`,
`Server.ulRoot`); a missing `-root`/`-ul-root` is a startup error. Client paths become root-relative
via `rootRelPath` (`path.go`). Symlinks that stay inside their root work (listed by their target's
type and size); one leaving the root — including any absolute link and, on Windows, any junction —
is refused (`isPathEscape`, `403` / WS error `errSymlinkEscape`), and `listDir` leaves it and
broken links out of listings, logging each one every time a listing hits it (only an operator
can create such links). Uploads
are published by renaming within `ulRoot`, which replaces a symlink at the target instead of
writing through it. Hard links and bind mounts are not detected.

`main.go`'s `checkNotServed` refuses to start when `-db`, `-cert` or `-cert-key` lies inside
`-root` or `-ul-root`, comparing directory identity (`fileInsideDir`, `path.go`), so symlinks and
bind mounts on either side are seen through; the file need not exist yet.

### IP firewall

`ipfilter.go`, enabled (`Server.ipFilter != nil`) when any of `-ip-allow`, `-ip-reg-path`,
`-ip-reg-max`, `-ip-reg-window` is set; built and validated by `newIPFilter`. `Server.withIPFilter`
runs *inside* `withPathPrefix` (it sees prefix-stripped paths) and in front of all routing, so it
covers static files, the API, and WS upgrades alike. Unrelated to `/api/public/checkin`, which only
logs.

- A request is served if its client IP is in `-ip-allow` (IPs/CIDRs, `parseIPList`) or registered.
  Everything else gets `writeBlank` — the same response as a wrong prefix.
- **Registration path**: `-ip-reg-path <segment>`, or — when only `-ip-reg-max`/`-ip-reg-window` are
  set — the base path `/` (i.e. `<prefix>`/`<prefix>/`), which then requires `-path-prefix` (a bare
  `/` would let internet noise claim the slots). In base-path mode `<prefix>/static/` (exactly) is a
  second registration URL, so reloading the UI after a restart re-registers; it holds no less secret
  than the base path. A dedicated `-ip-reg-path` is the only registration URL in its mode, since the
  UI URL doesn't contain it. While registration is open (window not expired,
  `len(registered) < regMax`), GET returns `regPageHTML`, a page that auto-submits an empty form
  POST to the same URL; only the POST registers, then 303s to `<prefix>/static/`. The page's one inline
  script is allowed by its own CSP (`regPageCSP`, `csp.go`) via a SHA-256 hash, replacing the global one. This keeps link
  scanners/proxies that just GET the URL from consuming slots. Admitted IPs bypass registration handling
  entirely (so `/static/` serves the UI), except on a dedicated `-ip-reg-path`, which 303s them to the
  UI.
- `wgcli register [registration-url]` (default: `-url`) sends the same empty form POST, without
  running any script, and reports success when the response redirects to `.../static/`. Every other
  wgcli request reports the blank page as an explanatory error instead of failing to decode it.
- `-ip-reg-max`/`-ip-reg-window` 0 mean unlimited; the window counts from `NewServer`. Existing
  registrations outlive the window. State is an in-memory map — lost on restart.
- **Client IP** (`clientIP`, also used by `handleCheckin`'s log line): the TCP peer, normalized
  (IPv4-mapped IPv6 unmapped). Only if the peer is in `-trusted-proxies` is `X-Forwarded-For` walked
  right to left, skipping trusted hops; the first untrusted entry is the client. A missing header, a
  malformed entry or a chain of only trusted hops leaves the client unknown: the IP firewall then
  blocks the request (fail closed), and the check-in log shows the peer marked "client unknown".

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
1. First chunk: `continuation-key` omitted. Behaves like a one-shot upload — starts a new temp file
   that will replace the target unconditionally, discarding any prior state (even an in-progress or
   completed session) recorded for that filename. `transform`/`args` apply to *this chunk only*: each chunk is
   transformed independently (its own `Untransform` call), not as one transform spanning the whole
   file, so `pdf`/`html-b64` chunks are self-contained per chunk, the same way download chunks
   already are.
2. If the resolved `ul-done` (see the defaulting rule above) is not `true`, the response includes a
   `continuation-key`: more chunks are expected.
3. Subsequent chunks: `continuation-key` set to the value from the previous response; the decoded
   bytes are appended to the session's temp file. Each successful non-final chunk's response carries a
   freshly-rotated `continuation-key` for the next chunk.
4. Final chunk: `ul-done: true`. The temp file is renamed over the target; the response omits
   `continuation-key`.

Implemented in `ul_session.go` (session/locking state, generic over any session payload type — see
below) and `upload.go` (`handleUpload`, `handleUploadFirstChunk`, `handleUploadContinuation`). The
identical protocol, sharing this same `ul_session.go` machinery, also powers `/api/sim/upload`'s
chunked benchmark upload — see "Benchmark/Simulation Endpoints" below.

- **Locking**: a per-filename claim (`sessionStore.tryClaim`/`release`, called via
  `Server.uploadSessions`, a `*sessionStore[*ulSession]`) rejects a request outright (`409`) rather
  than queuing it if another request for the same filename is already being processed —
  deliberately simple given chunks are expected to be sent strictly one at a time, each awaited
  before the next (`web/app/lib/upload.js`'s `uploadFileHTTPChunked` does this).
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
  write error, or the request exceeding `ulChunkProcessTimeout`) deletes both the temp file and
  the entire session — there is no partial-failure recovery; the client must restart the upload from
  scratch.
- **Expiry**: `ulSweepExpired` purges sessions idle longer than `ulSessionIdleExpiry` (both in
  `ul_session.go`; currently the same value as `ulChunkProcessTimeout` but an independent constant —
  one bounds a single request's own processing time, the other bounds the gap between successive
  chunks). An active (incomplete) session's temp file is deleted on expiry.
- **Temp-file publication**: every upload (HTTP one-shot, chunked, WS) is written to a temp file in
  `<ul-root>/.warpgate-tmp/` (`ulTmpDirName`, `upload.go`) and renamed over the target only on
  success, so the target always holds either the previous complete version or nothing — no listing
  hiding or download blocking is needed while an upload runs, and an aborted upload never touches it.
  `NewServer` clears and recreates the directory at startup (`initUlTmpDir`). Uploads land flat in
  `-ul-root` (`ulTarget` keeps only the base name). The name is reserved at any depth:
  `rootRelPath` and `ulTarget` reject any segment equal to it ignoring case and trailing dots/spaces
  (`isReservedName`, `400`). As the actual guarantee, `Server.reachesUlTmp` compares every prefix of
  a path against the directory's identity (`os.SameFile` with `Server.ulTmpInfo`) and refuses a
  match (`403`, `errUlTmpPath`), catching other spellings (8.3 short names, symlinks);
  `withoutUlTmp` drops it from listings. This holds whether `-root` equals, contains or is
  unrelated to `-ul-root`.
- **WS uploads** share the HTTP per-filename claim for the whole connection, and like a first chunk
  supersede any session on record.

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

Pre-auth limits (`Server.upgradeWS`, `ws.go`): right after upgrade the read limit is
`maxParamBodySize` and the first message must arrive within `wsAuthTimeout` (10 s);
`Server.authorizeWS` lifts the deadline once the token checks out. The upload endpoints then raise
the read limit to the negotiated `frameSize`, so a larger message closes the connection with 1009.
The upgrader's buffers are a fixed `wsBufferSize` (64 KiB), independent of the frame size —
Gorilla's `WriteMessage` sends a payload of any size as one frame.

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
`bytesize.go`) is the server-operator-controlled *ceiling* on that per-request value. `NewServer` clamps a
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
- **Upload** (`ws_upload.go`): the server spools *all* binary frames for the file into a temp file
  (see "Temp-file publication"), and only calls `Untransform` once, when the `{"done":true}` message
  arrives — calling it per message would corrupt anything but the `id` transform.

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
- Not applicable to upload: `handleWSUpload` collects all frames first and calls
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
6. On any error or disconnect: server sends `{"ok":false,"error":"..."}` (if it still can) then
   closes; the temp files are deleted and the target is left as it was

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
upload variant hashes frames incrementally as they arrive rather than spooling them like
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
`web/app/lib/authhash.js`'s `hashAuthKey()` is a separate, independently-verified JS reimplementation (frontend
JS can't import the Go package), reusing the same `hmacSha256Bytes()` helper `paramCrypt`'s key
derivation uses for its own labeled HMAC round.

- Set the key with `-auth-key <key>`. If omitted and `-no-auth` is not set, a random 32-byte hex key is auto-generated and printed to the log at startup. `Server.authHash` (`server.go`) is computed once from this key at startup, not per-request.
- `-no-auth` disables authentication entirely (no header check).
- The frontend persists the raw key in `sessionStorage`, separately from the rest of the unified UI settings (see "UI settings persistence" below), and restores it on reload within the same tab/session; the derived token is cached in the non-persisted `authKeyHash` signal (`store.js`), recomputed whenever the key changes (including once in `initStore()`, awaited before the first request goes out) rather than on every request.
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
`/api/ul/list`, `/api/ul/download`,
`/api/clip/new`, `/api/clip/edit`, `/api/clip/delete`, `/api/clip/attachment/download`,
`/api/clip/attachment/edit`,
`/api/sim/download`); only the JSON `transform`
part of the multipart upload endpoints (`/api/files/upload`, `/api/sim/upload`) — the binary `file`
part is untouched, since it's already independently protected by the transform system; and every
client→server WebSocket control message (not just the handshake) for all four WS endpoints — the
handshake, `{"start":...,"size":...}` chunk requests, and `{"done":true}`. Unavailable entirely under
`-no-auth` (there's no shared secret to derive keys from); `decodeParams` doesn't attempt the decrypt
fallback in that mode.

**Frontend**: a checkbox in the side panel's **Auth** section ("Parameter obfuscation",
`settings.paramCrypt.enabled`, disabled when no auth key is set) toggles this per-client — purely a
local preference, not negotiated with the server. `paramEncKeyBytes`/`paramMacKeyBytes` are derived
from `settings.auth.key` and cached as signals the same way `authKeyHash` is (recomputed by
`store.js`'s `recomputeDerivedKeys()` at startup and on every key change, not per-request).
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
appends `?e=1` for the bodyless GET endpoints (`ul-list`; `clip list` builds `clip/state?since=0`
with `&e=1`) to key a response off of. WS handshakes/chunk-requests/`done` messages route through `writeWSJSON`; acks
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
- In scope: the JSON response bodies of `/api/files/list`, `/api/ul/list`, `/api/files/upload`, `/api/clip/new`,
  `/api/clip/attachment`, `/api/sim/upload` (each has both a request body to key off of and a JSON
  response worth protecting), and `/api/files/ul-list`/`/api/clip/state` via the `?e` query parameter
  below. Out of scope: `/api/clip/edit`/`/api/clip/delete`/`/api/clip/attachment/edit`/`/api/clip/attachment/delete` (204, no body
  to encrypt), and `/api/files/download`/`/api/sim/download`/`/api/clip/attachment/download` (raw
  bytes, not JSON — already covered by the transform system).
- **`GET /api/files/ul-list` and `GET /api/clip/state` have no request body to mirror**, so they use a
  separate signal: the query parameter `?e=<anything>` (presence-only, value ignored, deliberately
  terse rather than descriptive — see `handleUlList`/`handleClipState`, which compute
  `encrypted := !s.cfg.NoAuth && r.URL.Query().Has("e")` and call `encodeParamsResponse` the same way
  every other in-scope handler does). Unlike every other signal in this feature, `?e` is **not
  cryptographically bound to anything** — it's a bare client-controlled hint the server trusts at face
  value, not something derived from a successful decrypt. An on-path party could strip it from the
  URL, causing a silent downgrade to a plaintext response with no error and nothing for the client to
  detect; this is an accepted, deliberate tradeoff (no client-side enforcement was added to reject an
  unexpectedly-plaintext response), unlike every other case in this feature where tampering causes a
  hard failure (401) rather than a silent one. Ignored entirely under `-no-auth`, same as everywhere
  else. `loadUploadDir()`/`pollClipboard()` (`web/app/lib/upload.js`/`clipboard.js`) append `?e=1` (`&e=1` for the latter) to the URL when
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
  an optional decrypt callback (`wsDecryptFor(encrypted)`: `decryptParams` or `null`) instead of always calling
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

## Clipboard (`db.go`, `clip.go`, `clip_attachment.go`)

Notes are stored in a SQLite database (default `warpgate.sqlite`, configurable with `-db`). A note is
text (optionally titled) plus zero or more attached images, each with an optional caption.

### Schema

```sql
CREATE TABLE clipboard (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    data      TEXT    NOT NULL,
    title     TEXT    NOT NULL DEFAULT '',
    rev       INTEGER NOT NULL DEFAULT 0,     -- global clip_rev value at last create/edit
    source    TEXT    NOT NULL DEFAULT ''     -- '' = typed in the UI, 'outbox' = pushed by a local script, 'cli' = wgcli
)
CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL)  -- 'clip_rev', 'last_del_rev'
CREATE TABLE clip_attachment (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    note_id INTEGER NOT NULL REFERENCES clipboard(id),  -- deletes are explicit, not via cascade
    mime    TEXT    NOT NULL,
    size    INTEGER NOT NULL,
    data    BLOB    NOT NULL,
    caption TEXT    NOT NULL DEFAULT ''
)
```

`migrate` (in `OpenDB`) is idempotent: `addColumnIfMissing` adds `title`/`rev`/`source` and `caption`, pre-existing rows get
`rev = id` backfilled (monotonic, distinct, positive), and `meta` is seeded (`clip_rev` to
`MAX(rev)`, `last_del_rev` to 0) via `INSERT OR IGNORE`. `''` is the only representation of an
untitled note. `OpenDB` sets `SetMaxOpenConns(1)`: it keeps a `:memory:` test DB single-shared (each
modernc in-memory connection is otherwise its own database) and removes `SQLITE_BUSY` under write
contention; blob reads load into memory and release the connection before the network write, so the
lack of read parallelism costs nothing.

### Clipboard sync (`/api/clip/state`)

The clipboard is polled incrementally rather than re-fetched in full. `meta.clip_rev` is a global
monotonic counter bumped — in the same transaction as the data change — on **every** write (create,
edit, delete). Each note's `rev` records the `clip_rev` value at its last create/edit;
`meta.last_del_rev` records the `clip_rev` value at the most recent delete. Adding/removing an
attachment, or changing its caption, is an edit of its note (bumps the note's `rev`, not its display
timestamp).

`DB.ClipState(since)` (one read transaction) returns `(rev, changed, ids, reset)`:
- **`changed`**: notes with `rev > since` (new or edited) as `NoteMeta` — text inline, images as
  `AttachmentMeta` references only, never blob bytes.
- **`ids`**: the full set of live note ids, returned **only when `last_del_rev > since`** (a deletion
  happened since the caller's cursor); the client drops any held id not in this set. This is how
  deletions propagate without tombstones. At clipboard scale the id list is cheap, and it's omitted on
  ordinary edit/create polls.
- **`reset`**: true when `since > rev` (a reset/swapped DB); `since` is then treated as 0 and the
  client discards its state, taking `changed` as a full snapshot. `since=0` is also a full snapshot
  (every note has `rev ≥ 1`).
- `since == rev` short-circuits to `{rev}` only — the quiet idle poll.

All writes serialize (SQLite's exclusive write lock + `MaxOpenConns(1)`), so `clip_rev` is monotonic
and a client's cursor only moves forward (barring the explicit reset). Concurrent edits to the *same*
note are last-writer-wins (no per-note optimistic concurrency).

### `db.go`

- `OpenDB(path string) (*DB, error)` — opens/creates the SQLite file and initialises/migrates the schema.
- `DB.InsertClipboard(title, source string, data []byte) (id, ts int64, err error)` / `UpdateClipboard(id, *title, data)` / `DeleteClipboard(id)` — each bumps `clip_rev` in its own transaction; delete also sets `last_del_rev` and removes the note's attachments.
- `DB.InsertAttachment(noteID, mime, caption, data) (id, error)` — stores a BLOB and bumps the note's `rev`; returns `errNoteNotFound` if the note is gone. `UpdateAttachmentCaption(id, caption)` / `DeleteAttachment(id)` / `GetAttachment(id)` round it out.
- `DB.ClipState(since)` — the sync query above.
- `DB.ListClipboard()` — retained for tests only (not exposed via the API since `/api/clip/list` was replaced by `/api/clip/state`).

`NoteMeta` = `{ID, TS, Title, Source (omitted when empty), Data(base64 text), Attachments []AttachmentMeta}`; `AttachmentMeta` =
`{ID, Mime, Size, Caption (omitted when empty)}`.

### `clip.go` / `clip_attachment.go`

Handlers `handleClipState`, `handleClipNew`, `handleClipEdit`, `handleClipDelete` (`clip.go`) and
`handleClipAttachmentNew`, `handleClipAttachmentDownload`, `handleClipAttachmentEdit`,
`handleClipAttachmentDelete` (`clip_attachment.go`) — all registered via `withAuth`. Base64 decoding of the incoming `data` field
is done in the handler before passing raw bytes to the DB layer. `title` is a plain JSON string (not
base64), validated by `validClipTitle`: valid UTF-8, at most `clipTitleMaxLen` (200) characters, no
control characters (so single-line); invalid → `400`. `handleClipEdit` decodes it into a `*string` so
an omitted title (e.g. from an older client) keeps the stored one. `source` (`handleClipNew` only)
must be `''`, `clipSourceOutbox` (`"outbox"`) or `clipSourceCLI` (`"cli"`, sent by every
`wgcli clip new`), else `400`; edits never change it. The UI marks outbox notes with ⇪ and cli
notes with a robot icon, both in the accent color; like the outbox marker, `cli` is a hint for
cooperative clients, not a security property. An image
`caption` (on upload and via `handleClipAttachmentEdit`) follows the same rules as `title`.

Image bytes reuse the file transform system: upload sends transform-encoded bytes in the multipart
`file` part (server `Untransform`s, stores raw, 20 MiB cap, `image/...` mime only); download applies
`Transform` to the stored blob (reusing `resolveContentType` from `download.go`, with the stored mime
as fallback) and returns raw bytes — not param-crypted, since the transform already covers them. The
transform used to store an image is independent of the one used to fetch it later (storage is raw),
exactly like file upload vs. download.

## Content Security Policy (`csp.go`)

`withCSP` is the outermost wrapper in `Server.handler()`, so every response — UI, API, blank pages —
carries `contentSecurityPolicy`: `default-src 'none'`, then `script-src 'self'` and `style-src 'self'`
with no `unsafe-*`, `img-src 'self' blob:` (clipboard thumbnails), `connect-src 'self'` (fetch +
same-origin WS), `worker-src 'self'` (pdf.js worker), `base-uri`/`form-action 'none'`, `frame-ancestors
'none'`, and Trusted Types (`require-trusted-types-for 'script'; trusted-types pdfjs-worker`). This
works because the frontend has no inline `<script>`/`<style>`, no import map, no `eval`/`new Function`,
no string HTML sinks (`innerHTML`, `dangerouslySetInnerHTML`), and sets styles only via the CSSOM
(Preact's `style` prop). The one Trusted Types policy, `pdfjs-worker` (`web/app/main.js`), vouches only
for `pdf.worker.min.js`; the worker is created there and handed to pdf.js as `workerPort`, since pdf.js
would otherwise call `new Worker` with a plain string. Keep it that way: a CSP violation shows up only
as a console error, not a server-side failure. The IP registration page is
the one exception, with its own hash-based policy (see "IP firewall").

## Frontend (`web/`)

Preact + htm + `@preact/signals` + `deepsignal`, as native ES modules — no build step, no import map.

- `index.html` — `<div id="app">`, the classic vendored scripts that set globals (`pdf.min.js`,
  `pdf-lib.min.js`, `aes.js`, `sha256.js`), and `<script type="module" src="app/main.js">`
- `style.css` — dark theme
- `pdf.worker.min.js` — loaded by pdf.js
- `app/vendor/` — `preact`, `hooks`, `signals`, `signals-core`, `htm`, `deepsignal` ES modules; their
  internal bare import specifiers are rewritten to relative paths so no import map is needed
- `app/main.js` — boot: `initStore`, initial listing, auto-refresh effect, `render(App)`
- `app/store.js` — `settings` (persisted) and `state` (transient) `deepSignal` objects, the derived
  `authKeyHash`/`paramEncKeyBytes`/`paramMacKeyBytes` signals, `initStore`, `API_BASE`
- `app/nav.js` — `setTab`, `refreshActive` (Refresh button + auto-refresh), `onAuthKeyChange`
- `app/lib/` — logic, no rendering: `api.js` (auth headers, `encodeBody`/`decodeResponseJSON`/
  `sendWSControl` choke points, `closeWSSession`), `paramcrypt.js`, `authhash.js`, `ws.js`,
  `transform.js` (decode + upload encode), `transfers.js` (progress list, `updateIfChanged`),
  `util.js`, `outbox.js` (see "Outbox"), and one module per tab: `download.js`, `upload.js`,
  `clipboard.js`, `benchmark.js`
- `app/components/` — `html.js` (bound `html` tag + `text`/`num`/`check`/`sel` two-way-binding prop
  spreads), `app.js` (header, tabs), `panel.js`, `transfers.js`, `outbox.js` (panel section, notices, PowerShell dialog), one component file per tab
- `push-to-wgclip.ps1` — the PowerShell push function shown by the Outbox dialog (see "Outbox")

All JS libraries are served locally (embedded in the binary); no CDN requests at runtime.

**htm pitfalls.** htm has no notion of HTML void elements and closes tags positionally (`</div>` and
`<//>` just pop one level), so an unclosed `<input …>` silently swallows its following siblings and
shifts every later close. Always self-close void elements: `<input … />`, `<img … />`, `<br />`.

**Reactivity.** Components read `settings`/`state` fields directly during render and re-render when
those fields change (signals auto-tracking). Mutate through the proxies (`state.x = …`,
`state.list.push(…)`), and when keeping a reference to a pushed object, read it back from the array
(see `addTransfer`) so later mutations go through the proxy. Rendering is asynchronous: after an
`input` event, a dependent `disabled` attribute updates only on the next render.

### Key frontend logic

- `authHeaders()` — returns `{"X-Auth": token}` if a key is set, else `{}`. Merged into every API request.
- `encodeBytesForUpload(bytes, filename)` — async; the transform-type switch shared by the whole-file
  and per-chunk upload paths (and clipboard images), resolving to `{data: Blob, args: {}}`.
- `encodeForUpload(file)` — async; reads the whole file and delegates to `encodeBytesForUpload`.
- `decrypt(buffer, password, htmlB64Key)` — reverses the transform on a downloaded chunk's `ArrayBuffer`.
- `fetchChunk(wsSession, path, start, size)` — dispatches to `fetchChunkHTTP` (POSTs to `/api/files/download`) or `fetchChunkWS` based on the `settings.backend` setting, calling `decrypt` on the (re)assembled response either way. `wsSession` is a plain `{ws, password, htmlB64Key}` object owned by the caller (`downloadFile`), threaded through repeated calls for the same file so the WS backend reuses one connection per file instead of opening one per chunk; `closeWSSession` ends it. Ignored for the HTTP backend.
- Chunked downloads with optional reassembly via the File System Access API (`showSaveFilePicker`). Requires a secure context (HTTPS or localhost); the checkbox is disabled otherwise.
- `uploadFiles(files)` — dispatches each file to `uploadFileHTTP` or `uploadFileWS` based on `settings.backend`, in parallel, then refreshes the upload listing. `uploadFileHTTP` further dispatches to `uploadFileHTTPChunked` when `settings.uploadChunking.enabled` — HTTP backend only, see "`/api/files/upload` multipart form" above for the protocol.
- `load()` / `loadUploadDir()` — each compares the fetched data against the current state with `JSON.stringify` (`updateIfChanged`) and only updates the reactive array on a change, preserving scroll position during auto-refresh. The clipboard instead syncs incrementally via `pollClipboard`/`applyClipState` (see "Clipboard tab").
- `onAuthKeyChange()` — after a key change: `resetClipSync()`, reload the download listing (kept across tab switches), and refresh the active tab.
- `textToBase64` / `base64ToText` — UTF-8–safe helpers used by the clipboard tab.
- `sha256Hex(buffer)` — wraps `crypto.subtle.digest('SHA-256', ...)`, hex-encoding via the shared `bytesToHex` helper (also used by `randomHex`). Used by the benchmark tab (see below); requires a secure context (HTTPS or localhost).
- `runDownloadBenchmark()` / `runUploadBenchmark()` — drive the benchmark tab; see "Benchmark tab" below.

### UI settings persistence

Everything under `store.js`'s `settings` object (backend choice, auto-refresh, confirm-delete, WS frame
size, transform config, chunking config, benchmark size/chunk-size config, clipboard sort/search scope,
and side-panel open/section-collapsed state) is persisted as one JSON blob in
`localStorage["warpgate-settings"]`, restored on the next page load. Everything else lives in the
separate, never-persisted `state` object: navigation (`tab`/`pathSegments` — a stale restored path
could point at a since-deleted directory), `wsFrameMinKB`/`wsFrameMaxKB` (fetched fresh from
`/api/public/settings` every load), `authShowKey` (the auth key always starts masked), and
`benchmarkResults` (a run history, not configuration).

- **`settings.auth.key` is the one field of `settings` carved out of that blob.** It stays in the
  reactive tree, but is persisted separately in `sessionStorage["warpgate-auth-key"]` instead of
  `localStorage`, so it doesn't outlive the browser tab/session the way the rest of the settings do —
  closing the tab (or browser) clears it, and it isn't shared with a different tab either, since
  `sessionStorage` is per-tab. `saveSettings()` zeroes `auth.key` out of the object before serializing
  it to `localStorage` and separately `sessionStorage.setItem`/`removeItem`s it; `loadSettings()`
  restores the rest of `settings` from `localStorage`, then unconditionally overwrites
  `settings.auth.key` from `sessionStorage` (or `''` if absent) — so even a stale `localStorage` blob
  that still has a key in it can never leak the key back in.
- Shape: `{"version":1,"settings":{...}}` — mirrors the `settings` object exactly (auth key aside), so
  persistence is just "serialize `settings`, merge back into `settings`" with no separate allowlist to
  maintain. Any new UI-configurable value should be added under `settings`, not `state`, or it won't
  be persisted.
- `mergeSettings(target, source)` (`store.js`) recursively copies fields from the parsed stored blob
  onto the live `settings` object, but only for keys that already exist on the target — this ignores
  stale/unknown fields from an older schema version rather than grafting them on.
- `initStore()` runs `loadSettings()` before `applyAuto()` — that order matters: it lets a restored
  `auto: false` + custom key survive untouched, while `auto: true` still gets a fresh key generated
  every load (`applyAuto()` only overwrites when the auto flag is true).
- `saveSettings()` is triggered by an `effect` that evaluates `JSON.stringify(settings)` — reading
  every nested field subscribes the effect to all of them, so a change anywhere in the tree saves.

### Side panel sections (alphabetical order)

The panel (`components/panel.js`) is shown and hidden by the ⚙ Settings button next to Refresh
(`settings.panel.open`); Esc closes it, except while the lightbox or the PowerShell dialog is open
or when the key goes to a field outside the panel. While it is closed, an orange dot on the button
flags a state the user has to act on: `state.authFailed` (the last list/poll request got a `401`,
set by `noteAuthStatus` in `lib/api.js`) or an outbox in `reconnect`/`auth`/`error`.

| Section   | Purpose |
|-----------|---------|
| Auth      | `X-Auth` key input; masked by default with a 👁 reveal toggle (reveal state itself is not persisted); change triggers listing refresh |
| Benchmark | Benchmark tab only; Size (MB, 1-1024) and Chunk size (MB, ≤ Size) for the synthetic transfer — see "Benchmark tab" below |
| Chunking  | Download tab: enable chunked download, set chunk size, enable File System Access API reassembly. Upload tab: enable chunked upload and set chunk size (HTTP POST backend only — hidden when Backend is Websocket) |
| General   | Auto-refresh (polls the active tab every second; re-renders only on changes); Confirm note delete; Backend (HTTP POST/Websocket, download+upload+benchmark tabs); WS frame size in KB (download+upload+benchmark tabs, only when Backend is Websocket; default 4M, clamped to the server's `-ws-frame-max` via `/api/public/settings`); Content-Type (download tab only, only when Backend is HTTP POST — a preset dropdown of common MIME types plus "Custom…", overriding the download response's `Content-Type` header; see `/api/files/download`'s `contentType` field above) |
| Outbox    | Connect/reconnect/disconnect the outbox folder, status, last pushes/failures, "PowerShell function" dialog; all tabs, hidden without `showDirectoryPicker` (see "Outbox") |
| Transform | Select transform type and configure its parameters (auto-generate keys/IVs) |

Every setting in the side panel has a `title` attribute (on both its `<label>` and its input/select)
giving a native browser tooltip on hover — kept short (1-2 sentences) and factual, not restating the
label text.

### Clipboard tab

- Optional single-line title input (`clipTitle`) above a large textarea for composing a note; Submit button creates a new note via `/api/clip/new`. Control characters in the title are replaced by spaces client-side before sending. Submit is enabled (`canSubmitClip()`) when there's any text, title, or pending image.
- **Pasting an image** (`onPaste` → `handleClipPaste`) into the title or textarea captures `image/*` entries from `clipboardData.items` into `pendingAttachments` (shown as thumbnails with a ✕ and a caption input). On Submit the note is created/edited first, then each pending image is uploaded to it via `uploadAttachment` (`encodeBytesForUpload` + the multipart `/api/clip/attachment`). Pending images are *new* only; existing images are managed on the note card. To add images to an existing note: edit it, paste, Submit. (Capture uses the `paste` event, which works over plain HTTP; copying an image back out would need a secure context, so it's not in the core path.)
- Clicking ✏ on a note pre-fills title and textarea for editing; the button becomes "Update" / "Cancel". Pressing Escape (in either field) or clicking "Cancel" discards the edit.
- Each note shows its title (bold, only if non-empty — no placeholder for untitled notes), timestamp and decoded text, then a thumbnail per attachment (lazy-fetched once via `ensureAttachment` → `/api/clip/attachment/download`, from a `useEffect` in the `Thumb` component, decoded with the current transform, cached in `state.clipImages` by attachment id as an object URL; the `<img>` renders only once a `blob:` URL exists, so a `loading`/`error` placeholder never triggers a spurious image request). Click a thumbnail to open it in the lightbox (Esc closes). The caption, if any, is shown under the thumbnail (one line, cut off, full text as tooltip) and in the lightbox. ⬇ (shown on hovering a thumbnail, and in the lightbox) saves the cached decoded image without a new request, named by `attachmentFileName`: caption, else note title (`-<n>` appended when the note has several images), else `note-<id>-<n>`, plus an extension from the mime type unless the caption already ends with it; `safeFileName` turns whitespace runs into `-` and characters invalid in file names into `_` (also for the note `.txt` download). While editing a note, ✕ on its thumbnail marks the image for removal (`toggleRemoveAttachment`, ↺ undoes) and a caption input replaces the caption (`state.clipCaptionEdits`); removals and changed captions (`/api/clip/attachment/edit`) are applied on Update. Footer buttons: ⬇ (download as `.txt`, named after the title), 📋 (copy text to system clipboard), ✏ (edit).
- **Sync** (`pollClipboard` → `applyClipState`): the list is maintained incrementally against `/api/clip/state?since=<clipCursor>` (see "Clipboard sync" above) — upsert `changed`, drop ids absent from `ids`, honor `reset`; `pruneClipImages` revokes object URLs for attachments no longer referenced. Auto-refresh, `setTab`, and post-write refreshes all call `pollClipboard`; an auth-key change calls `resetClipSync` (cursor 0, clear items/images) then re-polls.
- Search box + scope selector (Title + content / Title / Content, `settings.clipSearchScope`, persisted) above the list; the query (`clipSearch`) is not persisted. Purely client-side over the already-synced notes. `visibleClipItems()` sorts, filters (case-insensitive substring, over title, text and image captions; captions count as content), and splits title/content into `{t, m}` segments via `highlightSegments`; matches render as `<span class="clip-hl">` text children, never `dangerouslySetInnerHTML`, since note content comes from untrusted machines.
- ✕ delete button in the note header; optionally prompts for confirmation (controlled by "Confirm note delete" in the General panel section).

### Outbox (push to clipboard from local scripts)

Lets a local script push text into the clipboard when only the browser can reach the server (the
proxy is configured only there): the script writes into a folder, the open warpgate tab picks it
up and creates the note. Design notes and measurements: `push-to-clip.md`. Chromium only
(`showDirectoryPicker`); elsewhere the panel section is hidden.

- **Protocol**: the script writes `<name>` (UTF-8 text, ≤ 512 KiB), closes it, then writes
  `<name>.sha256`: line 1 `<hex>` or `<hex>  <name>` (any case), optional line 2 the note title. The
  hash file is the only trigger and doubles as completion signal and integrity check (the folder
  watcher sees files while they are still being written). The tab verifies size, hash and strict
  UTF-8, strips a leading BOM (Windows PowerShell 5.1 writes one, in both files), and creates the
  note via `createNote(title, text, 'outbox')` (`lib/clipboard.js`, shared with Submit). On success
  it deletes `<name>.sha256` first, then `<name>`; on failure it renames `<name>.sha256` to
  `<name>.failed`. The script polls for one of the two outcomes.
- **The tab never writes files.** Edge annotates page-written files (Mark of the Web) and such
  writes occasionally fail; reading, deleting and renaming script-written files is reliable.
- **Retries**: missing content, hash mismatch or a server/network error are retried until 10 s after
  the hash file was first seen (`FAIL_AFTER_MS`), then fail; too large / not UTF-8 fail immediately.
  A `401` pauses the outbox (`status: 'auth'`) without failing; any auth key change resumes it.
- **No duplicates**: before deleting, the pushed name+hash is recorded in IndexedDB
  (`warpgate-outbox` DB, `kv` store, key `pushed:<name>.sha256`); a hash file found with a matching
  record is only cleaned up. The same store keeps the folder handle (`dir`).
- **Leftovers**: `.failed` files and orphaned content are deleted after 1 h, but only `push-*` names
  (the push function's naming), so unrelated files in the folder are never touched.
- **One tab**: the watcher runs inside `navigator.locks.request('warpgate-outbox')`; other tabs
  show "Another warpgate tab handles this outbox" and take over when the holder closes.
- **Watching**: `FileSystemObserver` (when available) kicks an immediate scan; a full scan also runs
  every 5 s. No Worker: in a hidden tab the observer still fires within milliseconds, and timers
  are only rounded to ~1 s. But Chrome throttles *chained* timers to once a minute after 5 min
  hidden, so every timer here is set only after awaiting a file system call (`sleep` in the scan
  loop) — keep it that way.
- **Permissions**: the folder needs `readwrite`. After a reload, `queryPermission` is `granted` only
  if the user chose "Allow on every visit"; otherwise the section shows Reconnect.
- **Marker**: notes created here carry `source: 'outbox'` and show ⇪ in the note header. It is
  informational, not a security property: any process of the same user can push.
- **`push-to-wgclip.ps1`** must stay compatible with Windows PowerShell 5.1 and PowerShell 7, in
  Constrained Language Mode too (no .NET method calls on non-core types, no
  `$PSBoundParameters.ContainsKey`), and is meant to be loaded interactively, not run as a script
  file (execution policy only governs files). The dialog recommends Copy +
  `Get-Clipboard -Raw | Invoke-Expression`: pasting 80 lines directly crashes Windows PowerShell
  5.1's PSReadLine 2.0 when the paste is taller than the console window. The panel warns when the
  connected folder isn't named `wgclip-outbox` (the function's default; `$env:WGCLIP_OUTBOX`
  overrides it).

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
| `-path-prefix` | — | Serve UI+API only under this single path segment; all other paths return an empty page (see "Path prefix") |
| `-ip-allow` | — | Comma-separated IPs/CIDRs always served; enables the IP firewall (see "IP firewall") |
| `-ip-reg-path` | — | Path segment under the prefix that registers the client IP; enables the IP firewall |
| `-ip-reg-max` | `0` | Max registered IPs (0 = unlimited); without `-ip-reg-path` registers via the base path (needs `-path-prefix`) |
| `-ip-reg-window` | `0` | Registration accepted only this long after startup (Go duration; 0 = unlimited) |
| `-trusted-proxies` | — | Comma-separated IPs/CIDRs of reverse proxies whose `X-Forwarded-For` determines the client IP |
| `-ws-frame-max` | `16M` | Ceiling on the WS frame size a client may request; byte count or `K`/`M` suffix (case-insensitive), range `1K`-`256M` |
