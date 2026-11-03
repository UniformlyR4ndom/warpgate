# wgcli guide

wgcli is the client for a warpgate server: a file server with additional capabilities like a clipboard
for sharing notes.

`wgcli [global flags] <command> [flags] [args]`. Flags may follow arguments; `--` ends flags (for text
starting with `-`). Only global flags may precede the command. `clip` and `sim` take their subcommand
next: `wgcli clip new …`, not `wgcli clip -json new`. `wgcli <command> -h` lists the command's flags,
with the defaults your `WGCLI_*` variables set.

## Typical workflow

```
. ./wg.env && wgcli status                    # check key + resolved settings (incl. WGCLI_ROOT); see Setup
wgcli list -r data                            # look around; each entry's "path" feeds download
wgcli download -r -o ./collected data         # fetch, verified
wgcli upload -r -p -dir results/2026 ./out    # send into a (new) dir, verified
wgcli clip ask -reply-out reply.txt "paste …" # post a note, wait, save the reply — one step
wgcli wait-file -ul -since <ts>               # or wait for an uploaded file, then download -ul it
```

Details: "Asking the operator for something" below the clipboard commands.

## Setup

The operator gives you two things, separately: a **connect token** and the **passphrase**. The token
(`wg1` followed by letters and digits, printed by the server at startup) describes the server
except for the key: its address and path prefix, its IP registration URL, a pin of its TLS
certificate, whether it requires a client certificate, and a fingerprint of the key. With a token,
wgcli needs no other connection settings, trusts only that server's certificate, and checks the key
against the token's fingerprint before sending any authenticated request, so a wrong passphrase
never counts toward the server's failure limit. (Turning a passphrase into K still fetches a salt
from the server; a key passed with `-auth-k` is checked with no server contact at all.) The token
is optional: without one, the operator
gives you the URL (and a registration URL, if any) instead. Keep the token as secret as the
passphrase. `wgcli connect-info` shows what a token contains.

Global flags, accepted by every command; each falls back to its environment variable:

