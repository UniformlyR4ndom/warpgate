# wgcli guide

wgcli is the client for a warpgate server: a file server for getting files through content-inspecting
web proxies. Transfers can be disguised by a *transform* (encrypted bytes, a PDF, an HTML page) that
wgcli applies or reverses. The server also keeps a clipboard of text notes with images.

`wgcli <command> [flags] [args]`. Flags may follow arguments; `--` ends flags (for text starting with
`-`). `clip` and `sim` take their subcommand next: `wgcli clip new …`, not `wgcli clip -json new`.

## Setup

The operator gives you two things, separately: a **connect token** and the **passphrase**. The token
(`wg1` followed by letters and digits, printed by the server at startup) describes the server
except for the key: its address and path prefix, its IP registration URL, a pin of its TLS
certificate, whether it requires a client certificate, and a fingerprint of the key. With a token,
wgcli needs no other connection settings, trusts only that server's certificate, and rejects a
wrong passphrase before contacting the server. The token is optional: without one, the operator
gives you the URL (and a registration URL, if any) instead. Keep the token as secret as the
passphrase. `wgcli connect-info` shows what a token contains.

Global flags, accepted by every command; each falls back to its environment variable:

| Flag | Variable | Meaning |
|------|----------|---------|
| `-connect` | `WGCLI_CONNECT` | Connect token (`wg1…`, from the server's startup log): sets the URL and registration URL, pins the server certificate, checks the key before use |
| `-host` | `WGCLI_HOST` | Host or `host:port` replacing the token's; required when the token has none |
| `-url` | `WGCLI_URL` | Without a token: base URL including the server's path prefix, e.g. `https://host:8443/pfx`; default `http://localhost:8080`. Excludes `-connect` |
| `-auth-key` | `WGCLI_AUTH_KEY` | Passphrase; the key K is derived from it on every run (~0.25 s) |
| `-auth-k` | `WGCLI_AUTH_K` | K in hex, from `derive-key`; overrides `-auth-key` |
| `-param-crypt` | `WGCLI_PARAM_CRYPT` | Encrypt request parameters (paths, names, transform keys); needs a key |
| `-json` | `WGCLI_JSON` | Print the result as one JSON document on stdout |
| `-insecure` | `WGCLI_INSECURE` | Skip TLS certificate verification |
| `-cert`, `-cert-key` | `WGCLI_CERT`, `WGCLI_CERT_KEY` | PEM client certificate and key, for servers requiring one |

Booleans in variables: `1`/`true`/`0`/`false`. A flag beats its variable. Recommended:

```
export WGCLI_CONNECT=wg1… WGCLI_AUTH_KEY='passphrase'
wgcli status        # registers with the IP firewall if needed, checks the key
export WGCLI_AUTH_K=$(wgcli derive-key); unset WGCLI_AUTH_KEY
export WGCLI_JSON=1 WGCLI_PARAM_CRYPT=1
```

Without a token, set `WGCLI_URL` instead, plus `WGCLI_INSECURE=1` for a self-signed certificate.

Sizes (`-chunk-size`, `-frame-size`, `-total-size`) are bytes, or KiB/MiB with a `K`/`M` suffix.

Transfer defaults: `WGCLI_BACKEND`, `WGCLI_FRAME_SIZE`, `WGCLI_CHUNK_SIZE` and
`WGCLI_TRANSFORM` set `-backend`, `-frame-size`, `-chunk-size` and `-transform` in every command
that has them. Use them for what the operator found to get through the proxy, e.g.
`export WGCLI_BACKEND=ws WGCLI_FRAME_SIZE=64K`. `WGCLI_CHUNK_SIZE` also makes HTTP uploads
chunked; WebSocket uploads ignore it.

IP firewall: optional on the server. When it blocks this machine's IP (first contact, or after a
server restart), every command registers the IP by itself and resends the refused request; stderr
says `wgcli: registered this IP …`. That needs the token's registration URL, or a server that
registers at its base URL; otherwise run `wgcli register <url>` with the URL the operator gave you.

## Transforms

