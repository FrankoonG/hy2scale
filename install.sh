#!/usr/bin/env sh
# hy2scale installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo sh
#
# Re-running upgrades in place. Pin a version with:
#   curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo HY2SCALE_VERSION=v1.3.1 sh
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/FrankoonG/hy2scale/main/install.sh | sudo sh -s -- --uninstall
#
# Env overrides:
#   HY2SCALE_VERSION     release tag to install (default: latest)
#   HY2SCALE_DATA_DIR    data dir (default /var/lib/hy2scale)
#   HY2SCALE_BIN         binary path (default /usr/local/bin/hy2scale)
#   HY2SCALE_PLATFORM    auto (default) | linux | unifios
#   HY2SCALE_SCRIPT_REF  git ref the platform files come from (default main)
#
# UniFi OS (detected automatically) installs everything under /data/hy2scale,
# which survives firmware updates, and is managed afterwards with
# /data/hy2scale/manage.sh (status / update / uninstall). Files under
# platform/unifios/ in the repository.

set -eu

REPO="FrankoonG/hy2scale"
VERSION="${HY2SCALE_VERSION:-}"
DATA_DIR="${HY2SCALE_DATA_DIR:-/var/lib/hy2scale}"
BIN_PATH="${HY2SCALE_BIN:-/usr/local/bin/hy2scale}"
SVC_NAME="hy2scale"
SVC_PATH="/etc/systemd/system/${SVC_NAME}.service"
PORT=5565
PLATFORM="${HY2SCALE_PLATFORM:-auto}"
SCRIPT_REF="${HY2SCALE_SCRIPT_REF:-main}"
UNIFI_ROOT="/data/hy2scale"
UNIFI_FILES="manage.sh units/hy2scale.service units/hy2scale-ensure.service units/hy2scale-ensure.timer"

UNINSTALL=0
for arg in "$@"; do
  case "$arg" in
    --uninstall|--remove) UNINSTALL=1 ;;
    -h|--help)
      sed -n 's/^# \{0,1\}//p' "$0" | sed -n '2,22p'
      exit 0 ;;
  esac
done

err()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!\033[0m %s\n' "$*" >&2; }

# ─── 1. preflight ─────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || err "must run as root — re-run with sudo (curl … | sudo sh)"

case "$(uname -s)" in
  Linux) ;;
  *) err "only Linux is supported (this script ran on: $(uname -s))" ;;
esac

for cmd in curl tar gzip uname mktemp install grep; do
  command -v "$cmd" >/dev/null 2>&1 || err "$cmd not found in PATH"
done

# ─── 1b. platform ──────────────────────────────────────────────────────────
case "$PLATFORM" in
  auto)
    if command -v ubnt-device-info >/dev/null 2>&1 && [ -d /data ]; then
      PLATFORM=unifios
    else
      PLATFORM=linux
    fi ;;
  linux | unifios) ;;
  *) err "unknown HY2SCALE_PLATFORM: $PLATFORM (auto, linux or unifios)" ;;
esac
if [ "$PLATFORM" = unifios ]; then
  [ -d /run/systemd/system ] || err "UniFi OS install needs systemd"
  DATA_DIR="${UNIFI_ROOT}/data"
  BIN_PATH="${UNIFI_ROOT}/hy2scale"
  info "Platform: UniFi OS (installing under ${UNIFI_ROOT})"
fi

# ─── 2. architecture detection ────────────────────────────────────────────
RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
  x86_64 | amd64)
    ARCH="linux-amd64" ;;
  aarch64 | arm64)
    ARCH="linux-arm64" ;;
  armv7* | armv8l | armhf | arm)
    ARCH="linux-armv7" ;;
  mips64le | mips64el)
    ARCH="linux-mips64le" ;;
  mipsel | mipsle)
    ARCH="linux-mipsle-softfloat" ;;
  *)
    err "unsupported architecture: $RAW_ARCH (supported: x86_64, aarch64, armv7, mips64le, mipsel)" ;;
esac

# ─── 3. uninstall path ────────────────────────────────────────────────────
if [ "$UNINSTALL" -eq 1 ] && [ "$PLATFORM" = unifios ]; then
  [ -f "${UNIFI_ROOT}/manage.sh" ] || err "${UNIFI_ROOT}/manage.sh not found — nothing to uninstall"
  exec sh "${UNIFI_ROOT}/manage.sh" uninstall
fi
if [ "$UNINSTALL" -eq 1 ]; then
  info "Uninstalling hy2scale…"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop "$SVC_NAME" 2>/dev/null || true
    systemctl disable "$SVC_NAME" 2>/dev/null || true
  fi
  rm -f "$SVC_PATH"
  command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload 2>/dev/null || true
  rm -f "$BIN_PATH"
  info "Removed binary and service unit."
  info "Data dir at $DATA_DIR is preserved — delete manually with: rm -rf $DATA_DIR"
  exit 0
