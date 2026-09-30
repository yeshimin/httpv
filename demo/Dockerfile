FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /demo-upstream .

FROM alpine:3.21
COPY --from=build /demo-upstream /usr/local/bin/demo-upstream
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/demo-upstream"]
