# test/e2e_suite.py

End-to-end verification suite for warpgate. Builds the `warpgate` server and `wgcli`
(`cmd/wgcli`), starts a throwaway server instance, and exercises download/upload across all
transforms (`id`, `xor`, `aes-ctr`, `pdf`, `html-b64`) and both transports (HTTP, WebSocket),
the `/api/sim/*` benchmark/simulation endpoints (plain and chunked upload, both transports, no
transform), `ul-list`, the upload directory tree (`mkdir`, `upload -dir`, `list`/`download -ul`,
and their confinement), clipboard CRUD, a `--param-crypt` smoke test (parameter encryption, see
CLAUDE.md's "Parameter Encryption" section), and a handful of edge cases (bad auth, path
traversal, invalid transform args). Every downloaded/uploaded file is compared byte-for-byte
against its source; sim transfers are verified via wgcli's own end-to-end SHA-256 check instead,
since nothing touches disk on either side for those endpoints.

Two additional checks operate below wgcli's own round-trip logic, since a symmetric
`Untransform(Transform(x)) == x` comparison can't detect either side quietly becoming a no-op —
the round trip still succeeds either way:
- **Wire-level disguise checks** (`run_disguise_tests`) fetch each transform's raw, still-encoded
  output directly via a plain HTTP call (bypassing wgcli's decode step entirely) and confirm it
  actually looks like the disguise it claims — XOR/AES-CTR output isn't byte-identical to the
  plaintext, `pdf` output starts with the PDF magic bytes, `html-b64` output is the expected HTML
  wrapper with an embedded base64 `img` src.
- **Parameter-encryption wire check** (`run_param_crypt_wire_tests`) points a `wgcli list` call at a
  one-shot raw TCP listener instead of the real server and inspects the literal bytes it sent, to
  confirm `--param-crypt` actually changes the request body (plain JSON without the flag, an
  encrypted base64 blob with it) rather than the flag silently being a no-op end to end.

Stdlib-only — no pip dependencies. Requires `go` on `PATH` to build the binaries.

## Usage

```bash
python3 test/e2e_suite.py                # full run: 1K, 1M, 100M, 1G tiers
python3 test/e2e_suite.py --quick         # fast sanity run, drops tiers above --large-threshold
python3 test/e2e_suite.py --keep          # leave the run directory behind for inspection
python3 test/e2e_suite.py -h              # full flag list
```

Exit code is `0` if every check passed, `1` otherwise.

## Working directory

By default, generated files, binaries, and the server's SQLite DB go under a fresh
`wgtest-<timestamp>-<pid>/` directory created inside:

1. `/tmp/ramdisk`, if it exists and is a `tmpfs` mount (fast, avoids disk writes for the 1G tier).
2. Otherwise, the OS temp directory — a notice is printed to stderr when this fallback is used.

Pass `--workdir <dir>` to override. The run directory is removed on exit unless `--keep` is
given (the server is always stopped, keep or not).

## Configuring the matrix

- `--sizes` — comma-separated sizes, e.g. `1K,1M,100M,1G` (K/M/G are 1024-based).
- `--backends` — comma-separated, from `http,ws`.
- `--transforms` — comma-separated transforms applied to sizes `<= --large-threshold`.
- `--large-transforms` — transforms applied to sizes above the threshold (default
  `id,xor,aes-ctr` — `pdf`/`html-b64` are skipped at large sizes by default since PDF
  attachment embedding of a huge file is slow and not the interesting case at that size).
- `--large-threshold` — the size boundary between the two lists above (default `1M`).

Sim endpoints, `ul-list`, the upload directory tree, clipboard CRUD, the `--param-crypt` smoke test, and the edge-case checks
all always run regardless of the matrix configuration — sim reuses the same `--sizes`/`--backends`
lists as the download/upload matrix, but has no transform axis (the endpoints take none).

## HTTP/2 pass

A second server instance runs with TLS (a self-signed cert for `127.0.0.1` from Go's bundled
`crypto/tls/generate_cert.go`) and its own upload directory and DB. Against it, a reduced matrix runs
with `wgcli -http2`, which fails unless HTTP/2 is negotiated: HTTP downloads/uploads (`id`, `xor`,
`pdf`, `html-b64`, as far as the size tier allows), a chunked upload, sim download/upload (plain and
chunked), one `--param-crypt` round trip, the clipboard tests, WS with `-http2` set (WS itself stays
HTTP/1.1), and one HTTP/1.1-over-TLS download. Sizes above `--http2-max-size` (default `100M`) are
skipped.

## What's still not covered

`-no-auth` mode, mutual TLS (`-client-root`/`-client-fp`), the `contentType` override on
`/api/files/download` (wgcli has no flag for it), `/api/public/checkin`/`/api/public/settings` (no
wgcli command exists for either), and chunked-upload protocol edge cases beyond the happy path
(stale/replayed `continuation-key`, the concurrent-request `409` lock, session expiry, the
download-blocked-by-upload `409`) are exercised manually (see the `wgcli` skill) but not by this
suite.

# test/push_to_wgclip_test.sh

Tests the bash/zsh push function (`web/push-to-wgclip.sh`) without a browser or server: a
background loop plays the warpgate tab, verifying each hash file and then consuming the push or
renaming it to `.failed`. It covers stdin and `-f` input, titles, the 512 KiB limit, a failed
push, `-n`, the timeout, and that two pushes in the same second get distinct names. Each run
happens once in bash and once in zsh (skipped if not installed).

It also pastes the function into an interactive bash and zsh through `script(1)` (util-linux;
skipped if missing), the way a terminal paste arrives: history expansion (`!`) or other
interactive-only behavior would drop the definition there.

```bash
test/push_to_wgclip_test.sh
```

Takes about 10 s. Exit code is `0` if every check passed, `1` otherwise.

# test/web — frontend unit tests (Node)

The no-build frontend has no runtime of its own, so a tiny Node harness exercises the pure
first-party modules in isolation. An ESM loader (`test/web/loader.mjs`) redirects the two
browser-only imports (`store.js`, `pdf.min.mjs`) to stubs, and `run.mjs` installs the browser
globals those modules expect (`window.crypto`, the vendored `aesjs`) before importing them.

```bash
npm run test:web
```

- `transform.test.mjs` — guards finding #1 from the code-QA review: a transform setting changed
  mid-transfer must not affect an in-flight transfer. `snapshotTransform()` is captured once at
  transfer start and threaded explicitly into every encode/decode/arg call, so mutating `settings`
  afterward is ignored (covers the XOR key/type change, `buildTransformArgs`, `encodeBytesForUpload`,
  an `aes-ctr` round trip, and `perRequestSecrets`).

Exit code is `0` if every check passed, `1` otherwise.
