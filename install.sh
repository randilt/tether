#!/usr/bin/env bash
# Tether installer for Linux — build binary, check deps, load v4l2loopback.
# Never runs sudo without printing the exact command and asking first.
set -euo pipefail

REPO_URL="${TETHER_REPO_URL:-https://github.com/randilt/tether.git}"
REPO_REF="${TETHER_REPO_REF:-main}"
INSTALL_DIR="${TETHER_INSTALL_DIR:-$HOME/.local/bin}"
VIDEO_NR="${TETHER_VIDEO_NR:-10}"
CARD_LABEL="${TETHER_CARD_LABEL:-Tether}"
V4L2_DEV="/dev/video${VIDEO_NR}"

MODPROBE_CMD="modprobe v4l2loopback devices=1 video_nr=${VIDEO_NR} card_label=${CARD_LABEL} exclusive_caps=1"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

have_tty() {
  # /dev/tty can exist as a node but fail to open (containers/CI).
  if { true <>/dev/tty; } 2>/dev/null; then
    return 0
  fi
  [[ -t 0 || -t 1 ]]
}

need_linux() {
  [[ "$(uname -s)" == Linux ]] || die "Tether is Linux-only (v4l2loopback). Found: $(uname -s)"
}

confirm() {
  # usage: confirm "Prompt text"  → 0=yes 1=no (default no)
  # Prefer /dev/tty so piping the script does not steal answers.
  local prompt=$1
  local reply
  if { true <>/dev/tty; } 2>/dev/null; then
    read -r -p "${prompt} [y/N] " reply </dev/tty || return 1
  elif [[ -t 0 ]]; then
    read -r -p "${prompt} [y/N] " reply || return 1
  else
    note "(no TTY — answering no to: ${prompt})"
    return 1
  fi
  [[ "${reply}" == [yY] || "${reply}" == [yY][eE][sS] ]]
}

# Print the exact command, ask, then run. Uses sudo only when not root.
run_privileged() {
  local -a cmd=("$@")
  local display
  printf -v display '%q ' "${cmd[@]}"
  display=${display%% }

  if [[ "$(id -u)" -eq 0 ]]; then
    info "About to run as root:"
    note "${display}"
    if ! confirm "Run this command?"; then
      note "Skipped."
      return 1
    fi
    "${cmd[@]}"
    return
  fi

  if ! command -v sudo >/dev/null 2>&1; then
    info "Need root for:"
    note "${display}"
    die "sudo not found — re-run as root, or install sudo and try again"
  fi

  info "About to run (via sudo):"
  note "sudo ${display}"
  if ! confirm "Run this command with sudo?"; then
    note "Skipped."
    return 1
  fi
  sudo "${cmd[@]}"
}

detect_pkg() {
  if command -v apt-get >/dev/null 2>&1; then
    echo apt
  elif command -v dnf >/dev/null 2>&1; then
    echo dnf
  elif command -v pacman >/dev/null 2>&1; then
    echo pacman
  elif command -v zypper >/dev/null 2>&1; then
    echo zypper
  else
    echo unknown
  fi
}

