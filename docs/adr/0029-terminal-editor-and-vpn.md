# ADR 0029: Terminal, editor and VPN inside the app

Status: **accepted** (owner decision, 2026-11-05): owner-only, off by default; built in the order editor, terminal, VPN.

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

## Rollback
All routes are additive and off by default; remove the flags and the routes disappear.
