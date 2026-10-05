#!/usr/bin/env bash
#
# provision.sh - install the you-as-bee agent (yabd) on a Raspberry Pi.
#
# Target: Raspberry Pi OS (Debian bookworm) on a Pi 4B or Pi Zero 2 W.
#
# Idempotent: every step checks the installed state first, so running it twice
# changes nothing the second time and reports "no changes". It never prints the
# bearer token on a re-run, because it only creates (and prints) the token once.
#
# The script expects its sibling files next to it: agent.example.json,
# yabd.service, usbipd.service, yab-modprobe.conf and 90-you-as-bee.rules, plus
# the cross-compiled yabd binary (or --binary).
#
# Usage:
#   sudo ./provision.sh [--client-cidr CIDR[,CIDR...]] [--binary PATH] \
#                       [--example PATH] [--no-firewall]
#
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

CLIENT_CIDRS=""
BINARY="${SCRIPT_DIR}/yabd-linux-arm64"
EXAMPLE="${SCRIPT_DIR}/agent.example.json"
FIREWALL=1

TOKEN_FILE="/etc/you-as-bee/token"
AGENT_JSON="/etc/you-as-bee/agent.json"
MODULES_CONF="/etc/modules-load.d/yab.conf"
MODPROBE_CONF="/etc/modprobe.d/yab-modprobe.conf"
UDEV_RULE="/etc/udev/rules.d/90-you-as-bee.rules"
NFT_FILE="/etc/nftables.d/you-as-bee.nft"
NFT_TABLE="yab"

CHANGED=0
BINARY_CHANGED=0
FIRST_TOKEN=0

log() { printf 'provision: %s\n' "$*"; }
warn() { printf 'provision: warning: %s\n' "$*" >&2; }
die() {
	printf 'provision: error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: sudo ./provision.sh [options]

  --client-cidr CIDR[,CIDR...]  CIDR allowlist for the management API and the
                                nftables rule (default: keep the example's
                                RFC1918 ranges).
  --binary PATH                 yabd binary to install (default: ./yabd-linux-arm64).
  --example PATH                agent config template (default: ./agent.example.json).
  --no-firewall                 do not install or apply the nftables rule.
  -h, --help                    show this help.
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--client-cidr)
		[ "$#" -ge 2 ] || die "--client-cidr needs a value"
		CLIENT_CIDRS="$2"
		shift 2
		;;
	--binary)
		[ "$#" -ge 2 ] || die "--binary needs a value"
		BINARY="$2"
		shift 2
		;;
	--example)
		[ "$#" -ge 2 ] || die "--example needs a value"
		EXAMPLE="$2"
		shift 2
		;;
	--no-firewall)
		FIREWALL=0
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'provision: unknown argument: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
done

# --- preflight ---------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo ./provision.sh)"

[ -r /etc/os-release ] || die "cannot read /etc/os-release"
# shellcheck disable=SC1091
. /etc/os-release
case "${VERSION_CODENAME:-}" in
bookworm) ;;
*) die "expected Raspberry Pi OS bookworm, found '${PRETTY_NAME:-unknown}'" ;;
esac
case "${ID:-}${ID_LIKE:-}" in
*debian* | *raspbian*) ;;
*) die "expected Debian/Raspberry Pi OS, found ID='${ID:-unknown}'" ;;
esac

command -v python3 >/dev/null 2>&1 || die "python3 is required (apt-get install -y python3)"
command -v modprobe >/dev/null 2>&1 || die "modprobe not found"

if ! modprobe usbip-host; then
	die "modprobe usbip-host failed; is this a Raspberry Pi kernel with USB/IP host support?"
fi

if [ "$FIREWALL" -eq 1 ]; then
	command -v nft >/dev/null 2>&1 || NEED_NFT=1
else
	NEED_NFT=0
fi

