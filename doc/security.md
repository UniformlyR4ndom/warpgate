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
- No transform authenticates the payload. Tampering in transit is not detected: a modified payload
  may decode without error into modified file content (with `xor` and `aes-ctr`, flipping a bit of
  the payload flips the same bit of the file).
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

**Hiding and admission**
- `-path-prefix`: everything outside the prefix gets an identical blank page.
- IP firewall (`-ip-allow`, registration URL): unadmitted clients get the same blank page. Behind
  `-trusted-proxies`, a request whose client cannot be determined is blocked (fail closed).
- A registration URL is an admission secret, not an identity: anyone who learns it can claim a slot.

**Filesystem**
- All file access goes through `os.Root` handles; symlinks leaving a root are refused and left
  out of listings. Which root (download or upload) a request uses is fixed by its route,
  never by a request field.
- Uploads are written to a temp directory and renamed over the target only when complete, so an
  aborted or failed upload never touches the existing file. The temp directory is unreachable
  through the API under any spelling.
- File and directory names are validated, including names Windows would reinterpret.
- The server refuses to start if the database, certificate or key lies inside a served root.

**Browser**
- Strict CSP on every response: `script-src 'self'` with no `unsafe-*`, no framing, Trusted Types
  with a single policy that only allows the pdf.js worker URL.
- No string HTML sinks; note text, titles, captions and filenames are rendered as text.
- The raw key is kept in `sessionStorage`, so it doesn't outlive the tab.
- Vendored libraries are kept current (pdf.js 6.4.299).

**CLI (`wgcli`)**
- TLS verification is on unless `-insecure` is given.
- API requests never follow redirects, so the token can't be forwarded to another host.

## Accepted risks

| Risk | Why it is accepted |
|------|--------------------|
| No size limits or quotas on uploads, clipboard notes or attachments in total, no cap on concurrent transfers | Authenticated clients are trusted, and large transfers are the point of the tool. Use a dedicated, quota-limited filesystem if this matters for a deployment. |
| A small compressed PDF upload can expand to a large file and allocate several times that in memory before size checks apply | Requires authentication. Memory use scales with the expanded size, much like uploading a large plain file. Bounding it would require isolating or patching the PDF library. |
| The auth token is a replayable bearer credential; requests with unencrypted parameters are always accepted | Accepted for simplicity. Use TLS, and the IP firewall or mutual TLS, where capture is a concern. |
| A captured token or encrypted request allows offline passphrase guessing | Each guess costs a 128 MiB scrypt run. Only a PAKE would prevent it, at the cost of heavy dependencies. Use a strong passphrase where capture is a concern. |
| An attacker with many IPv6 /64s, or more than 100,000 addresses, gets more online guesses | Beyond 100,000 tracked clients the rest share one failure entry, which bounds memory and still limits them collectively. |
| `aes-ctr` keystream reuse; no payload integrity | Transforms are disguises, see above. |
| `-no-auth` exposes all of the above to anyone who can reach the server | Explicit operator choice; combine with the IP firewall or mutual TLS. |
| Hard links and bind mounts inside a served root are not detected | Only someone with local access to the host can create them, and the host is trusted. |
| Clipboard images are accepted by their declared `image/*` type without checking their bytes | They are displayed via `<img>` from `blob:` URLs, never as documents, so the declared type doesn't let them execute. |
| `wgcli` buffers responses without size or time limits and may leave a partial output file when a transfer fails | The client trusts the server it is pointed at; output paths are chosen by the user. |
| The key in `sessionStorage` is readable by any script of the same origin; outbox state in IndexedDB is origin-wide | Serve each instance on its own origin. The path prefix is not an origin boundary. |
