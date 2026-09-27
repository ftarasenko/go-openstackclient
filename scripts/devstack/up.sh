#!/usr/bin/env bash
#
# Bring up a single-node devstack for koc's functional tests.
#
# One script for both places the suite runs: a disposable VM on a developer's
# own server, and a GitHub-hosted runner in the nightly workflow. Keeping the
# bring-up here (rather than in a composite action) means a config that stacks
# locally is byte-for-byte the config CI runs.
#
#   scripts/devstack/up.sh --series <zed|caracal|epoxy|latest|YYYY.N> \
#       [--features core,net,dns] [--dest DIR] [--log-dir DIR] [--dry-run]
#
# Features (keystone + neutron are always on):
#   core  nova, glance, cinder (+ backup to swift), placement, swift + s3api
#   net   neutron's own devstack plugin (qos, trunk, segments, port forwarding,
#         ...) plus neutron-dynamic-routing, networking-bgpvpn, neutron-vpnaas,
#         neutron-fwaas and tap-as-a-service
#   dns   designate (bind9 backend); with "net" also neutron's dns integration
#
# Neutron backend by series: ML2/OVS through 2024.1, where fwaas, vpnaas and
# bgp dynamic routing need the L3 agent; ML2/OVN, devstack's default, from
# 2025.1, where vpnaas and fwaas ship OVN drivers. That covers both backends
# the fleet runs.
#
# Outputs, all under --log-dir:
#   stack.sh.log       devstack's own log (ends with its component timing table)
#   local.conf         the exact configuration used
#   refs.txt           the git ref resolved for devstack and every plugin
#   bringup.json       series, backend, refs, host, durations, peak RAM, disk used, result
#   functional.env     OS_CLOUD, S3 credentials and fixture names for the tests
#   openrc.env         the OS_* devstack's `openrc admin admin` exports, resolved,
#                      so the tests can run koc from an openrc instead of clouds.yaml
#
# The GitHub-runner workarounds (--gha, automatic when GITHUB_ACTIONS=true) are
# adapted from gophercloud/devstack-action v0.19 (Apache-2.0,
# https://github.com/gophercloud/devstack-action). A fresh VM does not need them.
#
# Every password below is devstack's throwaway default for a disposable node.

set -euo pipefail

GIT_BASE=${GIT_BASE:-https://github.com}
SERIES=""
FEATURES="core,net,dns"
DEST=${DEST:-/opt/stack}
LOG_DIR=${LOG_DIR:-$PWD/devstack-logs}
DEVSTACK_DIR=${DEVSTACK_DIR:-$PWD/devstack}
DRY_RUN=false
GHA=${GITHUB_ACTIONS:-false}

usage() { sed -n '3,40p' "$0" | sed 's/^# \{0,1\}//'; }
die() { echo "up.sh: $*" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --series) SERIES=$2; shift 2 ;;
        --features) FEATURES=$2; shift 2 ;;
        --dest) DEST=$2; shift 2 ;;
        --log-dir) LOG_DIR=$2; shift 2 ;;
        --devstack-dir) DEVSTACK_DIR=$2; shift 2 ;;
        --gha) GHA=true; shift ;;
        --dry-run) DRY_RUN=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
done
[[ -n "$SERIES" ]] || die "--series is required"
$DRY_RUN || [[ $EUID -ne 0 ]] || die "devstack refuses to run as root; run as a user with passwordless sudo (devstack's tools/create-stack-user.sh)"

has_feature() { [[ ",$FEATURES," == *",$1,"* ]]; }

# --- series → release name and the devstack branch it lives on -------------

# Release names map to the series number OpenStack's branches are named after.
# Zed predates numbered series, so its branches are literally ".../zed".
case "$SERIES" in
    zed|2022.2) SERIES=zed ;;
    caracal) SERIES=2024.1 ;;
    epoxy) SERIES=2025.1 ;;
    flamingo) SERIES=2025.2 ;;
    gazpacho) SERIES=2026.1 ;;
    hibiscus) SERIES=2026.2 ;;
    latest)
        # The newest stable branch devstack carries. A branch is cut at RC1, a
        # few weeks before the release date, so "latest" is the newest release
        # or its release candidate — never master.
        SERIES=$(git ls-remote --heads "$GIT_BASE/openstack/devstack" 'stable/*' |
            sed -n 's#.*refs/heads/stable/\([0-9][0-9]*\.[0-9]\)$#\1#p' | sort -V | tail -1)
        [[ -n "$SERIES" ]] || die "could not resolve the latest stable devstack branch"
        ;;
    [0-9][0-9][0-9][0-9].[12]) ;;
    *) die "unknown series: $SERIES" ;;
