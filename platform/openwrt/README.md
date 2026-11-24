# OpenWrt packages

`build.sh` turns the prebuilt static release binaries into OpenWrt packages:

- `hy2scale`: one per OpenWrt package architecture.
- `luci-app-hy2scale`: architecture independent, with the zh_Hans and ko translations bundled.

Each comes in two formats: `.ipk` for opkg (OpenWrt 24.10 and earlier, and derivatives) and `.apk` for apk-tools v3 (OpenWrt 25.12 and later).

The packages are laid out the way OpenWrt's `include/package-pack.mk` and `scripts/ipkg-build` lay them out. That covers the control fields, the conffiles, the default postinst and prerm, the apk scripts and the `/lib/apk/packages` metadata. Installing, upgrading and removing them therefore behaves like an official feed package: the init script is enabled and started on install, `/etc/config/hy2scale` is kept on upgrade, and removal stops and disables the service.

## Host tools

`build.sh` needs `apk` (apk-tools v3), `po2lmo` and, to sign an opkg feed, `usign`. `tools/build-host-tools.sh` builds them from the commits OpenWrt 25.12 pins:

```sh
sudo apt-get install build-essential git meson ninja-build pkg-config libssl-dev zlib1g-dev fakeroot
platform/openwrt/tools/build-host-tools.sh ~/owrt-tools
```

Run it again with the same pins and it does nothing, so CI caches the prefix keyed on that script. `fakeroot` is needed when `build.sh` does not run as root.

## Build

```sh
platform/openwrt/build.sh --bin-dir <dir with hy2scale-<platform>[.sig]> --out <out> \
    --tools ~/owrt-tools/bin [--version 1.4.6] [--arch x86_64,aarch64_generic] \
    [--feed] [--release-assets] [--usign-key key] [--apk-key key.pem]
```

`--version` defaults to the version in `internal/api/server.go`. Run `build.sh -h` for all the options, and `build.sh --list-arches` for the architecture map.

The output goes here:

| Path | Contents |
|---|---|
| `<out>/ipk/<arch>/` | the `.ipk` files; with `--feed`, also `Packages`, `Packages.gz` and `Packages.sig` |
| `<out>/apk/<arch>/` | the `.apk` files; with `--feed`, also `packages.adb` |
| `<out>/release/` | every package under a flat name, as attached to a GitHub release |

The flat release names are:

- `hy2scale_<ver>_<arch>.ipk`
- `luci-app-hy2scale_<ver>_all.ipk`
- `hy2scale-<ver>_<arch>.apk`
- `luci-app-hy2scale-<ver>.apk`

## Versions

| hy2scale | ipk | apk |
|---|---|---|
| `1.4.6` | `1.4.6-1` | `1.4.6-r1` |
| `1.4.6-dev.11241200` | `1.4.6~dev.11241200-1` | `1.4.6_pre11241200-r1` |
| `1.4.6-rc2` | `1.4.6~rc2-1` | `1.4.6_rc2-r1` |

In both formats a pre-release sorts before its release.

## Feeds and keys

- opkg feed: sign `Packages` with a usign key (`usign -G -s key -p key.pub`). On the router, run `opkg-key add key.pub` and add `src/gz hy2scale <url>/ipk/<arch>` to `/etc/opkg/customfeeds.conf`.
- apk feed: sign `packages.adb` with an EC key (`openssl ecparam -name prime256v1 -genkey -noout -out key.pem`). On the router, put the public key in `/etc/apk/keys/` and add `<url>/apk/<arch>/packages.adb` to `/etc/apk/repositories.d/customfeeds.list`.

## SDK Makefiles

- `hy2scale/Makefile` packages the official signed release binary for the target's platform; it does not compile Go. hy2scale needs a newer Go than the 24.10 packages feed has. Copy the directory into an SDK's `package/`.
- `luci-app-hy2scale/Makefile` is a standard `luci.mk` package. Put it in a LuCI feed's `applications/`. Built that way, the translations become separate `luci-i18n-hy2scale-*` packages, as usual for a LuCI feed.

Keep each Makefile's `PKG_VERSION` and `DEPENDS` in step with `build.sh`.