print_v4l2_install_cmds() {
  local pkg
  pkg=$(detect_pkg)
  info "v4l2loopback does not look installed (module + package check failed)."
  note "Install it with your package manager, then re-run this script."
  echo
  case "${pkg}" in
    apt)
      note "Debian / Ubuntu / Mint:"
      note "  sudo apt update"
      note "  sudo apt install v4l2loopback-dkms ffmpeg"
      note "  # then: sudo ${MODPROBE_CMD}"
      ;;
    dnf)
      note "Fedora / RHEL-ish (often needs RPM Fusion free):"
      note "  sudo dnf install v4l2loopback ffmpeg"
      note "  # if missing: enable RPM Fusion, or try: sudo dnf install akmod-v4l2loopback"
      note "  # then: sudo ${MODPROBE_CMD}"
      ;;
    pacman)
      note "Arch / Manjaro:"
      note "  sudo pacman -S v4l2loopback-dkms ffmpeg linux-headers"
      note "  # (headers package must match your running kernel)"
      note "  # then: sudo ${MODPROBE_CMD}"
      ;;
    zypper)
      note "openSUSE:"
      note "  sudo zypper install v4l2loopback-kmp-default ffmpeg"
      note "  # package name may vary with kernel flavor (default/lpae/...)"
      note "  # then: sudo ${MODPROBE_CMD}"
      ;;
    *)
      note "Could not detect apt/dnf/pacman/zypper."
      note "Install packages named like: v4l2loopback-dkms (or v4l2loopback) and ffmpeg,"
      note "then load the module:"
      note "  sudo ${MODPROBE_CMD}"
      ;;
  esac
  echo
  if [[ -f /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    note "Detected: ${PRETTY_NAME:-$ID}"
  fi
}

v4l2_module_available() {
  modinfo v4l2loopback >/dev/null 2>&1
}

v4l2_module_loaded() {
  lsmod 2>/dev/null | awk '{print $1}' | grep -qx v4l2loopback
}

v4l2_package_installed() {
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${Status}' v4l2loopback-dkms 2>/dev/null | grep -q 'install ok installed' \
      && return 0
    dpkg-query -W -f='${Status}' v4l2loopback 2>/dev/null | grep -q 'install ok installed' \
      && return 0
  fi
  if command -v rpm >/dev/null 2>&1; then
    rpm -q v4l2loopback >/dev/null 2>&1 && return 0
    rpm -q akmod-v4l2loopback >/dev/null 2>&1 && return 0
    rpm -q v4l2loopback-kmp-default >/dev/null 2>&1 && return 0
  fi
  if command -v pacman >/dev/null 2>&1; then
    pacman -Qi v4l2loopback-dkms >/dev/null 2>&1 && return 0
    pacman -Qi v4l2loopback-dkms-git >/dev/null 2>&1 && return 0
  fi
  return 1
}

ffmpeg_ok() {
  command -v ffmpeg >/dev/null 2>&1
}

print_ffmpeg_hint() {
  local pkg
  pkg=$(detect_pkg)
  info "ffmpeg not found on PATH (required to feed the virtual camera / audio)."
  case "${pkg}" in
    apt)    note "  sudo apt install ffmpeg" ;;
    dnf)    note "  sudo dnf install ffmpeg" ;;
    pacman) note "  sudo pacman -S ffmpeg" ;;
    zypper) note "  sudo zypper install ffmpeg" ;;
    *)      note "  Install ffmpeg with your distro package manager." ;;
  esac
}

ensure_go() {
  if command -v go >/dev/null 2>&1; then
    note "go: $(go version)"
    return 0
  fi
  info "Go toolchain not found."
  note "Install Go 1.24+ from https://go.dev/dl/ (or your distro), then re-run."
  case "$(detect_pkg)" in
    apt)    note "  # example: sudo apt install golang-go   (may be older than 1.24)" ;;
    dnf)    note "  # example: sudo dnf install golang" ;;
    pacman) note "  # example: sudo pacman -S go" ;;
  esac
  die "Go is required to build Tether"
}

resolve_source_dir() {
  local here self
  self="${BASH_SOURCE[0]-}"
  if [[ -n "${self}" && -f "${self}" ]]; then
    here=$(cd "$(dirname "${self}")" && pwd)
    if [[ -f "${here}/go.mod" && -f "${here}/main.go" ]]; then
      printf '%s\n' "${here}"
      return
    fi
  fi
  if [[ -f ./go.mod && -f ./main.go ]]; then
    pwd
    return
  fi
  return 1
}

clone_source() {
  local dest
  dest="${TMPDIR:-/tmp}/tether-src-$$"
  info "Cloning ${REPO_URL} (${REPO_REF}) → ${dest}"
  command -v git >/dev/null 2>&1 || die "git is required to download the source"
  git clone --depth 1 --branch "${REPO_REF}" "${REPO_URL}" "${dest}"
  printf '%s\n' "${dest}"
}

