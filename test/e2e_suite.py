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
import base64
import hashlib
import hmac
import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
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
    keep: bool, param_crypt: bool = False,
) -> TestResult:
    fname = f"file_{size_label}.bin"
    src = root_dir / fname
    out = download_dir / f"dl_{backend}_{transform}_{size_label}{'_pc' if param_crypt else ''}.bin"
    name = f"download {backend:4s} {transform:8s} {size_label}"
    if param_crypt:
        name += " [param-crypt]"

    frame_size, chunk_size = frame_and_chunk_size(size)
    args = [
        "download", "-url", base_url, "-auth-key", auth_key,
        "-o", str(out), "--transform", transform,
        *download_transform_flags(transform),
        "--backend", backend, "--chunk-size", str(chunk_size),
    ]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]
    if param_crypt:
        args.append("-param-crypt")
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
    param_crypt: bool = False,
) -> TestResult:
    fname = f"file_{size_label}.bin"
    src = root_dir / fname
    remote_name = f"ul_{backend}_{transform}_{size_label}{'_pc' if param_crypt else ''}.bin"
    stored = upload_dir / remote_name
    name = f"upload   {backend:4s} {transform:8s} {size_label}"
    if param_crypt:
        name += " [param-crypt]"

    frame_size, _ = frame_and_chunk_size(size)
    args = [
        "upload", "-url", base_url, "-auth-key", auth_key,
        "--remote-name", remote_name, "--transform", transform,
        *upload_transform_flags(transform),
        "--backend", backend,
    ]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]
    if param_crypt:
        args.append("-param-crypt")
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
# Wire-level disguise checks — a round trip (Untransform(Transform(x)) == x)
# can't catch a Transform that quietly became a no-op, since Untransform
# just reverses whatever Transform actually did. These fetch the raw,
# still-encoded bytes directly (a plain HTTP call, bypassing wgcli's own
# decode step entirely) and confirm each transform's output actually looks
# like the disguise it claims to apply.
# ---------------------------------------------------------------------------


def auth_token(key: str) -> str:
    """Mirrors internal/authhash.Hash: a labeled HMAC-SHA256 round (domain-
    separating this from internal/paramcrypt's derived subkeys) seeds 1000
    rounds of plain SHA-256, hex-encoded."""
    seed = hmac.new(key.encode("utf-8"), b"warpgate-auth-token-v1", hashlib.sha256).digest()
    h = hashlib.sha256(seed).digest()
    for _ in range(999):
        h = hashlib.sha256(h).digest()
    return h.hex()


