# Warpgate

Warpgate is a file server designed to bypass restrictive web proxies that perform deep-packet inspection. Files are served through a configurable *transform* that disguises their content, for example as an encrypted byte stream, a PDF attachment, or a base64-encoded HTML page. The browser-based frontend reverses the transform to recover the original file. Uploads work in the opposite direction: the frontend applies the transform before sending and the server reverses it on receipt.

All frontend assets are embedded in the server binary; no internet access is required at runtime.

## Usage

```
warpgate [flags]
```

Build:

```bash
go build -o warpgate .
```

Run:
```bash
./warpgate -root ./files -cert server.crt -cert-key server.key
```

## Command Line Parameters

### File serving

| Flag | Default | Description |
|------|---------|-------------|
| `-root <path>` | `./files` | Directory to serve files from (download root). |
| `-ul-root <path>` | — | Directory to store uploaded files. Upload endpoints are disabled if this flag is omitted. |
| `-listen <host:port>` | `0.0.0.0:8080` | Address to listen on. |
| `-path-prefix <segment>` | — | Serve the UI and API only under `/<segment>/` (one path segment of `[A-Za-z0-9._~-]`, leading `/` optional). Every other path returns an empty `200 text/html` page. The UI is then at `/<segment>/static/`; point `wgcli -url` at `http://host:port/<segment>`. |

### Authentication

| Flag | Default | Description |
|------|---------|-------------|
| `-auth-key <key>` | — | Secret key clients must supply in the `X-Auth` HTTP header. If omitted, a random 32-byte hex key is generated at startup and printed to the log. |
| `-no-auth` | false | Disable authentication entirely. No `X-Auth` header is required. |

### IP firewall

Restricts which client IPs are served at all. Enabled as soon as any of `-ip-allow`, `-ip-reg-path`, `-ip-reg-max` or `-ip-reg-window` is set. Rejected requests get the same empty page as a wrong `-path-prefix`.

| Flag | Default | Description |
|------|---------|-------------|
| `-ip-allow <list>` | — | Comma-separated IPs/CIDRs that are always served, e.g. `198.51.100.7,203.0.113.0/24`. |
| `-ip-reg-path <segment>` | — | Visiting `/<prefix>/<segment>` in a browser registers the client IP (the page registers via a script-submitted POST, so link scanners that only GET it don't). Must not be `static` or `api`. |
| `-ip-reg-max <n>` | `0` | Maximum number of registered IPs (`0` = unlimited). |
| `-ip-reg-window <duration>` | `0` | Accept registrations only this long after startup, e.g. `15m` (`0` = unlimited). |
| `-trusted-proxies <list>` | — | Comma-separated IPs/CIDRs of reverse proxies; for requests from them, the client IP is the rightmost `X-Forwarded-For` entry that isn't itself a trusted proxy. |

If `-ip-reg-max`/`-ip-reg-window` are set without `-ip-reg-path`, the base path `/<prefix>/` is the registration URL (requires `-path-prefix`); `/<prefix>/static/` registers as well, so reloading the UI after a server restart works. Registrations are kept in memory only and are lost on restart.

### TLS

All TLS flags require `-cert` and `-cert-key` to be provided together. Without them the server speaks plain HTTP.

| Flag | Default | Description |
|------|---------|-------------|
| `-cert <path>` | — | TLS certificate file (PEM). May be a full chain (leaf + intermediates). Must be paired with `-cert-key`. |
| `-cert-key <path>` | — | TLS private key file (PEM). Must be paired with `-cert`. |
| `-client-root <path>` | — | PEM certificate used as the trust anchor for TLS client authentication. Connections are rejected unless the client presents a certificate with a valid chain to this root. Requires `-cert`/`-cert-key`. |
| `-client-fp <hex>` | — | SHA-256 fingerprint (hex) of the exact client certificate to accept. Can be used alone (no chain check) or combined with `-client-root` (chain check + pin). Requires `-cert`/`-cert-key`. |



## Transforms

A transform is applied server-side when serving a download and reversed when receiving an upload. The frontend applies the inverse transform on download and the forward transform on upload. The transform type and its parameters are chosen in the side panel and sent as part of each request.

The File System Access API used for chunk reassembly on downloads requires a secure context (HTTPS or localhost).

---

### `id` — Identity (no-op)

Passes data through unchanged. Useful for testing or when no disguise is needed.

**Parameters:** none.

---

### `xor` — XOR

XORs every byte of the data with a repeating key. The same operation encodes and decodes (symmetric).

**Parameters:**

| Parameter | Format | Description |
|-----------|--------|-------------|
| `key` | hex string, any length | Key bytes, repeated cyclically over the data. |

The frontend can auto-generate a random key. The key is sent in plaintext with each request, so this transform only provides meaningful protection when used over HTTPS.

---

### `aes-ctr` — AES-CTR

Encrypts data using AES in CTR (counter) mode. Symmetric — the same operation encodes and decodes.

**Parameters:**

| Parameter | Format | Description |
|-----------|--------|-------------|
| `key` | hex string, 16/24/32 bytes | AES key (128, 192, or 256 bit). |
| `iv` | hex string, 16 bytes | Initial counter value (one AES block). |

The frontend can auto-generate a random key and IV. Both are sent in plaintext with each request; HTTPS is required for this to provide real confidentiality.

---

### `pdf` — PDF attachment

Embeds the file data as an attachment inside a PDF document, making the transfer look like a PDF download to an inspecting proxy.

Encode and decode are different operations (asymmetric).

**Parameters:**

| Parameter | Format | Description |
|-----------|--------|-------------|
| `filename` | string | Filename stored as the attachment name inside the PDF. Defaults to `attachment`. |
| `password` | hex string | Optional. Encrypts the PDF with AES-256 (download only). A random password is generated per request by the frontend and never stored server-side. |

**Notes:**
- Password protection applies to downloads only. The upload path uses `pdf-lib`, which does not support PDF encryption, so uploaded PDFs are always unencrypted.
- The server uses [pdfcpu](https://github.com/pdfcpu/pdfcpu) to create and parse PDFs. The browser uses [pdf.js](https://mozilla.github.io/pdf.js/) to decode downloads and [pdf-lib](https://pdf-lib.js.org/) to encode uploads.

---

### `html-b64` — HTML / Base64

XORs the data with a random key and base64-encodes the result, embedding it in a minimal HTML page as the `src` attribute of an `<img>` tag. The server responds with `Content-Type: text/html`, making the transfer appear as an ordinary web page load.

Symmetric — the same XOR key encodes and decodes.

**Parameters:**

| Parameter | Format | Description |
|-----------|--------|-------------|
| `key` | hex string, exactly 16 bytes | XOR key applied before base64 encoding. |

The frontend generates a fresh random key for every request. The key is sent in plaintext with each request; HTTPS is required for meaningful confidentiality.
