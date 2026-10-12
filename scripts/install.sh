#!/usr/bin/env bash
# Install dial.
#   curl -fsSL https://raw.githubusercontent.com/medpar/dial/main/scripts/install.sh | bash
# Environment:
#   DIAL_VERSION   release tag without the v (default: latest)
#   DIAL_BIN_DIR   where the `dial` command goes (default: ~/.local/bin)
#   DIAL_FROM      install from a local dist/ directory instead of GitHub
#   DIAL_NO_CRON   on Linux, skip the keepalive cron entry
set -euo pipefail

REPO="medpar/dial"
VERSION="${DIAL_VERSION:-latest}"
BIN_DIR="${DIAL_BIN_DIR:-$HOME/.local/bin}"
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fetch() { # asset -> path
  local asset="$1" out="$tmp/$1"
  if [ -n "${DIAL_FROM:-}" ]; then
    cp -R "$DIAL_FROM/$asset" "$out"
  else
    local url="https://github.com/$REPO/releases/latest/download/$asset"
    [ "$VERSION" = latest ] || url="https://github.com/$REPO/releases/download/v$VERSION/$asset"
    curl -fsSL "$url" -o "$out"
  fi
  echo "$out"
}

mkdir -p "$BIN_DIR"

if [ "$OS" = Darwin ]; then
  dest=/Applications
  [ -w "$dest" ] || dest="$HOME/Applications"
  mkdir -p "$dest"
  if [ -n "${DIAL_FROM:-}" ] && [ -d "$DIAL_FROM/Dial.app" ]; then
    src="$DIAL_FROM/Dial.app"
  else
    zip="$(fetch Dial-macos.zip)"
    ditto -x -k "$zip" "$tmp/app"
    src="$tmp/app/Dial.app"
  fi
  pkill -f "Dial.app/Contents/MacOS/Dial" 2>/dev/null || true
  rm -rf "$dest/Dial.app"
  ditto "$src" "$dest/Dial.app"
  xattr -dr com.apple.quarantine "$dest/Dial.app" 2>/dev/null || true
  ln -sf "$dest/Dial.app/Contents/Helpers/dial" "$BIN_DIR/dial"
  open -a "$dest/Dial.app"
  echo "✓ Dial.app installed in $dest (menu bar), dial → $BIN_DIR/dial"
else
  bin="$(fetch "dial-linux-$ARCH")"
  install -m 755 "$bin" "$BIN_DIR/dial"
  echo "✓ dial installed to $BIN_DIR/dial"
  if [ -z "${DIAL_NO_CRON:-}" ] && command -v crontab >/dev/null 2>&1; then
    line="17 */6 * * * $BIN_DIR/dial keepalive >/dev/null 2>&1"
    if ! crontab -l 2>/dev/null | grep -q "dial keepalive"; then
      { crontab -l 2>/dev/null || true; echo "$line"; } | crontab -
      echo "✓ keepalive cron added (refreshes idle logins every 6h)"
    fi
  fi
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "! add $BIN_DIR to your PATH" ;;
esac
echo "Next: dial import   # bring in the accounts you're logged into"
