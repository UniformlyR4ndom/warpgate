#!/usr/bin/env python3
"""End-to-end verification suite for warpgate.

Builds the warpgate server and wgcli (cmd/wgcli), starts a throwaway server
instance, and exercises download/upload across all transforms and both
transports (HTTP, WebSocket), plus clipboard CRUD and a handful of edge
cases (bad auth, path traversal, invalid transform args). Every downloaded
or uploaded file is compared byte-for-byte against its source.

Usage:
    python3 test/e2e_suite.py [options]

Run with -h/--help for the full flag list. See test/README.md for details.
"""

from __future__ import annotations

import argparse
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

XOR_KEY = "deadbeefcafebabe"
AES_KEY = "0123456789abcdef0123456789abcdef"
AES_IV = "fedcba9876543210fedcba9876543210"
HTMLB64_KEY = "aabbccddeeff00112233445566778899"
PDF_PASSWORD = "1234567890abcdef1234"

ALL_TRANSFORMS = ["id", "xor", "aes-ctr", "pdf", "html-b64"]

DEFAULT_SIZES = "1K,1M,100M,1G"
DEFAULT_BACKENDS = "http,ws"
DEFAULT_TRANSFORMS = ",".join(ALL_TRANSFORMS)
DEFAULT_LARGE_TRANSFORMS = "id,xor,aes-ctr"
DEFAULT_LARGE_THRESHOLD = "1M"


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def parse_size(s: str) -> int:
    s = s.strip()
    mult = 1
    if s and s[-1].upper() in ("K", "M", "G"):
        mult = {"K": 1024, "M": 1024**2, "G": 1024**3}[s[-1].upper()]
        s = s[:-1]
    return int(s) * mult


def is_tmpfs(path: Path) -> bool:
    """True if `path` is inside a tmpfs mount, per /proc/mounts."""
    try:
        resolved = path.resolve()
    except OSError:
        return False
    best_len = -1
    best_fstype = None
    try:
        with open("/proc/mounts") as f:
            for line in f:
                parts = line.split()
                if len(parts) < 3:
                    continue
                mnt_point, fstype = parts[1], parts[2]
                mnt_path = Path(mnt_point)
                try:
                    resolved.relative_to(mnt_path)
                except ValueError:
                    continue
                if len(str(mnt_path)) > best_len:
                    best_len = len(str(mnt_path))
                    best_fstype = fstype
    except OSError:
        return False
    return best_fstype == "tmpfs"


def resolve_base_dir(explicit: str | None) -> Path:
    if explicit:
        base = Path(explicit)
        base.mkdir(parents=True, exist_ok=True)
        return base
    ramdisk = Path("/tmp/ramdisk")
    if ramdisk.is_dir() and is_tmpfs(ramdisk):
        return ramdisk
    tmp_base = Path(tempfile.gettempdir())
    print(
        f"note: /tmp/ramdisk not found or not a tmpfs mount; "
        f"using {tmp_base} (regular disk, may be slower for large files)",
        file=sys.stderr,
    )
    return tmp_base


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def files_equal(a: Path, b: Path) -> bool:
    if a.stat().st_size != b.stat().st_size:
        return False
    buf_size = 4 * 1024 * 1024
    with open(a, "rb") as fa, open(b, "rb") as fb:
        while True:
            ba = fa.read(buf_size)
            bb = fb.read(buf_size)
            if ba != bb:
                return False
            if not ba:
                return True


def generate_test_file(path: Path, size: int) -> None:
    chunk = 1 << 20
    with open(path, "wb") as f:
        remaining = size
        while remaining > 0:
            n = min(chunk, remaining)
            f.write(os.urandom(n))
            remaining -= n


def run_cli(binary: Path, args: list[str], timeout: float = 300) -> subprocess.CompletedProcess:
    return subprocess.run(
        [str(binary), *args], capture_output=True, text=True, timeout=timeout
    )


# ---------------------------------------------------------------------------
# Transform arg wiring (mirrors CLAUDE.md's "Transform System" args tables)
# ---------------------------------------------------------------------------


def download_transform_flags(t: str) -> list[str]:
    return {
        "id": [],
        "xor": ["--key", XOR_KEY],
        "aes-ctr": ["--key", AES_KEY, "--iv", AES_IV],
        "pdf": ["--password", PDF_PASSWORD],
        "html-b64": ["--key", HTMLB64_KEY],
    }[t]


def upload_transform_flags(t: str) -> list[str]:
    return {
        "id": [],
        "xor": ["--key", XOR_KEY],
        "aes-ctr": ["--key", AES_KEY, "--iv", AES_IV],
        "pdf": ["--filename", "myfile.txt"],
        "html-b64": ["--key", HTMLB64_KEY],
    }[t]