def raw_download(base_url: str, auth_key: str, path: str, size: int, transform: str, transform_args: dict) -> bytes:
    """POSTs directly to /api/files/download and returns the raw (still
    transform-encoded) response bytes, without going through wgcli/its own
    Untransform step."""
    body = json.dumps({
        "path": path, "start": 0, "size": size,
        "transform": transform, "args": transform_args,
    }).encode("utf-8")
    req = urllib.request.Request(
        base_url + "/api/files/download", data=body, method="POST",
        headers={"Content-Type": "application/json", "X-Auth": auth_token(auth_key)},
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        return resp.read()


DISGUISE_TRANSFORM_ARGS = {
    "xor": {"key": XOR_KEY},
    "aes-ctr": {"key": AES_KEY, "iv": AES_IV},
    "pdf": {"password": PDF_PASSWORD},
    "html-b64": {"key": HTMLB64_KEY},
}


def run_disguise_tests(base_url: str, auth_key: str, root_dir: Path, size_label: str) -> list[TestResult]:
    results: list[TestResult] = []
    fname = f"file_{size_label}.bin"
    plaintext = (root_dir / fname).read_bytes()

    for t, targs in DISGUISE_TRANSFORM_ARGS.items():
        name = f"disguise {t:8s} {size_label}"
        try:
            raw = raw_download(base_url, auth_key, fname, len(plaintext), t, targs)
        except (urllib.error.URLError, OSError) as e:
            results.append(TestResult(name, False, str(e)))
            continue

        if raw == plaintext:
            results.append(TestResult(name, False, "raw wire bytes are byte-identical to plaintext (transform is a no-op)"))
            continue

        if t in ("xor", "aes-ctr"):
            ok = len(raw) == len(plaintext)
            detail = "" if ok else f"expected {len(plaintext)} bytes (same length, only XORed), got {len(raw)}"
        elif t == "pdf":
            ok = raw.startswith(b"%PDF-")
            detail = "" if ok else "output does not start with the PDF magic bytes (%PDF-)"
        elif t == "html-b64":
            ok = raw.startswith(b"<!DOCTYPE html>") and b'src="data:image/jpeg;base64,' in raw
            detail = "" if ok else "output is not the expected HTML wrapper with an embedded base64 img src"
        else:
            ok, detail = True, ""
        results.append(TestResult(name, ok, detail))

    return results


# ---------------------------------------------------------------------------
# Sim / benchmark endpoints — no filesystem I/O or transform on either side;
# "success" means wgcli's own end-to-end SHA-256 check passed (it exits
# non-zero and prints "hash mismatch" on a corrupted transfer, "hash OK" on a
# good one), not just that the process returned 0.
# ---------------------------------------------------------------------------


def run_sim_download_test(
    wgcli_bin: Path, base_url: str, auth_key: str, size_label: str, size: int, backend: str,
) -> TestResult:
    name = f"sim download {backend:4s} {size_label}"
    frame_size, chunk_size = frame_and_chunk_size(size)
    args = [
        "sim", "download", "-url", base_url, "-auth-key", auth_key,
        "--total-size", str(size), "--chunk-size", str(chunk_size), "--backend", backend,
    ]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]

    t0 = time.time()
    proc = run_cli(wgcli_bin, args)
    elapsed = time.time() - t0
    ok = proc.returncode == 0 and "hash OK" in proc.stdout
    return TestResult(name, ok, "" if ok else proc.stdout + proc.stderr, elapsed)


def run_sim_upload_test(
    wgcli_bin: Path, base_url: str, auth_key: str, size_label: str, size: int, backend: str,
    chunked: bool = False,
) -> TestResult:
    name = f"sim upload   {backend:4s} {size_label}" + (" chunked" if chunked else "")
    frame_size, chunk_size = frame_and_chunk_size(size)
    args = [
        "sim", "upload", "-url", base_url, "-auth-key", auth_key,
        "--total-size", str(size), "--backend", backend,
    ]
    if chunked:
        args += ["--chunk-size", str(chunk_size)]
    if backend == "ws":
        args += ["--frame-size", str(frame_size)]

    t0 = time.time()
    proc = run_cli(wgcli_bin, args)
    elapsed = time.time() - t0
    ok = proc.returncode == 0 and "hash OK" in proc.stdout
    return TestResult(name, ok, "" if ok else proc.stdout + proc.stderr, elapsed)


# ---------------------------------------------------------------------------
# ul-list
# ---------------------------------------------------------------------------


def parse_listing(stdout: str) -> dict[str, int]:
    """Parses wgcli list/ul-list output ("%-40s %d\\n" per file entry, "<dir>"
    for directories) into {name: size}, skipping directory entries."""
    entries: dict[str, int] = {}
    for line in stdout.splitlines():
        parts = line.split()
        if len(parts) != 2 or parts[1] == "<dir>":
            continue
        name, size_str = parts
        if not size_str.isdigit():
            continue
        entries[name] = int(size_str)
    return entries


