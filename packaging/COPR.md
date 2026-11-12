# Publishing Vesqira on COPR

Status: **specified; nothing has been created on COPR.** The source RPM builds locally (`make srpm`); a COPR build of it has not been run. Creating the project needs the owner's Fedora Account System (FAS) login, so the steps below are for the owner to do once.

## What COPR builds

COPR takes a git tag, runs `make -f .copr/Makefile srpm` in a mock chroot, and builds the resulting source RPM for each chosen Fedora release. `.copr/Makefile` installs the vendoring tools (`golang cargo nodejs npm`) and calls `tools/build-srpm.sh`, which calls `tools/vendor-sources.sh`. Vendoring needs the network and runs in the SRPM step; the binary build (`%build` in `packaging/vesqira.spec`) has none. The SRPM carries four sources: the git archive, the vendored Go modules, the vendored crates and an npm cache, each the exact versions in `go.sum`, `Cargo.lock` and `package-lock.json`. It is about 180 MB.

## One-time setup (owner)

1. Log in at <https://copr.fedorainfracloud.org> with the FAS account.
2. **New Project**: name `vesqira`; chroots `fedora-44-x86_64` (add newer releases and `fedora-rawhide-x86_64` once they build); description "Research workspace for astronomical data"; project homepage `https://github.com/unsound-mint/vesqira`. Leave "Enable internet access during builds" **off**: the build step needs none, and an offline build is the check that the vendor tarballs are complete.
3. Project **Settings**, "Build options": leave "Use bootstrap" at the default. If the SRPM step is refused for size (about 180 MB), ask Fedora Infra or trim the vendor tarballs (the crates include Windows-only targets).
4. **Packages -> Add package -> SCM**:
   - Clone URL `https://github.com/unsound-mint/vesqira.git`
   - Committish: leave empty for `main`, or a tag such as `v0.2.0`
   - Subdirectory: empty
   - Spec file: `packaging/vesqira.spec`
   - Type: **Make srpm** (the `.copr/Makefile` is found automatically)
   - Auto-rebuild: on.
5. Build from tags automatically: in the package's **Integrations** tab copy the webhook URL and add it in GitHub under the repository's Settings -> Webhooks (content type `application/json`, event "Create" for tags, or "Push"). Keep "Auto-rebuild" tied to the committish pattern `v*` so only tags build.
6. Start the first build from the package page and read the SRPM-step log first: it is where a missing build tool or a vendoring failure shows up.
7. Replace `<owner>` below and in the README with the FAS login.

## Install line

```
sudo dnf copr enable <owner>/vesqira && sudo dnf install vesqira
```

## Cutting a release

Tag `vX.Y.Z` on `main`. The SRPM takes its version from the tag (leading `v` dropped; `tools/build-srpm.sh`). The release workflow (`.github/workflows/rpm.yml`) builds the same SRPM and RPM, runs the install smoke test and attaches both to the GitHub release. Update `%changelog` in `packaging/vesqira.spec` when the packaging changes.

## Known gaps

- **Not Fedora-policy complete.** `License:` names only AGPL-3.0-only, but the build bundles Go modules, crates and npm packages with other licenses (`THIRD_PARTY_NOTICES.md` lists the ones the web app ships). Getting into Fedora proper needs a combined license expression and `Provides: bundled(...)`. COPR does not require this.
- **Unverified here:** the SRPM step on COPR's builders (it assumes `dnf install` works there and that the clone has tags; without tags the spec's own Version is used), and a `mock --rebuild` in a clean Fedora 44 chroot (mock was not installed where this was written; the offline rebuild was run with `rpmbuild --rebuild` under `unshare -rn` instead).
- **Python stack is Fedora's.** The RPM requires `python3-astropy`, `python3-astropy-healpix`, `python3-numpy`, `python3-reproject` and `python3-contourpy`; these are older than `services/scientific-worker/requirements.txt` (the pins used by `make setup`). The CI install test runs the worker tests against the distribution's packages.
