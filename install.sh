#!/usr/bin/env bash
# install.sh — binary-mode by default: downloads the latest scriptorium
# release for this machine's CPU architecture, verifies its checksum, and
# installs the binary plus a bootstrapped config.json/.env. When run from a
# scriptorium source checkout (this script's own directory has sibling
# go.mod + cmd/scriptorium — i.e. `git clone ... && cd scriptorium &&
# ./install.sh`), it instead builds the binary from source in place and
# keeps that checkout tracking the scriptorium repo.
#
# Re-running the same one-liner on an existing install is the self-update
# path: it fetches the latest release, verifies it, replaces the binary and
# prints `updated scriptorium vOLD → vNEW`.
#
# Works two ways:
#   curl -fsSL https://raw.githubusercontent.com/yshah-aromatech/scriptorium/main/install.sh | bash
#   git clone https://github.com/yshah-aromatech/scriptorium.git && cd scriptorium && ./install.sh
#
# Set SCRIPTORIUM_APP_DIR to control where config.json/.env/scripts live
# (default: ~/scriptorium).
set -euo pipefail

RELEASE_BASE="https://github.com/yshah-aromatech/scriptorium/releases/latest/download"

say() { printf '\033[38;2;130;170;255m==>\033[0m %s\n' "$*"; }
die() { printf '\033[38;2;255;100;100m==>\033[0m %s\n' "$*" >&2; exit 1; }

# --- prerequisites (v1.1.0, owner directive — reverses the v1.0 ruling) -----
# On apt systems, missing prerequisites are INSTALLED, not warned about:
# PowerShell 7 via the Microsoft repo (it upgrades the dependency scan from
# the regex fallback to the real AST scanner) and python3 + pip + venv (the
# venv is verified by actually running it — Debian ships python3 without a
# working venv module). Privilege ladder: root runs apt directly; otherwise
# `sudo -n` when a credential is cached; otherwise prompt for sudo ONCE on a
# real terminal; with no sudo at all each package becomes a WARN carrying the
# exact manual command. One failed prerequisite never aborts the install.

APT_READY="" # root | sudo | none
apt_probe() {
  [ -n "$APT_READY" ] && return 0
  if [ "$(id -u)" = "0" ]; then
    APT_READY=root; return 0
  fi
  if command -v sudo >/dev/null 2>&1; then
    if sudo -n true 2>/dev/null; then
      APT_READY=sudo; return 0
    fi
    if [ -t 1 ] && [ -e /dev/tty ]; then
      say "administrator access is needed to install missing prerequisites — asking for sudo once"
      if sudo -v </dev/tty; then
        APT_READY=sudo; return 0
      fi
    fi
  fi
  APT_READY=none
}

as_root() {
  case "$APT_READY" in
    root) "$@" ;;
    sudo) sudo "$@" ;;
    *) return 1 ;;
  esac
}

install_powershell() {
  # the PS-era §13.1 recipe: Microsoft's repo package for this release, then
  # the powershell package from it. On non-LTS Ubuntu the per-version MS repo
  # can set up fine yet not carry a powershell package at all (v1.1.1: e.g.
  # 25.04) — fall back to snap before giving up.
  VERSION_ID=""
  [ -r /etc/os-release ] && . /etc/os-release
  PMP_DIR="$(mktemp -d)"
  PMP_DEB="$PMP_DIR/packages-microsoft-prod.deb"
  say "installing PowerShell 7 via the Microsoft apt repo..."
  if curl -fsSL -o "$PMP_DEB" "https://packages.microsoft.com/config/ubuntu/${VERSION_ID}/packages-microsoft-prod.deb" &&
    as_root dpkg -i "$PMP_DEB" >/dev/null &&
    as_root apt-get update -y >/dev/null &&
    as_root apt-get install -y powershell; then
    say "PowerShell 7 installed"
  elif command -v snap >/dev/null 2>&1; then
    say "PowerShell 7 not available via the Microsoft apt repo — trying snap instead..."
    if as_root snap install powershell --classic; then
      say "PowerShell 7 installed via snap"
    else
      say "WARN: PowerShell 7 install failed — install it later with: sudo snap install powershell --classic (or via the Microsoft repo: https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux)"
    fi
  else
    say "WARN: PowerShell 7 install failed — install it later with: sudo apt-get install -y snapd && sudo snap install powershell --classic (or via the Microsoft repo: https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux)"
  fi
  rm -rf "$PMP_DIR"
}

