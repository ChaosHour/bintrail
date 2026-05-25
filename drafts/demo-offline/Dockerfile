# Demo image: bintrail binary + mysql client + bash.
#
# Used as the base image for every bintrail-related sidecar in the
# demo stack (init, stream, shim, proxysql-setup, traffic-gen). All
# of them need either the bintrail binary or the mysql client, and
# carrying one image keeps the demo's build cache hot.
#
# Multi-stage so the runtime image stays small. DuckDB's C++ libs
# are statically linked into the bintrail binary by duckdb-go, but
# libstdc++ at runtime is still required.

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=1
RUN go build -o /out/bintrail ./cmd/bintrail

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libstdc++6 \
        default-mysql-client \
        bash \
        procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/bintrail /usr/local/bin/bintrail

# No ENTRYPOINT — each compose service sets its own command so the
# same image can run bintrail, mysql, or bash as needed.
