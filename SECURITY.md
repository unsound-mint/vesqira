# Security policy

Vesqira has no released software yet. This policy covers the repository and, later, the application.

## Reporting a vulnerability

Please use GitHub's **private vulnerability reporting** (Security tab → "Report a vulnerability") once the repository is published. Do not open a public issue for a vulnerability. A dedicated security contact address is not yet set up.

## Areas we care most about (from `AGENTS.md`)

- Reviewers reading raw data, HPC sessions, or secrets through indirect URLs.
- Path traversal or SSRF through dataset registration and remote connectors.
- Untrusted FITS/PDF/TeX input causing resource exhaustion or code execution.
- Remote-access credentials or secrets appearing in logs, provenance, or shared reviews.

## Supported versions

None yet. This section will list supported releases when they exist.
