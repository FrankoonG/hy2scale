#!/usr/bin/env bash
# shellcheck disable=SC2016 # the OpenWrt script text is literal on purpose
# Builds the OpenWrt packages of hy2scale from prebuilt static binaries:
#   hy2scale           one per OpenWrt package architecture, .ipk and .apk
#   luci-app-hy2scale  architecture-independent, .ipk ("all") and .apk ("noarch")
# The packages are laid out the way OpenWrt's include/package-pack.mk and
# scripts/ipkg-build lay them out (control fields, conffiles, the default
# postinst/prerm, apk scripts and /lib/apk/packages metadata), so they
# install, upgrade and remove like packages from an OpenWrt feed.
#
# Usage: platform/openwrt/build.sh -b <bin-dir> -o <out-dir> [options]
# Run with -h for the options. Needs bash, GNU tar, gzip, sha256sum, and the
# host tools from tools/build-host-tools.sh (apk for .apk, po2lmo for the
# translations, usign to sign an opkg feed); fakeroot when not run as root.
set -euo pipefail
export LC_ALL=C
umask 022

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

# OpenWrt package architecture -> release platform (hy2scale-<platform>).
# Every architecture in the 24.10 and 25.12 package trees that Go can
# target. Not built: armeb_xscale (big-endian ARM), arm_fa526 (ARMv4; Go
# needs ARMv5), powerpc_464fp and powerpc_8548 (32-bit PowerPC),
# powerpc64_e5500 (Go needs POWER8).
ARCH_MAP='
x86_64                    linux-amd64
i386_pentium4             linux-386
i386_pentium-mmx          linux-386
aarch64_generic           linux-arm64
aarch64_cortex-a53        linux-arm64
aarch64_cortex-a72        linux-arm64
aarch64_cortex-a76        linux-arm64
arm_cortex-a5_vfpv4       linux-armv7
arm_cortex-a7_neon-vfpv4  linux-armv7
arm_cortex-a7_vfpv4       linux-armv7
arm_cortex-a8_vfpv3       linux-armv7
arm_cortex-a9_neon        linux-armv7
arm_cortex-a9_vfpv3-d16   linux-armv7
arm_cortex-a15_neon-vfpv4 linux-armv7
arm_arm1176jzf-s_vfp      linux-armv6
arm_arm926ej-s            linux-armv5
arm_cortex-a7             linux-armv5
arm_cortex-a9             linux-armv5
arm_xscale                linux-armv5
mips_24kc                 linux-mips-softfloat
mips_4kec                 linux-mips-softfloat
mips_mips32               linux-mips-softfloat
mipsel_24kc               linux-mipsle-softfloat
mipsel_24kc_24kf          linux-mipsle-softfloat
mipsel_74kc               linux-mipsle-softfloat
mipsel_mips32             linux-mipsle-softfloat
mips64_mips64r2           linux-mips64
mips64_octeonplus         linux-mips64
mips64el_mips64r2         linux-mips64le
riscv64_generic           linux-riscv64
riscv64_riscv64           linux-riscv64
loongarch64_generic       linux-loong64
'

# Package metadata. Keep in sync with hy2scale/Makefile and
# luci-app-hy2scale/Makefile.
PKG_DEPENDS="kmod-tun ca-bundle iptables-nft ip6tables-nft kmod-ipt-conntrack
	iptables-mod-tproxy kmod-ipt-tproxy iptables-mod-conntrack-extra
	kmod-ipt-conntrack-extra iptables-mod-extra kmod-ipt-extra ipset
	kmod-ipt-ipset iptables-mod-iprange kmod-ipt-iprange kmod-ipt-nat
	kmod-ipt-nat6"
