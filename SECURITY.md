# Security policy

Vesqira has no released software yet. This policy covers the repository and, later, the application.

## Reporting a vulnerability

Please use GitHub's **private vulnerability reporting** (Security tab → "Report a vulnerability") once the repository is published. Do not open a public issue for a vulnerability. A dedicated security contact address is not yet set up.

## Areas we care most about (from `AGENTS.md`)

- Reviewers reading raw data, HPC sessions, or secrets through indirect URLs.
- Path traversal or SSRF through dataset registration and remote connectors.
- Untrusted FITS/PDF/TeX input causing resource exhaustion or code execution.
- Remote-access credentials or secrets appearing in logs, provenance, or shared reviews.

## Who can reach the owner API

The owner API (`--addr`, loopback, no accounts) is the whole app: catalog, hosts, job submission and, when enabled, the editor, the terminal and the VPN command. What stops whom:

- **Web pages (any browser tab, any site):** refused by `Host` (DNS rebinding), `Sec-Fetch-Site`, a differing `Origin` on writes and JSON-only bodies; the terminal also needs a one-time token and an `Origin` equal to the page's.
- **Other user accounts on the same computer (Linux):** refused by a peer-uid gate on the listener. For every accepted connection the server looks the peer socket up in `/proc/net/tcp` and `tcp6` (IPv4, IPv6 and IPv4-mapped forms) and closes it, before any HTTP is read, unless the socket belongs to the uid the server runs as. If the table cannot be read or the connection is not found, it is refused (fail closed); the server will not start if `/proc/net/tcp` is unreadable. Refusals are counted and logged (one line per 10 s, no addresses or request data). `--allow-other-local-users` switches the gate off and is dangerous: on a shared workstation or HPC login node every account can then use the app as you.
- **Other processes of the same account:** trusted. This is the single-user model (ADR 0029, ADR 0036): anything running as you can already read your files and run commands, so the app does not try to separate itself from it.
- **macOS and Windows:** the peer-uid check does not exist there. Any local account can reach the owner API. The server logs a warning at start and refuses to start with `--enable-terminal`, `--enable-editor` or `--enable-vpn` unless `--allow-other-local-users` is given. Use these platforms only on a computer that has one user.
- **Not gated, by design:** the review listener (`--review-addr`, published previews for reviewers) and the publish server are separate listeners made for other people; they have no owner routes.

## Optional features that write or run things

Each is off unless the server is started with its flag, and only ever on the loopback owner address, never on `--review-addr` (ADR 0029).

### `--enable-editor`

Lets the person at this computer open and save text files from the app (`GET` and `PUT /api/files/text`). What it enables, and what it does not:

- It can **overwrite files** inside the source roots and opened project folders, with the privileges of the user running Vesqira. Turn it on only on a machine where that user is you.
- Only plain text of a known kind (toml, yaml, json, jl, py, sh, slurm, md, txt, cfg, ini, or no extension), at most 2 MiB. FITS, binary and invalid UTF-8 are refused, as are credential folders (`.ssh`, `.gnupg`), anything outside the roots, symbolic links that leave them, and files that do not exist yet.
- A save is refused with both versions shown when the file changed since it was opened, and is atomic (temporary file, fsync, rename; mode kept).
- Saves are in the audit log with the path and the SHA-256 before and after, never the contents.
- Requests must be same-origin: foreign `Host`, `Sec-Fetch-Site` other than same-origin or none, a different `Origin`, and non-JSON bodies are refused.
- It does not read or write files on remote hosts, and does not run anything.

### `--enable-terminal`

Lets the person at this computer open a shell from the app: the user's login shell here, or `ssh -tt` to a registered host (`POST /api/terminal/sessions`, then a WebSocket at `GET /api/terminal`). This is the most powerful switch: **a terminal runs any command with every privilege of the user running Vesqira**, and over ssh with that user's access to the host. Turn it on only on a machine where that user is you.

- Only on the loopback owner address, never on `--review-addr`; not available in multi-user mode (the group server refuses to start with it). Only the server's own user account reaches that address on Linux (see above); other platforms need `--allow-other-local-users`.
- A session needs a one-time token from an authenticated same-origin `POST`, carried in the WebSocket subprotocol (never in the URL), valid for 60 seconds and consumed by the first attempt. The upgrade also needs an `Origin` equal to the page's, a loopback `Host` and no cross-site `Sec-Fetch-Site`; a foreign page cannot open a session even if it can reach the port.
- The page chooses a host by id from the registered hosts; it cannot name a command, a binary or an ssh destination. ssh uses your own configuration and agent; a passphrase or one-time-code prompt is typed in the terminal and never seen by Vesqira.
- Closing a session hangs up and then kills every process of the shell's session, including jobs the shell put in their own process groups or that ignore SIGHUP. A process that deliberately starts a new session (`setsid`) is not tracked.
- The environment is the full environment of the user running Vesqira (a local shell sees all of it; ssh sends what `SendEnv` in your ssh configuration says). The terminal shares the workers' ssh `ControlPath`: a master connection opened in the terminal is reused by the background workers (which run `BatchMode`), and the other way round.
- One session per browser tab, at most 8 at once, ended after 30 minutes without input or 8 hours in all, or when the page goes away. Input, resize and output frames are size-bounded.
- The audit log records that a session opened and closed (where, how long, why), never what was typed or shown. The terminal itself keeps no transcript; do not paste secrets into it on a shared screen.
- It does not give a reviewer anything: the review listener has no terminal routes.

### `--enable-vpn`

Lets a host carry the command line of **your own VPN client** (for example `gpclient` or `openconnect`) and run it from a Connect button when the host is unreachable (`POST /api/hosts/{id}/vpn`, set with the host form or `POST /api/hosts/{id}/vpn-config`). Vesqira implements no VPN. Storing a command that the app later runs is the same power as the terminal: **anything that can use the owner API can make Vesqira run a program as you**. Turn it on only on a machine where that user is you.

- Only on the loopback owner address, never on `--review-addr`; the same guards as the rest of the owner API apply (web pages refused, other user accounts refused on Linux, and other platforms need `--allow-other-local-users`). Off: the two routes answer 404 and a host with a command is refused with 403.
- The command is an argv (a JSON list of strings): the first part an absolute path or a bare program name looked up in `PATH` when it runs, at most 16 parts, 512 bytes each and 2048 in all, no NUL or line breaks. It is stored exactly as written and run with `exec`, **never through a shell**, so `;`, `$`, quotes and backticks in a part are plain text. Only the owner's host form sets it, and the page shows it as stored.
- The page sends nothing but `{"action":"connect"}` or `{"action":"disconnect"}`; any other field is refused. It cannot name a program, an argument or an environment variable.
- It runs as the user running Vesqira with that user's environment and no input. Vesqira never asks for or sees a password: a client that needs privileges is started through `pkexec` or the user's own polkit rule (written by the user as the first parts of the command), and asks in its own window.
- One run at a time per host, stopped (with everything it started, as a process group) after two minutes, so the command must return once the tunnel is up; a client that stays in the foreground is stopped at the limit, which ends its tunnel. Output is shown to the owner (stdout and stderr, at most 16 KiB, control sequences removed) and not stored.
- Afterwards the host is checked for reachability and the answer is shown; a connect that succeeds makes the job watcher ask that host again at once instead of waiting out its backoff. Exit status zero and "reachable" are two separate facts.
- The audit log records the host, the action, how it ended (exit status, did not start, timed out) and how long it took, never the command or its output.

## Supported versions

None yet. This section will list supported releases when they exist.
