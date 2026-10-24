# SPDX-License-Identifier: MIT
# Multi-stage build for fxp_core, go_gateway and fix_bridge.
#   docker build -t fxp .
# One runtime image holds all three binaries; docker-compose.yml picks the
# command per service.

ARG PROTOC_VERSION=25.3

FROM debian:bookworm-slim AS protoc
ARG PROTOC_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH:-amd64}" in \
        amd64) arch=x86_64 ;; \
        arm64) arch=aarch_64 ;; \
        *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSLo /tmp/protoc.zip \
        "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${arch}.zip" \
    && unzip -q /tmp/protoc.zip -d /opt/protoc \
    && rm /tmp/protoc.zip

FROM rust:1-bookworm AS core-build
COPY --from=protoc /opt/protoc /opt/protoc
ENV PATH="/opt/protoc/bin:${PATH}"
WORKDIR /src/fxp_core
COPY fxp_core/ ./
RUN cargo build --release --locked --bin fxp_core

FROM golang:1.26-bookworm AS go-build
COPY --from=protoc /opt/protoc /opt/protoc
ENV PATH="/opt/protoc/bin:/go/bin:${PATH}" CGO_ENABLED=0
WORKDIR /src
COPY go.work ./
COPY go_gateway/go.mod go_gateway/go.sum go_gateway/
COPY fix_bridge/go.mod fix_bridge/go.sum fix_bridge/
RUN cd go_gateway && go mod download && cd ../fix_bridge && go mod download
COPY fxp_core/proto/ fxp_core/proto/
COPY scripts/gen_proto.sh scripts/
COPY go_gateway/ go_gateway/
COPY fix_bridge/ fix_bridge/
RUN bash scripts/gen_proto.sh \
    && mkdir -p /out \
    && (cd go_gateway && go build -trimpath -o /out/go_gateway .) \
    && (cd fix_bridge && go build -trimpath -o /out/fix_bridge .)

FROM debian:bookworm-slim AS runtime
RUN useradd --system --uid 10001 --home-dir /data --create-home fxp \
    && mkdir -p /data/fix_store && chown fxp:fxp /data/fix_store
COPY --from=core-build /src/fxp_core/target/release/fxp_core /usr/local/bin/fxp_core
COPY --from=go-build /out/go_gateway /usr/local/bin/go_gateway
COPY --from=go-build /out/fix_bridge /usr/local/bin/fix_bridge
USER fxp
WORKDIR /data
ENV FXP_FIX_STORE_DIR=/data/fix_store
EXPOSE 8081 8082 8083
CMD ["fxp_core"]