[ -f "$EXAMPLE" ] || die "agent config template not found: $EXAMPLE"
[ -f "$SCRIPT_DIR/yab-modprobe.conf" ] || die "missing $SCRIPT_DIR/yab-modprobe.conf"
[ -f "$SCRIPT_DIR/90-you-as-bee.rules" ] || die "missing $SCRIPT_DIR/90-you-as-bee.rules"
[ -f "$SCRIPT_DIR/yabd.service" ] || die "missing $SCRIPT_DIR/yabd.service"
[ -f "$SCRIPT_DIR/usbipd.service" ] || die "missing $SCRIPT_DIR/usbipd.service"
[ -f "$BINARY" ] || die "yabd binary not found: $BINARY"

if [ -n "$CLIENT_CIDRS" ]; then
	python3 - "$CLIENT_CIDRS" <<'PY' || exit 1
import ipaddress, sys
for c in sys.argv[1].split(","):
    c = c.strip()
    if c:
        ipaddress.ip_network(c, strict=False)
PY
fi

# --- helpers -----------------------------------------------------------------

# write_file DST MODE  (content on stdin; only writes when it differs)
write_file() {
	local dst="$1" mode="$2" tmp
	tmp="$(mktemp)"
	cat >"$tmp"
	if [ -f "$dst" ] && cmp -s "$tmp" "$dst"; then
		rm -f "$tmp"
		return 0
	fi
	install -D -m "$mode" "$tmp" "$dst"
	rm -f "$tmp"
	CHANGED=1
}

install_file() {
	local src="$1" dst="$2" mode="$3"
	if [ -f "$dst" ] && cmp -s "$src" "$dst"; then
		return 0
	fi
	install -D -m "$mode" "$src" "$dst"
	CHANGED=1
}

pkg_installed() { dpkg -s "$1" >/dev/null 2>&1; }

enable_unit() {
	local unit="$1"
	if systemctl is-enabled --quiet "$unit" 2>/dev/null; then
		return 0
	fi
	systemctl enable "$unit" >/dev/null
	CHANGED=1
}

start_unit() {
	local unit="$1"
	if systemctl is-active --quiet "$unit" 2>/dev/null; then
		return 0
	fi
	systemctl start "$unit"
	CHANGED=1
}

# --- packages ----------------------------------------------------------------

if ! pkg_installed usbip; then
	log "installing usbip"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update
	apt-get install -y usbip
	CHANGED=1
fi

if [ "${NEED_NFT:-0}" -eq 1 ] && ! pkg_installed nftables; then
	log "installing nftables"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update
	apt-get install -y nftables
	CHANGED=1
fi

USBIP_BIN="$(command -v usbip || true)"
USBIPD_BIN="$(command -v usbipd || true)"
[ -n "$USBIP_BIN" ] || die "usbip not found after install"
[ -n "$USBIPD_BIN" ] || die "usbipd not found after install"
log "usbip=$USBIP_BIN usbipd=$USBIPD_BIN"

# --- kernel plumbing ---------------------------------------------------------

write_file "$MODULES_CONF" 0644 < <(printf 'usbip-host\n')
install_file "$SCRIPT_DIR/yab-modprobe.conf" "$MODPROBE_CONF" 0644
install_file "$SCRIPT_DIR/90-you-as-bee.rules" "$UDEV_RULE" 0644
udevadm control --reload-rules >/dev/null 2>&1 || warn "udevadm reload failed (continuing)"

# --- agent binary and units --------------------------------------------------

if [ -f /usr/local/bin/yabd ] && cmp -s "$BINARY" /usr/local/bin/yabd; then
	:
else
	install -D -m 0755 "$BINARY" /usr/local/bin/yabd
	CHANGED=1
	BINARY_CHANGED=1
fi

install_file "$SCRIPT_DIR/usbipd.service" /etc/systemd/system/usbipd.service 0644
install_file "$SCRIPT_DIR/yabd.service" /etc/systemd/system/yabd.service 0644
systemctl daemon-reload

# --- token and agent config --------------------------------------------------

mkdir -p /etc/you-as-bee
chmod 0755 /etc/you-as-bee