esac

# ref_for <project>: the branch or tag to deploy <project> from. A series moves
# stable/S → unmaintained/S → S-eol over its life, and each project moves on its
# own schedule (on Zed, octavia/fwaas/vpnaas are already -eol while nova is
# still unmaintained), so resolve each one rather than hard-coding a table that
# goes stale the day a branch is renamed.
declare -A REFS=()
ref_for() {
    local project=$1 refs ref
    if [[ -n "${REFS[$project]:-}" ]]; then echo "${REFS[$project]}"; return; fi
    refs=$(git ls-remote --heads --tags "$GIT_BASE/openstack/$project") ||
        die "git ls-remote failed for $project"
    for ref in "refs/heads/stable/$SERIES" "refs/heads/unmaintained/$SERIES" "refs/tags/$SERIES-eol"; do
        if grep -q "[[:space:]]$ref\$" <<<"$refs"; then
            REFS[$project]=${ref#refs/*/}
            echo "${REFS[$project]}"
            return
        fi
    done
    die "$project has no stable/$SERIES, unmaintained/$SERIES or $SERIES-eol"
}

plugin() { # plugin <name>: an enable_plugin line pinned to the resolved ref
    echo "enable_plugin $1 $GIT_BASE/openstack/$1 $(ref_for "$1")"
}

# --- host checks -------------------------------------------------------------

# shellcheck disable=SC1091
. /etc/os-release
case "$SERIES" in
    zed|2024.1) WANT_UBUNTU="22.04" ;;         # devstack: jammy only (zed also focal)
    2025.1) WANT_UBUNTU="22.04 24.04" ;;       # jammy|noble
    *) WANT_UBUNTU="24.04" ;;                  # 2026.x: noble (2026.2 also resolute)
esac
if [[ "${ID:-}" != ubuntu || " $WANT_UBUNTU " != *" ${VERSION_ID:-} "* ]]; then
    msg="series $SERIES wants Ubuntu $WANT_UBUNTU, this host is ${PRETTY_NAME:-unknown}"
    if $DRY_RUN; then echo "up.sh: warning: $msg" >&2; else die "$msg"; fi
fi

# Neutron backend (see the header): OVS before 2025.1, OVN from it.
if [[ "$SERIES" == zed || "$(printf '%s\n' "$SERIES" 2025.1 | sort -V | head -1)" != 2025.1 ]]; then
    BACKEND=ovs
else
    BACKEND=ovn
fi

# --- local.conf ------------------------------------------------------------

mkdir -p "$LOG_DIR"

# Resolve every ref up front, in this shell: write_local_conf runs inside a
# command substitution, where a failed lookup's die() would only end the
# subshell and a cached ref would never reach refs.txt.
NEEDED=(devstack)
if has_feature net; then
    NEEDED+=(neutron neutron-dynamic-routing networking-bgpvpn neutron-vpnaas neutron-fwaas tap-as-a-service)
fi
if has_feature dns; then NEEDED+=(designate); fi
for p in "${NEEDED[@]}"; do ref_for "$p" >/dev/null; done
DEVSTACK_REF=${REFS[devstack]}