`-transform` on `download`, `upload` and the image commands of `clip`. Keys and IVs left out are
random per run, so naming the transform suffices. Use `id` unless a proxy blocks or alters transfers;
then try `aes-ctr`, or `pdf`/`html-b64` where only documents pass. A transform is a disguise, not
protection: its keys travel with the request unless `-param-crypt` is on.

| Transform | Flags |
|-----------|-------|
| `id` | none |
| `xor` | `-key <hex>` |
| `aes-ctr` | `-key <hex, 16/24/32 bytes>`, `-iv <hex, 16 bytes>` |
| `html-b64` | `-key <hex, 16 bytes>` |
| `pdf` | download: `-password <text>` makes the server encrypt the PDF; upload: `-filename <name>` inside the PDF |

## Commands

Paths are relative to the root the command works on and use `/`. Text output is meant for people;
use `-json` when parsing. JSON fields that are empty and optional are left out.

### wgcli status
Checks the connection and stops at the first problem, with a message saying what to fix: reaches
the server (registering with its IP firewall if that blocks), reads what it offers, and makes one
authenticated request. Run it first, and whenever commands fail unexpectedly.
JSON: `{"url","tls","pinned","registered","uploads","ulFlat","auth":"ok"|"disabled","keyId"}`;
`registered` is true when this run registered the IP.

### wgcli list [-ul] [dir]
Lists a directory of the download root (`-ul`: of the upload directory).
JSON: `[{"name","type":"f"|"d","size","mtime"}]`; `size` (bytes) only for files, `mtime` in Unix seconds.

### wgcli ul-list
Lists the top level of the upload directory; the only listing on servers with a flat upload
directory, where `list -ul` fails. Same JSON as `list`.

### wgcli download -o <local-file> [flags] <remote-path>
Downloads a file, reversing the transform; `-ul` reads from the upload directory. `-o` is
overwritten; a failed download may leave it partial. `-chunk-size` (default 4 MiB) is the plaintext
size per request. `-backend http|ws` picks the transport; `-frame-size` sets the WebSocket frame
size in bytes (0 = server maximum). Transform flags: `-transform`, `-key`, `-iv`, `-password`.
The result is compared with the server's SHA-256 of the file; on a mismatch `-o` is removed and the
command fails. `-no-verify` skips the check. JSON: `{"path","out","bytes","sha256","seconds"}`.

### wgcli upload [flags] <local-file>
Uploads into the upload directory, or into the existing subdirectory `-dir`. `-remote-name` sets
the stored name (default: the local base name). **An existing file of that name is replaced
without warning.** `-chunk-size <bytes>` splits the upload into sequential requests (HTTP only;
default: one request). `-backend http|ws`; `-frame-size` (0 = 1 MiB). Transform flags:
`-transform`, `-key`, `-iv`, `-filename`. The server's SHA-256 of the stored file is compared with
the local file's; a mismatch fails the command. JSON: `{"path","name","bytes","sha256","seconds"}`;
`name` is the stored path.

### wgcli mkdir <dir>
Creates one directory in the upload directory; its parent must exist, at most 4 levels deep.
JSON: `{"path"}`.

### wgcli clip list [flags]
Notes, newest first. `-since <rev>`: only notes created or edited after that revision.
`-limit <n>`: only the newest n. `-search <text>` with `-scope all|title|content`: case-insensitive
substring; `content` includes image captions. `-brief`: omit the text, count characters instead.
Text output ends with `rev N` on stderr.
JSON: `{"rev","reset","ids","notes":[{"id","ts","title","source","text"|"chars","attachments":[{"id","mime","size","caption"}]}]}`.
`ts` in Unix seconds; `source` is `cli` or `outbox` for notes not made in the browser. Polling:
pass the previous `rev` as `-since`. Deletions aren't listed: `ids` (all live note ids) appears
when something was deleted since `rev`. `reset: true` means `-since` was ahead of the server (new
database): all notes are returned.

### wgcli clip get [-brief] <id>...
The given notes in the given order; same JSON as `clip list`. An unknown id is an error.

