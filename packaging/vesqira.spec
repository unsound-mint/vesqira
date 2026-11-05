# Binary package: assembled from the already built server and web app by tools/build-rpm.sh.
%global debug_package %{nil}
%global __strip /bin/true

Name:           vesqira
Version:        %{vesqira_version}
Release:        1%{?dist}
Summary:        Research workspace for astronomical data
License:        AGPL-3.0-only
URL:            https://github.com/unsound-mint/vesqira
Source0:        vesqira-%{version}.tar.gz

BuildArch:      x86_64
Requires:       python3 >= 3.11
Requires:       python3-astropy
Requires:       python3-numpy
Requires:       python3-reproject
Requires:       openssh-clients
Requires:       curl
Requires:       xdg-utils
Requires:       hicolor-icon-theme

%description
Vesqira runs on your computer and opens in your web browser. Browse H I FITS
spectral cubes and VGT outputs, compare maps, follow Slurm jobs on your own
cluster over your existing ssh setup, build boards of figures and share a
read-only review with a supervisor. Files outside the folders you choose are
never read.

%prep
%autosetup -n vesqira-%{version}

%install
install -Dm0755 vesqira %{buildroot}%{_bindir}/vesqira
install -Dm0755 vesqira-api %{buildroot}%{_libexecdir}/vesqira/vesqira-api
install -d %{buildroot}%{_datadir}/vesqira/worker %{buildroot}%{_datadir}/vesqira/web
install -m0644 worker/*.py %{buildroot}%{_datadir}/vesqira/worker/
cp -a web/. %{buildroot}%{_datadir}/vesqira/web/
install -Dm0644 vesqira.desktop %{buildroot}%{_datadir}/applications/vesqira.desktop
install -Dm0644 vesqira.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/vesqira.svg
install -Dm0644 io.github.unsound_mint.Vesqira.metainfo.xml %{buildroot}%{_metainfodir}/io.github.unsound_mint.Vesqira.metainfo.xml
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
