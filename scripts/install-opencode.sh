#!/bin/sh
# Install the OpenCode CLI into $1 (default /data/install).
#
# OpenCode v2 has its own installer (https://opencode.ai/v2/install); it writes
# to $HOME/.opencode/bin and accepts
# `--version`, so we point HOME at a scratch dir, add a stable symlink, and
# record the installed version for the entrypoint's freshness check.
set -eu

DEST="${1:-/data/install}"
VERSION="${OPENCODE_VERSION:-2.3.2}"

mkdir -p "$DEST"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

echo "installing opencode (${VERSION}) into ${DEST}"

args="--no-modify-path"
if [ "$VERSION" != "latest" ]; then
  args="$args --version $VERSION"
fi

# `latest` must not silently fall back to a v1 build: the app relies on the v2
# JSON output of `opencode run`. Pin an explicit version in production.
HOME="$scratch" sh -c "curl -fsSL https://opencode.ai/v2/install | bash -s -- $args"

src="$scratch/.opencode/bin/opencode"
if [ ! -x "$src" ]; then
  echo "opencode binary not found after install" >&2
  exit 1
fi

cp "$src" "$DEST/opencode.new"
chmod 755 "$DEST/opencode.new"
mv "$DEST/opencode.new" "$DEST/opencode"
ln -sf "$DEST/opencode" /usr/local/bin/opencode 2>/dev/null || true

"$DEST/opencode" --version > "$DEST/version" 2>/dev/null || echo unknown > "$DEST/version"
echo "opencode ready: $(cat "$DEST/version")"
