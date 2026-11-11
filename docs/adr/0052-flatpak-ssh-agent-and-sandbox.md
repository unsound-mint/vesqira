# ADR 0052: Flatpak: ssh, the ssh agent, file choosing and the Python stack

- Status: proposed; awaiting owner review. Nothing is built: `flatpak-builder` is not installed where this was written, and no manifest exists.
- Date: 2026-11-11
- Settles the open item in ADR 0021 ("Flatpak: deferred") and the "xdg-desktop-portal, required inside Flatpak" note in ADR 0031. Everything below is **specified**.

## Context

The RPM (ADR 0021) runs as the researcher with full access to the account. What the app needs from the machine, and where a sandbox bites:

- **ssh to the researcher's own hosts** (ADR 0015, 0016). Vesqira runs the system `ssh -T -o BatchMode=yes` with `ControlMaster=auto`, passes `SSH_AUTH_SOCK` and `SSH_AGENT_PID`, and never reads keys. The destination is usually an alias from `~/.ssh/config` (`ProxyJump`, `Include`, `IdentityFile`, `HostName`), and host keys come from `~/.ssh/known_hosts`. Slurm is reached only through that ssh; no Slurm binary runs locally. The VPN the cluster needs (for example `gpclient`) is brought up by the user outside the app (ADR 0029's connect button is not built).
- **Choosing folders** (ADR 0031, 0037). Today `zenity`/`kdialog`, and the Tauri shell's native dialog, return real host paths; the folder then becomes a source root. In a sandbox only the document portal can grant a folder, and the path inside the sandbox is `/run/user/<uid>/doc/<id>/<name>`, not the host path.
- **The scientific worker** (`services/scientific-worker`, manifest `DEPLOY`) needs Python with astropy, numpy, reproject and contourpy. The RPM uses Fedora's packages (astropy 7.2.0, numpy 2.4.6, reproject 0.18.0, contourpy 1.4.0); `requirements.txt` pins newer ones (astropy 8.0.2, numpy 2.5.4, reproject 0.21.0).
- **The window** is the Tauri shell on WebKitGTK 4.1 (ADR 0037).

A Flatpak sandbox only helps if it keeps something out of reach. For this app, the thing worth keeping out of reach is everything outside the folders the researcher opens. It cannot keep ssh out of reach, because ssh to the researcher's hosts is the product.

## Decision

**Recommended: option A, ssh inside the sandbox through the forwarded agent and read-only config files, folders through the file chooser portal.** Concretely, when the Flatpak is built:

