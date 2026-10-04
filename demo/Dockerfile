FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY main.go ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /demo-upstream .

FROM alpine:3.21
COPY --from=build /demo-upstream /usr/local/bin/demo-upstream
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/demo-upstream"]
