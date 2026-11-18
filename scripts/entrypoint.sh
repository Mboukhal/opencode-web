#!/bin/sh
# Container entrypoint: prepare persistent dirs, seed the OpenCode CLI into the
# persistent volume, then exec the web server as PID 1.
set -eu

DATA_DIR="${DATA_DIR:-/data}"
INSTALL_DIR="${OPENCODE_INSTALL_DIR:-/data/install}"
SEED_DIR="/opt/opencode-seed"

for d in projects config state app ssh home install; do
  mkdir -p "${DATA_DIR}/$d" 2>/dev/null || true
done

# --- OpenCode CLI -----------------------------------------------------------
# The volume keeps the CLI across restarts. The image carries a pinned copy;
# OPENCODE_VERSION is compared against it so a rebuild with a new pin replaces
# the binary in place instead of on every boot.
want="${OPENCODE_VERSION:-2.3.2}"
# Version files hold "opencode v2.3.2"; compare bare numbers only.
strip() { printf %s "${1:-none}" | sed 's/^opencode v//; s/^v//'; }
have="$(strip "$(cat "$INSTALL_DIR/version" 2>/dev/null)")"
seed="$(strip "$(cat "$SEED_DIR/version" 2>/dev/null)")"
want="$(strip "$want")"

if [ ! -x "$INSTALL_DIR/opencode" ]; then
  if [ -x "$SEED_DIR/opencode" ]; then
    echo "entrypoint: seeding opencode ${seed} into ${INSTALL_DIR}"
    cp -a "$SEED_DIR"/. "$INSTALL_DIR"/
  else
    echo "entrypoint: no bundled CLI, downloading"
    OPENCODE_VERSION="$want" /usr/local/bin/install-opencode.sh "$INSTALL_DIR" || true
  fi
elif [ "$want" != "$have" ] && [ "$want" = "$seed" ]; then
  echo "entrypoint: replacing opencode ${have} with ${seed}"
  cp -a "$SEED_DIR"/. "$INSTALL_DIR"/
elif [ "$want" != "$have" ] && [ "$want" != "$seed" ]; then
  echo "entrypoint: installing requested opencode ${want}"
  OPENCODE_VERSION="$want" /usr/local/bin/install-opencode.sh "$INSTALL_DIR" || true
fi

# `opencode upgrade` and OpenCode's own launcher expect $HOME/.opencode/bin.
# The server binary is configured with OPENCODE_BIN=$INSTALL_DIR/opencode
# directly, so nothing needs root-owned /usr/local/bin.
if [ -x "$INSTALL_DIR/opencode" ]; then
  mkdir -p "$HOME/.opencode/bin"
  ln -sf "$INSTALL_DIR/opencode" "$HOME/.opencode/bin/opencode"
fi

# --- OpenCode config --------------------------------------------------------
CONFIG="${XDG_CONFIG_HOME:-/data/config}/opencode"
mkdir -p "$CONFIG"
if [ ! -f "$CONFIG/opencode.json" ]; then
  cat > "$CONFIG/opencode.json" <<JSON
{
  "\$schema": "https://opencode.ai/config.json",
  "model": "${OPENCODE_MODEL:-opencode/big-pickle}",
  "experimental": { "subagent_depth": 2 }
}
JSON
  echo "entrypoint: seeded $CONFIG/opencode.json"
fi

# --- git identity -----------------------------------------------------------
git config --global user.name  "${GIT_AUTHOR_NAME:-Mohammed Boukhal}" 2>/dev/null || true
git config --global user.email "${GIT_AUTHOR_EMAIL:-lios80466@gmail.com}" 2>/dev/null || true
git config --global init.defaultBranch main 2>/dev/null || true
git config --global --add safe.directory '*' 2>/dev/null || true

# --- ssh --------------------------------------------------------------------
KEY="${SSH_KEY_PATH:-/data/ssh/id_ed25519}"
if [ -f "$KEY" ]; then
  chmod 600 "$KEY" 2>/dev/null || true
fi
mkdir -p "$HOME/.ssh"
if ! grep -q "github.com" "$HOME/.ssh/known_hosts" 2>/dev/null; then
  timeout 20 ssh-keyscan -t ed25519,rsa github.com >> "$HOME/.ssh/known_hosts" 2>/dev/null || true
fi
chmod 600 "$HOME/.ssh/known_hosts" 2>/dev/null || true

echo "entrypoint: opencode -> $("$INSTALL_DIR/opencode" --version 2>/dev/null || echo unavailable); data=${DATA_DIR}"
exec /usr/local/bin/opencode-web
