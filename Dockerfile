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
RUN test "$(uname -m)" = x86_64 \
 && curl -fLsS https://github.com/duckdb/duckdb/releases/download/v1.5.0/duckdb_cli-linux-amd64.zip -o /tmp/duckdb.zip \
 && echo '179a481edf048dd1fef0f097d66933b4b5f5a5737d4034c645e415ec722c0292  /tmp/duckdb.zip' | sha256sum -c - \
 && python3 -m zipfile -e /tmp/duckdb.zip /usr/local/bin \
 && chmod 0755 /usr/local/bin/duckdb \
 && rm /tmp/duckdb.zip

FROM dev AS default
