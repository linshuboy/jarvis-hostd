#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
usage() {
  cat <<'EOF'
Usage: install.sh [options] [bin-src]

Options:
  --dry-run               Print actions without writing files
  --root PATH             Prefix install destinations with PATH for staging/no-root tests
  --bin-src PATH          Source hostd binary path
  --bin-dst PATH          Runtime binary path inside the target system
  --unit-dst PATH         systemd unit file destination
  --sysusers-dst PATH     sysusers config destination
  --tmpfiles-dst PATH     tmpfiles config destination
  --config-dst PATH       Runtime config path and default config install destination
  --state-path PATH       Runtime state path used by the service unit
  --working-dir PATH      WorkingDirectory for the service unit
  -h, --help              Show this help
EOF
}

escape_sed_replacement() {
  local value="${1}"
  value="${value//\\/\\\\}"
  value="${value//&/\\&}"
  value="${value//|/\\|}"
  printf '%s' "${value}"
}

apply_root() {
  local path="${1}"
  if [[ -z "${INSTALL_ROOT}" ]]; then
    printf '%s' "${path}"
    return 0
  fi
  if [[ "${path}" == /* ]]; then
    printf '%s%s' "${INSTALL_ROOT%/}" "${path}"
    return 0
  fi
  printf '%s/%s' "${INSTALL_ROOT%/}" "${path}"
}

run_cmd() {
  if [[ "${DRY_RUN}" == "1" ]]; then
    printf '+'
    for arg in "$@"; do
      printf ' %q' "${arg}"
    done
    printf '\n'
    return 0
  fi
  "$@"
}

install_template() {
  local template_src="${1}"
  local dst_path="${2}"
  local mode="${3}"
  if [[ "${DRY_RUN}" == "1" ]]; then
    printf '+ render %q -> %q (mode %s)\n' "${template_src}" "${dst_path}" "${mode}"
    return 0
  fi
  local rendered
  rendered="$(mktemp)"
  sed \
    -e "s|__HOSTD_BIN_PATH__|$(escape_sed_replacement "${BIN_DST}")|g" \
    -e "s|__HOSTD_CONFIG_PATH__|$(escape_sed_replacement "${CONFIG_DST}")|g" \
    -e "s|__HOSTD_STATE_PATH__|$(escape_sed_replacement "${STATE_PATH}")|g" \
    -e "s|__HOSTD_WORKING_DIR__|$(escape_sed_replacement "${WORKING_DIR}")|g" \
    -e "s|__HOSTD_CONFIG_DIR__|$(escape_sed_replacement "${CONFIG_DIR}")|g" \
    -e "s|__HOSTD_STATE_DIR__|$(escape_sed_replacement "${STATE_DIR}")|g" \
    "${template_src}" > "${rendered}"
  install -Dm"${mode}" "${rendered}" "${dst_path}"
  rm -f "${rendered}"
}

DRY_RUN="0"
INSTALL_ROOT="${HOSTD_INSTALL_ROOT:-}"
BIN_SRC="./hostd"
BIN_DST="${HOSTD_BIN_DST:-/usr/local/bin/hostd}"
UNIT_DST="${HOSTD_UNIT_DST:-/etc/systemd/system/hostd.service}"
SYSUSERS_DST="${HOSTD_SYSUSERS_DST:-/usr/lib/sysusers.d/hostd.conf}"
TMPFILES_DST="${HOSTD_TMPFILES_DST:-/usr/lib/tmpfiles.d/hostd.conf}"
CONFIG_DST="${HOSTD_CONFIG_DST:-/etc/hostd/config.json}"
STATE_PATH="${HOSTD_STATE_PATH:-/var/lib/hostd/state.json}"
WORKING_DIR="${HOSTD_WORKING_DIR:-}"

while [[ $# -gt 0 ]]; do
  case "${1}" in
    --dry-run)
      DRY_RUN="1"
      shift
      ;;
    --root)
      INSTALL_ROOT="${2:?missing value for --root}"
      shift 2
      ;;
    --bin-src)
      BIN_SRC="${2:?missing value for --bin-src}"
      shift 2
      ;;
    --bin-dst)
      BIN_DST="${2:?missing value for --bin-dst}"
      shift 2
      ;;
    --unit-dst)
      UNIT_DST="${2:?missing value for --unit-dst}"
      shift 2
      ;;
    --sysusers-dst)
      SYSUSERS_DST="${2:?missing value for --sysusers-dst}"
      shift 2
      ;;
    --tmpfiles-dst)
      TMPFILES_DST="${2:?missing value for --tmpfiles-dst}"
      shift 2
      ;;
    --config-dst)
      CONFIG_DST="${2:?missing value for --config-dst}"
      shift 2
      ;;
    --state-path)
      STATE_PATH="${2:?missing value for --state-path}"
      shift 2
      ;;
    --working-dir)
      WORKING_DIR="${2:?missing value for --working-dir}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      printf 'unsupported option: %s\n' "${1}" >&2
      usage >&2
      exit 1
      ;;
    *)
      if [[ "${BIN_SRC}" != "./hostd" ]]; then
        printf 'unexpected positional argument: %s\n' "${1}" >&2
        usage >&2
        exit 1
      fi
      BIN_SRC="${1}"
      shift
      ;;
  esac
done

if [[ $# -gt 0 ]]; then
  printf 'unexpected positional arguments: %s\n' "$*" >&2
  usage >&2
  exit 1
fi

if [[ -z "${WORKING_DIR}" ]]; then
  WORKING_DIR="$(dirname "${STATE_PATH}")"
fi
CONFIG_DIR="$(dirname "${CONFIG_DST}")"
STATE_DIR="$(dirname "${STATE_PATH}")"
SERVICE_NAME="$(basename "${UNIT_DST}")"

BIN_INSTALL_DST="$(apply_root "${BIN_DST}")"
UNIT_INSTALL_DST="$(apply_root "${UNIT_DST}")"
SYSUSERS_INSTALL_DST="$(apply_root "${SYSUSERS_DST}")"
TMPFILES_INSTALL_DST="$(apply_root "${TMPFILES_DST}")"
CONFIG_INSTALL_DST="$(apply_root "${CONFIG_DST}")"

run_cmd install -Dm0755 "${BIN_SRC}" "${BIN_INSTALL_DST}"
install_template "${SCRIPT_DIR}/hostd.service" "${UNIT_INSTALL_DST}" "0644"
run_cmd install -Dm0644 "${SCRIPT_DIR}/hostd.sysusers.conf" "${SYSUSERS_INSTALL_DST}"
install_template "${SCRIPT_DIR}/hostd.tmpfiles.conf" "${TMPFILES_INSTALL_DST}" "0644"

run_cmd systemd-sysusers "${SYSUSERS_INSTALL_DST}"
run_cmd systemd-tmpfiles --create "${TMPFILES_INSTALL_DST}"

if [[ ! -f "${CONFIG_INSTALL_DST}" ]]; then
  run_cmd install -Dm0644 "${SCRIPT_DIR}/config.example.json" "${CONFIG_INSTALL_DST}"
fi

run_cmd systemctl daemon-reload
run_cmd systemctl enable --now "${SERVICE_NAME}"