fi

# ─── 4. resolve version (default = latest release) ────────────────────────
if [ -z "$VERSION" ]; then
  info "Resolving latest release tag from GitHub…"
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' \
    | head -n1 \
    | sed 's/.*"\([^"]*\)".*/\1/')"
  [ -n "$VERSION" ] || err "could not determine latest version (rate-limited or network issue)"
fi
info "Installing hy2scale ${VERSION} for ${ARCH}…"

# ─── 5. download tarball ──────────────────────────────────────────────────
TARBALL="hy2scale-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM
info "Fetching $URL"
curl -fL --progress-bar "$URL" -o "${TMP_DIR}/${TARBALL}" \
  || err "download failed — check the version tag and network"

# Verify against the release's checksums-sha256.txt (required)
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums-sha256.txt"
curl -fsSL "$CHECKSUM_URL" -o "${TMP_DIR}/checksums.txt" \
  || err "could not download checksums-sha256.txt for ${VERSION}"
EXPECTED="$(grep -E "[[:space:]]\\*?${TARBALL}\$" "${TMP_DIR}/checksums.txt" | awk '{print $1}')"
[ -n "$EXPECTED" ] || err "${TARBALL} is not listed in checksums-sha256.txt"
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "${TMP_DIR}/${TARBALL}" | awk '{print $1}')"
elif command -v openssl >/dev/null 2>&1; then
  ACTUAL="$(openssl dgst -sha256 "${TMP_DIR}/${TARBALL}" | awk '{print $NF}')"
else
  err "need sha256sum or openssl to verify the download"
fi
[ "$ACTUAL" = "$EXPECTED" ] || err "checksum mismatch — expected $EXPECTED, got $ACTUAL"
info "Checksum OK"

# ─── 6. extract ───────────────────────────────────────────────────────────
tar -xzf "${TMP_DIR}/${TARBALL}" -C "${TMP_DIR}"
EXTRACTED="${TMP_DIR}/hy2scale-${ARCH}"
[ -f "$EXTRACTED" ] || err "expected binary at ${EXTRACTED} not found in tarball"

# ─── 6b. UniFi OS: binary, platform files and units under /data ──────────
if [ "$PLATFORM" = unifios ]; then
  # Platform files come from the repository (HY2SCALE_PLATFORM_SRC: a local
  # directory with the same layout, for offline installs and tests).
  mkdir -p "${TMP_DIR}/platform/units"
  for f in $UNIFI_FILES; do
    name="$(basename "$f")"
    if [ -n "${HY2SCALE_PLATFORM_SRC:-}" ]; then
      cp "${HY2SCALE_PLATFORM_SRC}/${name}" "${TMP_DIR}/platform/${f}"         || err "${HY2SCALE_PLATFORM_SRC}/${name} not found"
    else
      curl -fsSL "https://raw.githubusercontent.com/${REPO}/${SCRIPT_REF}/platform/unifios/${name}"         -o "${TMP_DIR}/platform/${f}" || err "could not download platform/unifios/${name} (ref ${SCRIPT_REF})"
    fi
  done

  if systemctl is-active --quiet "$SVC_NAME" 2>/dev/null; then
    info "Stopping existing $SVC_NAME service for in-place upgrade…"
    systemctl stop "$SVC_NAME"
  fi
  mkdir -p "${UNIFI_ROOT}/units" "$DATA_DIR"
  install -m 0755 "$EXTRACTED" "$BIN_PATH"
  if [ -f "${EXTRACTED}.sig" ]; then
    install -m 0644 "${EXTRACTED}.sig" "${BIN_PATH}.sig"
  else
    rm -f "${BIN_PATH}.sig"
  fi
  install -m 0755 "${TMP_DIR}/platform/manage.sh" "${UNIFI_ROOT}/manage.sh"
  for u in hy2scale.service hy2scale-ensure.service hy2scale-ensure.timer; do
    install -m 0644 "${TMP_DIR}/platform/units/${u}" "${UNIFI_ROOT}/units/${u}"
  done
  printf '%s
' "$SCRIPT_REF" > "${UNIFI_ROOT}/.script-ref"
  info "Binary installed at $BIN_PATH"

  sh "${UNIFI_ROOT}/manage.sh" setup
  sleep 2
  if systemctl is-active --quiet "$SVC_NAME"; then
    info "Service is active."
  else
    warn "Service did not stay active. Inspect with: journalctl -u $SVC_NAME -n 50 --no-pager"
  fi

  HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -n "$HOST_IP" ] || HOST_IP="<host>"
  printf '
