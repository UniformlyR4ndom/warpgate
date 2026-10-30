# Push to warpgate clipboard from local scripts

Status: brainstorming, nothing implemented yet. **The outbox approach is clearly preferred**;
the fragment approach is kept only as a possible fallback. The tab side was tested on Linux
(Chromium) and on a Windows VM (Edge, Defender on) on 2026-10-30, with good results (see
"Results so far"). The push side is sketched as a small pasteable shell function (see "Push
script"). Remaining experiments (see end) are run before the mechanism is finally chosen.

Decided: the feature only needs to work in browsers that have the required capability
(File System Access API, i.e. Chromium-based browsers). Elsewhere the UI hides it
(feature detection); no Firefox fallback is needed.

Decided: text only. The main use case is piping command output into the clipboard instead of
selecting and pasting it by hand. Binary data, images included, is out of scope.

## Intro / context

warpgate (`warpgate/`) is a Go file server with an embedded browser frontend (`warpgate/web/`).
Its purpose is to move data through restrictive, content-inspecting web proxies. Read
`warpgate/CLAUDE.md` first; it documents the architecture, API, auth and parameter encryption.

warpgate has a clipboard feature: notes (optional title, text, attached images) stored in SQLite.
- Server: `warpgate/clip.go`, `warpgate/clip_attachment.go`; routes in `warpgate/server.go`.
- API: `/api/clip/state|new|edit|delete` (+ attachment endpoints), all behind `withAuth`
  (`X-Auth` header). Note data is base64 in a JSON body, which can be param-encrypted
  (`internal/paramcrypt`).
- Frontend (Preact): `warpgate/web/app/lib/clipboard.js` (`pollClipboard`, `submitClip`, ...)
  and `warpgate/web/app/components/`. The auth key is kept per tab in `sessionStorage`.
  `refreshActive` (`web/app/nav.js`) auto-refreshes the open tab.

**Goal:** a local script (PowerShell, bash, ...) can push text into the warpgate clipboard,
e.g. `ls | push-to-wgclip`. Expected payloads: command output up to 512 KiB (see
"Size limits").

**Core constraint:** on the client machine, only the browser is set up to use the proxy
required for internet access. Scripts cannot reach the warpgate server directly. So the
content has to travel script → browser → server, and the browser makes the server request.

**User workflow assumption:** a warpgate tab is usually already open in the browser.

## Outbox folder watched by the open warpgate tab (preferred)

### Flow

1. One-time setup in the warpgate tab: "Connect outbox folder" → `showDirectoryPicker()` with
   `readwrite` access, e.g. `%USERPROFILE%\wgclip-outbox` (File System Access API).
2. The push function (see "Push script") writes the content file `<name>`, closes it, then
   writes the companion hash file `<name>.sha256` (see "Completion detection").
3. The open tab is notified by `FileSystemObserver` (polling as fallback), verifies the hash and
   pushes via the existing `/api/clip/new` with its normal auth and param encryption.
4. The tab cleans up (see "Cleanup by the tab"). The script waits for that and gets a real exit
   status. The tab never writes files of its own (see "Design principle").

### Watching mechanics (Chromium)

- **Change events:** `FileSystemObserver`. `observe(dirHandle)` delivers change records
  (`appeared`, `modified`, `disappeared`, `moved`, ...). It had an origin trial in Chrome
  129–134; the Intent to Ship was in Dec 2024, so it probably shipped around Chrome 133
  (unverified). Feature-detect it and fall back to polling. Works in Chromium on Linux (see
  results).
- **Polling fallback:** every ~1 s, list the folder with `for await (const [name, h] of
  dirHandle.entries())`; `getFile()` gives `size`/`lastModified`. Cheap for a small outbox.
- **Ignored files:** everything except `*.sha256` triggers nothing; `*.failed`, `*.tmp` and
  Chrome's `*.crswap` are ignored entirely.
- **Permissions:** `showDirectoryPicker` needs a click. The handle can be stored in IndexedDB.
  After a reload, `queryPermission()` returns `granted` only if "Allow on every visit" was chosen;
  otherwise `requestPermission()` needs another click (a "Reconnect outbox" button). Chrome
  refuses sensitive folders (home directory root, system folders), so use a subfolder.
- **Tab lifecycle:** watching stops if Chrome's Memory Saver discards the tab. Add an exception
  for the warpgate site, and restart watching on reload.
- **No Worker (decided from experiment 3):** watching runs in the page itself. In a hidden tab
  the observer still fires within milliseconds. Timers in the page are rounded up to ~1 s
  (a 300 ms wait took ~1.2 s), which is fine here. Pushing needs the auth key and param
  encryption, which live in the page anyway. This also avoids a second Trusted Types policy in
  the CSP, which `new Worker(url)` would need.
- **Timer pitfall:** Chrome's intensive throttling (after 5 min hidden) limits *chained* timers
  to one per minute; a plain 10 s `setTimeout` chain ran every 60 s in the test. The polling
  fallback must therefore `await` a file system call before scheduling its next `setTimeout`.
  That breaks the timer chain, and it kept 1 s polling in the test. Waits and retries started
  from observer events are not chained and are only rounded to ~1 s.
- **Auth key:** it lives in `sessionStorage`, so after a browser restart the tab has the folder
  permission but no key. The watcher then stays idle (files queue up) and the tab clearly shows
  "outbox: waiting for auth key". Accepted; the script can't tell this apart from "no tab
  open" (see "Script feedback").

### Completion detection

Tests showed that the observer reports a file while it is still being written (50 MB file seen
at 8 MB). A completion signal is required. "Size unchanged for N ms" is not enough, because a
pipeline like `slowcmd | push-to-wgclip` can pause for long stretches.

**Favored: companion hash file.** The script writes `<name>` completely, then `<name>.sha256`
(`sha256sum`-style `<hex>  <name>`, or bare hex; accept both, case-insensitive). An optional
second line holds the note title, UTF-8 (see "Note title").
- The tab acts only on `*.sha256` files. It checks they hold a valid 64-hex hash, hashes `<name>`
  (`crypto.subtle.digest`; fine within the size limit) and pushes only on a match.
- On a mismatch or missing `<name>`, it waits for further events and marks the push as failed
  after a timeout (rename to `<name>.failed`, see "Cleanup by the tab").
- The hash file is both the completion signal and an integrity check, so renaming is optional.
- It survives non-atomic copies, cross-volume moves, sync tools and scripts that don't follow
  the rules.
- The payload stays untouched until verified, and standard tools produce the hash
  (`sha256sum`, PowerShell `Get-FileHash`).
- After a hash match, the tab decodes the content as UTF-8 (`TextDecoder` with `fatal: true`)
  and strips a leading BOM (see "Push script"). Content that isn't valid UTF-8 fails the push
  ("not text").
- The hash protects integrity, not authenticity: any local process can produce a matching
  pair. See "Trust model".

### Trust model

- **Same user:** there is no sound boundary. Any process running as the user can push. A
  secret held by the push function (push key, keyed hash) would only be an extra step: it sits
  in the shell session or `$PROFILE`, and such a process can read it. Such a process could also
  read the browser profile or the system clipboard. So push keys were rejected.
- **Other users/services:** kept out by folder permissions. `%USERPROFILE%\…` is private by
  default. The bash function creates the outbox with mode 700 and warns if it is group- or
  world-writable.
- **Main remaining risk:** injected text that the user later copies out and runs. Mitigation
  (decided): pushed notes are visibly marked, and the tab shows a short notice for each push.
  - Storage: a `source` column on `clipboard` (`''` = manual, `'outbox'` = pushed), set by an
    optional `source` field in `/api/clip/new`, returned in `NoteMeta`. Editing a note keeps it.
  - It is informational, not a security property: any authenticated client can set it.
  - A title prefix was rejected: it can be edited away and clutters the title.

Considered alternative: **rename** (`<name>.tmp` → `<name>` at EOF, tab acts on the `moved`
record). Fine for well-behaved scripts, but atomic only within one file system. A move from
another volume is a copy + delete and can expose partial content again. It can still be
combined with the hash file.

### Design principle: the tab reads, deletes and renames, but never writes

Learned in the warptunnel ping-pong tests on Windows/Edge (details in `local-transport.md`):
files a page *writes* are troublesome there:
- Edge first creates them empty under their final name.
- After moving the content into place it annotates them (Mark of the Web), which takes
  60–110 ms per file.
- Deleting or opening such a file during that time makes the write fail and the file vanish.
- Even untouched, about 0–5 in 1000 writes fail and need a retry.

Files written by *other* processes and only read/deleted by the page are not affected. So in
the outbox design **the script writes and the tab only reads, deletes and renames**. There are
no `.done`/`.err` files. Speed doesn't matter here, so every step can be patient.

### Cleanup by the tab

Uses `readwrite` folder access (`removeEntry()` deletes permanently, no recycle bin;
`FileSystemFileHandle.move()` renames). The permission prompt for write access sounds scarier;
"Allow on every visit" applies to it as well.

- **Patient pick-up:** after `<name>.sha256` appears, wait briefly (e.g. 200–500 ms) before
  reading. Polling is an acceptable fallback; latency doesn't matter.
- **After a successful push** (server returned the note ID): delete `<name>.sha256` first (the
  trigger, so a crash right after can't cause a second push), then `<name>`.
  - Each deletion is retried several times with growing waits, in case a scanner or the
    script still holds the file.
  - If deleting `.sha256` keeps failing, the tab remembers it as pushed (name + hash, in
    IndexedDB), so it is never pushed twice.
  - An orphaned content file without a `.sha256` is never pushed; delete it after a timeout or
    leave it.
- **Retries and duplicates:** if the server created the note but the response was lost, the
  tab's retry creates a second note. Accepted.
- **On failure** (hash mismatch/missing content after a timeout, too large, not text, server
  error):
  rename `<name>.sha256` to `<name>.failed`. A rename is not a write; it took 32 ms and succeeded
  in the Edge diagnostics, but only for a browser-created file so far (to be verified for
  script-written files). The error details are shown in the warpgate tab.
- **Leftovers:** with `-NoWait` (see "Push script") nobody waits for `<name>.failed`, so the tab
  deletes `<name>.failed` and its `<name>` itself after a while (e.g. 1 h), once the error has
  been shown.
- **Script feedback** (check every ~200 ms, timeout ~30 s):
  - `<name>.sha256` and `<name>` gone → success;
  - `<name>.failed` appears → failure ("see the warpgate tab"); the script removes its files
    only after a short delay (a rename takes ~70 ms in Edge and is probably checked like a
    write);
  - nothing after the timeout → "not picked up: no warpgate tab open, outbox not connected, or
    auth key not entered? The push stays queued." Leaving the files queued matches `-NoWait`.
  - The tab's own failure timeout (mismatch/missing content) must be clearly shorter than the
    script's, e.g. 10 s vs. 30 s. Otherwise the script reports "not picked up" for a push that
    the tab later marks failed.
- **Several warpgate tabs** watching the same outbox would push twice. Use the Web Locks API
  (`navigator.locks.request('wg-outbox', ...)`) so only one tab of the origin processes the
  outbox.

### Size limits

Safeguard against misuse, e.g. piping a huge log or a binary file. Without limits the failure
would be ugly; see the server limit below.

**Existing server limit:** `/api/clip/new` reads its body through `decodeParams`
(`warpgate/paramdecode.go`), capped at `maxParamBodySize` = 1 MiB. The content is base64 in
JSON (×4/3), and with param encryption the JSON is base64-encoded again plus overhead. So the
effective raw content limit is about **750 KiB unencrypted, about 560 KiB encrypted**. An
oversized body currently fails as `401 invalid request body`, a misleading error.

**Decided: a fixed 512 KiB content limit; the server limit is not raised.** 512 KiB fits even
with param encryption (≈ 512 × 4/3 × 4/3 ≈ 910 KiB body), leaving room for the title and JSON
overhead. The same fixed value applies in the script and the tab, independent of the
param-encryption setting.

**Script limit:**
- 512 KiB, fixed (no override: anything larger would fail at the server anyway).
- Count bytes while copying stdin into the outbox file; never buffer unbounded input in memory.
- On exceeding the limit: stop reading, delete the partial file, write no `.sha256`, print
  "too large (> N KiB); push-to-wgclip is meant for text/small content" and exit non-zero.
- For an explicit file argument, check the size before copying.

**Watcher limit:**
- Before reading or hashing, check `getFile().size` (metadata only, cheap) against 512 KiB.
- On exceeding the limit: don't read, don't push; rename `<name>.sha256` to `<name>.failed`
  (so it isn't retried) and show "too large: N bytes, limit M" in the tab. The script reports
  the failure and removes its content file. Files the user drops in manually stay until
  cleaned up.
- Also ignore oversized content files that have no `.sha256` yet, i.e. never read them
  speculatively.

### Push script (PowerShell / bash function)

The pushing side stays a small shell function, not a binary.

**Workflow:**
1. The warpgate tab offers "Copy PowerShell function" / "Copy bash function", showing the code
   openly before copying (the user runs code that came from a web page).
2. The user loads it into the shell, which defines `push-to-wgclip` for that session: after Copy,
   `Get-Clipboard -Raw | Invoke-Expression` (pasting directly can crash Windows PowerShell 5.1's
   PSReadLine when the paste is taller than the window; see experiment 7). To keep it, put it in
   `$PROFILE` / `~/.bashrc`.
3. Use: `ls | push-to-wgclip`, `push-to-wgclip -Path file.txt` / `push-to-wgclip file.txt`.

**Why pasting instead of a `.ps1` file:**
- The PowerShell **execution policy only governs script files**. A function defined
  interactively works even under `Restricted`, `AllSigned` or `RemoteSigned`, which corporate
  clients often enforce by group policy.
- `$PROFILE` is a script file again: under `Restricted` the user has to paste in every new
  session.

**Outbox path:** the browser only tells the page the *name* of the picked folder, not its full
path, so the page can't fill it in. Use a fixed default (`%USERPROFILE%\wgclip-outbox`,
`~/wgclip-outbox`) that the page asks the user to pick, overridable via an environment variable
(`WGCLIP_OUTBOX`). Pitfall seen in testing: PowerShell doesn't expand `%USERPROFILE%`, use
`$env:USERPROFILE`.

**What the function does:**
1. Collect input (pipeline/stdin or a file argument).
2. Write `<name>` under a unique name (timestamp + random), enforcing the size limit.
3. Write `<name>.sha256` after the content file is closed.
4. Wait, checking every ~200 ms for up to ~30 s:
   - both files gone → success;
   - `<name>.failed` → failure ("see the warpgate tab"), clean up after a short delay;
   - timeout → "not picked up …" (see "Script feedback").
5. Exit code and a short message. A `-NoWait` switch queues the push without waiting (e.g. no
   tab open yet).

**Note title:** a `-Title` option (bash: `-t`). Default: the file name for a file argument,
otherwise empty (or maybe the timestamp). It is written as the optional second line of
`<name>.sha256`. That breaks strict `sha256sum -c` compatibility, which isn't needed. The tab
sanitizes it like the UI does (control characters → spaces, ≤ 200 characters). Encoding it in
the file name was rejected because of the limited character set and length.

**PowerShell must work in Constrained Language Mode** (AppLocker/WDAC environments block .NET
calls such as `[IO.File]::…`). Everything needed exists as cmdlets:

| Task | CLM-compatible way |
|---|---|
| Collect pipeline input | `$input` in the `end` block → `Out-String -Stream -Width 4096` (one table header for the whole input, no line truncation), lines joined with LF |
| File argument (copied as is) | `Get-Item` for the size check, `Copy-Item` into the outbox |
| Write text | `Set-Content -Encoding UTF8 -NoNewline` |
| Size check | `(Get-Item $f).Length` after writing; delete if too big |
| SHA-256 | `Get-FileHash -Algorithm SHA256` (user has used it under CLM before) |
| Unique name | `Get-Date -Format …` plus `Get-Random` |
| Hash file | `Set-Content -Encoding UTF8` (title may be non-ASCII; 5.1 adds a BOM, which the tab strips here too) |
| Wait for the outcome | `Test-Path` in a loop with `Start-Sleep -Milliseconds 200` |
| Clean up after a failure | `Remove-Item` |

Two compromises:
- **UTF-8 BOM:** Windows PowerShell 5.1 writes UTF-8 *with* a BOM, and avoiding it needs a .NET
  call. The **tab strips a leading UTF-8 BOM** (`EF BB BF`) from pushed content, after the
  hash check (the hash covers the file as written). PowerShell 7 writes no BOM anyway.
- **Pipeline size limit isn't streaming:** the function stops keeping formatted lines at the
  limit, but can't stop the upstream command. PowerShell itself buffers the raw pipeline objects
  for `$input`. File arguments are checked before copying.

Pitfalls found in testing:
- `$items += $x` copies the whole array per item in 5.1 (quadratic), so `$input` is used instead.
- `$PSBoundParameters.ContainsKey()` is a method call that CLM blocks.
- In 5.1, `Get-FileHash` is script code in `Microsoft.PowerShell.Utility`. If that module is first
  loaded *after* a manual switch to CLM, it runs restricted and fails. Under a real AppLocker/WDAC
  policy, System32 modules are trusted, so this only affects the manual simulation. The test
  runner loads it before switching.

Test CLM on any Windows box: `$ExecutionContext.SessionState.LanguageMode =
"ConstrainedLanguage"` switches the current session (one-way). A real AppLocker/WDAC policy may
additionally block `powershell.exe` itself.

**Bash:** `head -c $((limit+1))` enforces the limit without unbounded buffering; `sha256sum`
(or `shasum -a 256` on macOS); `mktemp` or date + `$RANDOM` for names; `sleep 0.2` loop for
waiting.

**Size estimate:** PowerShell ~40 lines minimal, ~80–120 polished (help, options, clear
errors); bash ~30 minimal, ~50–80 polished. Keep the pasted version compact (≤ ~80 lines,
sparse comments); multi-line paste works in PowerShell (PSReadLine) and bash.

### User experience

The terminal keeps focus and no window pops up; the note just appears in the open warpgate tab
(auto-refresh). If no tab is open, files wait in the outbox until warpgate is opened.

### Pros / cons

- \+ No focus loss, no tabs; no URL length limit (only the size limit above).
- \+ No push token needed (the local file system is the trust boundary); nothing in browser
  history.
- \+ Simple scripts in any language; real success/failure feedback.
- \+ Mostly a frontend change: the server only needs storage for the "pushed" marker (see
  "Trust model").
- − Chromium only (Chrome/Edge). Firefox has neither `showDirectoryPicker` nor
  `showOpenFilePicker`. `<input type="file" webkitdirectory>` gives only a fixed list taken when
  the folder is picked (new files stay invisible). Re-reading a file once picked via
  `<input type="file">` after it changed on disk is browser-specific and unreliable (Chrome
  throws `NotReadableError`; Firefox untested). And there is no way to write or delete, so no
  handshake. Accepted: the feature is simply unavailable there (see Status).
- − Requires folder permission: "Allow on every visit" exists since Chrome 122, otherwise one
  click per session. Enterprise policy can block the API.
- − Hidden tabs slow down timers: waits and retries are rounded up to ~1 s, and chained timers
  run only once a minute after 5 min hidden. Harmless with the observer and an `await`-based
  polling loop (see "Watching mechanics").
- − Requires an open warpgate tab.

## Fallback: push tab launched with a URL fragment, authenticated by a push token

Deprioritized. Only relevant if the outbox approach turns out to be unusable on target clients
(e.g. policy blocks the File System Access API) or pushes without an
open warpgate tab are needed.

### Flow

```
ls | push-to-wgclip
  └─ gzip → base64url → Start-Process / xdg-open
       https://host/<prefix>/static/push.html#t=<pushToken>&d=<payload>

push.html (new small page)
  ├─ reads the fragment, then history.replaceState() to strip it
  ├─ decompresses with DecompressionStream('gzip')
  ├─ POST /api/clip/push   (authenticated by the push token, param-encrypted with keys
  │                          derived from the token, e.g. paramcrypt.DeriveKeys(token))
  ├─ BroadcastChannel → open warpgate tab: "note #N pushed" (refresh / highlight)
  └─ window.close()
```

The fragment is never sent over the network. Only the POST goes through the proxy, and it can
be encrypted like the existing clipboard requests.

### Push tokens (rough idea; details deferred)

- Generated by the user in the UI; they authorize **only** creating notes (no read, edit or
  delete). Meant to be embedded in push scripts.
- Short-lived (TTL chosen at creation) and revocable. Likely stored hashed in the existing
  SQLite DB.
- Still needed even when the main tab is open: without a token, any link (e.g. in an email)
  pointing to `push.html#d=...` could inject clipboard content via a hand-off to the main tab.
- Deferred details: binding tokens to source IPs (so a found script can't be abused), storage
  format (plaintext token vs. token ID + derived keys), management endpoints and UI, generated
  vs. parameterized scripts, `wgcli` support.

### User experience

1. User runs the script in the terminal.
2. The browser opens a new tab and usually jumps to the foreground, taking focus from the
   terminal. Switching back from the script (PowerShell `AppActivate`) is unreliable because of
   Windows' focus-stealing rules.
3. The push tab finishes quickly and closes itself. Per the HTML spec, a tab whose history
   has exactly one entry may call `window.close()`, and an OS-launched tab has one entry.
   Expected to work in Chrome/Edge; Firefox needs testing.
4. The open warpgate tab is notified and shows the new note.
5. Focus goes back to the previously active tab; the terminal stays in the background.

Net effect: a browser "flash" plus lost terminal focus on every push.

### Pros / cons

- \+ Works in all major browsers; no folder setup; works without an open warpgate tab.
- − Focus is taken from the terminal on every push.
- − Size is limited by the OS launch path (Windows command line is about 32 K; some
  `ShellExecute` paths may cap URLs lower). gzip helps; scripts must fail clearly when the
  payload is too large.
- − The full URL (token + content) may land in browser history before `replaceState` runs.
- − The script only knows the browser was launched, not whether the push succeeded.
- − `Start-Process <url>` opens the OS default browser, which may not be the one configured for
  the proxy; scripts may need an optional browser path.
- − Needs server changes (push-token storage, endpoints, `/api/clip/push`).

## Ruled out for now: loopback listener polled by the page

The script listens on `127.0.0.1:<port>` and the warpgate page polls it with `fetch()`.
This is proven to work: the sibling project `warptunnel/` uses exactly this for port
forwarding. It is set aside because of:
- Chrome's Local Network Access permission prompt (and possible enterprise policy blocks);
- the overhead: a listener in every script, `nc`/`socat`/`python` needed for bash, CORS and
  preflight handling, and constant polling.

## Experiments

The early outbox results came from a temporary test page (`outbox-watch-test.html`, served by a
minimal server; no longer kept). It picked a folder, watched it via observer / polling / polling
in a Worker, and logged changes with their delay since the file's mtime. It never implemented
the `.sha256` handshake. The warptunnel ping-pong page (`pingpong/pingpong.html`) adds write
diagnostics and the external-files test (tab reads/renames/deletes script-written files).

Outbox approach:
1. Is `FileSystemObserver` available in the target Edge/Chrome versions, and does it report
   changes made by other processes? *Works in Chromium/Linux and in Edge 156 on Windows
   (ping-pong test); open for real corporate clients.*
2. Does `showDirectoryPicker` work on the target clients (policy)? Is "Allow on every visit"
   offered, also for `readwrite`, and does it survive a reload and a browser restart?
3. How quickly are files picked up while the tab is hidden for more than 5 minutes: observer,
   polling in the page, polling in a Worker? *Linux/Chromium, observer in a Worker: immediately,
   30 min hidden.*
   *Done 2026-10-30 on the VM, Edge 156, tab hidden ~14 min (background tab, window minimized),
   30 files written by another user every 30 s, all four detectors at once (test page, not in the repo:
   `push-to-wgclip/test/hidden-tab/`). Delay after the write, hidden, median/max in ms:
   observer in the page 2/9, observer in a Worker 4/21, polling in the page 254/377, polling in
   a Worker 504/960. A 300 ms timer after detection: page 1204/1299, Worker 314/329. Values
   after more than 5 min hidden were no worse. A plain chained 10 s timer ran every 60 s (intensive
   throttling active). Conclusion: no Worker needed.*
4. Does polling alone (observer disabled) pick up changes reliably? *Linux: yes, with a delay of
   about one poll interval (ping-pong test).*
5. Can the tab read, **rename** (`move()`) and **delete** files a *script* wrote, also right
   after they appear (while a scanner may hold them)? Test: "External files" section of
   `pingpong/pingpong.html`. *Done for the Windows VM/Edge: all succeeded immediately (see
   results).*

Push script:
6. Does the PowerShell function work end-to-end in a Constrained Language Mode session (and in
   Windows PowerShell 5.1 and PowerShell 7)? Does pasting work under `Restricted` policy?
   *Done 2026-10-30 on the VM, against a fake tab (`push-to-wgclip/test/watcher.ps1`, not in the repo): all 15
   cases pass in 5.1 and 7.6.9, each in FullLanguage and simulated CLM. The definition via
   `Invoke-Expression` works under `Restricted`. Interactive multi-line paste is known to work
   (user experience). A real AppLocker/WDAC policy is deferred: it is rare in practice, and the
   more likely case is that PowerShell is blocked entirely, which makes the feature unusable
   anyway.*
7. Does a full push (script → tab → server) work on the Windows VM and later on a corporate
   client?
   *VM, 2026-10-30, Edge 156, Windows PowerShell 5.1, implemented tab side: a manual
   `"…" | push-to-wgclip` arrives as a marked note. A push that timed out because the wrong folder
   was connected stayed queued and was pushed as soon as the right folder was connected.
   Pitfall: pasting the 80-line function directly crashed PSReadLine 2.0 (Windows PowerShell 5.1's
   version) because the paste was taller than the console window; `Get-Clipboard -Raw |
   Invoke-Expression` works. Later the same day: pushes as another Windows user via SSH, Windows
   PowerShell 5.1 (CLM) and PowerShell 7 (with and without CLM), each ~0.5 s for a success and
   ~1.4 s until the script reported a tab-side failure (not UTF-8); the script-side size check
   needs no tab. With the tab hidden 6 and 7 minutes (background tab, window minimized), pushes
   took 0.7 s and 1.5 s. Open: a corporate client.*

Fallback (only if needed):
8. Does an OS-launched tab close itself with `window.close()` in Edge/Chrome and in Firefox?
9. What is the largest URL that survives `Start-Process` / `ShellExecute` → browser on Windows?
   Is the fragment kept in browser history despite `replaceState`?
10. How bad is the focus loss in practice, and does `AppActivate` reliably switch back to the
    console?

### Results so far

All 2026-10-30. Unless noted: Chromium on Linux, read-only folder access, observer + polling
enabled.

- Windows/Edge 156 (user's VM), from the warptunnel ping-pong tests (`local-transport.md`):
  `FileSystemObserver` works; reading and deleting files written by another process works.
  Files *written by the page* are annotated and occasionally fail, hence the design principle
  above.
- **External-files test**, Windows VM, Edge 156, page via `http://localhost`, Defender on: a
  PowerShell loop wrote 50 files each of `read-*`, `move-*`, `delete-*` (200 ms apart); the page
  acted on each **immediately** (no wait). **150/150 succeeded on the first try**, no errors.
  - Checked on disk afterwards: 50 `move-*.txt.failed` and the 50 `read-*.txt` remain; all
    `delete-*` files are gone.
  - read: median 2.1 ms, max 6.4 ms;
  - delete (`removeEntry`): median 0.6 ms, max 9.9 ms;
  - rename to `*.failed` (`move()`): median 70.7 ms, max 182 ms. That is in the range of an
    annotated page write, so Edge probably runs the same post-write checks for a rename. A
    script that reacts to `<name>.failed` should therefore not delete it immediately (wait a
    second or leave cleanup to the tab), to avoid breaking that check.

- `FileSystemObserver` works for files written by an external process. Each write produced one
  `appeared` + `modified` record batch; the file was seen about 10 ms after its mtime. Deletion
  produced a `disappeared` record. The polling fallback was not checked separately: it only
  logs differences, and the observer had already caught them.
- Hidden tab (from the warptunnel ping-pong endurance run, see `local-transport.md`): Chromium on
  Linux, `FileSystemObserver` in a Web Worker, tab hidden for 30 minutes: every file was noticed
  within milliseconds, no throttling, no missed events.
- Rename (`a.txt` → `b.txt`): the observer sends a single `moved b.txt (from a.txt)` record.
  The test page logs this as `disappeared` + `appeared` only because it compares listings by
  name. A rename keeps the mtime, so the "seen N ms after mtime" value is meaningless for renames.
- 50 MB file written directly: `appeared` came at 8 MB, followed by `modified` at the full size
  about 10 ms later. A file may thus be picked up while it is still being written → completion
  detection via the companion `.sha256` file (see above).


## Related, separate feature: image download in the clipboard tab

Not part of push-to-clip (images aren't pushed this way). Attached images get a download button
and an optional caption; the caption becomes the download file name (falling back to e.g.
`note<id>-<n>.<ext>` from the MIME type). Storing the caption needs a server change: a column
in `clip_attachment` and a field in `AttachmentMeta`.
