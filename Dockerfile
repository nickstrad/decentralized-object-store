FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS dev

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git jq python3 python3-markdown curl procps libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*

ENV GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false \
    CGO_ENABLED=1 \
    OBJSTORE_DEV_CONTAINER=1

WORKDIR /src
CMD ["bash"]

FROM dev AS dev-observability
RUN set -eu; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) checksum=c6507206baf7793c6013dbd82368469a45f20dc0e17a6f92f204602f652ed2bc ;; \
        arm64) checksum=859e1875d2a9515fbb4bf8871061437a14af82320da68c753f094c4b776348fe ;; \
        *) echo "DuckDB CLI is unavailable for Linux architecture: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fLsS "https://github.com/duckdb/duckdb/releases/download/v2.0.0/duckdb-cli-linux-${arch}.tar.gz" -o /tmp/duckdb.tar.gz; \
    printf '%s  %s\n' "$checksum" /tmp/duckdb.tar.gz | sha256sum -c -; \
    tar -xzf /tmp/duckdb.tar.gz -C /usr/local/bin duckdb; \
    chmod 0755 /usr/local/bin/duckdb; \
    rm /tmp/duckdb.tar.gz

FROM dev AS default
