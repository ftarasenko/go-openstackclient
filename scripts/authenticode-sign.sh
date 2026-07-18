#!/usr/bin/env sh
# Authenticode-sign a single Windows PE binary in place using osslsigncode.
#
# GoReleaser calls this from a build post-hook once per built binary (see
# .goreleaser.yaml), passing the artifact path as $1. It runs BEFORE archiving,
# so the signed .exe is what lands inside the Windows .zip.
#
# osslsigncode cannot sign in place, so we sign to a temp file and move it back.
# Non-Windows targets (linux/darwin) pass through untouched — they are not PE
# files and Authenticode does not apply to them.
#
# Required env (set in CI from repo secrets, see .github/workflows/release.yml):
#   WINDOWS_CERT_FILE      path to a PKCS#12 (.p12/.pfx) code-signing certificate
#   WINDOWS_CERT_PASSWORD  its passphrase (may be empty)
# Optional env:
#   WINDOWS_CERT_TS_URL    RFC-3161 / Authenticode timestamp URL
#                          (default: http://timestamp.digicert.com). Set to the
#                          empty string to skip timestamping entirely — only for
#                          an air-gapped signer with no reachable TSA; the
#                          resulting signature stops verifying once the signing
#                          certificate expires, so avoid it for public releases.
#
# This script is a no-op unless WINDOWS_CERT_FILE is set, so a release cut without
# the signing secret configured simply ships unsigned binaries instead of failing.
set -eu

artifact="${1:?usage: authenticode-sign.sh <path-to-binary>}"

# Only Windows PE binaries are Authenticode-signable.
case "$artifact" in
  *.exe) ;;
  *) exit 0 ;;
esac

if [ -z "${WINDOWS_CERT_FILE:-}" ]; then
  echo "authenticode-sign: WINDOWS_CERT_FILE not set; leaving $artifact unsigned" >&2
  exit 0
fi

# Only-unset falls back to the default TSA; an explicitly-empty value skips
# timestamping (note the `-` not `:-`).
ts_url="${WINDOWS_CERT_TS_URL-http://timestamp.digicert.com}"
signed="${artifact}.signed"

# Base osslsigncode arguments; append the timestamp flag only when a TSA is set.
set -- sign \
  -pkcs12 "$WINDOWS_CERT_FILE" \
  -pass "${WINDOWS_CERT_PASSWORD:-}" \
  -h sha256 \
  -n "koc — OpenStack CLI for KeyStack" \
  -i "https://github.com/ftarasenko/go-openstackclient"
if [ -n "$ts_url" ]; then
  set -- "$@" -t "$ts_url"
else
  echo "authenticode-sign: WARNING timestamping disabled; signature expires with the certificate" >&2
fi

# Timestamping reaches an external RFC-3161 server, which is occasionally slow,
# rate-limited, or transiently unreachable (and osslsigncode has been known to
# crash rather than exit cleanly on a bad TSA response). Retry a few times before
# giving up. We deliberately do NOT silently fall back to an un-timestamped
# signature: a signature without a trusted timestamp stops verifying once the
# signing certificate expires, so a persistent TSA outage should fail loudly.
attempts=3
i=1
while :; do
  rm -f "$signed"
  if osslsigncode "$@" -in "$artifact" -out "$signed"; then
    break
  fi
  if [ "$i" -ge "$attempts" ]; then
    echo "authenticode-sign: signing failed after $attempts attempts" >&2
    rm -f "$signed"
    exit 1
  fi
  echo "authenticode-sign: attempt $i failed${ts_url:+ (TSA=$ts_url)}; retrying..." >&2
  i=$((i + 1))
  sleep 5
done

mv "$signed" "$artifact"
echo "authenticode-sign: signed $artifact" >&2