def frame_and_chunk_size(size: int) -> tuple[int, int]:
    """WS frame size and download/upload chunk size, scaled to the file size.

    Small/medium files use a tiny frame size to force multi-frame transfers
    (the case most likely to expose chunking bugs); large files use bigger
    frames so the run completes in a reasonable time.
    """
    if size <= 1 * 1024 * 1024:
        return 8 * 1024, 65536
    if size <= 100 * 1024 * 1024:
        return 65536, 4 * 1024 * 1024
    return 4 * 1024 * 1024, 16 * 1024 * 1024


# ---------------------------------------------------------------------------
# Server lifecycle
# ---------------------------------------------------------------------------


class ServerProcess:
    def __init__(self, binary: Path, root: Path, ul_root: Path, db: Path, listen: str, auth_key: str, log_path: Path):
        self.log_path = log_path
        self._log_file = open(log_path, "w")
        self.proc = subprocess.Popen(
            [
                str(binary),
                "-root", str(root),
                "-ul-root", str(ul_root),
                "-db", str(db),
                "-listen", listen,
                "-auth-key", auth_key,
            ],
            stdout=self._log_file,
            stderr=subprocess.STDOUT,
        )

    def wait_ready(self, host: str, port: int, timeout: float = 10) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(
                    f"server exited early (code {self.proc.returncode}); see {self.log_path}"
                )
            try:
                with socket.create_connection((host, port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.1)
        raise TimeoutError(f"server did not start listening within {timeout}s; see {self.log_path}")

    def stop(self) -> None:
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
        self._log_file.close()


def build_binaries(bin_dir: Path) -> tuple[Path, Path]:
    bin_dir.mkdir(parents=True, exist_ok=True)
    warpgate_bin = bin_dir / "warpgate"
    wgcli_bin = bin_dir / "wgcli"
    subprocess.run(["go", "build", "-o", str(warpgate_bin), "."], cwd=REPO_ROOT, check=True)
    subprocess.run(["go", "build", "-o", str(wgcli_bin), "./cmd/wgcli"], cwd=REPO_ROOT, check=True)
    return warpgate_bin, wgcli_bin


# ---------------------------------------------------------------------------
# Result tracking
# ---------------------------------------------------------------------------


@dataclass
class TestResult:
    name: str
    ok: bool
    detail: str = ""
    duration: float = 0.0


def print_result(r: TestResult) -> None:
    status = "OK  " if r.ok else "FAIL"
    dur = f"{r.duration:6.2f}s" if r.duration else "       "
    print(f"[{status}] {dur}  {r.name}")
    if not r.ok and r.detail:
        for line in r.detail.strip().splitlines():
            print(f"         {line}")


# ---------------------------------------------------------------------------
# Download / upload matrix
# ---------------------------------------------------------------------------


def run_download_test(
    wgcli_bin: Path, base_url: str, auth_key: str,
    root_dir: Path, download_dir: Path,
    size_label: str, size: int, transform: str, backend: str,
    keep: bool,
) -> TestResult:
    fname = f"file_{size_label}.bin"
    src = root_dir / fname
    out = download_dir / f"dl_{backend}_{transform}_{size_label}.bin"
    name = f"download {backend:4s} {transform:8s} {size_label}"

    frame_size, chunk_size = frame_and_chunk_size(size)
    args = [
        "download", "-url", base_url, "-auth-key", auth_key,
        "-o", str(out), "--transform", transform,
        *download_transform_flags(transform),
        "--backend", backend, "--chunk-size", str(chunk_size),
    ]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]
    args.append(fname)

    t0 = time.time()
    proc = run_cli(wgcli_bin, args)
    elapsed = time.time() - t0
    if proc.returncode != 0:
        return TestResult(name, False, proc.stdout + proc.stderr, elapsed)

    ok = files_equal(src, out)
    if not keep:
        out.unlink(missing_ok=True)
    return TestResult(name, ok, "" if ok else "downloaded content did not match source", elapsed)


def run_upload_test(
    wgcli_bin: Path, base_url: str, auth_key: str,
    root_dir: Path, upload_dir: Path,
    size_label: str, size: int, transform: str, backend: str,
) -> TestResult:
    fname = f"file_{size_label}.bin"
    src = root_dir / fname
    remote_name = f"ul_{backend}_{transform}_{size_label}.bin"
    stored = upload_dir / remote_name
    name = f"upload   {backend:4s} {transform:8s} {size_label}"

    frame_size, _ = frame_and_chunk_size(size)
    args = [
        "upload", "-url", base_url, "-auth-key", auth_key,
        "--remote-name", remote_name, "--transform", transform,
        *upload_transform_flags(transform),
        "--backend", backend,
    ]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]
    args.append(str(src))

    t0 = time.time()
    proc = run_cli(wgcli_bin, args)
    elapsed = time.time() - t0
    if proc.returncode != 0:
        return TestResult(name, False, proc.stdout + proc.stderr, elapsed)

    ok = stored.exists() and files_equal(src, stored)
    stored.unlink(missing_ok=True)
    return TestResult(name, ok, "" if ok else "uploaded content did not match source", elapsed)