# shellcheck disable=SC2086,SC2116
PKG_DEPENDS="$(echo $PKG_DEPENDS)"
LUCI_DEPENDS="luci-base hy2scale"
PKG_LICENSE="GPL-3.0-only"
PKG_URL="https://github.com/FrankoonG/hy2scale"
PKG_DESC="Hysteria 2 mesh relay network with a web management UI."
LUCI_DESC="LuCI support for hy2scale"
# LuCI language directory -> lmo language code (luci.mk LUCI_LC_ALIAS)
LUCI_LC_ALIAS="zh_Hans:zh-cn zh_Hant:zh-tw pt_BR:pt-br nb_NO:no bn_BD:bn"

usage() {
	cat <<-EOF
	Usage: $0 -b <bin-dir> -o <out-dir> [options]

	  -b, --bin-dir DIR     hy2scale-<platform> binaries (a hy2scale-<platform>.sig
	                        next to one is packaged as /usr/bin/hy2scale.sig)
	  -o, --out DIR         output directory
	  -v, --version VER     hy2scale version (default: internal/api/server.go)
	  -r, --release N       package release number (default 1)
	  -a, --arch LIST       only these OpenWrt architectures (comma-separated)
	  -f, --format FMT      ipk, apk or both (default both)
	      --feed            write feed indexes (Packages[.gz,.sig], packages.adb)
	      --release-assets  also copy every package, flat, into <out>/release
	      --skip-missing    skip architectures whose binary is missing
	  -j, --jobs N          parallel jobs (default: nproc)
	      --tools DIR       directory holding apk, usign, po2lmo (added to PATH)
	      --usign-key FILE  usign secret key: signs Packages (env USIGN_KEY)
	      --apk-key FILE    apk private key (PEM): signs packages.adb (env APK_KEY)
	      --pkg-src DIR     hy2scale package dir (default: $HERE/hy2scale)
	      --luci-src DIR    luci-app dir (default: $HERE/luci-app-hy2scale)
	      --epoch SECONDS   SOURCE_DATE_EPOCH (default: last commit time)
	      --list-arches     print the architecture map and exit

	Output:
	  <out>/ipk/<arch>/  hy2scale_<ver>_<arch>.ipk, luci-app-hy2scale_<ver>_all.ipk
	  <out>/apk/<arch>/  hy2scale-<ver>.apk, luci-app-hy2scale-<ver>.apk
	  <out>/release/     hy2scale_<ver>_<arch>.ipk, hy2scale-<ver>_<arch>.apk,
	                     luci-app-hy2scale_<ver>_all.ipk, luci-app-hy2scale-<ver>.apk
	EOF
}

die() { echo "build.sh: $*" >&2; exit 1; }

BIN_DIR="" OUT="" VERSION="" REL=1 ARCHES="" FORMAT=both FEED=0 ASSETS=0 SKIP_MISSING=0
JOBS="$(nproc 2>/dev/null || echo 4)"
USIGN_KEY="${USIGN_KEY:-}" APK_KEY="${APK_KEY:-}"
PKG_SRC="$HERE/hy2scale" LUCI_SRC="$HERE/luci-app-hy2scale" EPOCH="${SOURCE_DATE_EPOCH:-}"
while [ $# -gt 0 ]; do
	case "$1" in
		-b|--bin-dir) BIN_DIR="$2"; shift ;;
		-o|--out) OUT="$2"; shift ;;
		-v|--version) VERSION="$2"; shift ;;
		-r|--release) REL="$2"; shift ;;
		-a|--arch) ARCHES="$2"; shift ;;
		-f|--format) FORMAT="$2"; shift ;;
		--feed) FEED=1 ;;
		--release-assets) ASSETS=1 ;;
		--skip-missing) SKIP_MISSING=1 ;;
		-j|--jobs) JOBS="$2"; shift ;;
		--tools) PATH="$(cd "$2" && pwd):$PATH"; shift ;;
		--usign-key) USIGN_KEY="$2"; shift ;;
		--apk-key) APK_KEY="$2"; shift ;;
		--pkg-src) PKG_SRC="$2"; shift ;;
		--luci-src) LUCI_SRC="$2"; shift ;;
		--epoch) EPOCH="$2"; shift ;;
		--list-arches) echo "$ARCH_MAP" | sed '/^$/d'; exit 0 ;;
		-h|--help) usage; exit 0 ;;
		*) usage >&2; die "unknown option: $1" ;;
	esac
	shift
