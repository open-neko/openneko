#!/usr/bin/env bash
# Install the three external CLIs Neko's worker shells out to:
#   - graphjin  (always required; the metric agent runs `graphjin cli`)
#   - claude    (only for the Claude Agent backend; the SDK spawns it)
#   - hermes    (only for the Hermes backend; same — spawned, not bundled)
#
# Idempotent. Pass --skip-claude or --skip-hermes if you only need one
# backend. macOS uses Homebrew where possible; Debian/Ubuntu uses apt +
# direct installers. Other distros: read the body and adapt.
#
# Usage:
#   ./scripts/install-clis.sh                # install all three
#   ./scripts/install-clis.sh --skip-hermes  # graphjin + claude only
set -euo pipefail

GRAPHJIN_VERSION="${GRAPHJIN_VERSION:-3.18.10}"

SKIP_GRAPHJIN=false
SKIP_CLAUDE=false
SKIP_HERMES=false
for arg in "$@"; do
  case "$arg" in
    --skip-graphjin) SKIP_GRAPHJIN=true ;;
    --skip-claude)   SKIP_CLAUDE=true ;;
    --skip-hermes)   SKIP_HERMES=true ;;
    -h|--help)
      sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

uname_s=$(uname -s)
case "$uname_s" in
  Darwin) os=macos ;;
  Linux)  os=linux ;;
  *) echo "unsupported OS: $uname_s" >&2; exit 1 ;;
esac

# ─── graphjin ──────────────────────────────────────────────────────────
if ! $SKIP_GRAPHJIN && ! have graphjin; then
  log "installing graphjin v${GRAPHJIN_VERSION}"
  if [ "$os" = macos ] && have brew; then
    brew install dosco/tap/graphjin
  else
    arch=$(uname -m)
    case "$arch" in
      x86_64|amd64) gj_arch=amd64 ;;
      aarch64|arm64) gj_arch=arm64 ;;
      *) echo "unsupported arch for graphjin: $arch" >&2; exit 1 ;;
    esac
    suffix="${os}_${gj_arch}"
    [ "$os" = macos ] && suffix="darwin_${gj_arch}"
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/gj.tgz" \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_${suffix}.tar.gz"
    sudo tar -xzf "$tmp/gj.tgz" -C /usr/local/bin graphjin
    rm -rf "$tmp"
  fi
  graphjin version
fi

# ─── claude (Claude Code CLI) ──────────────────────────────────────────
if ! $SKIP_CLAUDE && ! have claude; then
  if ! have npm; then
    echo "claude install requires npm. Install Node 18+ first (https://nodejs.org)." >&2
    exit 1
  fi
  log "installing @anthropic-ai/claude-code globally via npm"
  if [ -w "$(npm root -g 2>/dev/null || echo /)" ]; then
    npm install -g @anthropic-ai/claude-code
  else
    sudo npm install -g @anthropic-ai/claude-code
  fi
fi

# ─── hermes (Nous Research) ────────────────────────────────────────────
# Hermes is a Python tool installed via uv. Needs Python 3.11+ + git.
# We install uv first if missing, then run their installer with --skip-setup
# so the interactive wizard doesn't block scripted installs.
if ! $SKIP_HERMES && ! have hermes; then
  log "installing hermes (Nous Research)"
  if [ "$os" = linux ] && have apt-get; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq python3 python3-venv python3-dev libffi-dev git build-essential ripgrep ffmpeg
  fi
  if ! have uv; then
    log "installing uv"
    if [ "$os" = macos ] && have brew; then
      brew install uv
    else
      curl -LsSf https://astral.sh/uv/install.sh | sudo env UV_INSTALL_DIR=/usr/local/bin sh -s -- --no-modify-path
    fi
  fi
  curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh \
    | bash -s -- --skip-setup
fi

log "done. installed:"
$SKIP_GRAPHJIN || { printf '  graphjin: '; have graphjin && graphjin version | head -1 || echo 'NOT FOUND'; }
$SKIP_CLAUDE   || { printf '  claude:   '; have claude   && claude --version 2>/dev/null || echo '(installed; --version may differ)'; }
$SKIP_HERMES   || { printf '  hermes:   '; have hermes   && hermes --version 2>/dev/null || echo '(installed; --version may differ)'; }
