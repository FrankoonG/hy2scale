#!/bin/sh
# hy2scale on UniFi OS — kept at /data/hy2scale/manage.sh by install.sh.
#
#   manage.sh status              service state and version
#   manage.sh update [vX.Y.Z]     re-run the installer (latest by default)
#   manage.sh uninstall [--purge] remove the service; --purge also deletes data
#   manage.sh on-boot             restore and start the units (run by the
#                                 hy2scale-ensure timer; safe to run any time)
#
# Layout: /data/hy2scale/{hy2scale,hy2scale.sig,manage.sh,units/,data/}.
# /data survives reboots and firmware updates; the unit files are copied
# (not linked) into /etc/systemd/system so systemd can read them early in
# boot, and on-boot puts them back if they go missing.

set -eu

REPO="FrankoonG/hy2scale"
ROOT="/data/hy2scale"
BIN="${ROOT}/hy2scale"
UNITS_SRC="${ROOT}/units"
UNITS_DST="/etc/systemd/system"
UNITS="hy2scale.service hy2scale-ensure.service hy2scale-ensure.timer"
REF_FILE="${ROOT}/.script-ref"

err()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[32m==>\033[0m %s\n' "$*"; }

[ "$(id -u)" -eq 0 ] || err "must run as root"

# copy_units copies every unit whose /etc copy is missing or different and
# sets changed=1 when it wrote any.
copy_units() {
  changed=0
  for u in $UNITS; do
    [ -f "${UNITS_SRC}/${u}" ] || err "missing ${UNITS_SRC}/${u}"
    if ! cmp -s "${UNITS_SRC}/${u}" "${UNITS_DST}/${u}"; then
      # A link here would point into /data, which systemd may not see yet.
      rm -f "${UNITS_DST}/${u}"
      cp "${UNITS_SRC}/${u}" "${UNITS_DST}/${u}"
      chmod 0644 "${UNITS_DST}/${u}"
      changed=1
    fi
  done
}

ensure() {
  [ -x "$BIN" ] || err "binary ${BIN} not found — re-run the installer"
  copy_units
  [ "$changed" -eq 0 ] || systemctl daemon-reload
  systemctl enable hy2scale.service hy2scale-ensure.timer >/dev/null 2>&1
  systemctl is-active --quiet hy2scale-ensure.timer || systemctl start hy2scale-ensure.timer
  systemctl is-active --quiet hy2scale.service || systemctl start hy2scale.service
}

cmd="${1:-status}"
[ $# -gt 0 ] && shift

case "$cmd" in
  on-boot)
    ensure
    ;;
  setup)
    # Called by install.sh after it placed a new binary.
    ensure
    systemctl restart hy2scale.service
    ;;
  status)
    printf 'version  %s\n' "$("$BIN" --version 2>/dev/null || echo '?')"
    printf 'service  %s\n' "$(systemctl is-active hy2scale.service 2>/dev/null || true)"
    printf 'timer    %s\n' "$(systemctl is-active hy2scale-ensure.timer 2>/dev/null || true)"
    ;;
  update)
    ref="main"
    [ -f "$REF_FILE" ] && ref="$(cat "$REF_FILE")"
    url="https://raw.githubusercontent.com/${REPO}/${ref}/install.sh"
    info "Running ${url}"
    curl -fsSL "$url" | HY2SCALE_PLATFORM=unifios HY2SCALE_SCRIPT_REF="$ref" HY2SCALE_VERSION="${1:-}" sh
    ;;
  uninstall)
    purge=0
    [ "${1:-}" = "--purge" ] && purge=1
    systemctl disable --now hy2scale-ensure.timer hy2scale.service >/dev/null 2>&1 || true
    for u in $UNITS; do rm -f "${UNITS_DST}/${u}"; done
    systemctl daemon-reload
    if [ "$purge" -eq 1 ]; then
      rm -rf "$ROOT"
      info "Removed hy2scale and ${ROOT}."
    else
      rm -rf "$BIN" "${BIN}.sig" "$UNITS_SRC" "$REF_FILE" "${ROOT}/manage.sh"
      info "Removed hy2scale. Data kept at ${ROOT}/data (delete with: rm -rf ${ROOT})."
    fi
    ;;
  *)
    sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