### wgcli clip new [flags] [text]
Creates a note. Text from the argument or `-f <file>` (`-f /dev/stdin` for a pipe); `-title`
optional. `-attach <image>` (repeatable; png, jpg, gif, webp, bmp, by extension) adds images, each
optionally followed by `-caption <text>`; with images the text is optional. `-transform`, `-key`,
`-iv` disguise the image bytes. If an image fails, the note exists and the error names its id.
JSON: `{"id","ts","attachments"}`.

### wgcli clip edit [flags] <id> [text]
Replaces a note's text (argument or `-f <file>`). `-title` replaces the title (`-title ""` clears
it); without `-title` the title stays. JSON: `{"id"}`.

### wgcli clip delete <id>
Deletes a note and its images. JSON: `{"id"}`.

### wgcli clip attach [flags] <note-id> <image>
Adds an image to a note; `-caption`, and `-transform`/`-key`/`-iv` as for `clip new`.
JSON: `{"id","mime","size","caption"}`.

### wgcli clip get-attachment -o <file> [flags] <attachment-id>
Saves an image; `-transform`/`-key`/`-iv` choose the disguise on the way down.
JSON: `{"id","out","bytes"}`.

### wgcli clip delete-attachment <attachment-id>...
Deletes images, stopping at the first failure. JSON: `{"ids"}`.

### wgcli clip caption <attachment-id> <caption>
Sets an image caption; `""` clears it. JSON: `{"id","caption"}`.

### wgcli register [registration-url]
Admits this machine's IP through the server's IP firewall. Other commands do this by themselves;
needed only for a registration URL wgcli doesn't know. Without an argument it uses the token's
registration URL, else `-url` (right when the server registers at its base URL). Succeeds
also when already admitted, or when the server has no firewall (`needed: false`). Admission is lost
when the server restarts. JSON: `{"url","needed"}`.

### wgcli connect-info [token]
Shows what a connect token contains (default: `WGCLI_CONNECT`), without contacting the server.
JSON: `{"url","host","port","tls","prefix","regPath","certPin","clientCert","keyId"}`; `url` is
empty when the token has no host.

### wgcli derive-key [-salt <hex>]
Prints K for `-auth-key`, for `-auth-k`/`WGCLI_AUTH_K`. The salt is fetched from the server unless
`-salt` is given. JSON: `{"key","fingerprint"}`; the fingerprint matches the one the server logs.

### wgcli sim download -total-size <bytes> [flags]
Benchmark: downloads synthetic data (no files, no transform) and verifies its SHA-256. `-chunk-size`
(default 4 MiB), `-backend http|ws`, `-frame-size`. Text output only.

### wgcli sim upload -total-size <bytes> [flags]
Benchmark: uploads random data and compares the server's SHA-256; needs uploads enabled on the
server. `-chunk-size` (HTTP only; default one request), `-backend http|ws`, `-frame-size`. Text
output only.

## Errors

Errors go to stderr as `wgcli: <message>`, exit status 1; HTTP errors read `<status>: <server text>`.

| Message contains | Meaning | Action |
|------------------|---------|--------|
| `connect token is damaged` | Copy-paste error | Copy the whole token again |
| `doesn't match the connect token` | Server certificate renewed, or wrong server; or (key) wrong passphrase | Ask the operator for a new token or the right passphrase |
| `server returned its blank page` | Wrong path prefix in `-url`, or the IP firewall blocks you and registering failed (reason follows) | Check `-url`; registration closed or full: ask the operator; unknown registration URL: `wgcli register <url>` |
| `401 Unauthorized`, `unauthorized` | Wrong key | Fix the key; each failure counts toward the block below |
| `429 Too Many Requests` | 10 wrong keys within 10 minutes from your IP | Every request is refused, even with the right key, until the oldest failure is 10 minutes old |
| `verification failed` | The SHA-256 doesn't match: data altered in transit, or (download) the file changed on the server meanwhile | Transfer again; if it keeps failing, try another `-transform` or `-backend` |
| `409 Conflict: upload already in progress` | Another upload to the same target is running | Retry later; never upload to one target in parallel |
| `404 Not Found` on upload commands | The server has uploads disabled, or the directory doesn't exist | — |
| connection refused / reset | Server down or restarting | Retry later |
