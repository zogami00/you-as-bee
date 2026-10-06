#!/usr/bin/env bash
#
# install.sh - bootstrap the you-as-bee agent (yabd) on a Raspberry Pi.
#
# Designed to be piped to a shell:
#
#   curl -fsSL <install.sh-url> | sudo bash -s -- --release v1.0.0 --token <token>
#
# It obtains the deployment bundle, then hands off to provision.sh for the
# actual provisioning. It never duplicates provision.sh: agent.json, the token,
# the units, the udev rule, the nftables rule and the binary install are all
# provision.sh's job.
#
# The bundle can come from one of these places, in order of preference:
#
#   * a local directory: when run from a checkout (this script's directory
#     holds provision.sh and a matching yabd binary, or the binary is in
#     ../../dist);
#   * --url <bundle-url>: a base URL providing the bundle files;
#   * a GitHub release (the default when piped): --release <tag>, or the latest
#     release when no tag is given.
#
# The repository is private, so a release download needs a token with "repo"
# scope: pass --token <token> or set GITHUB_TOKEN. The token is written to a
# mode-0600 curl config file in a temporary directory and is never printed.
#
# Usage:
#   sudo ./install.sh [--url URL | --release [TAG]] [--token TOKEN]
#                     [--client-cidr CIDR[,CIDR...]] [--no-firewall]
#                     [--binary PATH] [--example PATH]
#
set -euo pipefail

# The tools below live outside a non-login shell's PATH (a piped `sudo bash`
# inherits a minimal one); set a sane PATH for the tools this script runs.
PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export PATH

REPO_SLUG="zogami00/you-as-bee"
GITHUB_API="https://api.github.com"

BUNDLE_URL=""
RELEASE_TAG=""
RELEASE_REQUESTED=0
TOKEN="${GITHUB_TOKEN:-}"
CLIENT_CIDR=""
NO_FIREWALL=0
BINARY_OVERRIDE=""
EXAMPLE_OVERRIDE=""

BIN_NAME=""

TMP_DIR=""
AUTH_CFG=""

log() { printf 'install: %s\n' "$*"; }
warn() { printf 'install: warning: %s\n' "$*" >&2; }
die() {
	printf 'install: error: %s\n' "$*" >&2
	exit 1
}

cleanup() {
	if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
		rm -rf "$TMP_DIR"
	fi
}
trap cleanup EXIT HUP INT TERM

usage() {
	cat <<'EOF'
Usage: sudo ./install.sh [options]

  --url URL                     base URL of the bundle (fetch <URL>/provision.sh,
                                the support files and the yabd binary).
  --release [TAG]               download a GitHub release; TAG defaults to the
                                latest release.
  --token TOKEN                 GitHub token for a private release (or set
                                GITHUB_TOKEN). The token is never printed.
  --client-cidr CIDR[,CIDR...]  passed through to provision.sh.
  --no-firewall                 passed through to provision.sh.
  --binary PATH                 install this yabd binary instead of the bundle's.
  --example PATH                use this agent.json template instead of the
                                bundle's.
  -h, --help                    show this help.

Examples:
  curl -fsSL <url>/install.sh | sudo bash -s -- --release v1.0.0 --token <token>
  sudo ./install.sh --url https://example.invalid/yab
  sudo ./install.sh --binary ./dist/yabd-linux-arm64 --client-cidr 192.168.1.0/24
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--url)
		[ "$#" -ge 2 ] || die "--url needs a value"
		BUNDLE_URL="$2"
		shift 2
		;;
	--release)
		RELEASE_REQUESTED=1
		# A following non-option argument is the tag; otherwise use latest.
		if [ "$#" -ge 2 ] && [ "${2#--}" = "$2" ]; then
			RELEASE_TAG="$2"
			shift 2
		else
			shift
		fi
		;;
	--token)
		[ "$#" -ge 2 ] || die "--token needs a value"
		TOKEN="$2"
		shift 2
		;;
	--client-cidr)
		[ "$#" -ge 2 ] || die "--client-cidr needs a value"
		CLIENT_CIDR="$2"
		shift 2
		;;
	--no-firewall)
		NO_FIREWALL=1
		shift
		;;
	--binary)
		[ "$#" -ge 2 ] || die "--binary needs a value"
		BINARY_OVERRIDE="$2"
		shift 2
		;;
	--example)
		[ "$#" -ge 2 ] || die "--example needs a value"
		EXAMPLE_OVERRIDE="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'install: unknown argument: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
