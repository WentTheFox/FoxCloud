# FoxCloud sync daemon for a headless host (homelab). Runs foxcloudd only: no
# tray, no OS keyring. Build:  docker build -t foxcloud .
# See packaging/docker/ for a compose file and an example config.

# Declared before the first FROM so the second FROM can use it.
ARG RCLONE_IMAGE=rclone/rclone:1.75.1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X github.com/WentTheFox/FoxCloud/internal/version.Version=${VERSION}" \
      -o /out/foxcloudd ./cmd/foxcloudd \
 && go build -trimpath -ldflags "-s -w -X github.com/WentTheFox/FoxCloud/internal/version.Version=${VERSION}" \
      -o /out/foxcloud ./cmd/foxcloud

# The official rclone image supplies a pinned rclone on Alpine (it also has sh,
# which key_command needs). Its entrypoint is rclone, so it is replaced below.
FROM ${RCLONE_IMAGE}
# The journal (and `ssh_host` lookups) shell out to ssh, which the rclone image does not ship.
RUN apk add --no-cache openssh-client
COPY --from=build /out/foxcloudd /out/foxcloud /usr/local/bin/
COPY --chmod=755 packaging/docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
ENV FOXCLOUD_CONFIG=/config/config.toml \
    XDG_STATE_HOME=/state \
    XDG_CONFIG_HOME=/config \
    HOME=/state
VOLUME ["/state"]
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
