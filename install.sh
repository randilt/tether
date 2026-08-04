#!/usr/bin/env bash
# Tether installer — build the Go binary into ~/.local/bin.
set -euo pipefail

REPO_URL="${TETHER_REPO_URL:-https://github.com/randilt/tether.git}"
REPO_REF="${TETHER_REPO_REF:-main}"
INSTALL_DIR="${TETHER_INSTALL_DIR:-$HOME/.local/bin}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

ensure_go() {
  if command -v go >/dev/null 2>&1; then
    note "go: $(go version)"
    return 0
  fi
  die "Go not found — install Go 1.24+ from https://go.dev/dl/ then re-run"
}

resolve_source_dir() {
  local here
  here=$(cd "$(dirname "$0")" && pwd)
  if [[ -f "${here}/go.mod" ]] && grep -q 'module github.com/randilt/tether' "${here}/go.mod" 2>/dev/null; then
    printf '%s\n' "${here}"
    return 0
  fi
  return 1
}

clone_source() {
  local tmp
  command -v git >/dev/null 2>&1 || die "git not found"
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/tether-src.XXXXXX")
  info "Cloning ${REPO_URL} (${REPO_REF}) → ${tmp}"
  git clone --depth 1 --branch "${REPO_REF}" "${REPO_URL}" "${tmp}"
  printf '%s\n' "${tmp}"
}

build_binary() {
  local src=$1
  local out=$2
  info "Building tether…"
  (cd "${src}" && CGO_ENABLED=0 go build -ldflags '-s -w' -o "${out}" .)
}

install_binary() {
  local built=$1
  mkdir -p "${INSTALL_DIR}"
  local dest="${INSTALL_DIR}/tether"
  info "Installing binary → ${dest}"
  install -m 755 "${built}" "${dest}"
  note "Installed: ${dest}"

  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      info "Note: ${INSTALL_DIR} is not on your PATH."
      note "Add this to your shell rc, then open a new terminal:"
      note "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac
}

print_next_steps() {
  local bin="${INSTALL_DIR}/tether"
  echo
  info "Done. Next:"
  note "1. Start:  ${bin}"
  note "2. PC:     https://127.0.0.1:8444/control"
  note "3. Phone:  scan the terminal QR / open the printed pairing URL"
  note "4. OBS:    Browser Source → Copy OBS URL from a device → Start Virtual Camera for Zoom"
}

main() {
  info "Tether install"
  note "Install dir: ${INSTALL_DIR}"
  echo

  ensure_go

  local src built cleanup=""
  if src=$(resolve_source_dir); then
    info "Using existing source tree: ${src}"
  else
    src=$(clone_source)
    cleanup=${src}
  fi

  built=$(mktemp "${TMPDIR:-/tmp}/tether-build.XXXXXX")
  rm -f "${built}"
  build_binary "${src}" "${built}"
  install_binary "${built}"
  rm -f "${built}"
  if [[ -n "${cleanup}" ]]; then
    rm -rf "${cleanup}"
  fi

  print_next_steps
}

main "$@"
