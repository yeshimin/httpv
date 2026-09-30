FROM rust:1-alpine AS build
WORKDIR /src
COPY . .
RUN cargo build --release

FROM alpine:3.21
COPY --from=build /src/target/release/httpv-edge-agent /usr/local/bin/httpv-edge-agent
EXPOSE 9101/udp 9102
ENTRYPOINT ["/usr/local/bin/httpv-edge-agent"]
