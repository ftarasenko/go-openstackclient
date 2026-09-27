#!/usr/bin/env bash
#
# Prove a devstack brought up by scripts/devstack/up.sh actually works, feature
# by feature, before any koc test runs against it. stack.sh exiting 0 is not
# that proof: an agent can be dead, an extension missing, or bind9 unable to
# serve a zone while every API still answers.
#
#   scripts/devstack/smoke.sh [--log-dir DIR] [--koc PATH] [--devstack-dir DIR]
#
# Reads KOC_FT_FEATURES and the S3 credentials from <log-dir>/functional.env.
# Writes <log-dir>/smoke.txt (one PASS/FAIL/WARN line per check, with seconds)
# and <log-dir>/extensions.txt (every neutron extension alias). Exits non-zero
# if any check FAILs; a WARN is an expected per-release absence (tap-mirror
# is not in tap-as-a-service before 2025.1) and is recorded, not failed.

# The probe functions below only ever run as arguments to check/wait_for, which
# the linter cannot follow.
# shellcheck disable=SC2329

set -uo pipefail

LOG_DIR=${LOG_DIR:-$PWD/devstack-logs}
DEVSTACK_DIR=${DEVSTACK_DIR:-$PWD/devstack}
KOC=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --log-dir) LOG_DIR=$2; shift 2 ;;
        --devstack-dir) DEVSTACK_DIR=$2; shift 2 ;;
        --koc) KOC=$2; shift 2 ;;
        *) echo "smoke.sh: unknown argument: $1" >&2; exit 2 ;;
    esac
done

set -a
# shellcheck disable=SC1091
. "$LOG_DIR/functional.env"
set +a
set +u
# shellcheck disable=SC1091
. "$DEVSTACK_DIR/openrc" admin admin >/dev/null
set -u
has_feature() { [[ ",$KOC_FT_FEATURES," == *",$1,"* ]]; }

OUT="$LOG_DIR/smoke.txt"
: >"$OUT"
FAILED=0
# check <name> <command...>: run, time, record PASS/FAIL with the first error
# line (skipping koc's plain-HTTP warnings, which precede the real error).
check() {
    local name=$1 start rc err
    shift
    start=$(date +%s)
    err=$("$@" 2>&1 >/dev/null)
    rc=$?
    if [[ $rc -eq 0 ]]; then
        printf 'PASS %-40s %4ss\n' "$name" $(( $(date +%s) - start )) | tee -a "$OUT"
    else
        printf 'FAIL %-40s %4ss  %s\n' "$name" $(( $(date +%s) - start )) "$(grep -v -m1 '^WARNING: ' <<<"$err")" | tee -a "$OUT"
        FAILED=1
    fi
}
warn() { printf 'WARN %s\n' "$*" | tee -a "$OUT"; }

# wait_for <seconds> <command...>: poll every 5s until the command succeeds.
wait_for() {
    local deadline=$(( $(date +%s) + $1 ))
    shift
    until "$@"; do
        [[ $(date +%s) -lt $deadline ]] || return 1
        sleep 5
    done
}

catalog_has() { openstack catalog show "$1" -f value -c type >/dev/null; }
agents_alive() { # every neutron agent reports alive (":-)" on older clients)
    ! openstack network agent list -f value -c Alive | grep -qv -e True -e ':-)'
}
extension() { grep -qx "$1" "$LOG_DIR/extensions.txt"; }

# --- always: keystone + neutron ------------------------------------------------

check "catalog: identity" catalog_has identity
check "catalog: network" catalog_has network
check "neutron agents alive" agents_alive
openstack extension list --network -f value -c Alias | sort >"$LOG_DIR/extensions.txt"

# --- core ------------------------------------------------------------------------