def run_ullist_test(
    wgcli_bin: Path, base_url: str, auth_key: str, root_dir: Path, upload_dir: Path, size_label: str,
) -> list[TestResult]:
    results: list[TestResult] = []
    src = root_dir / f"file_{size_label}.bin"
    expected_size = src.stat().st_size
    names = [f"ullist_a_{size_label}.bin", f"ullist_b_{size_label}.bin"]
    stored = [upload_dir / n for n in names]

    for n in names:
        p = run_cli(wgcli_bin, [
            "upload", "-url", base_url, "-auth-key", auth_key,
            "--remote-name", n, str(src),
        ])
        ok = p.returncode == 0
        results.append(TestResult(f"ul-list setup upload {n}", ok, "" if ok else p.stdout + p.stderr))
        if not ok:
            return results

    p = run_cli(wgcli_bin, ["ul-list", "-url", base_url, "-auth-key", auth_key])
    listed = parse_listing(p.stdout) if p.returncode == 0 else {}
    ok = p.returncode == 0 and all(listed.get(n) == expected_size for n in names)
    detail = "" if ok else f"expected {names} at size {expected_size}, got: {p.stdout}{p.stderr}"
    results.append(TestResult("ul-list shows uploaded files with correct size", ok, detail))

    for f in stored:
        f.unlink(missing_ok=True)
    return results


# ---------------------------------------------------------------------------
# Parameter-encryption wire check — a functional round trip can't tell "the
# request was actually encrypted" apart from "--param-crypt was silently a
# no-op end to end": if wgcli's encode and decode sides both agreed to treat
# it as disabled, the server's auto-detect would still see plain JSON,
# answer in kind, and wgcli would parse the plain response it expected —
# full success, nothing ever actually encrypted. This captures the raw
# bytes wgcli puts on the wire (pointing it at a one-shot TCP listener
# instead of a real server) to check directly, independent of what wgcli
# itself believes about its own state.
# ---------------------------------------------------------------------------


def capture_raw_request(wgcli_bin: Path, subcommand_args: list[str], timeout: float = 5.0) -> bytes:
    """Runs `wgcli <subcommand_args>` against a one-shot raw TCP listener
    (inserting -url right after the subcommand token) and returns whatever
    bytes it sent before giving up. The listener never answers, so the
    wgcli call itself is expected to fail with a connection/EOF error —
    only the bytes captured on the wire matter here."""
    port = find_free_port()
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", port))
    listener.listen(1)
    listener.settimeout(timeout)

    captured = bytearray()

    def accept_and_capture() -> None:
        try:
            conn, _ = listener.accept()
        except socket.timeout:
            return
        conn.settimeout(1.0)
        try:
            while True:
                chunk = conn.recv(4096)
                if not chunk:
                    break
                captured.extend(chunk)
        except OSError:
            pass
        conn.close()

    thread = threading.Thread(target=accept_and_capture, daemon=True)
    thread.start()
    full_args = [subcommand_args[0], "-url", f"http://127.0.0.1:{port}", *subcommand_args[1:]]
    run_cli(wgcli_bin, full_args, timeout=timeout + 2)
    thread.join(timeout=timeout + 2)
    listener.close()
    return bytes(captured)


def http_request_body(raw: bytes) -> bytes:
    sep = raw.find(b"\r\n\r\n")
    return raw[sep + 4:] if sep != -1 else b""