done

machine="$(uname -m)"
case "$machine" in
aarch64 | arm64)
	BIN_NAME="yabd-linux-arm64"
	;;
armv7l | armv7 | armhf)
	BIN_NAME="yabd-linux-armv7"
	;;
*)
	die "unsupported architecture: $machine (this installer supports aarch64/arm64 and armv7l/armhf)"
	;;
esac

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo bash install.sh, or pipe to 'sudo bash')"

# When piped from curl, BASH_SOURCE[0] is not a readable file. That is fine:
# the script then falls back to a release download and uses a temp directory.
SCRIPT_SOURCE="${BASH_SOURCE[0]:-}"
SCRIPT_DIR=""
if [ -n "$SCRIPT_SOURCE" ] && [ -f "$SCRIPT_SOURCE" ]; then
	SCRIPT_DIR="$(cd -- "$(dirname -- "$SCRIPT_SOURCE")" && pwd)"
fi

prepare_temp() {
	if [ -z "$TMP_DIR" ]; then
		TMP_DIR="$(mktemp -d)"
	fi
	if [ -n "$TOKEN" ] && [ -z "$AUTH_CFG" ]; then
		AUTH_CFG="$TMP_DIR/curl-auth.conf"
		# Mode 0600, and the token never reaches the command line or the log.
		(
			umask 077
			printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" >"$AUTH_CFG"
		)
	fi
}

# fetch URL DEST [ACCEPT] - download with an optional bearer token and
# Accept header. The token lives only in the mode-0600 curl config file.
fetch() {
	local url="$1" dest="$2" accept="${3:-}"
	local args=(-fsSL --retry 2 -o "$dest")
	if [ -n "$AUTH_CFG" ]; then
		args+=(--config "$AUTH_CFG")
	fi
	if [ -n "$accept" ]; then
		args+=(-H "Accept: $accept")
	fi
	if ! curl "${args[@]}" "$url"; then
		die "download failed: $url"
	fi
}

local_ok=0
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/provision.sh" ]; then
	if [ -f "$SCRIPT_DIR/$BIN_NAME" ] || [ -f "$SCRIPT_DIR/../../dist/$BIN_NAME" ]; then
		local_ok=1
	fi
fi

if [ -n "$BUNDLE_URL" ]; then
	MODE="url"
elif [ "$RELEASE_REQUESTED" -eq 1 ]; then
	MODE="release"
elif [ "$local_ok" -eq 1 ]; then
	MODE="local"
else
	MODE="release"
fi

PROVISION_PATH=""
PROV_BIN=""
PROV_EXAMPLE=""

if [ "$MODE" = "local" ]; then
	log "architecture $machine -> $BIN_NAME; using the local checkout at $SCRIPT_DIR"
	PROVISION_PATH="$SCRIPT_DIR/provision.sh"
	if [ -f "$SCRIPT_DIR/$BIN_NAME" ]; then
		PROV_BIN="$SCRIPT_DIR/$BIN_NAME"
	else
		PROV_BIN="$SCRIPT_DIR/../../dist/$BIN_NAME"
	fi
	PROV_EXAMPLE="$SCRIPT_DIR/agent.example.json"
