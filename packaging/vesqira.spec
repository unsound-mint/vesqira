# Built from source in the build root with no network: the vendored Go modules, crates and npm cache are Source1-3
# (tools/vendor-sources.sh). tools/build-srpm.sh sets the Version line; do not pass --define for it.
%global debug_package %{nil}
%global __strip /bin/true

Name:           vesqira
Version:        0.2.0
Release:        1%{?dist}
Summary:        Research workspace for astronomical data
License:        AGPL-3.0-only
URL:            https://github.com/unsound-mint/vesqira
Source0:        vesqira-%{version}.tar.gz
Source1:        vesqira-%{version}-vendor-go.tar.gz
Source2:        vesqira-%{version}-vendor-cargo.tar.gz
Source3:        vesqira-%{version}-npm-cache.tar.gz

ExclusiveArch:  x86_64
# go.mod says go 1.26
BuildRequires:  golang >= 1.26
BuildRequires:  cargo
BuildRequires:  rust
BuildRequires:  nodejs >= 20.19
BuildRequires:  npm
BuildRequires:  gcc
BuildRequires:  python3
BuildRequires:  pkgconfig(webkit2gtk-4.1)
BuildRequires:  pkgconfig(gtk+-3.0)
BuildRequires:  pkgconfig(libsoup-3.0)
Requires:       python3 >= 3.11
Requires:       python3-astropy
Requires:       python3-contourpy
Requires:       python3-numpy
Requires:       python3-reproject
Requires:       openssh-clients
Requires:       curl
Requires:       webkit2gtk4.1
Requires:       gtk3
Requires:       xdg-utils
Requires:       hicolor-icon-theme

%description
Vesqira runs on your computer and opens in its own window. Browse H I FITS
spectral cubes and VGT outputs, compare maps, follow Slurm jobs on your own
cluster over your existing ssh setup, build boards of figures and share a
read-only review with a supervisor. Files outside the folders you choose are
never read.

%prep
%autosetup -n vesqira-%{version}
tar -xzf %{SOURCE1} -C services/api
tar -xzf %{SOURCE2} -C apps/desktop
tar -xzf %{SOURCE3} -C %{_builddir}
mkdir -p apps/desktop/.cargo
cat > apps/desktop/.cargo/config.toml <<'CARGO'
[source.crates-io]
replace-with = "vendored-sources"
[source.vendored-sources]
directory = "vendor"
CARGO

%build
# No network from here on: every dependency comes from the vendor sources above.
(cd apps/web && npm ci --offline --no-audit --no-fund --cache %{_builddir}/npm-cache && npm run build)
(cd services/api && CGO_ENABLED=0 GOFLAGS=-mod=vendor GOTOOLCHAIN=local GOPROXY=off \
   go build -trimpath -buildvcs=false -ldflags "-s -w" -o %{_builddir}/vesqira-api .)
(cd apps/desktop && cargo build --release --frozen --offline)
python3 tools/check_worker_manifest.py >/dev/null

%install
install -Dm0755 packaging/vesqira %{buildroot}%{_bindir}/vesqira
install -Dm0755 %{_builddir}/vesqira-api %{buildroot}%{_libexecdir}/vesqira/vesqira-api
install -Dm0755 apps/desktop/target/release/vesqira-desktop %{buildroot}%{_libexecdir}/vesqira/vesqira-desktop
install -d %{buildroot}%{_datadir}/vesqira/worker %{buildroot}%{_datadir}/vesqira/web
# the one worker manifest (also read by check_worker_manifest.py and the host deploy)
while read -r f; do case "$f" in ""|"#"*) continue;; esac; install -m0644 "services/scientific-worker/$f" %{buildroot}%{_datadir}/vesqira/worker/; done < services/scientific-worker/DEPLOY
cp -a apps/web/dist/. %{buildroot}%{_datadir}/vesqira/web/
install -Dm0644 packaging/vesqira.desktop %{buildroot}%{_datadir}/applications/vesqira.desktop
install -Dm0644 packaging/vesqira.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/vesqira.svg
install -Dm0644 packaging/io.github.unsound_mint.Vesqira.metainfo.xml %{buildroot}%{_metainfodir}/io.github.unsound_mint.Vesqira.metainfo.xml
install -Dm0644 LICENSE %{buildroot}%{_licensedir}/%{name}/LICENSE
install -Dm0644 THIRD_PARTY_NOTICES.md %{buildroot}%{_licensedir}/%{name}/THIRD_PARTY_NOTICES.md

%files
%license %{_licensedir}/%{name}/LICENSE
%license %{_licensedir}/%{name}/THIRD_PARTY_NOTICES.md
%{_bindir}/vesqira
%{_libexecdir}/vesqira
%{_datadir}/vesqira
%{_datadir}/applications/vesqira.desktop
%{_datadir}/icons/hicolor/scalable/apps/vesqira.svg
%{_metainfodir}/io.github.unsound_mint.Vesqira.metainfo.xml

%changelog
* Wed Nov 04 2026 Denis Andreyev <unsoundmint@gmail.com> - %{version}-1
- Packaged release
