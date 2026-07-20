#!/usr/bin/env bash
# install.sh — installs prerequisites (git, PowerShell 7), clones/updates the
# app if needed, creates config.json and .env from the examples, and adds a
# `scriptorium` launcher to ~/.local/bin.
#
# Works two ways:
#   curl -fsSL https://raw.githubusercontent.com/yshah-aromatech/scriptorium/main/install.sh | bash
#   git clone ... && cd scriptorium && ./install.sh
#
# Set SCRIPTORIUM_APP_DIR to control where the one-liner clones the app
# (default: ~/scriptorium).
set -euo pipefail

REPO_URL="https://github.com/yshah-aromatech/scriptorium.git"

say() { printf '\033[38;2;130;170;255m==>\033[0m %s\n' "$*"; }

# --- prerequisites ----------------------------------------------------------
if ! command -v git >/dev/null 2>&1; then
  say "installing git..."
  sudo apt-get update -y && sudo apt-get install -y git
fi

if ! command -v pwsh >/dev/null 2>&1; then
  say "installing PowerShell 7 (Microsoft apt repo)..."
  source /etc/os-release
  curl -fsSL -o /tmp/packages-microsoft-prod.deb \
    "https://packages.microsoft.com/config/ubuntu/${VERSION_ID}/packages-microsoft-prod.deb"
  sudo dpkg -i /tmp/packages-microsoft-prod.deb
  rm -f /tmp/packages-microsoft-prod.deb
  sudo apt-get update -y
  sudo apt-get install -y powershell
fi
say "pwsh: $(pwsh --version)"

# python runtime for python scripts (venvs need python3-venv on Ubuntu)
if ! command -v python3 >/dev/null 2>&1 || ! python3 -m venv --help >/dev/null 2>&1; then
  say "installing python3 + venv + pip..."
  sudo apt-get update -y && sudo apt-get install -y python3 python3-venv python3-pip
fi
say "python3: $(python3 --version 2>/dev/null || echo 'not installed')"

# --- locate or fetch the app ------------------------------------------------
# When run from a checkout, install in place. When piped (curl | bash) there is
# no source file on disk, so clone (or update) the app first.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/dev/null}")" 2>/dev/null && pwd || true)"
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/scriptorium.ps1" ]; then
  APP_DIR="$SCRIPT_DIR"
else
  # PSSCRIPTS_APP_DIR + ~/powershell-scripts-tui are the pre-rename fallbacks:
  # an old install found there is converted to scriptorium in place below
  APP_DIR="${SCRIPTORIUM_APP_DIR:-${PSSCRIPTS_APP_DIR:-$HOME/scriptorium}}"
  if [ ! -d "$APP_DIR/.git" ] && [ -d "$HOME/powershell-scripts-tui/.git" ]; then
    APP_DIR="$HOME/powershell-scripts-tui"
    say "found pre-rename install at $APP_DIR — converting it to scriptorium"
  fi
  if [ ! -d "$APP_DIR/.git" ]; then
    say "cloning app to $APP_DIR..."
    git clone "$REPO_URL" "$APP_DIR"
  fi
fi
cd "$APP_DIR"

# --- track + update from the scriptorium repo ---------------------------------
# Older installs pulled from the pre-rename powershell-scripts-tui repo: force
# origin to scriptorium, and only when converting such an install allow a hard
# reset to its main (their histories don't fast-forward). A repo that already
# tracks scriptorium is never reset, so local work in a checkout stays intact.
if [ -d .git ]; then
  OLD_URL="$(git remote get-url origin 2>/dev/null || true)"
  if [ "$OLD_URL" != "$REPO_URL" ]; then
    say "repointing origin -> $REPO_URL"
    git remote set-url origin "$REPO_URL"
  fi
  say "updating from scriptorium..."
  git fetch origin
  if ! git pull --ff-only origin main 2>/dev/null; then
    if [ "$OLD_URL" != "$REPO_URL" ]; then
      say "old install history diverged — resetting to scriptorium main"
      git reset --hard origin/main
    else
      say "NOTE: could not fast-forward (local changes or commits?) — left as is"
    fi
  fi
fi

# --- config -----------------------------------------------------------------
[ -f config.json ] || { cp config.json.example config.json; say "created config.json — set scriptsRepo and n8nWebhookUrl"; }
[ -f .env ]        || { cp .env.example .env;               say "created .env — set GITHUB_TOKEN"; }

# --- launcher ---------------------------------------------------------------
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/scriptorium" <<EOF
#!/usr/bin/env bash
exec pwsh -NoProfile -File '$APP_DIR/scriptorium.ps1' "\$@"
EOF
chmod +x "$HOME/.local/bin/scriptorium"
say "launcher installed: ~/.local/bin/scriptorium"

# the pre-rename 'psscripts' launcher is retired — remove it if present
if [ -e "$HOME/.local/bin/psscripts" ] || [ -L "$HOME/.local/bin/psscripts" ]; then
  rm -f "$HOME/.local/bin/psscripts"
  say "removed legacy 'psscripts' launcher"
fi

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) say "NOTE: ~/.local/bin is not on your PATH — add: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

say "done. Edit $APP_DIR/config.json + .env, then run: scriptorium"