def run_param_crypt_wire_tests(wgcli_bin: Path, auth_key: str) -> list[TestResult]:
    results: list[TestResult] = []

    plain_raw = capture_raw_request(wgcli_bin, ["list", "-auth-key", auth_key])
    plain_body = http_request_body(plain_raw)
    try:
        parsed_ok = json.loads(plain_body) == {"path": ""}
    except (json.JSONDecodeError, UnicodeDecodeError):
        parsed_ok = False
    detail = "" if parsed_ok else f"expected plain JSON {{\"path\": \"\"}}, got: {plain_body!r}"
    results.append(TestResult("wire: request body is plain JSON without --param-crypt", parsed_ok, detail))

    enc_raw = capture_raw_request(wgcli_bin, ["list", "-auth-key", auth_key, "-param-crypt"])
    enc_body = http_request_body(enc_raw)
    is_json = True
    try:
        json.loads(enc_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        is_json = False
    decoded_len = -1
    try:
        decoded_len = len(base64.b64decode(enc_body, validate=True))
    except Exception:
        pass
    ok = (not is_json) and decoded_len >= 16 + 32  # nonce + HMAC-SHA256, see internal/paramcrypt
    detail = "" if ok else f"expected a base64 blob (not JSON), >= 48 decoded bytes, with --param-crypt, got: {enc_body!r}"
    results.append(TestResult("wire: request body is an encrypted blob with --param-crypt", ok, detail))

    return results


# ---------------------------------------------------------------------------
# Clipboard CRUD
# ---------------------------------------------------------------------------


def run_clipboard_tests(wgcli_bin: Path, base_url: str, auth_key: str, param_crypt: bool = False) -> list[TestResult]:
    results: list[TestResult] = []
    common = ["-url", base_url, "-auth-key", auth_key]
    if param_crypt:
        common.append("-param-crypt")
    label_suffix = " [param-crypt]" if param_crypt else ""

    def check(name: str, proc: subprocess.CompletedProcess, expect_ok: bool = True) -> bool:
        ok = (proc.returncode == 0) == expect_ok
        results.append(TestResult(f"clip {name}{label_suffix}", ok, "" if ok else proc.stdout + proc.stderr))
        return ok

    p = run_cli(wgcli_bin, ["clip", "new", *common, "Hello, this is a test note!"])
    check("new #1", p)

    p = run_cli(wgcli_bin, ["clip", "new", *common, "Second note with unicode: héllo 世界"])
    check("new #2 (unicode)", p)

    p = run_cli(wgcli_bin, ["clip", "list", *common])
    ok = p.returncode == 0 and "Hello, this is a test note!" in p.stdout and "héllo 世界" in p.stdout
    results.append(TestResult(f"clip list shows both notes{label_suffix}", ok, "" if ok else p.stdout))

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
    results.append(TestResult(f"clip list omits deleted {id1}{label_suffix}", ok, "" if ok else p.stdout))

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

    p = run_cli(wgcli_bin, ["list", "-url", base_url, "-auth-key", wrong_key, "-param-crypt"])
    check("wrong auth key rejected (list, param-crypt)", p, expect_ok=False)

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
        print("wire-level disguise checks (raw transform output, bypassing wgcli's own decode):")
        for r in run_disguise_tests(base_url, auth_key, root_dir, sizes[0][0]):
            print_result(r)
            results.append(r)

        print()
        print("sim / benchmark endpoints:")
        for label, size in sizes:
            for backend in backends:
                r = run_sim_download_test(wgcli_bin, base_url, auth_key, label, size, backend)
                print_result(r)
                results.append(r)
                r = run_sim_upload_test(wgcli_bin, base_url, auth_key, label, size, backend)
                print_result(r)
                results.append(r)
                if backend == "http":
                    r = run_sim_upload_test(wgcli_bin, base_url, auth_key, label, size, backend, chunked=True)
                    print_result(r)
                    results.append(r)

        print()
        for r in run_ullist_test(wgcli_bin, base_url, auth_key, root_dir, upload_dir, sizes[0][0]):
            print_result(r)
            results.append(r)

        print()
        for r in run_clipboard_tests(wgcli_bin, base_url, auth_key):
            print_result(r)
            results.append(r)

        print()
        print("parameter encryption (--param-crypt) smoke test:")
        pc_label, pc_size = sizes[0]
        pc_transforms = [t for t in ("id", "xor") if t in small_transforms] or small_transforms[:1]
        for backend in backends:
            for t in pc_transforms:
                r = run_download_test(wgcli_bin, base_url, auth_key, root_dir, download_dir, pc_label, pc_size, t, backend, args.keep, param_crypt=True)
                print_result(r)
                results.append(r)
            for t in pc_transforms:
                r = run_upload_test(wgcli_bin, base_url, auth_key, root_dir, upload_dir, pc_label, pc_size, t, backend, param_crypt=True)
                print_result(r)
                results.append(r)
        for r in run_clipboard_tests(wgcli_bin, base_url, auth_key, param_crypt=True):
            print_result(r)
            results.append(r)
        for r in run_param_crypt_wire_tests(wgcli_bin, auth_key):
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