server_active() { [[ "$(openstack server show ft-smoke -f value -c status)" == ACTIVE ]]; }
volume_available() { [[ "$(openstack volume show ft-smoke -f value -c status)" == available ]]; }
boot_server() {
    openstack server create --flavor m1.tiny --image "$KOC_FT_IMAGE" --network private ft-smoke >/dev/null &&
        wait_for 600 server_active
}
make_volume() {
    openstack volume create --size 1 ft-smoke >/dev/null && wait_for 300 volume_available
}
volume_services_up() {
    [[ $(openstack volume service list -f value -c Binary -c State | grep -c ' up$') -ge 3 ]]
}
s3_list() { # ListBuckets via swift's s3api, signed with the EC2 credentials
    # Stdlib SigV4 rather than curl --aws-sigv4: jammy's curl 7.81 omits the
    # x-amz-content-sha256 header s3api requires, and a koc-independent
    # client keeps "the cloud's S3 works" separate from "koc's S3 works".
    python3 - <<'PY'
import datetime, hashlib, hmac, os, sys, urllib.error, urllib.parse, urllib.request
ep, region = os.environ["AWS_ENDPOINT_URL"], os.environ["AWS_REGION"]
ak, sk = os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"]
host = urllib.parse.urlparse(ep).netloc
now = datetime.datetime.now(datetime.timezone.utc)
amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
payload = hashlib.sha256(b"").hexdigest()
signed = "host;x-amz-content-sha256;x-amz-date"
canon = f"GET\n/\n\nhost:{host}\nx-amz-content-sha256:{payload}\nx-amz-date:{amz}\n\n{signed}\n{payload}"
scope = f"{day}/{region}/s3/aws4_request"
sts = f"AWS4-HMAC-SHA256\n{amz}\n{scope}\n" + hashlib.sha256(canon.encode()).hexdigest()
key = ("AWS4" + sk).encode()
for part in (day, region, "s3", "aws4_request"):
    key = hmac.new(key, part.encode(), hashlib.sha256).digest()
sig = hmac.new(key, sts.encode(), hashlib.sha256).hexdigest()
req = urllib.request.Request(ep + "/", headers={
    "x-amz-date": amz, "x-amz-content-sha256": payload,
    "Authorization": f"AWS4-HMAC-SHA256 Credential={ak}/{scope}, SignedHeaders={signed}, Signature={sig}"})
try:
    urllib.request.urlopen(req, timeout=30)
except urllib.error.HTTPError as e:
    sys.exit(f"HTTP {e.code}: {e.read(300).decode(errors='replace')}")
PY
}
if has_feature core; then
    for t in compute image volumev3 placement object-store; do check "catalog: $t" catalog_has "$t"; done
    check "cinder scheduler, volume and backup up" volume_services_up
    check "server boots to ACTIVE" boot_server
    openstack server delete --wait ft-smoke >/dev/null 2>&1
    check "volume becomes available" make_volume
    openstack volume delete ft-smoke >/dev/null 2>&1
    check "s3api ListBuckets" s3_list
fi

# --- net -------------------------------------------------------------------------

if has_feature net; then
    for e in qos trunk segment bgp bgpvpn vpnaas fwaas_v2 taas; do check "extension: $e" extension "$e"; done
    if extension tap-mirror; then echo "PASS extension: tap-mirror" | tee -a "$OUT"; else warn "extension: tap-mirror absent (expected before 2025.1)"; fi
fi

# --- dns -------------------------------------------------------------------------

zone_active() { [[ "$(openstack zone show ft-smoke.example.org. -f value -c status)" == ACTIVE ]]; }
make_zone() {
    openstack zone create --email admin@example.org ft-smoke.example.org. >/dev/null && wait_for 300 zone_active
}
if has_feature dns; then
    check "catalog: dns" catalog_has dns
    check "zone reaches ACTIVE (bind9 via mdns)" make_zone
    openstack zone delete ft-smoke.example.org. >/dev/null 2>&1
fi

# --- koc ---------------------------------------------------------------------------

if [[ -n "$KOC" ]]; then
    # koc runs from clouds.yaml alone, so drop every OS_* openrc exported
    # (including OS_VOLUME_API_VERSION=3, which koc would send as a microversion).
    mapfile -t OPENRC_VARS < <(compgen -e | grep '^OS_')
    koc() { env "${OPENRC_VARS[@]/#/--unset=}" OS_CLOUD=devstack-admin "$KOC" "$@" -f json; }
    check "koc catalog list" koc catalog list
    check "koc network list" koc network list
    check "koc network extension list" koc network extension list
    if has_feature core; then
        check "koc server list" koc server list
        check "koc volume list" koc volume list
        check "koc image list" koc image list
        check "koc s3 bucket list" koc s3 bucket list
    fi
    if has_feature dns; then check "koc zone list" koc zone list; fi
fi

exit $FAILED
