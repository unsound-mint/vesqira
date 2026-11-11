# ADR 0021: Installable app (RPM)

Status: accepted under delegated merge; owner review pending.

## Context
Starting Vesqira needed Go, Node and a shell script. The owner wants to launch it like any other application and install it with `dnf` or Flatpak.

## Decision
- The server serves the built web app on its own loopback address (`--web-dist`), so one address is the whole application. `/api` is never answered from disk; unknown paths fall back to `index.html`.
- An RPM for Fedora (`tools/build-rpm.sh`, `packaging/vesqira.spec`) installs: `/usr/bin/vesqira` (launcher), `/usr/libexec/vesqira/vesqira-api`, the worker scripts and web files under `/usr/share/vesqira`, a desktop entry, an icon and AppStream metadata. The Python libraries come from Fedora's own packages (`python3-astropy`, `python3-numpy`, `python3-reproject`); nothing is downloaded at install or run time.
- The launcher starts the server if it is not running and opens the browser. Readable folders are listed one per line in `~/.config/vesqira/roots` (`vesqira add-root DIR`); the first run creates `~/Vesqira`. Data lives in `~/.local/share/vesqira`, the log in `~/.local/state/vesqira`. The server keeps running after the browser tab closes until `vesqira stop`.

## Alternatives
Flatpak: **deferred**. It needs the Python scientific stack bundled as wheels (flatpak-builder is not available in the current environment, so it could not be built or tested), and sandboxing must be opened for the host `ssh` client, agent socket and VPN-reachable hosts, which weakens the point of the sandbox. It remains a candidate once it can be built and tested. The Flatpak decision is ADR 0052 (proposed).

**Update 2026-11-11: source RPM and COPR.** `packaging/vesqira.spec` now builds from source in the build root with no network: `tools/vendor-sources.sh` produces the git archive plus vendored Go modules, crates and an npm cache (Source0-3), `make srpm` (`tools/build-srpm.sh`) makes the SRPM, and `tools/build-rpm.sh` rebuilds it into the installable RPM (the release workflow runs that). COPR builds from tags through `.copr/Makefile`; the owner's one-time setup is in `docs/setup/copr.md`. Not Fedora-policy complete (the `License:` tag does not list the bundled dependencies' licenses).

## Consequences
Implemented for Fedora, x86_64. The RPM was installed in a clean Fedora 44 container; the worker test suite (60 tests) passed with the distribution's packages (astropy 7.2.0, reproject 0.18.0, Python 3.14), and the extracted package served the UI and read a plane from a generated cube. Not tested: a real desktop session click on the menu entry, and upgrade from one RPM to the next.

## Validation
Server test for static serving; container install and worker tests as above; launcher run against the extracted package (start, add-root with restart, register, read a plane, stop).

## Rollback/Migration
`sudo dnf remove vesqira`. User data under `~/.local/share/vesqira` and `~/.config/vesqira` is left in place.