done

[ -n "$BIN_DIR" ] && [ -d "$BIN_DIR" ] || die "--bin-dir is required and must exist"
[ -n "$OUT" ] || die "--out is required"
case "$FORMAT" in ipk) DO_IPK=1 DO_APK=0 ;; apk) DO_IPK=0 DO_APK=1 ;; both) DO_IPK=1 DO_APK=1 ;; *) die "bad --format $FORMAT" ;; esac
case "$REL" in ''|*[!0-9]*) die "bad --release $REL" ;; esac
BIN_DIR="$(cd "$BIN_DIR" && pwd)"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
[ -d "$PKG_SRC/files" ] || die "missing $PKG_SRC/files"
PKG_SRC="$(cd "$PKG_SRC" && pwd)"
[ -d "$LUCI_SRC" ] || die "missing $LUCI_SRC"
LUCI_SRC="$(cd "$LUCI_SRC" && pwd)"
for f in hy2scale.init hy2scale.config; do
	[ -f "$PKG_SRC/files/$f" ] || die "missing $PKG_SRC/files/$f"
done

if [ -z "$VERSION" ]; then
	VERSION="$(sed -n 's/^const Version = "\([0-9A-Za-z.-]*\)"$/\1/p' "$REPO/internal/api/server.go")"
	[ -n "$VERSION" ] || die "cannot read the version; pass --version"
fi
if [ -z "$EPOCH" ]; then
	EPOCH="$(git -C "$REPO" log -1 --format=%ct 2>/dev/null || date +%s)"
fi
case "$EPOCH" in ''|*[!0-9]*) die "bad epoch $EPOCH" ;; esac
export SOURCE_DATE_EPOCH="$EPOCH"
# scripts/ipkg-build's tar flags (run under fakeroot there: owner 0)
TAROPTS=(--format=gnu --numeric-owner --owner=0 --group=0 --sort=name "--mtime=@$EPOCH")

# ── Versions ─────────────────────────────────────────────────────────
# 1.4.6               -> ipk 1.4.6-1                  apk 1.4.6-r1
# 1.4.6-dev.11241200  -> ipk 1.4.6~dev.11241200-1     apk 1.4.6_pre11241200-r1
# 1.4.6-rc2           -> ipk 1.4.6~rc2-1              apk 1.4.6_rc2-r1
# opkg sorts "~" before anything, even the end of the string; apk sorts
# _alpha < _beta < _pre < _rc < release. So dev < rc < release in both.
BASE="${VERSION%%-*}"
PRE=""
[ "$BASE" = "$VERSION" ] || PRE="${VERSION#*-}"
echo "$BASE" | grep -Eq '^[0-9]+(\.[0-9]+)*$' || die "unsupported version $VERSION"
if [ -z "$PRE" ]; then
	IPK_VER="$BASE-$REL"
	APK_VER="$BASE-r$REL"
else
	echo "$PRE" | grep -Eq '^[0-9A-Za-z.]+$' || die "unsupported version $VERSION"
	IPK_VER="$BASE~$PRE-$REL"
	num="$(echo "$PRE" | sed -n 's/^[A-Za-z]*\.\{0,1\}\([0-9][0-9]*\)$/\1/p')"
	word="$(echo "$PRE" | sed -n 's/^\([A-Za-z]*\).*/\1/p' | tr '[:upper:]' '[:lower:]')"
	case "$word" in
		dev|pre|snapshot) word=pre ;;
		alpha|a) word=alpha ;;
		beta|b) word=beta ;;
		rc) ;;
		*)
			# any other pre-release still sorts before the release
			echo "build.sh: pre-release '$PRE' maps to apk _pre" >&2
			word=pre
			num="$(echo "$PRE" | tr -cd '0-9')"
			;;
	esac
	APK_VER="${BASE}_${word}${num}-r$REL"
