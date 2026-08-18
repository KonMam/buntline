# Multi-stage build. The web and Go stages run on the build host's
# architecture and cross-compile, so multi-arch builds need no emulation.

FROM --platform=$BUILDPLATFORM node:22-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags embedded -ldflags "-s -w -X main.version=$VERSION" \
    -o /out/tether ./cmd/tether

# Runtime carries the tools the agent shells out to. The agent only sees
# this filesystem: mount what it should work on at /workspace.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash git ripgrep ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/tether /usr/local/bin/tether

# All state (config, secrets, sessions) lives under /data: one volume.
ENV HOME=/data \
    TETHER_DATA_DIR=/data \
    TETHER_SESSIONS_DIR=/data/sessions \
    TETHER_ADDR=0.0.0.0:7433
WORKDIR /workspace
EXPOSE 7433
ENTRYPOINT ["tether", "-no-open"]