# ---------------------------------------------------------------------------
# Clipboard CRUD
# ---------------------------------------------------------------------------


def run_clipboard_tests(wgcli_bin: Path, base_url: str, auth_key: str) -> list[TestResult]:
    results: list[TestResult] = []
    common = ["-url", base_url, "-auth-key", auth_key]

    def check(name: str, proc: subprocess.CompletedProcess, expect_ok: bool = True) -> bool:
        ok = (proc.returncode == 0) == expect_ok
        results.append(TestResult(f"clip {name}", ok, "" if ok else proc.stdout + proc.stderr))
        return ok

    p = run_cli(wgcli_bin, ["clip", "new", *common, "Hello, this is a test note!"])
    check("new #1", p)

    p = run_cli(wgcli_bin, ["clip", "new", *common, "Second note with unicode: héllo 世界"])
    check("new #2 (unicode)", p)

    p = run_cli(wgcli_bin, ["clip", "list", *common])
    ok = p.returncode == 0 and "Hello, this is a test note!" in p.stdout and "héllo 世界" in p.stdout
    results.append(TestResult("clip list shows both notes", ok, "" if ok else p.stdout))

    ids = re.findall(r"^\[(\d+)\]", p.stdout, re.MULTILINE)
    if len(ids) < 2:
        results.append(TestResult("clip id parsing", False, f"expected >=2 ids, got: {p.stdout}"))
        return results
    id1, id2 = ids[0], ids[1]

    p = run_cli(wgcli_bin, ["clip", "edit", *common, id1, "Updated content for note 1"])
    check(f"edit {id1}", p)

    note_file = Path(tempfile.mktemp(suffix=".txt"))
    note_file.write_text("content from a file")
    try:
        p = run_cli(wgcli_bin, ["clip", "edit", *common, "-f", str(note_file), id2])
        check(f"edit -f {id2}", p)
    finally:
        note_file.unlink(missing_ok=True)

    p = run_cli(wgcli_bin, ["clip", "delete", *common, id1])
    check(f"delete {id1}", p)

    p = run_cli(wgcli_bin, ["clip", "list", *common])
    ok = p.returncode == 0 and f"[{id1}]" not in p.stdout
    results.append(TestResult(f"clip list omits deleted {id1}", ok, "" if ok else p.stdout))

    p = run_cli(wgcli_bin, ["clip", "delete", *common, "999999999"])
    check("delete nonexistent id rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, ["clip", "delete", *common, id2])
    check(f"delete {id2} (cleanup)", p)

    return results


# ---------------------------------------------------------------------------
# Edge cases
# ---------------------------------------------------------------------------


def run_edge_case_tests(wgcli_bin: Path, base_url: str, auth_key: str, sample_file: str) -> list[TestResult]:
    results: list[TestResult] = []
    wrong_key = "wrong-" + auth_key

    def check(name: str, proc: subprocess.CompletedProcess, expect_ok: bool) -> None:
        ok = (proc.returncode == 0) == expect_ok
        results.append(TestResult(name, ok, "" if ok else proc.stdout + proc.stderr))

    p = run_cli(wgcli_bin, ["list", "-url", base_url, "-auth-key", wrong_key])
    check("wrong auth key rejected (list)", p, expect_ok=False)

    p = run_cli(wgcli_bin, ["download", "-url", base_url, "-auth-key", wrong_key, "-o", os.devnull, sample_file])
    check("wrong auth key rejected (download http)", p, expect_ok=False)

    p = run_cli(wgcli_bin, ["download", "-url", base_url, "-auth-key", wrong_key, "-o", os.devnull, "--backend", "ws", sample_file])
    check("wrong auth key rejected (download ws)", p, expect_ok=False)

    p = run_cli(wgcli_bin, ["download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull, "does_not_exist.bin"])
    check("nonexistent file rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, ["download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull, "../../../etc/passwd"])
    check("path traversal rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, [
        "download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull,
        "--transform", "aes-ctr", "--key", "deadbeef", "--iv", AES_IV, sample_file,
    ])
    check("invalid AES key length rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, [
        "download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull,
        "--transform", "aes-ctr", "--key", AES_KEY, "--iv", "dead", sample_file,
    ])
    check("invalid AES IV length rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, [
        "download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull,
        "--transform", "html-b64", "--key", "aabb", sample_file,
    ])
    check("invalid html-b64 key length rejected", p, expect_ok=False)

    p = run_cli(wgcli_bin, [
        "download", "-url", base_url, "-auth-key", auth_key, "-o", os.devnull,
        "--transform", "bogus", sample_file,
    ])
    check("unknown transform name rejected", p, expect_ok=False)

    return results


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--workdir", help="base directory for generated files (default: /tmp/ramdisk if it's a tmpfs mount, else the OS temp dir)")
    parser.add_argument("--sizes", default=DEFAULT_SIZES, help=f"comma-separated file sizes, e.g. 1K,1M,100M,1G (default: {DEFAULT_SIZES})")
    parser.add_argument("--backends", default=DEFAULT_BACKENDS, help=f"comma-separated backends (default: {DEFAULT_BACKENDS})")
    parser.add_argument("--transforms", default=DEFAULT_TRANSFORMS, help=f"comma-separated transforms for sizes <= --large-threshold (default: {DEFAULT_TRANSFORMS})")
    parser.add_argument("--large-transforms", default=DEFAULT_LARGE_TRANSFORMS, help=f"comma-separated transforms for sizes > --large-threshold (default: {DEFAULT_LARGE_TRANSFORMS})")
    parser.add_argument("--large-threshold", default=DEFAULT_LARGE_THRESHOLD, help=f"size boundary between the two transform lists above (default: {DEFAULT_LARGE_THRESHOLD})")
    parser.add_argument("--quick", action="store_true", help="drop size tiers above --large-threshold, for a fast sanity run")
    parser.add_argument("--keep", action="store_true", help="don't delete the run directory or downloaded files on exit (server is still stopped)")
    parser.add_argument("--port", type=int, default=0, help="server port (default: an automatically chosen free port)")
    parser.add_argument("--auth-key", default=None, help="auth key to use (default: a random one)")
    args = parser.parse_args()

    sizes = [(label, parse_size(label)) for label in args.sizes.split(",")]
    threshold = parse_size(args.large_threshold)
    if args.quick:
        sizes = [(label, n) for label, n in sizes if n <= threshold]
    backends = args.backends.split(",")
    small_transforms = args.transforms.split(",")
    large_transforms = args.large_transforms.split(",")
    auth_key = args.auth_key or secrets.token_hex(16)
    port = args.port or find_free_port()
    base_url = f"http://127.0.0.1:{port}"

    base_dir = resolve_base_dir(args.workdir)
    run_dir = base_dir / f"wgtest-{int(time.time())}-{os.getpid()}"
    run_dir.mkdir(parents=True)
    print(f"run directory: {run_dir}")

    root_dir = run_dir / "root"
    upload_dir = run_dir / "uploads"
    download_dir = run_dir / "downloads"
    for d in (root_dir, upload_dir, download_dir):
        d.mkdir()

    print("building warpgate + wgcli...")
    warpgate_bin, wgcli_bin = build_binaries(run_dir / "bin")

    print("generating test files...")
    for label, size in sizes:
        generate_test_file(root_dir / f"file_{label}.bin", size)

    server = ServerProcess(
        warpgate_bin, root_dir, upload_dir, run_dir / "db.sqlite",
        f"127.0.0.1:{port}", auth_key, run_dir / "server.log",
    )

    results: list[TestResult] = []
    try:
        server.wait_ready("127.0.0.1", port)
        print(f"server ready at {base_url}\n")

        for label, size in sizes:
            transforms = small_transforms if size <= threshold else large_transforms
            for backend in backends:
                for t in transforms:
                    r = run_download_test(wgcli_bin, base_url, auth_key, root_dir, download_dir, label, size, t, backend, args.keep)
                    print_result(r)
                    results.append(r)
                for t in transforms:
                    r = run_upload_test(wgcli_bin, base_url, auth_key, root_dir, upload_dir, label, size, t, backend)
                    print_result(r)
                    results.append(r)

        print()
        for r in run_clipboard_tests(wgcli_bin, base_url, auth_key):
            print_result(r)
            results.append(r)

        print()
        sample_file = f"file_{sizes[0][0]}.bin"
        for r in run_edge_case_tests(wgcli_bin, base_url, auth_key, sample_file):
            print_result(r)
            results.append(r)

    finally:
        server.stop()
        if not args.keep:
            shutil.rmtree(run_dir, ignore_errors=True)

    passed = sum(1 for r in results if r.ok)
    failed = len(results) - passed
    print(f"\n{passed}/{len(results)} passed", end="")
    if failed:
        print(f", {failed} FAILED")
    else:
        print()
    if args.keep:
        print(f"run directory preserved at: {run_dir}")

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
