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
  --bin-dst PATH          Runtime binary destination inside the target system
  --config-dst PATH       Runtime config path
  --state-path PATH       Runtime state path
  --plist-dst PATH        launchd plist destination
  --stdout-log PATH       launchd stdout log path
  --stderr-log PATH       launchd stderr log path
  --label NAME            launchd label
  --hostd-cmd PATH        Command used to run 'hostd service install-launchd'
  -h, --help              Show this help
EOF
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

DRY_RUN="0"
INSTALL_ROOT="${HOSTD_INSTALL_ROOT:-}"
BIN_SRC="./hostd"
BIN_DST="${HOSTD_BIN_DST:-$HOME/Library/Application Support/Sunvisai/hostd/bin/hostd}"
CONFIG_DST="${HOSTD_CONFIG_DST:-$HOME/Library/Application Support/hostd/config.json}"
STATE_PATH="${HOSTD_STATE_PATH:-$HOME/Library/Application Support/hostd/state.json}"
PLIST_DST="${HOSTD_PLIST_DST:-$HOME/Library/LaunchAgents/ai.sunvisai.hostd.plist}"
STDOUT_LOG="${HOSTD_STDOUT_LOG:-$HOME/Library/Logs/hostd/stdout.log}"
STDERR_LOG="${HOSTD_STDERR_LOG:-$HOME/Library/Logs/hostd/stderr.log}"
LABEL="${HOSTD_LAUNCHD_LABEL:-ai.sunvisai.hostd}"
HOSTD_CMD="${HOSTD_CMD:-}"

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
    --config-dst)
      CONFIG_DST="${2:?missing value for --config-dst}"
      shift 2
      ;;
    --state-path)
      STATE_PATH="${2:?missing value for --state-path}"
      shift 2
      ;;
    --plist-dst)
      PLIST_DST="${2:?missing value for --plist-dst}"
      shift 2
      ;;
    --stdout-log)
      STDOUT_LOG="${2:?missing value for --stdout-log}"
      shift 2
      ;;
    --stderr-log)
      STDERR_LOG="${2:?missing value for --stderr-log}"
      shift 2
      ;;
    --label)
      LABEL="${2:?missing value for --label}"
      shift 2
      ;;
    --hostd-cmd)
      HOSTD_CMD="${2:?missing value for --hostd-cmd}"
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

BIN_INSTALL_DST="$(apply_root "${BIN_DST}")"
CONFIG_INSTALL_DST="$(apply_root "${CONFIG_DST}")"

run_cmd install -Dm0755 "${BIN_SRC}" "${BIN_INSTALL_DST}"
if [[ ! -f "${CONFIG_INSTALL_DST}" ]]; then
  run_cmd install -Dm0644 "${SCRIPT_DIR}/config.example.json" "${CONFIG_INSTALL_DST}"
fi

if [[ -z "${HOSTD_CMD}" ]]; then
  HOSTD_CMD="${BIN_INSTALL_DST}"
fi

run_cmd "${HOSTD_CMD}" service install-launchd \
  --bin "${BIN_DST}" \
  --config "${CONFIG_DST}" \
  --state "${STATE_PATH}" \
  --label "${LABEL}" \
  --plist "${PLIST_DST}" \
  --stdout-log "${STDOUT_LOG}" \
  --stderr-log "${STDERR_LOG}"
