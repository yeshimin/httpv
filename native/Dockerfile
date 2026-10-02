FROM alpine:3.21 AS build

ARG NGINX_VERSION=1.31.1
RUN apk add --no-cache build-base linux-headers pcre2-dev openssl-dev zlib-dev wget
WORKDIR /src
RUN wget -q https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz \
  && tar -xzf nginx-${NGINX_VERSION}.tar.gz
COPY ngx_http_httpv_module /src/ngx_http_httpv_module
WORKDIR /src/nginx-${NGINX_VERSION}
RUN ./configure --with-compat --add-dynamic-module=/src/ngx_http_httpv_module \
  && make modules \
  && mkdir -p /out \
  && cp objs/ngx_http_httpv_module.so /out/

FROM scratch
COPY --from=build /out/ngx_http_httpv_module.so /ngx_http_httpv_module.so
