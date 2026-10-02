FROM alpine:3.21 AS native-module-build

ARG NGINX_VERSION=1.31.1
RUN apk add --no-cache build-base linux-headers pcre2-dev openssl-dev zlib-dev wget
WORKDIR /src
RUN wget -q https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz \
  && tar -xzf nginx-${NGINX_VERSION}.tar.gz
COPY native/ngx_http_httpv_module /src/ngx_http_httpv_module
WORKDIR /src/nginx-${NGINX_VERSION}
RUN ./configure --with-compat --add-dynamic-module=/src/ngx_http_httpv_module \
  && make modules

FROM openresty/openresty:alpine-fat
RUN mkdir -p /usr/local/openresty/nginx/modules
COPY --from=native-module-build /src/nginx-1.31.1/objs/ngx_http_httpv_module.so /usr/local/openresty/nginx/modules/
COPY openresty/nginx.conf /usr/local/openresty/nginx/conf/nginx.conf
COPY openresty/lua/ /usr/local/openresty/nginx/lua/
EXPOSE 8088
