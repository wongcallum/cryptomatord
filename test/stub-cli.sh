#!/usr/bin/env bash
# Fake cryptomator-cli for hermetic supervisor tests and the NixOS VM test.
#
# Emulates:
#   cryptomator-cli unlock --password:stdin --mounter=<c> --mountPoint=<dir> [...] <vaultPath>
#
# "Mounting" is simulated by touching a sibling marker file "<mountPoint>.mounted"
# (a sibling, so the mount point directory itself stays empty as libfuse
# requires). SIGINT/SIGTERM removes the marker and exits 0, emulating a clean
# unmount.
#
# Behaviour is driven per-invocation by --mountOption directives so tests need
# no global state:
#   --mountOption=stub-fail       exit non-zero immediately (unlock failure)
#   --mountOption=stub-crash=N    become mounted, then exit non-zero after N secs
set -euo pipefail

mountpoint=""
fail=""
exit_after=""
for arg in "$@"; do
	case "$arg" in
		--mountPoint=*) mountpoint="${arg#--mountPoint=}" ;;
		--mountOption=stub-fail) fail=1 ;;
		--mountOption=stub-crash=*) exit_after="${arg#--mountOption=stub-crash=}" ;;
	esac
done

# Consume the passphrase from stdin (as --password:stdin does).
cat >/dev/null || true

if [[ -n "$fail" ]]; then
	echo "stub-cli: unlock failed" >&2
	exit 1
fi

if [[ -z "$mountpoint" ]]; then
	echo "stub-cli: --mountPoint is required" >&2
	exit 2
fi

marker="${mountpoint}.mounted"
touch "$marker"
echo "stub-cli: mounted at ${mountpoint}" >&2

cleanup() {
	rm -f "$marker"
	[[ -n "${sleeppid:-}" ]] && kill "$sleeppid" 2>/dev/null
	exit 0
}
trap cleanup INT TERM

if [[ -n "$exit_after" ]]; then
	sleep "$exit_after"
	echo "stub-cli: simulated crash" >&2
	exit 1
fi

# Stay "mounted" until signalled. `wait` (unlike a foreground sleep) is
# interrupted immediately by the trapped signal.
sleep infinity &
sleeppid=$!
wait "$sleeppid"
