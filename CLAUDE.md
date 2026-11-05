# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Comment Policy

The default is no comment: names, types, and structure should carry the meaning. A comment that
restates the code is noise that hides the comments that matter, and it rots. Add one only when
skipping it would leave a reader surprised or misled. If in doubt, leave it out. A warranted
comment must:

1. Be brief — the fact and, if needed, a short reason why. Prefer one line; a longer comment is
   fine when the rationale genuinely needs it.
2. State only what is true and non-obvious about the code it annotates — if the code already
   shows it, the comment isn't earning its place.
3. Explain why, not what: a non-obvious constraint, a deliberate choice that looks wrong, a
   workaround for an external bug, or a security invariant whose removal would look harmless
   (refer to `doc/security.md` rather than restating the threat-model decision).
4. Never narrate history — no past bugs, no design alternatives once tried; git records that.
5. Never narrate relationships to other code — no "only called from X," no "same as Y does it,"
   no caller/sibling enumeration. Describe this code, not the rest of the codebase.

Forbidden, for example:

- Restating code: `// create the output directory` above `os.MkdirAll(...)`.
- Doc comments that just repeat the name: `// NewSigner creates a new Signer`.
- History or change narration: `// added …`, `// now uses …`, `// fixed …`, `// previously …`.
- Remarks to the reviewer or about the conversation: `// as requested …`, `// changed because …`.
- Section banners and step labels: `// --- helpers ---`, `// step 1: …`.
- Explaining the language or the standard library.
- Commented-out code.

When editing, update or delete comments your change makes wrong; don't add comments to code you
didn't otherwise change.

Before finishing, review every comment in your diff against this policy and delete each one that
doesn't clearly fit.

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

Warpgate is a file server for security professionals who need to move their own files across
content-inspecting web proxies during authorized work (e.g. pentest engagements). Downloads pass
through a *transform* that reshapes them (e.g. as encrypted bytes or a PDF) so inspecting
middleboxes pass them through; the browser frontend reverses it. Uploads go the other way: the
frontend transforms, the server reverses.
It also offers a clipboard (text notes with images, in SQLite) and a benchmark tab that probes which
transport (HTTP POST or WebSocket) and frame/chunk size a proxy lets through.