| Flag | Variable | Meaning |
|------|----------|---------|
| `-connect` | `WGCLI_CONNECT` | Connect token (`wg1…`, from the server's startup log): sets the URL and registration URL, pins the server certificate, checks the key against its fingerprint |
| `-host` | `WGCLI_HOST` | Host or `host:port` replacing the token's; required when the token has none |
| `-url` | `WGCLI_URL` | Without a token: base URL including the server's path prefix, e.g. `https://host:8443/pfx`; default `http://localhost:8080`. Excludes `-connect` |
| `-auth-key` | `WGCLI_AUTH_KEY` | Passphrase; the key K is derived from it on every run (~0.25 s) |
| `-auth-k` | `WGCLI_AUTH_K` | K in hex, from `derive-key`; overrides `-auth-key` |
| `-param-crypt` | `WGCLI_PARAM_CRYPT` | Encrypt request parameters (paths, names, transform keys); needs a key |
| `-json` | `WGCLI_JSON` | Print the result as one JSON document on stdout |
| `-insecure` | `WGCLI_INSECURE` | Skip TLS certificate verification |
| `-cert`, `-cert-key` | `WGCLI_CERT`, `WGCLI_CERT_KEY` | PEM client certificate and key, for servers requiring one |
| `-root` | `WGCLI_ROOT` | Local working directory, which must exist: every local path a command takes (uploads, `-f`, `-attach`, downloads, `-o`) stays within it. Relative paths are relative to it, not to the current directory; an absolute path under it works too; a path leading outside it fails. Excludes `-no-root` |
| `-no-root` | `WGCLI_NO_ROOT` | Read and write files anywhere, opting out of the `-root` requirement; relative paths are relative to the current directory |

- **A working directory is required for local files.** Reading or writing one (the local paths of
  `download` and `upload`, `-f`/`-attach` of the `clip` commands, `-o` of `clip get-attachment`)
  fails unless `-root` is set, or `-no-root` is given to run unconfined on purpose. Everything else,
  including text given as an argument, works either way.
- Set `-root` to the one directory you work in (e.g. a project folder): then a stray absolute or
  `../` path can't read or clobber files elsewhere by accident. It guards against mistakes; it is no
  sandbox. `-cert`/`-cert-key` are read wherever they are.
- Booleans in variables: `1`/`true`/`0`/`false`. A flag beats its variable.

Keep the settings in a file and source it before each command (shell state often doesn't persist
between commands):

```
# run in the directory you work in; WGCLI_ROOT confines local files to it
printf "export WGCLI_CONNECT=%s WGCLI_AUTH_KEY=%s WGCLI_ROOT=%s\n" 'wg1…' "'passphrase'" "$PWD" > wg.env
. ./wg.env && wgcli status    # registers with the IP firewall if needed, checks the key
# replace the passphrase with the derived key (saves the derivation on every call):
. ./wg.env && printf "export WGCLI_CONNECT=%s WGCLI_AUTH_K=%s WGCLI_ROOT=%s WGCLI_JSON=1 WGCLI_PARAM_CRYPT=1\n" \
    "$WGCLI_CONNECT" "$(wgcli derive-key)" "$WGCLI_ROOT" > wg.env
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
says `wgcli: registered this IP …`, with "again … (server restarted?)" when the server knew it
earlier in the same run. That needs the token's registration URL, or a server that
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
authenticated request. Run it first, and whenever commands fail unexpectedly. It also resolves the
transfer defaults from the `WGCLI_*` variables (transform, backend, the download and upload chunk
sizes, the WS frame size, parameter encryption), so you can confirm what transfers will use;
invalid values are reported.
JSON: `{"url","tls","pinned","registered","uploads","ulFlat","auth":"ok"|"disabled","keyId",
"transfer":{"transform","backend","paramCrypt","downloadChunkSize","uploadChunkSize","frameSize","chunkSource","frameSource"}}`.
`registered` is true when this run registered the IP. `uploadChunkSize` 0 means one request per
file; `frameSize` 0 means the server's maximum (download) or 1 MiB (upload); `chunkSource` and
`frameSource` are `env` or `default`.

### wgcli list [-ul] [-r] [-hash] [path]
Lists a directory of the download root (`-ul`: of the upload directory), or one file. `-r` lists
the whole tree below it. Every entry carries a root-relative `path` (`logs/web/access.log`, ready
for `download`) next to its base `name`; `-r` text output shows the path, a plain listing the name.
`-hash` adds each file's SHA-256 as the server computes it, without downloading (reads every file
listed).
JSON: `[{"name","path","type":"f"|"d","size","mtime","sha256"}]`; `path` is root-relative, `size`
(bytes) only for files, `mtime` in Unix seconds, `sha256` only with `-hash`.

### wgcli ul-list
Lists the top level of the upload directory; the only listing on servers with a flat upload
directory, where `list -ul` fails. Same JSON as `list`.

### wgcli download -o <local> [flags] <remote-path>...
Downloads files, reversing the transform; `-ul` reads from the upload directory. Missing local
directories are created. One file: `-o` is the local file, or a directory (existing, or ending in
`/`) to put it in under its own name. Several paths or `-r` (directories with their contents): `-o` is a directory, and
each path lands in it under its own name (`download -r -o ./c docs logs` gives `./c/docs/…` and
`./c/logs/…`). Files
are written as `<name>.wgpart` and renamed only when complete and verified (SHA-256 against the
server's; `-no-verify` skips that), so a failed download never leaves a partial or altered file.
When two paths would land on the same local file (e.g. `a/x` and `b/x`), both fail instead of one
silently overwriting the other; `-parents` keeps each file's full remote directory path under `-o`
(`a/x` to `<o>/a/x`), which both avoids the clash and mirrors the server's layout.
With several paths or `-r`, files that are identical locally are skipped (`-force`: download
anyway), and names this system can't store fail. `-chunk-size` (default 4 MiB) is the plaintext
size per request; `-backend http|ws`; `-frame-size` (0 = server maximum). Transform flags:
`-transform`, `-key`, `-iv`, `-password`.
JSON, one file: `{"path","out","bytes","sha256","verified","transform","seconds"}` (`verified`: the server's
hash matched); several (see "Transfer summary"):
`{"transferred":[{"path","out","bytes","sha256","verified"}],"skipped","failed","bytes","transform","seconds"}`.

### wgcli upload [flags] <local>...
Uploads into the upload directory, or into its subdirectory `-dir`; each path lands there
under its own name (`-remote-name` renames a single file). `-dir` must already exist unless `-p`
is given, which creates it and any missing parents first, like `mkdir -p` (still at most 4 levels
below the upload root). `-r` uploads directories with their contents, creating subdirectories (at
most 4 levels below the upload root; checked before anything is sent); links to directories and
special files are skipped. Example: `upload -r -p -dir results/2026 ./report` gives
`results/2026/report/…` on the server. **An existing file of the same name
is replaced without warning**, except that with several paths or `-r` identical files are skipped
(`-force`: upload anyway). The server's SHA-256 of each stored file is compared with the local
one. `-chunk-size` splits each file into sequential requests (HTTP only; default: one request);
`-backend http|ws`; `-frame-size` (0 = 1 MiB). Transform flags: `-transform`, `-key`, `-iv`,
`-filename`. JSON, one file: `{"path","name","bytes","sha256","verified","transform","seconds"}` (`name`:
stored path); several: as for `download`, entries `{"path","name","bytes","sha256","verified"}`.

**Transfer summary** (several paths or `-r`): text output reports each file as it happens, then a
line with the totals. Transfers continue past failures; the exit status is 1 if any file failed.
`skipped` entries are `{"path","reason"}`, `failed` entries `{"path","error"}`.

### wgcli mkdir [flags] <dir>
Creates one directory in the upload directory; its parent must exist, at most 4 levels deep.
`-p` creates missing parents too (shallowest first) and treats an existing directory as success,
like `mkdir -p`; the 4-level limit still applies to each level. JSON: `{"path","created"}` with
`-p` (`created` lists the directories actually made, empty if all existed), else `{"path"}`.

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

### wgcli clip get [-brief|-raw] <id>...
The given notes in the given order; same JSON as `clip list`. An unknown id is an error. `-raw`
prints only the text of one note, unchanged, e.g. to save a reply: `clip get -raw 7 > reply.txt`.

### wgcli clip new [flags] [text]
Creates a note. Text from the argument or `-f <file>` (`-f /dev/stdin` for a pipe); `-title`
optional. `-attach <image>` (repeatable; png, jpg, gif, webp, bmp, by extension) adds images, each
optionally followed by `-caption <text>`; with images the text is optional. `-transform`, `-key`,
`-iv` disguise the image bytes. If an image fails, the note exists and the error names its id.
Each image is sent in one request (≤ 20 MiB); attachments are never chunked, so a small
`WGCLI_CHUNK_SIZE` doesn't apply to them (wgcli warns when an image is larger).
JSON: `{"id","ts","since","attachments"}`: pass `since` to `clip wait -since` and `ts` to
`wait-file -since` to catch the reply to this note however quickly it comes (see below). `since`
is the clipboard's revision before this note (it counts every change; 0 on an empty clipboard),
`ts` the server's Unix time.

### wgcli clip ask [flags] [text]
Posts a note (same content flags as `clip new`: text or `-f`, `-title`, `-attach`/`-caption`,
`-transform`/`-key`/`-iv`) and then waits for the operator's reply in one step, so you needn't
thread `since` yourself. It waits from the revision just before the note, so a reply can't be
missed however fast it comes; it ignores its own note unless `-any-source`, and `-search`/`-scope`
(as in `clip list`) restrict what counts as the reply. On a reply it prints the note(s) like
`clip list` on stdout, exit status 0; stderr says `posted note <id>; waiting…` right away, so don't
take output on stderr for the end. `-reply-out <file>` also writes the reply's text to a local file
(the newest match if several), sparing a follow-up `clip get -raw`. `-timeout` (default `2m`; `90s`,
`5m`, or plain seconds; `0` = indefinitely): on timeout it exits with status 2 and prints the
`clip wait -since` that resumes the wait without a gap, so the posted note isn't lost.

### wgcli clip edit [flags] <id> [text]
Replaces a note's text (argument or `-f <file>`). `-title` replaces the title (`-title ""` clears
it); without `-title` the title stays. JSON: `{"id"}`.

### wgcli clip delete <id>
Deletes a note and its images. JSON: `{"id"}`.

### wgcli clip attach [flags] <note-id> <image>
Adds an image to a note; `-caption`, and `-transform`/`-key`/`-iv` as for `clip new`. Sent in one
request (≤ 20 MiB; not chunked). JSON: `{"id","mime","size","caption"}`.

### wgcli clip get-attachment -o <file> [flags] <attachment-id>
Saves an image; `-transform`/`-key`/`-iv` choose the disguise on the way down.
JSON: `{"id","out","bytes"}`.

### wgcli clip delete-attachment <attachment-id>...
Deletes images, stopping at the first failure. JSON: `{"ids"}`.

### wgcli clip caption <attachment-id> <caption>
Sets an image caption; `""` clears it. JSON: `{"id","caption"}`.

### wgcli clip wait [flags]
Waits until a note is created or edited after `-since <rev>` (default: from now), then prints the
notes like `clip list`. Ignores notes written by wgcli unless `-any-source`. `-search`/`-scope` as
in `clip list` wait for a specific note. `-timeout` (default `2m`; `90s`, `5m`, or plain seconds;
`0` = indefinitely); on timeout it exits with status 2, prints `{"rev","notes":[]}` and says which
`-since` continues without gaps.

### wgcli wait-file [flags] [path]
Waits for a file, by default in the download root (`-ul`: the upload directory, where the
operator's uploads land). **Without a path or with a directory, any new file counts**, so you
needn't know its name; with a file path, that file (`-new`: only a newer version than the present
one). `-since <unix-time>` counts every file modified from then on as new, including ones that
arrived before the wait started, and your own uploads: to wait for the operator's file after
uploading into the same directory yourself, give its path, or a `-since` after your upload. Files appear only when completely uploaded. `-timeout` as in
`clip wait` (status 2, prints `[]`). JSON: `[{"path","size","mtime","sha256"}]`.

**Asking the operator for something:**
- For a reply note, `wgcli clip ask "please paste …"` does it in one step: it posts the note and
  blocks until a reply, with no `since` to carry. Run it in the background if your environment
  allows it, so you stay responsive while the operator takes their time; otherwise rely on its
  `-timeout` and the `clip wait -since` it prints to resume.
- To do it by hand, or to wait for an uploaded *file* rather than a note:
  1. `wgcli clip new "please paste …"`, keeping `since` and `ts` from its output.
  2. `wgcli clip wait -since <since>` for a reply note, or `wgcli wait-file -ul -since <ts>` for an
     upload.
Without `-since`, a wait counts from its own start and misses anything that arrived earlier, for
example while a failed wait was being retried. Server restarts during a wait are ridden out.

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

Errors go to stderr as `wgcli: <message>`, exit status 1, invalid flags included; status 2 means
only that a wait timed out. HTTP errors read `<status>: <server text>`.

| Message contains | Meaning | Action |
|------------------|---------|--------|
| `local file access needs a working directory` | Neither `-root` nor `-no-root` given | Set `WGCLI_ROOT` to the directory you work in |
| `is outside the working directory` | A local path (absolute, or climbing out with `../`) leads outside `-root` | Use a path inside the root; ask the operator before resorting to `-no-root` |
| `connect token is damaged` | Copy-paste error | Copy the whole token again |
| `doesn't match the connect token` | Server certificate renewed, or wrong server; or (key) wrong passphrase | Ask the operator for a new token or the right passphrase |
| `server returned its blank page` | Wrong path prefix in `-url`, or the IP firewall blocks you and registering failed (reason follows) | Check `-url`; registration closed or full: ask the operator; unknown registration URL: `wgcli register <url>` |
| `401 Unauthorized`, `unauthorized` | Wrong key | Fix the key; each failure counts toward the block below |
| `429 Too Many Requests` | 10 wrong keys within 10 minutes from your IP | Every request is refused, even with the right key, until the oldest failure is 10 minutes old |
| `verification failed` | The SHA-256 doesn't match: data altered in transit, or (download) the file changed on the server meanwhile | Transfer again; if it keeps failing, try another `-transform` or `-backend` |
| `409 Conflict: upload already in progress` | Another upload to the same target is running | Retry later; never upload to one target in parallel |
| `404 Not Found` on upload commands | The server has uploads disabled, or the directory doesn't exist | — |
| connection refused / reset | Server down or restarting | Retry later |
