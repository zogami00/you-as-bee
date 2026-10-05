#!/usr/bin/env bash
#
# uninstall.sh - remove the you-as-bee agent from a Raspberry Pi.
#
# Stops and disables both units, returns any exported device to its original
# kernel driver, and removes the files provision.sh installed. /etc/you-as-bee
# (the token and the config) and /var/lib/you-as-bee are kept unless --purge is
# given.
#
# Usage:
#   sudo ./uninstall.sh [--purge]
#
set -euo pipefail

PURGE=0

log() { printf 'uninstall: %s\n' "$*"; }
die() {
	printf 'uninstall: error: %s\n' "$*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--purge)
		PURGE=1
		shift
		;;
	-h | --help)
		printf 'Usage: sudo ./uninstall.sh [--purge]\n'
		exit 0
		;;
	*)
		printf 'uninstall: unknown argument: %s\n' "$1" >&2
		exit 2
		;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo ./uninstall.sh)"

# --- stop services -----------------------------------------------------------

systemctl disable --now yabd.service 2>/dev/null || true
systemctl disable --now usbipd.service 2>/dev/null || true

# --- return every usbip-host device to its original driver -------------------

# Remove the btusb blacklist FIRST. The kernel applies a modprobe blacklist
# when it tries to load a module; while btusb was still blacklisted, the
# drivers_probe below found no driver to bind and the dongle came back with no
# driver at all.
rm -f /etc/modprobe.d/yab-modprobe.conf

# Make sure the original driver is loadable before probing, and tolerate it
# already being loaded.
modprobe btusb 2>/dev/null || true

# A device left bound to usbip-host would disappear from the local USB bus.
# Reboot would recover it, but do it properly now: unbind usbip-host, drop the
# match_busid entry, then let the kernel re-probe the original driver.
if [ -d /sys/bus/usb/drivers/usbip-host ]; then
	for link in /sys/bus/usb/devices/*/driver; do
		[ -e "$link" ] || continue
		[ "$(basename "$(readlink -f "$link")")" = "usbip-host" ] || continue
		busid="$(basename "$(dirname "$link")")"
		log "releasing $busid from usbip-host"
		printf '%s' "$busid" >/sys/bus/usb/drivers/usbip-host/unbind 2>/dev/null || true
		if [ -w /sys/bus/usb/drivers/usbip-host/match_busid ]; then
			printf 'del %s' "$busid" >/sys/bus/usb/drivers/usbip-host/match_busid 2>/dev/null || true
		fi
		printf '%s' "$busid" >/sys/bus/usb/drivers_probe 2>/dev/null || true
	done
fi

# If probing did not re-bind a device (for example because it enumerated after
# the loop), ask udev to replay the add events now that btusb is available.
udevadm trigger --action=add --subsystem-match=usb 2>/dev/null || true

# --- firewall ----------------------------------------------------------------

if command -v nft >/dev/null 2>&1; then
	nft delete table inet yab 2>/dev/null || true
fi
rm -f /etc/nftables.d/you-as-bee.nft
rmdir /etc/nftables.d 2>/dev/null || true

# --- files -------------------------------------------------------------------

rm -f /usr/local/bin/yabd
rm -f /etc/systemd/system/yabd.service
rm -f /etc/systemd/system/usbipd.service
rm -f /etc/modules-load.d/yab.conf
rm -f /etc/udev/rules.d/90-you-as-bee.rules

if [ -d /etc/udev/rules.d ]; then
	udevadm control --reload-rules >/dev/null 2>&1 || true
fi

systemctl daemon-reload 2>/dev/null || true

if [ "$PURGE" -eq 1 ]; then
	rm -rf /etc/you-as-bee /var/lib/you-as-bee
	log "removed /etc/you-as-bee /var/lib/you-as-bee"
else
	log "kept /etc/you-as-bee and /var/lib/you-as-bee (use --purge to remove them)"
fi

log "uninstalled"
