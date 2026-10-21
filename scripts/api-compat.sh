#!/usr/bin/env bash
# api-compat.sh — compare the public Go API (pkg/...) of HEAD with a base ref,
# by default the latest v* tag, and fail on an incompatible change that the
# commit range does not declare as breaking.
#
#   scripts/api-compat.sh [<base-ref>]
#
# Needs apidiff (golang.org/x/exp/cmd/apidiff) on PATH; supply-chain.yml
# installs a pinned one. apidiff's module mode skips internal/ packages, and
# pkg/ is the only other tree with exported identifiers, so its report is the
# pkg/ API.
#
# A breaking change is declared the way Conventional Commits says, and the way
# scripts/release-notes.sh reads it: "!" after the type/scope of a subject, or
# a "BREAKING CHANGE:" footer. While no base carries pkg/ (the API's first
# release), the job only reports.
set -euo pipefail

module=$(go list -m)
base=${1:-$(git describe --tags --abbrev=0 --match 'v*' HEAD)}

work=$(mktemp -d)
cleanup() {
	git worktree remove --force "$work/base" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

# apidiff prints a line per skipped internal package; only the verdict matters.
quiet() { grep -v '^Ignoring internal package' || true; }

git worktree add --detach --quiet "$work/base" "$base"
(cd "$work/base" && apidiff -m -w "$work/base.export" "$module" 2>&1 | quiet)
apidiff -m -w "$work/head.export" "$module" 2>&1 | quiet

echo "Public API changes, $base → HEAD:"
apidiff -m "$work/base.export" "$work/head.export" 2>&1 | quiet
incompatible=$(apidiff -m -incompatible "$work/base.export" "$work/head.export" 2>&1 | quiet)

if [[ -z "$incompatible" ]]; then
	echo "No incompatible change."
	exit 0
fi
if [[ ! -d "$work/base/pkg" ]]; then
	echo "::notice::$base has no pkg/ yet; reporting only until the first release that carries the public API."
	exit 0
fi
if git log --format=%s "$base..HEAD" | grep -qE '^[a-z]+(\([^)]*\))?!: ' ||
	git log --format=%B "$base..HEAD" | grep -qE '^BREAKING[ -]CHANGE: '; then
	echo "::notice::Incompatible pkg/ changes, declared as breaking in $base..HEAD."
	exit 0
fi
echo "::error::Incompatible pkg/ changes since $base, and no commit in the range is marked breaking (a '!' after the type, or a 'BREAKING CHANGE:' footer). Follow the deprecation policy in pkg/openstack/doc.go instead, or declare the break."
echo "$incompatible"
exit 1
