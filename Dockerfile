# syntax=docker/dockerfile:1

# --- build stage ---
# msgbrowse uses the cgo-based mattn/go-sqlite3 driver with the sqlite_fts5 build
# tag, so the build needs a C toolchain. The bookworm image provides gcc.
FROM golang:1.25-bookworm AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pre-create the data dir so the runtime stage can own it (distroless has no
# shell to mkdir/chown at runtime). A fresh named volume mounted over /data
# inherits this ownership, so the non-root user can write the SQLite DB.
RUN mkdir -p /data

ARG VERSION=docker
ARG COMMIT=none
ARG BUILD_DATE=unknown

# CGO is required for the SQLite driver. Build a binary that links against glibc
# (the distroless base below ships glibc), and strip debug info for size.
ENV CGO_ENABLED=1
RUN go build -tags sqlite_fts5 \
      -ldflags "-s -w \
        -X github.com/joestump/msgbrowse/internal/cli.Version=${VERSION} \
        -X github.com/joestump/msgbrowse/internal/cli.Commit=${COMMIT} \
        -X github.com/joestump/msgbrowse/internal/cli.BuildDate=${BUILD_DATE}" \
      -o /out/msgbrowse ./cmd/msgbrowse

# --- runtime stage ---
# distroless/base-debian12 ships glibc + CA certs and a non-root "nonroot" user
# (uid 65532). No shell, no package manager — minimal attack surface.
FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=build /out/msgbrowse /usr/local/bin/msgbrowse

# /data is the writable app-data dir, owned by the non-root user (uid 65532 in
# distroless). A fresh named volume mounted here is initialized with this
# ownership so the SQLite database can be created.
COPY --from=build --chown=65532:65532 /data /data

# Writable app data lives in /data (a named volume); the archive is mounted
# read-only at /archive by compose. Defaults point the binary at both.
ENV MSGBROWSE_DATA_DIR=/data \
    MSGBROWSE_ARCHIVE_ROOT=/archive \
    MSGBROWSE_LISTEN_ADDR=0.0.0.0:8787

# The server binds inside the container; compose maps it to host loopback only.
EXPOSE 8787

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/msgbrowse"]
CMD ["serve"]
