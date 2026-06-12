#!/usr/bin/env bash
# install.sh — installs prerequisites (git, PowerShell 7), creates config.json
# and .env from the examples, and adds a `psscripts` launcher to ~/.local/bin.
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APP_DIR"

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

# --- config -----------------------------------------------------------------
[ -f config.json ] || { cp config.json.example config.json; say "created config.json — set scriptsRepo and n8nWebhookUrl"; }
[ -f .env ]        || { cp .env.example .env;               say "created .env — set GITHUB_TOKEN"; }

# --- launcher ---------------------------------------------------------------
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/psscripts" <<EOF
#!/usr/bin/env bash
exec pwsh -NoProfile -File '$APP_DIR/psscripts.ps1' "\$@"
EOF
chmod +x "$HOME/.local/bin/psscripts"
say "launcher installed: ~/.local/bin/psscripts"

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) say "NOTE: ~/.local/bin is not on your PATH — add: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

say "done. Edit config.json + .env, then run: psscripts"