fi

# ── Tools ────────────────────────────────────────────────────────────
need() { command -v "$1" >/dev/null 2>&1 || die "$1 not found (${2:-})"; }
need tar; need gzip; need sha256sum; need find
tar --version 2>/dev/null | grep -q 'GNU tar' || die "GNU tar required"
[ "$DO_APK" = 0 ] || need apk "build it with tools/build-host-tools.sh"
need po2lmo "build it with tools/build-host-tools.sh"
[ -z "$USIGN_KEY" ] || need usign "build it with tools/build-host-tools.sh"
[ -z "$USIGN_KEY" ] || [ -f "$USIGN_KEY" ] || die "no usign key $USIGN_KEY"
[ -z "$APK_KEY" ] || [ -f "$APK_KEY" ] || die "no apk key $APK_KEY"
if [ "$(id -u)" = 0 ] || [ "$DO_APK" = 0 ]; then
	AS_ROOT() { "$@"; }
else
	need fakeroot "needed to record root ownership in .apk files"
	AS_ROOT() { fakeroot -- "$@"; }
fi
if [ "$DO_APK" = 1 ]; then
	bad="$(apk version -c "$APK_VER" 2>/dev/null || true)"
	[ -z "$bad" ] || die "apk rejects version $APK_VER"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ── Architectures ────────────────────────────────────────────────────
SELECTED=""
if [ -n "$ARCHES" ]; then
	for a in $(echo "$ARCHES" | tr ',' ' '); do
		echo "$ARCH_MAP" | awk -v a="$a" '$1 == a { f = 1 } END { exit !f }' || die "unknown architecture $a"
		SELECTED="$SELECTED $a"
	done
else
	SELECTED="$(echo "$ARCH_MAP" | awk 'NF { print $1 }' | tr '\n' ' ')"
fi
platform_of() { echo "$ARCH_MAP" | awk -v a="$1" '$1 == a { print $2 }'; }

# ── Package staging ──────────────────────────────────────────────────
# A staged package: <dir>/root (the files), <dir>/meta (name, arch,
# depends, ...), <dir>/conffiles, <dir>/postinst-pkg, <dir>/prerm-pkg.

# stage_hy2scale <dir> <binary>
stage_hy2scale() {
	local d="$1" bin="$2" r="$1/root"
	mkdir -p "$r/usr/bin" "$r/etc/init.d" "$r/etc/config"
	install -m 0755 "$bin" "$r/usr/bin/hy2scale"
	[ ! -f "$bin.sig" ] || install -m 0644 "$bin.sig" "$r/usr/bin/hy2scale.sig"
	install -m 0755 "$PKG_SRC/files/hy2scale.init" "$r/etc/init.d/hy2scale"
	install -m 0600 "$PKG_SRC/files/hy2scale.config" "$r/etc/config/hy2scale"
	if [ -f "$PKG_SRC/files/hy2scale.keep" ]; then
		mkdir -p "$r/lib/upgrade/keep.d"
		install -m 0644 "$PKG_SRC/files/hy2scale.keep" "$r/lib/upgrade/keep.d/hy2scale"
	fi
	echo "/etc/config/hy2scale" > "$d/conffiles"
	# Package/hy2scale/prerm: drop the firewall rules on removal (opkg does
	# not run prerm on an upgrade; apk runs pre-deinstall on removal only).
	printf '%s\n' \
		'#!/bin/sh' \
		'[ -n "${IPKG_INSTROOT}" ] || [ "${PKG_UPGRADE}" = "1" ] || {' \
		'	[ -x /etc/init.d/hy2scale ] && /etc/init.d/hy2scale fwclean' \
		'}' \
		'exit 0' > "$d/prerm-pkg"
	# ipkg-build's data.tar.gz depends on the files only: one per platform
	[ "$DO_IPK" = 0 ] || ipk_data "$r" "$d/data.tar.gz"
}

