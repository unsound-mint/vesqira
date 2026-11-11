# ADR 0029: Terminal, editor and VPN inside the app

Status: **accepted**; editor for local files implemented (`--enable-editor`, `GET`/`PUT /api/files/text`, Monaco loaded on demand; host files, terminal and VPN not built). Owner decision 2026-11-05: owner-only, off by default; built in the order editor, terminal, VPN.

## Context
The owner wants a researcher never to leave the app: edit a pipeline config or Julia file, run a command, bring the cluster VPN up. The interview decided an integrated editor with external IDE hand-off (Q39) and named Monaco and xterm.js for the desktop shell (Q148); an integrated terminal appears in the workspace panels (Q9). No VPN client appears anywhere in the interview; the host form today tells the user to bring the VPN up themselves (for example `gpclient`).

Three things are different from everything built so far, and they share one risk. The app reads files and runs fixed, validated operations. A terminal runs any command as the user, an editor writes files, and a VPN client changes the machine's network. A web page that can reach the local API must never be able to do those things by accident, by a malicious site, or from the review listener.

## Options

**A. Terminal and editor as owner-only, off by default; VPN as a button that runs the user's own client (recommended).**

*Terminal.* A pseudo-terminal on the machine running Vesqira, shown with xterm.js. Local: the user's shell. Remote: `ssh -tt <host alias>` with the same ssh and agent the app already uses. Off unless the server is started with `--enable-terminal` (or the setting is turned on in the app, which restarts the listener). Only on the loopback owner listener, never on the review listener. The connection is a WebSocket that must pass the same Host and Sec-Fetch-Site checks as the API, a per-session random token carried in the WebSocket subprotocol (not the URL), and an `Origin` equal to the page's. One session per tab, closed with the tab; an idle limit. The audit log records that a session opened and closed (host, time), never keystrokes or output.

*Editor.* Monaco for text files the app already knows how to name: pipeline configs (TOML), job scripts, Julia and Python sources, Markdown notes. A file is editable only inside a registered source root (the same canonicalising check as reading) or a host's declared roots; saves are atomic (write a temp file, rename), refuse when the file changed since it was opened (compare size and modification time, show the two versions, never overwrite silently: Q38), and are recorded in the audit log with a hash before and after. FITS and other binary files are never opened as text. Language servers and Git operations are later (Q39).

*VPN.* Vesqira does not implement a VPN. A host can carry an optional "connect command" the user sets once (for example `gpclient connect vpn.example.edu`) and a "reachable check" (the existing host check). The app shows "VPN: not reachable" with a Connect button that runs that command; because such clients need privileges, the command is run through `pkexec` or the user's own polkit rule and the app never sees a password or token (the client or polkit asks in its own window). Output is shown, the result is verified by the host check, and nothing is stored beyond the command.

- Keeps the single-user trust model: everything is the owner's own account on the owner's machine.
- Each piece is optional and separately switchable; with all off the app is as safe as today.
- Costs: a WebSocket and PTY layer in Go, a Monaco bundle (large; load it only when a file is opened), polkit handling that differs per distribution.

**B. Hand off to external programs (a "Open in terminal / editor / VPN app" button).** Smallest and safest; does not meet the goal of not leaving the app.

**C. A shared hosted terminal and editor (a web IDE service).** Needs accounts and sandboxing (ADR 0028 option B); out of proportion for one researcher.

## Recommendation
Do A in three independent steps, each behind its own switch, in this order: editor (lowest risk: confined to roots, atomic and audited saves), terminal (needs the WebSocket guards), VPN button (small, but privileged). Keep B available throughout as the fallback when a piece is off.

## What A would add (if approved)
- `GET/PUT /api/files/text` for the editor with the root check, optimistic concurrency and audit; `GET /api/terminal` (WebSocket) with the guards above; host fields `vpnCommand` and a `POST /api/hosts/{id}/vpn` that runs it.
- Layering tests: the review package must still reach nothing but the snapshot store (`internal/arch`), and a test that the terminal and editor routes are not mounted on the review listener.
- A section in SECURITY.md describing what each switch enables.