- `--share=network` (the host's network namespace, so the user's VPN works), `--socket=wayland --socket=fallback-x11 --device=dri`.
- `--socket=ssh-auth` for the agent. No `--filesystem=~/.ssh`: only `--filesystem=~/.ssh/config:ro` and `--filesystem=~/.ssh/known_hosts:ro`, so private key files are not visible. Hosts that need a key file instead of an agent do not work from the Flatpak; the host form says so and points to the RPM. `Include`d config files outside those two paths are not found; documented the same way. (Verify that ssh accepts a read-only `known_hosts` with `BatchMode`: an unknown host key already fails by design in ADR 0015.)
- The ssh client from the runtime (check that `org.freedesktop.Platform` ships `ssh`; if it does not, build OpenSSH as a manifest module).
- No `--talk-name=org.freedesktop.Flatpak`, no `--filesystem=home` or `host`.
- Folders only through `org.freedesktop.portal.FileChooser`. `POST /api/fs/choose` gets a portal implementation (ADR 0031's "later"). The document-portal path is what the roots check and `POST /api/projects/open` see inside the sandbox; the **displayed and recorded path must be the host path**, obtained from the Documents portal (`GetHostPaths`, to verify available in the target portal version). Recording a `/run/user/.../doc/...` path as provenance would be an invented location (AGENTS.md invariant 7). Until that mapping exists the Flatpak is not releasable.
- The Python stack inside the app: a private site directory in `/app`, built from the same pinned `requirements.txt` (hashes), through `flatpak-pip-generator`. The worker is not run on the host.
- The desktop shell: a GNOME runtime that provides WebKitGTK 4.1 (to verify for the chosen runtime version, with GTK 3), with the Rust crate built offline from the same vendored crates as the RPM (`tools/vendor-sources.sh`).

### Options

| | ssh | folders | security consequence | verdict |
|---|---|---|---|---|
| **A** `--socket=ssh-auth` + read-only `~/.ssh/config`, `known_hosts`; portal chooser | ssh in the sandbox | portal | Every process in the sandbox can use every key loaded in the agent against any host the user reaches, and the app has network access to the whole LAN/VPN. Keys on disk stay invisible. | **recommended** |
| **A2** A plus `--filesystem=~/.ssh:ro` | same | same | adds the private key files; passphrase-less keys leak to anything in the sandbox | rejected: the app never needs them; the agent is enough |
| **B** `flatpak-spawn --host ssh` (`--talk-name=org.freedesktop.Flatpak`) | host ssh, exact host config and keys, works with every ssh setup | any | this permission lets the app run any command on the host with the user's rights: it removes the sandbox entirely, and a bug in the app or the web view becomes host code execution | rejected: a sandbox with this hole is a package format, not a sandbox, and AGENTS.md's trust boundaries assume the app runs a fixed set of commands |
| **C** Host Python through `flatpak-spawn --host` | n/a | n/a | same hole as B | rejected |
| **D** Do not ship ssh features in the Flatpak | none | portal | smallest surface; but remote hosts and Slurm are most of the product | rejected |
| Python stack, **E1** runtime Python plus pip modules in `/app` | | | pinned and reproducible; larger download; the worker then runs on newer pins than the RPM (7.2.0 vs 8.0.2) | recommended |
| Python stack, **E2** wheels only | | | Flathub policy and aarch64 favour source builds; slow compile | build sdists where Flathub requires, wheels otherwise |

### What the Flatpak is not

It does not isolate the researcher from their own hosts. A reviewer's access is unchanged: the review listener serves published snapshots only (ADR 0011), and nothing here changes it. The Flatpak is for convenience and for confining file access to chosen folders, and the docs must not claim more.

## Alternatives

Waiting: the RPM plus COPR (this slice) covers Fedora; a Flatpak adds Silverblue/Kinoite users and other distributions. The cost is a second packaging path, a portal implementation and the Python-version drift. If those users are not asking, the recommended action is to keep the item deferred.

## Consequences

- If accepted: the file chooser portal implementation (an ADR 0031 follow-up) comes first and has value outside Flatpak; the manifest, built and tested with `flatpak-builder`, comes after. An optional manifest skeleton is not committed in this slice, so nothing unbuilt is in the tree.
- The sandbox gains ssh access to all hosts reachable with the loaded agent keys. A user who loads keys with `ssh-add -c` (confirm on use) keeps a prompt per signature.
- Two Python stacks (Fedora's for the RPM, pinned for the Flatpak) means the worker tests must run against both; CI today runs them against pinned (`make check`) and Fedora's (RPM install test) already.
- Hosts that depend on key files, `Include`s elsewhere or a smart card are not supported in the Flatpak.

## Validation

When built: connect to a host through an agent-only alias and through a `ProxyJump`; confirm key files are not readable inside (`flatpak run --command=sh`); check a folder opened through the portal registers a cube, shows the host path and survives a restart; run the worker tests inside the sandbox; confirm the window renders the plane canvas (WebKitGTK performance is not yet measured, ADR 0037).

## Rollback/Migration

Not applicable until built; removing the Flatpak (`flatpak uninstall`) leaves `~/.var/app/<id>/` data behind, and the data directory differs from the RPM's `~/.local/share/vesqira`: `vesqira export` / `restore` (ADR 0022) is the way between them.