write_local_conf() {
    cat <<EOF
[[local|localrc]]
ADMIN_PASSWORD=secret
DATABASE_PASSWORD=root
RABBIT_PASSWORD=secret
SERVICE_PASSWORD=secret
SWIFT_HASH=1234123412341234
DEST=$DEST
LOGFILE=$LOG_DIR/stack.sh.log
GIT_BASE=$GIT_BASE
# Full clones, not GIT_DEPTH=1: pbr derives each project's version from its
# tags, so a shallow clone installs as 0.0.0 and pip then rejects any plugin
# that requires a minimum version of it (bagpipe needs networking-bgpvpn>=12)
# or replaces it with a PyPI wheel (neutron-dynamic-routing's neutron>=23).
INSTALL_TEMPEST=False
# One worker per API keeps the all-in-one node inside a runner's 16 GB.
API_WORKERS=1
SERVICE_TIMEOUT=120
disable_service horizon dstat tempest
EOF

    if [[ $BACKEND == ovs ]]; then
        cat <<EOF

# ML2/OVS (see the header).
Q_AGENT=openvswitch
Q_ML2_PLUGIN_MECHANISM_DRIVERS=openvswitch
Q_ML2_TENANT_NETWORK_TYPE=vxlan
disable_service ovn-controller ovn-northd ovs-vswitchd ovsdb-server q-ovn-metadata-agent q-ovn-agent br-ex-tcpdump br-int-flows
enable_service q-svc q-agt q-dhcp q-l3 q-meta
EOF
    else
        # devstack's defaults already are OVN; spelled out because the vpnaas
        # and fwaas plugin settings pick their drivers from Q_AGENT.
        cat <<EOF

# ML2/OVN (see the header).
Q_AGENT=ovn
Q_ML2_PLUGIN_MECHANISM_DRIVERS=ovn
Q_ML2_TENANT_NETWORK_TYPE=geneve
EOF
    fi

    if has_feature core; then
        cat <<EOF

# core: swift with the S3 API, cinder backups into swift.
enable_service s-account s-container s-object s-proxy s3api c-bak
CINDER_ISCSI_HELPER=lioadm
VOLUME_BACKING_FILE_SIZE=10G
EOF
    else
        echo "disable_service n-api n-cpu n-cond n-sch n-novnc n-api-meta placement-api c-api c-sch c-vol"
    fi

    if has_feature net; then
        cat <<EOF

# net: the neutron extensions koc's network commands cover.
$(plugin neutron)
enable_service q-qos q-trunk q-metering neutron-segments neutron-network-segment-range neutron-port-forwarding neutron-tag-ports-during-bulk-creation neutron-conntrack-helper neutron-ndp-proxy neutron-port-trusted-vif neutron-uplink-status-propagation
$(plugin neutron-dynamic-routing)
$(plugin networking-bgpvpn)
$(plugin neutron-vpnaas)
$(plugin neutron-fwaas)
$(plugin tap-as-a-service)
enable_service taas tap_mirror
EOF
        # Under OVN, IPsec runs in vpnaas's own agent instead of the L3 agent.
        if [[ $BACKEND == ovn ]]; then echo "enable_service q-ovn-vpn-agent"; fi
    fi

    if has_feature dns; then
        cat <<EOF

# dns: designate with its default bind9 backend.
$(plugin designate)
enable_service designate designate-central designate-api designate-worker designate-producer designate-mdns
EOF
        if has_feature net; then echo "enable_service neutron-dns"; fi
    fi

    # Meta sections end localrc, so they go last.
    if has_feature core; then
        # swift's s3token now authenticates to keystone's /v3/s3tokens with a
        # service token (swift ec975b1c7, backported to 2024.1 and 2025.1);
        # devstack only writes these credentials from 2026.2, so on older
        # branches every S3 request is a 401. Mirrors devstack 2026.2's
        # lib/swift; older s3token ignores the extra keys.
        cat <<EOF

[[post-config|/etc/swift/proxy-server.conf]]
[filter:s3token]
auth_type = password
auth_url = http://\$SERVICE_HOST/identity
project_name = service
project_domain_name = Default
username = swift
user_domain_name = Default
password = secret
EOF
    fi
}

LOCAL_CONF=$(write_local_conf)
printf '%s\n' "$LOCAL_CONF" >"$LOG_DIR/local.conf"
for p in "${!REFS[@]}"; do echo "$p ${REFS[$p]}"; done | sort >"$LOG_DIR/refs.txt"

echo "== series $SERIES, features $FEATURES, backend $BACKEND, devstack $DEVSTACK_REF"
cat "$LOG_DIR/refs.txt"
if $DRY_RUN; then
    cat "$LOG_DIR/local.conf"
    exit 0
fi

# --- GitHub-runner workarounds -------------------------------------------------

# The hosted image ships services devstack wants to install itself. Adapted
# from gophercloud/devstack-action (see the header); harmless on a fresh VM but
# not needed there, so only on --gha.
gha_workarounds() {
    python3 -m pip install --upgrade pip
    sudo apt-get update
    sudo apt-get purge -y 'mysql-*' || true
    # A leading "-e " in /etc/hosts (actions/runner-images#12192) and a
    # mismatched erlang both stop rabbitmq from starting.
    sudo sed -i 's/^-e \+//g' /etc/hosts
    sudo apt-get purge -y esl-erlang || true
    sudo apt-get install -y erlang rabbitmq-server
    sudo apt-get install -y runc || true
    sudo apt-get purge -y python3-simplejson python3-pyasn1-modules 'postgresql*' || true
}
if [[ "$GHA" == true ]]; then gha_workarounds; fi

# --- stack -------------------------------------------------------------------

if [[ ! -d "$DEVSTACK_DIR/.git" ]]; then
    git clone --depth 1 --branch "$DEVSTACK_REF" "$GIT_BASE/openstack/devstack" "$DEVSTACK_DIR"
fi
printf '%s\n' "$LOCAL_CONF" >"$DEVSTACK_DIR/local.conf"

