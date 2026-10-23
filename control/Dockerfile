FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /httpv-control .

FROM alpine:3.21
COPY --from=build /httpv-control /usr/local/bin/httpv-control
EXPOSE 8090 9100/udp
ENTRYPOINT ["/usr/local/bin/httpv-control"]
