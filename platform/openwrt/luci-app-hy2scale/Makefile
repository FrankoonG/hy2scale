# SPDX-License-Identifier: GPL-3.0-only
#
# LuCI page for hy2scale. Standard luci.mk package: place this directory in
# a LuCI feed's applications/ (luci.mk two levels up). Built that way, the
# translations become separate luci-i18n-hy2scale-<lang> packages, as in
# any LuCI feed; build.sh bundles them into luci-app-hy2scale instead.

include $(TOPDIR)/rules.mk

PKG_VERSION:=1.4.6
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-only

LUCI_TITLE:=LuCI support for hy2scale
LUCI_DESCRIPTION:=LuCI support for hy2scale
LUCI_DEPENDS:=+luci-base +hy2scale
LUCI_PKGARCH:=all
LUCI_URL:=https://github.com/FrankoonG/hy2scale

include ../../luci.mk

# call BuildPackage - OpenWrt buildroot signature