install_python() {
  say "installing python3 + venv + pip..."
  if as_root apt-get update -y >/dev/null && as_root apt-get install -y python3 python3-venv python3-pip; then
    say "python3 + venv + pip installed"
  else
    say "WARN: python3 install failed — install it later with: sudo apt-get install -y python3 python3-venv python3-pip"
  fi
}

MISSING_PWSH=0
MISSING_PY=0
command -v pwsh >/dev/null 2>&1 || MISSING_PWSH=1
# the REAL venv check: `command -v python3` passes on Debian systems whose
# venv module is a stub that only prints "install python3-venv"
if ! command -v python3 >/dev/null 2>&1 || ! python3 -m venv --help >/dev/null 2>&1; then
  MISSING_PY=1
fi

if [ "$MISSING_PWSH" = 1 ] || [ "$MISSING_PY" = 1 ]; then
  if command -v apt-get >/dev/null 2>&1; then
    apt_probe
    if [ "$APT_READY" = "none" ]; then
      [ "$MISSING_PWSH" = 1 ] && say "WARN: PowerShell 7 (pwsh) is missing and sudo is unavailable — install it with: sudo apt-get install -y snapd && sudo snap install powershell --classic (or via the Microsoft repo: https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux)"
      [ "$MISSING_PY" = 1 ] && say "WARN: python3/venv/pip are missing and sudo is unavailable — install them with: sudo apt-get install -y python3 python3-venv python3-pip"
    else
      [ "$MISSING_PWSH" = 1 ] && install_powershell
      [ "$MISSING_PY" = 1 ] && install_python
    fi
  else
    # non-apt systems: hints, unchanged
    [ "$MISSING_PWSH" = 1 ] && say "NOTE: PowerShell 7 (pwsh) not found — install: https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux"
    [ "$MISSING_PY" = 1 ] && say "NOTE: python3 with a working venv not found — install python3, python3-venv and python3-pip with your package manager"
  fi
fi
say "python3: $(python3 --version 2>/dev/null || echo 'not installed')"

# --- app dir + mode detection ------------------------------------------------
APP_DIR="${SCRIPTORIUM_APP_DIR:-$HOME/scriptorium}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/dev/null}")" 2>/dev/null && pwd || true)"
CHECKOUT_MODE=0
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ] && [ -d "$SCRIPT_DIR/cmd/scriptorium" ]; then
  CHECKOUT_MODE=1
  APP_DIR="$SCRIPT_DIR"
fi

mkdir -p "$HOME/.local/bin"
LAUNCHER="$HOME/.local/bin/scriptorium"

# What is being replaced, for the update line printed at the end: the version
# of an existing BINARY install, empty for a fresh one. A launcher that starts
# with '#!' is the old pwsh-wrapper script, not our binary — announce the
# switch instead of asking it for a version.
OLD_VERSION=""
if [ -f "$LAUNCHER" ] && [ "$(head -c 2 "$LAUNCHER" 2>/dev/null)" = "#!" ]; then
  say "replacing the old launcher script at $LAUNCHER with the scriptorium binary"
elif [ -x "$LAUNCHER" ]; then
  OLD_VERSION="$("$LAUNCHER" --version 2>/dev/null | awk '{print $2}' || true)"
fi

if [ "$CHECKOUT_MODE" = "1" ]; then
  # --- checkout mode: build from source ---------------------------------
  if ! command -v git >/dev/null 2>&1; then
    die "checkout install needs git — install it and re-run"
  fi

  # Repo tracking/self-update runs every invocation, even on an
  # already-installed checkout — never destructive: local work always
  # survives a re-run, so a failed fast-forward is just left as is.
  if [ -d "$APP_DIR/.git" ]; then
    (
      cd "$APP_DIR"
      say "updating from scriptorium..."
      git fetch origin
      if ! git pull --ff-only origin main 2>/dev/null; then
        say "NOTE: could not fast-forward (local changes or commits?) — left as is"
      fi
    )
  fi

  if ! command -v go >/dev/null 2>&1; then
    die "checkout install needs a Go toolchain (https://go.dev/dl/) — install Go and re-run"
  fi
  say "building scriptorium from source..."
  ( cd "$APP_DIR" && go build -o "$LAUNCHER" ./cmd/scriptorium )
  EXAMPLES_DIR="$APP_DIR"