# ipk_data <root> <data.tar.gz>
ipk_data() {
	(cd "$1" && tar "${TAROPTS[@]}" -cpf - . | gzip -n - > "$2")
}

# stage_luci <dir>
stage_luci() {
	local d="$1" r="$1/root" po lang lc alias
	mkdir -p "$r"
	[ ! -d "$LUCI_SRC/htdocs" ] || { mkdir -p "$r/www"; cp -R "$LUCI_SRC/htdocs/." "$r/www/"; }
	[ ! -d "$LUCI_SRC/root" ] || cp -R "$LUCI_SRC/root/." "$r/"
	# Translations: luci.mk builds po/<lang>/<name>.po into
	# /usr/lib/lua/luci/i18n/<name>.<lc>.lmo (zh_Hans -> zh-cn).
	if [ -d "$LUCI_SRC/po" ]; then
		for po in "$LUCI_SRC"/po/*/*.po; do
			[ -f "$po" ] || continue
			lang="$(basename "$(dirname "$po")")"
			[ "$lang" != templates ] || continue
			lc="$lang"
			for alias in $LUCI_LC_ALIAS; do
				[ "${alias%%:*}" != "$lang" ] || lc="${alias#*:}"
			done
			mkdir -p "$r/usr/lib/lua/luci/i18n"
			po2lmo "$po" "$r/usr/lib/lua/luci/i18n/$(basename "$po" .po).$lc.lmo"
		done
	fi
	# Modes as an OpenWrt checkout has them: executables where rpcd and
	# procd run them, everything else data.
	find "$r" -type d -exec chmod 0755 {} +
	find "$r" -type f -exec chmod 0644 {} +
	for x in usr/libexec usr/bin usr/sbin etc/init.d; do
		[ ! -d "$r/$x" ] || find "$r/$x" -type f -exec chmod 0755 {} +
	done
	# Package/luci-app-*/postinst from luci.mk
	printf '%s\n' \
		'[ -n "${IPKG_INSTROOT}" ] || {' \
		'	rm -f /tmp/luci-indexcache.*' \
		'	rm -rf /tmp/luci-modulecache/' \
		'	/etc/init.d/rpcd reload 2>/dev/null' \
		'	exit 0' \
		'}' > "$d/postinst-pkg"
	[ "$DO_IPK" = 0 ] || ipk_data "$r" "$d/data.tar.gz"
}

