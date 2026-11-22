# syntax=docker/dockerfile:1.7

# --- build the Go binary ---
FROM golang:1.27-bookworm AS build
WORKDIR /src

# sqlite3 is compiled with cgo; the bookworm image already has gcc.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/opencode-web .

# --- runtime ---
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/data/home \
    XDG_CONFIG_HOME=/data/config \
    XDG_DATA_HOME=/data/state \
    XDG_STATE_HOME=/data/state \
    OPENCODE_CONFIG_DIR=/data/config \
    OPENCODE_BIN=/data/install/opencode

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client ripgrep less procps \
      python3 python3-venv tzdata \
    && rm -rf /var/lib/apt/lists/*

# A real user: OpenCode and the projects should never run as root.
RUN groupadd -g 1000 app && useradd -u 1000 -g 1000 -m -d /data/home app \
    && mkdir -p /data/projects /data/config /data/state /data/app /data/ssh /data/install /data/home /data/skills

COPY scripts/install-opencode.sh /usr/local/bin/install-opencode.sh
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/install-opencode.sh /usr/local/bin/entrypoint.sh

COPY --from=build /out/opencode-web /usr/local/bin/opencode-web

# Bake the CLI into the image so boots never depend on the network.
# Pin the version: `opencode run --format json` output differs between v1 and v2,
# and the server parses the v2 shape.
ARG OPENCODE_VERSION=2.3.2
RUN OPENCODE_VERSION="${OPENCODE_VERSION}" install-opencode.sh /opt/opencode-seed || \
    echo "warning: opencode pre-install failed; the entrypoint will retry"

USER 1000:1000
WORKDIR /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
