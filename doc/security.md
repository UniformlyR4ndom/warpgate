# Security model and decisions

What Warpgate defends against, what it deliberately does not, and why. Reviewers: risks listed
under "Accepted risks" are known and accepted; please report them only if their premises no
longer hold.

## Threat model

- **Single operator.** Warpgate is run by one person, typically one instance per engagement. There
  is one shared auth key and no notion of users or per-user isolation: whoever holds the key has
  full access to both roots and the clipboard.
- **The adversary is the network path, not the client.** The tool exists to get files through
  content-inspecting proxies, including ones that terminate TLS. Such a proxy can read and modify
  everything it sees, including the frontend's own JavaScript. Warpgate's job is to keep it from
  recognizing or blocking the content, not to withstand it.
- **Unauthenticated clients** (internet noise, scanners, a proxy probing the server) must not get
  past authentication, read files, or cause disproportionate resource use.
- **Authenticated clients are trusted** not to attack the server's availability. They can already
  overwrite every file in the upload root.
- **The host is trusted.** Local users who can create links in the served roots, or read the
  database or key files, are outside the model.

## What transforms provide

Transforms (`xor`, `aes-ctr`, `pdf`, `html-b64`) are **reversible disguises**. They keep payloads from
matching content signatures; they provide **neither confidentiality nor integrity**:

- `aes-ctr` restarts its keystream from the same key and IV for every chunk and every request.
  An observer with known plaintext for one chunk can recover the same byte positions of another.
- No transform authenticates the payload: a modified payload may decode without error into modified
  file content (with `xor` and `aes-ctr`, flipping a bit of the payload flips the same bit of the
  file). wgcli detects this by comparing each transfer's SHA-256 with the server's; the hash itself
  is protected only with parameter encryption, which authenticates responses. The browser checks
  uploads only (`reportUpload`), not downloads.
- Without parameter encryption, the transform keys travel in the request next to the payload.

Parameter encryption hides keys, paths and filenames from an inspecting proxy and authenticates
each control message (encrypt-then-MAC). It does not prevent replay and is not mandatory: requests
whose parameters are plain, unencrypted JSON are always accepted (independent of whether TLS is used).

Confidentiality is provided only by TLS, and only as far as no proxy intercepts it.

## Defenses in place

**Authentication**
- The passphrase never goes on the wire. Clients derive a master key with scrypt (N=2^17, r=8,
  p=1: 128 MiB per guess) under a random per-database salt and send a labeled HMAC of it; the
  parameter-encryption keys are other labeled HMACs of the same master key. Anyone who captures a
  token or an encrypted request can still test passphrase guesses offline, but each guess costs a
  full scrypt run, so weak, typed passphrases hold up far longer. The scrypt parameters are fixed
  in the clients, so an active proxy can't lower them. Without `-auth-key` a random 32-byte key is
  generated.
- Online guessing is limited per client IP (IPv6: per /64): after 10 wrong tokens in 10 minutes,
  every authenticated request from it gets `429` until the failures age out. In memory only; a
  restart clears it. Clients sharing an IP with an attacker are blocked with it.
- WebSocket routes authenticate in-band. Before authentication a connection may send at most
  `maxParamBodySize` and must authenticate within 10 s; transport buffers are a fixed 64 KiB.
- After authentication, a WebSocket that receives no data for 5 minutes, or takes that long to
  send one message, is closed, releasing its upload claim and temp files.
- Optional mutual TLS (`-client-root`, `-client-fp`) in addition to the token.
- HTTP connections must send their request headers within 30 s and are closed after 2 minutes
  idle between requests.

**Hiding and admission**
- `-path-prefix`: everything outside the prefix gets an identical blank page.
- IP firewall (`-ip-allow`, registration URL): unadmitted clients get the same blank page. Behind
  `-trusted-proxies`, a request whose client cannot be determined is blocked (fail closed).
- A registration URL is an admission secret, not an identity: anyone who learns it can claim a slot.
- In base-path registration mode, an open UI tab whose API requests are refused re-registers its
  IP by itself (once per refused request) while registration is open, e.g. after a server restart.
  So any tab still open on another machine can claim a slot then; accepted, as the operator
  controls the few tabs that exist.

**Filesystem**
- All file access goes through `os.Root` handles; symlinks leaving a root are refused and left
  out of listings. Which root (download or upload) a request uses is fixed by its route,
  never by a request field.
- Uploads are written to a temp directory and renamed over the target only when complete, so an
  aborted or failed upload never touches the existing file. The temp directory is unreachable
  through the API under any spelling.
- File and directory names are validated, including names Windows would reinterpret.
- Transforms that hold their input in memory (`pdf`, `html-b64`) take at most 256 MiB of plaintext
  per request, chunk or WS upload (`413`), checked before the data is read into memory; the
  streaming transforms are unlimited.
