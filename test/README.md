# test/e2e_suite.py

End-to-end verification suite for warpgate. Builds the `warpgate` server and `wgcli`
(`cmd/wgcli`), starts a throwaway server instance, and exercises download/upload across all
transforms (`id`, `xor`, `aes-ctr`, `pdf`, `html-b64`) and both transports (HTTP, WebSocket),
plus clipboard CRUD and a handful of edge cases (bad auth, path traversal, invalid transform
args). Every downloaded/uploaded file is compared byte-for-byte against its source.

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

Clipboard CRUD and the edge-case checks always run regardless of the matrix configuration.
