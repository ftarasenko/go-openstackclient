#!/usr/bin/env bash
set -euo pipefail

# Build GitHub Release notes from the Conventional Commits (1.0.0) between the
# previous tag and HEAD. Commits are grouped by type; the subject line of each
# becomes one bullet. Emitted on stdout as Markdown.
#
# Usage: scripts/release-notes.sh <new-tag>
# Requires full git history (fetch-depth: 0 in CI).
TAG="${1:?tag required}"
REPO="${GITHUB_REPOSITORY:-ftarasenko/go-openstackclient}"

# End of range: the tag itself if it already exists (re-run / tag-push build),
# otherwise HEAD (first-time cut, before action-gh-release creates the tag).
if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then END="$TAG"; else END="HEAD"; fi

# Previous tag = highest semver tag that is not the tag we are cutting.
PREV="$(git tag --sort=-v:refname | grep -vFx "$TAG" | head -n1 || true)"
if [ -n "$PREV" ]; then REV="${PREV}..${END}"; else REV="$END"; fi

# Collect "<type>|<scope>|<breaking>|<subject>" per commit.
feat=(); fix=(); perf=(); refactor=(); docs=(); tooling=(); other=(); breaking=()

while IFS= read -r subject; do
  [ -z "$subject" ] && continue
  # Match: type(scope)!: description
  if [[ "$subject" =~ ^([a-zA-Z]+)(\(([^\)]*)\))?(!)?:[[:space:]]*(.*)$ ]]; then
    type="${BASH_REMATCH[1],,}"
    scope="${BASH_REMATCH[3]}"
    bang="${BASH_REMATCH[4]}"
    desc="${BASH_REMATCH[5]}"
  else
    type="_"; scope=""; bang=""; desc="$subject"
  fi
  if [ -n "$scope" ]; then bullet="- **${scope}:** ${desc}"; else bullet="- ${desc}"; fi
  [ -n "$bang" ] && breaking+=("- ${desc}")
  case "$type" in
    feat) feat+=("$bullet") ;;
    fix) fix+=("$bullet") ;;
    perf) perf+=("$bullet") ;;
    refactor) refactor+=("$bullet") ;;
    docs) docs+=("$bullet") ;;
    build|ci|chore) tooling+=("$bullet") ;;
    *) other+=("$bullet") ;;
  esac
done < <(git log --no-merges --format='%s' "$REV")

emit() { # heading, array-name
  local heading="$1"; shift
  local arr=("$@")
  [ "${#arr[@]}" -eq 0 ] && return 0
  printf '%s\n' "$heading"
  printf '%s\n' "${arr[@]}"
  printf '\n'
}

{
  emit "### ⚠️ Breaking changes" "${breaking[@]}"
  emit "### Features" "${feat[@]}"
  emit "### Bug fixes" "${fix[@]}"
  emit "### Performance" "${perf[@]}"
  emit "### Refactoring" "${refactor[@]}"
  emit "### Documentation" "${docs[@]}"
  emit "### Build & tooling" "${tooling[@]}"
  emit "### Other" "${other[@]}"
  if [ -n "$PREV" ]; then
    printf '**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$REPO" "$PREV" "$TAG"
  fi
}
