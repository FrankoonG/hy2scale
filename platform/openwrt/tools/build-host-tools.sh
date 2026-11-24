#!/usr/bin/env bash
# Builds the host tools build.sh needs, from the same sources OpenWrt uses:
#   apk     apk-tools v3 (mkpkg, mkndx, adbdump)   pinned as in openwrt-25.12
#   usign   OpenWrt usign (Packages.sig)            pinned as in openwrt-25.12
#   po2lmo  LuCI translation compiler               pinned luci openwrt-25.12
#
# Usage: build-host-tools.sh <prefix>
# Installs <prefix>/bin/{apk,usign,po2lmo}. Re-running with the same pins
# is a no-op, so CI can cache <prefix> keyed on this file.
#
# Needs: git, a C compiler, make, meson, ninja, pkg-config, OpenSSL and zlib
# headers (Debian/Ubuntu: build-essential git meson ninja-build pkg-config
# libssl-dev zlib1g-dev).
set -eu

APK_URL=https://gitlab.alpinelinux.org/alpine/apk-tools.git
APK_REV=b5a31c0d865342ad80be10d68f1bb3d3ad9b0866        # apk 3.0.5, openwrt-25.12
USIGN_URL=https://github.com/openwrt/usign.git
USIGN_REV=c4c72b1b07945ee192361dc751291a7c98d6adcd      # openwrt-25.12
LUCI_URL=https://github.com/openwrt/luci.git
LUCI_REV=cb87fe60e1e2e052af36a93647f2264577829e0a       # openwrt-25.12

PREFIX="${1:?usage: $0 <prefix>}"
mkdir -p "$PREFIX/bin"
PREFIX="$(cd "$PREFIX" && pwd)"
STAMP="$PREFIX/.pins"
PINS="apk=$APK_REV usign=$USIGN_REV luci=$LUCI_REV"
if [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$PINS" ] &&
   [ -x "$PREFIX/bin/apk" ] && [ -x "$PREFIX/bin/usign" ] && [ -x "$PREFIX/bin/po2lmo" ]; then
	echo "host tools up to date in $PREFIX"
	exit 0
fi

JOBS="$(nproc 2>/dev/null || echo 4)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# fetch <url> <commit> <dir> [sparse path]: exactly that commit, nothing else.
fetch() {
	git init -q "$3"
	git -C "$3" remote add origin "$1"
	if [ -n "${4:-}" ]; then
		git -C "$3" sparse-checkout set --no-cone "$4"
		git -C "$3" fetch -q --depth 1 --filter=blob:none origin "$2"
	else
		git -C "$3" fetch -q --depth 1 origin "$2"
	fi
	git -C "$3" -c advice.detachedHead=false checkout -q FETCH_HEAD
	[ "$(git -C "$3" rev-parse HEAD)" = "$2" ]
}

echo ">> apk-tools $APK_REV"
fetch "$APK_URL" "$APK_REV" "$WORK/apk"
(
	cd "$WORK/apk"
	# Same feature set as OpenWrt's host apk: no zstd (OpenWrt's apk
	# cannot read it), openssl, static libapk.
	meson setup build -Dprefix="$PREFIX" -Ddefault_library=static \
		-Dcrypto_backend=openssl -Durl_backend=wget -Dzstd=disabled \
		-Ddocs=disabled -Dhelp=disabled -Dlua=disabled -Dpython=disabled \
		-Dtests=disabled >/dev/null
	ninja -C build -j "$JOBS" src/apk >/dev/null
	install -m 0755 build/src/apk "$PREFIX/bin/apk"
)

echo ">> usign $USIGN_REV"
fetch "$USIGN_URL" "$USIGN_REV" "$WORK/usign"
(
	cd "$WORK/usign"
	${CC:-cc} -O2 -std=gnu99 -o "$PREFIX/bin/usign" \
		ed25519.c edsign.c f25519.c fprime.c sha512.c main.c base64.c
)

echo ">> po2lmo (luci $LUCI_REV)"
fetch "$LUCI_URL" "$LUCI_REV" "$WORK/luci" "/modules/luci-base/src/"
(
	cd "$WORK/luci/modules/luci-base/src"
	make -s po2lmo CC="${CC:-cc}" >/dev/null
	install -m 0755 po2lmo "$PREFIX/bin/po2lmo"
)

printf '%s' "$PINS" > "$STAMP"
"$PREFIX/bin/apk" --version
echo "host tools installed in $PREFIX/bin"
