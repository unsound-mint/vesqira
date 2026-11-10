# ADR 0031: Native window (Tauri shell)

Status: accepted (owner chose Tauri, 2026-11-10). Refines ADR 0021 and the desktop-shell decision in `docs/product/shape.md` section 3 (Q147, Q148).

## Context
The installed app started the server and opened a browser tab. The owner wants an application: its own window, taskbar entry and, next, a native Open Folder dialog that returns real paths (a browser cannot).

## Decision
- `apps/desktop` is a small Tauri 2 program (about 6 MB) that uses the system WebKitGTK. It starts the local server through the `vesqira` launcher (`vesqira start`), shows `http://127.0.0.1:$VESQIRA_PORT/` in one window, and stops the server on exit only if it started it.
- The web app and API are unchanged and still work in a browser (`VESQIRA_BROWSER=1 vesqira`, or when the window program or a display is missing). Native-only code stays in this crate; the web app must keep working without it.
- The RPM installs `/usr/libexec/vesqira/vesqira-desktop` and requires `webkit2gtk4.1` and `gtk3`. `vesqira` with no argument execs it.
- Native folder chooser: the `pick_folder` command returns a path and nothing else, and only the loopback origin may call it. The path then goes to `POST /api/projects/open`, which is what grants read access: it makes the folder a source root (remembered in the launcher's roots file) and refuses `/`, the home directory and its parents, and credential folders (`.ssh`, `.gnupg`). `GET /api/files/tree` lists names inside roots only, never follows symlinks out, and hides credential folders. Both routes are owner-only and not on the review listener. Opening a folder cannot be undone from the app yet (edit the roots file).
- Not yet: single-instance handling, project discovery (`docs/product/shape.md` section 3), and the editor/terminal of ADR 0029.

## Alternatives
Chrome `--app` window: no native folder picker, depends on a browser being installed. Electron: ~100 MB, a Node runtime in the product.

## Consequences
Build needs `cargo` and the webkit2gtk4.1, gtk3 and libsoup3 development packages. WebKitGTK renders the plane canvas; its performance on large cubes is **not yet measured**.

## Validation
Built and started against a prefix laid out like the RPM: the window process ran and the server it started answered. Open-folder API and tree: Go tests (open, remember, refusals, link escape) and a browser run against the built server. Not verified: the native chooser dialog itself, how the window looks, closing the window stopping the server, an RPM build and install, the benchmark above.

## Rollback
`VESQIRA_BROWSER=1` restores the browser behaviour; remove the RPM file and requires to drop the shell.
