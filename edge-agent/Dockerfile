FROM rust:1-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    cargo build --release \
    && cp /src/target/release/httpv-edge-agent /httpv-edge-agent

FROM alpine:3.21
COPY --from=build /httpv-edge-agent /usr/local/bin/httpv-edge-agent
EXPOSE 9101/udp 9102
ENTRYPOINT ["/usr/local/bin/httpv-edge-agent"]
