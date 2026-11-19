# syntax=docker/dockerfile:1
# issue-timeline as one container: the web app is built with Node, embedded into the Go binary, and the binary runs
# alone on a small image. Build it from the repo root: docker compose build (or docker build -t issue-timeline .).

# 1. The web app (the same build as `make ui`).
FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
# The page imports the shared vocabulary from ../../internal/derive, so it sits next to web/ as in the repo.
COPY internal/derive/vocabulary.json /src/internal/derive/vocabulary.json
COPY web/ ./
RUN npm run build

# 2. The binary, with the web app in internal/webui/dist (what `make build` does).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/ internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/issue-timeline ./cmd/issue-timeline

# 3. The runtime: the binary, CA certificates for the hosts' HTTPS, and no shell or Node.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --home-dir /data --shell /usr/sbin/nologin issue-timeline \
 && mkdir /data && chown issue-timeline /data
COPY --from=build /out/issue-timeline /usr/local/bin/issue-timeline

# Inside the container the server listens on every interface (the port is published on the host, see
# docker-compose.yml). Off loopback every request needs a sign-in, so set the OAuth apps before exposing it.
ENV HOST=0.0.0.0 \
    PORT=4310 \
    STORE_DIR=/data \
    NO_OPEN=1

USER issue-timeline
VOLUME ["/data"]
EXPOSE 4310
ENTRYPOINT ["issue-timeline"]
