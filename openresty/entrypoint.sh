#!/bin/sh
set -eu

: "${HTTPV_UPSTREAM_URL:=http://demo:9000}"
: "${HTTPV_UPSTREAM_HOST:=demo:9000}"
export HTTPV_UPSTREAM_URL HTTPV_UPSTREAM_HOST

envsubst '${HTTPV_UPSTREAM_URL} ${HTTPV_UPSTREAM_HOST}' \
  < /usr/local/openresty/nginx/conf/nginx.conf.template \
  > /tmp/httpv-nginx.conf

exec /usr/local/openresty/bin/openresty -c /tmp/httpv-nginx.conf -g 'daemon off;'