if [ ! -s "$TOKEN_FILE" ]; then
	umask 077
	head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$TOKEN_FILE"
	printf '\n' >>"$TOKEN_FILE"
	chmod 0600 "$TOKEN_FILE"
	chown root:root "$TOKEN_FILE"
	CHANGED=1
	FIRST_TOKEN=1
fi

if [ ! -f "$AGENT_JSON" ]; then
	python3 - "$EXAMPLE" "$AGENT_JSON" "$CLIENT_CIDRS" "$USBIP_BIN" "$USBIPD_BIN" <<'PY'
import json, sys

example, out, cidrs, usbip, usbipd = sys.argv[1:6]
with open(example, encoding="utf-8") as fh:
    cfg = json.load(fh)

if cidrs.strip():
    cfg["allowed_clients"] = [c.strip() for c in cidrs.split(",") if c.strip()]
cfg["token_file"] = "/etc/you-as-bee/token"
cfg.setdefault("usbip", {})
cfg["usbip"]["bin"] = usbip
cfg["usbip"]["usbipd_bin"] = usbipd

with open(out, "w", encoding="utf-8") as fh:
    json.dump(cfg, fh, indent=2)
    fh.write("\n")
PY
	chmod 0644 "$AGENT_JSON"
	CHANGED=1
	log "wrote $AGENT_JSON (edit the pinned devices for your dongles)"
fi

ALLOWED_LIST="$(python3 -c 'import json,sys; print(",".join(json.load(open(sys.argv[1]))["allowed_clients"]))' "$AGENT_JSON")"

# --- firewall ----------------------------------------------------------------

if [ "$FIREWALL" -eq 1 ]; then
	CIDR_SET="${ALLOWED_LIST//,/, }"
	desired="$(
		cat <<EOF
table inet ${NFT_TABLE} {
	chain input {
		type filter hook input priority filter; policy accept;
		ip saddr { ${CIDR_SET} } tcp dport { 3240, 3241 } accept
		tcp dport { 3240, 3241 } drop
	}
}
EOF
	)"
	FW_WRITTEN=0
	if ! printf '%s\n' "$desired" | cmp -s - "$NFT_FILE" 2>/dev/null; then
		write_file "$NFT_FILE" 0644 < <(printf '%s\n' "$desired")
		FW_WRITTEN=1
	fi

	if [ -f /etc/nftables.conf ] && ! grep -qF '/etc/nftables.d/' /etc/nftables.conf; then
		printf '\ninclude "/etc/nftables.d/*.nft"\n' >>/etc/nftables.conf
		CHANGED=1
	fi

	if [ "$FW_WRITTEN" -eq 1 ] || ! nft list table inet "$NFT_TABLE" >/dev/null 2>&1; then
		nft -f "$NFT_FILE"
		CHANGED=1
	fi
	if ! systemctl is-enabled --quiet nftables.service 2>/dev/null; then
		if systemctl enable nftables.service >/dev/null 2>&1; then
			CHANGED=1
		else
			warn "could not enable nftables.service for persistence"
		fi
	fi
fi

# --- services ----------------------------------------------------------------

enable_unit usbipd.service
enable_unit yabd.service
start_unit usbipd.service

if [ "$BINARY_CHANGED" -eq 1 ] && systemctl is-active --quiet yabd.service 2>/dev/null; then
	systemctl restart yabd.service
	CHANGED=1
else
	start_unit yabd.service
fi

# --- report ------------------------------------------------------------------

if [ "$FIRST_TOKEN" -eq 1 ]; then
	token="$(tr -d '\n' <"$TOKEN_FILE")"
	cat <<EOF

===================================================================
Management API bearer token. Copy this into the Windows client
(%ProgramData%\\you-as-bee\\client.json, server "pi", field "token"):

  ${token}

Stored at ${TOKEN_FILE} (mode 0600). It is printed only once.
===================================================================
EOF
fi

if [ "$CHANGED" -eq 0 ]; then
	log "no changes"
else
	log "provisioning complete"
fi
