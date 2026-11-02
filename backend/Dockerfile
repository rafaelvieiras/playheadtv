FROM --platform=$BUILDPLATFORM node:22-alpine AS webui-build
WORKDIR /src/webui
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY webui/ .
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
# Cross-compiling on the build platform (instead of emulating the target) keeps
# the multi-arch build fast: the Go toolchain targets any GOOS/GOARCH natively.
ARG TARGETOS
ARG TARGETARCH
# Release version and source revision baked into the binary (GET /version).
# Without the build args a local build reports version "dev".
ARG VERSION=dev
ARG COMMIT=
WORKDIR /src/backend

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ .
# vite.config.ts resolves its outDir via __dirname, so the webui-build stage
# above already wrote straight into internal/webui/dist relative to its own
# /src/webui — this copies that compiled bundle over the placeholder
# index.html embed.go embeds (see internal/webui/embed.go).
COPY --from=webui-build /src/backend/internal/webui/dist ./internal/webui/dist
# CGO disabled: modernc.org/sqlite is pure Go, so the binary is fully static
# and runs unmodified on the distroless image below (which is the "cc" variant
# only for what a plugin needs).
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X github.com/rafaelvieiras/playheadtv/backend/internal/buildinfo.Version=${VERSION} \
      -X github.com/rafaelvieiras/playheadtv/backend/internal/buildinfo.Commit=${COMMIT}" \
    -o /out/server ./cmd/server

# Not distroless: a plugin is an arbitrary external program that this backend
# runs, and what it needs cannot be guessed. distroless/static has no loader
# ("fork/exec ...: no such file or directory" for a file that is right there) and
# cc-debian12 still lacked libz. A slim Debian is the smallest base where an
# extractor and a remuxer just work; the image grows by ~30 MB and the attack
# surface is the same one a plugin already has.
FROM debian:bookworm-slim
COPY --from=build /out/server /server

ENV PLAYHEADTV_DB_PATH=/data/playheadtv.db
ENV PLAYHEADTV_ADDR=:9394
VOLUME ["/data"]
EXPOSE 9394

ENTRYPOINT ["/server"]