Go module `warpgate` (Go 1.25.0) at the repo root; the frontend in `web/` is embedded into the
binary. `cmd/wgcli` is a CLI client for the HTTP/WS API, for testing and scripted/agent use (flags
fall back to `WGCLI_*` env vars and may follow positional arguments; transform keys default to
random; one HTTP transport per invocation, honoring `HTTP(S)_PROXY` like the WS dialer). `wgcli guide` prints its usage guide (`cmd/wgcli/guide.md`, embedded; `guide_test.go`
fails when a command or flag is missing from it or the guide names a flag that doesn't exist).
`internal/transform`, `internal/authhash`, `internal/paramcrypt`, `internal/connecttoken`,
`internal/bytesize` (sizes with `K`/`M` suffix) and `internal/filename` (name rules) are shared by
server and `wgcli`, so the two can't drift apart.

`doc/openapi.yaml` specifies the REST API. `doc/security.md` holds the threat model, defenses and
accepted risks; update it when a security-relevant decision changes.

## Architecture

### API endpoints

| Method   | Path                    | Auth | Description |
|----------|-------------------------|------|-------------|
| GET      | `/api/public/checkin`   | No   | Logs the client IP (`clientIP`); called once on page load |
| GET      | `/api/public/settings`  | No   | `{"wsFrameMin","wsFrameMax","uploads","ulFlat","kdfSalt"}` for UI clamping, the Upload tab (hidden without `-ul-root`), the `-ul-flat` mode and key derivation |
| POST     | `/api/files/list`       | Yes  | List a directory of the download root, or one file; body `{"path":"subdir","hash":false}` (`hash`: add each file's `sha256`) |
| POST     | `/api/files/download`   | Yes  | Download a (chunk of a) file, transformed |
| POST     | `/api/files/upload`     | Yes  | Upload a (chunk of a) file (needs `-ul-root`); multipart |
| GET      | `/api/files/ul-list`    | Yes  | Top level of the upload directory (the `-ul-flat` UI) |
| POST     | `/api/ul/list`, `/api/ul/download` | Yes | Like the `/api/files/` variants, in the upload directory |
| POST     | `/api/ul/mkdir`         | Yes  | Create one directory in the upload directory; body `{"path":"a/b"}` |
| Upgrade  | `/api/files/ws/download`, `/api/ul/ws/download`, `/api/files/ws/upload` | in-band | WebSocket variants |
| POST     | `/api/sim/download`, `/api/sim/upload` | Yes | Benchmark: synthesized download / hashed upload, no transform |
| Upgrade  | `/api/sim/ws/download`, `/api/sim/ws/upload` | in-band | WebSocket variants |
| GET      | `/api/clip/state`       | Yes  | Incremental sync; `since=<rev>` → `{rev,changed,ids,reset}` |
| POST     | `/api/clip/new`         | Yes  | `{"title","data":"<base64>","source"}` (`title`, `source` optional) |
| POST     | `/api/clip/edit`        | Yes  | `{"id","title","data"}`; omitted `title` keeps the stored one |
| DELETE   | `/api/clip/delete`      | Yes  | `{"id"}` |
| POST     | `/api/clip/attachment`  | Yes  | Attach an image; multipart (`transform` part `{transform,args,note_id,mime,caption}` + `file`), image ≤ 20 MiB, body ≤ 20 MiB × 4/3 + 1 MiB |
| POST     | `/api/clip/attachment/download` | Yes | Image bytes, transformed; `{id,transform,args,contentType?}` |
| POST     | `/api/clip/attachment/edit`     | Yes | `{"id","caption"}` |
| DELETE   | `/api/clip/attachment/delete`   | Yes | `{"id"}` |
| GET      | `/`                     | No   | The UI (`web/index.html`) |
| GET      | `/static/...`           | No   | Embedded frontend assets; `/static` and `/static/` redirect to `/` |

Upload-directory endpoints need `-ul-root`, and so does `/api/sim/upload`.

### Path prefix

`-path-prefix <segment>` (`pathprefix.go`) mounts everything — UI, `/api/public/*`, WS — under
`/<segment>`, to keep a public instance out of plain sight. One segment of `[A-Za-z0-9._~-]`
(leading `/` optional, `.`/`..` rejected; `normalizePathSegment`). `withPathPrefix` strips the prefix
(and clears `RawPath`) from `<prefix>` and `<prefix>/…`; anything else, any method, gets the same
empty `200 text/html; charset=utf-8` (`writeBlank`), so nothing Go-specific can be fingerprinted.
Exactly `<prefix>` 301s to `<prefix>/` (relative asset URLs need the slash), after the IP firewall,
so an unadmitted client can't probe for a valid prefix.

The frontend derives `API_BASE` (`web/app/store.js`) from `location.pathname`; `wgcli` takes the
prefix as part of `-url`.

### Lifecycle

`NewServer` closes whatever it opened when it fails. `Server.Close` (idempotent) stops the upload
sweeper and closes the roots. `ListenAndServe(ctx)` shuts down when `ctx` ends, giving requests
`shutdownGrace` (5 s; WebSocket connections aren't waited for); `main` ends it on SIGINT/SIGTERM,
then closes server and database.

### Filesystem confinement

The route fixes the root, never a request field: `registerBrowse` (`server.go`) registers the same
list/download handlers under `/api/files/` (`Server.root`) and `/api/ul/` (`Server.ulRoot`).
`-ul-flat` keeps the upload directory flat: no `/api/ul/*` routes, and a non-empty upload `dir` is a
`400`.

All file access goes through `os.Root` handles opened in `NewServer`; a missing `-root`/`-ul-root`
is a startup error. Client paths become root-relative via `rootRelPath` (`path.go`). Symlinks inside
their root work (listed with the target's type and size). One leaving the root — any absolute link,
and on Windows any junction — is refused (`isPathEscape`; `403`, WS `errSymlinkEscape`); `listDir`
omits it and broken links, logging each on every listing. Uploads are published by renaming within
`ulRoot`, which replaces a symlink at the target rather than writing through it. Hard links and bind
mounts are not detected.

`checkNotServed` (`main.go`) refuses to start when `-db`, `-cert` or `-cert-key` lies inside
`-root` or `-ul-root`, comparing directory identity (`fileInsideDir`), so symlinks and bind mounts
are seen through; the file need not exist yet. `checkDBPath` rejects `-db` values with `file:` or
`?`, which the SQLite driver would read as URI syntax or options.

### IP firewall

`ipfilter.go`; on when any of `-ip-allow`, `-ip-reg-path`, `-ip-reg-max`, `-ip-reg-window` is set
(`newIPFilter`). `Server.withIPFilter` runs inside `withPathPrefix` (sees stripped paths) and before
all routing, covering static files, API and WS upgrades.

- Served: client IP in `-ip-allow` (IPs/CIDRs, `parseIPList`) or registered. Everything else gets
  `writeBlank`, like a wrong prefix.
- **Registration URL**: `-ip-reg-path <segment>`, or the base path (`<prefix>`/`<prefix>/`) when only
  `-ip-reg-max`/`-ip-reg-window` are set — then `-path-prefix` is required, since a bare `/` would
  let internet noise claim the slots. In base-path mode the UI URL is the registration URL, so
  reloading the UI after a restart re-registers. An open tab does so itself: `apiFetch` (`lib/api.js`)
  recognizes the blank response (empty `200 text/html; charset=utf-8`; not for the raw-byte download
  calls, where it can be genuine) and POSTs to the base path (`reRegister`, `303` = admitted). The
  refused request still fails; the next one (e.g. the next auto-refresh poll) succeeds.
- While registration is open (window not expired, `len(registered) < regMax`), GET returns
  `regPageHTML`, which auto-submits an empty form POST; only the POST registers, then 303s to
  `<prefix>/`. So link scanners that only GET can't consume slots. The page's inline script is
  allowed by its own hash-based CSP (`regPageCSP`, `csp.go`). Admitted IPs skip registration (so
  `<prefix>/` serves them the UI), except a dedicated `-ip-reg-path` (any method) and a base-path
  POST, which 303 them to the UI.
- `wgcli register [url]` (default `-url`) sends that POST without running a script and treats a
  `303` as success; otherwise, if `-url` serves it anyway (no firewall, or admitted), it reports
  `needed: false` and succeeds too. Every other wgcli command registers by itself when it gets the
  blank page (`blankPageTransport`, `dialWS`: to the token's registration URL or `-url`; a failed
  registration isn't retried within the invocation) and resends the refused request once; only if
  that fails does it report the blank page.
- `-ip-reg-max`/`-ip-reg-window` 0 = unlimited; the window counts from `NewServer`; registrations
  outlive it. State is in memory, lost on restart.
- **Client IP** (`clientIP`, also used by the check-in log): the TCP peer, IPv4-mapped addresses
  unmapped. Only if the peer is in `-trusted-proxies` is `X-Forwarded-For` walked right to left,
  skipping trusted hops; the first untrusted entry is the client. A missing header, a malformed entry
  or only trusted hops leave the client unknown: the firewall blocks (fail closed), the check-in log
  marks the peer "client unknown".

### Connect token

`connect.go` logs at startup one alphanumeric token holding what a client needs besides the key:
`internal/connecttoken` (shared with wgcli) encodes `wg1` + base58 of tagged fields (flags TLS /
client cert required, host, port, path prefix, `-ip-reg-path`, the first 16 bytes of the leaf
certificate's SHA-256, the 2-byte key fingerprint) plus a 4-byte SHA-256 checksum. Unknown tags are
skipped. Host: `-public-url` (`http(s)://host[:port]`, no path; `http` drops TLS and pin), else
the certificate's first non-wildcard DNS name or first IP (`certHost`), else a specific listen
address, else none: wgcli then needs `-host`.

wgcli `-connect`/`WGCLI_CONNECT` (excludes `-url`) sets the URL and pins the certificate
(`VerifyPeerCertificate`, CA checks off); `checkKeyFP` rejects a key whose fingerprint differs
before any request, so a wrong passphrase never reaches the failure limit. `register` defaults to
the token's registration URL; `connect-info` decodes a token offline.

### Downloads

`/api/files/download` body: `{"path","start","size","transform","args","contentType"}`.
`contentType` (optional) overrides the response's `Content-Type`, otherwise the transform's own
(an optional `ContentType()` method) or `application/octet-stream`; `validContentType` (`download.go`) rejects control
characters and > 255 bytes. HTTP only — the UI hides the setting for the WS backend.

Verification (wgcli, on unless `-no-verify`): HTTP downloads get size and `sha256` from one `list`
of the file path with `hash`, then hash what they write; on a mismatch they ask again to tell "changed
on the server" from "altered in transit", and delete the output. WS downloads get `sha256` in the
handshake ack, hashed from the file they are served from.

### Uploads

Multipart with two parts:
- `transform` (optional, JSON): `{"transform","args","dir","continuation-key","ul-done"}`; the only
  part that may be param-encrypted.
- `file`: the (transformed) data; its raw part filename names the target.

**Chunked upload** (HTTP only; `wgcli upload -chunk-size`). `ul-done` is a `*bool` defaulting to
`true` when absent, so a request without it (or without a `transform` part) is a one-shot upload;
an explicit `"ul-done":false` starts chunking.
1. First chunk (no `continuation-key`): starts a new temp file, discarding any session on record for
   that target. Each chunk is transformed independently (own `Untransform` call), so `pdf`/`html-b64`
   chunks are self-contained.
2. While `ul-done` isn't true, the response carries a fresh `continuation-key`.
3. Later chunks send the previous key; their data is appended.
4. The `ul-done: true` chunk renames the temp file over the target; no key in the response.

Every completing response (one-shot, final chunk, completed replay, WS final ack) carries
`sha256`: `publishUpload` hashes the temp file right before the rename; the session keeps it for
replays. wgcli compares it with the local file and fails on a mismatch. The browser UI compares it
too (`reportUpload`, with a js-sha256 hash taken while reading the file, so also over plain HTTP): a
match raises a green "uploaded (verified)" toast, a mismatch a red one.

`ul_session.go` (`sessionStore`, generic over the session payload) and `upload.go`
(`uploadFirstChunk`, `uploadContinuation`) implement it:
- **Locking**: a per-target claim on `Server.uploadSessions` answers `409` instead of queuing when
  the target is busy. Chunks must be sent one at a time. HTTP handlers take it through `withClaim`:
  the callback returns a value-only ack (`ulAck`, `simUlAck`) and the response is written from that
  after the release, never from the session (another request may own it by then). A WS upload holds
  `tryClaim`/`release` for the whole connection.
- **Key rotation**: current and previous key are kept. The previous key (a lost ack, retried) gets an
  idempotent answer with the current key; any other key is rejected without touching what's on record.
  So chunks must never be concurrent or out of order.
- **Completion** leaves a "completed" marker with the last key, so a retried final request gets an
  idempotent answer.
- **Failure** (transform error, write error) deletes temp file and session; the client starts over.
- **Stalls**: `parseUploadForm` (`uploadform.go`, shared with sim upload and clip attachments) fails
  a body that sends nothing for `ulBodyIdleTimeout` (5 min), extending the read deadline on every
  read, so a slow but progressing upload never times out; an over-limit body is a `413`.
- **Expiry**: `ulSweepExpired` removes sessions idle longer than `ulSessionIdleExpiry`, deleting an incomplete session's temp file.
- **WS uploads** hold the same per-target claim for the whole connection and supersede any session.

**Targets** (`Server.ulTarget`): `dir` goes through `rootRelPath` and the temp-dir guard and must be
an existing directory (`404`; `403` through an escaping symlink). The file name is reduced to its base
name (`ulBaseName`, splitting on `/` and `\`; HTTP reads the raw filename via `rawPartFilename`
because `mime/multipart` applies `filepath.Base`) and must pass `validName`. The relative path keys
the session and is returned as `name`. A directory at the target is a `409`. The key is the path as
spelled, not resolved: two spellings of one target (through a directory symlink, or differing case
on a case-insensitive filesystem) get separate claims and sessions, and the later publication wins;
each result is still a complete file. Accepted: upload each target under one spelling.

**Directory creation** (`mkdir.go`): exactly one directory whose parent exists — `201`, `409` if the
name exists, `404` without parent. At most `ulMkdirMaxDepth` (4) levels below `-ul-root` (creation
only; deeper existing directories are usable). Deliberately no delete, rename or symlink endpoint.

**Name rules** (`validName`, `path.go`, on top of `internal/filename.Valid`, which wgcli applies
to downloaded names) for files and created directories: not empty, `.` or `..`; no `/`, `\` or
control characters; ≤ 255 bytes; not the temp-dir name. On Windows `filename.ValidWindows` also
rejects `:*?"<>|`, trailing dots/spaces and device names (`CON`, `NUL`, `COM1`, …, with any
extension). Invalid → `400` before any data is stored.

**Temp-file publication**: every upload is written to `<ul-root>/.warpgate-tmp/` (`ulTmpDirName`)
and renamed over the target on success, so the target always holds the previous version or the new
one. `initUlTmpDir` clears the directory at startup. The name is reserved at any depth (`rootRelPath`
and `ulTarget` reject it ignoring case and trailing dots/spaces: `isReservedName`, `400`). The real
guarantee is `Server.reachesUlTmp`: every path prefix is compared to the directory's identity
(`os.SameFile` with `Server.ulTmpInfo`; `403`, `errUlTmpPath`), which also catches 8.3 short names
and symlinks; `withoutUlTmp` hides it from listings. Holds however `-root` and `-ul-root` relate.

## WebSocket Transport

`ws.go`, `ws_download.go`, `ws_upload.go` (gorilla/websocket). An alternative to HTTP the client picks
per transfer; one connection carries one file. Control messages are JSON text frames, data is binary.
`doc/openapi.yaml` doesn't model WS: this section is the spec.

- **Auth in-band**: browsers can't set headers on the handshake, so these routes skip `withAuth`; the
  first message's `auth` field carries the derived token. Invalid → `{"ok":false,"error":"unauthorized"}`
  and close 1008.
- **Limits** (`Server.upgradeWS`): before auth, read limit `maxParamBodySize` and the auth timeout
  (`defaultWSAuthTimeout`, 10 s) for the first message; `Server.authorizeWS` lifts the deadline. Then
  the idle timeout (`defaultWSIdleTimeout`, 5 min; both per-server `Config` fields so tests can shorten them):
  `wsReadMessage` renews it whenever data arrives, `wsWriteMessage` bounds each write, so a dead
  connection releases its claim and temp files. Upload endpoints raise the read limit to the frame
  size (larger → close 1009). Buffers are a fixed `wsBufferSize` (64 KiB).
- One `Transformer` per connection, built from the first message (transforms are stateless per call).
- Every client control message may be param-encrypted; acks mirror the handshake's choice.

**Frame size is a compatibility setting.** Inspecting proxies that buffer whole frames (Burp Suite
among them) can drop the connection on large frames. So the client picks `frameSize` per transfer
(UI default 4 MB); `Server.resolveFrameSize` accepts `[wsFrameSizeMin (1 KiB), -ws-frame-max]`, and
0/omitted means the ceiling. `NewServer` clamps a zero `WSFrameMax` up to the minimum, since
`io.ReadFull` on an empty buffer returns at once and the frame loop would spin.

**Frames never reach the transforms.** XOR and AES-CTR restart their keystream on every call, and
`pdf`/`html-b64` chunks are complete documents, so a chunk must be assembled before its one
`Transform`/`Untransform` call:
- Download: one `Transform` per chunk request; its output is cut into `frameSize` pieces with
  `io.ReadFull` and `WriteMessage` (not gorilla's own fragmentation).
- Upload: all frames are spooled to a temp file and `Untransform` runs once on `{"done":true}`.

**Download protocol:**
1. C→S `{"auth","path","transform","args","frameSize","hash"}` (`hash` optional)
2. S→C `{"ok":true,"size":N,"sha256"}` (plaintext file size; `sha256` of the open file, if `hash`)
   or `{"ok":false,"error"}`
3. C→S, one at a time: `{"start","size"}` for a chunk, or `{"done":true}`
4. S→C `{"ok":true,"chunkSize":N}`: the *transformed* length, since `pdf`/`html-b64` output is longer
   than the input. The plaintext range is first clamped to the file size announced at handshake, so
   a request reaching past EOF yields a shorter (possibly empty) chunk rather than announcing bytes
   the server can't deliver (which would stall the client). Transforms implementing
   `transform.SizedTransformer` (`EncodedLen`: all but `pdf`) stream; `pdf` output is buffered to
   measure it. A delivery falling short of the announced length (file truncated mid-transfer) is a
   terminal error, not a return to the request loop.
5. S→C binary messages of ≤ `frameSize`, `chunkSize` bytes in total
6. Any error: `{"ok":false,"error"}`, then close

**Upload protocol:**
1. C→S `{"auth","filename","dir","transform","args","frameSize"}` (`dir` optional)
2. S→C `{"ok":true,"frameSize":N}` (the resolved frame size; send no larger frames) or error
3. C→S binary messages of ≤ `frameSize` (transport pieces, not separately decodable)
4. C→S `{"done":true}`
5. S→C `{"ok":true,"name":"<dir/filename>","sha256"}`, normal close
6. Any error or disconnect: error message if possible, close; temp files deleted, target untouched

## Benchmark/Simulation Endpoints

`sim.go`, `sim_download.go`, `sim_upload.go`, `sim_ws_download.go`, `sim_ws_upload.go`: probe
transports and frame/chunk sizes in isolation — no transform, no `transform`/`args` fields, no
filesystem I/O.

- **Download** synthesizes `<totalSize in decimal><space><0xFF padding>` (`simReader`, any window,
  never materialized). `validateSimTotalSize`: `1 ≤ totalSize ≤ simMaxTotalSize` (1024 MiB) and large
  enough for its own prefix.
- **Upload** streams the bytes into SHA-256 and returns the hex hash instead of a name. Gated behind
  `-ul-root` anyway, so that flag disables everything upload-shaped. The body limit is
  `simMaxTotalSize + multipartOverheadSlack` (64 KiB), so a file of exactly the maximum fits with its
  multipart framing; chunked runs also cap the cumulative total (`simUlSession.totalBytes`).
- **Chunked sim upload** uses the same `ul_session.go` protocol as `/api/files/upload`, except:
  the `transform` part holds only `continuation-key`/`ul-done`; the filename is just a session key in
  its own store (`Server.simUploadSessions`), so names like `bench.bin` never collide with real
  uploads; the session holds the running hash and byte count (`simUlSession.hasher`/`totalBytes`),
  reset by a first chunk; every success is `200`; `hash` appears only on the completing response
  (one-shot, final chunk, completed replay), `continuation-key` on the others.
- HTTP: `POST /api/sim/download` `{"totalSize","start","size"}` → raw bytes.

**WS download:** C→S `{"auth","totalSize","frameSize"}`; S→C `{"ok":true,"size"}`; then C→S
`{"start","size"}` (`wsChunkRequest`) or `{"done":true}`, each answered with binary messages of
≤ `frameSize` (no `chunkSize` ack: sizes match without a transform). Errors as above.

**WS upload:** C→S `{"auth","frameSize"}`; S→C `{"ok":true,"frameSize"}`; binary messages, hashed as they arrive
(the running total is checked against `simMaxTotalSize` on each); C→S `{"done":true}`; S→C
`{"ok":true,"hash"}`, close.

## Authentication

All endpoints except `/api/public/*` need `X-Auth` (WS: in-band). `withAuth` (`server.go`) is
applied at route registration.

- The passphrase (`-auth-key`) never goes over the wire. `internal/authhash`: `DeriveKey` makes the
  master key `K = scrypt(passphrase, salt, N=2^17, r=8, p=1, 32 bytes)` (128 MiB, ~0.25 s in Go,
  ~0.5 s in Chromium); `Token(K)` = `HMAC-SHA256(K, "warpgate-auth-token-v2")`, hex. Every subkey
  of K is a labeled HMAC (`authhash.Label`): the token, the param-crypt keys, and `Fingerprint`
  (2 bytes, logged at startup and shown in the UI). `web/app/lib/authhash.js` reimplements it with
  the vendored `@noble/hashes` scrypt (`app/vendor/noble-hashes/`, `_`-prefixed files renamed because
  `go:embed` skips them). The scrypt parameters are constants on both sides, never served, so a
  proxy can't downgrade them; `authhash_test.go` pins a test vector.
- **Salt**: 16 random bytes in `meta_blob` (`kdf_salt`), created once per database (`DB.KDFSalt`) and
  served as `kdfSalt` by `/api/public/settings`. A new DB changes K even for the same passphrase.
- `-auth-key`; if omitted (and not `-no-auth`), a random 32-byte hex key is generated and logged.
  `NewServer` derives K once (via the `deriveKey` var, which tests replace with a cheap stand-in).
  `-no-auth` disables auth.
- **Failure limit** (`authlimit.go`): 10 wrong tokens within 10 min from one client (`authClient`:
  `clientIP`, else the TCP peer; IPv6 by /64) block every authenticated request from it with `429`
  until the oldest failure ages out. A missing token is no guess and doesn't count. Applied in
  `withAuth` and for WS in `readWSHandshake` (a handshake that doesn't decode counts too, since an
  encrypted one fails under a wrong key) and `authorizeWS`. Beyond 100k tracked clients the rest
  share one entry. In memory only: a restart clears it.
- **Frontend**: the Auth section takes a passphrase (Set / Enter); `submitPassphrase` derives K
  (`scryptAsync` with progress) and keeps it as hex in `sessionStorage["warpgate-auth-k"]`; the
  passphrase is dropped. With a key it shows "Key ID: <fingerprint>" with Change (Esc cancels) and
  Clear; the form reappears on `state.authFailed`. Every authenticated request goes through
  `apiFetch`/`apiWSOpen` (`lib/api.js`): after a `401` (or a WS `unauthorized` ack, `noteWSAck`)
  nothing is sent until the key changes, so polling doesn't use up the failure budget; `429` sets
  `state.authBlocked`.
- **wgcli**: `-auth-key` fetches the salt and derives K lazily (`globalFlags.key()`, cached);
  `-auth-k`/`WGCLI_AUTH_K` takes K directly; `wgcli derive-key` prints it.

## Parameter Encryption

Without it, request parameters — paths, file names, the very transform keys — travel as plaintext
JSON, so a TLS-terminating proxy could undo the disguise. Opt-in, client-side, keyed from K.

- **Format**: `base64(nonce[16] || AES-CTR ciphertext || HMAC-SHA256[32])`, encrypt-then-MAC over
  nonce and ciphertext. Subkeys from `internal/paramcrypt.DeriveKeys(K)`: labeled HMACs
  `warpgate-param-enc-v2` / `warpgate-param-mac-v2`.
- **Server** (`Server.decodeParams`, `paramdecode.go`): tries JSON first, then (auth enabled) the
  encrypted form; both are always accepted, no flag. `writeParamError` maps the failure to a status:
  `413` oversize (`errParamsTooLarge`), `400` plainly malformed (`errParamsMalformed`: plain-JSON
  parse failed with no keys to try, i.e. `-no-auth`), `401` only when a wrong key can't be told from
  a malformed body (`errParamsInvalid`, like a bad `X-Auth` — so a MAC failure leaks nothing). This
  keeps a size error (e.g. a clipboard note over `maxParamBodySize`) from latching the browser into
  its wrong-key state. Encryption itself is not available under `-no-auth`.
- **Scope**: every JSON request body, the multipart `transform` part (never `file`), and every WS
  control message (handshake, chunk requests, `done`).
- **Responses** are encrypted only when the request was: `decodeParams` returns `encrypted`, which
  `Server.encodeParamsResponse` mirrors (encrypted → `text/plain`). Covers the JSON responses of
  list, upload, `clip/new`, `clip/attachment` and `sim/upload`. `204`s and raw-byte downloads are
  out of scope. Bodyless `GET /api/files/ul-list` and `/api/clip/state` use the `?e` query flag
  (presence only) instead — not bound to anything, so stripping it silently downgrades to plaintext;
  accepted. WS: `readWSControl` reports `encrypted` for the handshake, and `writeWSOK`/`writeWSError`
  use that choice for the whole connection.
- **Frontend**: "Parameter obfuscation" checkbox (Auth section, `settings.paramCrypt.enabled`,
  disabled without a key). All sends go through `encodeBody` / `sendWSControl`, responses through
  `decodeResponseJSON` / `wsNextJSON(ws, decrypt)`; the client knows whether it encrypted and decodes
  accordingly (WS sessions keep it in `wsSession.encrypted`). Keys (`paramEncKeyBytes`,
  `paramMacKeyBytes`) are signals recomputed by `recomputeDerivedKeys()`. It must work over plain
  HTTP, so no `crypto.subtle`: nonces via `crypto.getRandomValues`, AES via `aes-js` (`web/aes.js`),
  HMAC via `js-sha256` (`web/sha256.js`); MAC comparison is constant-time.
- **wgcli**: `-param-crypt`, effective only with a key (`globalFlags.paramKeys()`).
  `cmd/wgcli/paramcrypt.go` has `encodeBody`/`decodeJSON`; `maybeEncryptedQuery` adds `?e=1` to
  bodyless GETs; WS goes through `writeWSJSON`/`readWSAck`.

## TLS

`-cert` and `-cert-key` (both or neither) enable HTTPS; the cert may be a full chain. The startup
log shows `https://` or `http://`.

Mutual TLS, both requiring `-cert`, combinable:
- `-client-root <pem>`: CA or self-signed cert to verify client chains (`RequireAndVerifyClientCert`).
- `-client-fp <hex>`: SHA-256 of the one accepted client cert, checked in `VerifyPeerCertificate`
  against `rawCerts[0]`; alone it uses `RequireAnyClientCert`.
`NewServer` rejects an unreadable root PEM or a fingerprint that isn't 32 bytes.

Recommended deployment: HTTPS plus a transform, so content stays disguised even where a proxy
terminates TLS. File System Access reassembly and the benchmark's hashing need a secure context.

## Transform System

`internal/transform`:

```go
type Transformer interface {
    Transform(r io.Reader) (io.Reader, error)   // server encodes a download
    Untransform(r io.Reader) (io.Reader, error) // server decodes an upload
}
```

Registered in the `transformers` map (`transform.go`). Optional interfaces: `ContentType()`
(`html-b64`: `text/html; charset=utf-8`, `pdf`: `application/pdf`) and `SizedTransformer`
(`EncodedLen`). Frontend decode/encode is in `web/app/lib/transform.js`.

`pdf` and `html-b64` hold a whole call's input in memory (`transform.Buffers`, `buffered.go`), a few
times its size; one call (request, chunk, or WS upload) is limited to `MaxBufferedPlain` (256 MiB)
of plaintext: `413` / WS error from the server (`bufferedTooLarge`, `bufferedUploadTooLarge`;
encoded input bounded by `MaxEncodedLen`), refused up front by wgcli. id/xor/aes-ctr stream and are
unlimited.

| Name | Args | Notes |
|------|------|-------|
| `id` | — | No-op |
| `xor` | `{"key":"<hex>"}`, any length, repeating | Symmetric, streaming |
| `aes-ctr` | `{"key","iv"}`: key 16/24/32 bytes, IV 16 | Symmetric, streaming; frontend uses `aes-js` |
| `pdf` | `{"filename","password"}` | See below |
| `html-b64` | `{"key"}`, exactly 16 bytes, random per request | XOR, then base64 in `<!DOCTYPE html><html><body>\n<img src="data:image/jpeg;base64,…">\n</body></html>`; UI label "HTML (base64)" |

**pdf** (asymmetric): `Transform` adds the data as an attachment to a cached base PDF
(`basePDFBytes`, from `basePDFJSON`) and, with a password (20 hex chars, generated per request by
the browser, never stored), encrypts it with AES-256. The browser decodes with pdf.js
(`getAttachments`). Uploads are built with `pdf-lib`, which can't encrypt, so they're unencrypted;
`Untransform` returns the first attachment (`api.ExtractAttachmentsRaw`).

## Clipboard (`db.go`, `clip.go`, `clip_attachment.go`)

SQLite (`-db`, default `warpgate.sqlite`). A note is text with an optional title, plus images with
optional captions.

```sql
CREATE TABLE clipboard (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    data      TEXT    NOT NULL,
    title     TEXT    NOT NULL DEFAULT '',     -- '' = untitled
    rev       INTEGER NOT NULL DEFAULT 0,      -- clip_rev at last create/edit
    source    TEXT    NOT NULL DEFAULT ''      -- '' UI, 'outbox' local script, 'cli' wgcli
)
CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL)  -- 'clip_rev', 'last_del_rev'
CREATE TABLE meta_blob (key TEXT PRIMARY KEY, value BLOB NOT NULL) -- 'kdf_salt'
CREATE TABLE clip_attachment (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    note_id INTEGER NOT NULL REFERENCES clipboard(id),  -- deleted explicitly, no cascade
    mime    TEXT    NOT NULL,
    size    INTEGER NOT NULL,
    data    BLOB    NOT NULL,
    caption TEXT    NOT NULL DEFAULT ''
)
```

- `migrate` (`OpenDB`) is idempotent: `addColumnIfMissing`, `rev = id` backfill, `meta` seeded with
  `INSERT OR IGNORE`.
- `SetMaxOpenConns(1)`: a `:memory:` test DB stays one database, and writes can't hit
  `SQLITE_BUSY`. Blob reads load into memory and release the connection before the network write.
- `DB.ListClipboard()` is used by tests only.

**Sync** (`DB.ClipState(since)`, one read transaction). Every write (create, edit, delete) bumps
`meta.clip_rev` in its transaction; a note's `rev` is the `clip_rev` of its last change, and
`last_del_rev` that of the last delete. Attachment changes are edits of their note (`rev`, not the
display timestamp).
- `changed`: notes with `rev > since`, as `NoteMeta` with `AttachmentMeta` references (no blobs).
- `ids`: all live note ids, only when `last_del_rev > since`; the client drops the rest. Deletions
  need no tombstones.
- `reset`: `since > rev` (swapped DB); `since` counts as 0 and `changed` is a full snapshot, as with
  `since=0`.
- `since == rev` returns only `{rev}`.
Writes serialize, so the cursor only moves forward. Concurrent edits of one note: last writer wins.

**Handlers**: `data` is base64, decoded in the handler. `title` and image `caption`
(`validClipTitle`): UTF-8, ≤ `clipTitleMaxLen` (200) characters, no control characters, else `400`.
`handleClipEdit` decodes `title` into a `*string` (omitted = keep). `source` (create only): `''`,
`clipSourceOutbox` or `clipSourceCLI` (sent by every `wgcli clip new`); edits keep it. The UI marks
outbox notes ⇪ and cli notes with a robot icon — hints from cooperative clients, not a security
property.

**Images** use the transform system: uploads are untransformed and stored raw (≤ 20 MiB,
`image/...` only); downloads are transformed (`resolveContentType`, stored mime as fallback) and not
param-encrypted. Store and fetch transforms are independent.

## Content Security Policy (`csp.go`)

`withCSP` wraps everything, so every response carries `contentSecurityPolicy`: `default-src 'none'`,
`script-src`/`style-src 'self'` without `unsafe-*`, `img-src 'self' blob:`, `connect-src 'self'`,
`worker-src 'self'`, `base-uri`/`form-action`/`frame-ancestors 'none'`, and Trusted Types
(`require-trusted-types-for 'script'; trusted-types pdfjs-worker`).

This requires: no inline `<script>`/`<style>`, no import map, no `eval`/`new Function`, no string
HTML sinks (`innerHTML`, `dangerouslySetInnerHTML`), styles only via the CSSOM (Preact's `style`
prop). The only Trusted Types policy, `pdfjs-worker` (`web/app/main.js`), admits only
`static/pdf.worker.min.mjs`; the module worker is created there and passed to pdf.js as
`workerPort`. Violations only show in the browser console. Exception: the IP registration page.

## Frontend (`web/`)

Preact + htm + `@preact/signals` + `deepsignal` as native ES modules; no build step, no CDN.

- `index.html` — served at `/`, so asset URLs carry `static/`; loads the global-setting scripts
  (`pdf-lib.min.js`, `aes.js`, `sha256.js`) and `static/app/main.js`
- `favicon.svg` — linked relatively (`/favicon.ico` would miss the path prefix)
- `pdf.min.mjs`, `pdf.worker.min.mjs` — pdf.js 6.4.299 (modern build)
- `app/vendor/` — the libraries, bare imports rewritten to relative paths; `noble-hashes/` is the
  scrypt subset of `@noble/hashes` 2.4.0
- `app/main.js` — boot; `app/store.js` — `settings`, `state`, derived key signals, `API_BASE`;
  `app/nav.js` — `TABS`, `setTab`, `restoreTab`, `refreshActive`, `onAuthKeyChange`
- `app/lib/` — logic: `api.js` (auth headers, `encodeBody`/`decodeResponseJSON`/`sendWSControl`),
  `transform.js`, `ws.js`, `transfers.js`, `paramcrypt.js`, `authhash.js`, `util.js`, `outbox.js`,
  one module per tab
- `app/components/` — `html.js` (bound `html` tag, `text`/`num`/`check`/`sel` binding helpers),
  `app.js`, `panel.js`, `browser.js` (file browser, both roots), `transfers.js`, `outbox.js`, one per tab
- `push-to-wgclip.ps1`, `push-to-wgclip.sh` — outbox push functions, both defining `push2wgclip`
  (the name has no hyphen so it autocompletes without colliding with `push-` commands; see "Outbox")

**htm pitfalls.** htm closes tags positionally and knows no void elements: always self-close
(`<input … />`, `<img … />`, `<br />`), or the element swallows its siblings. Whitespace containing a
line break is dropped, so text that ends a line before an element on the next line needs `${' '}`.

**Reactivity.** Components read `settings`/`state` during render and re-render on change. Mutate
through the proxies; keep references to pushed objects by reading them back from the array (see
`addTransfer`). Rendering is asynchronous: an attribute depending on an `input` updates on the next
render.

**Transfers.** `fetchChunk` picks HTTP or WS by `settings.backend`; a `wsSession`
(`{ws, password, htmlB64Key, encrypted}`) keeps one WS connection per file, closed by
`closeWSSession`. WS reads (`wsNextMessage`, `lib/ws.js`) take from a per-socket queue attached in
`wsOpen`, so nothing arriving between reads is lost; a read fails after 5 min without a message.
Downloads decode piecewise as data arrives for id/xor/aes-ctr (`newStreamCodec`, `transform.js`;
carries the XOR key position / AES counter across the pieces of one request), and in one piece per
request for pdf/html-b64. Decoded data is batched into 4 MiB blocks (`makeStager`) for the output
(`wholeFileSink`, `separateChunksSink`, `writableSink`); data for one file is kept as Blob parts,
which the browser can move out of the page's memory. Single-file downloads (chunking off, or
"Reassemble chunks") are verified: the server's SHA-256 (`list` with `hash`; WS: handshake `hash`) is
compared with a read-back hash of what was stored (`hashStored`, js-sha256 in 16 MiB slices), and only
a match is saved (`commit`), with a green "verified" toast. Reading back also catches a Blob beyond
the browser's blob storage limit (`NotReadableError`, measured in Chromium at ~2 GB in memory plus
~10% of the profile drive). Separate chunk files aren't verified. Chunked downloads can be
reassembled via File System Access
(`showSaveFilePicker`, secure context only) or, without it, as Blob parts saved at the end. `uploadFiles` uploads in parallel;
`uploadFileHTTPChunked` sends chunks strictly in sequence. A single request with pdf/html-b64 (`transformBuffers`) carries at most
250 MB (`REQUEST_LIMIT_BYTES`, a little under the server's 256 MiB): larger files are refused before
any request (`requestLimitError`), never switched to chunking silently. Uploads never read a file whole:
`encodeRange` reads it in 8 MiB slices (`readSlices`, hashing each) and builds one request's file
part, with id the `File` slice itself, with xor/aes-ctr encoded Blob parts, with pdf/html-b64 the
range encoded whole (`encodeBytesForUpload`, also used for clipboard images). WS uploads with a
streaming transform feed one codec for the whole connection, block by block (a multiple of the frame
size). Listings update only on change (`updateIfChanged`), keeping
scroll position under auto-refresh.

**File browser** (`components/browser.js`, `lib/download.js`): one per root in `state.browse`
(`dl`/`ul`); `API_PREFIX` maps the root to `/api/files/` or `/api/ul/`. Transfers carry their root, so
each tab lists its own. The Upload tab adds a drop zone (uploads go into the directory current when
they start) and "New folder" (disabled at `MKDIR_MAX_DEPTH`). With `state.ulFlat` it shows the flat
`loadUploadDir` table instead (`refreshUpload` picks).

`load`/`pollClipboard` go through a per-context `makeRequestGuard` (`lib/loadguard.js`): a response
whose context changed while in flight (navigation, auth-key/DB change) is dropped instead of
overwriting what's now on screen, and overlapping auto-refresh ticks are serialized. A context
change or a local mutation calls the `reload*` variant (`reload(root)`, `reloadUpload`,
`reloadClipboard`), which `bump()`s the guard so the refresh proceeds and supersedes any in-flight
read; plain `load`/`refreshUpload`/`pollClipboard` are the serialized auto-refresh path.

### UI settings persistence

`settings` (`store.js`) is saved as `{"version":1,"settings":{…}}` in
`localStorage["warpgate-settings"]` by an effect over `JSON.stringify(settings)`, so any change
saves. New configurable values belong in `settings`. `mergeSettings` restores only keys that still
exist, with the default's type; `normalizeSettings` then checks chunk sizes and the enum fields
against their option lists (`backendTypes`, `transformTypes`, `clipSearchScopes`,
`extOverrideModes`, which the UI renders too), falling back per field. `initStore()` runs `loadSettings()` before `applyAuto()`, so a custom key with `auto: false`
survives and `auto: true` regenerates.

- The auth key is not in `settings`: K lives in `sessionStorage["warpgate-auth-k"]` (per browser
  tab, gone when it closes; see "Authentication"). `loadAuthK` deletes the old
  `"warpgate-auth-key"` entry.
- `state` is not persisted (`browse` paths could go stale; `wsFrameMinKB`/`wsFrameMaxKB` come from
  `/api/public/settings`; `authShowKey` starts masked). Exception: `state.tab`, kept in
  `sessionStorage["warpgate-tab"]` by `setTab` and reapplied by `restoreTab()` (unknown or hidden → Download).

### Side panel

Toggled by ⚙ Settings (`settings.panel.open`); Esc closes it unless the lightbox or push-function
dialog is open or focus is outside the panel. While closed, an orange dot flags `state.authFailed`
(last list/poll got `401`, `noteAuthStatus`) or an outbox in `reconnect`/`auth`/`error`. Every
setting has a short `title` tooltip on its label and input that doesn't restate the label.

| Section   | Contents |
|-----------|----------|
| Auth      | Passphrase form (masked, 👁 not persisted; Set) or "Key ID: <fingerprint>" with Change / Clear (`components/auth.js`); Parameter obfuscation |
| Benchmark | Benchmark tab: Size (1–1024 MB), Chunk size (≤ Size) |
| Chunking  | Download: chunking, chunk size, reassembly. Upload: chunking and size (HTTP backend only). Chunk sizes ≤ 250 MB (`REQUEST_LIMIT_MB`) |
| General   | Auto-refresh (1 s); Confirm note delete; Backend (HTTP/WS); WS frame size (WS only, clamped to the server); Content-Type override (download, HTTP only); Extension override (`settings.extOverride`, replace/append the saved file's extension for browsers that block types; `applyExtOverride`) |
| Outbox    | Status, connect/reconnect/disconnect, "Push function" dialog; hidden without `showDirectoryPicker` |
| Transform | Type and parameters (auto-generated keys/IVs) |

### Clipboard tab

- Title (control characters → spaces) and text; Submit when there's text, title or a pending image.
  ✏ edits (Update/Cancel, Esc cancels).
- **Images**: pasting `image/*` into either field (`handleClipPaste`, works over plain HTTP) queues
  them with caption inputs; on Submit the note is saved first, then each image uploaded
  (`uploadAttachment`). While editing, ✕ marks an existing image for removal (↺ undoes) and captions
  are editable (`state.clipCaptionEdits`); both apply on Update.
- **Thumbnails** are fetched once (`ensureAttachment`), decoded with the current transform and
  cached as object URLs in `state.clipImages`; a result is kept only if its `'loading'` entry
  survived and it is still the id's latest load (`attachmentLoads`; a DB reset or prune
  drops it, a clipboard reload doesn't), and ⚠ retries a failed load; `<img>` renders only once a `blob:` URL exists. Click
  opens the lightbox. ⬇ saves the cached image as `attachmentFileName` (caption, else title with
  `-<n>` for several images, else `note-<id>-<n>`, plus an extension from the mime type);
  `safeFileName` sanitizes, also for the note's `.txt` download.
- **Sync**: `pollClipboard` → `applyClipState` against `clip/state?since=<clipCursor>`;
  `pruneClipImages` revokes unused URLs; an auth key change calls `resetClipSync` first.
- **Search** (client-side; scope `settings.clipSearchScope`, query not persisted): `visibleClipItems`
  matches title, text and captions case-insensitively; hits render as `<span class="clip-hl">` text
  children — note content comes from untrusted machines.
- ✕ deletes, with optional confirmation.

### Outbox (push to clipboard from local scripts)

For machines where only the browser can reach the server: a local script writes into a folder, the
open warpgate tab creates the note. Chromium only (`showDirectoryPicker`). Design notes (local,
git-ignored): `local/push-to-wgclip/push-to-clip.md`.

- **Protocol**: the script writes `<name>` (UTF-8, ≤ 512 KiB), then `<name>.sha256`: line 1 `<hex>`
  or `<hex>  <name>` (any case), optional line 2 the title. The hash file is the only trigger and the
  completion and integrity check, since the watcher sees files mid-write. The tab checks size, hash
  and strict UTF-8, strips a BOM (Windows PowerShell 5.1 writes one), and calls
  `createNote(title, text, 'outbox')`. Success deletes `<name>.sha256`, then `<name>`; failure renames
  `<name>.sha256` to `<name>.failed`. The script waits for either.
- **The tab never writes files**: Edge marks page-written files (Mark of the Web) and such writes
  sometimes fail; reading, deleting and renaming are reliable.
- **Retries**: missing content, hash mismatch or a server error retry until 10 s after the hash file
  appeared (`FAIL_AFTER_MS`); too large or not UTF-8 fail at once. A `401` pauses
  (`status: 'auth'`) until the key changes.
- **No duplicates**: name+hash is recorded before deleting, in the tab's memory (`pushedHere`) and
  in IndexedDB (`warpgate-outbox`, store `kv`, key `pushed:<name>.sha256`; writes resolve on the
  transaction's commit, a failed one raises a red toast); a recorded hash file is only cleaned up.
  Not covered: a create the server stored but whose response was lost is retried, so it can
  duplicate. The store also keeps the folder handle (`dir`).
- **Leftovers**: `.failed` and orphaned `push-*` files are deleted after 1 h; other files are never
  touched.
- **One tab** watches, under `navigator.locks.request('warpgate-outbox')`; others show
  "Handled by another warpgate tab" and take over when it closes. Each connect creates a watcher
  (`newWatcher`) owning its observer, timers and retry state; the lock is held until its run,
  including a push in flight, has ended, so a replacement (another tab, or a reconnect in this one)
  can't overlap it. Tests: `test/web/outbox.test.mjs`.
- **Watching**: `FileSystemObserver` (if available) triggers a scan; a full scan runs every 5 s. No
  Worker needed, but Chrome throttles *chained* timers to once a minute after 5 min hidden, so every
  timer is set only after awaiting a file system call (`sleep` in the scan loop) — keep it that way.
- **Permissions**: `readwrite`; after a reload it's granted only with "Allow on every visit",
  otherwise the section offers Reconnect.
- Each push raises a toast (`lib/notices.js` `addToast`: green successes vanish after 5 s, red
  failures stay; shared with upload verification, rendered by `<Toasts>`). The ⇪ marker is
  informational: any process of the same user can push.
- **`push-to-wgclip.ps1`** must work in Windows PowerShell 5.1 and PowerShell 7, also in
  Constrained Language Mode (no .NET calls on non-core types, no `$PSBoundParameters.ContainsKey`),
  loaded interactively (execution policy only governs files). The dialog recommends Copy +
  `Get-Clipboard -Raw | Invoke-Expression`: pasting it directly crashes PSReadLine 2.0 in 5.1 when the
  paste is taller than the console.
- **`push-to-wgclip.sh`** (bash and zsh, Linux and macOS: `sha256sum` or `shasum -a 256`, no GNU-only
  `date`) writes the hash file under a temporary name and renames it. File names take randomness
  from `/dev/urandom` because zsh's `$RANDOM` repeats in subshells. It must not contain `!`: pasted
  into an interactive shell, history expansion makes zsh drop the definition.
- The dialog has PowerShell / Bash tabs, preselected by platform. The panel's "Script outbox path"
  field (`settings.outboxPath`, empty = the default `~/wgclip-outbox`) is the path the script writes
  to; the dialog bakes it into the copied function as a `WGCLIP_OUTBOX` assignment (quoted for the
  shell), since the browser can't set the script's environment and the picker never reveals the
  connected folder's full path. The panel warns when the connected folder's name differs from that
  path's last segment.

### Benchmark tab

Drives `/api/sim/*` with the Download/Upload tabs' Backend and frame size, and the panel's Size and
Chunk size. Results table, newest first. Both directions verify SHA-256 end to end, so a proxy that
alters a single byte fails the run; hashing happens before timing starts, so only the network is
timed. `crypto.subtle` needs a secure context; without one a run fails with a message.

- **Download**: content is a function of `totalSize`, so `buildExpectedSimFile` reproduces it
  locally; chunks are written at their offsets into one buffer and hashed after the transfer.
- **Upload**: `generateRandomBuffer` (`getRandomValues` in ≤ 64 KiB slices, its limit), hashed
  locally and compared with the server's hash. HTTP splits by chunk size (`uploadSimHTTPChunked`,
  exercising the chunked-upload sessions); WS always sends one connection's frames.

## Go Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/pdfcpu/pdfcpu` | v0.13.0 | PDFs, attachments, AES-256 encryption |
| `modernc.org/sqlite` | v1.53.0 | Pure-Go SQLite (no CGO) |
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket transport |
| `golang.org/x/crypto` | v0.52.0 | scrypt for the auth key |

## Development

**Local-only files** (security reviews, design notes, ad-hoc tooling) go in `local/`, which is
git-ignored; nothing in the repo may depend on it.

**Error checking**: `.golangci.yml` encodes the policy. Unchecked errors are allowed only where
they carry nothing actionable (the `std-error-handling` preset: `Close` on read-only handles,
printing, `os.Remove`; plus a deferred `tx.Rollback` and `FlagSet.Parse` with `ExitOnError`).
`Close` on a file that was written to must be checked, since a write error can surface only
there. Writes to an `http.ResponseWriter` are discarded explicitly (`_, _ =`): a failed write means
the client is gone.

```bash
go run .                 # cmd/wgcli is a second main package, so not ./...
go build ./...
go test ./...
golangci-lint run        # v2, must report 0 issues

# Frontend lint (dev-only; the no-build-step frontend has no compiler to catch these).
# Flags dead references, bad import paths and imported names that no longer exist.
npm install              # once; devDeps only, node_modules is git-ignored, never shipped
npm run lint             # eslint over web/app; must report 0 issues

# End to end: builds warpgate + wgcli, every transform/backend, clipboard, edge cases
python3 test/e2e_suite.py --quick   # fast; see test/README.md
python3 test/e2e_suite.py           # full, up to 1G files

# push-to-wgclip.sh in bash + zsh against a fake tab, incl. interactive paste; no server
test/push_to_wgclip_test.sh
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-root` | `./files` | Download directory |
| `-ul-root` | — | Upload directory (upload endpoints off without it) |
| `-ul-flat` | false | Flat upload directory: no `/api/ul/*`, `dir` rejected; needs `-ul-root` |
| `-db` | `warpgate.sqlite` | SQLite database; a plain path (no `file:` URI or `?` options) |
| `-listen` | `0.0.0.0:8080` | Listen address |
| `-auth-key` | — | Auth passphrase (generated and logged if omitted); K is derived from it with scrypt |
| `-no-auth` | false | Disable authentication |
| `-cert`, `-cert-key` | — | TLS certificate (chain) and key; both or neither |
| `-client-root` | — | PEM to verify client certificates; needs `-cert` |
| `-client-fp` | — | Hex SHA-256 of the accepted client certificate; needs `-cert` |
| `-path-prefix` | — | Serve everything under this path segment only |
| `-ip-allow` | — | IPs/CIDRs always served; enables the IP firewall |
| `-ip-reg-path` | — | Registration path segment; enables the IP firewall |
| `-ip-reg-max` | `0` | Max registered IPs (0 = unlimited); alone, registers via the base path (needs `-path-prefix`) |
| `-ip-reg-window` | `0` | Registration only this long after startup (Go duration; 0 = unlimited) |
| `-public-url` | — | `http(s)://host[:port]` clients use, for the connect token |
| `-trusted-proxies` | — | IPs/CIDRs of reverse proxies whose `X-Forwarded-For` is trusted |
| `-ws-frame-max` | `16M` | Max WS frame size a client may request; bytes or `K`/`M` suffix, `1K`–`256M` |
| `-completion` | — | Print a `bash`/`zsh` completion script and exit (`completion.go`) |

Completion scripts are generated from the registered flags (`registerFlags`); a new non-boolean flag
needs an entry in `flagValueKinds` (dir/file/none), or `completion_test.go` fails.
