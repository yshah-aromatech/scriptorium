#!/usr/bin/env bash
# install.sh — binary-mode by default: downloads the latest scriptorium
# release for this machine's CPU architecture, verifies its checksum, and
# installs the binary plus a bootstrapped config.json/.env. When run from a
# scriptorium source checkout (this script's own directory has sibling
# go.mod + cmd/scriptorium — i.e. `git clone ... && cd scriptorium &&
# ./install.sh`), it instead builds the binary from source in place and
# keeps that checkout tracking the scriptorium repo.
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

# --- prerequisites that apply in both modes ---------------------------------
# python3 + venv keep their old auto-install behavior (Python scripts need a
# working venv regardless of which mode installed scriptorium). PowerShell 7
# is optional-but-recommended post-cutover (it upgrades the dependency scan
# from a degraded regex fallback to the real AST scanner) — warn, don't
# auto-install: apt/dpkg root access is no longer this script's business now
# that the binary itself has no PowerShell dependency.
if ! command -v python3 >/dev/null 2>&1 || ! python3 -m venv --help >/dev/null 2>&1; then
  say "installing python3 + venv + pip..."
  sudo apt-get update -y && sudo apt-get install -y python3 python3-venv python3-pip
fi
say "python3: $(python3 --version 2>/dev/null || echo 'not installed')"

if ! command -v pwsh >/dev/null 2>&1; then
  say "NOTE: PowerShell 7 (pwsh) not found — optional but recommended, it upgrades the PowerShell-script dependency scan from a degraded regex fallback to the real AST scanner. Install: https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux"
fi

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

# A launcher that starts with '#!' is the old pwsh-wrapper script, not our
# binary — announce the switch before it gets overwritten below.
if [ -f "$LAUNCHER" ] && [ "$(head -c 2 "$LAUNCHER" 2>/dev/null)" = "#!" ]; then
  say "replacing the old launcher script at $LAUNCHER with the scriptorium binary"
fi

if [ "$CHECKOUT_MODE" = "1" ]; then
  # --- checkout mode: build from source ---------------------------------
  if ! command -v git >/dev/null 2>&1; then
    say "installing git..."
    sudo apt-get update -y && sudo apt-get install -y git
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

  cp "$DLDIR/scriptorium" "$LAUNCHER"
  EXAMPLES_DIR="$DLDIR"
fi
chmod +x "$LAUNCHER"
say "installed: $LAUNCHER"

# --- config bootstrap (never overwrites an existing file) -------------------
mkdir -p "$APP_DIR"
[ -f "$APP_DIR/config.json" ] || { cp "$EXAMPLES_DIR/config.json.example" "$APP_DIR/config.json"; say "created config.json — set scriptsRepo and n8nWebhookUrl"; }
[ -f "$APP_DIR/.env" ]        || { cp "$EXAMPLES_DIR/.env.example" "$APP_DIR/.env";               say "created .env — set GITHUB_TOKEN"; }

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) say "NOTE: ~/.local/bin is not on your PATH — add: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

say "done. Edit $APP_DIR/config.json + .env, then run: scriptorium"