build_binary() {
  local src=$1
  local out=$2
  info "Building Tether from ${src}"
  (
    cd "${src}"
    # GOTOOLCHAIN=auto lets Go 1.21+ fetch the go.mod toolchain if needed
    GOTOOLCHAIN="${GOTOOLCHAIN:-auto}" go build -o "${out}" .
  )
  note "Built: ${out}"
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

offer_modprobe() {
  if [[ -e "${V4L2_DEV}" ]] && v4l2_module_loaded; then
    info "Virtual camera already present: ${V4L2_DEV} (module loaded)."
    return 0
  fi

  if ! v4l2_module_available; then
    print_v4l2_install_cmds
    return 1
  fi

  info "Ready to create a virtual webcam device."
  note "This loads the v4l2loopback kernel module with:"
  note "  devices=1          → one virtual camera"
  note "  video_nr=${VIDEO_NR}        → node ${V4L2_DEV}"
  note "  card_label=${CARD_LABEL}   → name shown in OBS/Zoom/etc."
  note "  exclusive_caps=1   → apps treat it as a capture device"
  note "Exact command:"
  note "  sudo ${MODPROBE_CMD}"
  echo

  if ! run_privileged modprobe v4l2loopback \
      devices=1 \
      "video_nr=${VIDEO_NR}" \
      "card_label=${CARD_LABEL}" \
      exclusive_caps=1; then
    note "Module not loaded. You can run the command above later."
    return 1
  fi

  if [[ -e "${V4L2_DEV}" ]]; then
    info "OK — ${V4L2_DEV} exists."
  else
    info "modprobe succeeded, but ${V4L2_DEV} is missing."
    note "Check: ls -l /dev/video*"
    note "Another video_nr may be in use; retry with TETHER_VIDEO_NR=N ./install.sh"
    return 1
  fi
}

offer_persist() {
  v4l2_module_loaded || return 0
  [[ -e /etc/modules-load.d/v4l2loopback.conf ]] && \
    [[ -e /etc/modprobe.d/v4l2loopback.conf ]] && {
      info "Persistence configs already present under /etc."
      return 0
    }

  info "Optional: load the virtual camera automatically on boot."
  note "Would write these files (via sudo):"
  note "  /etc/modules-load.d/v4l2loopback.conf  →  v4l2loopback"
  note "  /etc/modprobe.d/v4l2loopback.conf       →  options … video_nr=${VIDEO_NR} …"
  if ! confirm "Create persistence configs?"; then
    note "Skipped persistence."
    return 0
  fi

  local modules_tmp options_tmp
  modules_tmp=$(mktemp)
  options_tmp=$(mktemp)
  printf 'v4l2loopback\n' >"${modules_tmp}"
  printf 'options v4l2loopback devices=1 video_nr=%s card_label=%s exclusive_caps=1\n' \
    "${VIDEO_NR}" "${CARD_LABEL}" >"${options_tmp}"

  # One confirmation already done above — run_privileged would ask again.
  # Print exact commands, then invoke sudo (or run as root).
  info "Running:"
  note "sudo install -m 644 ${modules_tmp} /etc/modules-load.d/v4l2loopback.conf"
  note "sudo install -m 644 ${options_tmp} /etc/modprobe.d/v4l2loopback.conf"
  if [[ "$(id -u)" -eq 0 ]]; then
    install -m 644 "${modules_tmp}" /etc/modules-load.d/v4l2loopback.conf
    install -m 644 "${options_tmp}" /etc/modprobe.d/v4l2loopback.conf
  else
    command -v sudo >/dev/null 2>&1 || die "sudo not found"
    sudo install -m 644 "${modules_tmp}" /etc/modules-load.d/v4l2loopback.conf
    sudo install -m 644 "${options_tmp}" /etc/modprobe.d/v4l2loopback.conf
  fi
  rm -f "${modules_tmp}" "${options_tmp}"
  info "Persistence installed."
}

print_next_steps() {
  local bin="${INSTALL_DIR}/tether"
  echo
  info "Done. Next:"
  note "1. Start the server:"
  note "     ${bin}"
  note "   (or: tether   if ${INSTALL_DIR} is on PATH)"
  note "2. On this PC open:  https://127.0.0.1:8444/control"
  note "3. On your phone: scan the terminal QR / open the printed pairing URL"
  note "4. Trust the cert once on the phone (the phone page walks you through it)"
  if [[ ! -e "${V4L2_DEV}" ]]; then
    note "Virtual cam ${V4L2_DEV} is not up yet — Tether still runs; control UI shows the modprobe fix."
  else
    note "Virtual cam: ${V4L2_DEV}  (pass -v4l2 ${V4L2_DEV} if you change the number)"
  fi
}

main() {
  need_linux
  info "Tether install"
  note "Install dir: ${INSTALL_DIR}"
  note "v4l2 device: ${V4L2_DEV}"
  echo

  if ! ffmpeg_ok; then
    print_ffmpeg_hint
    if have_tty; then
      if ! confirm "Continue building without ffmpeg for now?"; then
        die "Install ffmpeg, then re-run ./install.sh"
      fi
      note "Continuing — install ffmpeg before running Tether."
    else
      note "Non-interactive session: continuing build; install ffmpeg before running Tether."
    fi
  else
    note "ffmpeg: $(command -v ffmpeg)"
  fi

  ensure_go

  local src built cleanup=""
  if src=$(resolve_source_dir); then
    info "Using existing source tree: ${src}"
  else
    src=$(clone_source)
    cleanup=${src}
  fi

  built=$(mktemp "${TMPDIR:-/tmp}/tether-build.XXXXXX")
  # go build wants a path without requiring +x on the temp name as dir — use file path
  rm -f "${built}"
  build_binary "${src}" "${built}"
  install_binary "${built}"
  rm -f "${built}"
  if [[ -n "${cleanup}" ]]; then
    rm -rf "${cleanup}"
  fi

  echo
  if v4l2_package_installed || v4l2_module_available; then
    if v4l2_module_available; then
      note "v4l2loopback module: available"
    fi
    if v4l2_package_installed; then
      note "v4l2loopback package: installed"
    fi
    offer_modprobe || true
    offer_persist || true
  else
    print_v4l2_install_cmds
    note "After installing the package, re-run: ./install.sh"
    note "Or load manually: sudo ${MODPROBE_CMD}"
  fi

  print_next_steps
}

main "$@"