## Validation (when built)
Cross-site and cross-origin WebSocket attempts, a missing or wrong token, and a request on the review listener are refused; a save over a changed file is refused with both versions shown; a path outside the roots or through a symlink is refused; the audit log shows opens, closes and saves without contents; the VPN command is stored verbatim, run without a shell, and never receives anything from the page except "run".

## Implementation notes: editor, step 1 (local files only)
- **Switch.** `--enable-editor` sets `Server.EditorEnabled`; the routes are registered only then (off: 404) and `GET /api/capabilities` reports `features.editor`. The web app shows the entry point only when that is true; a failed capabilities request counts as off.
- **Where.** Routes live in `internal/server`, the owner listener. The review handler is a different package that `internal/arch` already forbids from reaching the server package; `TestEditorRoutesAreNotOnTheReviewListener` sends GET, PUT, POST and DELETE to it.
- **Authorisation.** The path goes through `s.roots().Resolve`, the check that gates reading (symlinks resolved first, containment by `filepath.Rel`, regular file only; opened project folders count as roots). Everything after that uses the resolved path. Also refused: credential folders (`holdsCredentials`, as the file tree), extensions outside the text allowlist (toml, yaml, yml, json, jl, py, sh, slurm, sbatch, md, txt, cfg, ini, conf, log; no extension is allowed), FITS by extension and by `SIMPLE  =`, NUL bytes, invalid UTF-8, and files over 2 MiB. Also refused (403, open and save): files under `data/raw` or `data/external` of the root that holds them, by `discover.RoleOf`, the rule the scanner uses for the explorer's `readOnly` flag. The `PUT` body is checked with `utf8.Valid` before JSON decoding (the decoder would otherwise turn invalid bytes into U+FFFD and write them). A save never creates a file.
- **Concurrency.** `GET` returns content, size, `mtime` (nanoseconds as a decimal string, since a JSON number loses digits in a browser) and the SHA-256. `PUT` must send those three as `base`; if any differs from the file on disk it answers 409 with the current file and writes nothing. "Keep mine" in the UI resends with the base the conflict returned, after an explicit confirmation. One save at a time per process; another program writing in the instant between comparison and rename is not excluded (the rename itself is atomic).
- **Atomic save.** Done through an `os.Root` opened on the root that holds the file (`sourceroot.Roots.Under`), so a folder swapped for a symbolic link after the check cannot send the write outside it: temporary file created there (random name, `O_EXCL`, 0600), permission bits copied from the original, fsync, `Root.Rename`, fsync of the folder (`writeFileAtomic`, shared with the project file writer, whose root is the project folder). Owner, ACLs and extended attributes are not carried over; a read-only folder fails the save.
- **Audit.** `file saved`, kind `file`, detail `sha256 <before> -> <after> <path>` (hashes first because the detail is cut at 300 characters). `file opened for editing`, kind `file`, detail `sha256 <hash> <path>`, is recorded for each successful open. Never contents; refused opens and refused saves are not recorded.
- **Writes are guarded like the other mutating routes**, plus: `PUT` now requires `application/json` (the check covered POST only), and any non-GET request carrying an `Origin` that differs from `Host` is refused. Through the Vite dev and preview servers this holds only because `apps/web/vite.config.ts` sets `changeOrigin: false` on both proxies (the short proxy form rewrites `Host` and every write would be refused); `TestLocalOnlyOriginMustEqualHost` documents the rule and `make e2e` exercises it.
- **Text the editor surface cannot show.** A UTF-8 byte order mark is set aside on open and written back first on save. A file with a bare CR (not part of CRLF) is not opened, with a message, because the editor would save its line breaks as LF.
- **Not yet:** host files (a later slice, with the host's declared roots), the terminal, the VPN button, language servers. The editor screen is a first version that has not been through a design review.
- **Validation done:** Go tests for outside root, sibling prefix, `..`, symlink escape (file and folder), credential folder, binary, FITS (extension, magic, symlinked), oversize, unknown extension, stale base (409 with current content), keep-mine resend, removed file, mode preserved, audit without content, cross-site and foreign Host and Origin, switch off, review listener, and the responses against the OpenAPI spec; symlinked-folder swap during a save, raw and external data refused, invalid UTF-8 body refused, opens audited; web tests for the conflict flow, the byte order mark and bare CR.

## Rollback
All routes are additive and off by default; remove the flags and the routes disappear.
