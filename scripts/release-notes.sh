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

# Previous tag = the highest tag strictly older than TAG (its semver
# predecessor), so notes regenerate correctly for ANY tag, not just the newest.
# When TAG does not exist yet (first-time cut) it is absent from the list, so awk
# prints every tag and tail picks the newest — the right predecessor.
PREV="$(git tag --sort=v:refname | awk -v t="$TAG" '$0==t{exit} {print}' | tail -n1)"
if [ -n "$PREV" ]; then REV="${PREV}..${END}"; else REV="$END"; fi

# Collect one bullet per commit into its type's bucket.
feat=(); fix=(); perf=(); refactor=(); docs=(); tests=(); tooling=(); other=(); breaking=()

# Iterated by hash rather than by subject line so the commit BODY is available:
# Conventional Commits 1.0.0 marks a breaking change with "!" after the
# type/scope *or* with a "BREAKING CHANGE:" footer, and a commit that carries
# only the footer would otherwise be filed as an ordinary fix.
while IFS= read -r sha; do
  subject="$(git log -1 --format='%s' "$sha")"
  body="$(git log -1 --format='%b' "$sha")"
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
  # A breaking change is listed once, under its own heading, keeping its scope.
  # Listing it again under "Bug fixes"/"Features" reads as a duplicated bullet
  # and buries the one line a consumer most needs to see.
  if [ -n "$bang" ] || printf '%s\n' "$body" | grep -qE '^BREAKING[ -]CHANGE:'; then
    breaking+=("$bullet")
    continue
  fi
  case "$type" in
    feat) feat+=("$bullet") ;;
    fix) fix+=("$bullet") ;;
    perf) perf+=("$bullet") ;;
    refactor) refactor+=("$bullet") ;;
    docs) docs+=("$bullet") ;;
    test) tests+=("$bullet") ;;
    build|ci|chore) tooling+=("$bullet") ;;
    *) other+=("$bullet") ;;
  esac
done < <(git log --no-merges --format='%H' "$REV")

emit() { # heading, array-name
  local heading="$1"; shift
  local arr=("$@")
  [ "${#arr[@]}" -eq 0 ] && return 0
  printf '%s\n' "$heading"
  printf '%s\n' "${arr[@]}"
  printf '\n'
}

{
  # ${arr[@]+"${arr[@]}"} expands to nothing when the array is empty, instead of
  # tripping "unbound variable" under `set -u` on bash 3.2 (macOS default).
  emit "### ⚠️ Breaking changes" ${breaking[@]+"${breaking[@]}"}
  emit "### Features" ${feat[@]+"${feat[@]}"}
  emit "### Bug fixes" ${fix[@]+"${fix[@]}"}
  emit "### Performance" ${perf[@]+"${perf[@]}"}
  emit "### Refactoring" ${refactor[@]+"${refactor[@]}"}
  emit "### Documentation" ${docs[@]+"${docs[@]}"}
  emit "### Tests" ${tests[@]+"${tests[@]}"}
  emit "### Build & tooling" ${tooling[@]+"${tooling[@]}"}
  emit "### Other" ${other[@]+"${other[@]}"}
  if [ -n "$PREV" ]; then
    printf '**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$REPO" "$PREV" "$TAG"
  fi
}
