#!/usr/bin/env bash
#
# Gather a failed devstack's diagnostics into <log-dir>, next to what up.sh and
# smoke.sh already wrote. Adapted from gophercloud's script/collectlogs
# (Apache-2.0, https://github.com/gophercloud/gophercloud).
#
#   scripts/devstack/collect-logs.sh [--log-dir DIR] [--devstack-dir DIR]
#
# Deliberately no `set -e`: every step is best-effort, and a missing unit or
# tool must not stop the rest from being collected.
#
# Everything here comes from a disposable node with devstack's throwaway
# passwords. Still, do not attach it to the public repository; see AGENTS.md
# "Private data never leaves the org" before sharing logs from your own servers.

set -uo pipefail

LOG_DIR=${LOG_DIR:-$PWD/devstack-logs}
DEVSTACK_DIR=${DEVSTACK_DIR:-$PWD/devstack}
while [[ $# -gt 0 ]]; do
    case "$1" in
        --log-dir) LOG_DIR=$2; shift 2 ;;
        --devstack-dir) DEVSTACK_DIR=$2; shift 2 ;;
        *) echo "collect-logs.sh: unknown argument: $1" >&2; exit 2 ;;
    esac
done
mkdir -p "$LOG_DIR/units"

# sudo is for reading the system journal; the file is ours on purpose.
# shellcheck disable=SC2024
sudo journalctl -o short-precise --no-pager >"$LOG_DIR/journal.log" 2>&1
systemctl status 'devstack@*' --no-pager >"$LOG_DIR/devstack-services.txt" 2>&1
for unit in $(systemctl list-units --plain --no-legend 'devstack@*' | awk '{print $1}'); do
    svc=${unit#devstack@}
    journalctl -u "$unit" --no-pager >"$LOG_DIR/units/${svc%.service}.log" 2>&1
done
free -m >"$LOG_DIR/free.txt" 2>&1
df -h >"$LOG_DIR/df.txt" 2>&1
dpkg -l >"$LOG_DIR/dpkg-l.txt" 2>&1
python3 -m pip freeze >"$LOG_DIR/pip-freeze.txt" 2>&1
cp "$DEVSTACK_DIR/local.conf" "$LOG_DIR/local.conf" 2>/dev/null
sudo find "$LOG_DIR" -type d -exec chmod 0755 {} +
sudo find "$LOG_DIR" -type f -exec chmod 0644 {} +
exit 0
