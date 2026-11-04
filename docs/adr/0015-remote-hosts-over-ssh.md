# ADR 0015: Remote hosts over ssh

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
ADR 0013 makes the researcher's other machines (an HPC login node, another laptop) sources of cubes. The cubes are large and stay where they are; the user already has ssh working (and a VPN such as gpclient where needed).

## Decision
- **System ssh, user's own setup.** A host is a name, an ssh destination (a host name or an alias from the user's ssh config), a Python command and one to eight source folders. Vesqira runs `ssh -T -o BatchMode=yes` with connection multiplexing (`ControlMaster=auto`, sockets in a private temp directory) and passes only the agent variables (`SSH_AUTH_SOCK`, `SSH_AGENT_PID`) to it. It never reads keys, never asks for or stores passwords, and cannot answer an ssh prompt (an unknown host key or a 2FA prompt makes the connection fail with a plain message; the user connects once by hand).
- **Nothing that could become an ssh option.** The destination must start with a letter or digit and contain only letters, digits and `. _ @ -`; the Python command only `A-Za-z0-9_./~-`; roots are clean absolute paths, never `/`. The remote command is a fixed template wrapped in `sh -c '...'` (so a non-POSIX login shell does not matter) and the only variable part is the roots list, passed as single-quoted JSON in an environment variable.
- **The worker runs on the host.** `plane_worker.py` and `fits_header.py` are copied to `$HOME/.cache/vesqira/<content hash>/` the first time a host is used or checked (a tar stream over the same ssh; a new worker version means a new hash and directory). The line protocol of ADR 0008 then runs over the ssh channel, so only planes, coordinates, spectra and statistics travel and cubes never leave the host. One worker per host serves one request at a time, which also bounds the load on a shared login node; heavy computation belongs to Slurm jobs (ADR 0013).
- **The host's worker enforces the roots.** Started with `VESQIRA_ROOTS`, the worker refuses every path whose resolved real path is outside the roots or is not a regular file (or a directory for listing), with one identical refusal; listings omit links that leave the roots. The API also checks the roots lexically before any round trip. This is the control that matters, because the API cannot resolve symlinks on another machine.
- **Datasets know their host.** The catalog keys a file by (host, path): the same path on two machines is two datasets. Every request re-checks the file's size and nanosecond modification time on the host (`stat` through the worker) and answers 409 if it changed, exactly like local files. An unreachable host is a 502 with a plain message that never includes the destination; local files keep working.
- **Both maps of an orientation declaration must be on the same host**; compatibility checks and side-by-side comparison work across hosts because they only need the catalogued headers.
- **Reviewer exposure.** Figures of remote cubes carry the file name only: the reviewer test checks that host names, ssh destinations, remote roots and paths do not appear.

## Alternatives
An SSH library in the API (key handling and host-key policy become ours); a resident daemon on each host (more to install and trust); sshfs (stalls and silent partial reads); copying cubes to this machine (defeats the purpose for multi-GB cubes).

## Consequences
Everything remote depends on the user's ssh setup and on Python with astropy on the host. A host without a prompt-free ssh cannot be used. Browsing is limited to 500 entries per folder.

## Validation
Worker tests start the worker with roots and check ping, header, stat, resolve and listing inside the roots; identical refusals for outside paths, symlinks out, `..` and missing files; relative and odd paths; planes inside the roots. Go tests: host validation (option injection, spaces, relative or root-wide roots, odd Python commands), lexical root checks (including `/data` versus `/data2`), shell quoting, and an end-to-end run with a stand-in ssh and the real worker: deploy by hash, listing without the escaping link, refused registrations (outside, symlink, `..`, `/etc/passwd`), a remote cube giving the same plane and coordinates as the same file locally, cross-host compatibility, a reviewer response free of host and path names, 409 after the file changes, 502 when the host is down while local files keep working. The browser walk-through adds a host, checks it, browses, registers, reads the same pixel value as locally and compares the two.

## Rollback / Migration
Drop the `hosts` table and the `host_id` column's use; remove the routes. Local datasets are unaffected.