# stack.sh fetches cirros from download.cirros-cloud.net with one wget and no
# retry, and that host timing out has failed a whole cell. It skips the fetch
# when the file is already in files/, so on a runner put it there first, from
# cirros's GitHub release mirror (the host runners reach most reliably) with
# the original as a fallback, retrying both.
prefetch_cirros() {
    local version file url
    version=$(grep -m1 '^CIRROS_VERSION=' "$DEVSTACK_DIR/stackrc" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
    [[ -n $version ]] || { echo "up.sh: no CIRROS_VERSION in stackrc; leaving the fetch to stack.sh"; return 0; }
    file=cirros-$version-$(uname -m)-disk.img
    mkdir -p "$DEVSTACK_DIR/files"
    [[ -f $DEVSTACK_DIR/files/$file ]] && return 0
    for url in "https://github.com/cirros-dev/cirros/releases/download/$version/$file" \
        "https://download.cirros-cloud.net/$version/$file"; do
        if curl -fsSL --retry 5 --retry-all-errors --connect-timeout 20 -o "$DEVSTACK_DIR/files/$file.part" "$url"; then
            mv "$DEVSTACK_DIR/files/$file.part" "$DEVSTACK_DIR/files/$file"
            echo "up.sh: prefetched $file from $url"
            return 0
        fi
    done
    rm -f "$DEVSTACK_DIR/files/$file.part"
    echo "up.sh: could not prefetch $file; leaving the fetch to stack.sh"
}
if [[ "$GHA" == true && $FEATURES == *core* ]]; then prefetch_cirros; fi

disk_used_mb() { df -B1M --output=used / | tail -1 | tr -d ' '; }
DISK_BEFORE=$(disk_used_mb)
# Peak RAM: sample every 10s for the length of the stack. `free` is coarse but
# the question is only "does this fit in 16 GB with headroom".
(while sleep 10; do free -m | awk '/^Mem:/ {print $3}'; done) >"$LOG_DIR/mem.samples" &
SAMPLER=$!
trap 'kill $SAMPLER 2>/dev/null || true' EXIT

START=$(date +%s)
set +e
(cd "$DEVSTACK_DIR" && ./stack.sh)
RC=$?
set -e
STACK_SECONDS=$(( $(date +%s) - START ))
kill $SAMPLER 2>/dev/null || true
MEM_PEAK=$(sort -n "$LOG_DIR/mem.samples" | tail -1)
DISK_DELTA=$(( $(disk_used_mb) - DISK_BEFORE ))

# --- fixtures for the tests ----------------------------------------------------

if [[ $RC -eq 0 ]]; then
    set +u
    # shellcheck disable=SC1091
    . "$DEVSTACK_DIR/openrc" admin admin >/dev/null
    set -u
    {
        echo "OS_CLOUD=devstack-admin"
        echo "KOC_FT_SERIES=$SERIES"
        echo "KOC_FT_FEATURES=$FEATURES"
        echo "KOC_FT_BACKEND=$BACKEND"
        if has_feature core; then
            echo "KOC_FT_IMAGE=$(openstack image list -f value -c Name | grep -m1 -i cirros)"
            # S3 goes to swift's proxy through the s3api middleware, with EC2
            # credentials keystone mints for the admin user.
            { read -r ACCESS; read -r SECRET; } < <(openstack ec2 credentials create -f value -c access -c secret)
            echo "AWS_ENDPOINT_URL=http://${SERVICE_HOST:-127.0.0.1}:8080"
            echo "AWS_ACCESS_KEY_ID=$ACCESS"
            echo "AWS_SECRET_ACCESS_KEY=$SECRET"
            echo "AWS_REGION=us-east-1"
        fi
    } >"$LOG_DIR/functional.env"
    # The openrc path, resolved: a shell script cannot be read from Go, and
    # these are the variables an operator's `source openrc` leaves behind —
    # bare OS_VOLUME_API_VERSION=3 and ID-only domains included.
    env | grep '^OS_' | sort >"$LOG_DIR/openrc.env"
fi

cat >"$LOG_DIR/bringup.json" <<EOF
{
  "series": "$SERIES",
  "features": "$FEATURES",
  "backend": "$BACKEND",
  "devstack_ref": "$DEVSTACK_REF",
  "os": "${PRETTY_NAME:-unknown}",
  "kvm": $([[ -e /dev/kvm ]] && echo true || echo false),
  "cpus": $(nproc),
  "mem_total_mb": $(free -m | awk '/^Mem:/ {print $2}'),
  "stack_seconds": $STACK_SECONDS,
  "mem_peak_used_mb": ${MEM_PEAK:-0},
  "disk_delta_mb": $DISK_DELTA,
  "stack_rc": $RC
}
EOF
cat "$LOG_DIR/bringup.json"
exit $RC