- The server refuses to start if the database, certificate or key lies inside a served root.
  `-db` must be a plain path: SQLite URI syntax (`file:`) and `?` options are rejected, since the
  driver would open a different file than the one checked.

**Browser**
- Strict CSP on every response: `script-src 'self'` with no `unsafe-*`, no framing, Trusted Types
  with a single policy that only allows the pdf.js worker URL.
- No string HTML sinks; note text, titles, captions and filenames are rendered as text.
- Only the derived key is kept, in `sessionStorage`, so it doesn't outlive the tab; the passphrase
  is not stored.
- Vendored libraries are kept current (pdf.js 6.4.299).

**CLI (`wgcli`)**
- TLS verification is on unless `-insecure` is given.
- API requests never follow redirects, so the token can't be forwarded to another host.

## Accepted risks

| Risk | Why it is accepted |
|------|--------------------|
| No size limits or quotas on uploads, clipboard notes or attachments in total, no cap on concurrent transfers | Authenticated clients are trusted, and large transfers are the point of the tool. Use a dedicated, quota-limited filesystem if this matters for a deployment. |
| A small compressed PDF upload can expand to a large file and allocate several times that in memory before size checks apply (the 256 MiB per-request limit of buffering transforms bounds the PDF received, not what it decompresses to) | Requires authentication. Memory use scales with the expanded size, much like uploading a large plain file. Bounding it would require isolating or patching the PDF library. |
| The auth token is a replayable bearer credential; requests with unencrypted parameters are always accepted | Accepted for simplicity. Use TLS, and the IP firewall or mutual TLS, where capture is a concern. |
| A captured token or encrypted request allows offline passphrase guessing | Each guess costs a 128 MiB scrypt run. Only a PAKE would prevent it, at the cost of heavy dependencies. Use a strong passphrase where capture is a concern. |
| An attacker with many IPv6 /64s, or more than 100,000 addresses, gets more online guesses | Beyond 100,000 tracked clients the rest share one failure entry, which bounds memory and still limits them collectively. |
| Once 100,000 clients are tracked, each request from a new address scans the whole table under a lock (~7 ms), slowing authentication checks for new clients | Filling the table takes 100,000 addresses each sending a wrong token at least every 10 minutes. With IPv6 a single actor can do this (a /48 holds 65,536 /64s), so it is not limited to botnets. The IP firewall (with `-ip-allow` or `-ip-reg-max`), a secret `-path-prefix` or mutual TLS keep such clients from reaching authentication; the default listener is IPv4-only. It slows checks but bypasses nothing. |
| A client can hold an HTTP connection open after its headers by declaring a request body and never sending it; there is no overall request deadline, and upload bodies fail only after stalling for 5 minutes | A whole-request timeout would break large transfers. Holding connections open is already possible by sending headers slowly (30 s) and reconnecting, so a body deadline would gain little. Put a reverse proxy with connection limits, or mutual TLS, in front if this matters. |
| `/api/public/settings` (no auth) tells anyone past the path prefix and IP firewall whether uploads are enabled (`uploads`, `ulFlat`), besides the WS frame limits and the KDF salt | The UI needs them before the key is set (to hide the Upload tab and derive the key). Uploading still requires authentication. |
| The connect token holds the path prefix and registration path (admission secrets) and the key fingerprint, which lets its holder test passphrase guesses offline without capturing traffic | The token is handed to trusted clients like the passphrase, separately from it. Each guess costs a full scrypt run. The fingerprint has 16 bits: it rules out all but 1 in 65,536 wrong guesses, and confirming a remaining candidate still needs the server or captured traffic. |
| The token pins the server certificate to 128 bits of its SHA-256; a renewed certificate needs a new token | 128 bits leave a second preimage out of reach. A TLS-terminating proxy between agent and server breaks the pin, which is not expected for agent connections. |
| File hashes (`sha256` in upload responses, `list` with `hash`, the WS download ack) fingerprint content; a proxy could look them up in known-file databases without decoding the payload | They travel only in parameter-encrypted responses when parameter encryption is on. Without it, the transform keys travel in plaintext anyway, so the content itself is exposed already. |
| `aes-ctr` keystream reuse; no payload integrity | Transforms are disguises, see above. |
| `-no-auth` exposes all of the above to anyone who can reach the server | Explicit operator choice; combine with the IP firewall or mutual TLS. |
| Hard links and bind mounts inside a served root are not detected | Only someone with local access to the host can create them, and the host is trusted. |
| Clipboard images are accepted by their declared `image/*` type without checking their bytes | They are displayed via `<img>` from `blob:` URLs, never as documents, so the declared type doesn't let them execute. |
| `wgcli` buffers responses without size or time limits; a killed download leaves its `<name>.<random>.wgpart` temporary file behind | The client trusts the server it is pointed at; output paths are chosen by the user. |
| The key in `sessionStorage` is readable by any script of the same origin; outbox state in IndexedDB is origin-wide | Serve each instance on its own origin. The path prefix is not an origin boundary. |