'
  printf '[32m✓ hy2scale %s installed (UniFi OS).[0m
' "$(echo "$VERSION" | sed 's/^v//')"
  printf '
'
  printf '  Binary    %s
' "$BIN_PATH"
  printf '  Data dir  %s
' "$DATA_DIR"
  printf '  Service   %s (enabled, running; restored by hy2scale-ensure.timer)
' "$SVC_NAME"
  printf '  Logs      journalctl -u %s -f
' "$SVC_NAME"
  printf '  Web UI    https://%s:%s/scale  (login: admin / admin — forces password change on first login)
' "$HOST_IP" "$PORT"
  printf '  Manage    %s/manage.sh status | update | uninstall
' "$UNIFI_ROOT"
  printf '
'
  warn "Open UDP $PORT to the gateway in UniFi Network (firewall policy) if other nodes connect to this one."
  if command -v ss >/dev/null 2>&1 && ss -lun 2>/dev/null | grep -qE ':(500|4500)[[:space:]]'; then
    warn "UDP 500/4500 is in use by UniFi's own VPN — leave hy2scale's IKEv2 and L2TP off."
  fi
  exit 0
fi

# ─── 7. install binary (stop service first if upgrading) ──────────────────
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$SVC_NAME" 2>/dev/null; then
  info "Stopping existing $SVC_NAME service for in-place upgrade…"
  systemctl stop "$SVC_NAME"
fi

mkdir -p "$(dirname "$BIN_PATH")"
install -m 0755 "$EXTRACTED" "$BIN_PATH"
# Release signature next to the binary (the node shows itself as an official build)
if [ -f "${EXTRACTED}.sig" ]; then
  install -m 0644 "${EXTRACTED}.sig" "${BIN_PATH}.sig"
else
  rm -f "${BIN_PATH}.sig"
fi
info "Binary installed at $BIN_PATH"

# ─── 8. data dir ──────────────────────────────────────────────────────────
if [ ! -d "$DATA_DIR" ]; then
  mkdir -p "$DATA_DIR"
  info "Created data dir $DATA_DIR"
fi

# ─── 9. systemd unit ──────────────────────────────────────────────────────
HAS_SYSTEMD=0
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  HAS_SYSTEMD=1
  info "Writing systemd unit $SVC_PATH"
  # NOTE: we run as root deliberately. hy2scale needs CAP_NET_ADMIN
  # for iptables / TUN management and CAP_NET_RAW for some rule
  # probes; running as root yields these without ambient-capability
  # gymnastics, matching the documented Docker deploy.
  cat > "$SVC_PATH" <<UNIT
[Unit]
Description=hy2scale — Hysteria 2 mesh relay
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_PATH} --data ${DATA_DIR}
Restart=always
RestartSec=3
LimitNOFILE=1048576
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable "$SVC_NAME" >/dev/null 2>&1 || true
  info "Starting $SVC_NAME…"
  systemctl restart "$SVC_NAME"
  # Give it a moment to crash if it's going to
  sleep 2
  if systemctl is-active --quiet "$SVC_NAME"; then
    info "Service is active."
  else
    warn "Service did not stay active. Inspect with: journalctl -u $SVC_NAME -n 50 --no-pager"
  fi
else
  warn "systemd not detected — skipping service registration."
  warn "Run manually: $BIN_PATH --data $DATA_DIR"
fi

# ─── 10. final info ───────────────────────────────────────────────────────
HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$HOST_IP" ] || HOST_IP="<host>"
VER_NUM="$(echo "$VERSION" | sed 's/^v//')"

printf '\n'
printf '\033[32m✓ hy2scale %s installed.\033[0m\n' "$VER_NUM"
printf '\n'
printf '  Binary    %s\n' "$BIN_PATH"
printf '  Data dir  %s\n' "$DATA_DIR"
if [ "$HAS_SYSTEMD" -eq 1 ]; then
  printf '  Service   %s (enabled, running)\n' "$SVC_PATH"
  printf '  Logs      journalctl -u %s -f\n' "$SVC_NAME"
fi
printf '  Web UI    http://%s:%s/scale  (login: admin / admin — forces password change on first login)\n' "$HOST_IP" "$PORT"
printf '\n'
printf '  Uninstall curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sudo sh -s -- --uninstall\n' "$REPO"
printf '\n'

if command -v ufw >/dev/null 2>&1 || command -v firewall-cmd >/dev/null 2>&1; then
  warn "A host firewall is present. Allow TCP+UDP $PORT (and 500/4500/1194/51820/UDP if you use VPN services)."
fi
