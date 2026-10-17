#!/usr/bin/env bash
#
# sbom-bundle.sh — produce ONE SPDX SBOM bundle for the whole release.
#
# Called by GoReleaser's `sboms:` pipe (see .goreleaser.yaml). It runs syft once
# per shipped artifact — 6 archives + 4 nfpm packages — and tars the resulting
# per-artifact SPDX documents into a single `koc_<version>_sboms.tar.gz`.
#
# WHY A BUNDLE
#   Emitting `${artifact}.spdx.json` per artifact put 10 loose files on the
#   release page (23 assets for 10 real downloads), which buried the binaries.
#   Bundling loses no fidelity: the tarball still holds one SPDX document per
#   artifact, named `<artifact>.spdx.json`, so a consumer's component review is
#   unchanged after one `tar xzf`.
#
# WHY IT MUST STAY INSIDE GORELEASER
#   GoReleaser's pipeline is build -> archive -> package -> sbom -> checksum ->
#   sign -> publish. Anything the sbom pipe registers as a document becomes a
#   tracked artifact, so it lands in `checksums.txt`, which cosign then signs.
#   That is the release's whole integrity chain. A bundle built *after*
#   GoReleaser (a workflow step, a `release.extra_files` glob) would sit outside
#   `checksums.txt` and therefore outside the signature — tidier page, weaker
#   supply chain. Do not move this out of the `sboms:` pipe.
#
# WHY THE NORMALISATION STEP EXISTS
#   AGENTS.md requires byte-reproducible releases: two builds of the same tag
#   must hash identically so an air-gapped consumer can re-derive `checksums.txt`
#   instead of trusting it. syft is not reproducible on its own — every run
#   stamps a wall-clock `creationInfo.created` and a fresh random UUID into
#   `documentNamespace`, and it ignores SOURCE_DATE_EPOCH (verified against syft
#   v1.x). Left alone, the bundle's sha256 changes on every run, so the SBOM line
#   in `checksums.txt` would be unverifiable by rebuild.
#   So both fields are pinned:
#     * `created`           -> the commit date, the same instant already pinned
#                             into builds.mod_timestamp / archives.builds_info
#                             .mtime / nfpms.mtime.
#     * documentNamespace   -> UUID-shaped digest of the artifact's own sha256,
#                             which keeps the SPDX uniqueness requirement (a
#                             different artifact, or the same artifact rebuilt
#                             differently, gets a different namespace) while
#                             being stable for identical bytes.
#   The tar itself is pinned the same way (--sort, fixed mtime, uid/gid 0,
#   `gzip -n` so no name/mtime lands in the gzip header).
#
# CWD: GoReleaser runs `sboms[].cmd` with the working directory set to `dist/`,
# so the artifacts are alongside us and `$1` is a plain filename. That is also
# why .goreleaser.yaml invokes this as `../scripts/sbom-bundle.sh`.
#
# Usage: sbom-bundle.sh <bundle-name.tar.gz> <commit-unix-timestamp> <commit-date-rfc3339>

set -euo pipefail
shopt -s nullglob

bundle=${1:?bundle file name required (GoReleaser passes $document)}
commit_epoch=${2:?commit unix timestamp required}
commit_date=${3:?commit date in RFC3339 required}

command -v syft >/dev/null 2>&1 || {
	echo "sbom-bundle: syft not found on PATH" >&2
	exit 1
}

# Collect the shipped artifacts sitting in dist/. GoReleaser has not written
# artifacts.json yet at this point in the pipeline (it is emitted after the
# checksum stage), so the artifact set is discovered by extension. Exclude our
# own output, which also matches *.tar.gz.
artifacts=()
for f in *.tar.gz *.zip *.rpm *.deb; do
	[[ -f $f ]] || continue
	[[ $f == "$bundle" ]] && continue
	artifacts+=("$f")
done

if ((${#artifacts[@]} == 0)); then
	echo "sbom-bundle: no archives or packages found in $(pwd)" >&2
	exit 1
fi

# Sorted so the tar member order — and therefore the bundle's hash — does not
# depend on the shell's glob expansion order.
mapfile -t artifacts < <(printf '%s\n' "${artifacts[@]}" | LC_ALL=C sort)

documents=()
for artifact in "${artifacts[@]}"; do
	doc="${artifact}.spdx.json"
	echo "sbom-bundle: cataloging ${artifact}" >&2
	syft "$artifact" --output "spdx-json=${doc}" --quiet

	# Reproducibility fix-ups; see "WHY THE NORMALISATION STEP EXISTS" above.
	# The namespace UUID is folded out of the artifact's own sha256 — the same
	# thing a name-based (v5) UUID is, so the version/variant nibbles are forced
	# to keep it well-formed per RFC 4122 and not trip an SPDX validator.
	sha=$(sha256sum "$artifact" | cut -d' ' -f1)
	variant=$(printf '%x' $(((0x${sha:16:1} & 0x3) | 0x8)))
	ns="${sha:0:8}-${sha:8:4}-5${sha:13:3}-${variant}${sha:17:3}-${sha:20:12}"

	# Done by parsing the JSON rather than with sed, because a regex here fails
	# SILENTLY in two ways that both cost reproducibility without erroring: on
	# pretty-printed output an unanchored substitution rewrites the first
	# UUID-shaped value on *every* line, corrupting unrelated fields; on compact
	# single-line output it rewrites only the first UUID in the whole document,
	# which may not be documentNamespace at all — leaving the random UUID in
	# place. Addressing the field by name cannot miss, the re-dump is canonical
	# (sorted keys, no incidental whitespace) so syft's own key ordering cannot
	# affect the hash either, and a missing field is a hard error.
	python3 - "$doc" "$ns" "$commit_date" <<-'PY'
		import json, re, sys

		path, ns, created = sys.argv[1], sys.argv[2], sys.argv[3]
		uuid_re = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"

		with open(path) as fh:
		    doc = json.load(fh)

		if "documentNamespace" not in doc:
		    sys.exit(f"{path}: no documentNamespace to pin")
		pinned, n = re.subn(uuid_re, ns, doc["documentNamespace"])
		if n != 1:
		    sys.exit(f"{path}: expected 1 UUID in documentNamespace, found {n}")
		doc["documentNamespace"] = pinned

		if "creationInfo" not in doc or "created" not in doc["creationInfo"]:
		    sys.exit(f"{path}: no creationInfo.created to pin")
		doc["creationInfo"]["created"] = created

		with open(path, "w") as fh:
		    json.dump(doc, fh, sort_keys=True, separators=(",", ":"))
	PY

	documents+=("$doc")
done

# One deterministic tarball. Every knob that would otherwise record "now" or the
# builder's identity is pinned.
tar --create \
	--sort=name \
	--format=gnu \
	--owner=0 --group=0 --numeric-owner \
	--mtime="@${commit_epoch}" \
	-- "${documents[@]}" |
	gzip -n -9 >"$bundle"

# Drop the loose documents: only "$bundle" is meant to reach the release page,
# and a clean dist/ keeps a later `--clean`-less run from re-tarring stale files.
rm -f -- "${documents[@]}"

echo "sbom-bundle: wrote ${bundle} (${#documents[@]} SPDX documents)" >&2
