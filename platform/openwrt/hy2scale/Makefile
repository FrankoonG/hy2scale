# SPDX-License-Identifier: GPL-3.0-only
#
# hy2scale for the OpenWrt SDK / buildroot. Packages the official, signed
# release binary of the target's platform (the same bytes build.sh
# packages); hy2scale needs a newer Go than the 24.10 packages feed has.

include $(TOPDIR)/rules.mk

PKG_NAME:=hy2scale
PKG_VERSION:=1.4.6
PKG_RELEASE:=1

PKG_LICENSE:=GPL-3.0-only

# OpenWrt target -> release platform (see build.sh ARCH_MAP)
HY2SCALE_FPU:=$(word 2,$(subst +, ,$(call qstrip,$(CONFIG_CPU_TYPE))))
HY2SCALE_PLATFORM:=$(strip \
	$(if $(filter x86_64,$(ARCH)),linux-amd64) \
	$(if $(filter i386,$(ARCH)),linux-386) \
	$(if $(filter aarch64,$(ARCH)),linux-arm64) \
	$(if $(filter arm,$(ARCH)),linux-armv$(if $(HY2SCALE_FPU),$(if $(filter vfp vfpv2,$(HY2SCALE_FPU)),6,7),5)) \
	$(if $(filter mips,$(ARCH)),linux-mips-softfloat) \
	$(if $(filter mipsel,$(ARCH)),linux-mipsle-softfloat) \
	$(if $(filter mips64,$(ARCH)),linux-mips64) \
	$(if $(filter mips64el,$(ARCH)),linux-mips64le) \
	$(if $(filter riscv64,$(ARCH)),linux-riscv64) \
	$(if $(filter loongarch64,$(ARCH)),linux-loong64))

PKG_SOURCE:=hy2scale-$(PKG_VERSION)-$(HY2SCALE_PLATFORM).tar.gz
PKG_SOURCE_URL:=https://github.com/FrankoonG/hy2scale/releases/download/v$(PKG_VERSION)
PKG_SOURCE_URL_FILE:=hy2scale-$(HY2SCALE_PLATFORM).tar.gz
# One archive per platform; the binary carries its release signature
# (hy2scale-<platform>.sig), which hy2scale itself verifies.
PKG_HASH:=skip

include $(INCLUDE_DIR)/package.mk

define Package/hy2scale
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Hysteria 2 mesh relay network
  URL:=https://github.com/FrankoonG/hy2scale
  DEPENDS:=@(aarch64||arm||i386||loongarch64||mips||mips64||mips64el||mipsel||riscv64||x86_64) \
	+kmod-tun +ca-bundle +iptables-nft +ip6tables-nft +kmod-ipt-conntrack \
	+iptables-mod-tproxy +kmod-ipt-tproxy +iptables-mod-conntrack-extra \
	+kmod-ipt-conntrack-extra +iptables-mod-extra +kmod-ipt-extra +ipset \
	+kmod-ipt-ipset +iptables-mod-iprange +kmod-ipt-iprange +kmod-ipt-nat \
	+kmod-ipt-nat6
endef

define Package/hy2scale/description
  Hysteria 2 mesh relay network with a web management UI.
endef

define Package/hy2scale/conffiles
/etc/config/hy2scale
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(TAR) -C $(PKG_BUILD_DIR) -xzf $(DL_DIR)/$(PKG_SOURCE)
endef

define Build/Configure
endef

define Build/Compile
endef

define Package/hy2scale/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/hy2scale-$(HY2SCALE_PLATFORM) $(1)/usr/bin/hy2scale
	$(INSTALL_DATA) $(PKG_BUILD_DIR)/hy2scale-$(HY2SCALE_PLATFORM).sig $(1)/usr/bin/hy2scale.sig
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/hy2scale.init $(1)/etc/init.d/hy2scale
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/hy2scale.config $(1)/etc/config/hy2scale
	$(INSTALL_DIR) $(1)/lib/upgrade/keep.d
	$(INSTALL_DATA) ./files/hy2scale.keep $(1)/lib/upgrade/keep.d/hy2scale
endef

define Package/hy2scale/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || [ "$${PKG_UPGRADE}" = "1" ] || {
	[ -x /etc/init.d/hy2scale ] && /etc/init.d/hy2scale fwclean
}
exit 0
endef

$(eval $(call BuildPackage,hy2scale))
