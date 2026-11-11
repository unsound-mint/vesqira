# COPR "make_srpm" method (docs/setup/copr.md): COPR clones the tag and runs `make -f .copr/Makefile srpm outdir=... spec=...`
# in a mock chroot. Vendoring needs the network, which the SRPM step has; the RPM build step does not.
outdir ?= $(CURDIR)/dist/srpm

srpm:
	dnf -y install git golang cargo nodejs npm rpm-build tar gzip
	tools/build-srpm.sh "" "$(outdir)"
