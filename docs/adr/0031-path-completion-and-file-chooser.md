# ADR 0031: Path completion and the desktop file chooser

Status: **implemented** (owner direction, 2026-11-05, `docs/product/shape.md` section 3 "Every input is interactive").

## Context
A researcher should never type a path blind. Every path field has to finish what is typed the way zsh does, and offer the desktop's own file chooser, including the folders the file manager reaches on a remote host (a GVFS sftp mount, as Nautilus makes). Today a path field is a plain text box: a typo is found only when the request fails, and a folder on a host has to be recalled from memory.

Two facts shape the design. The API listens on loopback and runs as the researcher, so it can show the researcher's own desktop dialogs and list the researcher's own folders. And the app reads file contents only inside source roots (the roots check, on the resolved path); completion needs to name folders that are not source roots yet, because choosing a folder to open is how it becomes one.

## Decision
**Completion returns names only.** `GET /api/fs/complete?prefix=&kind=dir|file|any&ext=` (this computer) and `GET /api/hosts/{id}/complete` (a host, through its worker, op `complete`) answer `{base, entries:[{name, path, dir}], truncated}`. Never file contents, sizes, dates or permissions, and nothing from outside the one folder asked for: the folder of the typed prefix is read once, with no recursion and no following of links beyond one stat of an entry that is a link, to say whether it is a folder. Rules: a leading `~/` expands to the home folder of whoever lists it; the prefix must be absolute after that (else 400), without NUL bytes; the folder is cleaned lexically; the last component is a case-sensitive prefix match with a case-insensitive fallback when nothing matches; dot entries are hidden unless the typed component starts with a dot; folders first, then files, by name; at most 50 entries; reading stops after 200 ms or 2 000 directory entries and says `truncated: true`. A folder that is missing or unreadable answers an empty list, so a half-typed path never looks like an error and the answer does not tell the two cases apart.

**Trade-off, stated.** Completion may list folders outside the source roots. The alternative, listing only inside roots, makes "open this folder" impossible, because nothing outside a root is ever offered. The exposure is names of entries in folders the owner's own account can already list, returned to the owner's own browser on the loopback listener. Reading contents (`/api/preview`, registration, planes, `readtext`, `list` on a host) is unchanged and stays limited to the roots, checked on the resolved path.

**Owner routes only.** The routes are mounted on the owner listener behind the same Host, `Sec-Fetch-Site` and content-type checks as every other owner route, and are not mounted on the review listener (a test asserts a 404 there). On a host, the worker answers the same names-only shape and the API re-encodes it, so only those three fields leave.

**File chooser.** `POST /api/fs/choose {mode: "folder"|"file", title, ext:[".fits"]}` runs, on the machine the server runs on, `zenity --file-selection [--directory] --title=...` (the GTK chooser: it shows the file manager's bookmarks and GVFS mounts), falling back to `kdialog --getopenfilename` or `--getexistingdirectory`, else answers 501 "no file chooser on this desktop". The title and extensions are validated and passed as separate arguments, never through a shell. One chooser at a time (409 while one is open), five minutes at most (408), cancelling answers 204. The answer is `{path, hostId, remotePath, gvfs}`.

**GVFS to host.** A path under `/run/user/<uid>/gvfs/sftp:host=H[,user=U][,port=P]/rest` (or `ssh:`) is parsed (the mount folder's name is URL-escaped, names below it are literal). It matches a registered host when the host's ssh destination, or the HostName its alias stands for in the user's ssh config, equals `H` (case-insensitively), and the user agrees wherever both sides state one; the port is not compared, because ssh takes it from the user's own configuration. A match answers `hostId` and `remotePath` `/rest`; no match answers `gvfs: {host, user, port}` so the page can offer to add the host. The mapping never reads anything through the mount.

**Later.** xdg-desktop-portal over D-Bus replaces the two commands, and is required inside Flatpak (ADR for the Flatpak build decides how).

## Alternatives
- **A chooser written in the web page** (a tree dialog over `/api/fs/complete`): consistent look, works everywhere, but it does not show the researcher's bookmarks or mounts, which is the point. Kept as the possible fallback when no chooser exists.
- **Complete inside the source roots only**: rejected above.
- **Browser `<input type=file>` or the File System Access API**: gives a handle, not a path, and cannot reach a host.
- **Complete on the client from one big directory listing**: transfers far more names than a prefix needs and leaks more.

## Consequences
- A researcher can pick any folder; the server names entries outside the roots (names only).
- The chooser needs a desktop session and zenity or kdialog; otherwise the Browse button is hidden after the first 501.
- A GVFS mount is only a way to learn which host and folder is meant; reading still goes through the host's worker and roots.
- The port is not part of host matching: two hosts that differ only by port and share a destination would be confused. Hosts are ssh destinations, so this does not arise today.

## Validation
Go tests: names-only answers outside the roots, dot entries hidden, case rules, sort order, the 50-entry limit with `truncated`, NUL, relative and over-long prefixes rejected, `~` expansion, cross-site and foreign-Host requests refused, the review listener answering 404 for `/api/fs/*` and `/api/hosts/{id}/complete`, the GVFS parser (with and without user and port, URL-escaped user, literal names below the mount, non-GVFS paths), host matching, the chooser route with a fake chooser (cancel, no chooser, timeout, one at a time), and host completion end to end through a fake ssh. Python tests for the worker op, including that its reply carries only names. Not tried in CI: a real zenity or kdialog window.

## Rollback/Migration
Additive: no schema, no stored state. Remove the three routes and the `complete` worker op; the path fields keep working as plain text boxes.