else
  # --- binary mode: fetch the latest release -----------------------------
  case "$(uname -m)" in
    x86_64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture: $(uname -m) — scriptorium ships linux amd64/arm64 releases only" ;;
  esac

  ASSET="scriptorium_linux_${ARCH}.tar.gz"
  DLDIR="$(mktemp -d)"
  trap 'rm -rf "$DLDIR"' EXIT

  say "downloading $ASSET..."
  curl -fsSL -o "$DLDIR/$ASSET" "$RELEASE_BASE/$ASSET"
  curl -fsSL -o "$DLDIR/checksums.txt" "$RELEASE_BASE/checksums.txt"

  say "verifying checksum..."
  if ! grep -q " $ASSET\$" "$DLDIR/checksums.txt"; then
    die "no checksum entry for $ASSET in checksums.txt"
  fi
  ( cd "$DLDIR"
    grep " $ASSET\$" checksums.txt > "$ASSET.sha256"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c "$ASSET.sha256" >/dev/null
    else
      shasum -a 256 -c "$ASSET.sha256" >/dev/null
    fi
  ) || die "checksum mismatch for $ASSET — refusing to install"

  say "extracting..."
  tar -xzf "$DLDIR/$ASSET" -C "$DLDIR"

  # atomic replace: cp-in-place onto a running binary fails with "Text file
  # busy" (e.g. the scriptorium-mcp systemd service still executing it) —
  # stage in the same directory (same filesystem) and rename over the
  # target instead. A rename just swaps the directory entry; a process
  # already executing the old inode keeps running against it untouched.
  cp "$DLDIR/scriptorium" "$LAUNCHER.new"
  trap 'rm -f "$LAUNCHER.new"; rm -rf "$DLDIR"' EXIT
  chmod +x "$LAUNCHER.new"
  mv -f "$LAUNCHER.new" "$LAUNCHER"
  EXAMPLES_DIR="$DLDIR"
fi
chmod +x "$LAUNCHER"

# --- the self-update line ---------------------------------------------------
NEW_VERSION="$("$LAUNCHER" --version 2>/dev/null | awk '{print $2}' || true)"
if [ -z "$OLD_VERSION" ]; then
  say "installed scriptorium ${NEW_VERSION:-(version unknown)} at $LAUNCHER"
elif [ "$OLD_VERSION" = "$NEW_VERSION" ]; then
  say "scriptorium $NEW_VERSION is already current — binary refreshed at $LAUNCHER"
else
  say "updated scriptorium $OLD_VERSION → ${NEW_VERSION:-(version unknown)} at $LAUNCHER"
fi

# --- restart hint: the rename above lets a running scriptorium-mcp service
# finish out its old inode, but it won't pick up the new binary on its own --
if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet scriptorium-mcp 2>/dev/null; then
    say "scriptorium-mcp is running the old binary — restart to apply: systemctl restart scriptorium-mcp"
  elif [ "$(id -u)" != "0" ] && systemctl --user is-active --quiet scriptorium-mcp 2>/dev/null; then
    say "scriptorium-mcp is running the old binary — restart to apply: systemctl --user restart scriptorium-mcp"
  fi
fi

# --- config bootstrap (never overwrites an existing file) -------------------
mkdir -p "$APP_DIR"
[ -f "$APP_DIR/config.json" ] || { cp "$EXAMPLES_DIR/config.json.example" "$APP_DIR/config.json"; say "created config.json — set scriptsRepo and n8nWebhookUrl"; }
[ -f "$APP_DIR/.env" ]        || { cp "$EXAMPLES_DIR/.env.example" "$APP_DIR/.env";               say "created .env — set GITHUB_TOKEN"; }

# --- PATH persistence (v1.1.0): make the install usable in the NEXT shell ---
# ~/.local/bin joins PATH via the user's shell rc, marker-guarded so three
# installs append one line, not three. $SHELL picks the rc; a missing rc is
# created by the append; an unwritable one degrades to the old plain warning.
PATH_MARKER="# added by scriptorium install.sh"
case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *)
    case "${SHELL:-}" in
      *zsh*) RC_FILE="$HOME/.zshrc" ;;
      *)     RC_FILE="$HOME/.bashrc" ;;
    esac
    if [ -f "$RC_FILE" ] && grep -qF "$PATH_MARKER" "$RC_FILE" 2>/dev/null; then
      say "PATH: ~/.local/bin already configured in $RC_FILE — open a new shell (or source it) to pick it up"
    elif { printf '\n%s\nexport PATH="$HOME/.local/bin:$PATH"\n' "$PATH_MARKER" >> "$RC_FILE"; } 2>/dev/null; then
      say "PATH: added ~/.local/bin to $RC_FILE — open a new shell or run: source $RC_FILE"
    else
      say "NOTE: ~/.local/bin is not on your PATH — add: export PATH=\"\$HOME/.local/bin:\$PATH\""
    fi
    ;;
esac

say "done. Edit $APP_DIR/config.json + .env, then run: scriptorium"