# ── .ipk (include/package-pack.mk + scripts/ipkg-build) ──────────────
# make_ipk <stage> <name> <version> <arch> <depends> <section> <desc> <dest>
make_ipk() {
	local s="$1" name="$2" ver="$3" arch="$4" deps="$5" section="$6" desc="$7" dest="$8"
	local p t c size
	p="$(mktemp -d "$WORK/ipk.XXXXXX")"
	c="$p/CONTROL"
	mkdir -p "$c"
	{
		echo "Package: $name"
		echo "Version: $ver"
		echo "Depends: $(echo "libc $deps" | sed 's/ \{1,\}/, /g')"
		echo "Source: feeds/hy2scale/$name"
		echo "SourceName: $name"
		echo "License: $PKG_LICENSE"
		echo "Section: $section"
		echo "SourceDateEpoch: $EPOCH"
		echo "URL: $PKG_URL"
		echo "Architecture: $arch"
		echo "Installed-Size: 0"
		printf "Description: "; echo "$desc" | sed -e 's,^[[:space:]]*, ,g'
	} > "$c/control"
	cat > "$c/postinst" <<-'EOF'
	#!/bin/sh
	[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0
	[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0
	. ${IPKG_INSTROOT}/lib/functions.sh
	default_postinst $0 $@
	EOF
	cat > "$c/prerm" <<-'EOF'
	#!/bin/sh
	[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0
	. ${IPKG_INSTROOT}/lib/functions.sh
	default_prerm $0 $@
	EOF
	chmod 0644 "$c/control"
	chmod 0755 "$c/postinst" "$c/prerm"
	for t in postinst-pkg prerm-pkg; do
		[ ! -f "$s/$t" ] || install -m 0755 "$s/$t" "$c/$t"
	done
	if [ -f "$s/conffiles" ]; then
		# ipkg-build keeps the conffiles that exist, sorted
		while read -r t; do
			[ -z "$t" ] || [ ! -f "$s/root$t" ] || echo "$t"
		done < "$s/conffiles" | sort > "$c/conffiles"
		chmod 0644 "$c/conffiles"
		[ -s "$c/conffiles" ] || rm -f "$c/conffiles"
	fi
	cp "$s/data.tar.gz" "$p/data.tar.gz"
	size="$(zcat < "$p/data.tar.gz" | wc -c)"
	sed -i -e "s/^Installed-Size: .*/Installed-Size: $size/" "$c/control"
	(cd "$c" && tar "${TAROPTS[@]}" -cf - . | gzip -n - > "$p/control.tar.gz")
	echo "2.0" > "$p/debian-binary"
	mkdir -p "$(dirname "$dest")"
	(cd "$p" && tar "${TAROPTS[@]}" -cf - ./debian-binary ./data.tar.gz ./control.tar.gz | gzip -n - > "$dest")
	rm -rf "$p"
}

# ── .apk (include/package-pack.mk, CONFIG_USE_APK) ──────────────────
# make_apk <stage> <name> <version> <arch> <depends> <desc> <dest>
make_apk() {
	local s="$1" name="$2" ver="$3" arch="$4" deps="$5" desc="$6" dest="$7"
	local p i a f csum
	p="$(mktemp -d "$WORK/apk.XXXXXX")"
	cp -a "$s/root" "$p/idir"
	i="$p/idir" a="$p/adir"
	mkdir -p "$a" "$i/lib/apk/packages"
	{
		echo "#!/bin/sh"
		echo '[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0'
		echo '[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$name\""
		echo "add_group_and_user"
		echo "default_postinst"
		[ ! -f "$s/postinst-pkg" ] || sed '/^\s*#!/d' "$s/postinst-pkg"
	} > "$a/post-install"
	{
		echo "#!/bin/sh"
		echo 'export PKG_UPGRADE=1'
		sed '/^\s*#!/d' "$a/post-install"
	} > "$a/post-upgrade"
	{
		echo "#!/bin/sh"
		echo '[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$name\""
		echo "default_prerm"
		[ ! -f "$s/prerm-pkg" ] || sed '/^\s*#!/d' "$s/prerm-pkg"
	} > "$a/pre-deinstall"
	chmod 0755 "$a"/*
	(cd "$i" && find . -type f,l -printf "/%P\n" | sort > "$p/list" && mv "$p/list" "$i/lib/apk/packages/$name.list")
	if [ -f "$s/conffiles" ]; then
		cp "$s/conffiles" "$i/lib/apk/packages/$name.conffiles"
		for f in $(cat "$i/lib/apk/packages/$name.conffiles"); do
			[ -f "$i/$f" ] || continue
			csum="$(sha256sum "$i/$f" | cut -d' ' -f1)"
			echo "$f $csum" >> "$i/lib/apk/packages/$name.conffiles_static"
		done
	fi
	find "$i/lib" -type d -exec chmod 0755 {} +
	find "$i/lib/apk/packages" -type f -exec chmod 0644 {} +
	local args=(
		--info "name:$name"
		--info "version:$ver"
		--info "description:$desc"
		--info "arch:$arch"
		--info "license:$PKG_LICENSE"
		--info "origin:feeds/hy2scale/$name"
		--info "url:$PKG_URL"
		--info "provides:$name-any"
		--script "post-install:$a/post-install"
		--script "post-upgrade:$a/post-upgrade"
		--script "pre-deinstall:$a/pre-deinstall"
		--info "depends:libc $deps"
		--files "$i"
		--output "$dest"
	)
	mkdir -p "$(dirname "$dest")"
	AS_ROOT sh -c 'chown -R 0:0 "$1" && shift && exec apk mkpkg "$@"' sh "$i" "${args[@]}" >/dev/null
	rm -rf "$p"
}

# ── Feed indexes ─────────────────────────────────────────────────────
# ipk: scripts/ipkg-make-index.sh + package/Makefile (index filtering,
# the usign padding workaround, Packages.gz, Packages.sig).
make_ipk_index() {
	local d="$1" pkg file_size sum
	(
		cd "$d"
		for pkg in $(find . -maxdepth 1 -name '*.ipk' | sed 's,^\./,,' | sort); do
			file_size="$(stat -L -c%s "$pkg")"
			sum="$(sha256sum "$pkg" | cut -d' ' -f1)"
			tar -xzOf "$pkg" ./control.tar.gz | tar xzOf - ./control | sed -e "s/^Description:/Filename: $pkg\\
Size: $file_size\\
SHA256sum: $sum\\
Description:/"
			echo ""
		done > Packages.manifest
		grep -vE '^(Maintainer|LicenseFiles|Source|SourceName|Require|SourceDateEpoch)' Packages.manifest > Packages
		case "$(((64 + $(stat -L -c%s Packages)) % 128))" in
			110|111) { echo ""; echo ""; } >> Packages ;;
		esac
		gzip -9nc Packages > Packages.gz
		rm -f Packages.sig
		[ -z "$USIGN_KEY" ] || usign -S -m Packages -s "$USIGN_KEY"
	)
}

# apk: package/Makefile (apk mkndx, signed with the feed key).
make_apk_index() {
	local d="$1" empty="$WORK/empty"
	mkdir -p "$empty"
	(
		cd "$d"
		rm -f packages.adb
		apk mkndx --root "$empty" --keys-dir "$empty" --allow-untrusted \
			${APK_KEY:+--sign-key "$APK_KEY"} --output packages.adb ./*.apk >/dev/null
	)
}

# ── Build ────────────────────────────────────────────────────────────
echo "hy2scale $VERSION -> ipk $IPK_VER, apk $APK_VER (epoch $EPOCH)"

LUCI_STAGE="$WORK/stage-luci"
stage_luci "$LUCI_STAGE"
LUCI_IPK="$WORK/luci-app-hy2scale_${IPK_VER}_all.ipk"
LUCI_APK="$WORK/luci-app-hy2scale-${APK_VER}.apk"
[ "$DO_IPK" = 0 ] || make_ipk "$LUCI_STAGE" luci-app-hy2scale "$IPK_VER" all "$LUCI_DEPENDS" luci "$LUCI_DESC" "$LUCI_IPK"
[ "$DO_APK" = 0 ] || make_apk "$LUCI_STAGE" luci-app-hy2scale "$APK_VER" noarch "$LUCI_DEPENDS" "$LUCI_DESC" "$LUCI_APK"

if [ "$ASSETS" = 1 ]; then
	rm -rf "$OUT/release"
	mkdir -p "$OUT/release"
	# GitHub renames release assets with a "~" (a pre-release ipk version)
	[ "$DO_IPK" = 0 ] || cp "$LUCI_IPK" "$OUT/release/$(basename "$LUCI_IPK" | tr '~' '-')"
	[ "$DO_APK" = 0 ] || cp "$LUCI_APK" "$OUT/release/"
fi

# run_jobs <function> <args...>: one background job per argument, at most
# $JOBS at a time; fails if any job failed.
run_jobs() {
	local fn="$1" x fail=0 n=0
	shift
	for x in "$@"; do
		if [ "$n" -ge "$JOBS" ]; then
			wait -n || fail=1
			n=$((n - 1))
		fi
		"$fn" "$x" &
		n=$((n + 1))
	done
	while [ "$n" -gt 0 ]; do
		wait -n || fail=1
		n=$((n - 1))
	done
	[ "$fail" = 0 ] || die "a $fn job failed"
}

ARCHES_OK="" PLATS=""
for arch in $SELECTED; do
	plat="$(platform_of "$arch")"
	if [ ! -f "$BIN_DIR/hy2scale-$plat" ]; then
		[ "$SKIP_MISSING" = 1 ] || die "missing $BIN_DIR/hy2scale-$plat (for $arch)"
		echo "  skip $arch: no hy2scale-$plat"
		continue
	fi
	ARCHES_OK="$ARCHES_OK $arch"
	case " $PLATS " in *" $plat "*) ;; *) PLATS="$PLATS $plat" ;; esac
done
[ -n "$ARCHES_OK" ] || die "nothing to build"

stage_platform() { stage_hy2scale "$WORK/stage-$1" "$BIN_DIR/hy2scale-$1"; }
# shellcheck disable=SC2086
run_jobs stage_platform $PLATS

build_arch() {
	local arch="$1" plat stage d
	plat="$(platform_of "$arch")"
	stage="$WORK/stage-$plat"
	if [ "$DO_IPK" = 1 ]; then
		d="$OUT/ipk/$arch"
		rm -rf "$d"; mkdir -p "$d"
		make_ipk "$stage" hy2scale "$IPK_VER" "$arch" "$PKG_DEPENDS" net "$PKG_DESC" "$d/hy2scale_${IPK_VER}_${arch}.ipk"
		cp "$LUCI_IPK" "$d/"
		[ "$FEED" = 0 ] || make_ipk_index "$d"
		[ "$ASSETS" = 0 ] || cp "$d/hy2scale_${IPK_VER}_${arch}.ipk" "$OUT/release/hy2scale_$(echo "$IPK_VER" | tr '~' '-')_${arch}.ipk"
	fi
	if [ "$DO_APK" = 1 ]; then
		d="$OUT/apk/$arch"
		rm -rf "$d"; mkdir -p "$d"
		make_apk "$stage" hy2scale "$APK_VER" "$arch" "$PKG_DEPENDS" "$PKG_DESC" "$d/hy2scale-${APK_VER}.apk"
		cp "$LUCI_APK" "$d/"
		[ "$FEED" = 0 ] || make_apk_index "$d"
		[ "$ASSETS" = 0 ] || cp "$d/hy2scale-${APK_VER}.apk" "$OUT/release/hy2scale-${APK_VER}_${arch}.apk"
	fi
	echo "  $arch ($plat)"
}
# shellcheck disable=SC2086
run_jobs build_arch $ARCHES_OK
built="$(echo $ARCHES_OK | wc -w)"

# Public keys next to the feeds, for /etc/opkg/keys and /etc/apk/keys.
if [ "$FEED" = 1 ]; then
	if [ -n "$USIGN_KEY" ] && [ "$DO_IPK" = 1 ] && command -v python3 >/dev/null 2>&1; then
		# usign secret key -> public key: algorithm, fingerprint, key
		python3 - "$USIGN_KEY" > "$OUT/ipk/hy2scale-feed.usign.pub" <<-'EOF'
		import base64, sys
		s = base64.b64decode(open(sys.argv[1]).read().splitlines()[1])
		print("untrusted comment: public key " + s[32:40].hex())
		print(base64.b64encode(s[0:2] + s[32:40] + s[72:104]).decode())
		EOF
	fi
	if [ -n "$APK_KEY" ] && [ "$DO_APK" = 1 ] && command -v openssl >/dev/null 2>&1; then
		openssl pkey -in "$APK_KEY" -pubout -out "$OUT/apk/hy2scale-feed.pem"
	fi
fi
echo "built $built architectures into $OUT"
