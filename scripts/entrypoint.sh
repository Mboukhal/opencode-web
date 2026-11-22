#!/bin/sh
# Container entrypoint: prepare persistent dirs, seed the OpenCode CLI, make the
# deploy key and git identity ready, then exec the web server as PID 1.
#
# The web server owns the OpenCode background service: it starts `opencode serve`
# itself and talks to it over HTTP, which is what lets OpenCode Mobile pair with
# the same sessions the web UI runs.
set -eu

DATA_DIR="${DATA_DIR:-/data}"
INSTALL_DIR="${OPENCODE_INSTALL_DIR:-/data/install}"
SEED_DIR="/opt/opencode-seed"

for d in projects config state app ssh home install skills; do
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
if [ -x "$INSTALL_DIR/opencode" ]; then
  mkdir -p "$HOME/.opencode/bin"
  ln -sf "$INSTALL_DIR/opencode" "$HOME/.opencode/bin/opencode"
fi

# --- OpenCode config --------------------------------------------------------
# opencode.json is written by the app on boot (model, skills path), so only the
# directory has to exist. Secrets never live here: API keys come from
# PROVIDER_KEYS and are stored as credentials by the server.
mkdir -p "${XDG_CONFIG_HOME:-/data/config}/opencode"

# --- git identity -----------------------------------------------------------
git config --global user.name  "${GIT_AUTHOR_NAME:-Mohammed Boukhal}" 2>/dev/null || true
git config --global user.email "${GIT_AUTHOR_EMAIL:-lios80466@gmail.com}" 2>/dev/null || true
git config --global init.defaultBranch main 2>/dev/null || true
git config --global --add safe.directory '*' 2>/dev/null || true

# --- ssh --------------------------------------------------------------------
# Clones, pulls and pushes all go over SSH. The key is mounted read-only by
# compose; ownership is fixed on the host, so only permissions are set here.
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
