#!/usr/bin/env bash
# Repository settings that GitHub allows only on a public repository. Run once, right after making it public.
set -euo pipefail
repo=${1:-unsound-mint/vesqira}
# Workflows from forks wait for a maintainer's approval.
gh api -X PUT "repos/$repo/actions/permissions/fork-pr-contributor-approval" -f approval_policy=all_external_contributors >/dev/null
# SECURITY.md points reporters to GitHub's private vulnerability reporting.
gh api -X PUT "repos/$repo/private-vulnerability-reporting" >/dev/null
# main: changes arrive by pull request with green checks; no force-push or deletion.
gh api -X PUT "repos/$repo/branches/main/protection" --input - >/dev/null <<'JSON'
{"required_status_checks":{"strict":false,"checks":[{"context":"fixtures"},{"context":"api"},{"context":"web"}]},
 "enforce_admins":false,"required_pull_request_reviews":null,"restrictions":null,
 "allow_force_pushes":false,"allow_deletions":false,"required_linear_history":true}
JSON
echo "Settings applied to $repo."
