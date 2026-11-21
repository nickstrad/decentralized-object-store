FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git jq python3 python3-markdown curl procps libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*

ENV GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false \
    CGO_ENABLED=1 \
    OBJSTORE_DEV_CONTAINER=1

WORKDIR /src
CMD ["bash"]