elif [ "$MODE" = "url" ]; then
	log "architecture $machine -> $BIN_NAME; fetching the bundle from $BUNDLE_URL"
	prepare_temp
	fetch_dir="$TMP_DIR/bundle"
	mkdir -p "$fetch_dir"
	base="${BUNDLE_URL%/}"
	for name in provision.sh agent.example.json yabd.service usbipd.service yab-modprobe.conf 90-you-as-bee.rules "$BIN_NAME"; do
		fetch "$base/$name" "$fetch_dir/$name"
		[ -s "$fetch_dir/$name" ] || die "downloaded empty file: $name"
	done
	PROVISION_PATH="$fetch_dir/provision.sh"
	PROV_BIN="$fetch_dir/$BIN_NAME"
	PROV_EXAMPLE="$fetch_dir/agent.example.json"
else
	log "architecture $machine -> $BIN_NAME; fetching the release"
	command -v python3 >/dev/null 2>&1 || die "python3 is required for release downloads"
	if [ -z "$TOKEN" ]; then
		warn "no --token/GITHUB_TOKEN given; a private release will not download. Use --token for a private repo."
	fi
	prepare_temp
	json="$TMP_DIR/release.json"
	if [ -z "$RELEASE_TAG" ] || [ "$RELEASE_TAG" = "latest" ]; then
		api_url="$GITHUB_API/repos/$REPO_SLUG/releases/latest"
	else
		api_url="$GITHUB_API/repos/$REPO_SLUG/releases/tags/$RELEASE_TAG"
	fi
	fetch "$api_url" "$json" "application/vnd.github+json"

	# The API "url" field plus Accept: application/octet-stream is the reliable
	# way to download an asset from a private repository; browser_download_url
	# redirects to a signed URL that rejects the bearer token.
	python3 - "$json" >"$TMP_DIR/assets.txt" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as fh:
    release = json.load(fh)
for asset in release.get("assets", []):
    print(asset["name"] + "\t" + asset["url"])
PY

	find_asset() {
		awk -F '\t' -v want="$1" '$1 == want { print $2; found = 1 } END { exit found ? 0 : 1 }' "$TMP_DIR/assets.txt"
	}

	fetch_dir="$TMP_DIR/bundle"
	mkdir -p "$fetch_dir"
	for name in provision.sh agent.example.json yabd.service usbipd.service yab-modprobe.conf 90-you-as-bee.rules "$BIN_NAME"; do
		asset_url="$(find_asset "$name")" || die "release does not contain asset: $name"
		fetch "$asset_url" "$fetch_dir/$name" "application/octet-stream"
		[ -s "$fetch_dir/$name" ] || die "downloaded empty file: $name"
	done
	PROVISION_PATH="$fetch_dir/provision.sh"
	PROV_BIN="$fetch_dir/$BIN_NAME"
	PROV_EXAMPLE="$fetch_dir/agent.example.json"
fi

[ -f "$PROVISION_PATH" ] || die "provision.sh not found: $PROVISION_PATH"
[ -f "$PROV_BIN" ] || die "yabd binary not found: $PROV_BIN"

# Hand off to provision.sh: it does the real work. User overrides are appended
# last, because provision.sh's parser lets the last occurrence win.
provision_args=(--binary "$PROV_BIN")
if [ -f "$PROV_EXAMPLE" ]; then
	provision_args+=(--example "$PROV_EXAMPLE")
fi
if [ -n "$CLIENT_CIDR" ]; then
	provision_args+=(--client-cidr "$CLIENT_CIDR")
fi
if [ "$NO_FIREWALL" -eq 1 ]; then
	provision_args+=(--no-firewall)
fi
if [ -n "$BINARY_OVERRIDE" ]; then
	provision_args+=(--binary "$BINARY_OVERRIDE")
fi
if [ -n "$EXAMPLE_OVERRIDE" ]; then
	provision_args+=(--example "$EXAMPLE_OVERRIDE")
fi

log "handing off to provision.sh"
bash "$PROVISION_PATH" "${provision_args[@]}"

printf '\n'
log "done. If a bearer token was printed above, copy it into the Windows client config (deploy/windows), then run install.ps1. The Pi is provisioned either way."
