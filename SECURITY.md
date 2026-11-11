# Security policy

Vesqira has no released software yet. This policy covers the repository and, later, the application.

## Reporting a vulnerability

Please use GitHub's **private vulnerability reporting** (Security tab → "Report a vulnerability") once the repository is published. Do not open a public issue for a vulnerability. A dedicated security contact address is not yet set up.

## Areas we care most about (from `AGENTS.md`)

- Reviewers reading raw data, HPC sessions, or secrets through indirect URLs.
- Path traversal or SSRF through dataset registration and remote connectors.
- Untrusted FITS/PDF/TeX input causing resource exhaustion or code execution.
- Remote-access credentials or secrets appearing in logs, provenance, or shared reviews.

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

- Only on the loopback owner address, never on `--review-addr`; not available in multi-user mode.
- A session needs a one-time token from an authenticated same-origin `POST`, carried in the WebSocket subprotocol (never in the URL), valid for 60 seconds and consumed by the first attempt. The upgrade also needs an `Origin` equal to the page's, a loopback `Host` and no cross-site `Sec-Fetch-Site`; a foreign page cannot open a session even if it can reach the port.
- The page chooses a host by id from the registered hosts; it cannot name a command, a binary or an ssh destination. ssh uses your own configuration and agent; a passphrase or one-time-code prompt is typed in the terminal and never seen by Vesqira.
- One session per browser tab, at most 8 at once, ended after 30 minutes without input or 8 hours in all, or when the page goes away. Input, resize and output frames are size-bounded.
- The audit log records that a session opened and closed (where, how long, why), never what was typed or shown. The terminal itself keeps no transcript; do not paste secrets into it on a shared screen.
- It does not give a reviewer anything: the review listener has no terminal routes.

## Supported versions

None yet. This section will list supported releases when they exist.
